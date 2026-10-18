package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/auth"
)

func (s *Server) defaultReplyRoutes(r chi.Router) {
	r.Get("/", s.defaultReplies)
	r.Get("/{accountID}", s.defaultReply)
	r.Put("/{accountID}", s.defaultReplyPut)
	r.Delete("/{accountID}", s.defaultReplyDelete)
	r.Post("/{accountID}/clear-records", s.defaultReplyRecordsClear)
}
func (s *Server) defaultReplyRecordsClear(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM default_reply_records WHERE owner_id=? AND account_id=? AND item_id=''`, ownerID, accountID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	deleted, _ := res.RowsAffected()
	write(w, 200, map[string]any{"message": "默认回复记录已清空", "success": true, "deleted_count": deleted})
}
func (s *Server) defaultReplies(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query, args := `SELECT account_id,data FROM default_replies`, []any{}
	if u.Role != "ADMIN" {
		query += ` WHERE owner_id=?`
		args = append(args, u.ID)
	}
	query += ` ORDER BY id`
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var id, data string
		_ = rows.Scan(&id, &data)
		var m map[string]any
		_ = json.Unmarshal([]byte(data), &m)
		out[id] = m
	}
	write(w, 200, out)
}
func (s *Server) defaultReply(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	var data string
	e := s.db.QueryRowContext(r.Context(), `SELECT data FROM default_replies WHERE owner_id=? AND account_id=? ORDER BY id DESC LIMIT 1`, ownerID, accountID).Scan(&data)
	if e == sql.ErrNoRows {
		write(w, 200, map[string]any{"enabled": false, "reply_type": "text", "reply_content": "", "reply_image": "", "api_url": "", "api_timeout": 80, "reply_once": false})
		return
	}
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(data), &m)
	write(w, 200, m)
}
func (s *Server) defaultReplyPut(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if firstString(p, "reply_type") == "api" {
		if message := validateDefaultReplyAPIURL(r.Context(), firstString(p, "api_url")); message != "" {
			write(w, 200, response{Success: false, Message: message})
			return
		}
		timeout := intNumber(p["api_timeout"])
		if timeout < 1 {
			timeout = 1
		}
		if timeout > 120 {
			timeout = 120
		}
		p["api_timeout"] = timeout
	}
	if firstString(p, "reply_type") == "external_contact" {
		if message := s.validateExternalContact(r.Context(), ownerID, p); message != "" {
			write(w, 200, response{Success: false, Message: message})
			return
		}
	}
	b, _ := json.Marshal(p)
	reply, _ := p["reply_content"].(string)
	enabled, _ := p["enabled"].(bool)
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM default_replies WHERE owner_id=? AND account_id=?`, ownerID, accountID)
	_, e = tx.ExecContext(r.Context(), `INSERT INTO default_replies(owner_id,account_id,reply,enabled,data) VALUES(?,?,?,?,?)`, ownerID, accountID, reply, enabled, string(b))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	_ = tx.Commit()
	write(w, 200, map[string]any{"success": true, "message": "默认回复更新成功", "enabled": enabled, "reply_once": p["reply_once"]})
}

