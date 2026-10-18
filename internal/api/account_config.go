package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) accountConfigRoutes(r chi.Router, kind string, defaults map[string]any) {
	r.Get("/", s.accountConfigs(kind))
	r.Get("/{accountID}", s.accountConfigGet(kind, defaults))
	r.Put("/{accountID}", s.accountConfigPut(kind))
	r.Delete("/{accountID}", s.accountConfigDelete(kind))
}
func (s *Server) accountConfigs(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		rows, e := s.db.QueryContext(r.Context(), `SELECT account_id,data FROM resource_records WHERE owner_id=? AND kind=? ORDER BY id`, u.ID, kind)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		defer rows.Close()
		out := map[string]any{}
		for rows.Next() {
			var ac sql.NullString
			var data string
			_ = rows.Scan(&ac, &data)
			var m map[string]any
			_ = json.Unmarshal([]byte(data), &m)
			out[ac.String] = m
		}
		write(w, 200, out)
	}
}
func (s *Server) accountConfigGet(kind string, defaults map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var data string
		e := s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND kind=? AND account_id=? ORDER BY id DESC LIMIT 1`, u.ID, kind, chi.URLParam(r, "accountID")).Scan(&data)
		if e == sql.ErrNoRows {
			copy := map[string]any{}
			for k, v := range defaults {
				copy[k] = v
			}
			write(w, 200, copy)
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
}
func (s *Server) accountConfigPut(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var p map[string]any
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		b, _ := json.Marshal(p)
		tx, e := s.db.BeginTx(r.Context(), nil)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		defer tx.Rollback()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND account_id=?`, u.ID, kind, chi.URLParam(r, "accountID"))
		_, e = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,data) VALUES(?,?,?,?)`, u.ID, kind, chi.URLParam(r, "accountID"), string(b))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		_ = tx.Commit()
		ok(w, "配置更新成功", p)
	}
}
func (s *Server) accountConfigDelete(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		res, e := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND account_id=?`, u.ID, kind, chi.URLParam(r, "accountID"))
		affected(w, res, e, "配置删除成功")
	}
}

func (s *Server) bulkAccountConfigs(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var settings map[string]map[string]any
		if decode(r, &settings) != nil || len(settings) == 0 {
			fail(w, http.StatusBadRequest, "请至少填写一个修改项")
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer tx.Rollback()
		updated := 0
		for accountID, setting := range settings {
			var owned int
			_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&owned)
			if owned == 0 || len(setting) == 0 {
				continue
			}
			payload, _ := json.Marshal(setting)
			_, _ = tx.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND account_id=?`, u.ID, kind, accountID)
			if _, err = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,data) VALUES(?,?,?,?)`, u.ID, kind, accountID, string(payload)); err != nil {
				fail(w, http.StatusInternalServerError, err.Error())
				return
			}
			updated++
		}
		if updated == 0 {
			write(w, http.StatusOK, response{Success: false, Message: "未找到任何可更新的账号"})
			return
		}
		if err = tx.Commit(); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		ok(w, fmt.Sprintf("已更新 %d 个账号的AI回复设置", updated), nil)
	}
}
