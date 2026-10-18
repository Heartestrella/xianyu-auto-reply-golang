package api

import (
	"database/sql"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) locateBackup(name string, recorded sql.NullString) string {
	if name == "" || filepath.Base(name) != name {
		return ""
	}
	root, _ := filepath.Abs(s.backupDirectory())
	candidates := []string{filepath.Join(root, name)}
	if recorded.Valid {
		candidates = append(candidates, recorded.String)
	}
	for _, candidate := range candidates {
		abs, e := filepath.Abs(candidate)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(root, abs)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if st, e := os.Stat(abs); e == nil && !st.IsDir() {
			return abs
		}
	}
	return ""
}
func (s *Server) dbBackupLogs(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "ADMIN" {
		write(w, 403, response{Success: false, Message: "需要管理员权限"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	where := "1=1"
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	if start := r.URL.Query().Get("start_date"); start != "" {
		where += " AND created_at>=?"
		args = append(args, start+" 00:00:00")
	}
	if end := r.URL.Query().Get("end_date"); end != "" {
		where += " AND created_at<=?"
		args = append(args, end+" 23:59:59")
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_db_backup_log WHERE `+where, args...).Scan(&total)
	args = append(args, limit, offset)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,status,file_name,file_path,file_size,table_count,total_rows,duration_ms,error_message,created_at FROM xy_db_backup_log WHERE `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if e != nil {
		write(w, 200, map[string]any{"success": false, "message": "加载数据库备份日志失败: " + e.Error(), "data": []any{}, "total": 0, "limit": limit, "offset": offset})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var status, created string
		var name, path, errorMessage sql.NullString
		var size, tables, totalRows, duration sql.NullInt64
		_ = rows.Scan(&id, &status, &name, &path, &size, &tables, &totalRows, &duration, &errorMessage, &created)
		items = append(items, map[string]any{"id": id, "status": status, "file_name": nullText(name), "file_path": nullText(path), "file_size": nullable(size), "table_count": nullable(tables), "total_rows": nullable(totalRows), "duration_ms": nullable(duration), "error_message": nullText(errorMessage), "downloadable": s.locateBackup(name.String, path) != "", "created_at": created})
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) dbBackupDownload(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "ADMIN" {
		write(w, 403, response{Success: false, Message: "需要管理员权限"})
		return
	}
	var name string
	var path sql.NullString
	e := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(file_name,''),file_path FROM xy_db_backup_log WHERE id=?`, chi.URLParam(r, "log_id")).Scan(&name, &path)
	if e != nil {
		write(w, 200, response{Success: false, Message: "备份文件不存在或已被删除，无法下载"})
		return
	}
	file := s.locateBackup(name, path)
	if file == "" {
		write(w, 200, response{Success: false, Message: "备份文件不存在或已被删除，无法下载"})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeFile(w, r, file)
}