func (s *Server) validateExternalContact(ctx context.Context, ownerID int64, values map[string]any) string {
	configured := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM user_settings WHERE owner_id=? AND key IN ('location_chat.remote_url','location_chat.remote_secret_key')`, ownerID)
	if err == nil {
		for rows.Next() {
			var key, value string
			_ = rows.Scan(&key, &value)
			configured[key] = strings.TrimSpace(value)
		}
		_ = rows.Close()
	}
	remoteURL := configured["location_chat.remote_url"]
	if remoteURL == "" {
		return "请先到个人设置的远程URL配置中填写位置聊天远程URL"
	}
	if configured["location_chat.remote_secret_key"] == "" {
		return "请先到个人设置的远程URL配置中填写位置聊天远程URL和秘钥"
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "位置聊天远程URL格式不正确"
	}
	if port := parsed.Port(); port != "" && port != "80" && port != "443" {
		return "远程URL仅允许使用 80 或 443 端口"
	}
	name, title, subtitle := firstString(values, "location_name"), firstString(values, "location_title"), firstString(values, "location_subtitle")
	if name == "" {
		return "请选择定位信息"
	}
	if len([]rune(name)) > 255 {
		return "定位名称不能超过255个字符"
	}
	if title == "" {
		return "请输入位置标题"
	}
	if len([]rune(title)) > 128 || len([]rune(subtitle)) > 255 {
		return "位置标题或副标题长度超出限制"
	}
	longitude, longErr := strconv.ParseFloat(firstString(values, "location_longitude"), 64)
	latitude, latErr := strconv.ParseFloat(firstString(values, "location_latitude"), 64)
	if longErr != nil || latErr != nil || math.IsInf(longitude, 0) || math.IsNaN(longitude) || math.IsInf(latitude, 0) || math.IsNaN(latitude) {
		return "定位信息缺少有效经纬度，请重新选择地址"
	}
	if longitude < -180 || longitude > 180 {
		return "经度必须在-180至180之间"
	}
	if latitude < -90 || latitude > 90 {
		return "纬度必须在-90至90之间"
	}
	return ""
}

func validateDefaultReplyAPIURL(ctx context.Context, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "API地址不能为空"
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "API地址必须以 http:// 或 https:// 开头"
	}
	if parsed.Hostname() == "" {
		return "API地址缺少主机名"
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return ""
	}
	for _, address := range addresses {
		if !address.IP.IsGlobalUnicast() || address.IP.IsPrivate() || address.IP.IsLoopback() || address.IP.IsLinkLocalUnicast() || address.IP.IsUnspecified() {
			return "API地址不允许指向内网或回环地址"
		}
	}
	return ""
}
func (s *Server) defaultReplyDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM default_replies WHERE owner_id=? AND account_id=?`, ownerID, accountID)
	affected(w, res, e, "默认回复删除成功")
}

func (s *Server) accountOwned(r *http.Request, ownerID int64, accountID string) bool {
	var exists int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, ownerID, accountID).Scan(&exists)
	return exists > 0
}

func (s *Server) proxyRoutes(r chi.Router) {
	r.Get("/{accountID}", s.proxyGet)
	r.Put("/{accountID}", s.proxyPut)
	r.Delete("/{accountID}", s.proxyDelete)
}
func (s *Server) proxyGet(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	var typ, host string
	var port sql.NullInt64
	var user, pass sql.NullString
	e := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, ownerID, accountID).Scan(&typ, &host, &port, &user, &pass)
	if e != nil {
		fail(w, 404, "账号不存在")
		return
	}
	write(w, 200, map[string]any{"proxy_type": typ, "proxy_host": host, "proxy_port": nullable(port), "proxy_user": user.String, "proxy_pass": pass.String})
}
func (s *Server) proxyPut(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET proxy_type=?,proxy_host=?,proxy_port=?,proxy_user=?,proxy_pass=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, p["proxy_type"], p["proxy_host"], p["proxy_port"], p["proxy_user"], p["proxy_pass"], ownerID, accountID)
	affected(w, res, e, "代理配置更新成功")
}
func (s *Server) proxyDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET proxy_type='none',proxy_host=NULL,proxy_port=NULL,proxy_user=NULL,proxy_pass=NULL,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, ownerID, accountID)
	affected(w, res, e, "代理配置已清除")
}

func (s *Server) userRoutes(r chi.Router) {
	r.Get("/me", s.userMe)
	r.Post("/change-password", s.changePassword)
	r.Post("/renew", s.renewMembership)
}

