package api

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) chatSendImage(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "account_id")
	account, found := s.ownedChatAccount(r, accountID)
	if !found {
		write(w, 200, response{Success: false, Message: "账号不存在或无权操作"})
		return
	}
	if s.chatImage == nil || (s.online != nil && !s.online(accountID)) {
		write(w, 200, response{Success: false, Message: "账号未连接，请先连接"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 11<<20)
	if err := r.ParseMultipartForm(11 << 20); err != nil {
		write(w, 200, response{Success: false, Message: "图片大小不能超过10MB"})
		return
	}
	cid, toUserID := strings.TrimSpace(r.FormValue("cid")), strings.TrimSpace(r.FormValue("toUserId"))
	if cid == "" || toUserID == "" {
		write(w, 200, response{Success: false, Message: "会话和收件人不能为空"})
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		write(w, 200, response{Success: false, Message: "请上传图片文件"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (10<<20)+1))
	if err != nil || len(data) == 0 {
		write(w, 200, response{Success: false, Message: "上传文件为空"})
		return
	}
	if len(data) > 10<<20 {
		write(w, 200, response{Success: false, Message: "图片大小不能超过10MB"})
		return
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		write(w, 200, response{Success: false, Message: "请上传图片文件"})
		return
	}
	width, height := 800, 600
	if cfg, _, e := image.DecodeConfig(bytes.NewReader(data)); e == nil && cfg.Width > 0 && cfg.Height > 0 {
		width, height = cfg.Width, cfg.Height
	}
	imageURL, err := xianyu.UploadChatImage(r.Context(), account.Cookie, accountProxy(account), header.Filename, data)
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	messageID, err := s.chatImage(r.Context(), accountID, cid, toUserID, imageURL, width, height)
	if err != nil {
		write(w, 200, response{Success: false, Message: "发送失败：" + err.Error()})
		return
	}
	u := current(r)
	raw, _ := json.Marshal(map[string]any{"cid": cid, "senderId": "self", "senderName": "我", "toUserId": toUserID, "text": "[图片]", "type": "image", "images": []string{imageURL}, "messageId": messageID, "time": time.Now().UnixMilli()})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'outgoing_message',?,'[图片]',?)`, u.ID, accountID, string(raw))
	ok(w, "发送成功", map[string]any{"messageId": messageID, "imageUrl": imageURL})
}
