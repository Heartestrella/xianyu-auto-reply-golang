package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/scheduler"
)

func (s *Server) updateScheduledTaskConfig(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "无法保存任务配置")
		return
	}
	defer tx.Rollback()
	var raw string
	var interval int
	var enabled bool
	if err = tx.QueryRowContext(r.Context(), `SELECT data,interval_seconds,enabled FROM scheduled_tasks WHERE task_code=?`, code).Scan(&raw, &interval, &enabled); err != nil {
		if err == sql.ErrNoRows {
			fail(w, 404, "任务不存在")
		} else {
			fail(w, 500, "无法读取任务配置")
		}
		return
	}
	cfg := map[string]any{}
	if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
		fail(w, 500, "任务配置格式错误")
		return
	}
	query := r.URL.Query()
	if query.Has("interval_seconds") {
		interval, err = strconv.Atoi(query.Get("interval_seconds"))
		if err != nil || interval < 1 {
			fail(w, 400, "间隔时间必须为正整数秒")
			return
		}
	}
	if query.Has("enabled") {
		enabled, err = strconv.ParseBool(query.Get("enabled"))
		if err != nil {
			fail(w, 400, "启用状态无效")
			return
		}
	}
	if query.Has("run_start_time") || query.Has("run_end_time") {
		if code != "polish" {
			fail(w, 400, "仅擦亮任务支持每日执行时段")
			return
		}
		start, end := scheduler.PolishWindow(cfg)
		if query.Has("run_start_time") {
			start = query.Get("run_start_time")
		}
		if query.Has("run_end_time") {
			end = query.Get("run_end_time")
		}
		if err := scheduler.ValidatePolishWindow(start, end); err != nil {
			fail(w, 400, err.Error())
			return
		}
		cfg["run_start_time"], cfg["run_end_time"] = start, end
	}
	encoded, _ := json.Marshal(cfg)
	if _, err = tx.ExecContext(r.Context(), `UPDATE scheduled_tasks SET enabled=?,interval_seconds=?,data=? WHERE task_code=?`, enabled, interval, string(encoded), code); err != nil {
		fail(w, 500, "无法保存任务配置")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "无法保存任务配置")
		return
	}
	cfg["task_code"], cfg["enabled"], cfg["interval_seconds"] = code, enabled, interval
	ok(w, "任务配置已更新", cfg)
}
