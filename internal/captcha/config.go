package captcha

import (
	"strconv"
	"strings"
	"time"
)

// Settings 是本地滑块求解的持久化配置键（system_settings）。
const (
	KeyEnabled    = "captcha.local_slider_enabled"
	KeyHeadless   = "captcha.local_slider_headless"
	KeyChromePath = "captcha.local_slider_chrome_path"
	KeyTimeout    = "captcha.local_slider_timeout"
	KeyProxy      = "captcha.local_slider_proxy"
	KeyRetries    = "captcha.local_slider_retries"
)

// LoadConfig 通过 lookup 回调读取系统设置并构造求解器配置。
//
// 未配置 captcha.local_slider_enabled 时默认关闭，避免在无浏览器的服务器上
// 无意拉起进程；显式设置 "true" 后才启用。
func LoadConfig(lookup func(key string) string, globalProxy string) Config {
	cfg := DefaultConfig()
	cfg.Enabled = strings.EqualFold(strings.TrimSpace(lookup(KeyEnabled)), "true")
	if v := strings.TrimSpace(lookup(KeyHeadless)); v != "" {
		cfg.Headless = strings.EqualFold(v, "true")
	}
	cfg.ChromePath = strings.TrimSpace(lookup(KeyChromePath))
	cfg.Proxy = strings.TrimSpace(lookup(KeyProxy))
	if cfg.Proxy == "" {
		cfg.Proxy = strings.TrimSpace(globalProxy)
	}
	if v, err := strconv.Atoi(strings.TrimSpace(lookup(KeyTimeout))); err == nil && v > 0 && v <= 600 {
		cfg.Timeout = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(strings.TrimSpace(lookup(KeyRetries))); err == nil && v > 0 && v <= 10 {
		cfg.MaxRetries = v
	}
	return cfg
}
