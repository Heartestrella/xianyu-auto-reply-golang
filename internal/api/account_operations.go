package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func batchAccountIDs(r *http.Request) []string {
	var object struct {
		AccountIDs []string `json:"account_ids"`
	}
	if decode(r, &object) == nil && len(object.AccountIDs) > 0 {
		return object.AccountIDs
	}
	return nil
}
func batchResult(w http.ResponseWriter, action string, success []string, failed []map[string]string) {
	message := fmt.Sprintf("%s完成：成功 %d 个，失败 %d 个", action, len(success), len(failed))
	write(w, 200, response{Success: len(success) > 0, Message: message, Data: map[string]any{"success_count": len(success), "failed_count": len(failed), "success_ids": success, "failed_items": failed}})
}

func (s *Server) accountCloseNoticeBatch(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	ids := batchAccountIDs(r)
	if len(ids) == 0 {
		write(w, 200, response{Success: false, Message: "请选择至少一个账号", Data: map[string]any{"success_count": 0, "failed_count": 0, "success_ids": []any{}, "failed_items": []any{}}})
		return
	}
	success := []string{}
	failed := []map[string]string{}
	for _, id := range ids {
		ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, id)
		if accessErr != nil {
			failed = append(failed, map[string]string{"account_id": id, "message": "账号不存在或无权限访问"})
			continue
		}
		var account platformAccount
		err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, ownerID, id).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass)
		if err != nil {
			failed = append(failed, map[string]string{"account_id": id, "message": "账号不存在或无权限访问"})
			continue
		}
		result := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idlemessage.pc.profile.notice.update", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Data: map[string]any{"oprType": 2, "appKeys": []string{"444e9908a51d1cb236a27862abc769c9"}}})
		if !result.Success {
			failed = append(failed, map[string]string{"account_id": id, "message": firstNonemptyAPI(result.Error, "关闭通知失败")})
			continue
		}
		if result.Cookie != account.Cookie {
			_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, result.Cookie, ownerID, id)
		}
		success = append(success, id)
	}
	batchResult(w, "关闭通知", success, failed)
}

func (s *Server) accountClearTokenBatch(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	ids := batchAccountIDs(r)
	if len(ids) == 0 {
		write(w, 200, response{Success: false, Message: "请选择至少一个账号"})
		return
	}
	success := []string{}
	failed := []map[string]string{}
	for _, id := range ids {
		ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, id)
		if accessErr != nil {
			failed = append(failed, map[string]string{"account_id": id, "message": "账号不存在或无权限访问"})
			continue
		}
		var cookie string
		err := s.db.QueryRowContext(r.Context(), `SELECT cookie FROM xy_accounts WHERE owner_id=? AND account_id=?`, ownerID, id).Scan(&cookie)
		if err != nil {
			failed = append(failed, map[string]string{"account_id": id, "message": "账号不存在或无权限访问"})
			continue
		}
		if xianyu.ParseCookies(cookie)["unb"] == "" {
			failed = append(failed, map[string]string{"account_id": id, "message": "账号缺少unb信息，无法清除缓存"})
			continue
		}
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET last_refresh_at=NULL,status='active',disable_reason=NULL,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, ownerID, id)
		if s.accountRestart != nil {
			s.accountRestart(id)
		}
		success = append(success, id)
	}
	batchResult(w, "清除Token缓存并重启", success, failed)
}

func mergeResponseCookies(raw string, cookies []*http.Cookie) (string, []string) {
	current := xianyu.ParseCookies(raw)
	updated := []string{}
	for _, cookie := range cookies {
		if cookie.MaxAge < 0 {
			continue
		}
		if old := current[cookie.Name]; old != cookie.Value {
			current[cookie.Name] = cookie.Value
			updated = append(updated, cookie.Name)
		}
	}
	parts := make([]string, 0, len(current))
	for k, v := range current {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; "), updated
}
func renewCookie(ctx context.Context, raw string, configuredProxy ...string) (string, []string, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if len(configuredProxy) > 0 && strings.TrimSpace(configuredProxy[0]) != "" {
		parsed, err := url.Parse(configuredProxy[0])
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return raw, nil, fmt.Errorf("账号代理配置无效")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	current := raw
	updated := []string{}
	calls := []struct {
		endpoint string
		query    url.Values
		body     string
	}{{"https://passport.goofish.com/newlogin/silentHasLogin.do", url.Values{"documentReferer": {"https://www.goofish.com/"}, "appName": {"xianyu"}, "appEntrance": {"xianyu_sdkSilent"}, "fromSite": {"0"}, "ltl": {"true"}}, ""}, {"https://passport.goofish.com/ac/account/setLoginSettings.do", url.Values{"fromSite": {"77"}, "appName": {"xianyu"}, "bizEntrance": {"web"}}, "status=0"}}
	for _, call := range calls {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, call.endpoint+"?"+call.query.Encode(), strings.NewReader(call.body))
		if err != nil {
			return current, updated, err
		}
		req.Header.Set("Cookie", strings.ReplaceAll(strings.ReplaceAll(current, "\r", ""), "\n", ""))
		req.Header.Set("Referer", "https://www.goofish.com/")
		req.Header.Set("User-Agent", "Mozilla/5.0 Windows Chrome/139")
		if call.body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := client.Do(req)
		if err != nil {
			return current, updated, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 302 && resp.StatusCode != 303 {
			return current, updated, fmt.Errorf("续期接口HTTP %d", resp.StatusCode)
		}
		var names []string
		current, names = mergeResponseCookies(current, resp.Cookies())
		updated = append(updated, names...)
	}
	if len(updated) == 0 {
		return current, updated, fmt.Errorf("续期接口未返回有效Cookie")
	}
	return current, updated, nil
}

func (s *Server) accountRenewLogin(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	ids := []string{}
	if decode(r, &ids) != nil || len(ids) == 0 {
		fail(w, 400, "请选择至少一个账号")
		return
	}
	results := []map[string]any{}
	successCount, failedCount := 0, 0
	for _, id := range ids {
		ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, id)
		if accessErr != nil {
			results = append(results, map[string]any{"account_id": id, "success": false, "message": "账号不存在或无权限访问"})
			failedCount++
			continue
		}
		var account platformAccount
		if s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, ownerID, id).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass) != nil {
			results = append(results, map[string]any{"account_id": id, "success": false, "message": "账号不存在"})
			failedCount++
			continue
		}
		renewed, names, err := renewCookie(r.Context(), account.Cookie, accountProxy(account))
		if renewed != account.Cookie {
			_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,status='active',disable_reason=NULL,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, renewed, ownerID, id)
		}
		if err != nil {
			results = append(results, map[string]any{"account_id": id, "success": false, "message": err.Error()})
			failedCount++
			continue
		}
		if s.accountRestart != nil {
			s.accountRestart(id)
		}
		results = append(results, map[string]any{"account_id": id, "success": true, "message": fmt.Sprintf("续期成功，更新了 %d 个字段：%s", len(names), strings.Join(names, ", "))})
		successCount++
	}
	write(w, 200, response{Success: successCount > 0, Message: fmt.Sprintf("续期完成：成功 %d 个，失败 %d 个", successCount, failedCount), Data: map[string]any{"results": results, "success_count": successCount, "failed_count": failedCount}})
}
