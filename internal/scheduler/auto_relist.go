package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type relistEvent struct {
	ID, UserID, RuleID, MaterialID int64
	AccountID, OrderNo, OldItemID  string
	Status, PublishState           string
	Attempt                        int
	PublishItemID                  sql.NullString
}

func (e *Engine) autoRelist(ctx context.Context, cfg map[string]any) error {
	if e.publish == nil {
		return errors.New("自动续售发布器未配置")
	}
	batch := positiveInt(cfg["batch_size"], 20)
	lease := positiveInt(cfg["lease_seconds"], 300)
	maxRetries := positiveInt(cfg["max_retries"], 3)
	if err := e.discoverRelistEvents(ctx); err != nil {
		return err
	}
	if err := e.recoverRelistLeases(ctx); err != nil {
		return err
	}
	rows, err := e.db.QueryContext(ctx, `SELECT id FROM xy_auto_relist_events WHERE status IN ('pending','retry','migration_retry') AND (next_retry_at IS NULL OR next_retry_at<=CURRENT_TIMESTAMP) AND (lease_expires_at IS NULL OR lease_expires_at<CURRENT_TIMESTAMP) ORDER BY id LIMIT ?`, batch)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var failures []string
	for _, id := range ids {
		if err = e.processRelistEvent(ctx, id, lease, maxRetries); err != nil {
			failures = append(failures, fmt.Sprintf("事件%d: %v", id, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func positiveInt(value any, fallback int) int {
	if n, ok := value.(float64); ok && n > 0 {
		return int(n)
	}
	return fallback
}

func (e *Engine) discoverRelistEvents(ctx context.Context) error {
	// delivery_content/card_only_delivered are kept inside the compatibility JSON.
	// The predicates mirror the original service and deliberately reject merely
	// paid orders so a relist can never race normal delivery.
	_, err := e.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO xy_auto_relist_events
		(user_id,rule_id,material_id,account_id,order_no,old_item_id,status,next_retry_at,publish_request_id,publish_state)
		SELECT r.user_id,r.id,r.material_id,r.account_id,o.order_no,r.current_item_id,'pending',
		       datetime(COALESCE(o.updated_at,CURRENT_TIMESTAMP),'+'||MAX(r.delay_seconds,0)||' seconds'),
		       'auto-relist:'||r.id||':'||lower(hex(randomblob(16))),'not_started'
		FROM xy_auto_relist_rules r JOIN xy_orders o
		  ON o.owner_id=r.user_id AND o.account_id=r.account_id AND o.item_id=r.current_item_id
		WHERE r.enabled=1 AND r.status NOT IN ('paused','disabled')
		  AND trim(COALESCE(json_extract(o.data,'$.delivery_content'),json_extract(o.data,'$.deliveryContent'),''))<>''
		  AND ((lower(o.status) IN ('shipped','completed','已发货','已完成') AND lower(COALESCE(o.delivery_method,''))='auto')
		    OR (COALESCE(json_extract(o.data,'$.card_only_delivered'),json_extract(o.data,'$.cardOnlyDelivered'),0)=1
		      AND lower(COALESCE(o.delivery_method,'')) IN ('auto','scheduled')
		      AND lower(o.status) NOT IN ('cancelled','refunded','refunding','已取消','已退款','退款中')))`)
	if err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx, `UPDATE xy_auto_relist_rules SET
		last_order_no=(SELECT ev.order_no FROM xy_auto_relist_events ev WHERE ev.rule_id=xy_auto_relist_rules.id ORDER BY ev.id DESC LIMIT 1),
		last_order_id=(SELECT o.id FROM xy_orders o WHERE o.order_no=last_order_no),
		last_order_updated_at=(SELECT o.updated_at FROM xy_orders o WHERE o.order_no=last_order_no),updated_at=CURRENT_TIMESTAMP
		WHERE enabled=1 AND EXISTS(SELECT 1 FROM xy_auto_relist_events ev WHERE ev.rule_id=xy_auto_relist_rules.id)`)
	return err
}

func (e *Engine) recoverRelistLeases(ctx context.Context) error {
	_, err := e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET
		status=CASE WHEN publish_state='succeeded' AND COALESCE(publish_item_id,'')<>'' THEN 'migration_retry'
		            WHEN status IN ('claimed','checking') AND publish_state='not_started' THEN 'retry' ELSE 'unknown' END,
		result_unknown=CASE WHEN (publish_state='succeeded' AND COALESCE(publish_item_id,'')<>'') OR (status IN ('claimed','checking') AND publish_state='not_started') THEN 0 ELSE 1 END,
		publish_state=CASE WHEN status IN ('claimed','checking') AND publish_state='not_started' THEN 'not_started'
		                   WHEN publish_state='succeeded' THEN publish_state ELSE 'unknown' END,
		next_retry_at=CASE WHEN (publish_state='succeeded' AND COALESCE(publish_item_id,'')<>'') OR (status IN ('claimed','checking') AND publish_state='not_started') THEN CURRENT_TIMESTAMP ELSE NULL END,
		error_message='任务租约已过期，已按发布阶段恢复',claim_token=NULL,claimed_at=NULL,lease_expires_at=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE status IN ('claimed','checking','publishing','reconciling') AND (lease_expires_at IS NULL OR lease_expires_at<CURRENT_TIMESTAMP)`)
	return err
}

func (e *Engine) processRelistEvent(ctx context.Context, id int64, leaseSeconds, maxRetries int) error {
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), id)
	res, err := e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET status='claimed',claim_token=?,claimed_at=CURRENT_TIMESTAMP,lease_expires_at=datetime('now',?),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('pending','retry','migration_retry') AND (next_retry_at IS NULL OR next_retry_at<=CURRENT_TIMESTAMP) AND (lease_expires_at IS NULL OR lease_expires_at<CURRENT_TIMESTAMP)`, token, fmt.Sprintf("+%d seconds", leaseSeconds), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	var ev relistEvent
	err = e.db.QueryRowContext(ctx, `SELECT id,user_id,rule_id,material_id,account_id,order_no,old_item_id,status,attempt_count,publish_state,publish_item_id FROM xy_auto_relist_events WHERE id=? AND claim_token=?`, id, token).Scan(&ev.ID, &ev.UserID, &ev.RuleID, &ev.MaterialID, &ev.AccountID, &ev.OrderNo, &ev.OldItemID, &ev.Status, &ev.Attempt, &ev.PublishState, &ev.PublishItemID)
	if err != nil {
		return err
	}
	var enabled int
	var currentItem, accountID string
	var cardID int64
	err = e.db.QueryRowContext(ctx, `SELECT enabled,current_item_id,account_id,card_id FROM xy_auto_relist_rules WHERE id=? AND user_id=?`, ev.RuleID, ev.UserID).Scan(&enabled, &currentItem, &accountID, &cardID)
	if err != nil || enabled != 1 || currentItem != ev.OldItemID || accountID != ev.AccountID {
		return e.finishRelistEvent(ctx, ev, token, "skipped", "规则已停用或商品已变化", "", false)
	}
	if ev.PublishState == "succeeded" && ev.PublishItemID.Valid {
		return e.completeRelistMigration(ctx, ev, token, cardID, ev.PublishItemID.String)
	}
	var orderEligible int
	err = e.db.QueryRowContext(ctx, `SELECT CASE WHEN trim(COALESCE(json_extract(data,'$.delivery_content'),json_extract(data,'$.deliveryContent'),''))<>'' AND ((lower(status) IN ('shipped','completed','已发货','已完成') AND lower(COALESCE(delivery_method,''))='auto') OR (COALESCE(json_extract(data,'$.card_only_delivered'),json_extract(data,'$.cardOnlyDelivered'),0)=1 AND lower(COALESCE(delivery_method,'')) IN ('auto','scheduled') AND lower(status) NOT IN ('cancelled','refunded','refunding','已取消','已退款','退款中'))) THEN 1 ELSE 0 END FROM xy_orders WHERE owner_id=? AND order_no=?`, ev.UserID, ev.OrderNo).Scan(&orderEligible)
	if err != nil || orderEligible != 1 {
		return e.finishRelistEvent(ctx, ev, token, "skipped", "订单已不满足自动续售条件", "", false)
	}
	if err = e.ensureRelistDependencies(ctx, ev, cardID); err != nil {
		return e.retryRelist(ctx, ev, token, maxRetries, err)
	}
	live, err := e.relistItemLive(ctx, ev.UserID, ev.AccountID, ev.OldItemID)
	if err != nil {
		return e.retryRelist(ctx, ev, token, maxRetries, fmt.Errorf("确认旧商品状态: %w", err))
	}
	if live {
		return e.finishRelistEvent(ctx, ev, token, "skipped", "旧商品仍在售，无需续售", "", false)
	}
	ev.Attempt++
	requestID := relistRequestID(ev.RuleID, ev.OrderNo, ev.OldItemID)
	res, err = e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET status='publishing',attempt_count=?,publish_request_id=?,publish_state='started',last_checked_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND claim_token=? AND status='claimed'`, ev.Attempt, requestID, ev.ID, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("自动续售事件租约已失效")
	}
	newID, unknown, publishErr := e.publish(ctx, ev.UserID, ev.MaterialID, ev.AccountID)
	if publishErr != nil {
		if unknown {
			return e.finishRelistEvent(ctx, ev, token, "unknown", publishErr.Error(), newID, true)
		}
		return e.retryRelist(ctx, ev, token, maxRetries, publishErr)
	}
	if _, err = e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET publish_state='succeeded',publish_item_id=?,new_item_id=?,status='reconciling',last_checked_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND claim_token=? AND status='publishing'`, newID, newID, ev.ID, token); err != nil {
		// The platform may already have created an item. Never publish again when
		// persistence becomes ambiguous.
		_, _ = e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET status='unknown',result_unknown=1,publish_state='unknown',publish_item_id=?,new_item_id=?,error_message=?,claim_token=NULL,claimed_at=NULL,lease_expires_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, newID, newID, "发布成功但本地结果持久化失败: "+err.Error(), ev.ID)
		return err
	}
	return e.completeRelistMigration(ctx, ev, token, cardID, newID)
}

func (e *Engine) ensureRelistDependencies(ctx context.Context, ev relistEvent, cardID int64) error {
	var count int
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM product_materials WHERE id=? AND owner_id=?`, ev.MaterialID, ev.UserID).Scan(&count); err != nil || count != 1 {
		return errors.New("自动续售素材不存在")
	}
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=? AND status='active'`, ev.UserID, ev.AccountID).Scan(&count); err != nil || count != 1 {
		return errors.New("自动续售账号不存在或未启用")
	}
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM cards WHERE id=? AND user_id=? AND enabled=1`, cardID, ev.UserID).Scan(&count); err != nil || count != 1 {
		return errors.New("关联卡券不存在或未启用")
	}
	return nil
}

