package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) sharedScanCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := uuid.NewString()
	expires := time.Now().UTC().Add(72 * time.Hour)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO shared_scan_sessions(session_id,owner_id,owner_username,expires_at) VALUES(?,?,?,?)`, id, u.ID, u.Username, expires.Format(time.RFC3339))
	if err != nil {
		write(w, 200, response{Success: false, Message: "创建失败: " + err.Error()})
		return
	}
	ok(w, "共享会话创建成功", map[string]any{"session_id": id, "share_url": sharedScanURL(r, id), "expires_at": expires.Format(time.RFC3339)})
}

func sharedScanURL(r *http.Request, id string) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host + "/shared-scan-page?session_id=" + url.QueryEscape(id)
}

func (s *Server) sharedScanList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT x.session_id,x.status,x.expires_at,x.created_at,count(w.sub_session_id),COALESCE(sum(CASE WHEN w.status='success' THEN 1 ELSE 0 END),0) FROM shared_scan_sessions x LEFT JOIN shared_scan_workers w ON w.shared_session_id=x.session_id WHERE x.owner_id=? GROUP BY x.session_id ORDER BY x.created_at DESC`, u.ID)
	if err != nil {
		write(w, 200, response{Success: false, Message: "查询失败: " + err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, status, expires, created string
		var workers, success int
		if rows.Scan(&id, &status, &expires, &created, &workers, &success) == nil {
			out = append(out, map[string]any{"session_id": id, "status": status, "share_url": sharedScanURL(r, id), "expires_at": expires, "created_at": created, "worker_count": workers, "success_count": success})
		}
	}
	ok(w, "获取成功", map[string]any{"sessions": out})
}

func (s *Server) sharedScanStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := strings.TrimSpace(r.URL.Query().Get("session_id"))
	var status string
	if s.db.QueryRowContext(r.Context(), `SELECT status FROM shared_scan_sessions WHERE session_id=? AND owner_id=?`, id, u.ID).Scan(&status) != nil {
		write(w, 200, response{Success: false, Message: "会话不存在或无权访问"})
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT sub_session_id,status,COALESCE(account_id,''),cookie_saved,created_at FROM shared_scan_workers WHERE shared_session_id=? ORDER BY created_at`, id)
	if err != nil {
		write(w, 200, response{Success: false, Message: "查询失败: " + err.Error()})
		return
	}
	defer rows.Close()
	workers := []map[string]any{}
	for rows.Next() {
		var sid, st, account, created string
		var saved bool
		_ = rows.Scan(&sid, &st, &account, &saved, &created)
		workers = append(workers, map[string]any{"sub_session_id": sid, "status": st, "account_id": account, "cookie_saved": saved, "joined_at": created})
	}
	ok(w, "获取成功", map[string]any{"session_id": id, "session_status": status, "part_time_workers": workers})
}

func (s *Server) sharedScanDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM shared_scan_sessions WHERE session_id=? AND owner_id=?`, chi.URLParam(r, "session_id"), u.ID)
	if err != nil {
		write(w, 200, response{Success: false, Message: "删除失败: " + err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{Success: false, Message: "会话不存在或无权访问"})
		return
	}
	ok(w, "删除成功", nil)
}

func (s *Server) sharedScanJoin(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SessionID    string `json:"session_id"`
		VisitorToken string `json:"visitor_token"`
		ForceRefresh bool   `json:"force_refresh"`
	}
	if decode(r, &p) != nil || strings.TrimSpace(p.SessionID) == "" {
		write(w, 200, response{Success: false, Message: "缺少 session_id 参数"})
		return
	}
	var owner int64
	var status, expires string
	err := s.db.QueryRowContext(r.Context(), `SELECT owner_id,status,expires_at FROM shared_scan_sessions WHERE session_id=?`, p.SessionID).Scan(&owner, &status, &expires)
	exp, _ := time.Parse(time.RFC3339, expires)
	if err != nil || status != "active" || time.Now().After(exp) {
		write(w, 200, response{Success: false, Message: "共享会话不存在或已过期"})
		return
	}
	if p.VisitorToken != "" && !p.ForceRefresh {
		var sub, qr string
		e := s.db.QueryRowContext(r.Context(), `SELECT sub_session_id,COALESCE(qr_code_url,'') FROM shared_scan_workers WHERE shared_session_id=? AND visitor_token=? ORDER BY created_at DESC LIMIT 1`, p.SessionID, p.VisitorToken).Scan(&sub, &qr)
		if e == nil {
			ok(w, "加入成功，请扫描二维码", map[string]any{"sub_session_id": sub, "qrcode_data_url": qr})
			return
		}
	}
	qr, err := s.qrLogin.generate(owner)
	if err != nil {
		write(w, 200, response{Success: false, Message: "生成二维码失败: " + err.Error()})
		return
	}
	sub := uuid.NewString()
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO shared_scan_workers(sub_session_id,shared_session_id,xianyu_session_id,qr_code_url,visitor_token) VALUES(?,?,?,?,?)`, sub, p.SessionID, qr.ID, qr.QRCodeURL, nullString(p.VisitorToken))
	if err != nil {
		write(w, 200, response{Success: false, Message: "加入失败: " + err.Error()})
		return
	}
	ok(w, "加入成功，请扫描二维码", map[string]any{"sub_session_id": sub, "qrcode_data_url": qr.QRCodeURL})
}

func (s *Server) sharedScanWorkerStatus(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimSpace(r.URL.Query().Get("sub_session_id"))
	var qrID, status string
	var owner int64
	var saved bool
	err := s.db.QueryRowContext(r.Context(), `SELECT w.xianyu_session_id,w.status,w.cookie_saved,x.owner_id FROM shared_scan_workers w JOIN shared_scan_sessions x ON x.session_id=w.shared_session_id WHERE w.sub_session_id=?`, sub).Scan(&qrID, &status, &saved, &owner)
	if err != nil {
		write(w, 200, response{Success: false, Message: "兼职会话不存在或已过期"})
		return
	}
	qr := s.qrLogin.get(qrID)
	if qr == nil {
		ok(w, "获取成功", map[string]any{"status": status})
		return
	}
	qr.mu.RLock()
	qstatus, cookie, unb := qr.Status, qr.Cookie, qr.UNB
	qr.mu.RUnlock()
	if qstatus == "success" && !saved {
		account := unb
		if account == "" {
			account = "qr-" + strings.ReplaceAll(qrID[:12], "-", "")
		}
		var existing int
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE account_id=? AND owner_id=?`, account, owner).Scan(&existing)
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO xy_accounts(owner_id,account_id,unb,cookie,login_method,status,last_login_at) VALUES(?,?,?,?,?,'active',CURRENT_TIMESTAMP) ON CONFLICT(account_id) DO UPDATE SET cookie=excluded.cookie,unb=excluded.unb,login_method='qr',status='active',updated_at=CURRENT_TIMESTAMP WHERE xy_accounts.owner_id=excluded.owner_id`, owner, account, nullString(unb), cookie, "qr")
		if err != nil {
			write(w, 200, response{Success: false, Message: "保存账号失败: " + err.Error()})
			return
		}
		_, _ = s.db.ExecContext(r.Context(), `UPDATE shared_scan_workers SET status='success',account_id=?,cookie_saved=1,updated_at=CURRENT_TIMESTAMP WHERE sub_session_id=?`, account, sub)
		if s.accountRestart != nil {
			go s.accountRestart(account)
		}
		ok(w, "扫码登录成功", map[string]any{"status": "success", "account_id": account, "is_new_account": existing == 0})
		return
	}
	mapped := map[string]string{"waiting": "waiting", "scanned": "scanned", "expired": "expired", "cancelled": "cancelled", "verification_required": "verification_required"}[qstatus]
	if mapped == "" {
		mapped = status
	}
	if mapped != status && mapped != "verification_required" {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE shared_scan_workers SET status=?,updated_at=CURRENT_TIMESTAMP WHERE sub_session_id=?`, mapped, sub)
	}
	data := map[string]any{"status": mapped}
	ok(w, "获取成功", data)
}
