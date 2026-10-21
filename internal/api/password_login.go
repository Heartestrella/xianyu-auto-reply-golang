package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

const passwordModulus = "d3bcef1f00424f3261c89323fa8cdfa12bbac400d9fe8bb627e8d27a44bd5d59dce559135d678a8143beb5b8d7056c4e1f89c4e1f152470625b7b41944a97f02da6f605a49a93ec6eb9cbaf2e7ac2b26a354ce69eb265953d2c29e395d6d8c1cdb688978551aa0f7521f290035fad381178da0bea8f9e6adce39020f513133fb"

type passwordLoginSession struct {
	OwnerID                                            int64
	AccountID, Account, Password                       string
	ShowBrowser                                        bool
	Status, Message, Error, VerificationURL, FaceQRURL string
	IsNew                                              *bool
	Created, Terminal                                  time.Time
	Cancel                                             context.CancelFunc
}

// ExecuteScheduledLoginRenewal validates active sessions with the same MTOP
// login-user endpoint as the original scheduler. Expired sessions with stored
// credentials are handed to the Go password-login flow asynchronously.
func (s *Server) ExecuteScheduledLoginRenewal(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(username,''),COALESCE(login_password,''),show_browser,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE status='active' ORDER BY id`)
	if err != nil {
		return err
	}
	type account struct {
		id, owner                     int64
		accountID, cookie, user, pass string
		show                          bool
		platform                      platformAccount
	}
	accounts := []account{}
	for rows.Next() {
		var a account
		if rows.Scan(&a.id, &a.owner, &a.accountID, &a.cookie, &a.user, &a.pass, &a.show, &a.platform.ProxyType, &a.platform.ProxyHost, &a.platform.ProxyPort, &a.platform.ProxyUser, &a.platform.ProxyPass) == nil {
			a.platform.ID, a.platform.OwnerID, a.platform.AccountID, a.platform.Cookie = a.id, a.owner, a.accountID, a.cookie
			accounts = append(accounts, a)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	batchID := fmt.Sprintf("login-%d", time.Now().UnixMilli())
	failures := []string{}
	for _, a := range accounts {
		result := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idlemessage.pc.loginuser.get", Version: "1.0", Cookie: a.cookie, Proxy: accountProxy(a.platform), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/", Data: map[string]any{}})
		if result.Cookie != "" && result.Cookie != a.cookie {
			a.cookie = result.Cookie
			_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.cookie, a.id)
		}
		status, level, message := "success", "INFO", "登录状态有效"
		if !result.Success {
			message = firstNonemptyAPI(result.Error, "登录状态检查失败")
			if result.AccountInvalid {
				status, level = "session_expired", "WARN"
				if a.user != "" && a.pass != "" && s.startScheduledPasswordLogin(a.owner, a.accountID, a.user, a.pass, a.show) {
					message += "，已触发密码重新登录"
				} else if a.user == "" || a.pass == "" {
					message += "，未配置登录账号或密码"
				}
			} else {
				status, level = "failed", "ERROR"
				failures = append(failures, a.accountID+": "+message)
			}
		}
		payload, _ := json.Marshal(map[string]any{"batch_id": batchID, "status": status, "account_invalid": result.AccountInvalid})
		_, _ = s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'login_renew',?,?,?,?)`, a.owner, a.accountID, level, message, string(payload))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) startScheduledPasswordLogin(owner int64, accountID, username, password string, show bool) bool {
	s.passwordMu.Lock()
	s.cleanupPasswordLoginsLocked()
	for _, current := range s.passwordLogins {
		if current.OwnerID == owner && current.AccountID == accountID && (current.Status == "processing" || current.Status == "verification_required") {
			s.passwordMu.Unlock()
			return false
		}
	}
	id := "scheduled_" + randomHex(16)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	session := &passwordLoginSession{OwnerID: owner, AccountID: accountID, Account: username, Password: password, ShowBrowser: show, Status: "processing", Message: "定时登录续期处理中", Created: time.Now(), Cancel: cancel}
	s.passwordLogins[id] = session
	s.passwordMu.Unlock()
	go s.runPasswordLogin(ctx, id, session)
	return true
}

