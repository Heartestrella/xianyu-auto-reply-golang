package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (e *Engine) polishItems(ctx context.Context, _ map[string]any) error {
	accounts, err := e.accountsWithFlag(ctx, "auto_polish")
	if err != nil {
		return err
	}
	failures := []string{}
	for _, a := range accounts {
		rows, x := e.db.QueryContext(ctx, `SELECT item_id FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND COALESCE(json_extract(data,'$.is_polished'),0)=0 ORDER BY id`, a.OwnerID, a.AccountID)
		if x != nil {
			failures = append(failures, a.AccountID+": "+x.Error())
			continue
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && id != "" {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, itemID := range ids {
			res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idle.item.polish", Version: "2.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/", Data: map[string]any{"itemId": itemID}, ExtraParams: map[string]string{"spm_pre": "a21ybx.personal.feeds.1.42f86ac21eZ9zd", "log_id": "42f86ac21eZ9zd"}})
			if res.Cookie != "" && res.Cookie != a.Cookie {
				a.Cookie = res.Cookie
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
			}
			success := res.Success || strings.Contains(res.Error, "POLISH_DUPLICATE") || strings.Contains(res.Error, "一天只能擦亮一次") || strings.Contains(res.Error, "宝贝已经擦亮过了")
			if success {
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_catalog_items SET data=json_set(COALESCE(data,'{}'),'$.is_polished',true),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, a.OwnerID, a.AccountID, itemID)
			} else {
				failures = append(failures, a.AccountID+"/"+itemID+": "+res.Error)
			}
			logData, _ := json.Marshal(map[string]any{"item_id": itemID, "success": success, "error": res.Error})
			_, _ = e.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'scheduled_polish',?,?,?,?)`, a.OwnerID, a.AccountID, map[bool]string{true: "INFO", false: "ERROR"}[success], map[bool]string{true: "擦亮成功", false: "擦亮失败"}[success], string(logData))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("擦亮部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (e *Engine) autoRate(ctx context.Context, _ map[string]any) error {
	accounts, err := e.accountsWithFlag(ctx, "scheduled_rate")
	if err != nil {
		return err
	}
	failures := []string{}
	for _, a := range accounts {
		cfg, ok := e.rateSettings(ctx, a)
		if !ok {
			continue
		}
		feedback := rateFeedback(ctx, cfg)
		if feedback == "" {
			failures = append(failures, a.AccountID+": 评价内容为空")
			continue
		}
		list := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idle.merchant.rate.list", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"pageNumber": 1, "rowsPerPage": 100, "queryType": "ORDER", "rateSearchParam": map[string]any{"sellerRateStatus": "5"}}, ExtraParams: map[string]string{"type": "json", "valueType": "string"}})
		if !list.Success {
			failures = append(failures, a.AccountID+": "+list.Error)
			continue
		}
		if list.Cookie != "" {
			a.Cookie = list.Cookie
		}
		module := syncNested(list.Response, "data", "module")
		items, _ := module["items"].([]any)
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			common := syncNested(item, "merchantCommonData")
			orderID := strings.TrimSpace(fmt.Sprint(common["orderId"]))
			if orderID == "" {
				orderID = strings.TrimSpace(fmt.Sprint(item["orderId"]))
			}
			if orderID == "" {
				continue
			}
			res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idle.rate.create", Version: "4.0", Cookie: a.Cookie, Proxy: syncProxy(a), Data: map[string]any{"tradeId": orderID, "rate": 1, "feedback": feedback, "createOrAppend": 0}})
			if res.Cookie != "" && res.Cookie != a.Cookie {
				a.Cookie = res.Cookie
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
			}
			if res.Success {
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_orders SET data=json_set(COALESCE(data,'{}'),'$.is_rated',true),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND order_no=?`, a.OwnerID, orderID)
			} else {
				failures = append(failures, a.AccountID+"/"+orderID+": "+res.Error)
			}
			data, _ := json.Marshal(map[string]any{"order_no": orderID, "success": res.Success, "error": res.Error})
			_, _ = e.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'scheduled_rate',?,?,?,?)`, a.OwnerID, a.AccountID, map[bool]string{true: "INFO", false: "ERROR"}[res.Success], map[bool]string{true: "评价成功", false: "评价失败"}[res.Success], string(data))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("评价部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (e *Engine) accountsWithFlag(ctx context.Context, column string) ([]syncAccount, error) {
	if column != "auto_polish" && column != "scheduled_rate" && column != "auto_red_flower" {
		return nil, fmt.Errorf("不支持的账号开关: %s", column)
	}
	rows, err := e.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass,red_flower_after_shipment FROM xy_accounts WHERE status='active' AND `+column+`=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []syncAccount{}
	for rows.Next() {
		var a syncAccount
		if rows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass, &a.RedFlowerAfterShipment) == nil {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}
func (e *Engine) rateSettings(ctx context.Context, a syncAccount) (map[string]any, bool) {
	var raw string
	if e.db.QueryRowContext(ctx, `SELECT data FROM resource_records WHERE owner_id=? AND kind='auto_rate' AND account_id=? AND enabled=1 ORDER BY id DESC LIMIT 1`, a.OwnerID, a.AccountID).Scan(&raw) != nil {
		return nil, false
	}
	cfg := map[string]any{}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return nil, false
	}
	if value, ok := cfg["enabled"].(bool); ok && !value {
		return nil, false
	}
	return cfg, true
}
func rateFeedback(ctx context.Context, cfg map[string]any) string {
	typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(cfg["rate_type"])))
	if typ == "" || typ == "text" {
		value := strings.TrimSpace(fmt.Sprint(cfg["text_content"]))
		if value == "" || value == "<nil>" {
			value = "不错的买家"
		}
		return value
	}
	if typ != "api" {
		return ""
	}
	target := strings.TrimSpace(fmt.Sprint(cfg["api_url"]))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func (e *Engine) closeNotice(ctx context.Context, _ map[string]any) error {
	accounts, err := e.syncAccounts(ctx)
	if err != nil {
		return err
	}
	failures := []string{}
	for i, a := range accounts {
		res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idlemessage.account.notice.set", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Data: map[string]any{"oprType": 2, "appKeys": []string{"444e9908a51d1cb236a27862abc769c9"}}})
		success := res.Success && syncResponseSuccess(res.Response)
		if !success {
			failures = append(failures, a.AccountID+": "+firstError(res.Error, "接口返回success=false"))
		}
		data, _ := json.Marshal(map[string]any{"success": success, "error": res.Error})
		_, _ = e.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'scheduled_close_notice',?,?,?,?)`, a.OwnerID, a.AccountID, map[bool]string{true: "INFO", false: "ERROR"}[success], map[bool]string{true: "关闭消息通知成功", false: "关闭消息通知失败"}[success], string(data))
		if i < len(accounts)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("关闭消息通知部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (e *Engine) redFlower(ctx context.Context, _ map[string]any) error {
	accounts, err := e.accountsWithFlag(ctx, "auto_red_flower")
	if err != nil {
		return err
	}
	failures := []string{}
	for _, a := range accounts {
		rows, x := e.db.QueryContext(ctx, `SELECT order_no FROM xy_orders WHERE owner_id=? AND account_id=? AND status NOT IN ('cancelled','processing','refunding','refunded') AND (?=0 OR lower(status) IN ('shipped','completed')) AND COALESCE(json_extract(data,'$.is_red_flower'),0)=0 AND COALESCE(json_extract(data,'$.placed_at'),created_at)>=datetime('now','-10 days') ORDER BY id`, a.OwnerID, a.AccountID, a.RedFlowerAfterShipment)
		if x != nil {
			return x
		}
		orders := []string{}
		for rows.Next() {
			var order string
			if rows.Scan(&order) == nil {
				orders = append(orders, order)
			}
		}
		rows.Close()
		for _, orderNo := range orders {
			res := e.callMTOP(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idlemessage.red.flower", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Data: map[string]any{"orderId": orderNo, "channel": "list"}})
			if res.Cookie != "" && res.Cookie != a.Cookie {
				a.Cookie = res.Cookie
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, a.Cookie, a.ID)
			}
			if res.Success {
				_, _ = e.db.ExecContext(ctx, `UPDATE xy_orders SET data=json_set(COALESCE(data,'{}'),'$.is_red_flower',true),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND order_no=?`, a.OwnerID, orderNo)
			} else {
				failures = append(failures, a.AccountID+"/"+orderNo+": "+res.Error)
			}
			data, _ := json.Marshal(map[string]any{"order_no": orderNo, "success": res.Success, "error": res.Error})
			_, _ = e.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'scheduled_red_flower',?,?,?,?)`, a.OwnerID, a.AccountID, map[bool]string{true: "INFO", false: "ERROR"}[res.Success], map[bool]string{true: "求小红花成功", false: "求小红花失败"}[res.Success], string(data))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("求小红花部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}

func syncResponseSuccess(response map[string]any) bool {
	data := syncNested(response, "data")
	value, exists := data["success"]
	if !exists {
		return true
	}
	success, ok := value.(bool)
	return ok && success
}
func firstError(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func (e *Engine) redelivery(ctx context.Context, cfg map[string]any) error {
	if e.redeliver == nil {
		return fmt.Errorf("补发服务未连接")
	}
	cooldown := 120
	if n, ok := cfg["cooldown_seconds"].(float64); ok && n >= 5 {
		cooldown = int(n)
	}
	threshold := time.Now().Unix() - int64(cooldown)
	rows, err := e.db.QueryContext(ctx, `SELECT o.order_no FROM xy_orders o JOIN xy_accounts a ON a.owner_id=o.owner_id AND a.account_id=o.account_id WHERE a.status='active' AND a.scheduled_redelivery=1 AND (a.auto_confirm=1 OR a.only_send_card=1) AND lower(o.status) IN ('pending_ship','pending','paid','待发货') AND COALESCE(json_extract(o.data,'$.auto_delivered'),0)=0 AND COALESCE(json_extract(o.data,'$.last_redelivery_attempt'),0)<? ORDER BY o.id LIMIT 100`, threshold)
	if err != nil {
		return err
	}
	orders := []string{}
	for rows.Next() {
		var order string
		if rows.Scan(&order) == nil {
			orders = append(orders, order)
		}
	}
	rows.Close()
	failures := []string{}
	for _, order := range orders {
		_, _ = e.db.ExecContext(ctx, `UPDATE xy_orders SET data=json_set(COALESCE(data,'{}'),'$.last_redelivery_attempt',?),updated_at=CURRENT_TIMESTAMP WHERE order_no=?`, time.Now().Unix(), order)
		if err = e.redeliver(ctx, order); err != nil {
			failures = append(failures, order+": "+err.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("补发部分失败: %s", strings.Join(failures, "; "))
	}
	return nil
}