func (e *Engine) relistItemLive(ctx context.Context, ownerID int64, accountID, itemID string) (bool, error) {
	if e.relistLiveCheck != nil {
		return e.relistLiveCheck(ctx, ownerID, accountID, itemID)
	}
	var a syncAccount
	err := e.db.QueryRowContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=? AND status='active'`, ownerID, accountID).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
	if err != nil {
		return false, err
	}
	for page := 1; page <= 200; page++ {
		res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.alibaba.idle.seller.pc.common.item.search", Version: "1.0", Cookie: a.Cookie, Proxy: syncProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"pageNo": page, "pageSize": 30, "bizType": "commonPro", "searchRequest": "{}", "itemStatus": "0"}, ExtraParams: map[string]string{"needLoginPC": "true", "accountSite": "xianyu"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO"}})
		if !res.Success {
			return false, errors.New(res.Error)
		}
		biz := syncNested(res.Response, "data", "data")
		items, _ := biz["itemSearchResponseList"].([]any)
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok && strings.TrimSpace(fmt.Sprint(item["itemId"])) == itemID {
				return true, nil
			}
		}
		more, _ := biz["hasNextPage"].(bool)
		if !more {
			return false, nil
		}
	}
	return false, nil
}

func relistRequestID(ruleID int64, orderNo, oldItemID string) string {
	sum := sha256.Sum256([]byte(orderNo + "\x00" + oldItemID))
	return fmt.Sprintf("auto-relist:%d:%s", ruleID, hex.EncodeToString(sum[:16]))
}

func (e *Engine) retryRelist(ctx context.Context, ev relistEvent, token string, maxRetries int, cause error) error {
	attempt := ev.Attempt
	if attempt < 1 {
		attempt = 1
	}
	status, ruleStatus, modifier := "failed", "error", ""
	if attempt < maxRetries {
		delays := []int{60, 300, 900}
		delay := delays[min(attempt-1, len(delays)-1)]
		status, ruleStatus, modifier = "retry", "retrying", fmt.Sprintf("+%d seconds", delay)
	}
	next := any(nil)
	if modifier != "" {
		next = time.Now().Add(time.Duration([]int{60, 300, 900}[min(attempt-1, 2)]) * time.Second).Format("2006-01-02 15:04:05")
	}
	_, _ = e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET status=?,attempt_count=?,next_retry_at=?,error_message=?,claim_token=NULL,claimed_at=NULL,lease_expires_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND claim_token=?`, status, attempt, next, truncateRelist(cause.Error()), ev.ID, token)
	_, _ = e.db.ExecContext(ctx, `UPDATE xy_auto_relist_rules SET status=?,retry_count=?,next_retry_at=?,last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND current_item_id=?`, ruleStatus, attempt, next, truncateRelist(cause.Error()), ev.RuleID, ev.OldItemID)
	return cause
}

