package xianyu

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type MTOPOptions struct {
	API, Version, AppKey, Cookie, Origin, Referer, Proxy string
	Data                                                 map[string]any
	ExtraParams, ExtraHeaders                            map[string]string
	Method, FormField                                    string
}
type MTOPResult struct {
	Success, AccountInvalid, StatusUnknown bool
	Response                               map[string]any
	Error, Cookie, PunishURL               string
}

var tokenExpired = []string{"FAIL_SYS_TOKEN_EXOIRED", "FAIL_SYS_TOKEN_EXPIRED", "FAIL_SYS_TOKEN_EMPTY", "令牌过期", "令牌为空"}
var accountInvalid = []string{"FAIL_SYS_SESSION_EXPIRED", "FAIL_SYS_USER_VALIDATE", "RGV587", "FAIL_SYS_ILLEGAL_ACCESS", "FAIL_BIZ_WUA_IS_MACHINE", "WUA_IS_MACHINE", "挤爆", "punish", "captcha", "validate"}

// SliderSolver 是自动过滑块引擎的抽象，由上层（captcha 包）注入实现。
//
// 返回的 cookies 只需包含 x5* 系列且必须含 x5sec，MTOPCall 会把它合并进
// 当前 Cookie 后自动重试原请求。
type SliderSolver interface {
	Solve(ctx context.Context, punishURL, cookie string) (map[string]string, error)
}

var (
	sliderSolverMu sync.RWMutex
	sliderSolver   SliderSolver
)

// SetSliderSolver 注册（或清空）自动过滑块引擎。
func SetSliderSolver(s SliderSolver) {
	sliderSolverMu.Lock()
	sliderSolver = s
	sliderSolverMu.Unlock()
}

func currentSliderSolver() SliderSolver {
	sliderSolverMu.RLock()
	defer sliderSolverMu.RUnlock()
	return sliderSolver
}

func MTOPCall(ctx context.Context, opt MTOPOptions) MTOPResult {
	if opt.Version == "" {
		opt.Version = "1.0"
	}
	if opt.Origin == "" {
		opt.Origin = "https://www.goofish.com"
	}
	if opt.Referer == "" {
		opt.Referer = opt.Origin + "/"
	}
	if opt.FormField == "" {
		opt.FormField = "data"
	}
	if opt.AppKey == "" {
		opt.AppKey = "34839810"
	}
	if opt.Method == "" {
		opt.Method = http.MethodPost
	}
	cookie := opt.Cookie
	var last string
	solvedPunish := 0
	endpoint := fmt.Sprintf("https://h5api.m.goofish.com/h5/%s/%s/", opt.API, opt.Version)
	for attempt := 0; attempt < 5; attempt++ {
		raw, _ := json.Marshal(opt.Data)
		data := string(raw)
		ts := fmt.Sprint(time.Now().UnixMilli())
		token := strings.Split(ParseCookies(cookie)["_m_h5_tk"], "_")[0]
		q := url.Values{"jsv": {"2.7.2"}, "appKey": {opt.AppKey}, "t": {ts}, "sign": {Sign(ts, token, data)}, "v": {opt.Version}, "type": {"originaljson"}, "accountSite": {"xianyu"}, "dataType": {"json"}, "timeout": {"20000"}, "api": {opt.API}, "sessionOption": {"AutoLoginOnly"}, "spm_cnt": {"a21ybx.item.0.0"}}
		for k, v := range opt.ExtraParams {
			q.Set(k, v)
		}
		var body io.Reader
		if strings.EqualFold(opt.Method, http.MethodPost) {
			body = strings.NewReader(url.Values{opt.FormField: {data}}.Encode())
		} else {
			q.Set(opt.FormField, data)
		}
		req, e := http.NewRequestWithContext(ctx, opt.Method, endpoint+"?"+q.Encode(), body)
		if e != nil {
			return MTOPResult{Error: e.Error(), Cookie: cookie}
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", opt.Origin)
		req.Header.Set("Referer", opt.Referer)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/139 Safari/537.36")
		req.Header.Set("Cookie", strings.ReplaceAll(strings.ReplaceAll(cookie, "\r", ""), "\n", ""))
		for k, v := range opt.ExtraHeaders {
			req.Header.Set(k, v)
		}
		tr := proxy.Transport()
		if p := proxy.EffectiveURL(opt.Proxy); p != "" {
			parsed, parseErr := url.Parse(p)
			if parseErr != nil {
				return MTOPResult{Error: "代理地址无效: " + parseErr.Error(), Cookie: cookie}
			}
			tr.Proxy = http.ProxyURL(parsed)
		}
		resp, e := (&http.Client{Timeout: 30 * time.Second, Transport: tr}).Do(req)
		if e != nil {
			last = e.Error()
			select {
			case <-ctx.Done():
				return MTOPResult{Error: ctx.Err().Error(), Cookie: cookie}
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if e != nil {
			last = e.Error()
			continue
		}
		out, e := decodeMTOPResponse(b)
		if e != nil {
			last = "响应解析失败: " + e.Error()
			continue
		}
		updates := map[string]string{}
		for _, c := range resp.Cookies() {
			updates[c.Name] = c.Value
		}
		if len(updates) > 0 {
			cookie = mergeCookies(cookie, updates)
		}
		ret := firstRet(out)
		if strings.Contains(ret, "SUCCESS") {
			return MTOPResult{Success: true, Response: out, Cookie: cookie}
		}
		if containsAny(ret, tokenExpired) {
			last = ret
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if containsAny(ret, accountInvalid) {
			pURL := punishURL(out)
			// 风控返回 punish 验证链接时尝试本地自动过滑块（与父仓库编排一致：
			// 求解成功拿到新的 x5sec 后，合并 Cookie 并自动重试原请求）。
			// 链接过期（URLEXpiredError）视为本次求解失败，由上层重取链接。
			if pURL != "" && solvedPunish < 2 {
				if solver := currentSliderSolver(); solver != nil {
					if updates, solveErr := solver.Solve(ctx, pURL, cookie); solveErr == nil && len(updates) > 0 {
						cookie = mergeCookies(cookie, updates)
						solvedPunish++
						last = "punish(" + pURL + ") 已自动过滑块，重试请求"
						continue
					} else if solveErr != nil {
						last = "自动过滑块失败: " + solveErr.Error()
					}
				}
			}
			return MTOPResult{AccountInvalid: true, Response: out, Error: ret, Cookie: cookie, PunishURL: pURL}
		}
		return MTOPResult{Response: out, Error: ret, Cookie: cookie}
	}
	return MTOPResult{Error: last, Cookie: cookie, StatusUnknown: last != ""}
}

func decodeMTOPResponse(body []byte) (map[string]any, error) {
	var out map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	// 订单号、商品 ID、买家 ID 可能以 JSON 数字返回。默认解码为
	// float64 会丢失长整型精度，并在后续转字符串时变成科学计数法。
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func firstRet(m map[string]any) string {
	switch x := m["ret"].(type) {
	case []any:
		if len(x) > 0 {
			return fmt.Sprint(x[0])
		}
	case []string:
		if len(x) > 0 {
			return x[0]
		}
	case string:
		return x
	}
	return "调用失败"
}
func containsAny(s string, a []string) bool {
	for _, x := range a {
		if strings.Contains(strings.ToLower(s), strings.ToLower(x)) {
			return true
		}
	}
	return false
}
func punishURL(m map[string]any) string {
	if d, ok := m["data"].(map[string]any); ok {
		return fmt.Sprint(d["url"])
	}
	return ""
}
