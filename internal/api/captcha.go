package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func randomCode(alphabet string, length int) string {
	raw := make([]byte, length)
	_, _ = rand.Read(raw)
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(raw)
}

func codeHash(sessionID, code, secret string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + strings.ToUpper(code) + ":" + secret))
	return hex.EncodeToString(sum[:])
}

func captchaSVG(code string) string {
	var text strings.Builder
	for i, char := range code {
		text.WriteString(fmt.Sprintf(`<text x="%d" y="30" font-size="27" font-family="sans-serif" font-weight="bold" fill="#%02x%02x%02x" transform="rotate(%d %d 20)">%s</text>`, 8+i*27, 30+i%3, 35+i*19, 65+i*13, -8+i*5, 8+i*27, html.EscapeString(string(char))))
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40" viewBox="0 0 120 40"><rect width="120" height="40" fill="#f7f7f7"/><path d="M0 8L120 31M0 34L120 7M6 20L115 18" stroke="#9aa0a6" stroke-width="1" opacity=".65"/>` + text.String() + `</svg>`
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

func (s *Server) captchaGenerate(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if decode(r, &p) != nil || strings.TrimSpace(p.SessionID) == "" || len(p.SessionID) > 160 {
		fail(w, http.StatusBadRequest, "session_id无效")
		return
	}
	code := randomCode("23456789ABCDEFGHJKLMNPQRSTUVWXYZ", 4)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO captcha_sessions(session_id,code_hash,verified,attempts,expires_at) VALUES(?,?,0,0,datetime('now','+5 minutes')) ON CONFLICT(session_id) DO UPDATE SET code_hash=excluded.code_hash,verified=0,attempts=0,expires_at=excluded.expires_at,created_at=CURRENT_TIMESTAMP`, p.SessionID, codeHash(p.SessionID, code, s.cfg.JWTSecret))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "验证码生成成功", map[string]any{"captcha_image": captchaSVG(code), "session_id": p.SessionID})
}

func (s *Server) captchaVerify(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SessionID string `json:"session_id"`
		Code      string `json:"captcha_code"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	var expected string
	var attempts int
	err := s.db.QueryRowContext(r.Context(), `SELECT code_hash,attempts FROM captcha_sessions WHERE session_id=? AND expires_at>CURRENT_TIMESTAMP`, p.SessionID).Scan(&expected, &attempts)
	if err != nil || attempts >= 5 || expected != codeHash(p.SessionID, p.Code, s.cfg.JWTSecret) {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE captcha_sessions SET attempts=attempts+1 WHERE session_id=?`, p.SessionID)
		write(w, 200, response{Success: false, Message: "验证码错误或已过期"})
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE captcha_sessions SET verified=1 WHERE session_id=?`, p.SessionID)
	ok(w, "验证码验证成功", nil)
}

func (s *Server) emailCodeSend(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Email   string `json:"email"`
		Purpose string `json:"type"`
	}
	if decode(r, &p) != nil || !emailPattern.MatchString(p.Email) {
		fail(w, 400, "邮箱格式无效")
		return
	}
	if p.Purpose == "" {
		p.Purpose = "register"
	}
	code := randomCode("0123456789", 6)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO verification_codes(email,code,purpose,expires_at) VALUES(?,?,?,datetime('now','+10 minutes'))`, strings.ToLower(p.Email), code, p.Purpose)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	cfg, sendErr := loadSMTPSettings(r.Context(), s.db)
	if sendErr == nil {
		subject, body := verificationEmail(code, p.Purpose)
		sendErr = sendSMTP(r.Context(), cfg, p.Email, subject, body)
	}
	if sendErr != nil {
		_, _ = s.db.ExecContext(r.Context(), `DELETE FROM verification_codes WHERE email=? AND code=? AND purpose=?`, strings.ToLower(p.Email), code, p.Purpose)
		s.log.Warn("send verification email", "email", p.Email, "error", sendErr)
		write(w, 200, response{Success: false, Message: sendErr.Error()})
		return
	}
	s.log.Info("email verification code sent", "email", p.Email, "purpose", p.Purpose, "expires_in", 600*time.Second)
	ok(w, "验证码已发送", nil)
}

func (s *Server) emailCodeVerify(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Email   string `json:"email"`
		Code    string `json:"code"`
		Purpose string `json:"type"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if p.Purpose == "" {
		p.Purpose = "register"
	}
	var id int64
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM verification_codes WHERE email=? AND code=? AND purpose=? AND used=0 AND expires_at>CURRENT_TIMESTAMP ORDER BY id DESC LIMIT 1`, strings.ToLower(p.Email), p.Code, p.Purpose).Scan(&id)
	if err != nil {
		write(w, 200, response{Success: false, Message: "验证码无效或已过期"})
		return
	}
	ok(w, "验证码有效", nil)
}
