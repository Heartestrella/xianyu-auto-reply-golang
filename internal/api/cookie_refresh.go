package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ExecuteScheduledCookieRenewal renews every enabled account through the same
// hasLogin/login-settings protocol used by the manual endpoint. Partial
// success is persisted immediately and each account gets an auditable log.
func (s *Server) ExecuteScheduledCookieRenewal(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE status='active' ORDER BY id`)
	if err != nil {
		return err
	}
	type candidate struct {
		id, owner              int64
		account, cookie, proxy string
	}
	accounts := []candidate{}
	for rows.Next() {
		var a candidate
		var proxyAccount platformAccount
		if rows.Scan(&a.id, &a.owner, &a.account, &a.cookie, &proxyAccount.ProxyType, &proxyAccount.ProxyHost, &proxyAccount.ProxyPort, &proxyAccount.ProxyUser, &proxyAccount.ProxyPass) == nil {
			a.proxy = accountProxy(proxyAccount)
			accounts = append(accounts, a)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	batchID := fmt.Sprintf("scheduled-%d", time.Now().UnixMilli())
	failures := []string{}
	for _, a := range accounts {
		renewed, names, renewErr := renewCookie(ctx, a.cookie, a.proxy)
		if renewed != a.cookie {
			_, err = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, renewed, a.id)
			if err != nil && renewErr == nil {
				renewErr = err
			}
		}
		status, level, message := "success", "INFO", fmt.Sprintf("接口续期成功，更新%d个Cookie字段", len(names))
		if renewErr != nil {
			status, level, message = "failed", "ERROR", renewErr.Error()
			failures = append(failures, a.account+": "+renewErr.Error())
		} else {
			_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET status='active',disable_reason=NULL,last_account_error_time=NULL,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.id)
			if s.accountRestart != nil {
				s.accountRestart(a.account)
			}
		}
		payload, _ := json.Marshal(map[string]any{"batch_id": batchID, "status": status, "updated_cookie_count": len(names), "updated_fields": names})
		_, _ = s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'api_cookie_renew',?,?,?,?)`, a.owner, a.account, level, message, string(payload))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// ExecuteScheduledCookiesRefresh implements the original cookies_refresh task
// without Playwright. The Go MTOP refresher performs the authenticated request,
// merges every Set-Cookie value, and persists the same per-account schedule and
// batch log semantics. Disabled accounts are retried and re-enabled on success.
func (s *Server) ExecuteScheduledCookiesRefresh(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,status,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE status<>'deleted' ORDER BY id`)
	if err != nil {
		return err
	}
	type candidate struct {
		id, owner                      int64
		account, cookie, status, proxy string
	}
	accounts := []candidate{}
	for rows.Next() {
		var a candidate
		var proxyAccount platformAccount
		if rows.Scan(&a.id, &a.owner, &a.account, &a.cookie, &a.status, &proxyAccount.ProxyType, &proxyAccount.ProxyHost, &proxyAccount.ProxyPort, &proxyAccount.ProxyUser, &proxyAccount.ProxyPass) == nil {
			a.proxy = accountProxy(proxyAccount)
			accounts = append(accounts, a)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	batchID := fmt.Sprintf("cookies-%d", time.Now().UnixMilli())
	failures := []string{}
	for _, a := range accounts {
		var expireAt string
		err = s.db.QueryRowContext(ctx, `SELECT expire_at FROM cookie_refresh_schedules WHERE account_id=?`, a.account).Scan(&expireAt)
		if errors.Is(err, sql.ErrNoRows) {
			delay := 1 + a.id%30
			_, err = s.db.ExecContext(ctx, `INSERT INTO cookie_refresh_schedules(account_id,expire_at,last_status) VALUES(?,datetime('now',?),'initialized')`, a.account, fmt.Sprintf("+%d seconds", delay))
			if err != nil {
				return err
			}
			s.logCookieRefreshResult(ctx, batchID, a, "initialized", 0, fmt.Sprintf("首次初始化续期时间，%d秒后到期", delay))
			continue
		}
		if err != nil {
			return err
		}
		var due int
		if err = s.db.QueryRowContext(ctx, `SELECT expire_at<=CURRENT_TIMESTAMP FROM cookie_refresh_schedules WHERE account_id=?`, a.account).Scan(&due); err != nil {
			return err
		}
		if due == 0 {
			continue
		}
		if isDisabledStatus(a.status) {
			var consecutive int
			_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT json_extract(data,'$.status') status FROM event_logs WHERE kind='cookie_refresh' AND account_id=? ORDER BY id DESC LIMIT 10) WHERE status='failed'`, a.account).Scan(&consecutive)
			if consecutive >= 10 {
				continue
			}
		}
		if strings.TrimSpace(a.cookie) == "" {
			err = errors.New("账号Cookie为空，无法执行续期")
		} else if s.cookieRefresh != nil {
			var renewed string
			var names []string
			renewed, names, err = s.cookieRefresh(ctx, a.cookie)
			if err == nil {
				err = s.finishCookiesRefresh(ctx, batchID, a, renewed, names)
			}
		} else {
			var renewed string
			var names []string
			renewed, names, err = renewCookie(ctx, a.cookie, a.proxy)
			if err == nil {
				err = s.finishCookiesRefresh(ctx, batchID, a, renewed, names)
			}
		}
		if err != nil {
			message := truncateRunes(err.Error(), 500)
			_, _ = s.db.ExecContext(ctx, `UPDATE cookie_refresh_schedules SET last_status='failed',last_error_message=?,updated_at=CURRENT_TIMESTAMP WHERE account_id=?`, message, a.account)
			s.logCookieRefreshResult(ctx, batchID, a, "failed", 0, message)
			failures = append(failures, a.account+": "+message)
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func isDisabledStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "inactive", "disabled", "suspended":
		return true
	default:
		return false
	}
}

