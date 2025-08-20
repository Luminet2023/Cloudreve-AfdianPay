package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Verify 与 Python 版一致的签名验证
func Verify(r *http.Request, signature string, timestamp string) (bool, string) {
	communicationKey := os.Getenv("COMMUNICATION_KEY")
	if communicationKey == "" {
		return false, "服务端配置错误"
	}

	// 时间戳校验（与 Python 等价：当前时间大于时间戳则失败）
	now := time.Now().Unix()
	ts, err := parseInt64(timestamp)
	if err != nil {
		log.Printf("[Sign] invalid timestamp: %q", timestamp)
		return false, "无效的时间戳"
	}
	if now > ts {
		log.Printf("[Sign] timestamp expired: now=%d ts=%d", now, ts)
		return false, "时间戳验证失败"
	}

	var signContent string
	if r.Method == http.MethodPost {
		// 收集以 X-Cr- 开头的请求头
		var signedHeaders []string
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-Cr-") && len(v) > 0 {
				signedHeaders = append(signedHeaders, k+"="+v[0])
			}
		}
		sort.Strings(signedHeaders)
		signedHeadersStr := strings.Join(signedHeaders, "&")

		path := r.URL.Path
		if path == "" {
			path = "/"
		}

		bodyBytes, _ := readBodyWithoutConsume(r)
		body := string(bodyBytes)

		type signPayload struct {
			Path   string `json:"Path"`
			Header string `json:"Header"`
			Body   string `json:"Body"`
		}
		payload := signPayload{Path: path, Header: signedHeadersStr, Body: body}
		b, _ := json.Marshal(payload)
		s := string(b)
		s = strings.ReplaceAll(s, "&", "\\u0026")
		signContent = s
		log.Printf("[Sign] POST path=%s header=%q bodyLen=%d", path, signedHeadersStr, len(body))
	} else {
		path := r.URL.Path
		if path == "" {
			path = "/"
		}
		signContent = path
		log.Printf("[Sign] GET path=%s", path)
	}

	signContentFinal := signContent + ":" + timestamp
	mac := hmac.New(sha256.New, []byte(communicationKey))
	mac.Write([]byte(signContentFinal))
	computed := mac.Sum(nil)
	computedSignature := base64.URLEncoding.EncodeToString(computed)
	if computedSignature != signature {
		log.Printf("[Sign] signature mismatch: got=%q want=%q", signature, computedSignature)
		return false, "签名无效"
	}
	return true, ""
}

func parseInt64(s string) (int64, error) {
	var n int64
	var err error
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			err = errInvalid
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n, err
}

var errInvalid = &parseError{}

type parseError struct{}

func (e *parseError) Error() string { return "invalid number" }

// 读取请求体但不消费（保留给 Gin）
func readBodyWithoutConsume(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return []byte{}, nil
	}
	if r.GetBody != nil {
		rc, err := r.GetBody()
		if err != nil {
			return []byte{}, nil
		}
		defer rc.Close()
		var buf strings.Builder
		p := make([]byte, 4096)
		for {
			n, err := rc.Read(p)
			if n > 0 {
				buf.Write(p[:n])
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
		}
		return []byte(buf.String()), nil
	}
	return []byte{}, nil
}