func (e *Engine) finishRelistEvent(ctx context.Context, ev relistEvent, token, status, message, itemID string, unknown bool) error {
	publishState := ev.PublishState
	if unknown {
		publishState = "unknown"
	}
	_, err := e.db.ExecContext(ctx, `UPDATE xy_auto_relist_events SET status=?,publish_state=?,result_unknown=?,publish_item_id=CASE WHEN ?<>'' THEN ? ELSE publish_item_id END,new_item_id=CASE WHEN ?<>'' THEN ? ELSE new_item_id END,error_message=?,next_retry_at=NULL,claim_token=NULL,claimed_at=NULL,lease_expires_at=NULL,last_checked_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND claim_token=?`, status, publishState, unknown, itemID, itemID, itemID, itemID, truncateRelist(message), ev.ID, token)
	return err
}

func (e *Engine) completeRelistMigration(ctx context.Context, ev relistEvent, token string, cardID int64, newItemID string) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A successful publish is immediately represented in the local catalog so
	// every copied association has a valid target before the next catalog sync.
	_, err = tx.ExecContext(ctx, `INSERT INTO xy_catalog_items(owner_id,account_id,item_id,status,data) VALUES(?,?,?,'published',json_object('item_id',?,'source','auto_relist')) ON CONFLICT(account_id,item_id) DO NOTHING`, ev.UserID, ev.AccountID, newItemID, newItemID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO card_item_relations(card_id,item_id,source,dock_record_id) SELECT card_id,?,source,dock_record_id FROM card_item_relations WHERE item_id=? AND card_id=?`, newItemID, ev.OldItemID, cardID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO xy_keyword_rules(owner_id,account_id,keyword,reply,item_id,type,extra) SELECT owner_id,account_id,keyword,reply,?,type,extra FROM xy_keyword_rules old WHERE owner_id=? AND account_id=? AND item_id=? AND NOT EXISTS(SELECT 1 FROM xy_keyword_rules n WHERE n.owner_id=old.owner_id AND n.account_id=old.account_id AND n.item_id=? AND n.keyword=old.keyword)`, newItemID, ev.UserID, ev.AccountID, ev.OldItemID, newItemID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO default_replies(owner_id,account_id,reply,enabled,data) SELECT owner_id,account_id,reply,enabled,json_set(COALESCE(data,'{}'),'$.item_id',?) FROM default_replies old WHERE owner_id=? AND account_id=? AND json_extract(old.data,'$.item_id')=? AND NOT EXISTS(SELECT 1 FROM default_replies n WHERE n.owner_id=old.owner_id AND n.account_id=old.account_id AND json_extract(n.data,'$.item_id')=?)`, newItemID, ev.UserID, ev.AccountID, ev.OldItemID, newItemID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE xy_catalog_items SET data=json_set(COALESCE(data,'{}'),'$.ai_prompt',(SELECT json_extract(old.data,'$.ai_prompt') FROM xy_catalog_items old WHERE old.owner_id=? AND old.account_id=? AND old.item_id=?)) WHERE owner_id=? AND account_id=? AND item_id=? AND (SELECT json_extract(old.data,'$.ai_prompt') FROM xy_catalog_items old WHERE old.owner_id=? AND old.account_id=? AND old.item_id=?) IS NOT NULL`, ev.UserID, ev.AccountID, ev.OldItemID, ev.UserID, ev.AccountID, newItemID, ev.UserID, ev.AccountID, ev.OldItemID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE xy_accounts SET delivery_disabled_excluded_items=json_insert(CASE WHEN json_valid(delivery_disabled_excluded_items) THEN delivery_disabled_excluded_items ELSE '[]' END,'$[#]',?),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(delivery_disabled_excluded_items) THEN delivery_disabled_excluded_items ELSE '[]' END) WHERE value=?) AND NOT EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(delivery_disabled_excluded_items) THEN delivery_disabled_excluded_items ELSE '[]' END) WHERE value=?)`, newItemID, ev.UserID, ev.AccountID, ev.OldItemID, newItemID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE resource_records SET data=json_insert(data,'$.excluded_item_ids[#]',?),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND kind='delivery_block_rule' AND json_type(data,'$.excluded_item_ids')='array' AND EXISTS(SELECT 1 FROM json_each(data,'$.excluded_item_ids') WHERE value=?) AND NOT EXISTS(SELECT 1 FROM json_each(data,'$.excluded_item_ids') WHERE value=?)`, newItemID, ev.UserID, ev.AccountID, ev.OldItemID, newItemID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) SELECT owner_id,kind,account_id,enabled,json_set(data,'$.item_id',?) FROM resource_records old WHERE owner_id=? AND kind='personal_blacklist' AND COALESCE(account_id,'') IN ('',?) AND json_extract(data,'$.item_id')=? AND NOT EXISTS(SELECT 1 FROM resource_records n WHERE n.owner_id=old.owner_id AND n.kind='personal_blacklist' AND COALESCE(n.account_id,'')=COALESCE(old.account_id,'') AND json_extract(n.data,'$.buyer_id')=json_extract(old.data,'$.buyer_id') AND json_extract(n.data,'$.item_id')=?)`, newItemID, ev.UserID, ev.AccountID, ev.OldItemID, newItemID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE xy_auto_relist_events SET status='success',publish_state='succeeded',result_unknown=0,new_item_id=?,publish_item_id=?,next_retry_at=NULL,error_message=NULL,claim_token=NULL,claimed_at=NULL,lease_expires_at=NULL,last_checked_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND claim_token=?`, newItemID, newItemID, ev.ID, token)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE xy_auto_relist_rules SET current_item_id=?,status='active',version=version+1,retry_count=0,next_retry_at=NULL,last_old_item_id=?,last_new_item_id=?,last_error=NULL,paused_reason=NULL,last_relisted_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND current_item_id=? AND enabled=1`, newItemID, ev.OldItemID, newItemID, ev.RuleID, ev.UserID, ev.OldItemID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func truncateRelist(s string) string {
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}

// Kept here because migration helpers may need to extend JSON arrays as the
// compatibility schema evolves.
func decodeRelistJSON(raw string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
