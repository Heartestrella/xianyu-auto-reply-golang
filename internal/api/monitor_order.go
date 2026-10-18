package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) orderCall(ctx context.Context, account *platformAccount, api, version string, data map[string]any) xianyu.MTOPResult {
	result := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: api, Version: version, Cookie: account.Cookie, Proxy: accountProxy(*account), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/", Data: data})
	if result.Cookie != "" && result.Cookie != account.Cookie {
		account.Cookie = result.Cookie
		_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, result.Cookie, account.ID)
	}
	return result
}

func orderID(result xianyu.MTOPResult) string {
	data := nestedMap(result.Response, "data")
	return firstMapString(data, "bizOrderIdStr", "bizOrderId", "orderId")
}

func yhbOnly(message string) bool {
	for _, marker := range []string{"FAIL_BIZ_ITEM_ONLY_YHB_BUY_APP_LIMIT", "必走验货宝", "ONLY_YHB"} {
		if strings.Contains(strings.ToUpper(message), strings.ToUpper(marker)) {
			return true
		}
	}
	return false
}

func (s *Server) placeYHBOrder(ctx context.Context, account *platformAccount, itemID string) (string, bool, error) {
	address := s.orderCall(ctx, account, "mtop.taobao.idle.logistic.address.list.query", "1.0", map[string]any{})
	if !address.Success {
		return "", address.AccountInvalid, errors.New(firstNonemptyAPI(address.Error, "查询收货地址失败"))
	}
	inner := nestedMap(address.Response, "data", "data")
	list, _ := inner["addressList"].([]any)
	var addressID any
	for _, raw := range list {
		if row, ok := raw.(map[string]any); ok {
			if addressID == nil {
				addressID = row["addressId"]
			}
			if fmt.Sprint(row["status"]) == "1" {
				addressID = row["addressId"]
				break
			}
		}
	}
	if addressID == nil {
		return "", false, errors.New("账号未配置收货地址，验货宝下单需要收货地址")
	}
	yhbVersion, buyQuantity := 3, 1
	render := s.orderCall(ctx, account, "mtop.alibaba.idle.pc.yhb.order.create.render", "1.0", map[string]any{"itemId": itemID})
	if render.AccountInvalid {
		return "", true, errors.New(render.Error)
	}
	if render.Success {
		data := nestedMap(render.Response, "data")
		if intNumber(data["yhbVersion"]) > 0 {
			yhbVersion = intNumber(data["yhbVersion"])
		}
		confirm, _ := data["yhbConfirmBuy"].(map[string]any)
		if intNumber(confirm["buyQuantity"]) > 0 {
			buyQuantity = intNumber(confirm["buyQuantity"])
		}
	}
	channel, _ := json.Marshal(map[string]any{"yhbVersion": yhbVersion})
	created := s.orderCall(ctx, account, "mtop.alibaba.idle.pc.yhb.order.create", "1.0", map[string]any{"itemId": itemID, "optionalPromotionIdValueList": "[]", "buyerAddressId": addressID, "buyQuantity": buyQuantity, "channel": "web", "channelData": string(channel)})
	if !created.Success {
		return "", created.AccountInvalid, errors.New(firstNonemptyAPI(created.Error, "验货宝创建订单失败"))
	}
	return orderID(created), false, nil
}

func (s *Server) placeMonitoredOrder(ctx context.Context, account platformAccount, itemID string) (string, bool, error) {
	if s.orderPlace != nil {
		return s.orderPlace(ctx, account, itemID)
	}
	render := s.orderCall(ctx, &account, "mtop.taobao.idle.trade.order.render", "7.0", map[string]any{"itemId": itemID})
	if !render.Success {
		if yhbOnly(render.Error) {
			return s.placeYHBOrder(ctx, &account, itemID)
		}
		return "", render.AccountInvalid, errors.New(firstNonemptyAPI(render.Error, "下单渲染失败"))
	}
	data := nestedMap(render.Response, "data", "commonData")
	buyInfo := data["itemBuyInfo"]
	if buyInfo == nil {
		return "", false, errors.New("下单渲染缺少itemBuyInfo")
	}
	params, _ := json.Marshal(buyInfo)
	created := s.orderCall(ctx, &account, "mtop.taobao.idle.trade.order.create", "5.0", map[string]any{"params": string(params)})
	if !created.Success {
		if yhbOnly(created.Error) {
			return s.placeYHBOrder(ctx, &account, itemID)
		}
		return "", created.AccountInvalid, errors.New(firstNonemptyAPI(created.Error, "创建订单失败"))
	}
	return orderID(created), false, nil
}

