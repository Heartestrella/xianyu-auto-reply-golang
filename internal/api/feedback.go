package api

import (
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const feedbackKind = "feedback"

func (s *Server) registerFeedbackRoutes(r chi.Router) {
	r.Get("/feedbacks/stats", s.feedbackStats)
	r.Get("/feedbacks", s.feedbackList)
	r.Post("/feedbacks", s.feedbackCreate)
	r.Get("/feedbacks/{feedback_id}", s.feedbackGet)
	r.Post("/feedbacks/{feedback_id}/reply", s.feedbackReply)
	r.Put("/feedbacks/{feedback_id}/resolve", s.feedbackResolve(true))
	r.Put("/feedbacks/{feedback_id}/unresolve", s.feedbackResolve(false))
	r.Delete("/feedbacks/{feedback_id}", s.feedbackDelete)
}

func (s *Server) feedbackStats(w http.ResponseWriter, r *http.Request) {
	var total, resolved int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*),COALESCE(sum(json_extract(data,'$.is_resolved')=1),0) FROM resource_records WHERE kind=?`, feedbackKind).Scan(&total, &resolved)
	ok(w, "", map[string]any{"total": total, "resolved": resolved, "pending": total - resolved})
}

func (s *Server) feedbackList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	query := ` FROM resource_records WHERE kind=?`
	args := []any{feedbackKind}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if value := r.URL.Query().Get("is_resolved"); value != "" {
		query += ` AND json_extract(data,'$.is_resolved')=?`
		args = append(args, value == "true" || value == "1")
	}
	if value := r.URL.Query().Get("feedback_type"); value != "" {
		query += ` AND json_extract(data,'$.feedback_type')=?`
		args = append(args, value)
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total)
	args = append(args, size, (p-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,owner_id,data,created_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, owner int64
		var raw, created string
		_ = rows.Scan(&id, &owner, &raw, &created)
		m := map[string]any{"id": id, "user_id": owner, "created_at": created}
		mergeJSON(m, raw)
		messages, _ := m["messages"].([]any)
		m["message_count"] = len(messages) + 1
		delete(m, "content")
		delete(m, "messages")
		items = append(items, m)
	}
	ok(w, "", map[string]any{"items": items, "total": total, "page": p, "page_size": size})
}

func feedbackPayload(r *http.Request) map[string]any {
	p := compatibilityPayload(r)
	if len(p) == 0 {
		_ = r.ParseForm()
		for k, v := range r.Form {
			if len(v) > 0 {
				p[k] = v[0]
			}
		}
	}
	return p
}
func validFeedbackType(v string) bool {
	return v == "bug" || v == "feature" || v == "suggestion" || v == "other"
}
func (s *Server) feedbackCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := feedbackPayload(r)
	title := strings.TrimSpace(firstString(p, "title"))
	content := strings.TrimSpace(firstString(p, "content"))
	kind := firstString(p, "feedback_type")
	if title == "" || content == "" {
		write(w, 200, response{Success: false, Message: "标题和内容不能为空"})
		return
	}
	if !validFeedbackType(kind) {
		write(w, 200, response{Success: false, Message: "无效的反馈类型"})
		return
	}
	images := any([]any{})
	if raw := firstString(p, "images"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &images)
	} else if p["images"] != nil {
		images = p["images"]
	}
	data := map[string]any{"cookie_id": p["cookie_id"], "title": html.EscapeString(title), "content": html.EscapeString(content), "feedback_type": kind, "images": images, "is_resolved": false, "resolved_at": nil, "messages": []any{}}
	raw, _ := json.Marshal(data)
	res, err := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,?,1,?)`, u.ID, feedbackKind, string(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	ok(w, "反馈提交成功", map[string]any{"id": id})
}

func (s *Server) loadFeedback(r *http.Request) (int64, int64, map[string]any, string, error) {
	var id, owner int64
	var raw, created string
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,data,created_at FROM resource_records WHERE kind=? AND id=?`, feedbackKind, chi.URLParam(r, "feedback_id")).Scan(&id, &owner, &raw, &created)
	m := map[string]any{}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &m)
	}
	return id, owner, m, created, err
}
func (s *Server) feedbackGet(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, owner, m, created, err := s.loadFeedback(r)
	if err == sql.ErrNoRows {
		write(w, 200, response{Success: false, Message: "反馈不存在"})
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if u.Role != "ADMIN" && owner != u.ID {
		write(w, 200, response{Success: false, Message: "无权查看此反馈"})
		return
	}
	messages := []any{map[string]any{"id": 0, "is_admin": false, "content": m["content"], "created_at": created}}
	if saved, ok := m["messages"].([]any); ok {
		messages = append(messages, saved...)
	}
	m["id"], m["user_id"], m["created_at"], m["messages"] = id, owner, created, messages
	ok(w, "", m)
}

func (s *Server) feedbackReply(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, owner, m, _, err := s.loadFeedback(r)
	if err != nil {
		write(w, 200, response{Success: false, Message: "反馈不存在"})
		return
	}
	if u.Role != "ADMIN" && owner != u.ID {
		write(w, 200, response{Success: false, Message: "无权回复此反馈"})
		return
	}
	p := feedbackPayload(r)
	content := strings.TrimSpace(firstString(p, "content"))
	if content == "" {
		write(w, 200, response{Success: false, Message: "回复内容不能为空"})
		return
	}
	messages, _ := m["messages"].([]any)
	messageID := time.Now().UnixMilli()
	messages = append(messages, map[string]any{"id": messageID, "user_id": u.ID, "is_admin": u.Role == "ADMIN", "content": html.EscapeString(content), "created_at": time.Now().Format(time.RFC3339)})
	m["messages"] = messages
	raw, _ := json.Marshal(m)
	_, err = s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "回复成功", map[string]any{"id": messageID})
}

func (s *Server) feedbackResolve(resolved bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		if u.Role != "ADMIN" {
			fail(w, 403, "需要管理员权限")
			return
		}
		id, _, m, _, err := s.loadFeedback(r)
		if err != nil {
			write(w, 200, response{Success: false, Message: "反馈不存在"})
			return
		}
		m["is_resolved"] = resolved
		if resolved {
			m["resolved_at"] = time.Now().Format(time.RFC3339)
		} else {
			m["resolved_at"] = nil
		}
		raw, _ := json.Marshal(m)
		_, err = s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), id)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		ok(w, map[bool]string{true: "已标记为解决", false: "已标记为未解决"}[resolved], nil)
	}
}
func (s *Server) feedbackDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if u.Role != "ADMIN" {
		fail(w, 403, "需要管理员权限")
		return
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE kind=? AND id=?`, feedbackKind, chi.URLParam(r, "feedback_id"))
	affected(w, res, err, "删除成功")
}
