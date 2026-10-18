package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) faceVerificationNotifications(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	query := ` FROM event_logs e LEFT JOIN xy_accounts a ON a.account_id=e.account_id WHERE e.kind='face_verification'`
	args := []any{}
	if u.Role != "ADMIN" {
		query += ` AND e.owner_id=?`
		args = append(args, u.ID)
	}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		query += ` AND a.id=?`
		args = append(args, accountID)
	}
	if start := strings.TrimSpace(r.URL.Query().Get("start_date")); start != "" {
		query += ` AND date(e.created_at)>=date(?)`
		args = append(args, start)
	}
	if end := strings.TrimSpace(r.URL.Query().Get("end_date")); end != "" {
		query += ` AND date(e.created_at)<=date(?)`
		args = append(args, end)
	}
	var total int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total); err != nil {
		fail(w, 500, err.Error())
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT e.id,a.id,COALESCE(a.username,a.display_name,a.account_id,'未知账号'),e.message,e.data,e.created_at`+query+` ORDER BY e.id DESC LIMIT ? OFFSET ?`, append(args, size, (p-1)*size)...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var accountID sql.NullInt64
		var accountName, message, raw, created string
		if rows.Scan(&id, &accountID, &accountName, &message, &raw, &created) != nil {
			continue
		}
		extra := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &extra)
		items = append(items, map[string]any{"id": id, "account_id": nullable(accountID), "account_name": accountName, "title": firstNonemptyAPI(firstString(extra, "title"), "需要人脸验证"), "content": message, "type": "face_verification", "is_read": extra["is_read"] == true, "created_at": created, "extra_data": extra})
	}
	ok(w, "查询成功", map[string]any{"list": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}

func (s *Server) faceVerificationAccount(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "account_id"), 10, 64)
	var accountID, accountName string
	query := `SELECT account_id,COALESCE(username,display_name,account_id) FROM xy_accounts WHERE id=?`
	args := []any{id}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if s.db.QueryRowContext(r.Context(), query, args...).Scan(&accountID, &accountName) != nil {
		write(w, 200, response{Success: false, Message: "账号不存在或无权查看"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 10
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,message,data,created_at FROM event_logs WHERE kind='face_verification' AND account_id=? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var eventID int64
		var message, raw, created string
		_ = rows.Scan(&eventID, &message, &raw, &created)
		extra := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &extra)
		items = append(items, map[string]any{"id": eventID, "title": firstNonemptyAPI(firstString(extra, "title"), "需要人脸验证"), "content": message, "type": "face_verification", "is_read": extra["is_read"] == true, "created_at": created, "extra_data": extra})
	}
	ok(w, "查询成功", map[string]any{"account_id": id, "account_name": accountName, "notifications": items, "total": len(items)})
}

func (s *Server) faceVerificationRead(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := chi.URLParam(r, "notification_id")
	query := `UPDATE event_logs SET data=json_set(COALESCE(data,'{}'),'$.is_read',json('true')) WHERE id=? AND kind='face_verification'`
	args := []any{id}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	res, err := s.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{Success: false, Message: "通知不存在或无权操作"})
		return
	}
	ok(w, "标记成功", map[string]any{"notification_id": smart(id)})
}

var safeAccountFilename = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (s *Server) faceVerificationScreenshot(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "account_id")
	if !safeAccountFilename.MatchString(accountID) || !s.canAccessAccount(r, accountID) {
		write(w, 200, response{Success: false, Message: "账号不存在或无权查看该账号截图"})
		return
	}
	files, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "uploads", "face", "face_verify_"+accountID+"_*.jpg"))
	if len(files) == 0 {
		write(w, 200, response{Success: false, Message: "未找到验证截图"})
		return
	}
	latest := files[0]
	latestInfo, _ := os.Stat(latest)
	for _, file := range files[1:] {
		if info, err := os.Stat(file); err == nil && info.ModTime().After(latestInfo.ModTime()) {
			latest, latestInfo = file, info
		}
	}
	filename := filepath.Base(latest)
	ok(w, "获取成功", map[string]any{"screenshot": map[string]any{"filename": filename, "account_id": accountID, "path": "/uploads/face/" + filename, "size": latestInfo.Size(), "created_time": latestInfo.ModTime().Unix(), "created_time_str": latestInfo.ModTime().Format("2006-01-02 15:04:05")}})
}

func (s *Server) faceVerificationScreenshotDelete(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "account_id")
	if !safeAccountFilename.MatchString(accountID) || !s.canAccessAccount(r, accountID) {
		write(w, 200, response{Success: false, Message: "账号不存在或无权删除该账号截图"})
		return
	}
	files, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "uploads", "face", "face_verify_"+accountID+"_*.jpg"))
	deleted := 0
	for _, file := range files {
		if os.Remove(file) == nil {
			deleted++
		}
	}
	message := "未找到截图文件"
	if deleted > 0 {
		message = "删除成功，共删除 " + strconv.Itoa(deleted) + " 个截图"
	}
	ok(w, message, map[string]any{"deleted_count": deleted})
}

func (s *Server) canAccessAccount(r *http.Request, accountID string) bool {
	u := current(r)
	query := `SELECT count(*) FROM xy_accounts WHERE account_id=?`
	args := []any{accountID}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	var count int
	_ = s.db.QueryRowContext(r.Context(), query, args...).Scan(&count)
	return count > 0
}
