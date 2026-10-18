package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) publishAccountCapability(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "account_id")
	var account platformAccount
	query := `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE account_id=?`
	args := []any{accountID}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if err := s.db.QueryRowContext(r.Context(), query, args...).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass); err != nil {
		write(w, 200, response{Success: false, Message: "发布账号不存在或无权使用"})
		return
	}
	if strings.TrimSpace(account.Cookie) == "" {
		write(w, 200, response{Success: false, Message: "发布账号缺少Cookie，请重新登录账号"})
		return
	}
	backend := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.idle.pc.backend.idleitem.preget", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"publishScene": "pcBackendPublish"}, ExtraParams: map[string]string{"idle_site_biz_code": "COMMONPRO", "spm_cnt": "a21107h.42826273.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	latestCookie := updateCapabilityCookie(r, s, account, backend.Cookie)
	if backend.Success {
		if capability, recognized := parsePublishCapability(backend.Response); recognized && capability["is_fish_shop"] == true {
			s.writePublishCapability(w, accountID, capability)
			return
		}
	}
	backendUnreliable := backend.Error == "" || containsAny(strings.ToUpper(backend.Error), "FAIL_SYS_", "RGV587", "VALIDATE", "CAPTCHA", "PUNISH", "FLOW_LIMIT")
	personal := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.idle.pc.idleitem.preget", Version: "1.0", Cookie: latestCookie, Proxy: accountProxy(account), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/publish", Data: map[string]any{}, ExtraParams: map[string]string{"spm_cnt": "a21ybx.publish.0.0"}})
	updateCapabilityCookie(r, s, account, personal.Cookie)
	if !personal.Success {
		write(w, 200, response{Success: false, Message: "账号发布能力检测失败：" + firstNonemptyAPI(personal.Error, "闲鱼接口调用失败")})
		return
	}
	capability, recognized := parsePublishCapability(personal.Response)
	if !recognized {
		write(w, 200, response{Success: false, Message: "账号发布能力检测失败：无法识别账号是否开通鱼小铺"})
		return
	}
	if capability["is_fish_shop"] != true && backendUnreliable {
		write(w, 200, response{Success: false, Message: "账号发布能力检测不可信：鱼小铺后台接口未给出可靠结论，请稍后重试"})
		return
	}
	s.writePublishCapability(w, accountID, capability)
}

func updateCapabilityCookie(r *http.Request, s *Server, account platformAccount, next string) string {
	if next == "" {
		return account.Cookie
	}
	if next != account.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, next, account.ID)
	}
	return next
}

func parsePublishCapability(response map[string]any) (map[string]any, bool) {
	data, _ := response["data"].(map[string]any)
	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}
	commission, _ := data["commissionConfig"].(map[string]any)
	tipURL := fmt.Sprint(commission["tipUrl"])
	title := fmt.Sprint(commission["commissionTitle"])
	defaultTitle := fmt.Sprint(commission["defaultCommissionTitle"])
	isShop := strings.Contains(tipURL, "fish-shop-service-fee-rule") || strings.Contains(title, "鱼小铺") || strings.Contains(defaultTitle, "鱼小铺")
	isPersonal := strings.Contains(tipURL, "fish-seller-service-fee-rule") || defaultTitle == "基础软件服务费"
	support, supportKnown := data["supportSkuOrInventory"].(bool)
	if !isShop && !isPersonal && !supportKnown {
		return nil, false
	}
	if !isShop && !isPersonal {
		isShop = support
	}
	config := map[string]any{"title": fmt.Sprint(commission["commissionTitle"]), "default_title": defaultTitle, "tips": fmt.Sprint(commission["commissionTips"]), "percent": fmt.Sprint(commission["percent"]), "max_commission": fmt.Sprint(commission["maxCommission"]), "tip_url": tipURL}
	return map[string]any{"is_fish_shop": isShop, "support_sku_or_inventory": map[bool]bool{true: support, false: isShop}[supportKnown], "commission_config": config}, true
}

func (s *Server) writePublishCapability(w http.ResponseWriter, accountID string, capability map[string]any) {
	capability["account_id"] = accountID
	ok(w, "账号发布能力检测成功", capability)
}
