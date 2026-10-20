package xianyu

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func usableOrderChat(chat string) bool {
	chat = strings.TrimSpace(chat)
	return chat != "" && !strings.HasPrefix(strings.ToUpper(chat), "FAILED_")
}

// EnsureOrderChat repairs only the named account's order and preserves other
// order data. Failed attempts remain retryable; callers apply their cooldown.
func EnsureOrderChat(ctx context.Context, db *sql.DB, owner int64, account, order string, create func(context.Context, string, string, string) (string, error)) (string, error) {
	var id int64
	var item, buyer, existing string
	const chatExpr = `COALESCE(NULLIF(json_extract(data,'$.chat_id'),''),json_extract(data,'$.chatId'),'')`
	err := db.QueryRowContext(ctx, `SELECT id,COALESCE(item_id,''),COALESCE(buyer_id,''),`+chatExpr+` FROM xy_orders WHERE owner_id=? AND account_id=? AND order_no=?`, owner, account, order).Scan(&id, &item, &buyer, &existing)
	if err != nil {
		return "", errors.New("订单不存在或不属于当前账号")
	}
	if usableOrderChat(existing) {
		return strings.TrimSpace(existing), nil
	}
	if item == "" || buyer == "" {
		return "", errors.New("订单缺少商品或买家信息")
	}
	const eventChat = `COALESCE(NULLIF(json_extract(data,'$.ChatID'),''),NULLIF(json_extract(data,'$.cid'),''),json_extract(data,'$.chat_id'),'')`
	const eventItem = `COALESCE(json_extract(data,'$.ItemID'),json_extract(data,'$.itemId'),json_extract(data,'$.item_id'),'')`
	rows, err := db.QueryContext(ctx, `SELECT `+eventChat+` FROM event_logs WHERE owner_id=? AND account_id=? AND kind IN ('incoming_message','outgoing_message')
	 AND CASE WHEN kind='outgoing_message' THEN COALESCE(json_extract(data,'$.toUserId'),json_extract(data,'$.to_user_id'),'') ELSE COALESCE(json_extract(data,'$.SenderID'),json_extract(data,'$.senderId'),json_extract(data,'$.send_user_id'),'') END=?
	 AND `+eventItem+` IN ('',?) ORDER BY CASE WHEN `+eventItem+`=? THEN 0 ELSE 1 END,id DESC LIMIT 50`, owner, account, buyer, item, item)
	if err != nil {
		return "", err
	}
	chat := ""
	for rows.Next() {
		var candidate string
		if err = rows.Scan(&candidate); err != nil {
			break
		}
		if usableOrderChat(candidate) {
			chat = strings.TrimSpace(candidate)
			break
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	if rowErr != nil {
		return "", rowErr
	}
	if chat == "" {
		if create == nil {
			return "", errors.New("订单缺少会话ID，且创建会话服务未就绪")
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		chat, err = create(callCtx, account, buyer, item)
		if err != nil {
			return "", fmt.Errorf("创建买家会话失败: %w", err)
		}
		chat = strings.TrimSpace(chat)
		if !usableOrderChat(chat) {
			return "", errors.New("创建买家会话未返回有效会话ID")
		}
	}
	result, err := db.ExecContext(ctx, `UPDATE xy_orders SET data=json_set(COALESCE(data,'{}'),'$.chat_id',?,'$.chatId',?),updated_at=CURRENT_TIMESTAMP WHERE id=? AND owner_id=? AND account_id=? AND `+chatExpr+`=?`, chat, chat, id, owner, account, existing)
	if err != nil {
		return "", err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		if err = db.QueryRowContext(ctx, `SELECT `+chatExpr+` FROM xy_orders WHERE id=? AND owner_id=? AND account_id=?`, id, owner, account).Scan(&chat); err != nil || !usableOrderChat(chat) {
			return "", errors.New("订单会话已变化，请稍后重试")
		}
	}
	return chat, nil
}
