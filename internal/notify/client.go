package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Channel struct {
	Type   string
	Config map[string]any
}

func ParseConfig(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	text := strings.TrimSpace(fmt.Sprint(value))
	if json.Unmarshal([]byte(text), &m) != nil && text != "" {
		m["config"] = text
	}
	return m
}
func text(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v := strings.TrimSpace(fmt.Sprint(m[k]))
		if v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

func Send(ctx context.Context, ch Channel, message string) error {
	t := strings.ToLower(strings.TrimSpace(ch.Type))
	c := ch.Config
	endpoint := ""
	method := http.MethodPost
	headers := map[string]string{}
	var body any
	switch t {
	case "ding_talk", "dingtalk":
		endpoint = text(c, "webhook_url", "config")
		if secret := text(c, "secret"); secret != "" {
			stamp := fmt.Sprint(time.Now().UnixMilli())
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write([]byte(stamp + "\n" + secret))
			sep := "&"
			if !strings.Contains(endpoint, "?") {
				sep = "?"
			}
			endpoint += sep + "timestamp=" + url.QueryEscape(stamp) + "&sign=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		}
		body = map[string]any{"msgtype": "markdown", "markdown": map[string]any{"title": "闲鱼自动回复通知", "text": message}}
	case "feishu", "lark":
		endpoint = text(c, "webhook_url")
		body = map[string]any{"msg_type": "text", "content": map[string]string{"text": message}}
	case "wechat":
		endpoint = text(c, "webhook_url")
		body = map[string]any{"msgtype": "text", "text": map[string]string{"content": message}}
	case "bark":
		endpoint = strings.TrimRight(text(c, "server_url"), "/")
		if endpoint == "" {
			endpoint = "https://api.day.app"
		}
		endpoint += "/push"
		body = map[string]any{"device_key": text(c, "device_key"), "title": first(text(c, "title"), "闲鱼自动回复通知"), "body": message, "sound": first(text(c, "sound"), "default"), "group": first(text(c, "group"), "xianyu")}
	case "telegram":
		token := text(c, "bot_token")
		endpoint = "https://api.telegram.org/bot" + token + "/sendMessage"
		body = map[string]any{"chat_id": text(c, "chat_id"), "text": message}
	case "pushplus":
		endpoint = strings.TrimRight(first(text(c, "server_url"), "https://www.pushplus.plus"), "/") + "/send"
		body = map[string]any{"token": text(c, "token"), "title": first(text(c, "title"), "闲鱼自动回复通知"), "content": message, "template": first(text(c, "template"), "txt"), "topic": text(c, "topic")}
	case "webhook":
		endpoint = text(c, "webhook_url")
		method = strings.ToUpper(first(text(c, "http_method"), http.MethodPost))
		body = map[string]any{"message": message, "content": message}
		if raw := c["headers"]; raw != nil {
			switch h := raw.(type) {
			case map[string]any:
				for k, v := range h {
					headers[k] = fmt.Sprint(v)
				}
			case string:
				var values map[string]string
				_ = json.Unmarshal([]byte(h), &values)
				for k, v := range values {
					headers[k] = v
				}
			}
		}
	default:
		return fmt.Errorf("不支持的通知渠道类型: %s", ch.Type)
	}
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("通知渠道地址未配置")
	}
	encoded, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := proxy.Client(15 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知接口HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
