package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

type resourceSpec struct {
	kind, itemKey string
	directList    bool
}

func (s *Server) registerResources(r chi.Router) {
	s.resource(r, "/product-monitor/categories", resourceSpec{"listing_monitor_category", "category", false})
	s.resource(r, "/product-publish/addresses", resourceSpec{"publish_address", "address", false})
	r.Get("/product-publish/addresses/account-options", s.publishAddressAccountOptions)
	r.Get("/product-publish/addresses/input-tips", s.publishAddressInputTips)
	s.resource(r, "/product-publish/personal-addresses", resourceSpec{"personal_address", "address", false})
	r.Get("/product-publish/personal-addresses/export", s.personalAddressExport)
	r.Post("/product-publish/personal-addresses/import", s.personalAddressImport)
	s.resource(r, "/notification-channels", resourceSpec{"notification_channel", "channel", true})
	s.registerMessageFilterRoutes(r)
}
func (s *Server) resource(r chi.Router, path string, spec resourceSpec) {
	r.Route(path, func(r chi.Router) {
		r.Get("/", s.resourceList(spec))
		r.Post("/", s.resourceCreate(spec))
		r.Post("/batch-delete", s.resourceBatchDelete(spec))
		r.Get("/{recordID}", s.resourceGet(spec))
		r.Put("/{recordID}", s.resourceUpdate(spec))
		r.Put("/{recordID}/toggle", s.resourceToggle(spec))
		r.Put("/{recordID}/status", s.resourceUpdate(spec))
		r.Delete("/{recordID}", s.resourceDelete(spec))
	})
}
func (s *Server) resourceGet(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var id int64
		var account sql.NullString
		var enabled bool
		var data, created, updated string
		err := s.db.QueryRowContext(r.Context(), `SELECT id,account_id,enabled,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, spec.kind, chi.URLParam(r, "recordID")).Scan(&id, &account, &enabled, &data, &created, &updated)
		if err == sql.ErrNoRows {
			fail(w, 404, "记录不存在")
			return
		}
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		item := map[string]any{"id": id, "enabled": enabled, "is_enabled": enabled, "created_at": created, "updated_at": updated}
		if account.Valid {
			item["account_id"] = account.String
		}
		mergeJSON(item, data)
		ok(w, "查询成功", map[string]any{spec.itemKey: item})
	}
}

func (s *Server) messageFilterBatchCreateLegacy(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		AccountIDs  []string `json:"account_ids"`
		Keyword     string   `json:"keyword"`
		FilterTypes []string `json:"filter_types"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	keyword := strings.TrimSpace(p.Keyword)
	accounts := uniqueStrings(p.AccountIDs)
	types := uniqueStrings(p.FilterTypes)
	if len(accounts) == 0 || keyword == "" || len(types) == 0 {
		write(w, 200, response{Success: false, Message: "请选择账号、填写关键词并选择过滤类型"})
		return
	}
	for _, typ := range types {
		if typ != "skip_reply" && typ != "skip_notify" {
			write(w, 200, response{Success: false, Message: "无效的过滤类型: " + typ})
			return
		}
	}
	for _, accountID := range accounts {
		var count int
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&count)
		if count == 0 {
			write(w, 200, response{Success: false, Message: "以下账号不存在或无权操作: " + accountID})
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	created, duplicates := 0, 0
	for _, accountID := range accounts {
		for _, typ := range types {
			var exists int
			_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind='message_filter' AND account_id=? AND json_extract(data,'$.keyword')=? AND json_extract(data,'$.filter_type')=?`, u.ID, accountID, keyword, typ).Scan(&exists)
			if exists > 0 {
				duplicates++
				continue
			}
			data, _ := json.Marshal(map[string]any{"account_id": accountID, "keyword": keyword, "filter_type": typ, "enabled": true})
			if _, err = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,'message_filter',?,1,?)`, u.ID, accountID, string(data)); err != nil {
				fail(w, 500, err.Error())
				return
			}
			created++
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "批量创建完成", map[string]any{"created_count": created, "failed_count": duplicates, "total_count": created + duplicates})
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
func (s *Server) resourceList(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		p, n := page(r)
		q := ` FROM resource_records WHERE owner_id=? AND kind=?`
		args := []any{u.ID, spec.kind}
		if ac := r.URL.Query().Get("account_id"); ac != "" {
			q += ` AND account_id=?`
			args = append(args, ac)
		}
		var total int
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+q, args...).Scan(&total)
		args = append(args, n, (p-1)*n)
		rows, e := s.db.QueryContext(r.Context(), `SELECT id,account_id,enabled,data,created_at,updated_at`+q+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id int64
			var ac sql.NullString
			var enabled bool
			var data, created, updated string
			_ = rows.Scan(&id, &ac, &enabled, &data, &created, &updated)
			m := map[string]any{"id": id, "enabled": enabled, "is_enabled": enabled, "created_at": created, "updated_at": updated}
			if ac.Valid {
				m["account_id"] = ac.String
			}
			mergeJSON(m, data)
			list = append(list, m)
		}
		if spec.directList {
			write(w, 200, list)
			return
		}
		if spec.kind == "message_filter" {
			ok(w, "获取成功", list)
			return
		}
		if spec.kind == "listing_monitor_category" {
			ok(w, "查询成功", list)
			return
		}
		data := map[string]any{"list": list, "items": list, "total": total, "page": p, "page_size": n, "total_pages": pages(total, n)}
		ok(w, "查询成功", data)
	}
}
func (s *Server) resourceCreate(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var p map[string]any
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		ac := fmt.Sprint(p["account_id"])
		enabled := true
		if v, okv := p["enabled"].(bool); okv {
			enabled = v
		}
		if v, okv := p["is_enabled"].(bool); okv {
			enabled = v
		}
		b, _ := json.Marshal(p)
		res, e := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,?,?)`, u.ID, spec.kind, nullString(ac), enabled, string(b))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		id, _ := res.LastInsertId()
		p["id"] = id
		p["enabled"] = enabled
		p["is_enabled"] = enabled
		ok(w, "创建成功", map[string]any{spec.itemKey: p})
	}
}
func (s *Server) resourceUpdate(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		id, e := strconv.ParseInt(chi.URLParam(r, "recordID"), 10, 64)
		if e != nil {
			fail(w, 400, "ID无效")
			return
		}
		var p map[string]any
		if decode(r, &p) != nil {
			p = map[string]any{}
		}
		var old string
		var enabled bool
		e = s.db.QueryRowContext(r.Context(), `SELECT data,enabled FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, spec.kind, id).Scan(&old, &enabled)
		if e != nil {
			fail(w, 404, "记录不存在")
			return
		}
		var merged map[string]any
		_ = json.Unmarshal([]byte(old), &merged)
		for k, v := range p {
			merged[k] = v
		}
		if v, okv := p["enabled"].(bool); okv {
			enabled = v
		}
		if v, okv := p["is_enabled"].(bool); okv {
			enabled = v
		}
		b, _ := json.Marshal(merged)
		_, e = s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, enabled, string(b), id)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		merged["id"] = id
		merged["enabled"] = enabled
		merged["is_enabled"] = enabled
		ok(w, "更新成功", map[string]any{spec.itemKey: merged})
	}
}
func (s *Server) resourceToggle(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		res, e := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=NOT enabled,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind=? AND id=?`, u.ID, spec.kind, chi.URLParam(r, "recordID"))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			fail(w, 404, "记录不存在")
			return
		}
		var enabled bool
		_ = s.db.QueryRowContext(r.Context(), `SELECT enabled FROM resource_records WHERE id=?`, chi.URLParam(r, "recordID")).Scan(&enabled)
		ok(w, "状态更新成功", map[string]any{"enabled": enabled, "is_enabled": enabled})
	}
}
func (s *Server) resourceDelete(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		res, e := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, spec.kind, chi.URLParam(r, "recordID"))
		affected(w, res, e, "删除成功")
	}
}
func (s *Server) resourceBatchDelete(spec resourceSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var p struct {
			IDs []int64 `json:"ids"`
		}
		if decode(r, &p) != nil || len(p.IDs) == 0 {
			fail(w, 400, "请选择记录")
			return
		}
		args := []any{u.ID, spec.kind}
		for _, id := range p.IDs {
			args = append(args, id)
		}
		res, e := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id IN (`+placeholders(len(p.IDs))+`)`, args...)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		n, _ := res.RowsAffected()
		ok(w, "批量删除成功", map[string]any{"success_count": n, "deleted_count": n, "total_count": len(p.IDs)})
	}
}
func nullString(v string) any {
	if strings.TrimSpace(v) == "" || v == "<nil>" {
		return nil
	}
	return v
}
func (s *Server) publicResource(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, e := s.db.QueryContext(r.Context(), `SELECT id,data,created_at,updated_at FROM resource_records WHERE kind=? AND enabled=1 ORDER BY id DESC`, kind)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id int64
			var data, created, updated string
			_ = rows.Scan(&id, &data, &created, &updated)
			m := map[string]any{"id": id, "source": "local", "created_at": created, "updated_at": updated}
			mergeJSON(m, data)
			items = append(items, m)
		}
		ok(w, "查询成功", map[string]any{"items": items})
	}
}
