package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// externalSendMessage preserves the original API-key authenticated integration
// endpoint used by bots and other systems. It is deliberately outside JWT auth.
func (s *Server) externalSendMessage(w http.ResponseWriter, r *http.Request) {
	var p struct {
		APIKey   string `json:"api_key"`
		CookieID string `json:"cookie_id"`
		ChatID   string `json:"chat_id"`
		ToUserID string `json:"to_user_id"`
		Message  string `json:"message"`
	}
	if decode(r, &p) != nil {
		write(w, http.StatusOK, response{Success: false, Message: "请求格式错误"})
		return
	}
	clean := func(v string) string { return strings.ReplaceAll(strings.ReplaceAll(v, `\n`, ""), "\n", "") }
	p.APIKey, p.CookieID, p.ChatID, p.ToUserID, p.Message = clean(p.APIKey), clean(p.CookieID), clean(p.ChatID), clean(p.ToUserID), clean(p.Message)
	if p.APIKey == "" {
		write(w, http.StatusOK, response{Success: false, Message: "API秘钥不能为空"})
		return
	}
	secret := strings.TrimSpace(s.cfg.APISecretKey)
	if secret == "" {
		write(w, http.StatusOK, response{Success: false, Message: "消息发送接口未配置API秘钥，已禁用"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(p.APIKey), []byte(secret)) != 1 {
		write(w, http.StatusOK, response{Success: false, Message: "API秘钥验证失败"})
		return
	}
	for name, value := range map[string]string{"cookie_id": p.CookieID, "chat_id": p.ChatID, "to_user_id": p.ToUserID, "message": p.Message} {
		if value == "" {
			write(w, http.StatusOK, response{Success: false, Message: "参数 " + name + " 不能为空"})
			return
		}
	}
	if s.chatSend == nil {
		write(w, http.StatusOK, response{Success: false, Message: "IM连接器未就绪"})
		return
	}
	messageID, err := s.chatSend(r.Context(), p.CookieID, p.ChatID, p.ToUserID, p.Message)
	if err != nil {
		write(w, http.StatusOK, response{Success: false, Message: err.Error()})
		return
	}
	var ownerID int64
	_ = s.db.QueryRowContext(r.Context(), `SELECT owner_id FROM xy_accounts WHERE account_id=?`, p.CookieID).Scan(&ownerID)
	raw, _ := json.Marshal(map[string]any{"cid": p.ChatID, "senderId": "self", "senderName": "我", "toUserId": p.ToUserID, "text": p.Message, "type": "text", "messageId": messageID, "time": time.Now().UnixMilli()})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'outgoing_message',?,?,?)`, ownerID, p.CookieID, p.Message, string(raw))
	write(w, http.StatusOK, response{Success: true, Message: "消息发送成功"})
}
