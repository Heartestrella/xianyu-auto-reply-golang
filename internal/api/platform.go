package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type platformAccount struct {
	ID, OwnerID          int64
	AccountID, Cookie    string
	ProxyType, ProxyHost string
	ProxyPort            sql.NullInt64
	ProxyUser, ProxyPass sql.NullString
}

func (s *Server) loadPlatformAccounts(r *http.Request, id string) ([]platformAccount, error) {
	u := current(r)
	q := `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE status='active'`
	args := []any{}
	if u.Role != "ADMIN" {
		q += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if id != "" {
		q += ` AND account_id=?`
		args = append(args, id)
	}
	rows, e := s.db.QueryContext(r.Context(), q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []platformAccount{}
	for rows.Next() {
		var a platformAccount
		if e = rows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass); e == nil {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}
func accountProxy(a platformAccount) string {
	if a.ProxyType == "" || a.ProxyType == "none" || !a.ProxyPort.Valid {
		return ""
	}
	auth := ""
	if a.ProxyUser.Valid && a.ProxyUser.String != "" {
		auth = a.ProxyUser.String + ":" + a.ProxyPass.String + "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", a.ProxyType, auth, a.ProxyHost, a.ProxyPort.Int64)
}
func (s *Server) fetchItemsPage(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CookieID string `json:"cookie_id"`
		Page     int    `json:"page"`
	}
	if decode(r, &p) != nil || p.CookieID == "" {
		fail(w, 400, "cookie_id不能为空")
		return
	}
	if p.Page < 1 {
		p.Page = 1
	}
	accounts, e := s.loadPlatformAccounts(r, p.CookieID)
	if e != nil || len(accounts) == 0 {
		fail(w, 404, "账号不存在或未启用")
		return
	}
	items, more, e := s.fetchSellerItems(r, accounts[0], p.Page)
	if e != nil {
		write(w, 200, response{false, e.Error(), nil})
		return
	}
	ok(w, "商品同步成功", map[string]any{"items": items, "current_count": len(items), "has_more": more, "page": p.Page})
}
func (s *Server) fetchItemsAll(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CookieID string `json:"cookie_id"`
	}
	_ = decode(r, &p)
	accounts, e := s.loadPlatformAccounts(r, p.CookieID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	total, saved, success := 0, 0, 0
	failed := []string{}
	for _, a := range accounts {
		accountOK := true
		for page := 1; page <= 200; page++ {
			items, more, e := s.fetchSellerItems(r, a, page)
			if e != nil {
				failed = append(failed, a.AccountID+": "+e.Error())
				accountOK = false
				break
			}
			total += len(items)
			saved += len(items)
			if !more {
				break
			}
		}
		if accountOK {
			success++
		}
	}
	write(w, 200, map[string]any{"success": len(failed) == 0, "message": "商品同步完成", "total_count": total, "saved_count": saved, "account_count": len(accounts), "success_account_count": success, "failed_account_count": len(failed), "failed_accounts": failed})
}
func (s *Server) fetchSellerItems(r *http.Request, a platformAccount, page int) ([]map[string]any, bool, error) {
	result := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.alibaba.idle.seller.pc.common.item.search", Version: "1.0", Cookie: a.Cookie, Proxy: accountProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"pageNo": page, "pageSize": 30, "bizType": "commonPro", "searchRequest": "{}", "itemStatus": "0"}, ExtraParams: map[string]string{"spm_cnt": "a21107h.42826273.0.0", "needLoginPC": "true", "accountSite": "xianyu"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	if result.Cookie != a.Cookie && result.Cookie != "" {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP WHERE id=?`, result.Cookie, a.ID)
	}
	if !result.Success {
		return nil, false, fmt.Errorf("获取商品失败: %s", result.Error)
	}
	biz := nestedMap(result.Response, "data", "data")
	raw, _ := biz["itemSearchResponseList"].([]any)
	out := []map[string]any{}
	for _, v := range raw {
		m, okv := v.(map[string]any)
		if !okv {
			continue
		}
		id := fmt.Sprint(m["itemId"])
		if id == "" {
			continue
		}
		mapped := map[string]any{"item_id": id, "id": id, "title": fmt.Sprint(m["title"]), "price": fmt.Sprint(m["reservePrice"]), "status": fmt.Sprint(m["itemStatus"]), "source": "seller", "image_url": fmt.Sprint(m["itemImageUrl"]), "quantity": m["quantity"], "idle_item_sku_list": m["idleItemSkuList"]}
		b, _ := json.Marshal(mapped)
		_, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_catalog_items(owner_id,account_id,item_id,title,price,status,data) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,item_id) DO UPDATE SET title=excluded.title,price=excluded.price,status=excluded.status,data=json_patch(COALESCE(xy_catalog_items.data,'{}'),excluded.data),updated_at=CURRENT_TIMESTAMP`, a.OwnerID, a.AccountID, id, mapped["title"], mapped["price"], mapped["status"], string(b))
		if e == nil {
			out = append(out, mapped)
		}
	}
	more, _ := biz["hasNextPage"].(bool)
	return out, more, nil
}
func nestedMap(root map[string]any, keys ...string) map[string]any {
	cur := root
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		cur = next
	}
	return cur
}

func (s *Server) fetchOrders(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CookieID string `json:"cookie_id"`
	}
	_ = decode(r, &p)
	accounts, e := s.loadPlatformAccounts(r, p.CookieID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	fetched, inserted, updated, failed := 0, 0, 0, 0
	errs := []string{}
	for _, a := range accounts {
		for page := 1; page <= 200; page++ {
			data := map[string]any{"pageNumber": page, "rowsPerPage": 30, "orderIds": "", "queryCode": "ALL", "orderSearchParam": "{}"}
			res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idle.trade.merchant.sold.get", Version: "1.0", Cookie: a.Cookie, Proxy: accountProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/", Data: data, ExtraParams: map[string]string{"valueType": "string", "spm_cnt": "a21107h.42826273.0.0"}})
			if !res.Success {
				failed++
				errs = append(errs, a.AccountID+": "+res.Error)
				break
			}
			if res.Cookie != a.Cookie && res.Cookie != "" {
				a.Cookie = res.Cookie
				_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
			}
			module := nestedMap(res.Response, "data", "module")
			items, _ := module["items"].([]any)
			for _, raw := range items {
				m, okv := raw.(map[string]any)
				if !okv {
					continue
				}
				isNew, e := s.upsertSoldOrder(r, a, m)
				if e != nil {
					failed++
					continue
				}
				fetched++
				if isNew {
					inserted++
				} else {
					updated++
				}
			}
			if fmt.Sprint(module["nextPage"]) != "true" {
				break
			}
		}
	}
	ok(w, "同步完成", map[string]any{"total_fetched": fetched, "new_inserted": inserted, "updated": updated, "failed": failed, "accounts_processed": len(accounts), "errors": errs})
}
func (s *Server) upsertSoldOrder(r *http.Request, a platformAccount, m map[string]any) (bool, error) {
	common, _ := m["commonData"].(map[string]any)
	buyer, _ := m["buyerInfoVO"].(map[string]any)
	price, _ := m["priceVO"].(map[string]any)
	right, _ := m["rightVO"].(map[string]any)
	no := fmt.Sprint(common["orderId"])
	if no == "" {
		return false, fmt.Errorf("订单号为空")
	}
	statusMap := map[string]string{"待付款": "pending_payment", "待发货": "pending_ship", "已发货": "shipped", "交易成功": "completed", "交易关闭": "cancelled", "退款中": "refunding", "退款成功": "refunded", "已退款": "refunded", "退款关闭": "cancelled"}
	status := statusMap[fmt.Sprint(common["orderStatus"])]
	if status == "" {
		status = "unknown"
	}
	if fmt.Sprint(common["inRefund"]) == "true" {
		status = "refunding"
	}
	bargain := false
	if btns, ok := right["btnList"].([]any); ok {
		for _, v := range btns {
			if x, ok := v.(map[string]any); ok && fmt.Sprint(x["tradeAction"]) == "SKIP_PIN" {
				bargain = true
			}
		}
	}
	quantity, _ := strconv.Atoi(fmt.Sprint(price["buyNum"]))
	if quantity < 1 {
		quantity = 1
	}
	payload := map[string]any{"order_id": no, "order_no": no, "cookie_id": a.AccountID, "item_id": fmt.Sprint(common["itemId"]), "buyer_id": fmt.Sprint(buyer["buyerId"]), "buyer_fish_nick": fmt.Sprint(buyer["userNick"]), "receiver_name": fmt.Sprint(buyer["name"]), "receiver_phone": fmt.Sprint(buyer["phone"]), "receiver_address": fmt.Sprint(buyer["address"]), "amount": fmt.Sprint(price["totalPrice"]), "quantity": quantity, "status": status, "is_bargain": bargain, "is_rated": fmt.Sprint(common["sellerRateStatus"]) == "4", "placed_at": common["createTime"]}
	b, _ := json.Marshal(payload)
	var exists int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_orders WHERE order_no=?`, no).Scan(&exists)
	_, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES(?,?,?,?,?,?,?) ON CONFLICT(order_no) DO UPDATE SET item_id=excluded.item_id,buyer_id=excluded.buyer_id,status=excluded.status,data=json_patch(COALESCE(xy_orders.data,'{}'),excluded.data),updated_at=CURRENT_TIMESTAMP`, a.OwnerID, a.AccountID, no, payload["item_id"], payload["buyer_id"], status, string(b))
	return exists == 0, e
}

var _ = strings.TrimSpace
