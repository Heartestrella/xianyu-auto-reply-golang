package api

import (
	"net/http"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) dataAnalysisSummary(apiName string, browse bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var p struct {
			AccountID int64  `json:"account_id"`
			DateType  string `json:"date_type"`
			DateRange string `json:"date_range"`
		}
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		if p.DateType == "" {
			p.DateType = "recent7d"
		}
		valid := map[string]bool{"recent1d": true, "recent7d": true, "recent30d": true, "customDate": true}
		if !valid[p.DateType] {
			write(w, 200, response{Success: false, Message: "无效的时间范围类型，支持: recent1d, recent7d, recent30d, customDate"})
			return
		}
		if p.DateType == "customDate" {
			parts := strings.Split(p.DateRange, "|")
			if len(parts) != 2 || len(parts[0]) != 8 || len(parts[1]) != 8 {
				write(w, 200, response{Success: false, Message: "日期范围格式错误，正确格式: yyyyMMdd|yyyyMMdd"})
				return
			}
		}
		var account platformAccount
		query := `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE id=?`
		args := []any{p.AccountID}
		if u.Role != "ADMIN" {
			query += ` AND owner_id=?`
			args = append(args, u.ID)
		}
		if s.db.QueryRowContext(r.Context(), query, args...).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass) != nil {
			write(w, 200, response{Success: false, Message: "账号不存在或无权限访问"})
			return
		}
		payload := map[string]any{"dateType": p.DateType}
		if !browse {
			payload["dateRange"] = p.DateRange
			payload["ms"] = ""
			payload["selectedSellerId"] = "undefined"
		} else if p.DateType == "customDate" {
			payload["dateRange"] = p.DateRange
		}
		result := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: apiName, Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: payload, ExtraParams: map[string]string{"showErrorToast": "true", "sessionOption": "AutoLoginOnly"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
		if result.Cookie != "" && result.Cookie != account.Cookie {
			_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, result.Cookie, account.ID)
		}
		if !result.Success {
			message := firstNonemptyAPI(result.Error, "获取数据失败")
			// 数据罗盘是鱼小铺卖家中心功能，普通个人账号无权访问（与原版 Python 项目一致，无个人版替代接口）。
			if strings.Contains(result.Error, "FAIL_BIZ_IDLE_USER_UNAUTHORIZED") {
				message = "该账号未开通鱼小铺，无法使用卖家数据罗盘（数据罗盘为鱼小铺卖家中心功能）"
			}
			write(w, 200, response{Success: false, Message: message})
			return
		}
		dataResult, _ := result.Response["data"].(map[string]any)
		ok(w, "获取成功", dataResult)
	}
}
