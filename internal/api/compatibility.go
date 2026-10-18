package api

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// compatibility_routes.json is generated from the original FastAPI route table.
// Routes belonging to the intentionally removed distribution/rebate subsystem are
// not included. Explicit Go handlers are registered before these compatibility
// handlers and therefore always take precedence.
//
//go:embed compatibility_routes.json
var compatibilityRouteData []byte

type compatibilityRoute struct {
	Module  string `json:"module"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

func (s *Server) registerCompatibility(r chi.Router) {
	var routes []compatibilityRoute
	if err := json.Unmarshal(compatibilityRouteData, &routes); err != nil {
		s.log.Error("cannot load compatibility route table", "error", err)
		return
	}
	for _, route := range routes {
		relative := strings.TrimPrefix(route.Path, "/api/v1")
		r.Method(route.Method, relative, s.compatibilityHandler(route))
	}
}

// compatibilityHandler preserves the original HTTP surface for secondary
// modules while keeping their data in SQLite. It is deliberately data-driven:
// each module gets an isolated resource namespace and every record remains
// scoped to the authenticated owner.
func (s *Server) compatibilityHandler(route compatibilityRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		if u.ID == 0 {
			fail(w, http.StatusUnauthorized, "未登录或登录已过期")
			return
		}

		kind := "compat:" + route.Module
		switch r.Method {
		case http.MethodGet:
			s.compatibilityGet(w, r, kind, route)
		case http.MethodPost:
			s.compatibilityCreate(w, r, kind, route)
		case http.MethodPut, http.MethodPatch:
			s.compatibilityUpdate(w, r, kind, route)
		case http.MethodDelete:
			s.compatibilityDelete(w, r, kind, route)
		default:
			fail(w, http.StatusMethodNotAllowed, "请求方法不支持")
		}
	}
}

func (s *Server) compatibilityGet(w http.ResponseWriter, r *http.Request, kind string, route compatibilityRoute) {
	u := current(r)
	id := compatibilityID(r)
	if id != "" {
		var rowID int64
		var enabled bool
		var data, created, updated string
		err := s.db.QueryRowContext(r.Context(), `SELECT id,enabled,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind=? AND (id=? OR json_extract(data,'$.external_id')=?) ORDER BY id DESC LIMIT 1`, u.ID, kind, id, id).Scan(&rowID, &enabled, &data, &created, &updated)
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "记录不存在")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		item := compatibilityRecord(rowID, enabled, data, created, updated)
		ok(w, "查询成功", item)
		return
	}

	p, size := page(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind=?`, u.ID, kind).Scan(&total)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,enabled,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind=? ORDER BY id DESC LIMIT ? OFFSET ?`, u.ID, kind, size, (p-1)*size)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var enabled bool
		var data, created, updated string
		if rows.Scan(&id, &enabled, &data, &created, &updated) == nil {
			items = append(items, compatibilityRecord(id, enabled, data, created, updated))
		}
	}
	data := map[string]any{"list": items, "items": items, "records": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)}
	write(w, http.StatusOK, map[string]any{"success": true, "data": data, "list": items, "items": items, "total": total})
}

func (s *Server) compatibilityCreate(w http.ResponseWriter, r *http.Request, kind string, route compatibilityRoute) {
	u := current(r)
	p := compatibilityPayload(r)
	p["operation"] = route.Handler
	b, _ := json.Marshal(p)
	account := firstString(p, "account_id", "cookie_id")
	res, err := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,?,?)`, u.ID, kind, nullString(account), compatibilityEnabled(p, true), string(b))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	p["id"] = id
	ok(w, "操作成功", p)
}

func (s *Server) compatibilityUpdate(w http.ResponseWriter, r *http.Request, kind string, route compatibilityRoute) {
	u := current(r)
	id := compatibilityID(r)
	if id == "" {
		s.compatibilityCreate(w, r, kind, route)
		return
	}
	var old string
	var rowID int64
	var enabled bool
	err := s.db.QueryRowContext(r.Context(), `SELECT id,data,enabled FROM resource_records WHERE owner_id=? AND kind=? AND (id=? OR json_extract(data,'$.external_id')=?) ORDER BY id DESC LIMIT 1`, u.ID, kind, id, id).Scan(&rowID, &old, &enabled)
	if err == sql.ErrNoRows {
		p := compatibilityPayload(r)
		p["external_id"] = id
		p["operation"] = route.Handler
		b, _ := json.Marshal(p)
		res, e := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,?,?,?)`, u.ID, kind, compatibilityEnabled(p, true), string(b))
		if e != nil {
			fail(w, http.StatusInternalServerError, e.Error())
			return
		}
		rowID, _ = res.LastInsertId()
		p["id"] = rowID
		ok(w, "更新成功", p)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	merged := map[string]any{}
	_ = json.Unmarshal([]byte(old), &merged)
	for k, v := range compatibilityPayload(r) {
		merged[k] = v
	}
	merged["operation"] = route.Handler
	enabled = compatibilityEnabled(merged, enabled)
	b, _ := json.Marshal(merged)
	_, err = s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND owner_id=?`, enabled, string(b), rowID, u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	merged["id"] = rowID
	ok(w, "更新成功", merged)
}

func (s *Server) compatibilityDelete(w http.ResponseWriter, r *http.Request, kind string, route compatibilityRoute) {
	u := current(r)
	id := compatibilityID(r)
	if id == "" || strings.Contains(route.Handler, "clear") {
		res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=?`, u.ID, kind)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		n, _ := res.RowsAffected()
		ok(w, "清理成功", map[string]any{"deleted_count": n})
		return
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND (id=? OR json_extract(data,'$.external_id')=?)`, u.ID, kind, id, id)
	affected(w, res, err, "删除成功")
}

func compatibilityPayload(r *http.Request) map[string]any {
	p := map[string]any{}
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		_ = r.ParseMultipartForm(32 << 20)
		for k, values := range r.MultipartForm.Value {
			if len(values) == 1 {
				p[k] = values[0]
			} else {
				p[k] = values
			}
		}
	} else {
		_ = json.NewDecoder(r.Body).Decode(&p)
	}
	for k, values := range r.URL.Query() {
		if len(values) == 1 {
			p[k] = values[0]
		} else {
			p[k] = values
		}
	}
	return p
}

func compatibilityID(r *http.Request) string {
	for _, name := range []string{"id", "record_id", "task_id", "config_id", "ad_id", "feedback_id", "address_id", "channel_id", "notification_id", "batch_id", "order_id", "account_id", "cookie_id", "goods_id"} {
		if value := chi.URLParam(r, name); value != "" {
			return value
		}
	}
	return ""
}

func compatibilityRecord(id int64, enabled bool, raw, created, updated string) map[string]any {
	m := map[string]any{"id": id, "enabled": enabled, "is_enabled": enabled, "created_at": created, "updated_at": updated}
	mergeJSON(m, raw)
	return m
}

func compatibilityEnabled(p map[string]any, fallback bool) bool {
	for _, key := range []string{"enabled", "is_enabled", "active"} {
		if value, ok := p[key]; ok {
			switch v := value.(type) {
			case bool:
				return v
			case string:
				parsed, err := strconv.ParseBool(v)
				if err == nil {
					return parsed
				}
			}
		}
	}
	return fallback
}

func firstString(p map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fmt.Sprint(p[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}
