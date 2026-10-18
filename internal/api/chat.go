package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) registerChatRoutes(r chi.Router) {
	r.Get("/chat-new/accounts", s.chatAccounts)
	r.Get("/chat-new/connected-accounts", s.chatConnected)
	r.Post("/chat-new/connect/{account_id}", s.chatConnection(true))
	r.Post("/chat-new/disconnect/{account_id}", s.chatConnection(false))
	r.Get("/chat-new/conversations/{account_id}", s.chatConversations)
	r.Get("/chat-new/messages/{account_id}/{cid}", s.chatMessages)
	r.Post("/chat-new/send-message/{account_id}", s.chatSendMessage)
	r.Post("/chat-new/recall-message/{account_id}", s.chatRecallMessage)
	r.Post("/chat-new/send-image/{account_id}", s.chatSendImage)
	r.Get("/chat-new/customer-orders/{account_id}/{buyer_id}", s.chatCustomerOrders)
	r.Get("/chat-new/official-blacklist/{account_id}/{cid}", s.chatOfficialBlacklistQuery)
	r.Post("/chat-new/official-blacklist/{account_id}/{cid}/{action}", s.chatOfficialBlacklistChange)
	r.Post("/chat-new/avatars/{account_id}", s.chatAvatars)
	r.Get("/chat-new/account-profile/{account_id}", s.chatAccountProfile)
	// 快捷短语的响应体在原项目中直接是数组，不能复用通用资源接口
	// （其返回 data.list），否则前端工作台无法读取已保存的短语。
	r.Get("/chat-new/quick-phrases", s.chatQuickPhraseList)
	r.Post("/chat-new/quick-phrases", s.chatQuickPhraseCreate)
	r.Put("/chat-new/quick-phrases/{phrase_id}", s.chatQuickPhraseUpdate)
	r.Delete("/chat-new/quick-phrases/{phrase_id}", s.chatQuickPhraseDelete)
}

type chatQuickPhrase struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	SortOrder int64  `json:"sort_order"`
}

func readChatQuickPhrase(r *http.Request) (chatQuickPhrase, error) {
	var phrase chatQuickPhrase
	if err := decode(r, &phrase); err != nil {
		return phrase, fmt.Errorf("请求格式错误")
	}
	phrase.Title = strings.TrimSpace(phrase.Title)
	phrase.Content = strings.TrimSpace(phrase.Content)
	if phrase.Title == "" || phrase.Content == "" {
		return phrase, fmt.Errorf("标题和内容不能为空")
	}
	if len([]rune(phrase.Title)) > 80 || len([]rune(phrase.Content)) > 2000 {
		return phrase, fmt.Errorf("标题不能超过80字，内容不能超过2000字")
	}
	return phrase, nil
}

func (s *Server) chatQuickPhraseList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, data FROM resource_records WHERE owner_id=? AND kind='quick_phrase' ORDER BY CAST(COALESCE(json_extract(data,'$.sort_order'), 0) AS INTEGER), id`, u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []chatQuickPhrase{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		var phrase chatQuickPhrase
		_ = json.Unmarshal([]byte(raw), &phrase)
		phrase.ID = id
		items = append(items, phrase)
	}
	ok(w, "查询成功", items)
}

func (s *Server) chatQuickPhraseCreate(w http.ResponseWriter, r *http.Request) {
	phrase, err := readChatQuickPhrase(r)
	if err != nil {
		write(w, http.StatusBadRequest, response{Success: false, Message: err.Error()})
		return
	}
	u := current(r)
	data, _ := json.Marshal(phrase)
	result, err := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,'quick_phrase',1,?)`, u.ID, string(data))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	phrase.ID, _ = result.LastInsertId()
	ok(w, "快捷短语已添加", phrase)
}

func (s *Server) chatQuickPhraseUpdate(w http.ResponseWriter, r *http.Request) {
	phrase, err := readChatQuickPhrase(r)
	if err != nil {
		write(w, http.StatusBadRequest, response{Success: false, Message: err.Error()})
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "phrase_id"), 10, 64)
	if err != nil || id < 1 {
		write(w, http.StatusBadRequest, response{Success: false, Message: "快捷短语不存在"})
		return
	}
	u := current(r)
	phrase.ID = id
	data, _ := json.Marshal(phrase)
	result, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND owner_id=? AND kind='quick_phrase'`, string(data), id, u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "快捷短语不存在"})
		return
	}
	ok(w, "快捷短语已更新", phrase)
}

func (s *Server) chatQuickPhraseDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "phrase_id"), 10, 64)
	if err != nil || id < 1 {
		write(w, http.StatusBadRequest, response{Success: false, Message: "快捷短语不存在"})
		return
	}
	u := current(r)
	result, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE id=? AND owner_id=? AND kind='quick_phrase'`, id, u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "快捷短语不存在"})
		return
	}
	ok(w, "快捷短语已删除", nil)
}

