package api

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
)

const qrHost = "https://passport.goofish.com"

type qrLoginSession struct {
	mu              sync.RWMutex
	ID              string
	OwnerID         int64
	Status          string
	QRCodeURL       string
	FaceQRURL       string
	FaceQRContent   string
	VerificationURL string
	Error           string
	reportedStatus  string
	Cookie          string
	UNB             string
	Params          url.Values
	CreatedAt       time.Time
	ExpiresAt       time.Time
	client          *http.Client
	cookies         map[string]string
	processed       bool
	accountID       string
	isNew           bool
}

type qrLoginManager struct {
	mu       sync.RWMutex
	sessions map[string]*qrLoginSession
}

func newQRLoginManager() *qrLoginManager {
	return &qrLoginManager{sessions: map[string]*qrLoginSession{}}
}

func qrHeaders(r *http.Request) {
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	r.Header.Set("Accept", "application/json, text/plain, */*")
	r.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	r.Header.Set("Referer", qrHost+"/")
	r.Header.Set("Origin", qrHost)
}

func (m *qrLoginManager) generate(ownerID int64) (*qrLoginSession, error) {
	jar, _ := cookiejar.New(nil)
	s := &qrLoginSession{ID: uuid.NewString(), OwnerID: ownerID, Status: "waiting", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute), client: &http.Client{Timeout: 60 * time.Second, Jar: jar, Transport: proxy.Transport()}, cookies: map[string]string{}}
	if err := m.bootstrap(s); err != nil {
		return nil, err
	}
	if err := m.generateCode(s); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	go m.monitor(s)
	return s, nil
}

func (m *qrLoginManager) bootstrap(s *qrLoginSession) error {
	mtop := "https://h5api.m.goofish.com/h5/mtop.gaia.nodejs.gaia.idle.data.gw.v2.index.get/1.0/"
	req, _ := http.NewRequest(http.MethodGet, mtop, nil)
	qrHeaders(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("获取登录令牌失败: %w", err)
	}
	s.captureResponseCookies(resp)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	token := ""
	if value := s.cookies["m_h5_tk"]; value != "" {
		token = strings.Split(value, "_")[0]
	}
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	data := `{"bizScene":"home"}`
	h := md5.Sum([]byte(token + "&" + t + "&34839810&" + data))
	v := url.Values{"jsv": {"2.7.2"}, "appKey": {"34839810"}, "t": {t}, "sign": {hex.EncodeToString(h[:])}, "v": {"1.0"}, "type": {"originaljson"}, "dataType": {"json"}, "timeout": {"20000"}, "api": {"mtop.gaia.nodejs.gaia.idle.data.gw.v2.index.get"}, "data": {data}}
	req, _ = http.NewRequest(http.MethodPost, mtop+"?"+v.Encode(), strings.NewReader(""))
	qrHeaders(req)
	s.attachCookies(req)
	resp, err = s.client.Do(req)
	if err != nil {
		return fmt.Errorf("初始化登录会话失败: %w", err)
	}
	s.captureResponseCookies(resp)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	p := url.Values{"lang": {"zh_cn"}, "appName": {"xianyu"}, "appEntrance": {"web"}, "styleType": {"vertical"}, "bizParams": {""}, "notLoadSsoView": {"false"}, "notKeepLogin": {"false"}, "isMobile": {"false"}, "qrCodeFirst": {"false"}, "stie": {"77"}, "rnd": {fmt.Sprintf("%.16f", rand.Float64())}}
	req, _ = http.NewRequest(http.MethodGet, qrHost+"/mini_login.htm?"+p.Encode(), nil)
	qrHeaders(req)
	s.attachCookies(req)
	resp, err = s.client.Do(req)
	if err != nil {
		return fmt.Errorf("获取登录参数失败: %w", err)
	}
	s.captureResponseCookies(resp)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	re := regexp.MustCompile(`(?s)window\.viewData\s*=\s*(\{.*?\});`)
	match := re.FindSubmatch(body)
	if len(match) != 2 {
		return fmt.Errorf("获取登录参数失败")
	}
	var view struct {
		LoginFormData map[string]any `json:"loginFormData"`
	}
	if json.Unmarshal(match[1], &view) != nil || len(view.LoginFormData) == 0 {
		return fmt.Errorf("未找到loginFormData")
	}
	s.Params = url.Values{}
	for k, x := range view.LoginFormData {
		s.Params.Set(k, fmt.Sprint(x))
	}
	s.Params.Set("umidTag", "SERVER")
	return nil
}

