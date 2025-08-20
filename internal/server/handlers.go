package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cloudreve-afdianpay/internal/afdian"
	"cloudreve-afdianpay/internal/signature"

	"github.com/gin-gonic/gin"
)

type Server struct {
	Svc *afdian.Service
}

func NewServer(svc *afdian.Service) *Server { return &Server{Svc: svc} }

var currencyUnit = map[string]int64{
	"USD": 100, "EUR": 100, "GBP": 100, "JPY": 1, "CNY": 100, "HKD": 100, "SGD": 100, "KRW": 1, "INR": 100, "RUB": 100, "BRL": 100, "AUD": 100, "CAD": 100, "CHF": 100,
}

func (s *Server) Order(c *gin.Context) {
	// 删除 SITE_URL 尾部的 /
	site := os.Getenv("SITE_URL")
	if strings.HasSuffix(site, "/") {
		_ = os.Setenv("SITE_URL", strings.TrimRight(site, "/"))
	}
	log.Printf("[Order] %s %s?%s X-Cr-Site-Url=%q SITE_URL=%q", c.Request.Method, c.FullPath(), c.Request.URL.RawQuery, c.GetHeader("X-Cr-Site-Url"), os.Getenv("SITE_URL"))
	// 校验 X-Cr-Site-Url
	reqSite := c.GetHeader("X-Cr-Site-Url")
	if reqSite != os.Getenv("SITE_URL") {
		log.Printf("[Order] site header mismatch: got=%q want=%q", reqSite, os.Getenv("SITE_URL"))
		c.JSON(200, gin.H{"code": 412, "error": "验证失败，请检查.env文件"})
		return
	}

	var signatureStr, timestamp string
	if c.Request.Method == http.MethodPost {
		// 读取并缓存请求体，供签名校验读取相同内容
		bodyBytes, _ := io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		c.Request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil }

		auth := c.GetHeader("Authorization")
		log.Printf("[Order] Authorization len=%d", len(auth))
		if !strings.HasPrefix(auth, "Bearer Cr ") {
			c.JSON(200, gin.H{"code": 412, "error": "无效的Authorization头格式"})
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(auth, "Bearer Cr "), ":", 2)
		if len(parts) != 2 {
			c.JSON(200, gin.H{"code": 412, "error": "无效的签名格式"})
			return
		}
		signatureStr, timestamp = parts[0], parts[1]
		log.Printf("[Order] POST signature len=%d ts=%s bodyLen=%d", len(signatureStr), timestamp, len(bodyBytes))
	} else {
		signParam := c.Query("sign")
		if signParam == "" {
			c.JSON(200, gin.H{"code": 412, "error": "未获取到签名信息"})
			return
		}
		s := signParam
		log.Printf("[Order] GET raw sign=%q", s)
		// Python 使用 urllib.parse.unquote
		if u, err := urlDecode(s); err == nil {
			s = u
		}
		log.Printf("[Order] GET decoded sign=%q", s)
		parts := strings.SplitN(s, ":", 2)
		if len(parts) != 2 {
			c.JSON(200, gin.H{"code": 412, "error": "URL中无效的签名格式"})
			return
		}
		signatureStr, timestamp = parts[0], parts[1]
		log.Printf("[Order] GET signature len=%d ts=%s", len(signatureStr), timestamp)
	}
	if ok, msg := signature.Verify(c.Request, signatureStr, timestamp); !ok {
		log.Printf("[Order] signature verify failed: %s", msg)
		c.JSON(200, gin.H{"code": 412, "error": msg})
		return
	}
	log.Printf("[Order] signature verify ok")

	if c.Request.Method == http.MethodPost {
		s.createOrder(c)
		return
	}
	s.checkOrder(c)
}

func (s *Server) createOrder(c *gin.Context) {
	var body struct {
		OrderNo   string `json:"order_no"`
		Amount    int64  `json:"amount"`
		Currency  string `json:"currency"`
		NotifyURL string `json:"notify_url"`
	}
	b, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "error": "请求体读取失败"})
		return
	}
	if err := json.Unmarshal(b, &body); err != nil {
		c.JSON(200, gin.H{"code": 400, "error": "请求体格式错误"})
		return
	}

	amountFen := body.Amount
	currency := body.Currency
	if strings.ToUpper(currency) != "CNY" {
		unit, ok := currencyUnit[strings.ToUpper(currency)]
		if !ok {
			c.JSON(200, gin.H{"code": 417, "error": "不支持的货币"})
			return
		}
		cnFen, err := convertToCNY(amountFen, unit, strings.ToUpper(currency))
		if err != nil {
			c.JSON(200, gin.H{"code": 502, "error": "汇率转换失败"})
			return
		}
		amountFen = cnFen
	}

	if amountFen < 500 {
		c.JSON(200, gin.H{"code": 417, "error": "CNY金额需要大于等于5元"})
		return
	}

	orderInfo := map[string]interface{}{
		"order_no":   body.OrderNo,
		"amount":     amountFen,
		"currency":   "CNY",
		"notify_url": body.NotifyURL,
	}
	orderInfoJSON, _ := json.Marshal(orderInfo)
	orderURL, err := s.Svc.NewOrder(string(orderInfoJSON), amountFen)
	if err != nil {
		c.JSON(200, gin.H{"code": 500, "error": "创建订单失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": orderURL})
}

