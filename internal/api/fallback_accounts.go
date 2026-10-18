package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) fallbackAccountList(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		query := `SELECT rr.id,rr.owner_id,u.username,rr.data,rr.created_at,rr.updated_at FROM resource_records rr LEFT JOIN xy_users u ON u.id=rr.owner_id WHERE rr.kind=? AND rr.enabled=1`
		args := []any{kind}
		if u.Role != "ADMIN" {
			query += ` AND rr.owner_id=?`
			args = append(args, u.ID)
		}
		query += ` ORDER BY rr.owner_id,json_extract(rr.data,'$.category_id') IS NOT NULL,rr.id DESC`
		rows, err := s.db.QueryContext(r.Context(), query, args...)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		type fallbackRow struct {
			id, owner             int64
			username              sql.NullString
			raw, created, updated string
		}
		records := []fallbackRow{}
		for rows.Next() {
			var record fallbackRow
			if rows.Scan(&record.id, &record.owner, &record.username, &record.raw, &record.created, &record.updated) == nil {
				records = append(records, record)
			}
		}
		rows.Close()
		items := []map[string]any{}
		for _, record := range records {
			item := map[string]any{"id": record.id, "owner_id": record.owner, "owner_username": nullText(record.username), "created_at": record.created, "updated_at": record.updated}
			mergeJSON(item, record.raw)
			categoryID := fallbackInt64(item["category_id"])
			if categoryID > 0 {
				var categoryRaw string
				if s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE id=? AND kind='listing_monitor_category'`, categoryID).Scan(&categoryRaw) == nil {
					category := map[string]any{}
					_ = json.Unmarshal([]byte(categoryRaw), &category)
					item["category_name"] = firstString(category, "name", "title")
				}
			} else {
				item["category_id"], item["category_name"] = nil, nil
			}
			accountIDs := stringSlice(item["account_ids"])
			item["account_ids"] = accountIDs
			item["accounts"] = s.describeFallbackAccounts(r, record.owner, accountIDs, u.Role == "ADMIN")
			items = append(items, item)
		}
		ok(w, "查询成功", items)
	}
}

func (s *Server) fallbackAccountUpsert(kind, successMessage string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var p struct {
			CategoryID *int64   `json:"category_id"`
			AccountIDs []string `json:"account_ids"`
			OwnerID    *int64   `json:"owner_id"`
		}
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		owner := u.ID
		if u.Role == "ADMIN" && p.OwnerID != nil && *p.OwnerID > 0 {
			owner = *p.OwnerID
		}
		if p.CategoryID != nil {
			var count int
			query := `SELECT count(*) FROM resource_records WHERE id=? AND kind='listing_monitor_category' AND enabled=1`
			args := []any{*p.CategoryID}
			if u.Role != "ADMIN" {
				query += ` AND owner_id=?`
				args = append(args, owner)
			}
			_ = s.db.QueryRowContext(r.Context(), query, args...).Scan(&count)
			if count == 0 {
				write(w, 200, response{Success: false, Message: "分类不存在或无权引用"})
				return
			}
		}
		accountIDs := uniqueStrings(p.AccountIDs)
		for _, accountID := range accountIDs {
			var count int
			query := `SELECT count(*) FROM xy_accounts WHERE account_id=?`
			args := []any{accountID}
			if u.Role != "ADMIN" {
				query += ` AND owner_id=?`
				args = append(args, owner)
			}
			_ = s.db.QueryRowContext(r.Context(), query, args...).Scan(&count)
			if count == 0 {
				write(w, 200, response{Success: false, Message: "以下账号不存在或不属于当前用户：" + accountID})
				return
			}
		}
		data, _ := json.Marshal(map[string]any{"category_id": p.CategoryID, "account_ids": accountIDs})
		var existing int64
		categoryValue := any(nil)
		if p.CategoryID != nil {
			categoryValue = *p.CategoryID
		}
		err := s.db.QueryRowContext(r.Context(), `SELECT id FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.category_id') IS ? ORDER BY enabled DESC,id LIMIT 1`, owner, kind, categoryValue).Scan(&existing)
		if err == sql.ErrNoRows {
			res, insertErr := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,?,1,?)`, owner, kind, string(data))
			if insertErr != nil {
				fail(w, 500, insertErr.Error())
				return
			}
			existing, _ = res.LastInsertId()
		} else if err == nil {
			_, err = s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=1,data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(data), existing)
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
		} else {
			fail(w, 500, err.Error())
			return
		}
		ok(w, successMessage, map[string]any{"id": existing, "owner_id": owner, "category_id": categoryValue, "account_ids": accountIDs, "accounts": s.describeFallbackAccounts(r, owner, accountIDs, u.Role == "ADMIN")})
	}
}

func (s *Server) fallbackAccountDelete(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		owner := u.ID
		if value, err := strconv.ParseInt(r.URL.Query().Get("owner_id"), 10, 64); err == nil && value > 0 && u.Role == "ADMIN" {
			owner = value
		}
		categoryValue := any(nil)
		if value, err := strconv.ParseInt(r.URL.Query().Get("category_id"), 10, 64); err == nil && value > 0 {
			categoryValue = value
		}
		res, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=0,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind=? AND enabled=1 AND json_extract(data,'$.category_id') IS ?`, owner, kind, categoryValue)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			write(w, 200, response{Success: false, Message: "配置不存在"})
			return
		}
		ok(w, "删除成功", nil)
	}
}

func (s *Server) describeFallbackAccounts(r *http.Request, owner int64, ids []string, admin bool) []map[string]any {
	result := []map[string]any{}
	for _, id := range ids {
		var name, status string
		query := `SELECT COALESCE(display_name,username,remark,account_id),status FROM xy_accounts WHERE account_id=?`
		args := []any{id}
		if !admin {
			query += ` AND owner_id=?`
			args = append(args, owner)
		}
		if s.db.QueryRowContext(r.Context(), query, args...).Scan(&name, &status) == nil {
			result = append(result, map[string]any{"account_id": id, "name": name, "enabled": status == "active", "exists": true})
		} else {
			result = append(result, map[string]any{"account_id": id, "name": id, "enabled": false, "exists": false})
		}
	}
	return result
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value := strings.TrimSpace(toString(item)); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func toString(value any) string {
	text, _ := value.(string)
	return text
}

func fallbackInt64(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case json.Number:
		result, _ := number.Int64()
		return result
	default:
		result, _ := strconv.ParseInt(strings.TrimSpace(toString(value)), 10, 64)
		return result
	}
}