func (s *Server) chatRecallMessage(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "account_id")
	if _, found := s.ownedChatAccount(r, accountID); !found {
		write(w, 200, response{Success: false, Message: "账号不存在或无权操作"})
		return
	}
	var p struct {
		MessageID   string `json:"messageId"`
		MessageTime int64  `json:"messageTime"`
	}
	if decode(r, &p) != nil || strings.TrimSpace(p.MessageID) == "" {
		write(w, 200, response{Success: false, Message: "缺少消息ID，无法撤回"})
		return
	}
	messageTime := p.MessageTime
	if messageTime < 1_000_000_000_000 {
		messageTime *= 1000
	}
	elapsed := time.Now().UnixMilli() - messageTime
	if elapsed < -10_000 || elapsed > 120_000 {
		write(w, 200, response{Success: false, Message: "消息发送超过两分钟，无法撤回"})
		return
	}
	if s.chatRecall == nil {
		write(w, 200, response{Success: false, Message: "账号未连接"})
		return
	}
	if err := s.chatRecall(r.Context(), accountID, p.MessageID); err != nil {
		write(w, 200, response{Success: false, Message: "撤回失败: " + err.Error()})
		return
	}
	ok(w, "消息已撤回", nil)
}

func (s *Server) chatAccounts(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=?`, u.ID).Scan(&total)
	rows, err := s.db.QueryContext(r.Context(), `SELECT account_id,COALESCE(display_name,''),COALESCE(remark,''),status FROM xy_accounts WHERE owner_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, u.ID, size, (p-1)*size)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, remark, status string
		_ = rows.Scan(&id, &name, &remark, &status)
		items = append(items, map[string]any{"account_id": id, "display_name": name, "remark": remark, "status": status, "connected": s.online != nil && s.online(id)})
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": total, "hasMore": p*size < total})
}

func (s *Server) chatConnected(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT account_id FROM xy_accounts WHERE owner_id=? AND status='active'`, u.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		if s.online != nil && s.online(id) {
			ids = append(ids, id)
		}
	}
	ok(w, "查询成功", map[string]any{"account_ids": ids})
}

func (s *Server) chatConnection(connect bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		status := "inactive"
		message := "账号IM连接已停用"
		if connect {
			status = "active"
			message = "账号IM连接已启用，连接器将在数秒内建立连接"
		}
		res, err := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET status=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, status, u.ID, chi.URLParam(r, "account_id"))
		affected(w, res, err, message)
	}
}

func (s *Server) chatConversations(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "account_id")
	limit := queryLimit(r, 20, 100)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,data,created_at FROM event_logs WHERE owner_id=? AND account_id=? AND kind IN ('incoming_message','outgoing_message') ORDER BY id DESC LIMIT 1000`, u.ID, accountID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	seen := map[string]bool{}
	items := []map[string]any{}
	for rows.Next() && len(items) < limit {
		var id int64
		var raw, created string
		_ = rows.Scan(&id, &raw, &created)
		msg := decodeChatLog(raw, created)
		cid := fmt.Sprint(msg["cid"])
		if cid == "" || seen[cid] {
			continue
		}
		seen[cid] = true
		items = append(items, map[string]any{"cid": cid, "rawCid": cid, "otherUserId": msg["senderId"], "otherUserName": msg["senderName"], "otherUserAvatar": "", "itemTitle": "", "lastMessageSummary": msg["text"], "lastMessageTime": msg["time"], "unreadCount": 0})
	}
	ok(w, "查询成功", map[string]any{"conversations": items, "hasMore": false, "nextCursor": nil})
}

