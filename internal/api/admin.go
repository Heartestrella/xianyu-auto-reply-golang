package api

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/auth"
)

func (s *Server) adminRoutes(r chi.Router) {
	r.Use(s.adminOnly)
	r.Get("/users", s.adminUsers)
	r.Post("/users", s.adminUserCreate)
	r.Put("/users/{userID}", s.adminUserUpdate)
	r.Delete("/users/{userID}", s.adminUserDisable)
	r.Post("/users/{userID}/recharge", s.adminRecharge)
	r.Get("/stats", s.adminStats)
	r.Get("/stats/today", s.todayStats)
	r.Get("/logs", s.adminLogs)
	r.Post("/logs/clear", s.adminLogsClear)
	r.Get("/scheduled-tasks", s.scheduledTasks)
	r.Put("/scheduled-tasks/{code}", s.scheduledTaskUpdate)
	r.Post("/scheduled-tasks/{code}/trigger", s.scheduledTaskTrigger)
	r.Get("/data/{table}", s.adminTableData)
	r.Delete("/data/{table}", s.adminTableClear)
	r.Delete("/data/{table}/{id}", s.adminTableDelete)
	r.Get("/backup/list", s.adminBackupList)
	r.Get("/backup/download", s.adminBackupDownload)
	r.Post("/backup/upload", s.adminBackupUpload)
	r.Delete("/risk-control-logs", s.adminEventKindClear("risk_control"))
	r.Delete("/account-login-logs", s.adminEventKindClear("account_login"))
	for _, spec := range []struct{ path, kind string }{{"redelivery", "redelivery"}, {"rate", "rate"}, {"polish", "polish"}, {"red-flower", "red_flower"}, {"login-renew", "login_renew"}, {"close-notice", "close_notice"}} {
		r.Get("/"+spec.path+"-batches", s.adminBatchList(spec.kind))
		r.Get("/"+spec.path+"-batches/{batch_id}", s.adminBatchDetail(spec.kind))
		r.Delete("/"+spec.path+"-logs/clear", s.adminEventKindClear(spec.kind))
	}
	for _, spec := range []struct{ path, kind string }{{"cookies-refresh", "cookie_refresh"}, {"token-renewal", "token_renewal"}, {"api-cookie-renew", "api_cookie_renew"}} {
		r.Get("/"+spec.path+"-batches", s.adminBatchList(spec.kind))
		r.Get("/"+spec.path+"-batches/{batch_id}", s.adminBatchDetail(spec.kind))
	}
	r.Delete("/api-cookie-renew-logs/clear", s.adminEventKindClear("api_cookie_renew"))
	r.Delete("/risk-control-logs", s.adminEventKindClear("risk_control"))
	r.Delete("/account-login-logs", s.adminEventKindClear("account_login"))
	r.Get("/logs/export", s.adminLogsExport)
	r.Get("/log-files", s.adminLogFiles)
	r.Post("/reload-cache", func(w http.ResponseWriter, r *http.Request) {
		ok(w, "缓存已刷新", map[string]any{"reloaded_at": time.Now().Format(time.RFC3339)})
	})
	for _, spec := range []struct{ path, kind string }{{"redelivery", "redelivery"}, {"rate", "rate"}, {"polish", "polish"}, {"red-flower", "red_flower"}, {"login-renew", "login_renew"}, {"close-notice", "close_notice"}} {
		r.Get("/"+spec.path+"-batches", s.adminBatchList(spec.kind))
		r.Get("/"+spec.path+"-batches/{batch_id}", s.adminBatchDetail(spec.kind))
		r.Delete("/"+spec.path+"-logs/clear", s.adminEventKindClear(spec.kind))
	}
	for _, spec := range []struct{ path, kind string }{{"cookies-refresh", "cookie_refresh"}, {"token-renewal", "token_renewal"}, {"api-cookie-renew", "api_cookie_renew"}} {
		r.Get("/"+spec.path+"-batches", s.adminBatchList(spec.kind))
		r.Get("/"+spec.path+"-batches/{batch_id}", s.adminBatchDetail(spec.kind))
	}
	r.Delete("/api-cookie-renew-logs/clear", s.adminEventKindClear("api_cookie_renew"))
}