func (s *Server) passwordLoginStart(w http.ResponseWriter, r *http.Request) {
	var p struct {
		AccountID   string `json:"account_id"`
		Account     string `json:"account"`
		Password    string `json:"password"`
		ShowBrowser bool   `json:"show_browser"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	p.AccountID = strings.TrimSpace(p.AccountID)
	p.Account = strings.TrimSpace(p.Account)
	// Mobile clients historically omit account when account_id is the login name.
	if p.Account == "" {
		p.Account = p.AccountID
	}
	if p.AccountID == "" || p.Account == "" || p.Password == "" {
		write(w, 200, map[string]any{"success": false, "message": "账号ID、登录账号和密码不能为空"})
		return
	}
	u := current(r)
	var exists int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, p.AccountID).Scan(&exists)
	if exists == 0 && u.AccountLimit.Valid {
		var count int64
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=?`, u.ID).Scan(&count)
		if count >= u.AccountLimit.Int64 {
			write(w, 200, map[string]any{"success": false, "message": "账号数量已达到上限"})
			return
		}
	}
	s.passwordMu.Lock()
	s.cleanupPasswordLoginsLocked()
	for id, session := range s.passwordLogins {
		if session.OwnerID == u.ID && session.AccountID == p.AccountID && (session.Status == "processing" || session.Status == "verification_required") {
			s.passwordMu.Unlock()
			write(w, 200, map[string]any{"success": true, "session_id": id, "status": session.Status, "message": session.Message})
			return
		}
	}
	id := "pl_" + randomHex(16)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	session := &passwordLoginSession{OwnerID: u.ID, AccountID: p.AccountID, Account: p.Account, Password: p.Password, ShowBrowser: p.ShowBrowser, Status: "processing", Message: "登录处理中，请稍候...", Created: time.Now(), Cancel: cancel}
	s.passwordLogins[id] = session
	s.passwordMu.Unlock()
	go s.runPasswordLogin(ctx, id, session)
	write(w, 200, map[string]any{"success": true, "session_id": id, "status": "processing", "message": "登录任务已启动，请等待..."})
}

func (s *Server) passwordLoginCheck(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "session_id")
	u := current(r)
	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	s.cleanupPasswordLoginsLocked()
	x := s.passwordLogins[id]
	if x == nil || x.OwnerID != u.ID {
		write(w, 200, map[string]any{"status": "not_found", "message": "会话不存在或已过期"})
		return
	}
	if (x.Status == "success" || x.Status == "failed") && x.Terminal.IsZero() {
		x.Terminal = time.Now()
	}
	write(w, 200, map[string]any{"status": x.Status, "message": x.Message, "error": emptyNil(x.Error), "verification_url": emptyNil(x.VerificationURL), "face_qr_url": emptyNil(x.FaceQRURL), "account_id": x.AccountID, "is_new_account": x.IsNew})
}