// ExecuteScheduledAutoOrder creates unpaid orders only; it never calls a pay
// endpoint. Owner/item deduplication and per-task limits prevent double orders.
func (s *Server) ExecuteScheduledAutoOrder(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.owner_id,i.data,t.data FROM resource_records i JOIN resource_records t ON t.owner_id=i.owner_id AND t.kind='listing_monitor_task' AND t.enabled=1 AND CAST(t.id AS TEXT)=CAST(json_extract(i.data,'$.monitor_task_id') AS TEXT) WHERE i.kind='listing_monitor_item' AND i.enabled=1 AND i.created_at>=datetime('now','-2 days') AND COALESCE(json_extract(i.data,'$.is_ordered'),0)=0 AND COALESCE(json_extract(i.data,'$.order_attempts'),0)<3 ORDER BY i.owner_id,i.id LIMIT 500`)
	if err != nil {
		return err
	}
	type job struct {
		id, owner        int64
		itemRaw, taskRaw string
	}
	jobs := []job{}
	for rows.Next() {
		var j job
		if rows.Scan(&j.id, &j.owner, &j.itemRaw, &j.taskRaw) == nil {
			jobs = append(jobs, j)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	disabled := map[string]bool{}
	perTask := map[string]int{}
	failures := []string{}
	for _, j := range jobs {
		item, task := map[string]any{}, map[string]any{}
		if json.Unmarshal([]byte(j.itemRaw), &item) != nil || json.Unmarshal([]byte(j.taskRaw), &task) != nil {
			continue
		}
		itemID := strings.TrimSpace(firstString(item, "item_id"))
		taskID := fmt.Sprint(item["monitor_task_id"])
		if itemID == "" {
			continue
		}
		var duplicate int
		_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind='listing_monitor_item' AND id<>? AND json_extract(data,'$.item_id')=? AND json_extract(data,'$.order_status')='success'`, j.owner, j.id, itemID).Scan(&duplicate)
		if duplicate > 0 {
			_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.is_ordered',true,'$.order_status','duplicate','$.order_fail_reason','同一用户已对该商品下单'),updated_at=CURRENT_TIMESTAMP WHERE id=?`, j.id)
			continue
		}
		limit := intNumber(task["order_batch_size"])
		if limit < 1 {
			limit = 5
		}
		if perTask[taskID] >= limit {
			continue
		}
		ids := monitorAccountIDs(task["order_account_ids"])
		if dm := strings.TrimSpace(firstString(item, "dm_account_id")); dm != "" {
			ids = append([]string{dm}, ids...)
		}
		seen := map[string]bool{}
		ordered := false
		attempted := false
		lastError := ""
		noAccount := true
		for _, aid := range ids {
			if seen[aid] || disabled[aid] {
				continue
			}
			seen[aid] = true
			var a platformAccount
			err = s.db.QueryRowContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=? AND status='active'`, j.owner, aid).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
			if err != nil {
				continue
			}
			noAccount = false
			id, invalid, placeErr := s.placeMonitoredOrder(ctx, a, itemID)
			if invalid {
				disabled[aid] = true
				if placeErr != nil {
					lastError = placeErr.Error()
				} else {
					lastError = "账号不可用"
				}
				continue
			}
			attempted = true
			if placeErr != nil {
				lastError = placeErr.Error()
				continue
			}
			if id == "" {
				lastError = "创建订单成功但未返回订单ID"
				continue
			}
			_, err = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.is_ordered',true,'$.order_status','success','$.order_id',?,'$.order_account_id',?,'$.order_fail_reason',NULL,'$.ordered_at',CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND COALESCE(json_extract(data,'$.is_ordered'),0)=0`, id, aid, j.id)
			if err == nil {
				ordered = true
				perTask[taskID]++
			}
			break
		}
		if ordered {
			continue
		}
		if noAccount {
			_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.order_status','no_account','$.order_fail_reason','无可用下单账号'),updated_at=CURRENT_TIMESTAMP WHERE id=?`, j.id)
			continue
		}
		if attempted {
			_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.order_status','failed','$.order_fail_reason',?,'$.order_attempts',COALESCE(json_extract(data,'$.order_attempts'),0)+1),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(lastError, 500), j.id)
			perTask[taskID]++
			failures = append(failures, fmt.Sprintf("商品%s: %s", itemID, lastError))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
