package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type platformItemBatchRequest struct {
	CookieID string   `json:"cookie_id"`
	ItemIDs  []string `json:"item_ids"`
}

func cleanItemIDs(values []string, maximum int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) == maximum {
			break
		}
	}
	return out
}

func (s *Server) itemOperationAccount(r *http.Request, payload platformItemBatchRequest) (platformAccount, []string, error) {
	ids := cleanItemIDs(payload.ItemIDs, 100)
	if payload.CookieID == "" || len(ids) == 0 {
		return platformAccount{}, nil, fmt.Errorf("请选择账号和商品")
	}
	accounts, err := s.loadPlatformAccounts(r, payload.CookieID)
	if err != nil || len(accounts) == 0 {
		return platformAccount{}, nil, fmt.Errorf("账号不存在或未启用")
	}
	return accounts[0], ids, nil
}

func persistPlatformCookie(s *Server, r *http.Request, account platformAccount, cookie string) {
	if cookie != "" && cookie != account.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cookie, account.ID)
	}
}

func (s *Server) batchOfflineItems(w http.ResponseWriter, r *http.Request) {
	var payload platformItemBatchRequest
	if decode(r, &payload) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	account, ids, err := s.itemOperationAccount(r, payload)
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	result := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{
		API: "mtop.alibaba.idle.seller.pc.item.batch.offline", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account),
		Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO",
		Data:         map[string]any{"itemIds": strings.Join(ids, ",")},
		ExtraParams:  map[string]string{"needLoginPC": "true", "showErrorToast": "true", "spm_cnt": "a21107h.42826273.0.0"},
		ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO"},
	})
	persistPlatformCookie(s, r, account, result.Cookie)
	if !result.Success {
		write(w, 200, response{Success: false, Message: result.Error, Data: map[string]any{"suc_count": 0, "fail_count": len(ids), "results": platformResults(ids, false)}})
		return
	}
	inner := nestedMap(result.Response, "data", "data")
	successCount, failCount := intNumber(inner["sucCount"]), intNumber(inner["failCount"])
	results := []map[string]any{}
	if raw, ok := inner["itemProcessResultList"].([]any); ok {
		for _, entry := range raw {
			if item, ok := entry.(map[string]any); ok {
				results = append(results, map[string]any{"item_id": fmt.Sprint(item["itemId"]), "success": item["success"] == true})
			}
		}
	}
	if len(results) == 0 && successCount == 0 && failCount == 0 {
		successCount = len(ids)
		results = platformResults(ids, true)
	}
	if successCount == 0 && len(results) > 0 {
		for _, item := range results {
			if item["success"] == true {
				successCount++
			} else {
				failCount++
			}
		}
	}
	write(w, 200, response{Success: successCount > 0, Message: fmt.Sprintf("已下架 %d 个商品，%d 个失败", successCount, failCount), Data: map[string]any{"suc_count": successCount, "fail_count": failCount, "results": results}})
}

func (s *Server) batchDeletePlatformItems(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var payload platformItemBatchRequest
	if decode(r, &payload) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	account, ids, err := s.itemOperationAccount(r, payload)
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	results, succeeded := []map[string]any{}, []string{}
	currentCookie := account.Cookie
	for _, itemID := range ids {
		res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{
			API: "mtop.alibaba.idle.seller.pc.item.delete", Version: "1.0", Cookie: currentCookie, Proxy: accountProxy(account),
			Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO",
			Data:         map[string]any{"itemId": itemID, "draftId": nil},
			ExtraParams:  map[string]string{"needLoginPC": "true", "showErrorToast": "true", "spm_cnt": "a21107h.42829799.0.0"},
			ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO"},
		})
		if res.Cookie != "" {
			currentCookie = res.Cookie
		}
		entry := map[string]any{"item_id": itemID, "success": res.Success, "message": res.Error}
		if res.Success {
			entry["message"] = "删除成功"
			succeeded = append(succeeded, itemID)
		}
		results = append(results, entry)
	}
	persistPlatformCookie(s, r, account, currentCookie)
	localDeleted := int64(0)
	if len(succeeded) > 0 {
		args := []any{u.ID, account.AccountID}
		for _, id := range succeeded {
			args = append(args, id)
		}
		res, _ := s.db.ExecContext(r.Context(), `DELETE FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id IN (`+placeholders(len(succeeded))+`)`, args...)
		if res != nil {
			localDeleted, _ = res.RowsAffected()
		}
	}
	failCount := len(ids) - len(succeeded)
	write(w, 200, response{Success: len(succeeded) > 0, Message: fmt.Sprintf("已删除 %d 个闲鱼商品，%d 个失败", len(succeeded), failCount), Data: map[string]any{"results": results, "success_count": len(succeeded), "fail_count": failCount, "local_deleted_count": localDeleted, "local_failed_ids": []string{}}})
}

func platformResults(ids []string, success bool) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"item_id": id, "success": success})
	}
	return out
}

func intNumber(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
