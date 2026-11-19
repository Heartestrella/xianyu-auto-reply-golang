package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/captcha"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

// apiSliderSolver 将本地纯 Go 滑块求解器接入 MTOP 自动重试链路。
//
// 与父仓库的区别：父仓库用 Playwright 拉起 Chromium；Go 版直接通过 CDP
// （DevTools Protocol）驱动本地 Chrome/Chromium，无任何 Python 依赖。
type apiSliderSolver struct{ s *Server }

func (a apiSliderSolver) Solve(ctx context.Context, punishURL, cookie string) (map[string]string, error) {
	cfg := a.s.localSliderConfigFor(ctx)
	if !cfg.Enabled {
		return nil, captchaDisabledError{}
	}
	result, err := captcha.NewSolver(cfg).Solve(ctx, punishURL, cookie)
	status, level, message := "success", "info", "本机自动过滑块成功"
	if err != nil {
		status, level, message = "failed", "error", "本机自动过滑块失败: "+err.Error()
	}
	attempts := 0
	if result != nil {
		attempts = result.Attempts
	}
	var baxia *captcha.BaxiaPunishError
	if errors.As(err, &baxia) {
		// 图形识别验证无法脚本通过：单独记一条通知类日志，便于前端提示人工处理。
		payload, _ := json.Marshal(map[string]any{"title": "触发风控图形验证", "raw": punishURL, "is_read": false})
		_, _ = a.s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(0,'baxia_punish_captcha','','warn',?,?)`, baxia.Error(), string(payload))
	}
	a.s.logLocalSliderEvent(ctx, punishURL, status, level, message, attempts)
	if err != nil {
		return nil, err
	}
	return result.Cookies, nil
}

// logLocalSliderEvent 写入风控日志，便于前端成功率统计区分本机/远程。
func (s *Server) logLocalSliderEvent(ctx context.Context, punishURL, status, level, message string, attempts int) {
	payload, _ := json.Marshal(map[string]any{
		"processing_status": status,
		"success":           status == "success",
		"call_type":         "local",
		"attempts":          attempts,
		"url":               punishURL,
	})
	_, _ = s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(0,'risk_control','',?,?,?)`, level, message, string(payload))
}

type captchaDisabledError struct{}

func (captchaDisabledError) Error() string { return "本地自动过滑块未启用" }

// localSliderConfig 读取 system_settings 中的本地滑块求解配置。
func (s *Server) localSliderConfigFor(ctx context.Context) captcha.Config {
	lookup := func(key string) string {
		var value string
		if s.db.QueryRowContext(ctx, `SELECT COALESCE(value,'') FROM system_settings WHERE key=?`, key).Scan(&value) == nil {
			return value
		}
		return ""
	}
	cfg := captcha.LoadConfig(lookup, s.globalProxyURL(ctx))
	// captcha.local_slider_disabled 为父仓库的"本机不处理滑块"总开关，优先于启用开关。
	var disabled string
	if s.db.QueryRowContext(ctx, `SELECT COALESCE(value,'') FROM system_settings WHERE key='captcha.local_slider_disabled'`).Scan(&disabled) == nil && strings.EqualFold(disabled, "true") {
		cfg.Enabled = false
	}
	return cfg
}

// globalProxyURL 返回全局网络代理地址（仅在 proxy.enabled 打开时生效）。
func (s *Server) globalProxyURL(ctx context.Context) string {
	var enabled, proxyURL string
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(value,'') FROM system_settings WHERE key='proxy.enabled'`).Scan(&enabled)
	if !strings.EqualFold(strings.TrimSpace(enabled), "true") {
		return ""
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(value,'') FROM system_settings WHERE key='proxy.url'`).Scan(&proxyURL)
	return strings.TrimSpace(proxyURL)
}

// registerLocalSliderSolver 在服务启动时把求解器注入 MTOP 调用链。
func (s *Server) registerLocalSliderSolver() {
	xianyu.SetSliderSolver(apiSliderSolver{s: s})
}

// localSliderConfigView 处理 GET /captcha/local-config。
func (s *Server) localSliderConfigView(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	cfg := s.localSliderConfigFor(r.Context())
	data := map[string]any{
		"enabled":     cfg.Enabled,
		"headless":    cfg.Headless,
		"chrome_path": cfg.ChromePath,
		"proxy":       cfg.Proxy,
		"timeout":     int(cfg.Timeout.Seconds()),
		"retries":     cfg.MaxRetries,
	}
	ok(w, "本地滑块配置加载成功", data)
}

// localSliderConfigSave 处理 PUT /captcha/local-config。
func (s *Server) localSliderConfigSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p struct {
		Enabled    *bool   `json:"enabled"`
		Headless   *bool   `json:"headless"`
		ChromePath *string `json:"chrome_path"`
		Proxy      *string `json:"proxy"`
		Timeout    *int    `json:"timeout"`
		Retries    *int    `json:"retries"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	save := func(key, value, description string) error {
		_, err := s.db.ExecContext(r.Context(), `INSERT INTO system_settings(key,value,description) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, key, value, description)
		return err
	}
	if p.Enabled != nil {
		if err := save(captcha.KeyEnabled, strconv.FormatBool(*p.Enabled), "启用本地自动过滑块"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if p.Headless != nil {
		if err := save(captcha.KeyHeadless, strconv.FormatBool(*p.Headless), "本地滑块浏览器无头模式"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if p.ChromePath != nil {
		if err := save(captcha.KeyChromePath, strings.TrimSpace(*p.ChromePath), "本地滑块浏览器可执行文件路径"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if p.Proxy != nil {
		if err := save(captcha.KeyProxy, strings.TrimSpace(*p.Proxy), "本地滑块浏览器代理地址"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if p.Timeout != nil {
		if err := save(captcha.KeyTimeout, strconv.Itoa(*p.Timeout), "本地滑块单次超时（秒）"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if p.Retries != nil {
		if err := save(captcha.KeyRetries, strconv.Itoa(*p.Retries), "本地滑块滑动重试次数"); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	ok(w, "本地滑块配置已保存", s.localSliderConfigFor(r.Context()))
}

// localSliderSolve 处理 POST /captcha/local-solve，用于手工验证本地引擎。
func (s *Server) localSliderSolve(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p struct {
		URL    string `json:"url"`
		Cookie string `json:"cookie"`
	}
	if decode(r, &p) != nil || strings.TrimSpace(p.URL) == "" {
		fail(w, 400, "请填写 punish 验证链接")
		return
	}
	cfg := s.localSliderConfigFor(r.Context())
	if !cfg.Enabled {
		write(w, 200, response{Success: false, Message: "本地自动过滑块未启用，请先在配置中开启"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cfg.Timeout)
	defer cancel()
	result, err := captcha.NewSolver(cfg).Solve(ctx, p.URL, p.Cookie)
	if err != nil {
		write(w, 200, response{Success: false, Message: "本地过滑块失败: " + err.Error()})
		return
	}
	ok(w, "本地过滑块成功", map[string]any{"cookies": result.Cookies, "attempts": result.Attempts, "engine": result.Engine})
}
