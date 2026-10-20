package xianyu

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

func ConnectionFailureReason(err error) string {
	if err == nil {
		return "connection_closed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case containsAny(message, []string{"FAIL_SYS_USER_VALIDATE", "RGV587", "captcha", "WUA_IS_MACHINE", "punish"}):
		return "platform_verification_required"
	case containsAny(message, []string{"FAIL_SYS_SESSION_EXPIRED", "FAIL_SYS_SESSION_INVALID"}):
		return "session_expired"
	case strings.Contains(message, "cookie缺少unb"):
		return "invalid_cookie"
	case containsAny(message, tokenExpired):
		return "token_expired"
	default:
		return "connection_failed"
	}
}

func (m *Manager) reloadConnectionAccount(ctx context.Context, a *Account) error {
	return m.db.QueryRowContext(ctx, `SELECT owner_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE id=? AND status='active'`, a.ID).Scan(&a.OwnerID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
}

func (m *Manager) connectionDeviceID(ctx context.Context, a Account) (string, error) {
	uid := ParseCookies(a.Cookie)["unb"]
	if uid == "" {
		return "", errors.New("Cookie缺少unb")
	}
	var device string
	if err := m.db.QueryRowContext(ctx, `SELECT device_id FROM xy_token_cache WHERE user_id=?`, uid).Scan(&device); err == nil && device != "" {
		return device, nil
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO xy_token_cache(user_id,token,device_id,expire_at) VALUES(?,'',?,'1970-01-01 00:00:00') ON CONFLICT(user_id) DO UPDATE SET device_id=COALESCE(NULLIF(xy_token_cache.device_id,''),excluded.device_id)`, uid, DeviceID(uid))
	if err != nil {
		return "", err
	}
	err = m.db.QueryRowContext(ctx, `SELECT device_id FROM xy_token_cache WHERE user_id=?`, uid).Scan(&device)
	return device, err
}

func (m *Manager) fetchConnectionToken(ctx context.Context, a Account, device string) (string, map[string]string, error) {
	if m.tokenFetcher != nil {
		return m.tokenFetcher(ctx, a, device)
	}
	return fetchToken(ctx, a, device)
}

func fetchToken(ctx context.Context, a Account, device string) (string, map[string]string, error) {
	proxy := ""
	if p := proxyURL(a); p != nil {
		proxy = p.String()
	}
	result := MTOPCall(ctx, MTOPOptions{
		API: "mtop.taobao.idlemessage.pc.login.token", Version: "1.0", Cookie: a.Cookie, Proxy: proxy,
		Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/",
		Data:        map[string]any{"appKey": AppKey, "deviceId": device},
		ExtraParams: map[string]string{"spm_cnt": "a21ybx.im.0.0"},
	})
	updates := map[string]string{}
	original := ParseCookies(a.Cookie)
	for key, value := range ParseCookies(result.Cookie) {
		if value != original[key] {
			updates[key] = value
		}
	}
	if !result.Success {
		return "", updates, fmt.Errorf("token获取失败: %s", result.Error)
	}
	data, _ := result.Response["data"].(map[string]any)
	token, _ := data["accessToken"].(string)
	if strings.TrimSpace(token) == "" {
		return "", updates, errors.New("token响应缺少accessToken")
	}
	return token, updates, nil
}

func (m *Manager) saveConnectionCookies(ctx context.Context, a *Account, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	merged := mergeCookies(a.Cookie, updates)
	// A user may have replaced the login while a remote request was in flight.
	_, err := m.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND cookie=?`, merged, a.ID, a.Cookie)
	if err != nil {
		return err
	}
	return m.reloadConnectionAccount(ctx, a)
}

func (m *Manager) connectionBlocked(a Account) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	stamp, blocked := m.blocked[a.AccountID]
	return blocked && stamp == sha256.Sum256([]byte(a.Cookie))
}

func (m *Manager) waitForConnectionAccountUpdate(ctx context.Context, a Account) error {
	m.mu.Lock()
	if m.blocked == nil {
		m.blocked = map[string][32]byte{}
	}
	m.blocked[a.AccountID] = sha256.Sum256([]byte(a.Cookie))
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.blocked, a.AccountID); m.mu.Unlock() }()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		fresh := a
		if err := m.reloadConnectionAccount(ctx, &fresh); err != nil {
			return err
		}
		if fresh.Cookie != a.Cookie {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