func (s *Server) finishCookiesRefresh(ctx context.Context, batchID string, a struct {
	id, owner                      int64
	account, cookie, status, proxy string
}, renewed string, names []string) error {
	if strings.TrimSpace(renewed) == "" {
		return errors.New("续期响应未返回有效Cookie")
	}
	nextSeconds := 60 + a.id%241
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,status='active',disable_reason=NULL,last_account_error_time=NULL,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, renewed, a.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cookie_refresh_schedules SET expire_at=datetime('now',?),last_refresh_at=CURRENT_TIMESTAMP,last_status='success',last_error_message=NULL,updated_at=CURRENT_TIMESTAMP WHERE account_id=?`, fmt.Sprintf("+%d seconds", nextSeconds), a.account); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"batch_id": batchID, "status": "success", "updated_cookie_count": len(names), "updated_fields": names, "next_expire_seconds": nextSeconds})
	if _, err = tx.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'cookie_refresh',?,'INFO',?,?)`, a.owner, a.account, fmt.Sprintf("COOKIES续期成功，更新%d个Cookie字段", len(names)), string(payload)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.accountRestart != nil {
		s.accountRestart(a.account)
	}
	return nil
}

func (s *Server) logCookieRefreshResult(ctx context.Context, batchID string, a struct {
	id, owner                      int64
	account, cookie, status, proxy string
}, status string, updated int, message string) {
	level := "INFO"
	if status == "failed" {
		level = "ERROR"
	}
	payload, _ := json.Marshal(map[string]any{"batch_id": batchID, "status": status, "updated_cookie_count": updated, "error_message": message})
	_, _ = s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'cookie_refresh',?,?,?,?)`, a.owner, a.account, level, message, string(payload))
}

type refreshAccount struct {
	ID                           int64
	OwnerID                      int64
	AccountID, Username, Cookie  string
	Proxy                        string
	LastPassword, LastAccountErr sql.NullString
}

func (s *Server) loadRefreshAccount(r *http.Request) (refreshAccount, string) {
	u := current(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "account_id"), 10, 64)
	if err != nil || id < 1 {
		return refreshAccount{}, "账号不存在"
	}
	var account refreshAccount
	var proxyAccount platformAccount
	query := `SELECT id,owner_id,account_id,COALESCE(username,''),cookie,last_password_login_time,last_account_error_time,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE id=?`
	args := []any{id}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	err = s.db.QueryRowContext(r.Context(), query, args...).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Username, &account.Cookie, &account.LastPassword, &account.LastAccountErr, &proxyAccount.ProxyType, &proxyAccount.ProxyHost, &proxyAccount.ProxyPort, &proxyAccount.ProxyUser, &proxyAccount.ProxyPass)
	if err != nil {
		return refreshAccount{}, "账号不存在或无权操作"
	}
	account.Proxy = accountProxy(proxyAccount)
	return account, ""
}

func cooldownState(raw sql.NullString, duration time.Duration) (bool, int) {
	if !raw.Valid {
		return false, 0
	}
	timestamp, err := time.Parse("2006-01-02 15:04:05", raw.String)
	if err != nil {
		timestamp, err = time.Parse(time.RFC3339, raw.String)
	}
	if err != nil {
		return false, 0
	}
	remaining := duration - time.Since(timestamp)
	if remaining <= 0 {
		return false, 0
	}
	return true, int(remaining.Seconds())
}

func (s *Server) cookieRefreshCooldown(w http.ResponseWriter, r *http.Request) {
	account, message := s.loadRefreshAccount(r)
	if message != "" {
		write(w, 200, response{Success: false, Message: message})
		return
	}
	passwordCooldown, passwordRemaining := cooldownState(account.LastPassword, time.Minute)
	errorCooldown, errorRemaining := cooldownState(account.LastAccountErr, 5*time.Hour)
	ok(w, "查询成功", map[string]any{"account_id": account.ID, "username": account.Username, "password_login_cooldown": passwordCooldown, "password_login_remaining_seconds": passwordRemaining, "account_error_cooldown": errorCooldown, "account_error_remaining_seconds": errorRemaining, "last_password_login_time": nullText(account.LastPassword), "last_account_error_time": nullText(account.LastAccountErr)})
}

func (s *Server) cookieRefreshCooldownReset(w http.ResponseWriter, r *http.Request) {
	account, message := s.loadRefreshAccount(r)
	if message != "" {
		write(w, 200, response{Success: false, Message: message})
		return
	}
	typ := r.URL.Query().Get("cooldown_type")
	column, successMessage := "", ""
	switch typ {
	case "password_login":
		column, successMessage = "last_password_login_time", "密码登录冷却时间已重置"
	case "account_error":
		column, successMessage = "last_account_error_time", "账密错误冷却时间已重置"
	default:
		write(w, 200, response{Success: false, Message: "无效的冷却类型，必须是 password_login 或 account_error"})
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET `+column+`=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, account.ID); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, successMessage, map[string]any{"account_id": account.ID, "cooldown_type": typ})
}

func (s *Server) cookieRefreshTrigger(w http.ResponseWriter, r *http.Request) {
	account, message := s.loadRefreshAccount(r)
	if message != "" {
		write(w, 200, response{Success: false, Message: message})
		return
	}
	if active, remaining := cooldownState(account.LastPassword, time.Minute); active {
		write(w, 200, response{Success: false, Message: fmt.Sprintf("密码登录冷却中，请等待 %d 秒后再试", remaining)})
		return
	}
	if active, remaining := cooldownState(account.LastAccountErr, 5*time.Hour); active {
		write(w, 200, response{Success: false, Message: fmt.Sprintf("账密错误冷却中，请等待 %d 小时 %d 分钟后再试", remaining/3600, (remaining%3600)/60)})
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET last_password_login_time=CURRENT_TIMESTAMP WHERE id=?`, account.ID)
	renewed, names, err := renewCookie(r.Context(), account.Cookie, account.Proxy)
	if err != nil {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET last_account_error_time=CURRENT_TIMESTAMP WHERE id=?`, account.ID)
		payload, _ := json.Marshal(map[string]any{"batch_id": fmt.Sprintf("manual-%d", time.Now().UnixMilli()), "status": "failed", "error_message": err.Error(), "updated_cookie_count": 0})
		_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'cookie_refresh',?,'ERROR',?,?)`, account.OwnerID, account.AccountID, err.Error(), string(payload))
		write(w, 200, response{Success: false, Message: "Cookie刷新失败: " + err.Error()})
		return
	}
	_, err = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,status='active',disable_reason=NULL,last_refresh_at=CURRENT_TIMESTAMP,last_account_error_time=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, renewed, account.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if s.accountRestart != nil {
		s.accountRestart(account.AccountID)
	}
	payload, _ := json.Marshal(map[string]any{"batch_id": fmt.Sprintf("manual-%d", time.Now().UnixMilli()), "status": "success", "updated_cookie_count": len(names)})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'cookie_refresh',?,'INFO','Cookie刷新成功',?)`, account.OwnerID, account.AccountID, string(payload))
	ok(w, "Cookie刷新成功", map[string]any{"account_id": account.ID, "username": account.Username, "updated_fields": names})
}
