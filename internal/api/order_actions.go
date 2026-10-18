package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type actionableOrder struct {
	ID, OwnerID                                       int64
	AccountID, OrderNo, ItemID, BuyerID, Status, Data string
	Account                                           platformAccount
}

func (s *Server) loadActionableOrder(r *http.Request, orderNo string) (actionableOrder, string) {
	u := current(r)
	var o actionableOrder
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,COALESCE(account_id,''),order_no,COALESCE(item_id,''),COALESCE(buyer_id,''),COALESCE(status,''),data FROM xy_orders WHERE order_no=?`, orderNo).Scan(&o.ID, &o.OwnerID, &o.AccountID, &o.OrderNo, &o.ItemID, &o.BuyerID, &o.Status, &o.Data)
	if err != nil {
		return o, "订单不存在"
	}
	if !strings.EqualFold(u.Role, "ADMIN") && o.OwnerID != u.ID {
		return o, "无权操作此订单"
	}
	if o.AccountID == "" {
		return o, "订单缺少账号信息"
	}
	err = s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, o.OwnerID, o.AccountID).Scan(&o.Account.ID, &o.Account.OwnerID, &o.Account.AccountID, &o.Account.Cookie, &o.Account.ProxyType, &o.Account.ProxyHost, &o.Account.ProxyPort, &o.Account.ProxyUser, &o.Account.ProxyPass)
	if err != nil {
		return o, "账号不存在"
	}
	if strings.TrimSpace(o.Account.Cookie) == "" {
		return o, "账号Cookie为空，请重新登录账号"
	}
	return o, ""
}

func (s *Server) orderNoLogisticsDelivery(w http.ResponseWriter, r *http.Request) {
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
	if !oneOf(strings.ToLower(o.Status), "pending_ship", "pending", "paid", "待发货") {
		write(w, 200, response{Success: false, Message: "只有待发货订单可以发货"})
		return
	}
	if o.ItemID == "" || o.BuyerID == "" {
		write(w, 200, response{Success: false, Message: "订单缺少账号、商品或买家信息"})
		return
	}
	bargain := false
	var raw map[string]any
	if json.Unmarshal([]byte(o.Data), &raw) == nil {
		bargain = truthy(raw["is_bargain"])
	}
	api := "mtop.taobao.idle.logistic.consign.dummy"
	payload := map[string]any{"orderId": o.OrderNo, "tradeText": "", "picList": []any{}, "newUnconsign": true}
	if bargain {
		api = "mtop.idle.groupon.activity.seller.freeshipping"
		payload = map[string]any{"bizOrderId": o.OrderNo, "itemId": json.Number(o.ItemID), "buyerId": json.Number(o.BuyerID)}
	}
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: api, Version: "1.0", Cookie: o.Account.Cookie, Proxy: accountProxy(o.Account), Data: payload})
	s.persistOrderActionCookie(r, o.Account, res.Cookie)
	already := strings.Contains(res.Error, "ORDER_ALREADY_DELIVERY") || strings.Contains(res.Error, "已发货成功")
	if !res.Success && !already {
		write(w, 200, response{Success: false, Message: firstNonemptyAPI(res.Error, "无物流发货失败")})
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET status='shipped',delivery_method='manual',updated_at=CURRENT_TIMESTAMP WHERE id=?`, o.ID)
	ok(w, "无物流发货成功", map[string]any{"order_no": o.OrderNo, "already_delivered": already})
}

func (s *Server) orderCancel(w http.ResponseWriter, r *http.Request) {
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
	if !oneOf(strings.ToLower(o.Status), "pending_payment", "pending_ship", "pending", "paid", "待付款", "待发货") {
		write(w, 200, response{Success: false, Message: "当前订单状态不可取消"})
		return
	}
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idle.trade.merchant.close.by.seller", Version: "2.0", Cookie: o.Account.Cookie, Proxy: accountProxy(o.Account), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"tid": o.OrderNo, "bizOrderId": o.OrderNo, "closeReason": "其他原因"}, ExtraParams: map[string]string{"spm_cnt": "a21107h.44911108.0.0", "spm_pre": "a21107h.44911108.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	s.persistOrderActionCookie(r, o.Account, res.Cookie)
	if !res.Success {
		write(w, 200, response{Success: false, Message: firstNonemptyAPI(res.Error, "取消订单失败")})
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET status='cancelled',updated_at=CURRENT_TIMESTAMP WHERE id=?`, o.ID)
	ok(w, "订单已取消", map[string]any{"order_no": o.OrderNo})
}

func (s *Server) persistOrderActionCookie(r *http.Request, a platformAccount, cookie string) {
	if cookie != "" && cookie != a.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cookie, a.ID)
	}
}
func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == strings.ToLower(x) {
			return true
		}
	}
	return false
}
