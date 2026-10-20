package xianyu

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// SelectDeliveryCard requires exact specification matches when either the item
// or a linked card is multi-spec. Missing details must never select the first SKU.
func SelectDeliveryCard(ctx context.Context, db *sql.DB, owner int64, account, item, orderData string) (int64, string, string, error) {
	order := map[string]any{}
	_ = json.Unmarshal([]byte(orderData), &order)
	name, value := stringSetting(order, "spec_name"), stringSetting(order, "spec_value")
	var itemData string
	_ = db.QueryRowContext(ctx, `SELECT data FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, owner, account, item).Scan(&itemData)
	itemConfig := map[string]any{}
	_ = json.Unmarshal([]byte(itemData), &itemConfig)
	multi := boolSetting(itemConfig, "is_multi_spec")
	rows, err := db.QueryContext(ctx, `SELECT c.id,c.type,c.data FROM cards c JOIN card_item_relations r ON r.card_id=c.id WHERE c.user_id=? AND c.enabled=1 AND r.item_id=? ORDER BY c.id`, owner, item)
	if err != nil {
		return 0, "", "", err
	}
	defer rows.Close()
	type candidate struct {
		id                    int64
		typ, raw, name, value string
		multi                 bool
	}
	cards := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.typ, &c.raw); err != nil {
			return 0, "", "", err
		}
		data := map[string]any{}
		if err = json.Unmarshal([]byte(c.raw), &data); err != nil {
			return 0, "", "", err
		}
		c.multi = boolSetting(data, "is_multi_spec")
		c.name = stringSetting(data, "spec_name")
		c.value = stringSetting(data, "spec_value")
		multi = multi || c.multi
		cards = append(cards, c)
	}
	if err = rows.Err(); err != nil {
		return 0, "", "", err
	}
	if multi && (name == "" || value == "") {
		return 0, "", "", errors.New("多规格订单缺少规格信息，请先同步订单详情")
	}
	for _, c := range cards {
		if multi && (!c.multi || c.name != name || c.value != value) {
			continue
		}
		return c.id, c.typ, c.raw, nil
	}
	return 0, "", "", errors.New("未找到与订单规格匹配的可用卡券")
}
