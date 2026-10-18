package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

var remoteCaptchaKeys = map[string]string{"url": "captcha.remote_service_url", "secret_key": "captcha.remote_secret_key", "pass_cookies": "captcha.remote_pass_cookies", "block_remote_calls": "captcha.block_remote_calls", "local_weight": "captcha.real_mouse_weight_local", "remote_weight": "captcha.real_mouse_weight_remote", "remote_processing_max": "captcha.remote_processing_max", "remote_cooldown_seconds": "captcha.remote_cooldown_seconds"}

func firstNonemptyAPI(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) registerRiskRoutes(r chi.Router) {
	r.Get("/risk-control-logs", s.riskLogs)
	r.Get("/risk-control-logs/today-success-rate", s.riskTodayRate)
	r.Get("/risk-control-logs/local-slider-config", s.localSliderConfig)
	r.Put("/risk-control-logs/local-slider-config", s.localSliderConfigUpdate)
	r.Post("/captcha/slider-solve/test", s.remoteSliderTest)
	r.Get("/captcha/remote-config", s.remoteCaptchaConfig)
	r.Put("/captcha/remote-config", s.remoteCaptchaConfigUpdate)
}

func (s *Server) riskLogs(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	query := ` FROM event_logs WHERE kind LIKE 'risk%'`
	args := []any{}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	filters := []struct{ param, path string }{{"cookie_id", "account_id"}, {"processing_status", "$.processing_status"}, {"call_type", "$.call_type"}, {"call_user", "$.call_user"}}
	for _, f := range filters {
		if value := r.URL.Query().Get(f.param); value != "" {
			if f.path == "account_id" {
				query += ` AND account_id=?`
			} else {
				query += ` AND json_extract(data,?)=?`
				args = append(args, f.path)
			}
			args = append(args, value)
		}
	}
	if value := r.URL.Query().Get("start_date"); value != "" {
		query += ` AND created_at>=?`
		args = append(args, value)
	}
	if value := r.URL.Query().Get("end_date"); value != "" {
		query += ` AND created_at<datetime(?,'+1 day')`
		args = append(args, value)
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,owner_id,COALESCE(account_id,''),level,message,data,created_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, "加载风控日志失败: "+err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, owner int64
		var account, level, message, raw, created string
		_ = rows.Scan(&id, &owner, &account, &level, &message, &raw, &created)
		m := map[string]any{"id": id, "owner_id": owner, "cookie_id": account, "level": level, "message": message, "created_at": created}
		mergeJSON(m, raw)
		items = append(items, m)
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) riskTodayRate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query := ` FROM event_logs WHERE kind LIKE 'risk%' AND date(created_at)=date('now','localtime')`
	args := []any{}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	// Keep the aggregation contract identical to the original service: pending,
	// cancelled, and expired-link failures do not participate in success rates.
	status := `COALESCE(json_extract(data,'$.processing_status'),'')`
	callType := `COALESCE(json_extract(data,'$.call_type'),'local')`
	result := `COALESCE(json_extract(data,'$.processing_result'),message,'')`
	settled := status + ` NOT IN ('processing','cancelled') AND ` + result + ` NOT LIKE '%验证链接已过期%'`
	isSuccess := `(` + status + `='success' OR json_extract(data,'$.success')=1)`
	isRemote := callType + `='remote'`
	isProcessing := status + `='processing'`
	stmt := `SELECT
		COALESCE(sum(CASE WHEN ` + settled + ` THEN 1 ELSE 0 END),0),
		COALESCE(sum(CASE WHEN ` + settled + ` AND ` + isSuccess + ` THEN 1 ELSE 0 END),0),
		COALESCE(sum(CASE WHEN ` + settled + ` AND ` + isRemote + ` THEN 1 ELSE 0 END),0),
		COALESCE(sum(CASE WHEN ` + settled + ` AND ` + isRemote + ` AND ` + isSuccess + ` THEN 1 ELSE 0 END),0),
		COALESCE(sum(CASE WHEN ` + isProcessing + ` THEN 1 ELSE 0 END),0),
		COALESCE(sum(CASE WHEN ` + isProcessing + ` AND ` + isRemote + ` THEN 1 ELSE 0 END),0)` + query
	var total, success, remoteTotal, remoteSuccess, processing, remoteProcessing int
	if err := s.db.QueryRowContext(r.Context(), stmt, args...).Scan(&total, &success, &remoteTotal, &remoteSuccess, &processing, &remoteProcessing); err != nil {
		fail(w, 500, "统计当日成功率失败: "+err.Error())
		return
	}
	localTotal := total - remoteTotal
	localSuccess := success - remoteSuccess
	localProcessing := processing - remoteProcessing
	rate := func(okCount, all int) float64 {
		if all == 0 {
			return 0
		}
		return math.Round(float64(okCount)*10000/float64(all)) / 100
	}
	ok(w, "", map[string]any{
		"date":              time.Now().Format("2006-01-02"),
		"total":             total,
		"success":           success,
		"rate":              rate(success, total),
		"local_total":       localTotal,
		"local_success":     localSuccess,
		"local_rate":        rate(localSuccess, localTotal),
		"remote_total":      remoteTotal,
		"remote_success":    remoteSuccess,
		"remote_rate":       rate(remoteSuccess, remoteTotal),
		"processing":        processing,
		"local_processing":  localProcessing,
		"remote_processing": remoteProcessing,
	})
}
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if current(r).Role != "ADMIN" {
		fail(w, 403, "需要管理员权限")
		return false
	}
	return true
}
func (s *Server) localSliderConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var value string
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key='captcha.local_slider_disabled'`).Scan(&value)
	ok(w, "本机滑块处理开关加载成功", map[string]any{"enabled": strings.EqualFold(value, "true")})
}
func (s *Server) localSliderConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p struct {
		Enabled bool `json:"enabled"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO system_settings(key,value,description) VALUES('captcha.local_slider_disabled',?,'本机是否停止处理滑块') ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, strconv.FormatBool(p.Enabled))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, map[bool]string{true: "本机滑块不处理已开启", false: "本机滑块不处理已关闭"}[p.Enabled], map[string]any{"enabled": p.Enabled})
}

