package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) orderManualDelivery(w http.ResponseWriter, r *http.Request) {
	s.agreePickupMu.Lock()
	defer s.agreePickupMu.Unlock()
	var p struct {
		OrderNo string `json:"order_no"`
	}
	if decode(r, &p) != nil || strings.TrimSpace(p.OrderNo) == "" {
		write(w, 200, response{Success: false, Message: "订单号不能为空"})
		return
	}
	o, msg := s.loadActionableOrder(r, strings.TrimSpace(p.OrderNo))
	if msg != "" {
		write(w, 200, response{Success: false, Message: msg})
		return
	}
	raw := map[string]any{}
	_ = json.Unmarshal([]byte(o.Data), &raw)
	if truthy(raw["card_only_delivered"]) {
		write(w, 200, response{Success: false, Message: "该订单已执行过只发卡券，本次未重复发送；如需确认平台发货，请关闭该开关后使用无物流发货"})
		return
	}
	if oneOf(strings.ToLower(o.Status), "shipped", "completed") {
		write(w, 200, response{Success: false, Message: "该订单已发货，本次未重复发送卡券"})
		return
	}
	if o.ItemID == "" || o.BuyerID == "" {
		write(w, 200, response{Success: false, Message: "订单缺少商品或买家信息"})
		return
	}
	chatID := firstText(raw["chat_id"], raw["chatId"])
	if s.online == nil || !s.online(o.AccountID) || s.chatSend == nil {
		write(w, 200, response{Success: false, Message: "账号IM未连接，请先在在线聊天中启动账号"})
		return
	}
	if chatID == "" {
		// Platform order APIs do not return a chat ID. Recover it from a message
		// already seen for this buyer, or create a conversation before shipping.
		_ = s.db.QueryRowContext(r.Context(), `SELECT COALESCE(json_extract(data,'$.ChatID'),json_extract(data,'$.cid'),json_extract(data,'$.chat_id'),'') FROM event_logs WHERE owner_id=? AND account_id=? AND kind IN ('incoming_message','outgoing_message') AND (COALESCE(json_extract(data,'$.SenderID'),json_extract(data,'$.senderId'),json_extract(data,'$.toUserId'),'')=?) AND COALESCE(json_extract(data,'$.ChatID'),json_extract(data,'$.cid'),json_extract(data,'$.chat_id'),'')<>'' ORDER BY id DESC LIMIT 1`, o.OwnerID, o.AccountID, o.BuyerID).Scan(&chatID)
	}
	if chatID == "" && s.chatCreate != nil {
		created, err := s.chatCreate(r.Context(), o.AccountID, o.BuyerID, o.ItemID)
		if err != nil {
			write(w, 200, response{Success: false, Message: "创建买家会话失败: " + err.Error()})
			return
		}
		chatID = strings.TrimSpace(created)
	}
	if chatID == "" {
		write(w, 200, response{Success: false, Message: "订单没有会话ID，且无法创建买家会话"})
		return
	}
	if firstText(raw["chat_id"], raw["chatId"]) == "" {
		raw["chat_id"] = chatID
		encoded, _ := json.Marshal(raw)
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(encoded), o.ID)
	}
	po := pickupOrder{ID: o.ID, OwnerID: o.OwnerID, OrderNo: o.OrderNo, AccountID: o.AccountID, ItemID: o.ItemID, BuyerID: o.BuyerID, Status: o.Status, Data: o.Data}
	card, cardErr := s.pickupCardContent(r, po)
	if cardErr != "" {
		write(w, 200, response{Success: false, Message: cardErr})
		return
	}
	ship := s.shipPickupOrder(r, po, o.Account, raw)
	already := strings.Contains(ship.Error, "ORDER_ALREADY_DELIVERY") || strings.Contains(ship.Error, "已发货成功")
	if !ship.Success && !already {
		raw["delivery_fail_reason"] = firstNonemptyAPI(ship.Error, "发货失败")
		encoded, _ := json.Marshal(raw)
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(encoded), o.ID)
		write(w, 200, response{Success: false, Message: text(raw["delivery_fail_reason"])})
		return
	}
	if _, err := s.chatSend(r.Context(), o.AccountID, chatID, o.BuyerID, card.Content); err != nil {
		raw["delivery_fail_reason"] = err.Error()
		encoded, _ := json.Marshal(raw)
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(encoded), o.ID)
		write(w, 200, response{Success: false, Message: "发货消息发送失败: " + err.Error()})
		return
	}
	if card.Consumable {
		result, err := s.db.ExecContext(r.Context(), `UPDATE cards SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND data=?`, card.RemainingData, card.ID, card.OriginalData)
		var affected int64
		if result != nil {
			affected, _ = result.RowsAffected()
		}
		if err != nil || affected == 0 {
			write(w, 200, response{Success: false, Message: "卡密库存状态已变化，请人工核对本次发货"})
			return
		}
	}
	raw["delivery_content"], raw["delivery_fail_reason"] = card.Content, ""
	encoded, _ := json.Marshal(raw)
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET status='shipped',delivery_method='manual',data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(encoded), o.ID)
	_, _ = s.db.ExecContext(r.Context(), `UPDATE cards SET data=json_set(data,'$.delivery_count',COALESCE(json_extract(data,'$.delivery_count'),0)+1),updated_at=CURRENT_TIMESTAMP WHERE id=?`, card.ID)
	ok(w, "发货成功", map[string]any{"order_no": o.OrderNo, "content": card.Content, "already_delivered": already})
}