func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID, cid := chi.URLParam(r, "account_id"), chi.URLParam(r, "cid")
	limit := queryLimit(r, 20, 100)
	// IM may echo messages sent by the seller as an incoming event.  Retain the
	// normal log-kind check, then use both account_id and Cookie's unb as a
	// backwards-compatible fallback for records persisted before IsSelf existed.
	selfIDs := map[string]bool{"self": true, accountID: true}
	var cookie string
	if s.db.QueryRowContext(r.Context(), `SELECT cookie FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&cookie) == nil {
		if unb := chatCookieValue(cookie, "unb"); unb != "" {
			selfIDs[unb] = true
		}
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,kind,data,created_at FROM event_logs WHERE owner_id=? AND account_id=? AND kind IN ('incoming_message','outgoing_message') ORDER BY id DESC LIMIT 1000`, u.ID, accountID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() && len(items) < limit {
		var id int64
		var kind, raw, created string
		_ = rows.Scan(&id, &kind, &raw, &created)
		msg := decodeChatLog(raw, created)
		if fmt.Sprint(msg["cid"]) != cid {
			continue
		}
		msg["messageId"] = strconv.FormatInt(id, 10)
		persistedSelf, _ := msg["isSelf"].(bool)
		msg["isSelf"] = kind == "outgoing_message" || persistedSelf || selfIDs[fmt.Sprint(msg["senderId"])]
		items = append(items, msg)
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	ok(w, "查询成功", map[string]any{"messages": items, "hasMore": false, "nextCursor": nil})
}

func chatCookieValue(raw, name string) string {
	for _, part := range strings.Split(raw, ";") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && strings.TrimSpace(key) == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Server) chatSendMessage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "account_id")
	var owned int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&owned)
	if owned == 0 {
		fail(w, 404, "账号不存在")
		return
	}
	var p struct {
		CID      string `json:"cid"`
		ToUserID string `json:"toUserId"`
		Text     string `json:"text"`
	}
	if decode(r, &p) != nil || p.CID == "" || p.ToUserID == "" || p.Text == "" {
		fail(w, 400, "会话、收件人和消息内容不能为空")
		return
	}
	if s.chatSend == nil {
		fail(w, 503, "IM连接器未就绪")
		return
	}
	messageID, err := s.chatSend(r.Context(), accountID, p.CID, p.ToUserID, p.Text)
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	raw, _ := json.Marshal(map[string]any{"cid": p.CID, "senderId": "self", "senderName": "我", "text": p.Text, "type": "text", "images": []string{}, "time": time.Now().UnixMilli()})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'outgoing_message',?,?,?)`, u.ID, accountID, p.Text, string(raw))
	ok(w, "发送成功", map[string]any{"messageId": messageID})
}

func (s *Server) chatCustomerOrders(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT COALESCE(order_no,''),COALESCE(item_id,''),COALESCE(buyer_id,''),COALESCE(status,''),COALESCE(delivery_method,''),COALESCE(data,'{}'),COALESCE(created_at,'') FROM xy_orders WHERE owner_id=? AND account_id=? AND buyer_id=? ORDER BY id DESC`, u.ID, chi.URLParam(r, "account_id"), chi.URLParam(r, "buyer_id"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var orderNo, itemID, buyerID, status, delivery, raw, created string
		if err := rows.Scan(&orderNo, &itemID, &buyerID, &status, &delivery, &raw, &created); err != nil {
			fail(w, 500, "读取客户订单失败")
			return
		}
		item := map[string]any{}
		mergeJSON(item, raw)
		item["order_no"], item["item_id"], item["buyer_id"] = orderNo, itemID, buyerID
		item["status"], item["delivery_method"], item["placed_at"] = status, delivery, created
		items = append(items, item)
	}
	ok(w, "查询成功", items)
}

func queryLimit(r *http.Request, fallback, maximum int) int {
	value, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if value < 1 {
		value = fallback
	}
	if value > maximum {
		value = maximum
	}
	return value
}

func decodeChatLog(raw, created string) map[string]any {
	source := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &source)
	value := func(names ...string) any {
		for _, name := range names {
			if v, exists := source[name]; exists {
				return v
			}
		}
		return ""
	}
	timestamp := time.Now().UnixMilli()
	if parsed, err := time.Parse("2006-01-02 15:04:05", created); err == nil {
		timestamp = parsed.UnixMilli()
	}
	if received := fmt.Sprint(value("ReceivedAt")); received != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, received); err == nil {
			timestamp = parsed.UnixMilli()
		}
	}
	images, _ := value("Images", "images").([]any)
	if images == nil {
		images = []any{}
	}
	return map[string]any{"cid": value("ChatID", "cid"), "senderId": value("SenderID", "senderId"), "senderName": value("SenderName", "senderName"), "type": value("Type", "type"), "text": value("Text", "text"), "images": images, "time": timestamp}
}

var _ sql.NullString