// personalAdminRoutes exposes only local maintenance operations that are
// useful to the owner. User, announcement, finance and service-management
// endpoints intentionally stay out of the personal build.
func (s *Server) personalAdminRoutes(r chi.Router) {
	r.Use(s.adminOnly)
	r.Get("/logs", s.adminLogs)
	r.Post("/logs/clear", s.adminLogsClear)
	r.Get("/logs/export", s.adminLogsExport)
	r.Get("/scheduled-tasks", s.scheduledTasks)
	r.Put("/scheduled-tasks/{code}", s.scheduledTaskUpdate)
	r.Post("/scheduled-tasks/{code}/trigger", s.scheduledTaskTrigger)
	r.Get("/data/{table}", s.adminTableData)
	r.Delete("/data/{table}", s.adminTableClear)
	r.Delete("/data/{table}/{id}", s.adminTableDelete)
	r.Get("/backup/list", s.adminBackupList)
	r.Get("/backup/download", s.adminBackupDownload)
	r.Post("/backup/upload", s.adminBackupUpload)
	r.Get("/log-files", s.adminLogFiles)
	r.Delete("/risk-control-logs", s.adminEventKindClear("risk_control"))
	r.Delete("/account-login-logs", s.adminEventKindClear("account_login"))
	for _, spec := range []struct{ path, kind string }{{"redelivery", "redelivery"}, {"rate", "rate"}, {"polish", "polish"}, {"red-flower", "red_flower"}, {"login-renew", "login_renew"}, {"close-notice", "close_notice"}} {
		r.Get("/"+spec.path+"-batches", s.adminBatchList(spec.kind))
		r.Get("/"+spec.path+"-batches/{batch_id}", s.adminBatchDetail(spec.kind))
		r.Delete("/"+spec.path+"-logs/clear", s.adminEventKindClear(spec.kind))
	}
	for _, spec := range []struct{ path, kind string }{{"cookies-refresh", "cookie_refresh"}, {"token-renewal", "token_renewal"}, {"api-cookie-renew", "api_cookie_renew"}} {
		r.Get("/"+spec.path+"-batches", s.adminBatchList(spec.kind))
		r.Get("/"+spec.path+"-batches/{batch_id}", s.adminBatchDetail(spec.kind))
	}
	r.Delete("/api-cookie-renew-logs/clear", s.adminEventKindClear("api_cookie_renew"))
}
func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if current(r).Role != "ADMIN" {
			fail(w, 403, "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit < 1 || limit > 500 {
		limit = 20
	}
	q := ` FROM xy_users WHERE 1=1`
	args := []any{}
	if name := strings.TrimSpace(r.URL.Query().Get("username")); name != "" {
		q += ` AND username LIKE ?`
		args = append(args, "%"+name+"%")
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+q, args...).Scan(&total)
	args = append(args, limit, offset)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,username,COALESCE(email,''),COALESCE(phone,''),role,status,account_limit,expire_at,created_at`+q+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	users := []map[string]any{}
	for rows.Next() {
		var id int64
		var username, email, phone, role, status, created string
		var limit sql.NullInt64
		var expire sql.NullString
		_ = rows.Scan(&id, &username, &email, &phone, &role, &status, &limit, &expire, &created)
		var cookies, cards int
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=?`, id).Scan(&cookies)
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM cards WHERE user_id=?`, id).Scan(&cards)
		users = append(users, map[string]any{"id": id, "username": username, "email": email, "phone": phone, "role": role, "status": status, "is_admin": role == "ADMIN", "account_limit": nullable(limit), "expire_at": nullText(expire), "cookie_count": cookies, "card_count": cards, "balance": s.userBalance(r.Context(), id), "created_at": created})
	}
	write(w, 200, map[string]any{"success": true, "users": users, "total": total})
}
func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	var p map[string]any
	if decode(r, &p) != nil || len(fmt.Sprint(p["password"])) < 6 {
		fail(w, 400, "请求参数无效")
		return
	}
	h, _ := auth.Hash(fmt.Sprint(p["password"]))
	res, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_users(username,email,phone,password_hash,status,role,account_limit,expire_at) VALUES(?,?,?,?,?,?,?,?)`, p["username"], nullString(fmt.Sprint(p["email"])), nullString(fmt.Sprint(p["phone"])), h, valueOr(p["status"], "ACTIVE"), valueOr(p["role"], "MEMBER"), p["account_limit"], p["expire_at"])
	if e != nil {
		write(w, 409, response{false, "用户名或邮箱已存在", nil})
		return
	}
	id, _ := res.LastInsertId()
	ok(w, "用户创建成功", map[string]any{"user": map[string]any{"id": id, "username": p["username"]}})
}
func (s *Server) adminUserUpdate(w http.ResponseWriter, r *http.Request) {
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	id := chi.URLParam(r, "userID")
	tx, _ := s.db.BeginTx(r.Context(), nil)
	defer tx.Rollback()
	if v := fmt.Sprint(p["password"]); v != "" && v != "<nil>" {
		h, _ := auth.Hash(v)
		_, _ = tx.ExecContext(r.Context(), `UPDATE xy_users SET password_hash=? WHERE id=?`, h, id)
	}
	_, e := tx.ExecContext(r.Context(), `UPDATE xy_users SET username=COALESCE(?,username),email=COALESCE(?,email),phone=COALESCE(?,phone),role=COALESCE(?,role),status=COALESCE(?,status),account_limit=?,expire_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, p["username"], p["email"], p["phone"], p["role"], p["status"], p["account_limit"], p["expire_at"], id)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	_ = tx.Commit()
	ok(w, "用户更新成功", map[string]any{"user": map[string]any{"id": id}})
}
func (s *Server) adminUserDisable(w http.ResponseWriter, r *http.Request) {
	if fmt.Sprint(current(r).ID) == chi.URLParam(r, "userID") {
		fail(w, 400, "不能停用当前管理员")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_users SET status='INACTIVE',updated_at=CURRENT_TIMESTAMP WHERE id=?`, chi.URLParam(r, "userID"))
	affected(w, res, e, "用户已停用")
}
func (s *Server) adminRecharge(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Amount string `json:"amount"`
		Remark string `json:"remark"`
	}
	if decode(r, &payload) != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	amount, err := decimalCents(payload.Amount)
	if err != nil || amount == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "调整金额必须是非零且最多两位小数"})
		return
	}
	targetID, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
	if err != nil {
		fail(w, http.StatusBadRequest, "用户ID无效")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	var exists int
	_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_users WHERE id=?`, targetID).Scan(&exists)
	if exists == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "用户不存在"})
		return
	}
	var raw string
	err = tx.QueryRowContext(r.Context(), `SELECT value FROM user_settings WHERE owner_id=? AND key='balance'`, targetID).Scan(&raw)
	if err == sql.ErrNoRows {
		raw = "0.00"
	} else if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	before, err := decimalCents(raw)
	if err != nil {
		fail(w, http.StatusInternalServerError, "账户余额格式错误")
		return
	}
	after := before + amount
	if after < 0 {
		write(w, http.StatusOK, response{Success: false, Message: "余额不足，不能扣减到负数"})
		return
	}
	beforeText, amountText, afterText := formatCents(before), formatCents(amount), formatCents(after)
	_, err = tx.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,'balance',?,'账户余额') ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, targetID, afterText)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO fund_flows(owner_id,operator_id,flow_type,amount,balance_before,balance_after,remark) VALUES(?,?,?,?,?,?,?)`, targetID, current(r).ID, "admin_adjust", amountText, beforeText, afterText, strings.TrimSpace(payload.Remark))
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, "余额调整成功", map[string]string{"balance_before": beforeText, "balance_after": afterText, "amount": amountText})
}
func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"success": true}
	queries := map[string]string{"total_users": `SELECT count(*) FROM xy_users`, "total_cookies": `SELECT count(*) FROM xy_accounts`, "active_cookies": `SELECT count(*) FROM xy_accounts WHERE status='active'`, "total_cards": `SELECT count(*) FROM cards`, "total_keywords": `SELECT count(*) FROM xy_keyword_rules`, "total_orders": `SELECT count(*) FROM xy_orders`, "today_reply_count": `SELECT count(*) FROM event_logs WHERE kind='auto_reply' AND date(created_at,'localtime')=date('now','localtime')`, "yesterday_reply_count": `SELECT count(*) FROM event_logs WHERE kind='auto_reply' AND date(created_at,'localtime')=date('now','localtime','-1 day')`}
	for k, q := range queries {
		var n int
		_ = s.db.QueryRowContext(r.Context(), q).Scan(&n)
		out[k] = n
	}
	online := 0
	rows, _ := s.db.QueryContext(r.Context(), `SELECT account_id FROM xy_accounts WHERE status='active'`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			if s.online != nil && s.online(id) {
				online++
			}
		}
	}
	out["online_cookies"] = online
	write(w, 200, out)
}
func (s *Server) todayStats(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"success": true}
	for k, q := range map[string]string{"today_users": `SELECT count(*) FROM xy_users WHERE date(created_at,'localtime')=date('now','localtime')`, "today_accounts": `SELECT count(*) FROM xy_accounts WHERE date(created_at,'localtime')=date('now','localtime')`, "today_orders": `SELECT count(*) FROM xy_orders WHERE date(created_at,'localtime')=date('now','localtime')`, "today_shipped": `SELECT count(*) FROM xy_orders WHERE status='shipped' AND date(updated_at,'localtime')=date('now','localtime')`, "today_pending": `SELECT count(*) FROM xy_orders WHERE status='pending_ship' AND date(created_at,'localtime')=date('now','localtime')`} {
		var n int
		_ = s.db.QueryRowContext(r.Context(), q).Scan(&n)
		out[k] = n
	}
	out["today_amount"] = 0
	out["today_agent_orders"] = 0
	write(w, 200, out)
}
func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines < 1 || lines > 5000 {
		lines = 200
	}
	query := `SELECT id,created_at,level,kind,COALESCE(message,'') FROM event_logs WHERE 1=1`
	args := []any{}
	if level := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("level"))); level != "" {
		if level == "WARNING" {
			level = "WARN"
		}
		query += ` AND upper(level) IN (?,?)`
		other := level
		if level == "WARN" {
			other = "WARNING"
		}
		args = append(args, level, other)
	}
	args = append(args, lines)
	rows, e := s.db.QueryContext(r.Context(), query+` ORDER BY id DESC LIMIT ?`, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	logs := []string{}
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var t, l, k, m string
		if err := rows.Scan(&id, &t, &l, &k, &m); err != nil {
			fail(w, 500, err.Error())
			return
		}
		local := databaseTimeInServerZone(t)
		logs = append(logs, fmt.Sprintf("%s %s [%s] %s", local, l, k, m))
		level := strings.ToLower(l)
		if level == "warn" {
			level = "warning"
		}
		items = append(items, map[string]any{"id": fmt.Sprint(id), "created_at": local, "level": level, "module": k, "message": m})
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	write(w, 200, map[string]any{"success": true, "data": items, "logs": logs, "total": len(logs)})
}
func (s *Server) adminLogsClear(w http.ResponseWriter, r *http.Request) {
	_, e := s.db.ExecContext(r.Context(), `DELETE FROM event_logs`)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "日志已清空", nil)
}

