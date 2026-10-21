package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const currentAppVersion = "1.0.0"

func (s *Server) pickupURLSuggestion(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		if referer, err := url.Parse(r.Header.Get("Referer")); err == nil && referer.Host != "" {
			origin = referer.Scheme + "://" + referer.Host
		}
	}
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_PUBLIC_URL")), "/")
	warning := ""
	if publicURL == "" {
		publicURL = origin
		if publicURL == "" || strings.Contains(publicURL, "localhost") || strings.Contains(publicURL, "127.0.0.1") {
			warning = "当前未配置公网前端地址，请设置 FRONTEND_PUBLIC_URL 后再向买家发送提货链接"
		}
	}
	pickupURL := ""
	if publicURL != "" {
		pickupURL = publicURL + "/pickup"
	}
	example := ""
	if pickupURL != "" {
		example = pickupURL + "?order_no=订单号&order_id=订单ID"
	}
	ok(w, "", map[string]any{"pickup_url": pickupURL, "warning": warning, "env_name": "FRONTEND_PUBLIC_URL", "example_url": example})
}

func (s *Server) locationChatRemoteTest(w http.ResponseWriter, r *http.Request) {
	var p struct {
		URL       string `json:"url"`
		SecretKey string `json:"secret_key"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	started := time.Now()
	mid := "location-test-" + randomHex(16)
	uuid := randomHex(32)
	data := map[string]any{"protocol_version": "xianyu-location-envelope-v1", "mid": mid, "uuid": uuid, "chat_id": "location-test-chat", "send_user_id": "location-test-receiver", "sender_user_id": "location-test-sender", "location": map[string]any{"longitude": "0", "latitude": "0", "title": "位置消息接口测试", "subtitle": "0, 0"}}
	result, err := testRemoteInterface(r.Context(), remoteTestRequest{RemoteURL: p.URL, RemoteSecretKey: p.SecretKey}, "xianyu_location_message", data, func(status int, _ string, _ map[string]any) bool { return status == http.StatusOK })
	if err != nil {
		write(w, 200, response{Success: false, Message: "位置聊天远程接口测试失败，请检查URL和秘钥"})
		return
	}
	message := firstNonemptyAPI(fmt.Sprint(result["message"]), "测试成功")
	if message == "<nil>" {
		message = "测试成功"
	}
	ok(w, message, map[string]any{"status_code": 200, "duration_ms": time.Since(started).Milliseconds(), "protocol_version": "xianyu-location-envelope-v1"})
}

func (s *Server) versionCheck(w http.ResponseWriter, r *http.Request) {
	baseURL := "https://xy-update.zhinianboke.com"
	if raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "update_config.json")); err == nil {
		var config map[string]any
		if json.Unmarshal(raw, &config) == nil {
			if configured := strings.TrimSpace(fmt.Sprint(config["update_url"])); configured != "" && configured != "<nil>" {
				baseURL = strings.TrimRight(configured, "/")
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version.json", nil)
	req.Header.Set("User-Agent", "XianyuAutoReply-GoUpdater")
	resp, err := proxy.Client(10 * time.Second).Do(req)
	empty := map[string]any{"has_update": false, "current_version": currentAppVersion, "remote_version": "", "description": "", "filename": "", "download_url": ""}
	if err != nil {
		write(w, 200, response{Success: false, Message: "无法连接更新服务器", Data: empty})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		write(w, 200, response{Success: false, Message: fmt.Sprintf("更新服务器返回错误状态码: %d", resp.StatusCode), Data: empty})
		return
	}
	var remote map[string]any
	if json.NewDecoder(resp.Body).Decode(&remote) != nil {
		write(w, 200, response{Success: false, Message: "更新服务器返回的数据格式无效", Data: empty})
		return
	}
	remoteVersion := strings.TrimSpace(fmt.Sprint(remote["version"]))
	if remoteVersion == "" || remoteVersion == "<nil>" {
		write(w, 200, response{Success: false, Message: "更新服务器未返回版本号", Data: empty})
		return
	}
	filename := strings.TrimSpace(fmt.Sprint(remote["filename"]))
	if filename == "<nil>" {
		filename = ""
	}
	downloadURL := ""
	if filename != "" {
		downloadURL = baseURL + "/" + filename
	}
	ok(w, "", map[string]any{"has_update": versionGreater(remoteVersion, currentAppVersion), "current_version": currentAppVersion, "remote_version": remoteVersion, "description": firstNonemptyAPI(fmt.Sprint(remote["description"]), "无更新说明"), "filename": filename, "download_url": downloadURL})
}

func versionGreater(remote, local string) bool {
	parse := func(value string) []int {
		parts := strings.Split(strings.TrimLeft(strings.TrimSpace(value), "vV"), ".")
		result := make([]int, len(parts))
		for index, part := range parts {
			result[index], _ = strconv.Atoi(part)
		}
		return result
	}
	a, b := parse(remote), parse(local)
	for len(a) < len(b) {
		a = append(a, 0)
	}
	for len(b) < len(a) {
		b = append(b, 0)
	}
	for index := range a {
		if a[index] != b[index] {
			return a[index] > b[index]
		}
	}
	return false
}