func (s *Server) remoteCaptchaConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT key,value FROM system_settings WHERE key LIKE 'captcha.%'`)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		values[k] = v
	}
	data := map[string]any{"url": values[remoteCaptchaKeys["url"]], "secret_key": values[remoteCaptchaKeys["secret_key"]], "pass_cookies": values[remoteCaptchaKeys["pass_cookies"]] == "true", "block_remote_calls": values[remoteCaptchaKeys["block_remote_calls"]] != "false", "local_weight": floatValue(values[remoteCaptchaKeys["local_weight"]], 1), "remote_weight": floatValue(values[remoteCaptchaKeys["remote_weight"]], 1), "remote_processing_max": intValue(values[remoteCaptchaKeys["remote_processing_max"]], 5), "remote_cooldown_seconds": intValue(values[remoteCaptchaKeys["remote_cooldown_seconds"]], 60)}
	ok(w, "", data)
}
func floatValue(value string, fallback float64) float64 {
	v, e := strconv.ParseFloat(value, 64)
	if e != nil {
		return fallback
	}
	return v
}
func intValue(value string, fallback int) int {
	v, e := strconv.Atoi(value)
	if e != nil {
		return fallback
	}
	return v
}
func (s *Server) remoteCaptchaConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	remoteURL := firstString(p, "url")
	if remoteURL != "" {
		parsed, err := url.Parse(remoteURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			write(w, 200, response{Success: false, Message: "远程服务URL无效"})
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	for field, key := range remoteCaptchaKeys {
		value := p[field]
		if value == nil {
			continue
		}
		if (field == "remote_processing_max" || field == "remote_cooldown_seconds") && intNumber(value) < 0 {
			write(w, 200, response{Success: false, Message: "数值不能小于 0"})
			return
		}
		text := fmt.Sprint(value)
		if b, ok := value.(bool); ok {
			text = strconv.FormatBool(b)
		}
		_, err = tx.ExecContext(r.Context(), `INSERT INTO system_settings(key,value,description) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, key, text, "远程过滑块配置")
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "远程过滑块配置已保存", nil)
}

