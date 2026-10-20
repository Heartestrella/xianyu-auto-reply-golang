package xianyu

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type OrderSyncAccount struct {
	OwnerID                  int64
	AccountID, Cookie, Proxy string
}

// SyncSoldOrder commits the list entry before fetching details. A failed detail
// request leaves the base order available for a later pending-order retry.
func SyncSoldOrder(ctx context.Context, db *sql.DB, call func(context.Context, MTOPOptions) MTOPResult, a *OrderSyncAccount, item map[string]any, queryCode string) (bool, error) {
	common := orderMap(item, "commonData")
	buyer := orderMap(item, "buyerInfoVO")
	price := orderMap(item, "priceVO")
	no := stringSetting(common, "orderId")
	if no == "" {
		return false, errors.New("订单号为空")
	}
	var owner int64
	var account, old string
	err := db.QueryRowContext(ctx, `SELECT owner_id,COALESCE(account_id,''),data FROM xy_orders WHERE order_no=?`, no).Scan(&owner, &account, &old)
	newOrder := err == sql.ErrNoRows
	if err != nil && !newOrder {
		return false, err
	}
	if !newOrder && (owner != a.OwnerID || account != a.AccountID) {
		return false, errors.New("订单不属于当前账号")
	}
	status := map[string]string{"待付款": "pending_payment", "待发货": "pending_ship", "已发货": "shipped", "交易成功": "completed", "交易关闭": "cancelled", "退款中": "refunding", "退款成功": "refunded", "已退款": "refunded", "退款关闭": "cancelled"}[stringSetting(common, "orderStatus")]
	if status == "" {
		status = "unknown"
	}
	if boolSetting(common, "inRefund") {
		status = "refunding"
	}
	quantity, _ := strconv.Atoi(stringSetting(price, "buyNum"))
	if quantity < 1 {
		quantity = 1
	}
	bargain := false
	if buttons, ok := orderMap(item, "rightVO")["btnList"].([]any); ok {
		for _, v := range buttons {
			if b, ok := v.(map[string]any); ok && stringSetting(b, "tradeAction") == "SKIP_PIN" {
				bargain = true
			}
		}
	}
	payload := map[string]any{"order_id": no, "order_no": no, "cookie_id": a.AccountID, "item_id": stringSetting(common, "itemId"), "buyer_id": stringSetting(buyer, "buyerId"), "buyer_fish_nick": stringSetting(buyer, "userNick"), "receiver_name": stringSetting(buyer, "name"), "receiver_phone": stringSetting(buyer, "phone"), "receiver_address": stringSetting(buyer, "address"), "amount": stringSetting(price, "totalPrice"), "quantity": quantity, "status": status, "is_bargain": bargain, "is_rated": stringSetting(common, "sellerRateStatus") == "4"}
	if placed, ok := common["createTime"]; ok && placed != nil {
		payload["placed_at"] = placed
	}
	encoded, _ := json.Marshal(payload)
	result, err := db.ExecContext(ctx, `INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES(?,?,?,?,?,?,?)
	 ON CONFLICT(order_no) DO UPDATE SET item_id=excluded.item_id,buyer_id=excluded.buyer_id,status=excluded.status,data=json_patch(COALESCE(xy_orders.data,'{}'),excluded.data),updated_at=CURRENT_TIMESTAMP
	 WHERE xy_orders.owner_id=excluded.owner_id AND xy_orders.account_id=excluded.account_id`, a.OwnerID, a.AccountID, no, payload["item_id"], payload["buyer_id"], status, string(encoded))
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return false, errors.New("订单归属已变化")
	}
	previous := map[string]any{}
	_ = json.Unmarshal([]byte(old), &previous)
	missingSpec := stringSetting(previous, "spec_name") == "" || stringSetting(previous, "spec_value") == ""
	knownSingle := boolSetting(previous, "order_detail_fetched") && stringSetting(previous, "spec_name") == "" && stringSetting(previous, "spec_value") == ""
	if !newOrder && !(queryCode != "ALL" && status == "pending_ship" && missingSpec && !knownSingle) {
		return false, nil
	}
	if call == nil {
		call = MTOPCall
	}
	res := call(ctx, MTOPOptions{API: "mtop.idle.web.trade.order.detail", Version: "1.0", Cookie: a.Cookie, Proxy: a.Proxy, Data: map[string]any{"tid": no}, ExtraParams: map[string]string{"spm_cnt": "a21ybx.order-detail.0.0"}})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		_, err = db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND cookie=?`, res.Cookie, a.OwnerID, a.AccountID, a.Cookie)
		if err != nil {
			return newOrder, err
		}
		if err = db.QueryRowContext(ctx, `SELECT cookie FROM xy_accounts WHERE owner_id=? AND account_id=?`, a.OwnerID, a.AccountID).Scan(&a.Cookie); err != nil {
			return newOrder, err
		}
	}
	patch, detailErr := parseOrderSpec(res.Response, stringSetting(common, "itemId"), stringSetting(buyer, "buyerId"))
	if !res.Success {
		detailErr = errors.New(firstNonempty(res.Error, "订单详情获取失败"))
	}
	if detailErr != nil {
		logData, _ := json.Marshal(map[string]any{"order_no": no, "error": detailErr.Error()})
		_, err = db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,account_id,kind,level,message,data) VALUES(?,?,'order_detail_sync','ERROR','订单规格补全失败，待发货同步将重试',?)`, a.OwnerID, a.AccountID, string(logData))
		return newOrder, err
	}
	encoded, _ = json.Marshal(patch)
	_, err = db.ExecContext(ctx, `UPDATE xy_orders SET data=json_patch(COALESCE(data,'{}'),?),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND order_no=?`, string(encoded), a.OwnerID, a.AccountID, no)
	return newOrder, err
}

func orderMap(m map[string]any, key string) map[string]any { v, _ := m[key].(map[string]any); return v }

func parseOrderSpec(response map[string]any, itemID, buyerID string) (map[string]any, error) {
	data := orderMap(response, "data")
	if id := stringSetting(data, "itemId"); id != "" && itemID != "" && id != itemID {
		return nil, errors.New("订单详情商品不匹配")
	}
	if id := stringSetting(data, "peerUserId"); id != "" && buyerID != "" && id != buyerID {
		return nil, errors.New("订单详情买家不匹配")
	}
	components, _ := data["components"].([]any)
	for _, entry := range components {
		component, ok := entry.(map[string]any)
		if !ok || stringSetting(component, "render") != "orderInfoVO" {
			continue
		}
		info := orderMap(component, "data")
		item := orderMap(info, "itemInfo")
		if item == nil {
			continue
		}
		if id := stringSetting(item, "itemId"); id != "" && itemID != "" && id != itemID {
			return nil, errors.New("订单详情商品不匹配")
		}
		if id := stringSetting(info, "buyerUserId", "buyerId"); id != "" && buyerID != "" && id != buyerID {
			return nil, errors.New("订单详情买家不匹配")
		}
		sku := stringSetting(item, "skuInfo")
		if sku == "" {
			return map[string]any{"order_detail_fetched": true}, nil
		}
		name, value, ok := strings.Cut(strings.ReplaceAll(sku, "：", ":"), ":")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("订单规格格式无法解析")
		}
		return map[string]any{"spec_name": strings.TrimSpace(name), "spec_value": strings.TrimSpace(value), "order_detail_fetched": true}, nil
	}
	return nil, errors.New("订单详情缺少商品信息")
}
