package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) recordUserLogin(r *http.Request, ownerID int64, identity, method string, success bool) {
	payload, _ := json.Marshal(map[string]any{
		"identity": identity, "login_method": method, "success": success,
		"ip_address": r.RemoteAddr, "user_agent": r.UserAgent(),
	})
	var owner any
	if ownerID > 0 {
		owner = ownerID
	}
	level := "INFO"
	if !success {
		level = "WARN"
	}
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,level,message,data) VALUES(?,'user_login',?,?,?)`, owner, level, identity, string(payload))
}

func (s *Server) userLoginLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	limit, offset := limitOffset(r)
	query := ` FROM event_logs WHERE kind='user_login'`
	args := []any{}
	if keyword := strings.TrimSpace(r.URL.Query().Get("keyword")); keyword != "" {
		query += ` AND (message LIKE ? OR CAST(owner_id AS TEXT)=?)`
		args = append(args, "%"+keyword+"%", keyword)
	}
	if method := strings.TrimSpace(r.URL.Query().Get("login_method")); method != "" {
		query += ` AND json_extract(data,'$.login_method')=?`
		args = append(args, method)
	}
	query, args = appendDateFilters(r, query, args)
	s.writeLoginLogPage(w, r, query, args, limit, offset, false)
}

func (s *Server) accountLoginLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	limit, offset := limitOffset(r)
	query := ` FROM event_logs WHERE kind='account_login'`
	args := []any{}
	if accountID := strings.TrimSpace(r.URL.Query().Get("cookie_id")); accountID != "" {
		query += ` AND account_id=?`
		args = append(args, accountID)
	}
	if status := strings.TrimSpace(r.URL.Query().Get("login_status")); status != "" {
		query += ` AND json_extract(data,'$.login_status')=?`
		args = append(args, status)
	}
	query, args = appendDateFilters(r, query, args)
	s.writeLoginLogPage(w, r, query, args, limit, offset, true)
}

func limitOffset(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func appendDateFilters(r *http.Request, query string, args []any) (string, []any) {
	if start := strings.TrimSpace(r.URL.Query().Get("start_date")); start != "" {
		query += ` AND date(created_at,'localtime')>=date(?)`
		args = append(args, start)
	}
	if end := strings.TrimSpace(r.URL.Query().Get("end_date")); end != "" {
		query += ` AND date(created_at,'localtime')<=date(?)`
		args = append(args, end)
	}
	return query, args
}

func (s *Server) writeLoginLogPage(w http.ResponseWriter, r *http.Request, query string, args []any, limit, offset int, account bool) {
	var total int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total); err != nil {
		fail(w, 500, err.Error())
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,owner_id,COALESCE(account_id,''),level,message,data,created_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var owner sql.NullInt64
		var accountID, level, message, raw, created string
		if rows.Scan(&id, &owner, &accountID, &level, &message, &raw, &created) != nil {
			continue
		}
		item := map[string]any{"id": id, "owner_id": nullable(owner), "message": message, "created_at": created}
		var extra map[string]any
		if json.Unmarshal([]byte(raw), &extra) == nil {
			for key, value := range extra {
				item[key] = value
			}
		}
		if account {
			item["cookie_id"] = accountID
			if _, exists := item["login_status"]; !exists {
				item["login_status"] = map[bool]string{true: "success", false: "failed"}[strings.EqualFold(level, "INFO")]
			}
		}
		items = append(items, item)
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) autoReplyLogs(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	pageNumber, size := page(r)
	query := ` FROM event_logs WHERE owner_id=? AND kind IN ('auto_reply','ai_reply')`
	args := []any{u.ID}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		query += ` AND account_id=?`
		args = append(args, accountID)
	}
	if start := strings.TrimSpace(r.URL.Query().Get("start_date")); start != "" {
		query += ` AND date(created_at,'localtime')>=date(?)`
		args = append(args, start)
	}
	if end := strings.TrimSpace(r.URL.Query().Get("end_date")); end != "" {
		query += ` AND date(created_at,'localtime')<=date(?)`
		args = append(args, end)
	}
	var total int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total); err != nil {
		fail(w, 500, err.Error())
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,COALESCE(account_id,''),kind,level,message,data,created_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, size, (pageNumber-1)*size)...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var accountID, kind, level, message, raw, created string
		if rows.Scan(&id, &accountID, &kind, &level, &message, &raw, &created) != nil {
			continue
		}
		item := map[string]any{"id": id, "account_id": accountID, "message_type": "auto_reply", "reply_content": message, "send_status": map[bool]string{true: "success", false: "failed"}[strings.EqualFold(level, "INFO")], "created_at": created}
		if kind == "ai_reply" {
			item["matched_rule_type"] = "ai"
		} else {
			item["matched_rule_type"] = "keyword"
		}
		var extra map[string]any
		if json.Unmarshal([]byte(raw), &extra) == nil {
			for key, value := range extra {
				item[key] = value
			}
		}
		items = append(items, item)
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + size - 1) / size
	}
	write(w, 200, map[string]any{"success": true, "message": "查询成功", "data": items, "total": total, "page": pageNumber, "page_size": size, "total_pages": totalPages})
}
