package afdian

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Service struct {
	DBPath string
}

func NewService(dbPath string) *Service {
	return &Service{DBPath: dbPath}
}

func (s *Service) open() (*sql.DB, error) {
	abs, _ := filepath.Abs(s.DBPath)
	// 使用 WAL 与 busy_timeout，减少并发访问时的锁冲突
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&cache=shared", abs)
	log.Printf("[DB] open dsn=%s", dsn)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 推荐单连接，避免数据库锁
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func ensureTableExists(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS afdian_pay (
		order_no TEXT,
		amount TEXT,
		notify_url TEXT,
		is_paid BOOLEAN DEFAULT 0
	)`)
	if err != nil {
		log.Printf("[DB] ensure table error: %v", err)
	}
	return err
}

func (s *Service) EnsureDB() error {
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	return ensureTableExists(db)
}

func (s *Service) dbInsert(orderNo string, amountFen int64, notifyURL string) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureTableExists(db); err != nil {
		return err
	}
	amountStr := fmt.Sprintf("%.2f", float64(amountFen)/100.0)
	_, err = db.Exec("INSERT INTO afdian_pay (order_no, amount, notify_url, is_paid) VALUES (?,?,?,0)", orderNo, amountStr, notifyURL)
	return err
}

// NewOrder 生成爱发电下单 URL，并写入本地 DB
func (s *Service) NewOrder(orderInfoJSON string, amountFen int64) (string, error) {
	userID := os.Getenv("USER_ID")
	if userID == "" {
		return "", errors.New("USER_ID 未设置")
	}
	var oi struct {
		OrderNo   string `json:"order_no"`
		NotifyURL string `json:"notify_url"`
	}
	if err := json.Unmarshal([]byte(orderInfoJSON), &oi); err != nil {
		return "", err
	}
	orderURL := fmt.Sprintf("https://afdian.com/order/create?user_id=%s&remark=%s&custom_price=%s", userID, url.QueryEscape(oi.OrderNo), fmt.Sprintf("%.2f", float64(amountFen)/100.0))
	if err := s.dbInsert(oi.OrderNo, amountFen, oi.NotifyURL); err != nil {
		return "", err
	}
	return orderURL, nil
}

// CheckOrder 先通过 API 主动验证，再查本地订单
func (s *Service) CheckOrder(orderNo, outTradeNo string) (string, string, string, bool, error) {
	apiOrderNo, apiTotalAmount, ok, err := s.apiCheck(outTradeNo)
	if err != nil || !ok || apiOrderNo == "" || apiTotalAmount == 0 {
		if err != nil {
			log.Printf("[CheckOrder] apiCheck error: %v", err)
		} else {
			log.Printf("[CheckOrder] apiCheck not ok: ok=%v orderNo=%q total=%d", ok, apiOrderNo, apiTotalAmount)
		}
		return "", "", "", false, err
	}

	db, err := s.open()
	if err != nil {
		log.Printf("[DB] open error: %v", err)
		return "", "", "", false, err
	}
	defer db.Close()
	if err := ensureTableExists(db); err != nil {
		return "", "", "", false, err
	}
	row := db.QueryRow("SELECT order_no, amount, notify_url FROM afdian_pay WHERE order_no = ?", orderNo)
	var ono, amountStr, notify string
	if err := row.Scan(&ono, &amountStr, &notify); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[CheckOrder] no local order: %s", orderNo)
			return "", "", "", false, nil
		}
		log.Printf("[CheckOrder] scan error: %v", err)
		return "", "", "", false, err
	}
	log.Printf("[CheckOrder] local order matched: order_no=%s amount=%s notify=%s", ono, amountStr, notify)
	return ono, amountStr, notify, true, nil
}

func (s *Service) MarkOrderPaid(orderNo string) error {
	db, err := s.open()
	if err != nil {
		log.Printf("[DB] open error: %v", err)
		return err
	}
	defer db.Close()
	if err := ensureTableExists(db); err != nil {
		return err
	}
	_, err = db.Exec("UPDATE afdian_pay SET is_paid = 1 WHERE order_no = ?", orderNo)
	if err != nil {
		log.Printf("[DB] mark paid error: %v", err)
	}
	return err
}

func (s *Service) GetOrderStatus(orderNo string) (bool, error) {
	db, err := s.open()
	if err != nil {
		log.Printf("[DB] open error: %v", err)
		return false, err
	}
	defer db.Close()
	if err := ensureTableExists(db); err != nil {
		return false, err
	}
	row := db.QueryRow("SELECT is_paid FROM afdian_pay WHERE order_no = ?", orderNo)
	var paidRaw interface{}
	if err := row.Scan(&paidRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[GetOrderStatus] no such order: %s", orderNo)
			return false, nil
		}
		log.Printf("[GetOrderStatus] scan error: %v", err)
		return false, err
	}
	paid := false
	switch v := paidRaw.(type) {
	case int64:
		paid = v != 0
	case bool:
		paid = v
	case string:
		lv := strings.ToLower(strings.TrimSpace(v))
		if lv == "true" || lv == "1" {
			paid = true
		}
	case []byte:
		lv := strings.ToLower(strings.TrimSpace(string(v)))
		if lv == "true" || lv == "1" {
			paid = true
		}
	default:
		// 尝试格式化解析
		s := fmt.Sprintf("%v", v)
		if n, err := strconv.Atoi(s); err == nil {
			paid = n != 0
		} else if strings.EqualFold(s, "true") {
			paid = true
		}
	}
	log.Printf("[GetOrderStatus] order=%s paid=%v (raw=%T:%v)", orderNo, paid, paidRaw, paidRaw)
	return paid, nil
}

// apiCheck 调用爱发电 API 查询订单
func (s *Service) apiCheck(outTradeNo string) (string, int, bool, error) {
	urlStr := "https://afdian.com/api/open/query-order"
	userID := os.Getenv("USER_ID")
	token := os.Getenv("TOKEN")
	if userID == "" || token == "" {
		return "", 0, false, errors.New("USER_ID/TOKEN 未设置")
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	params := fmt.Sprintf("{\"out_trade_no\":\"%s\"}", outTradeNo)
	signData := token + "params" + params + "ts" + ts + "user_id" + userID
	h := md5.Sum([]byte(signData))
	sign := hex.EncodeToString(h[:])

	form := url.Values{}
	form.Set("user_id", userID)
	form.Set("params", params)
	form.Set("ts", ts)
	form.Set("sign", sign)
	log.Printf("[apiCheck] request out_trade_no=%s ts=%s", outTradeNo, ts)

	resp, err := http.PostForm(urlStr, form)
	if err != nil {
		log.Printf("[apiCheck] http error: %v", err)
		return "", 0, false, err
	}
	defer resp.Body.Close()
	var payload struct {
		Data struct {
			TotalCount int `json:"total_count"`
			List       []struct {
				TotalAmount interface{} `json:"total_amount"`
				Remark      string      `json:"remark"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Printf("[apiCheck] decode error: %v", err)
		return "", 0, false, err
	}
	log.Printf("[apiCheck] total_count=%d list_len=%d", payload.Data.TotalCount, len(payload.Data.List))
	if payload.Data.TotalCount == 0 || len(payload.Data.List) == 0 {
		return "", 0, false, nil
	}
	it := payload.Data.List[0]
	// Python: int(str(total_amount).split(".")[0])
	totalStr := fmt.Sprintf("%v", it.TotalAmount)
	if idx := strings.IndexByte(totalStr, '.'); idx >= 0 {
		totalStr = totalStr[:idx]
	}
	var totalInt int
	for i := 0; i < len(totalStr); i++ {
		c := totalStr[i]
		if c < '0' || c > '9' {
			break
		}
		totalInt = totalInt*10 + int(c-'0')
	}
	log.Printf("[apiCheck] remark=%s total=%d", it.Remark, totalInt)
	return it.Remark, totalInt, true, nil
}
