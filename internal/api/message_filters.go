package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Message filters have a distinct API contract from generic resources: one
// request may create multiple filter types and the list response is an array.
func (s *Server) registerMessageFilterRoutes(r chi.Router) {
	r.Get("/message-filters", s.messageFilterList)
	r.Post("/message-filters", s.messageFilterCreate)
	r.Post("/message-filters/batch-create", s.messageFilterBatchCreate)
	r.Post("/message-filters/batch-delete", s.messageFilterBatchDelete)
	r.Put("/message-filters/{filterID}", s.messageFilterUpdate)
	r.Put("/message-filters/{filterID}/toggle", s.messageFilterToggle)
	r.Delete("/message-filters/{filterID}", s.messageFilterDelete)
}

type messageFilterInput struct {
	AccountID   string   `json:"account_id"`
	AccountIDs  []string `json:"account_ids"`
	Keyword     string   `json:"keyword"`
	FilterType  string   `json:"filter_type"`
	FilterTypes []string `json:"filter_types"`
	Enabled     *bool    `json:"enabled"`
}

func validMessageFilterType(value string) bool {
	return value == "skip_reply" || value == "skip_notify"
}

func (s *Server) messageFilterList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query := `SELECT id,account_id,enabled,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind='message_filter'`
	args := []any{u.ID}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		query += ` AND account_id=?`
		args = append(args, accountID)
	}
	query += ` ORDER BY id DESC`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var accountID sql.NullString
		var enabled bool
		var raw, created, updated string
		if err := rows.Scan(&id, &accountID, &enabled, &raw, &created, &updated); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		item := map[string]any{"id": id, "account_id": accountID.String, "enabled": enabled, "created_at": created, "updated_at": updated}
		mergeJSON(item, raw)
		// Ignore malformed legacy records instead of exposing incomplete rows.
		if strings.TrimSpace(toString(item["keyword"])) != "" && validMessageFilterType(toString(item["filter_type"])) {
			items = append(items, item)
		}
	}
	ok(w, "获取成功", items)
}

func (s *Server) messageFilterCreate(w http.ResponseWriter, r *http.Request) {
	var input messageFilterInput
	if decode(r, &input) != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	if input.AccountID == "" {
		write(w, http.StatusOK, response{Success: false, Message: "账号不能为空"})
		return
	}
	s.messageFilterCreateRecords(w, r, []string{input.AccountID}, input.Keyword, input.FilterTypes)
}

func (s *Server) messageFilterBatchCreate(w http.ResponseWriter, r *http.Request) {
	var input messageFilterInput
	if decode(r, &input) != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	s.messageFilterCreateRecords(w, r, input.AccountIDs, input.Keyword, input.FilterTypes)
}

func (s *Server) messageFilterCreateRecords(w http.ResponseWriter, r *http.Request, accounts []string, keyword string, types []string) {
	u := current(r)
	keyword = strings.TrimSpace(keyword)
	accounts = uniqueStrings(accounts)
	types = uniqueStrings(types)
	if keyword == "" {
		write(w, http.StatusOK, response{Success: false, Message: "关键词不能为空"})
		return
	}
	if len(accounts) == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "请选择至少一个账号"})
		return
	}
	if len(types) == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "请选择至少一种过滤类型"})
		return
	}
	for _, kind := range types {
		if !validMessageFilterType(kind) {
			write(w, http.StatusOK, response{Success: false, Message: "无效的过滤类型: " + kind})
			return
		}
	}
	for _, accountID := range accounts {
		var count int
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&count)
		if count == 0 {
			write(w, http.StatusOK, response{Success: false, Message: "以下账号不存在或无权操作: " + accountID})
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	ids := []int64{}
	duplicateCount := 0
	for _, accountID := range accounts {
		for _, kind := range types {
			var exists int
			_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind='message_filter' AND account_id=? AND json_extract(data,'$.keyword')=? AND json_extract(data,'$.filter_type')=?`, u.ID, accountID, keyword, kind).Scan(&exists)
			if exists > 0 {
				duplicateCount++
				continue
			}
			raw, _ := json.Marshal(map[string]any{"account_id": accountID, "keyword": keyword, "filter_type": kind, "enabled": true})
			result, err := tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,'message_filter',?,1,?)`, u.ID, accountID, string(raw))
			if err != nil {
				fail(w, http.StatusInternalServerError, err.Error())
				return
			}
			id, _ := result.LastInsertId()
			ids = append(ids, id)
		}
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(ids) == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "创建失败：规则已存在", Data: map[string]any{"created_ids": ids, "created_count": 0, "failed_count": duplicateCount}})
		return
	}
	ok(w, "成功创建 "+strconv.Itoa(len(ids))+" 条规则", map[string]any{"created_ids": ids, "created_count": len(ids), "failed_count": duplicateCount})
}