func (m *qrLoginManager) generateCode(s *qrLoginSession) error {
	req, _ := http.NewRequest(http.MethodGet, qrHost+"/newlogin/qrcode/generate.do?"+s.Params.Encode(), nil)
	qrHeaders(req)
	s.attachCookies(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("生成二维码失败: %w", err)
	}
	defer resp.Body.Close()
	s.captureResponseCookies(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("读取二维码响应失败: %w", err)
	}
	var result struct {
		Content struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Data    struct {
				T           json.RawMessage `json:"t"`
				CK          string          `json:"ck"`
				CodeContent string          `json:"codeContent"`
			} `json:"data"`
		} `json:"content"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("二维码接口响应异常(HTTP %d): %s", resp.StatusCode, compactResponse(body))
	}
	if !result.Content.Success || result.Content.Data.CodeContent == "" {
		message := strings.TrimSpace(result.Content.Message)
		if message == "" {
			message = compactResponse(body)
		}
		return fmt.Errorf("获取登录二维码失败: %s", message)
	}
	timestamp := strings.Trim(strings.TrimSpace(string(result.Content.Data.T)), `"`)
	if timestamp == "" || timestamp == "null" {
		return fmt.Errorf("获取登录二维码失败: 响应缺少轮询时间戳")
	}
	s.Params.Set("t", timestamp)
	s.Params.Set("ck", result.Content.Data.CK)
	png, err := qrcode.Encode(result.Content.Data.CodeContent, qrcode.Low, 320)
	if err != nil {
		return err
	}
	s.QRCodeURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	return nil
}

