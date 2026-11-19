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
			items, more, err := e.fetchSellerItemsPage(ctx, a, page)
			if err != nil {
				failures = append(failures, a.AccountID+": "+err.Error())
				break
			}
			for _, mapped := range items {
				b, _ := json.Marshal(mapped)
				_, _ = e.db.ExecContext(ctx, `INSERT INTO xy_catalog_items(owner_id,account_id,item_id,title,price,status,data) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,item_id) DO UPDATE SET title=excluded.title,price=excluded.price,status=excluded.status,data=json_patch(COALESCE(xy_catalog_items.data,'{}'),excluded.data),updated_at=CURRENT_TIMESTAMP`, a.OwnerID, a.AccountID, mapped["item_id"], mapped["title"], mapped["price"], mapped["status"], string(b))
			}
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

// fetchSellerItemsPage 抓取一页商品：优先走鱼小铺卖家接口；普通个人账号返回
// FAIL_BIZ_IDLE_USER_UNAUTHORIZED 时自动回落个人版接口 mtop.idle.web.xyh.item.list。
func (e *Engine) fetchSellerItemsPage(ctx context.Context, a syncAccount, page int) ([]map[string]any, bool, error) {
	res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.alibaba.idle.seller.pc.common.item.search", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"pageNo": page, "pageSize": xianyu.ItemPageSize, "bizType": "commonPro", "searchRequest": "{}", "itemStatus": "0"}, ExtraParams: map[string]string{"spm_cnt": "a21107h.42826273.0.0", "needLoginPC": "true", "accountSite": "xianyu"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		a.Cookie = res.Cookie
		_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
	}
	if !res.Success {
		// 普通个人账号（未开通鱼小铺）无权访问卖家专业版接口，回落个人版接口。
		if strings.Contains(res.Error, "FAIL_BIZ_IDLE_USER_UNAUTHORIZED") {
			return e.fetchPersonalItemsPage(ctx, a, page)
		}
		return nil, false, fmt.Errorf("获取商品失败: %s", res.Error)
	}
	biz := syncNested(res.Response, "data", "data")
	raw, _ := biz["itemSearchResponseList"].([]any)
	out := []map[string]any{}
	for _, rawItem := range raw {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(item["itemId"]))
		if id == "" {
			continue
		}
		out = append(out, map[string]any{"item_id": id, "id": id, "title": fmt.Sprint(item["title"]), "price": fmt.Sprint(item["reservePrice"]), "status": fmt.Sprint(item["itemStatus"]), "source": "seller", "image_url": fmt.Sprint(item["itemImageUrl"]), "quantity": item["quantity"], "idle_item_sku_list": item["idleItemSkuList"]})
	}
	more, _ := biz["hasNextPage"].(bool)
	return out, more, nil
}

// fetchPersonalItemsPage 抓取个人版账号一页在售商品（mtop.idle.web.xyh.item.list），
// 请求结构与字段映射参照原版 Python 项目的 ItemInfoManager.get_item_list_info。
func (e *Engine) fetchPersonalItemsPage(ctx context.Context, a syncAccount, page int) ([]map[string]any, bool, error) {
	res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.idle.web.xyh.item.list", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/", Data: xianyu.PersonalItemListData(page, xianyu.AccountUserID(a.Cookie, a.AccountID)), ExtraParams: xianyu.PersonalItemListParams()})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		a.Cookie = res.Cookie
		_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
	}
	if !res.Success {
		// 后续页出现"每页条数/最大可查看页数超限"= 该账号已到接口可查看页数上限，
		// 按没有更多数据处理；首页出现则说明分页参数不合规，直接给出可读原因。
		if xianyu.PersonalItemPageLimitError(res.Error) {
			if page > 1 {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("获取商品失败(个人版): 分页参数超出接口限制（每页最多 %d 条、可查看页数有上限）: %s", xianyu.ItemPageSize, res.Error)
		}
		return nil, false, fmt.Errorf("获取商品失败(个人版): %s", res.Error)
	}
	data := syncNested(res.Response, "data")
	cards, _ := data["cardList"].([]any)
	out := []map[string]any{}
	for _, rawCard := range cards {
		card, okc := rawCard.(map[string]any)
		if !okc {
			continue
		}
		cardData, okd := card["cardData"].(map[string]any)
		if !okd {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(cardData["id"]))
		if id == "" {
			continue
		}
		price := ""
		if priceInfo, okp := cardData["priceInfo"].(map[string]any); okp {
			price = fmt.Sprint(priceInfo["price"])
		}
		status := ""
		if itemStatus, oks := cardData["itemStatus"]; oks {
			status = fmt.Sprint(itemStatus)
		}
		imageURL := ""
		if picInfo, oki := cardData["picInfo"].(map[string]any); oki {
			imageURL = fmt.Sprint(picInfo["url"])
		}
		out = append(out, map[string]any{"item_id": id, "id": id, "title": fmt.Sprint(cardData["title"]), "price": price, "status": status, "source": "personal", "image_url": imageURL, "quantity": cardData["quantity"]})
	}
	more := xianyu.PageHasMore(len(cards))
	return out, more, nil
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
