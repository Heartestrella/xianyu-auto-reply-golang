package api

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type backupInfo struct {
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	ModifiedTime string `json:"modified_time"`
}

func (s *Server) backupDirectory() string { return filepath.Join(s.cfg.DataDir, "backups") }

func (s *Server) adminBackupList(w http.ResponseWriter, r *http.Request) {
	directory := s.backupDirectory()
	_ = os.MkdirAll(directory, 0755)
	entries, err := os.ReadDir(directory)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	files := []backupInfo{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".db") {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			files = append(files, backupInfo{Name: entry.Name(), Size: info.Size(), ModifiedTime: info.ModTime().Format(time.RFC3339)})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModifiedTime > files[j].ModifiedTime })
	write(w, 200, map[string]any{"backups": files, "total": len(files)})
}

func (s *Server) adminBackupDownload(w http.ResponseWriter, r *http.Request) {
	directory := s.backupDirectory()
	if err := os.MkdirAll(directory, 0755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("filename"))
	if name != "" {
		name = filepath.Base(name)
		if !strings.HasSuffix(strings.ToLower(name), ".db") {
			fail(w, 400, "备份文件名无效")
			return
		}
		path := filepath.Join(directory, name)
		if _, err := os.Stat(path); err != nil {
			fail(w, 404, "备份文件不存在")
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		http.ServeFile(w, r, path)
		return
	}
	name = "xianyu-" + time.Now().Format("20060102-150405") + ".db"
	path := filepath.Join(directory, name)
	if _, err := s.db.ExecContext(r.Context(), `VACUUM INTO ?`, path); err != nil {
		fail(w, 500, "创建SQLite备份失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	http.ServeFile(w, r, path)
}

func (s *Server) adminBackupUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	if err := r.ParseMultipartForm(2 << 30); err != nil {
		fail(w, 400, "备份文件过大或格式无效")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择SQLite备份文件")
		return
	}
	defer file.Close()
	directory := s.backupDirectory()
	if err = os.MkdirAll(directory, 0755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	name := "uploaded-" + time.Now().Format("20060102-150405") + "-" + randomHex(8) + ".db"
	path := filepath.Join(directory, name)
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	_, copyErr := io.Copy(target, file)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		fail(w, 500, "保存备份文件失败")
		return
	}
	check, err := sql.Open("sqlite", path+"?mode=ro")
	if err == nil {
		err = check.PingContext(r.Context())
		_ = check.Close()
	}
	if err != nil {
		_ = os.Remove(path)
		fail(w, 400, "上传文件不是有效的SQLite数据库")
		return
	}
	ok(w, "备份文件已安全保存；为避免在线覆盖损坏，需停机后手动替换数据库", map[string]any{"filename": name, "original_filename": filepath.Base(header.Filename), "restore_required": true})
}