func (s *Server) adminEventKindClear(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := `DELETE FROM event_logs WHERE kind IN (?,?)`
		args := []any{kind, "scheduled_" + kind}
		if r.URL.Query().Has("days") {
			days, err := strconv.Atoi(r.URL.Query().Get("days"))
			if err != nil || days < 1 || days > 36500 {
				fail(w, 400, "保留天数必须为正整数")
				return
			}
			query += ` AND created_at<datetime('now',?)`
			args = append(args, fmt.Sprintf("-%d days", days))
		}
		res, err := s.db.ExecContext(r.Context(), query, args...)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		n, _ := res.RowsAffected()
		ok(w, "日志已清空", map[string]any{"deleted_count": n})
	}
}

func (s *Server) adminLogsExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="system_logs_%d.csv"`, time.Now().Unix()))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"ID", "时间", "级别", "类型", "账号", "消息", "数据"})
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,created_at,level,kind,COALESCE(account_id,''),message,data FROM event_logs ORDER BY id DESC`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var created, level, kind, account, message, data string
		if rows.Scan(&id, &created, &level, &kind, &account, &message, &data) == nil {
			_ = writer.Write([]string{fmt.Sprint(id), databaseTimeInServerZone(created), level, kind, account, message, data})
		}
	}
	writer.Flush()
}