func postJSON(ctxReq *http.Request, endpoint string, payload map[string]any) (int, map[string]any, string, error) {
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctxReq.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out, string(body), nil
}

func (s *Server) remoteSliderTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	endpoint := firstString(p, "url")
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		write(w, 200, response{Success: false, Message: "请填写有效的远程服务URL"})
		return
	}
	status, body, raw, err := postJSON(r, endpoint, map[string]any{"secret_key": firstString(p, "secret_key"), "account_id": "connectivity-test", "url": ""})
	if err != nil {
		write(w, 200, response{Success: false, Message: "无法连接到远程服务: " + err.Error()})
		return
	}
	message := firstString(body, "message", "detail")
	if status != 200 {
		write(w, 200, response{Success: false, Message: fmt.Sprintf("远程服务返回异常（HTTP %d）：%s", status, firstNonemptyAPI(message, raw))})
		return
	}
	if strings.Contains(message, "无效") || strings.Contains(message, "缺少秘钥") {
		write(w, 200, response{Success: false, Message: "连接成功，但秘钥无效（远程：" + message + "）"})
		return
	}
	ok(w, "连接成功（远程返回："+firstNonemptyAPI(message, "正常")+"）", nil)
}

func (s *Server) sliderSolve(w http.ResponseWriter, r *http.Request) {
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	secret := firstString(p, "secret_key")
	if secret == "" {
		write(w, 200, response{Success: false, Message: "缺少秘钥"})
		return
	}
	var owner int64
	err := s.db.QueryRowContext(r.Context(), `SELECT owner_id FROM user_settings WHERE key IN ('secret_key','card_secret_key') AND value=? LIMIT 1`, secret).Scan(&owner)
	if err != nil {
		write(w, 200, response{Success: false, Message: "无效的秘钥"})
		return
	}
	punish := firstString(p, "url")
	if punish == "" {
		write(w, 200, response{Success: false, Message: "punish 链接不能为空"})
		return
	}
	var blocked, endpoint, remoteSecret string
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key=?`, remoteCaptchaKeys["block_remote_calls"]).Scan(&blocked)
	if blocked == "true" {
		write(w, 200, response{Success: false, Message: "系统已禁止远程过滑块调用"})
		return
	}
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key=?`, remoteCaptchaKeys["url"]).Scan(&endpoint)
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key=?`, remoteCaptchaKeys["secret_key"]).Scan(&remoteSecret)
	if endpoint == "" {
		write(w, 200, response{Success: false, Message: "未配置远程过滑块服务"})
		return
	}
	payload := map[string]any{"secret_key": remoteSecret, "account_id": firstString(p, "account_id"), "url": punish, "cookies": p["cookies"], "device_id": p["device_id"], "browser_timeout": p["browser_timeout"]}
	status, body, raw, err := postJSON(r, endpoint, payload)
	success := err == nil && status == 200 && body["success"] == true
	logData, _ := json.Marshal(map[string]any{"processing_status": map[bool]string{true: "success", false: "failed"}[success], "success": success, "call_type": "remote", "call_user": owner, "remote_status": status})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'risk_control',?,?,?,?)`, owner, firstString(p, "account_id"), map[bool]string{true: "info", false: "error"}[success], firstNonemptyAPI(firstString(body, "message"), raw), string(logData))
	if err != nil {
		write(w, 200, response{Success: false, Message: "过滑块失败: " + err.Error()})
		return
	}
	write(w, 200, response{Success: success, Message: firstNonemptyAPI(firstString(body, "message"), map[bool]string{true: "过滑块成功", false: "过滑块失败"}[success]), Data: body["data"]})
}
