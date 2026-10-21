package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) autoRateBatch(w http.ResponseWriter, r *http.Request) {
	var p struct {
		AccountIDs []string `json:"account_ids"`
	}
	if decode(r, &p) != nil || len(p.AccountIDs) == 0 {
		fail(w, http.StatusBadRequest, "请选择账号")
		return
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range p.AccountIDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	u := current(r)
	details := []map[string]any{}
	totalRated, totalFailed, successAccounts := 0, 0, 0
	for _, accountID := range ids {
		result := map[string]any{"account_id": accountID, "success": false, "rated_count": 0, "failed_count": 0, "total_pending": 0, "message": ""}
		account, found := s.rateAccount(r, u, accountID)
		if !found {
			result["message"] = "账号不存在或无权限"
			details = append(details, result)
			continue
		}
		cfg, found := s.rateConfig(r, account.OwnerID, accountID)
		if !found || !truthy(cfg["enabled"]) {
			result["message"] = "未启用自动评价"
			details = append(details, result)
			continue
		}
		feedback := s.rateFeedback(r, cfg)
		if feedback == "" {
			result["message"] = "获取评价内容失败"
			details = append(details, result)
			continue
		}
		items, res := s.pendingRateItems(r, account)
		if !res.Success {
			result["message"] = "获取待评价列表失败: " + firstNonemptyAPI(res.Error, "未知错误")
			details = append(details, result)
			continue
		}
		result["total_pending"] = len(items)
		rated, failed := 0, 0
		for index, item := range items {
			common, _ := item["merchantCommonData"].(map[string]any)
			orderID := firstText(common["orderId"], item["orderId"])
			if orderID == "" {
				failed++
				continue
			}
			rate := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idle.rate.create", Version: "4.0", Cookie: account.Cookie, Proxy: accountProxy(account), Data: map[string]any{"tradeId": orderID, "rate": 1, "feedback": feedback, "createOrAppend": 0}})
			if rate.Cookie != "" && rate.Cookie != account.Cookie {
				account.Cookie = rate.Cookie
				_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, rate.Cookie, account.ID)
			}
			if rate.Success {
				rated++
				s.markOrderRated(r, account.OwnerID, orderID)
			} else {
				failed++
			}
			if index < len(items)-1 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
			}
		}
		result["success"] = true
		result["rated_count"] = rated
		result["failed_count"] = failed
		result["message"] = "评价完成: 成功 " + strconv.Itoa(rated) + " 笔，失败 " + strconv.Itoa(failed) + " 笔"
		totalRated += rated
		totalFailed += failed
		successAccounts++
		details = append(details, result)
	}
	ok(w, "批量补评价完成: "+strconv.Itoa(successAccounts)+"/"+strconv.Itoa(len(ids))+" 个账号处理成功，共评价 "+strconv.Itoa(totalRated)+" 笔订单", map[string]any{"total_rated": totalRated, "total_failed": totalFailed, "success_accounts": successAccounts, "total_accounts": len(ids), "details": details})
}

func (s *Server) rateAccount(r *http.Request, u User, id string) (platformAccount, bool) {
	var a platformAccount
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE account_id=? AND (owner_id=? OR ?='ADMIN')`, id, u.ID, strings.ToUpper(u.Role)).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
	return a, err == nil && strings.TrimSpace(a.Cookie) != ""
}
func (s *Server) rateConfig(r *http.Request, owner int64, account string) (map[string]any, bool) {
	var raw string
	if s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND kind='auto_rate' AND account_id=? ORDER BY id DESC LIMIT 1`, owner, account).Scan(&raw) != nil {
		return nil, false
	}
	m := map[string]any{}
	mergeJSON(m, raw)
	return m, true
}
func (s *Server) rateFeedback(r *http.Request, cfg map[string]any) string {
	typ := strings.ToLower(firstText(cfg["rate_type"], "text"))
	if typ == "text" {
		return firstText(cfg["text_content"], "不错的买家")
	}
	if typ != "api" {
		return ""
	}
	target := strings.TrimSpace(text(cfg["api_url"]))
	if target == "" {
		return ""
	}
	req, e := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if e != nil {
		return ""
	}
	resp, e := proxy.Client(30 * time.Second).Do(req)
	if e != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}
func (s *Server) pendingRateItems(r *http.Request, a platformAccount) ([]map[string]any, xianyu.MTOPResult) {
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idle.merchant.rate.list", Version: "1.0", Cookie: a.Cookie, Proxy: accountProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"pageNumber": 1, "rowsPerPage": 100, "queryType": "ORDER", "rateSearchParam": map[string]any{"sellerRateStatus": "5"}}, ExtraParams: map[string]string{"type": "json", "valueType": "string"}})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Cookie, a.ID)
	}
	if !res.Success {
		return nil, res
	}
	data, _ := res.Response["data"].(map[string]any)
	module, _ := data["module"].(map[string]any)
	raw, _ := module["items"].([]any)
	items := []map[string]any{}
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items, res
}
func (s *Server) markOrderRated(r *http.Request, owner int64, order string) {
	var raw string
	if s.db.QueryRowContext(r.Context(), `SELECT data FROM xy_orders WHERE owner_id=? AND order_no=?`, owner, order).Scan(&raw) != nil {
		return
	}
	m := map[string]any{}
	mergeJSON(m, raw)
	m["is_rated"] = true
	b, _ := json.Marshal(m)
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_orders SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND order_no=?`, string(b), owner, order)
}