func (s *Server) renewMembership(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var payload struct {
		Months int `json:"months"`
	}
	if decode(r, &payload) != nil || payload.Months < 1 {
		write(w, 200, response{Success: false, Message: "续期月数必须为正整数"})
		return
	}
	if payload.Months > 120 {
		write(w, 200, response{Success: false, Message: "单次续期不能超过120个月"})
		return
	}
	var priceRaw string
	if err := s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key='user.renew_month_price'`).Scan(&priceRaw); err != nil || strings.TrimSpace(priceRaw) == "" {
		write(w, 200, response{Success: false, Message: "续期功能未开放，请联系管理员配置续期单价"})
		return
	}
	unitCents, err := decimalCents(priceRaw)
	if err != nil || unitCents <= 0 {
		write(w, 200, response{Success: false, Message: "续期单价配置有误，请联系管理员"})
		return
	}
	if unitCents > (1<<63-1)/int64(payload.Months) {
		write(w, 200, response{Success: false, Message: "续期金额超出允许范围"})
		return
	}
	totalCents := unitCents * int64(payload.Months)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	var balanceRaw string
	err = tx.QueryRowContext(r.Context(), `SELECT value FROM user_settings WHERE owner_id=? AND key='balance'`, u.ID).Scan(&balanceRaw)
	if errors.Is(err, sql.ErrNoRows) {
		balanceRaw = "0"
	} else if err != nil {
		fail(w, 500, err.Error())
		return
	}
	balanceCents, err := decimalCents(balanceRaw)
	if err != nil {
		write(w, 200, response{Success: false, Message: "当前余额数据异常，请联系管理员处理"})
		return
	}
	if balanceCents < totalCents {
		write(w, 200, response{Success: false, Message: fmt.Sprintf("余额不足，续期 %d 个月需 ¥%s，当前余额 ¥%s", payload.Months, formatCents(totalCents), formatCents(balanceCents))})
		return
	}
	var expire sql.NullString
	if err = tx.QueryRowContext(r.Context(), `SELECT expire_at FROM xy_users WHERE id=?`, u.ID).Scan(&expire); err != nil {
		write(w, 200, response{Success: false, Message: "用户不存在"})
		return
	}
	base := time.Now()
	if expire.Valid {
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, parseErr := time.ParseInLocation(layout, expire.String, time.Local); parseErr == nil && parsed.After(base) {
				base = parsed
				break
			}
		}
	}
	newExpire := base.AddDate(0, payload.Months, 0)
	afterCents := balanceCents - totalCents
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,'balance',?,'用户余额') ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, u.ID, formatCents(afterCents)); err != nil {
		fail(w, 500, err.Error())
		return
	}
	remark := fmt.Sprintf("账户续期 %d 个月（单价 ¥%s/月）", payload.Months, formatCents(unitCents))
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO fund_flows(owner_id,operator_id,flow_type,amount,balance_before,balance_after,remark) VALUES(?,?,'expense',?,?,?,?)`, u.ID, u.ID, formatCents(totalCents), formatCents(balanceCents), formatCents(afterCents), remark); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE xy_users SET expire_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, newExpire.Format("2006-01-02 15:04:05"), u.ID); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, fmt.Sprintf("续期成功，已延长 %d 个月", payload.Months), map[string]any{"months": payload.Months, "unit_price": formatCents(unitCents), "total": formatCents(totalCents), "balance_before": formatCents(balanceCents), "balance_after": formatCents(afterCents), "expire_at": newExpire.Format(time.RFC3339)})
}
func (s *Server) userMe(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var email, phone, status, role string
	var expire sql.NullString
	_ = s.db.QueryRowContext(r.Context(), `SELECT COALESCE(email,''),COALESCE(phone,''),status,role,expire_at FROM xy_users WHERE id=?`, u.ID).Scan(&email, &phone, &status, &role, &expire)
	write(w, 200, map[string]any{"id": u.ID, "username": u.Username, "email": email, "phone": phone, "status": status, "role": role, "is_admin": role == "ADMIN", "account_limit": nullable(u.AccountLimit), "expire_at": func() any {
		if expire.Valid {
			return expire.String
		}
		return nil
	}()})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		Old     string `json:"old_password"`
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if decode(r, &p) != nil || len(p.New) < 6 {
		fail(w, 400, "新密码长度不能少于6位")
		return
	}
	old := p.Old
	if old == "" {
		old = p.Current
	}
	var h string
	_ = s.db.QueryRowContext(r.Context(), `SELECT password_hash FROM xy_users WHERE id=?`, u.ID).Scan(&h)
	if !auth.Verify(h, old) {
		write(w, 200, response{false, "原密码错误", nil})
		return
	}
	nh, e := auth.Hash(p.New)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	_, e = s.db.ExecContext(r.Context(), `UPDATE xy_users SET password_hash=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, nh, u.ID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "密码修改成功", nil)
}
func (s *Server) getUserCode(column string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var v sql.NullString
		e := s.db.QueryRowContext(r.Context(), `SELECT `+column+` FROM xy_users WHERE id=?`, u.ID).Scan(&v)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		write(w, 200, map[string]any{column: v.String})
	}
}
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
func (s *Server) resetUserCode(column string, n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		v := randomHex(n)
		_, e := s.db.ExecContext(r.Context(), `UPDATE xy_users SET `+column+`=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, v, u.ID)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		ok(w, "重置成功", map[string]any{column: v})
	}
}

var _ = strconv.Itoa
