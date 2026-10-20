package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type pickupOrder struct {
	ID, OwnerID                                       int64
	OrderNo, AccountID, ItemID, BuyerID, Status, Data string
}
type pickupCard struct {
	ID                                   int64
	Content, OriginalData, RemainingData string
	Consumable                           bool
}

func (s *Server) loadPickupOrder(r *http.Request, orderNo, orderID string) (pickupOrder, string) {
	orderNo = strings.TrimSpace(orderNo)
	id, e := strconv.ParseInt(strings.TrimSpace(orderID), 10, 64)
	if e != nil || id <= 0 || orderNo == "" {
		return pickupOrder{}, "链接无效，缺少订单参数"
	}
	var o pickupOrder
	e = s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,order_no,COALESCE(account_id,''),COALESCE(item_id,''),COALESCE(buyer_id,''),COALESCE(status,''),data FROM xy_orders WHERE id=?`, id).Scan(&o.ID, &o.OwnerID, &o.OrderNo, &o.AccountID, &o.ItemID, &o.BuyerID, &o.Status, &o.Data)
	if e == sql.ErrNoRows {
		return o, "订单不存在"
	}
	if e != nil {
		return o, "系统繁忙，请稍后再试"
	}
	if strings.TrimSpace(o.OrderNo) != orderNo {
		return o, "订单号与订单id不匹配"
	}
	return o, ""
}

func pickupData(o pickupOrder) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(o.Data), &m)
	agreed := truthy(m["agree_deliver_agreed"])
	title := firstText(m["item_title"], m["title"])
	content := any(nil)
	if agreed {
		content = m["delivery_content"]
	}
	return map[string]any{"order_no": o.OrderNo, "amount": m["amount"], "quantity": m["quantity"], "spec_name": m["spec_name"], "spec_value": m["spec_value"], "item_id": nilIfEmpty(o.ItemID), "item_title": nilIfEmpty(title), "item_url": map[bool]any{true: "https://www.goofish.com/item?id=" + o.ItemID, false: nil}[o.ItemID != ""], "already_agreed": agreed, "content": content}
}

func (s *Server) agreePickupOrder(w http.ResponseWriter, r *http.Request) {
	o, msg := s.loadPickupOrder(r, r.URL.Query().Get("orderNo"), r.URL.Query().Get("orderId"))
	if msg != "" {
		write(w, 200, response{Success: false, Message: msg})
		return
	}
	data := pickupData(o)
	if data["item_title"] == nil && o.ItemID != "" {
		var title string
		_ = s.db.QueryRowContext(r.Context(), `SELECT COALESCE(title,'') FROM xy_catalog_items WHERE owner_id=? AND item_id=? LIMIT 1`, o.OwnerID, o.ItemID).Scan(&title)
		data["item_title"] = nilIfEmpty(title)
	}
	ok(w, "查询成功", data)
}

func (s *Server) agreePickupAgree(w http.ResponseWriter, r *http.Request) {
	s.agreePickupMu.Lock()
	defer s.agreePickupMu.Unlock()
	var p struct {
		OrderNo string `json:"order_no"`
		OrderID string `json:"order_id"`
	}
	if decode(r, &p) != nil {
		write(w, 200, response{Success: false, Message: "链接无效，缺少订单参数"})
		return
	}
	o, msg := s.loadPickupOrder(r, p.OrderNo, p.OrderID)
	if msg != "" {
		write(w, 200, response{Success: false, Message: msg})
		return
	}
	raw := map[string]any{}
	_ = json.Unmarshal([]byte(o.Data), &raw)
	if truthy(raw["agree_deliver_agreed"]) && strings.TrimSpace(text(raw["delivery_content"])) != "" {
		ok(w, "您已同意发货，以下为发货内容", map[string]any{"order_no": o.OrderNo, "content": raw["delivery_content"], "already_agreed": true})
		return
	}
	if !oneOf(strings.ToLower(o.Status), "pending_ship", "pending", "paid", "待发货") {
		write(w, 200, response{Success: false, Message: "当前订单状态不可发货"})
		return
	}
	if o.AccountID == "" || o.ItemID == "" || o.BuyerID == "" {
		write(w, 200, response{Success: false, Message: "订单缺少账号、商品或买家信息"})
		return
	}
	account, found := s.pickupAccount(r, o)
	if !found {
		write(w, 200, response{Success: false, Message: "账号不存在或Cookie为空"})
		return
	}
	card, errMsg := s.pickupCardContent(r, o)
	if errMsg != "" {
		write(w, 200, response{Success: false, Message: errMsg})
		return
	}
	chatID := firstText(raw["chat_id"], raw["chatId"])
	if chatID == "" || s.chatSend == nil {
		write(w, 200, response{Success: false, Message: "账号未连接或订单缺少会话信息，请联系卖家"})
		return
	}
	res := s.shipPickupOrder(r, o, account, raw)
	if !res.Success && !strings.Contains(res.Error, "ORDER_ALREADY_DELIVERY") && !strings.Contains(res.Error, "已发货成功") {
		write(w, 200, response{Success: false, Message: firstNonemptyAPI(res.Error, "发货失败，请稍后重试或联系卖家")})
		return
	}
	if _, err := s.chatSend(r.Context(), o.AccountID, chatID, o.BuyerID, card.Content); err != nil {
		write(w, 200, response{Success: false, Message: "卡券发送失败: " + err.Error()})
		return
	}
	if card.Consumable {
		res, e := s.db.ExecContext(r.Context(), `UPDATE cards SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND data=?`, card.RemainingData, card.ID, card.OriginalData)
		n := int64(0)
		if res != nil {
			n, _ = res.RowsAffected()
		}
		if e != nil || n == 0 {
			write(w, 200, response{Success: false, Message: "卡密库存状态已变化，请联系卖家核对"})
			return
		}
	}
	raw["agree_deliver_agreed"] = true
	raw["delivery_content"] = card.Content
	encoded, _ := json.Marshal(raw)
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET status='shipped',delivery_method='manual',data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(encoded), o.ID)
	_, _ = s.db.ExecContext(r.Context(), `UPDATE cards SET data=json_set(data,'$.delivery_count',COALESCE(json_extract(data,'$.delivery_count'),0)+1),updated_at=CURRENT_TIMESTAMP WHERE id=?`, card.ID)
	ok(w, "发货成功", map[string]any{"order_no": o.OrderNo, "content": card.Content, "already_agreed": true})
}

func (s *Server) pickupAccount(r *http.Request, o pickupOrder) (platformAccount, bool) {
	var a platformAccount
	e := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, o.OwnerID, o.AccountID).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
	return a, e == nil && strings.TrimSpace(a.Cookie) != ""
}
func (s *Server) pickupCardContent(r *http.Request, o pickupOrder) (pickupCard, string) {
	var card pickupCard
	id, typ, data, e := xianyu.SelectDeliveryCard(r.Context(), s.db, o.OwnerID, o.AccountID, o.ItemID, o.Data)
	if e != nil {
		return card, e.Error()
	}
	card.ID = id
	m := map[string]any{}
	_ = json.Unmarshal([]byte(data), &m)
	switch typ {
	case "text":
		card.Content = strings.TrimSpace(firstText(m["text_content"], m["content"]))
		if card.Content == "" {
			return card, "文本卡券内容为空"
		}
		return card, ""
	case "data":
		lines := nonemptyLines(firstText(m["data_content"], m["content"]))
		if len(lines) == 0 {
			return card, "卡密库存不足"
		}
		card.Content = lines[0]
		card.Consumable = true
		card.OriginalData = data
		m["data_content"] = strings.Join(lines[1:], "\n")
		next, _ := json.Marshal(m)
		card.RemainingData = string(next)
		return card, ""
	default:
		return card, "同意后发货当前仅支持文本或卡密卡券"
	}
}
func (s *Server) shipPickupOrder(r *http.Request, o pickupOrder, a platformAccount, raw map[string]any) xianyu.MTOPResult {
	api := "mtop.taobao.idle.logistic.consign.dummy"
	payload := map[string]any{"orderId": o.OrderNo, "tradeText": "", "picList": []any{}, "newUnconsign": true}
	if truthy(raw["is_bargain"]) {
		api = "mtop.idle.groupon.activity.seller.freeshipping"
		payload = map[string]any{"bizOrderId": o.OrderNo, "itemId": json.Number(o.ItemID), "buyerId": json.Number(o.BuyerID)}
	}
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: api, Version: "1.0", Cookie: a.Cookie, Proxy: accountProxy(a), Data: payload})
	s.persistOrderActionCookie(r, a, res.Cookie)
	return res
}
func nonemptyLines(v string) []string {
	out := []string{}
	for _, line := range strings.Split(v, "\n") {
		if x := strings.TrimSpace(line); x != "" {
			out = append(out, x)
		}
	}
	return out
}