func (s *Server) checkOrder(c *gin.Context) {
	orderNo := c.Query("order_no")
	if orderNo == "" {
		c.JSON(200, gin.H{"code": 400, "error": "缺少 order_no"})
		return
	}
	log.Printf("[checkOrder] order_no=%s", orderNo)
	paid, err := s.Svc.GetOrderStatus(orderNo)
	if err != nil {
		log.Printf("[checkOrder] GetOrderStatus error: %v", err)
		c.JSON(200, gin.H{"code": 500, "error": "Failed to query order status."})
		return
	}
	log.Printf("[checkOrder] status=%v", paid)
	if paid {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": "PAID"})
	} else {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": "UNPAID"})
	}
}

func (s *Server) AfdianCallback(c *gin.Context) {
	// 解析返回的 json 值
	var payload struct {
		Data struct {
			Order map[string]interface{} `json:"order"`
		} `json:"data"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		// 与 Python 行为一致，若解析失败则仍返回成功（避免回调方重试风暴）
		log.Printf("[AfdianCallback] bind JSON error: %v", err)
		c.Data(http.StatusOK, "application/json", []byte(`{"ec":200,"em":""}`))
		return
	}

	order := payload.Data.Order
	outTradeNo, _ := asString(order["out_trade_no"])
	orderNo, _ := asString(order["remark"])
	afdAmountStr := fmt.Sprintf("%v", order["total_amount"])
	log.Printf("[AfdianCallback] out_trade_no=%s order_no=%s total_amount=%s", outTradeNo, orderNo, afdAmountStr)

	// 查询订单
	_, amountStr, notifyURL, ok, err := s.Svc.CheckOrder(orderNo, outTradeNo)
	if err != nil {
		log.Printf("[AfdianCallback] CheckOrder error: %v", err)
	}
	if ok && amountStr != "" && afdAmountStr == amountStr {
		_ = s.Svc.MarkOrderPaid(orderNo)
		// 通知网站
		url := notifyURL
		for attempt := 0; attempt < 3; attempt++ {
			resp, err := http.Get(url)
			if err == nil && resp.StatusCode == 200 {
				var r struct {
					Code int `json:"code"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&r)
				resp.Body.Close()
				if r.Code == 0 {
					log.Printf("[AfdianCallback] notify ok")
					break
				}
			}
			log.Printf("[AfdianCallback] notify retry #%d", attempt+1)
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
	} else {
		log.Printf("[AfdianCallback] order not matched ok=%v dbAmount=%q", ok, amountStr)
	}

	c.Data(http.StatusOK, "application/json", []byte(`{"ec":200,"em":""}`))
}

func convertToCNY(amountFen int64, unit int64, from string) (int64, error) {
	// 将最小单位转换为该货币的基础单位数量
	baseAmount := float64(amountFen) / float64(unit)
	// 拉取汇率
	url := fmt.Sprintf("https://api.exchangerate.host/convert?from=%s&to=CNY&amount=%f", from, baseAmount)
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var payload struct {
		Result float64 `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	// 转换后为 CNY 元，转为分
	return int64(payload.Result*100 + 0.5), nil
}

func urlDecode(s string) (string, error) {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			a := fromHex(s[i+1])
			b := fromHex(s[i+2])
			if a >= 0 && b >= 0 {
				buf.WriteByte(byte(a<<4 | b))
				i += 2
				continue
			}
		}
		if s[i] == '+' {
			buf.WriteByte(' ')
			continue
		}
		buf.WriteByte(s[i])
	}
	return buf.String(), nil
}

func fromHex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

func asString(v interface{}) (string, bool) {
	s, ok := v.(string)
	if ok {
		return s, true
	}
	if v == nil {
		return "", false
	}
	return fmt.Sprintf("%v", v), true
}