func (s *Server) adminLogFiles(w http.ResponseWriter, r *http.Request) {
	var count int64
	var first, last sql.NullString
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*),min(created_at),max(created_at) FROM event_logs`).Scan(&count, &first, &last)
	write(w, 200, map[string]any{"success": true, "data": []map[string]any{{"name": "sqlite:event_logs", "size": count, "created_at": nullText(first), "updated_at": nullText(last)}}})
}

func (s *Server) scheduledTasks(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.QueryContext(r.Context(), `SELECT rowid,task_code,enabled,COALESCE(interval_seconds,0),COALESCE(last_run_at,''),COALESCE(next_run_at,''),data FROM scheduled_tasks ORDER BY rowid`)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var code, last, next, data string
		var enabled bool
		var interval int
		_ = rows.Scan(&id, &code, &enabled, &interval, &last, &next, &data)
		m := map[string]any{"id": id, "task_code": code, "task_name": code, "enabled": enabled, "interval_seconds": interval, "last_run_at": last, "next_run_at": next, "task_running": s.taskRunning != nil && s.taskRunning(code), "description": ""}
		var extra map[string]any
		_ = json.Unmarshal([]byte(data), &extra)
		for k, v := range extra {
			m[k] = v
		}
		out = append(out, m)
	}
	write(w, 200, map[string]any{"success": true, "data": out, "scheduler_running": true})
}
func (s *Server) scheduledTaskUpdate(w http.ResponseWriter, r *http.Request) {
	interval, _ := strconv.Atoi(r.URL.Query().Get("interval_seconds"))
	enabled := r.URL.Query().Get("enabled")
	if interval > 0 {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE scheduled_tasks SET interval_seconds=? WHERE task_code=?`, interval, chi.URLParam(r, "code"))
	}
	if enabled != "" {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE scheduled_tasks SET enabled=? WHERE task_code=?`, enabled == "true", chi.URLParam(r, "code"))
	}
	ok(w, "任务配置已更新", map[string]any{"task_code": chi.URLParam(r, "code")})
}
func (s *Server) scheduledTaskTrigger(w http.ResponseWriter, r *http.Request) {
	if s.triggerTask == nil {
		fail(w, 503, "调度器未就绪")
		return
	}
	code := chi.URLParam(r, "code")
	go func() { _ = s.triggerTask(context.Background(), code) }()
	ok(w, "任务已触发", nil)
}

var adminTables = map[string]string{"users": "xy_users", "accounts": "xy_accounts", "items": "xy_catalog_items", "orders": "xy_orders", "cards": "cards", "keywords": "xy_keyword_rules", "logs": "event_logs"}

func (s *Server) adminTableData(w http.ResponseWriter, r *http.Request) {
	table := adminTables[chi.URLParam(r, "table")]
	if table == "" {
		fail(w, 400, "不允许访问该表")
		return
	}
	rows, e := s.db.QueryContext(r.Context(), `SELECT * FROM `+table+` LIMIT 1000`)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		_ = rows.Scan(ptr...)
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				m[c] = string(b)
			} else {
				m[c] = vals[i]
			}
		}
		out = append(out, m)
	}
	write(w, 200, map[string]any{"success": true, "data": out, "columns": cols, "count": len(out)})
}
func (s *Server) adminTableClear(w http.ResponseWriter, r *http.Request) {
	table := adminTables[chi.URLParam(r, "table")]
	if table == "" || table == "users" {
		fail(w, 400, "不允许清空该表")
		return
	}
	_, e := s.db.ExecContext(r.Context(), `DELETE FROM `+table)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "数据已清空", nil)
}
func (s *Server) adminTableDelete(w http.ResponseWriter, r *http.Request) {
	table := adminTables[chi.URLParam(r, "table")]
	if table == "" || table == "users" {
		fail(w, 400, "不允许删除该记录")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM `+table+` WHERE id=?`, chi.URLParam(r, "id"))
	affected(w, res, e, "记录已删除")
}
func nullText(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}
func valueOr(v any, d string) any {
	if v == nil || fmt.Sprint(v) == "" || fmt.Sprint(v) == "<nil>" {
		return d
	}
	return v
}

func (s *Server) userBalance(ctx context.Context, userID int64) string {
	var balance string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM user_settings WHERE owner_id=? AND key='balance'`, userID).Scan(&balance); err != nil {
		return "0.00"
	}
	if cents, err := decimalCents(balance); err == nil {
		return formatCents(cents)
	}
	return "0.00"
}

func decimalCents(value string) (int64, error) {
	value = strings.TrimSpace(value)
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(strings.TrimPrefix(value, "+"), "-")
	parts := strings.Split(value, ".")
	if value == "" || len(parts) > 2 || len(parts) == 2 && len(parts[1]) > 2 {
		return 0, fmt.Errorf("invalid decimal")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > (1<<63-1)/100 {
		return 0, fmt.Errorf("invalid decimal")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		text := parts[1]
		if len(text) == 1 {
			text += "0"
		}
		if text != "" {
			fraction, err = strconv.ParseInt(text, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid decimal")
			}
		}
	}
	cents := whole*100 + fraction
	if negative {
		cents = -cents
	}
	return cents, nil
}

func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
