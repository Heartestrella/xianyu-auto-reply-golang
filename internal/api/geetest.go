package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
)

type geetestState struct {
	Status  int
	Expires time.Time
	Online  bool
}

func geetestEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func geetestConfig() (string, string, string, string) {
	return geetestEnv("GEETEST_CAPTCHA_ID", "a30cdbb466e9349385762477cb2c7df6"), geetestEnv("GEETEST_PRIVATE_KEY", "6f70322308eb29ae0d85516a14a32d2c"), geetestEnv("GEETEST_USER_ID", "xianyu_system"), strings.TrimRight(geetestEnv("GEETEST_API_URL", "http://api.geetest.com"), "/")
}
func (s *Server) setGeetest(challenge string, status int, online bool) {
	s.geetestMu.Lock()
	defer s.geetestMu.Unlock()
	now := time.Now()
	for k, v := range s.geetestStatus {
		if now.After(v.Expires) {
			delete(s.geetestStatus, k)
		}
	}
	s.geetestStatus[challenge] = geetestState{status, now.Add(5 * time.Minute), online}
}
func (s *Server) getGeetest(challenge string) (geetestState, bool) {
	s.geetestMu.Lock()
	defer s.geetestMu.Unlock()
	v, ok := s.geetestStatus[challenge]
	if !ok || time.Now().After(v.Expires) {
		delete(s.geetestStatus, challenge)
		return geetestState{}, false
	}
	return v, true
}
func localGeetest(captcha string) map[string]any {
	return map[string]any{"success": 0, "gt": captcha, "challenge": strings.ReplaceAll(uuid.NewString(), "-", ""), "new_captcha": true}
}
func (s *Server) geetestRegister(w http.ResponseWriter, r *http.Request) {
	captcha, key, userID, base := geetestConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	q := url.Values{"digestmod": {"md5"}, "user_id": {userID}, "client_type": {"web"}, "gt": {captcha}, "json_format": {"1"}, "sdk": {"python-fastapi:3.1.1"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/register.php?"+q.Encode(), nil)
	resp, e := proxy.Client(30 * time.Second).Do(req)
	data := map[string]any{}
	online := false
	if e == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var raw map[string]any
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && json.Unmarshal(body, &raw) == nil {
			challenge := strings.TrimSpace(toString(raw["challenge"]))
			if len(challenge) == 32 {
				sum := md5.Sum([]byte(challenge + key))
				data = map[string]any{"success": 1, "gt": captcha, "challenge": hex.EncodeToString(sum[:]), "new_captcha": true}
				online = true
			}
		}
	}
	if !online {
		data = localGeetest(captcha)
	}
	challenge := toString(data["challenge"])
	s.setGeetest(challenge, 0, online)
	message := "获取成功"
	if !online {
		message = "宕机模式"
	}
	write(w, 200, map[string]any{"success": true, "code": 200, "message": message, "data": data})
}
func (s *Server) geetestValidate(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Challenge string `json:"challenge"`
		Validate  string `json:"validate"`
		Seccode   string `json:"seccode"`
	}
	if decode(r, &p) != nil || p.Challenge == "" || p.Validate == "" || p.Seccode == "" {
		write(w, 200, map[string]any{"success": false, "code": 400, "message": "正常模式，本地校验，参数challenge、validate、seccode不可为空"})
		return
	}
	state, exists := s.getGeetest(p.Challenge)
	if exists && state.Status == 1 {
		write(w, 200, map[string]any{"success": true, "code": 200, "message": "验证通过"})
		return
	}
	captcha, _, userID, base := geetestConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	values := url.Values{"seccode": {p.Seccode}, "json_format": {"1"}, "challenge": {p.Challenge}, "sdk": {"python-fastapi:3.1.1"}, "captchaid": {captcha}, "user_id": {userID}, "client_type": {"web"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/validate.php", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := proxy.Client(30 * time.Second).Do(req)
	if e != nil {
		write(w, 200, map[string]any{"success": false, "code": 500, "message": "验证服务异常"})
		return
	}
	defer resp.Body.Close()
	var result map[string]any
	e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	seccode := strings.TrimSpace(toString(result["seccode"]))
	if e != nil || seccode == "" {
		write(w, 200, map[string]any{"success": false, "code": 400, "message": "请求极验validate接口失败"})
		return
	}
	if seccode == "false" {
		write(w, 200, map[string]any{"success": false, "code": 400, "message": "极验二次验证不通过"})
		return
	}
	s.setGeetest(p.Challenge, 1, state.Online)
	write(w, 200, map[string]any{"success": true, "code": 200, "message": "验证通过"})
}
