package api

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

var qrCodeTypes = map[string]bool{"wechat": true, "qq": true, "wechat_official": true, "telegram": true, "reward": true}

func (s *Server) getQRCode(w http.ResponseWriter, r *http.Request) {
	qrType := chi.URLParam(r, "qrcode_type")
	if !qrCodeTypes[qrType] {
		write(w, 200, response{Success: false, Message: "无效的二维码类型"})
		return
	}
	var value string
	err := s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key=?`, "qrcode."+qrType).Scan(&value)
	if err == sql.ErrNoRows || strings.TrimSpace(value) == "" {
		write(w, 200, response{Success: false, Message: "二维码未配置"})
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "", map[string]any{"image_url": value})
}

func (s *Server) uploadQRCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	qrType := chi.URLParam(r, "qrcode_type")
	if !qrCodeTypes[qrType] {
		write(w, 200, response{Success: false, Message: "无效的二维码类型，只支持 wechat、qq、wechat_official、telegram 或 reward"})
		return
	}
	imageURL, err := s.receiveSingleImage(w, r, "qrcode", 2<<20)
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO system_settings(key,value,description,is_public) VALUES(?,?,?,1) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, "qrcode."+qrType, imageURL, qrType+"群二维码")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "上传成功", map[string]any{"image_url": imageURL})
}