func (s *Server) messageFilterUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "filterID"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "规则不存在")
		return
	}
	var input messageFilterInput
	if decode(r, &input) != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	u := current(r)
	var accountID, raw string
	var enabled bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(account_id,''),data,enabled FROM resource_records WHERE id=? AND owner_id=? AND kind='message_filter'`, id, u.ID).Scan(&accountID, &raw, &enabled); err != nil {
		write(w, http.StatusOK, response{Success: false, Message: "规则不存在"})
		return
	}
	data := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &data)
	if input.Keyword != "" {
		keyword := strings.TrimSpace(input.Keyword)
		if keyword == "" {
			fail(w, http.StatusBadRequest, "关键词不能为空")
			return
		}
		data["keyword"] = keyword
	}
	if input.FilterType != "" {
		if !validMessageFilterType(input.FilterType) {
			fail(w, http.StatusBadRequest, "无效的过滤类型")
			return
		}
		data["filter_type"] = input.FilterType
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	data["enabled"] = enabled
	encoded, _ := json.Marshal(data)
	if _, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND owner_id=? AND kind='message_filter'`, enabled, string(encoded), id, u.ID); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, "更新成功", nil)
}

func (s *Server) messageFilterToggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "filterID"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "规则不存在")
		return
	}
	u := current(r)
	var enabled bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT enabled FROM resource_records WHERE id=? AND owner_id=? AND kind='message_filter'`, id, u.ID).Scan(&enabled); err != nil {
		write(w, http.StatusOK, response{Success: false, Message: "规则不存在"})
		return
	}
	enabled = !enabled
	if _, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND owner_id=? AND kind='message_filter'`, enabled, id, u.ID); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	message := "已禁用"
	if enabled {
		message = "已启用"
	}
	ok(w, message, map[string]any{"enabled": enabled})
}

func (s *Server) messageFilterDelete(w http.ResponseWriter, r *http.Request) {
	s.messageFilterDeleteIDs(w, r, []int64{parseFilterID(chi.URLParam(r, "filterID"))}, false)
}
func (s *Server) messageFilterBatchDelete(w http.ResponseWriter, r *http.Request) {
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	s.messageFilterDeleteIDs(w, r, p.IDs, true)
}
func parseFilterID(value string) int64 { id, _ := strconv.ParseInt(value, 10, 64); return id }
func (s *Server) messageFilterDeleteIDs(w http.ResponseWriter, r *http.Request, ids []int64, batch bool) {
	u := current(r)
	valid := []int64{}
	for _, id := range ids {
		if id > 0 {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "请选择要删除的规则"})
		return
	}
	args := []any{u.ID}
	for _, id := range valid {
		args = append(args, id)
	}
	result, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind='message_filter' AND id IN (`+placeholders(len(valid))+`)`, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "规则不存在"})
		return
	}
	message := "删除成功"
	if batch {
		message = "成功删除 " + strconv.FormatInt(count, 10) + " 条规则"
	}
	ok(w, message, map[string]any{"deleted_count": count})
}
