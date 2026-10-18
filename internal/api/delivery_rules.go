package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

var deliveryRuleMetadata = []map[string]any{
	{"rule_code": "personal_blacklist", "rule_name": "个人黑名单", "rule_description": "检查买家是否在个人黑名单中（支持商品级、账户级、用户级匹配）", "default_config": map[string]any{}, "default_priority": 5},
	{"rule_code": "buyer_credit_zero", "rule_name": "买家信用度检查", "rule_description": "检查买家被评价总数，评价数为0（或低于设定阈值）时禁止发货", "default_config": map[string]any{"threshold": 0}, "default_priority": 10},
	{"rule_code": "buyer_has_order", "rule_name": "买家已有订单", "rule_description": "检查买家在当前卖家下是否已有其他订单，有则禁止发货", "default_config": map[string]any{"same_item_only": false}, "default_priority": 20},
	{"rule_code": "buyer_has_order_global", "rule_name": "买家在同用户其他账号已有订单", "rule_description": "检查买家在当前用户名下所有账号中是否已有其他订单，有则禁止发货", "default_config": map[string]any{"same_item_only": false}, "default_priority": 25},
	{"rule_code": "buyer_unconfirmed", "rule_name": "买家存在未确认收货订单", "rule_description": "检查买家在当前卖家下是否有未确认收货的订单，有则禁止发货", "default_config": map[string]any{"min_count": 1, "same_item_only": false}, "default_priority": 30},
}

func (s *Server) deliveryBlockAvailable(w http.ResponseWriter, r *http.Request) {
	ok(w, "查询成功", deliveryRuleMetadata)
}

func (s *Server) deliveryBlockRules(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "id")
	var owned int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&owned)
	if owned == 0 {
		fail(w, 404, "账号不存在")
		return
	}
	stored := map[string]map[string]any{}
	rows, err := s.db.QueryContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND kind='delivery_block_rule' AND account_id=? ORDER BY id`, u.ID, accountID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		_ = rows.Scan(&raw)
		m := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &m)
		stored[fmt.Sprint(m["rule_code"])] = m
	}
	list := make([]map[string]any, 0, len(deliveryRuleMetadata))
	for _, metadata := range deliveryRuleMetadata {
		code := fmt.Sprint(metadata["rule_code"])
		item := map[string]any{}
		for key, value := range metadata {
			item[key] = value
		}
		item["enabled"], item["priority"], item["block_reason"], item["auto_close_order"], item["only_card_after_close"], item["excluded_item_ids"], item["config"] = false, metadata["default_priority"], "", false, false, []string{}, metadata["default_config"]
		if saved := stored[code]; saved != nil {
			for key, value := range saved {
				item[key] = value
			}
		}
		list = append(list, item)
	}
	ok(w, "查询成功", list)
}

func (s *Server) deliveryBlockRulesPut(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "id")
	var p struct {
		Rules []map[string]any `json:"rules"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	var owned int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&owned)
	if owned == 0 {
		fail(w, 404, "账号不存在")
		return
	}
	valid := map[string]bool{}
	for _, metadata := range deliveryRuleMetadata {
		valid[fmt.Sprint(metadata["rule_code"])] = true
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind='delivery_block_rule' AND account_id=?`, u.ID, accountID)
	for _, rule := range p.Rules {
		if !valid[fmt.Sprint(rule["rule_code"])] {
			write(w, 200, response{Success: false, Message: "存在未知的禁止发货规则"})
			return
		}
		raw, _ := json.Marshal(rule)
		enabled, _ := rule["enabled"].(bool)
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,'delivery_block_rule',?,?,?)`, u.ID, accountID, enabled, string(raw)); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil && err != sql.ErrTxDone {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "禁止发货规则已更新", nil)
}
