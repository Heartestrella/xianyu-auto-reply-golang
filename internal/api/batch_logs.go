package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

const batchKeySQL = `COALESCE(NULLIF(CAST(json_extract(data,'$.batch_id') AS TEXT),''),CAST(id AS TEXT))`
const batchStatusSQL = `COALESCE(NULLIF(json_extract(data,'$.status'),''),CASE WHEN lower(level)='error' OR json_extract(data,'$.success')=0 THEN 'failed' ELSE 'success' END)`

func batchSummary(id, created, updated string, total, accounts int, counts map[string]int) map[string]any {
	failed := counts["failed"] + counts["session_expired"]
	status := "completed"
	if failed == total {
		status = "failed"
	} else if failed > 0 {
		status = "partial"
	}
	return map[string]any{
		"batch_id": id, "status": status, "executed_at": created, "created_at": created, "updated_at": updated,
		"total": total, "total_orders": total, "total_items": total, "total_accounts": accounts,
		"success_count": counts["success"], "failed_count": failed, "initialized_count": counts["initialized"],
		"cookie_updated_count": counts["cookie_updated"], "token_refreshed_count": counts["token_refreshed"], "session_expired_count": counts["session_expired"],
	}
}

func (s *Server) adminBatchList(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, size := page(r)
		query, args := appendDateFilters(r, ` FROM event_logs WHERE kind IN (?,?)`, []any{kind, "scheduled_" + kind})
		var total int
		if err := s.db.QueryRowContext(r.Context(), `SELECT count(DISTINCT `+batchKeySQL+`)`+query, args...).Scan(&total); err != nil {
			fail(w, 500, err.Error())
			return
		}
		statuses := []string{"success", "failed", "initialized", "cookie_updated", "token_refreshed", "session_expired"}
		selectSQL := `SELECT ` + batchKeySQL + `,min(created_at),max(created_at),count(*),count(DISTINCT account_id)`
		for _, status := range statuses {
			selectSQL += `,sum(CASE WHEN (` + batchStatusSQL + `)='` + status + `' THEN 1 ELSE 0 END)`
		}
		pageArgs := append(append([]any{}, args...), size, (p-1)*size)
		rows, err := s.db.QueryContext(r.Context(), selectSQL+query+` GROUP BY 1 ORDER BY max(id) DESC LIMIT ? OFFSET ?`, pageArgs...)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, created, updated string
			var count, accounts int
			values := make([]int, len(statuses))
			dest := []any{&id, &created, &updated, &count, &accounts}
			for i := range values {
				dest = append(dest, &values[i])
			}
			if err := rows.Scan(dest...); err != nil {
				fail(w, 500, err.Error())
				return
			}
			counts := map[string]int{}
			for i, status := range statuses {
				counts[status] = values[i]
			}
			items = append(items, batchSummary(id, created, updated, count, accounts, counts))
		}
		if err := rows.Err(); err != nil {
			fail(w, 500, err.Error())
			return
		}
		ok(w, "查询成功", map[string]any{"items": items, "list": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
	}
}

func (s *Server) adminBatchDetail(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "batch_id")
		rows, err := s.db.QueryContext(r.Context(), `SELECT id,owner_id,COALESCE(account_id,''),level,COALESCE(message,''),COALESCE(data,'{}'),created_at,`+batchStatusSQL+` FROM event_logs WHERE kind IN (?,?) AND `+batchKeySQL+`=? ORDER BY id`, kind, "scheduled_"+kind, id)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		counts, accounts := map[string]int{}, map[string]bool{}
		var first, last string
		for rows.Next() {
			var rowID int64
			var owner any
			var account, level, message, raw, created, status string
			if err := rows.Scan(&rowID, &owner, &account, &level, &message, &raw, &created, &status); err != nil {
				fail(w, 500, err.Error())
				return
			}
			m := map[string]any{}
			mergeJSON(m, raw)
			m["id"], m["owner_id"], m["account_id"], m["batch_id"] = rowID, owner, account, id
			m["level"], m["message"], m["created_at"], m["status"] = level, message, created, status
			m["error_message"] = firstText(m["error_message"], m["error"])
			if (status == "failed" || status == "session_expired") && m["error_message"] == "" {
				m["error_message"] = message
			}
			for _, field := range []string{"updated_cookie_count"} {
				if m[field] == nil {
					m[field] = 0
				}
			}
			if first == "" || created < first {
				first = created
			}
			if created > last {
				last = created
			}
			counts[status]++
			if strings.TrimSpace(account) != "" {
				accounts[account] = true
			}
			items = append(items, m)
		}
		if err := rows.Err(); err != nil {
			fail(w, 500, err.Error())
			return
		}
		if len(items) == 0 {
			fail(w, 404, "批次不存在")
			return
		}
		data := batchSummary(id, first, last, len(items), len(accounts), counts)
		data["logs"], data["items"] = items, items
		ok(w, "查询成功", data)
	}
}
