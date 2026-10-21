package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type remoteTestRequest struct {
	RemoteURL       string `json:"remote_url"`
	RemoteSecretKey string `json:"remote_secret_key"`
}

func (s *Server) testTokenRemote(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p remoteTestRequest
	if err := decode(r, &p); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	result, err := testRemoteInterface(r.Context(), p, "xianyu_token", map[string]any{"cookies": ""}, tokenConnectivityMessage)
	s.recordRemoteTest(r, "risk_remote_token_test", p.RemoteURL, err == nil)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, "远程 Token 接口连接成功", result)
}

func (s *Server) testPasswordLoginRemote(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p remoteTestRequest
	if err := decode(r, &p); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	result, err := testRemoteInterface(r.Context(), p, "x5sec_ali", map[string]any{"url": ""}, passwordConnectivityMessage)
	s.recordRemoteTest(r, "risk_remote_password_test", p.RemoteURL, err == nil)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, "远程密码登录接口连接成功", result)
}

func testRemoteInterface(parent context.Context, p remoteTestRequest, kind string, data map[string]any, acceptable func(int, string, map[string]any) bool) (map[string]any, error) {
	endpoint := strings.TrimSpace(p.RemoteURL)
	secret := strings.TrimSpace(p.RemoteSecretKey)
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("远程接口地址无效")
	}
	if secret == "" {
		return nil, fmt.Errorf("远程接口密钥不能为空")
	}
	body, _ := json.Marshal(map[string]any{"type": kind, "data": data})
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建远程请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", secret)
	resp, err := proxy.Client(30 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接远程接口失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("读取远程响应失败: %w", err)
	}
	var result map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &result) != nil {
		return nil, fmt.Errorf("远程接口返回了无效 JSON")
	}
	if result == nil {
		result = map[string]any{}
	}
	message := fmt.Sprint(result["message"])
	if !acceptable(resp.StatusCode, message, result) {
		if message == "<nil>" || message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return result, fmt.Errorf("远程接口测试失败: %s", message)
	}
	return result, nil
}

func tokenConnectivityMessage(status int, message string, result map[string]any) bool {
	if status != http.StatusOK {
		return false
	}
	if success, _ := result["success"].(bool); success {
		data, _ := result["data"].(map[string]any)
		return strings.TrimSpace(fmt.Sprint(data["token"])) != "" && strings.TrimSpace(fmt.Sprint(data["device_id"])) != ""
	}
	return containsAny(strings.ToLower(message), "cookies长度不足", "cookie长度不足", "cookies不能为空", "cookie不能为空")
}

func passwordConnectivityMessage(status int, message string, _ map[string]any) bool {
	if status != http.StatusOK {
		return false
	}
	lower := strings.ToLower(message)
	return !containsAny(lower, "秘钥无效", "秘钥错误", "缺少秘钥", "api-key", "api_key", "鉴权失败", "认证失败", "unauthorized", "forbidden")
}

func containsAny(value string, hints ...string) bool {
	for _, hint := range hints {
		if strings.Contains(value, hint) {
			return true
		}
	}
	return false
}

func (s *Server) recordRemoteTest(r *http.Request, kind, endpoint string, success bool) {
	payload, _ := json.Marshal(map[string]any{"endpoint": endpoint, "success": success})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,data) VALUES(?,?,?)`, current(r).ID, kind, string(payload))
}
