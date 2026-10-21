package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type syncAccount struct {
	RedFlowerAfterShipment                  bool
	ID, OwnerID                             int64
	AccountID, Cookie, ProxyType, ProxyHost string
	ProxyPort                               sql.NullInt64
	ProxyUser, ProxyPass                    sql.NullString
}

func (e *Engine) syncAccounts(ctx context.Context) ([]syncAccount, error) {
	rows, err := e.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE status='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []syncAccount{}
	for rows.Next() {
		var a syncAccount
		if rows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass) == nil {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}
func syncProxy(a syncAccount) string {
	if proxy.Enabled() {
		return proxy.URL()
	}
	if a.ProxyType == "" || a.ProxyType == "none" || !a.ProxyPort.Valid {
		return ""
	}
	auth := ""
	if a.ProxyUser.Valid && a.ProxyUser.String != "" {
		auth = a.ProxyUser.String + ":" + a.ProxyPass.String + "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", a.ProxyType, auth, a.ProxyHost, a.ProxyPort.Int64)
}
func syncNested(root map[string]any, keys ...string) map[string]any {
	cur := root
	for _, key := range keys {
		next, ok := cur[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		cur = next
	}
	return cur
}

func (e *Engine) fetchItems(ctx context.Context, _ map[string]any) error {
	accounts, err := e.syncAccounts(ctx)
	if err != nil {
		return err
	}
	failures := []string{}
	for _, a := range accounts {
		for page := 1; page <= 200; page++ {
			res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.alibaba.idle.seller.pc.common.item.search", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"pageNo": page, "pageSize": 30, "bizType": "commonPro", "searchRequest": "{}", "itemStatus": "0"}, ExtraParams: map[string]string{"spm_cnt": "a21107h.42826273.0.0", "needLoginPC": "true", "accountSite": "xianyu"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
			if res.Cookie != "" && res.Cookie != a.Cookie {
				a.Cookie = res.Cookie
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
			}
			if !res.Success {
				failures = append(failures, a.AccountID+": "+res.Error)
				break
			}
			biz := syncNested(res.Response, "data", "data")
			items, _ := biz["itemSearchResponseList"].([]any)
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				id := strings.TrimSpace(fmt.Sprint(item["itemId"]))
				if id == "" {
					continue
				}
				mapped := map[string]any{"item_id": id, "id": id, "title": fmt.Sprint(item["title"]), "price": fmt.Sprint(item["reservePrice"]), "status": fmt.Sprint(item["itemStatus"]), "source": "seller", "image_url": fmt.Sprint(item["itemImageUrl"]), "quantity": item["quantity"], "idle_item_sku_list": item["idleItemSkuList"]}
				b, _ := json.Marshal(mapped)
				_, _ = e.db.ExecContext(ctx, `INSERT INTO xy_catalog_items(owner_id,account_id,item_id,title,price,status,data) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,item_id) DO UPDATE SET title=excluded.title,price=excluded.price,status=excluded.status,data=json_patch(COALESCE(xy_catalog_items.data,'{}'),excluded.data),updated_at=CURRENT_TIMESTAMP`, a.OwnerID, a.AccountID, id, mapped["title"], mapped["price"], mapped["status"], string(b))
			}
			more, _ := biz["hasNextPage"].(bool)
			if !more {
				break
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("商品同步部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (e *Engine) fetchOrders(ctx context.Context, _ map[string]any) error {
	return e.fetchOrdersByCode(ctx, xianyu.SoldOrderQueryAll)
}
func (e *Engine) fetchPendingOrders(ctx context.Context, _ map[string]any) error {
	return e.fetchOrdersByCode(ctx, xianyu.SoldOrderQueryPending)
}
func (e *Engine) fetchRefundOrders(ctx context.Context, _ map[string]any) error {
	return e.fetchOrdersByCode(ctx, "REFUNDING")
}
func (e *Engine) fetchOrdersByCode(ctx context.Context, queryCode string) error {
	accounts, err := e.syncAccounts(ctx)
	if err != nil {
		return err
	}
	failures := []string{}
	for _, a := range accounts {
		for page := 1; page <= 200; page++ {
			res := e.callMTOP(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idle.trade.merchant.sold.get", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/", Data: map[string]any{"pageNumber": page, "rowsPerPage": 30, "orderIds": "", "queryCode": queryCode, "orderSearchParam": "{}"}, ExtraParams: map[string]string{"valueType": "string", "spm_cnt": "a21107h.42826273.0.0"}})
			if res.Cookie != "" && res.Cookie != a.Cookie {
				a.Cookie = res.Cookie
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
			}
			if !res.Success {
				failures = append(failures, a.AccountID+": "+res.Error)
				break
			}
			module := syncNested(res.Response, "data", "module")
			items, _ := module["items"].([]any)
			for _, raw := range items {
				if item, ok := raw.(map[string]any); ok {
					account := xianyu.OrderSyncAccount{OwnerID: a.OwnerID, AccountID: a.AccountID, Cookie: a.Cookie, Proxy: syncProxy(a)}
					_, syncErr := xianyu.SyncSoldOrder(ctx, e.db, e.callMTOP, &account, item, queryCode)
					a.Cookie = account.Cookie
					if syncErr != nil {
						failures = append(failures, a.AccountID+": "+syncErr.Error())
					}
				}
			}
			if fmt.Sprint(module["nextPage"]) != "true" {
				break
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("订单同步部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}