func (m *qrLoginManager) monitor(s *qrLoginSession) {
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		terminal := s.Status == "success" || s.Status == "expired" || s.Status == "cancelled" || s.Status == "verification_required"
		expired := time.Now().After(s.ExpiresAt)
		s.mu.RUnlock()
		if terminal {
			return
		}
		if expired {
			s.mu.Lock()
			s.Status = "expired"
			s.mu.Unlock()
			return
		}
		req, _ := http.NewRequest(http.MethodPost, qrHost+"/newlogin/qrcode/query.do", strings.NewReader(s.Params.Encode()))
		qrHeaders(req)
		s.attachCookies(req)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}
		s.captureResponseCookies(resp)
		var result struct {
			Content struct {
				Data struct {
					QRCodeStatus      string `json:"qrCodeStatus"`
					IframeRedirect    bool   `json:"iframeRedirect"`
					IframeRedirectURL string `json:"iframeRedirectUrl"`
				} `json:"data"`
			} `json:"content"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result)
		resp.Body.Close()
		if err != nil {
			continue
		}
		s.mu.Lock()
		switch result.Content.Data.QRCodeStatus {
		case "NEW":
			s.Status = "waiting"
		case "SCANED":
			s.Status = "scanned"
		case "EXPIRED":
			s.Status = "expired"
		case "CONFIRMED":
			if result.Content.Data.IframeRedirect {
				s.Status = "verification_required"
				s.VerificationURL = result.Content.Data.IframeRedirectURL
				s.ExpiresAt = time.Now().Add(5 * time.Minute)
				verificationURL := s.VerificationURL
				go m.runFaceVerification(s, verificationURL)
			} else {
				s.Status = "success"
				s.captureCookies(req.URL)
			}
		default:
			if result.Content.Data.QRCodeStatus != "" {
				s.Status = "cancelled"
			}
		}
		s.mu.Unlock()
	}
}

func (s *qrLoginSession) captureCookies(u *url.URL) {
	if u != nil && s.client.Jar != nil {
		for _, c := range s.client.Jar.Cookies(u) {
			s.cookies[c.Name] = c.Value
		}
	}
	parts := make([]string, 0, len(s.cookies))
	for name, value := range s.cookies {
		parts = append(parts, name+"="+value)
		if name == "unb" {
			s.UNB = value
		}
	}
	slices.Sort(parts)
	s.Cookie = strings.Join(parts, "; ")
}

func (s *qrLoginSession) captureResponseCookies(resp *http.Response) {
	if s.cookies == nil {
		s.cookies = map[string]string{}
	}
	for _, c := range resp.Cookies() {
		s.cookies[c.Name] = c.Value
		if c.Name == "unb" {
			s.UNB = c.Value
		}
	}
}

func (s *qrLoginSession) attachCookies(req *http.Request) {
	for name, value := range s.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
}

func compactResponse(body []byte) string {
	value := strings.TrimSpace(string(body))
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 300 {
		value = value[:300] + "..."
	}
	if value == "" {
		return "空响应"
	}
	return value
}

func (m *qrLoginManager) get(id string) *qrLoginSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

func (s *Server) qrLoginGenerate(w http.ResponseWriter, r *http.Request) {
	sess, err := s.qrLogin.generate(current(r).ID)
	if err != nil {
		write(w, http.StatusOK, response{Success: false, Message: err.Error()})
		return
	}
	ok(w, "二维码生成成功", map[string]any{"session_id": sess.ID, "qr_code_url": sess.QRCodeURL})
}

func (s *Server) qrLoginStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.qrLogin.get(chi.URLParam(r, "session_id"))
	u := current(r)
	if sess == nil || sess.OwnerID != u.ID {
		ok(w, "查询成功", map[string]any{"status": "not_found", "session_id": chi.URLParam(r, "session_id")})
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if time.Now().After(sess.ExpiresAt) && sess.Status != "success" && sess.Status != "failed" {
		sess.Status = "expired"
	}
	s.reportQRLoginState(r.Context(), sess)
	if sess.Status == "success" && !sess.processed {
		accountID := sess.UNB
		if accountID == "" {
			accountID = "qr-" + strings.ReplaceAll(sess.ID[:12], "-", "")
		}
		var existing int64
		err := s.db.QueryRowContext(r.Context(), `SELECT id FROM xy_accounts WHERE account_id=? AND owner_id=?`, accountID, u.ID).Scan(&existing)
		if err == sql.ErrNoRows && u.AccountLimit.Valid {
			var count int64
			_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=?`, u.ID).Scan(&count)
			if count >= u.AccountLimit.Int64 {
				ok(w, "账号数量已达上限", map[string]any{"status": "failed", "message": "账号数量已达上限", "account_info": map[string]any{"account_id": accountID, "is_new_account": true}})
				return
			}
		}
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO xy_accounts(owner_id,account_id,unb,cookie,login_method,status,last_login_at) VALUES(?,?,?,?,?,'active',CURRENT_TIMESTAMP) ON CONFLICT(account_id) DO UPDATE SET cookie=excluded.cookie,unb=excluded.unb,login_method='qr',status='active',last_login_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE xy_accounts.owner_id=excluded.owner_id`, u.ID, accountID, nullString(sess.UNB), sess.Cookie, "qr")
		if err != nil {
			ok(w, "保存账号失败: "+err.Error(), nil)
			return
		}
		sess.processed = true
		sess.accountID = accountID
		sess.isNew = existing == 0
		if s.accountRestart != nil {
			go s.accountRestart(accountID)
		}
	}
	if sess.processed {
		s.reportQRLoginState(r.Context(), sess)
		ok(w, "扫码登录成功", map[string]any{"status": "success", "account_info": map[string]any{"account_id": sess.accountID, "is_new_account": sess.isNew}})
		return
	}
	data := map[string]any{"status": sess.Status, "session_id": sess.ID}
	if sess.Status == "verification_required" || sess.Status == "failed" {
		data["message"] = "需要人脸验证，请使用手机闲鱼完成验证"
		data["verification_url"] = sess.VerificationURL
		data["face_qr_url"] = sess.FaceQRURL
		if sess.Error != "" {
			data["message"] = sess.Error
		}
	}
	ok(w, "查询成功", data)
}

func (s *Server) reportQRLoginState(ctx context.Context, sess *qrLoginSession) {
	state := sess.Status
	if sess.processed {
		state = "saved"
	}
	if state == sess.reportedStatus {
		return
	}
	sess.reportedStatus = state
	payload, _ := json.Marshal(map[string]any{
		"session_id": sess.ID,
		"status":     state,
		"error":      sess.Error,
		"has_unb":    sess.UNB != "",
		"has_cookie": sess.Cookie != "",
		"account_id": sess.accountID,
	})
	level := "INFO"
	message := "二维码登录状态: " + state
	if state == "failed" || state == "expired" || state == "cancelled" {
		level = "WARNING"
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'qr_login',?,?,?,?)`, sess.OwnerID, nullString(sess.accountID), level, message, string(payload))
}

func (s *Server) qrLoginCookie(w http.ResponseWriter, r *http.Request) {
	sess := s.qrLogin.get(chi.URLParam(r, "session_id"))
	if sess == nil || sess.OwnerID != current(r).ID {
		write(w, http.StatusOK, response{Success: false, Message: "Cookie不存在或会话未完成"})
		return
	}
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.Status != "success" || sess.Cookie == "" {
		write(w, http.StatusOK, response{Success: false, Message: "Cookie不存在或会话未完成"})
		return
	}
	ok(w, "获取Cookie成功", map[string]any{"cookies": sess.Cookie, "unb": sess.UNB})
}