func (s *Server) passwordLoginCancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "session_id")
	u := current(r)
	s.passwordMu.Lock()
	x := s.passwordLogins[id]
	if x == nil || x.OwnerID != u.ID {
		s.passwordMu.Unlock()
		write(w, 200, map[string]any{"success": false, "message": "会话不存在"})
		return
	}
	x.Cancel()
	delete(s.passwordLogins, id)
	s.passwordMu.Unlock()
	write(w, 200, map[string]any{"success": true, "message": "登录会话已取消"})
}
func emptyNil(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func (s *Server) cleanupPasswordLoginsLocked() {
	now := time.Now()
	for id, x := range s.passwordLogins {
		if (!x.Terminal.IsZero() && now.Sub(x.Terminal) > 30*time.Second) || now.Sub(x.Created) > time.Hour {
			x.Cancel()
			delete(s.passwordLogins, id)
		}
	}
}

func (s *Server) setPasswordState(id, status, message, errText, verify string) {
	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	if x := s.passwordLogins[id]; x != nil {
		x.Status = status
		x.Message = message
		x.Error = errText
		x.VerificationURL = verify
		if status == "success" || status == "failed" {
			x.Terminal = time.Now()
		}
	}
}

func (s *Server) runPasswordLogin(ctx context.Context, id string, x *passwordLoginSession) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar, Transport: proxy.Transport()}
	form, err := passwordLoginForm(x.Account, x.Password)
	if err != nil {
		s.setPasswordState(id, "failed", "", err.Error(), "")
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		body, cookies, err := postPasswordLogin(ctx, client, form)
		if err != nil {
			s.setPasswordState(id, "failed", "", "登录失败："+err.Error(), "")
			return
		}
		branch, message, verify := classifyPasswordLogin(body)
		switch branch {
		case "slider":
			s.setPasswordState(id, "processing", fmt.Sprintf("正在处理登录滑块（第 %d/3 次）", attempt+1), "", "")
			solved, solveErr := s.solvePasswordSlider(ctx, verify)
			if solveErr != nil {
				s.setPasswordState(id, "verification_required", "需要完成安全验证", solveErr.Error(), verify)
				return
			}
			u, _ := url.Parse("https://passport.goofish.com/")
			extra := []*http.Cookie{}
			for k, v := range solved {
				extra = append(extra, &http.Cookie{Name: k, Value: v, Domain: ".goofish.com", Path: "/"})
			}
			jar.SetCookies(u, extra)
			continue
		case "face":
			s.setPasswordState(id, "verification_required", "需要完成人脸验证", "", verify)
			return
		case "fail":
			s.setPasswordState(id, "failed", "", message, "")
			return
		case "success":
			_, _ = client.Get("https://www.goofish.com/")
			cookieString := jarCookieString(jar)
			unb := cookieValue(jar, "unb")
			if unb == "" {
				unb = cookies["unb"]
			}
			if unb == "" {
				s.setPasswordState(id, "failed", "", "登录成功但未获取到账号标识(unb)", "")
				return
			}
			isNew, saveErr := s.savePasswordLogin(ctx, x, cookieString, unb)
			if saveErr != nil {
				s.setPasswordState(id, "failed", "", saveErr.Error(), "")
				return
			}
			s.passwordMu.Lock()
			if live := s.passwordLogins[id]; live != nil {
				live.IsNew = &isNew
			}
			s.passwordMu.Unlock()
			msg := "账号登录成功"
			if isNew {
				msg = "新账号登录成功"
			}
			s.setPasswordState(id, "success", msg, "", "")
			return
		default:
			s.setPasswordState(id, "failed", "", "登录响应异常，请稍后重试或改用扫码登录", "")
			return
		}
	}
	s.setPasswordState(id, "failed", "", "协议登录未通过（滑块多次未通过）", "")
}

func passwordLoginForm(account, password string) (url.Values, error) {
	n := new(big.Int)
	if _, ok := n.SetString(passwordModulus, 16); !ok {
		return nil, errors.New("登录公钥无效")
	}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, &rsa.PublicKey{N: n, E: 65537}, []byte(password))
	if err != nil {
		return nil, err
	}
	v := url.Values{"keepLogin": {"false"}, "isIframe": {"true"}, "documentReferer": {"https://www.goofish.com/"}, "defaultView": {"password"}, "appName": {"xianyu"}, "appEntrance": {"web"}, "bizParams": {"taobaoBizLoginFrom=web&renderRefer=https%3A%2F%2Fwww.goofish.com%2F"}, "mainPage": {"false"}, "isMobile": {"false"}, "lang": {"zh_CN"}, "returnUrl": {""}, "fromSite": {"77"}, "weiBoMpBridge": {""}, "jsVersion": {"0.10.36"}, "screenPixel": {"1920x1080"}, "navlanguage": {"zh-CN"}, "navUserAgent": {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/146 Safari/537.36"}, "navPlatform": {"Win32"}, "umidGetStatusVal": {"255"}, "umidTag": {"SERVER"}, "loginId": {account}, "password2": {hex.EncodeToString(encrypted)}}
	return v, nil
}

func postPasswordLogin(ctx context.Context, c *http.Client, form url.Values) (map[string]any, map[string]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://passport.goofish.com/newlogin/login.do?appName=xianyu&fromSite=77", strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://passport.goofish.com")
	req.Header.Set("Referer", "https://passport.goofish.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/146 Safari/537.36")
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, nil, err
	}
	out := map[string]any{}
	if json.Unmarshal(b, &out) != nil {
		return nil, nil, errors.New("登录响应解析失败")
	}
	cs := map[string]string{}
	for _, c := range resp.Cookies() {
		cs[c.Name] = c.Value
	}
	return out, cs, nil
}

