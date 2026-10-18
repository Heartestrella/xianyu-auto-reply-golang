package api

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

var imageTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}
var videoTypes = map[string]string{"video/mp4": ".mp4", "video/quicktime": ".mov", "video/webm": ".webm"}

func (s *Server) uploadPublishImages(w http.ResponseWriter, r *http.Request) {
	s.uploadMedia(w, r, "images", imageTypes, 10<<20, 9)
}

func (s *Server) uploadPublishVideos(w http.ResponseWriter, r *http.Request) {
	s.uploadMedia(w, r, "videos", videoTypes, 200<<20, 3)
}

func (s *Server) uploadGeneralImage(w http.ResponseWriter, r *http.Request) {
	s.uploadSingleImage(w, r, "images", false)
}

func (s *Server) uploadCardImage(w http.ResponseWriter, r *http.Request) {
	s.uploadSingleImage(w, r, "cards", false)
}

func (s *Server) uploadDefaultReplyImage(w http.ResponseWriter, r *http.Request) {
	s.uploadSingleImage(w, r, "default_reply", true)
}

func (s *Server) uploadConfirmReceiptImage(w http.ResponseWriter, r *http.Request) {
	s.uploadSingleImage(w, r, "confirm_receipt", true)
}

func (s *Server) uploadSingleImage(w http.ResponseWriter, r *http.Request, directory string, requireAccount bool) {
	if requireAccount {
		u := current(r)
		accountID := strings.TrimSpace(chi.URLParam(r, "account_id"))
		var count int
		query := `SELECT count(*) FROM xy_accounts WHERE account_id=?`
		args := []any{accountID}
		if u.Role != "ADMIN" {
			query += ` AND owner_id=?`
			args = append(args, u.ID)
		}
		_ = s.db.QueryRowContext(r.Context(), query, args...).Scan(&count)
		if count == 0 {
			fail(w, http.StatusNotFound, "账号不存在")
			return
		}
	}
	imageURL, err := s.receiveSingleImage(w, r, directory, 10<<20)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	write(w, 200, map[string]any{"success": true, "message": "图片上传成功", "image_url": imageURL, "data": map[string]any{"image_url": imageURL}})
}

func (s *Server) receiveSingleImage(w http.ResponseWriter, r *http.Request, directory string, maxSize int64) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+(2<<20))
	if err := r.ParseMultipartForm(maxSize + (2 << 20)); err != nil {
		return "", fmt.Errorf("上传内容过大或格式无效")
	}
	files := r.MultipartForm.File["image"]
	if len(files) == 0 {
		files = r.MultipartForm.File["file"]
	}
	if len(files) != 1 {
		return "", fmt.Errorf("请选择一张图片")
	}
	root := filepath.Join(s.cfg.DataDir, "uploads", directory)
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	_, imageURL, err := saveUploadedFile(root, directory, files[0], imageTypes, maxSize)
	return imageURL, err
}

func (s *Server) uploadPaymentQRCode(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	imageURL, err := s.receiveSingleImage(w, r, "payment-qrcode", 10<<20)
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	paymentType := strings.TrimSpace(r.FormValue("payment_type"))
	if paymentType != "alipay" && paymentType != "wechat" {
		_ = os.Remove(filepath.Join(s.cfg.DataDir, filepath.FromSlash(strings.TrimPrefix(imageURL, "/"))))
		write(w, 200, response{Success: false, Message: "收款方式无效，必须是 alipay 或 wechat"})
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	for _, item := range []struct{ key, value, description string }{{"payment_qrcode", imageURL, "收款码图片路径"}, {"payment_type", paymentType, "收款方式"}} {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,?,?,?) ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,description=excluded.description,updated_at=CURRENT_TIMESTAMP`, u.ID, item.key, item.value, item.description); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "收款码上传成功", map[string]any{"image_url": imageURL, "payment_type": paymentType})
}

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request, directory string, allowed map[string]string, maxFileSize int64, maxFiles int) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize*int64(maxFiles)+(4<<20))
	if err := r.ParseMultipartForm(maxFileSize * int64(maxFiles)); err != nil {
		fail(w, http.StatusBadRequest, "上传内容过大或格式无效")
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		files = r.MultipartForm.File["file"]
	}
	if len(files) == 0 || len(files) > maxFiles {
		fail(w, http.StatusBadRequest, fmt.Sprintf("请选择1到%d个文件", maxFiles))
		return
	}
	root := filepath.Join(s.cfg.DataDir, "uploads", directory)
	if err := os.MkdirAll(root, 0755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	paths, urls := []string{}, []string{}
	media := []map[string]any{}
	for _, header := range files {
		path, url, err := saveUploadedFile(root, directory, header, allowed, maxFileSize)
		if err != nil {
			for _, created := range paths {
				_ = os.Remove(created)
			}
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		paths = append(paths, path)
		urls = append(urls, url)
		if directory == "videos" {
			media = append(media, map[string]any{"url": url, "path": path, "name": filepath.Base(header.Filename), "size": header.Size})
		}
	}
	data := map[string]any{"paths": paths, "urls": urls}
	if directory == "videos" {
		data["videos"] = media
	}
	ok(w, "上传成功", data)
}

func saveUploadedFile(root, directory string, header *multipart.FileHeader, allowed map[string]string, maxSize int64) (string, string, error) {
	if header.Size <= 0 || header.Size > maxSize {
		return "", "", fmt.Errorf("文件 %s 大小无效", filepath.Base(header.Filename))
	}
	source, err := header.Open()
	if err != nil {
		return "", "", err
	}
	defer source.Close()
	peek := make([]byte, 512)
	n, err := io.ReadFull(source, peek)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", "", fmt.Errorf("读取文件失败")
	}
	contentType := http.DetectContentType(peek[:n])
	extension, ok := allowed[contentType]
	if !ok {
		return "", "", fmt.Errorf("不支持的文件类型: %s", contentType)
	}
	name := fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), randomHex(16), extension)
	path := filepath.Join(root, name)
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", "", err
	}
	okWrite := false
	defer func() {
		_ = target.Close()
		if !okWrite {
			_ = os.Remove(path)
		}
	}()
	if _, err = target.Write(peek[:n]); err == nil {
		_, err = io.CopyN(target, source, maxSize-int64(n)+1)
		if err == io.EOF {
			err = nil
		}
	}
	if err != nil {
		return "", "", err
	}
	if err = target.Close(); err != nil {
		return "", "", err
	}
	okWrite = true
	absolute, _ := filepath.Abs(path)
	return absolute, "/uploads/" + strings.Trim(directory, "/") + "/" + name, nil
}