func classifyPasswordLogin(body map[string]any) (string, string, string) {
	if ret, ok := body["ret"].([]any); ok {
		for _, v := range ret {
			if strings.Contains(fmt.Sprint(v), "FAIL_SYS_USER_VALIDATE") {
				if u := firstMapString(childMap(body, "data"), "url"); u != "" {
					return "slider", "", u
				}
			}
		}
	}
	data := childMap(childMap(body, "content"), "data")
	if mapBool(data["iframeRedirect"]) {
		if u := firstMapString(data, "iframeRedirectUrl"); u != "" {
			return "face", "", u
		}
	}
	if strings.EqualFold(firstMapString(data, "loginResult"), "success") || strings.EqualFold(firstMapString(data, "st"), "success") {
		return "success", "", ""
	}
	if m := firstMapString(data, "titleMsg"); m != "" {
		return "fail", m, ""
	}
	return "unknown", "", ""
}

func (s *Server) solvePasswordSlider(ctx context.Context, punish string) (map[string]string, error) {
	var endpoint, key string
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key='password_login.remote_url'`).Scan(&endpoint)
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key='password_login.remote_secret_key'`).Scan(&key)
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(key) == "" {
		return nil, errors.New("未配置协议登录远程接口，无法过滑块")
	}
	raw, _ := json.Marshal(map[string]any{"type": "x5sec_ali", "data": map[string]any{"url": punish}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)
	resp, err := proxy.Client(90 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil {
		return nil, errors.New("远程接口响应无效")
	}
	if resp.StatusCode != 200 || out["success"] != true {
		return nil, fmt.Errorf("远程过滑块未通过: %s", firstMapString(out, "message"))
	}
	d := childMap(out, "data")
	cookies := map[string]string{}
	for _, k := range []string{"x5sec", "bx-pp", "bx_et"} {
		if v := firstMapString(d, k); v != "" {
			cookies[k] = v
		}
	}
	if cookies["x5sec"] == "" {
		return nil, errors.New("远程接口返回成功但缺少 x5sec")
	}
	return cookies, nil
}

func jarCookieString(j *cookiejar.Jar) string {
	domains := []string{"https://www.goofish.com/", "https://passport.goofish.com/", "https://h5api.m.goofish.com/"}
	m := map[string]string{}
	for _, raw := range domains {
		u, _ := url.Parse(raw)
		for _, c := range j.Cookies(u) {
			m[c.Name] = c.Value
		}
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}
func cookieValue(j *cookiejar.Jar, name string) string {
	for _, raw := range []string{"https://www.goofish.com/", "https://passport.goofish.com/"} {
		u, _ := url.Parse(raw)
		for _, c := range j.Cookies(u) {
			if c.Name == name {
				return c.Value
			}
		}
	}
	return ""
}

func (s *Server) savePasswordLogin(ctx context.Context, x *passwordLoginSession, cookie, unb string) (bool, error) {
	var owner int64
	err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM xy_accounts WHERE account_id=?`, x.AccountID).Scan(&owner)
	isNew := errors.Is(err, sql.ErrNoRows)
	if err == nil && owner != x.OwnerID {
		return false, errors.New("该闲鱼账号已被其他用户绑定")
	}
	if err != nil && !isNew {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO xy_accounts(owner_id,account_id,unb,cookie,login_method,status,username,login_password,show_browser,last_login_at,last_refresh_at) VALUES(?,?,?,?,?,'active',?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP) ON CONFLICT(account_id) DO UPDATE SET unb=excluded.unb,cookie=excluded.cookie,login_method='password',status='active',username=excluded.username,login_password=excluded.login_password,show_browser=excluded.show_browser,last_login_at=CURRENT_TIMESTAMP,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE xy_accounts.owner_id=excluded.owner_id`, x.OwnerID, x.AccountID, unb, cookie, "password", x.Account, x.Password, x.ShowBrowser)
	if err == nil && s.accountRestart != nil {
		s.accountRestart(x.AccountID)
	}
	return isNew, err
}
