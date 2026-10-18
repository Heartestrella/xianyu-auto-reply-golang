package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func externalWrite(w http.ResponseWriter, success bool, code int, message string, data any) {
	write(w, http.StatusOK, map[string]any{"success": success, "code": code, "message": message, "data": data})
}
func externalSecret(raw string) (string, string) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", "秘钥不能为空"
	}
	if len(key) > 128 {
		return "", "秘钥长度不能超过128位"
	}
	return key, ""
}
func (s *Server) externalOwner(r *http.Request, key string) (int64, string, string) {
	key, msg := externalSecret(key)
	if msg != "" {
		return 0, "40001", msg
	}
	var id int64
	var status string
	e := s.db.QueryRowContext(r.Context(), `SELECT id,status FROM xy_users WHERE secret_key=? LIMIT 1`, key).Scan(&id, &status)
	if e != nil {
		return 0, "40001", "秘钥不存在"
	}
	if strings.EqualFold(status, "DELETED") {
		return 0, "40001", "秘钥对应的用户不可用"
	}
	return id, "", ""
}

func (s *Server) externalAccounts(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SecretKey string `json:"secret_key"`
	}
	if decode(r, &p) != nil {
		externalWrite(w, false, 40001, "秘钥不能为空", nil)
		return
	}
	owner, code, msg := s.externalOwner(r, p.SecretKey)
	if code != "" {
		n, _ := strconv.Atoi(code)
		externalWrite(w, false, n, msg, nil)
		return
	}
	rows, e := s.db.QueryContext(r.Context(), `SELECT account_id,COALESCE(remark,''),status,COALESCE(disable_reason,'') FROM xy_accounts WHERE owner_id=? ORDER BY account_id`, owner)
	if e != nil {
		externalWrite(w, false, 50001, "账号信息查询失败，请稍后重试", nil)
		return
	}
	defer rows.Close()
	accounts := []map[string]any{}
	enabled := 0
	for rows.Next() {
		var id, remark, status, reason string
		_ = rows.Scan(&id, &remark, &status, &reason)
		active := strings.EqualFold(strings.TrimSpace(status), "active")
		if active {
			enabled++
		}
		accounts = append(accounts, map[string]any{"account_id": id, "remark": strings.TrimSpace(remark), "enabled": active, "status": strings.TrimSpace(status), "status_name": map[bool]string{true: "启用", false: "禁用"}[active], "disable_reason": strings.TrimSpace(reason)})
	}
	externalWrite(w, true, 200, "查询成功", map[string]any{"accounts": accounts, "total": len(accounts), "enabled_total": enabled, "disabled_total": len(accounts) - enabled})
}

func (s *Server) externalAccountCapability(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SecretKey string `json:"secret_key"`
		AccountID string `json:"account_id"`
	}
	if decode(r, &p) != nil {
		externalWrite(w, false, 40008, "请求参数格式不正确", nil)
		return
	}
	owner, code, msg := s.externalOwner(r, p.SecretKey)
	if code != "" {
		n, _ := strconv.Atoi(code)
		externalWrite(w, false, n, msg, nil)
		return
	}
	p.AccountID = strings.TrimSpace(p.AccountID)
	if p.AccountID == "" {
		externalWrite(w, false, 40002, "闲鱼账号ID不能为空", nil)
		return
	}
	var account platformAccount
	e := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, owner, p.AccountID).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass)
	if e != nil {
		externalWrite(w, false, 40002, "闲鱼账号不存在或不属于该秘钥用户", nil)
		return
	}
	backend := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.idle.pc.backend.idleitem.preget", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"publishScene": "pcBackendPublish"}, ExtraParams: map[string]string{"idle_site_biz_code": "COMMONPRO", "spm_cnt": "a21107h.42826273.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	latest := updateCapabilityCookie(r, s, account, backend.Cookie)
	if backend.Success {
		if capability, recognized := parsePublishCapability(backend.Response); recognized && capability["is_fish_shop"] == true {
			capability["account_id"] = account.AccountID
			externalWrite(w, true, 200, "账号发布能力获取成功", capability)
			return
		}
	}
	unreliable := backend.Error == "" || containsAny(strings.ToUpper(backend.Error), "FAIL_SYS_", "RGV587", "VALIDATE", "CAPTCHA", "PUNISH", "FLOW_LIMIT")
	personal := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.idle.pc.idleitem.preget", Version: "1.0", Cookie: latest, Proxy: accountProxy(account), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/publish", Data: map[string]any{}, ExtraParams: map[string]string{"spm_cnt": "a21ybx.publish.0.0"}})
	updateCapabilityCookie(r, s, account, personal.Cookie)
	if !personal.Success {
		externalWrite(w, false, 50201, "账号发布能力检测失败："+firstNonemptyAPI(personal.Error, "闲鱼接口调用失败"), nil)
		return
	}
	capability, recognized := parsePublishCapability(personal.Response)
	if !recognized || (capability["is_fish_shop"] != true && unreliable) {
		externalWrite(w, false, 50201, "账号发布能力检测失败，请稍后重试", nil)
		return
	}
	capability["account_id"] = account.AccountID
	externalWrite(w, true, 200, "账号发布能力获取成功", capability)
}

func (s *Server) externalCookieSync(w http.ResponseWriter, r *http.Request) {
	var p struct {
		AccountID string `json:"account_id"`
		Cookies   string `json:"cookies"`
		SecretKey string `json:"secret_key"`
	}
	if decode(r, &p) != nil {
		externalWrite(w, false, 40008, "参数不完整", nil)
		return
	}
	p.AccountID = strings.TrimSpace(p.AccountID)
	p.Cookies = strings.TrimSpace(p.Cookies)
	p.SecretKey = strings.TrimSpace(p.SecretKey)
	if p.AccountID == "" || p.Cookies == "" || p.SecretKey == "" {
		externalWrite(w, false, 40008, "参数不完整", nil)
		return
	}
	if len(p.Cookies) > 16384 {
		externalWrite(w, false, 40007, "Cookie 长度不能超过16384位", nil)
		return
	}
	var accountOwner int64
	var expectedUNB string
	if s.db.QueryRowContext(r.Context(), `SELECT owner_id,COALESCE(unb,'') FROM xy_accounts WHERE account_id=?`, p.AccountID).Scan(&accountOwner, &expectedUNB) != nil {
		externalWrite(w, false, 40002, "账号不存在", nil)
		return
	}
	var keyOwner int64
	var role, status string
	if s.db.QueryRowContext(r.Context(), `SELECT id,role,status FROM xy_users WHERE secret_key=?`, p.SecretKey).Scan(&keyOwner, &role, &status) != nil {
		externalWrite(w, false, 40001, "分销秘钥无效", nil)
		return
	}
	if strings.EqualFold(status, "DELETED") {
		externalWrite(w, false, 40001, "分销秘钥对应的用户不可用", nil)
		return
	}
	if role != "ADMIN" && keyOwner != accountOwner {
		externalWrite(w, false, 40002, "分销秘钥与账号不匹配", nil)
		return
	}
	cookies := map[string]string{}
	for _, part := range strings.Split(p.Cookies, ";") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) == 2 && pair[0] != "" {
			cookies[pair[0]] = pair[1]
		}
	}
	unb := strings.TrimSpace(cookies["unb"])
	if len(cookies) == 0 || unb == "" {
		externalWrite(w, false, 40007, "Cookie 格式不正确或缺少登录态", nil)
		return
	}
	if expectedUNB != "" && unb != expectedUNB {
		externalWrite(w, false, 40007, "Cookie 与账号不匹配（unb 不一致）", nil)
		return
	}
	_, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,unb=COALESCE(NULLIF(unb,''),?),last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE account_id=?`, p.Cookies, unb, p.AccountID)
	if e != nil {
		externalWrite(w, false, 50001, "Cookie 更新失败，请稍后重试", nil)
		return
	}
	externalWrite(w, true, 200, "Cookie 已更新", nil)
}

type externalOrdersRequest struct {
	SecretKey string   `json:"secret_key"`
	ItemIDs   []string `json:"item_ids"`
	ItemID    string   `json:"item_id"`
	OrderNo   string   `json:"order_no"`
	Page      int      `json:"page"`
	PageSize  int      `json:"page_size"`
}

func externalPaging(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}
func normalizeExternalIDs(values []string) ([]string, string) {
	ids := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			id := strings.TrimSpace(part)
			if len(id) > 64 {
				return nil, "商品ID长度不能超过64位"
			}
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > 100 {
		return nil, "商品ID数量不能超过100个"
	}
	return ids, ""
}
func (s *Server) queryExternalOrders(w http.ResponseWriter, r *http.Request, p externalOrdersRequest) {
	owner, code, msg := s.externalOwner(r, p.SecretKey)
	if code != "" {
		n, _ := strconv.Atoi(code)
		externalWrite(w, false, n, msg, nil)
		return
	}
	ids, msg := normalizeExternalIDs(append(p.ItemIDs, p.ItemID))
	if msg != "" {
		externalWrite(w, false, 40008, msg, nil)
		return
	}
	p.Page, p.PageSize = externalPaging(p.Page, p.PageSize)
	where := "owner_id=?"
	args := []any{owner}
	if len(ids) > 0 {
		where += " AND item_id IN (" + placeholders(len(ids)) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if order := strings.TrimSpace(p.OrderNo); order != "" {
		where += " AND order_no=?"
		args = append(args, order)
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_orders WHERE `+where, args...).Scan(&total)
	args = append(args, p.PageSize, (p.Page-1)*p.PageSize)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,order_no,COALESCE(item_id,''),COALESCE(buyer_id,''),status,COALESCE(account_id,''),COALESCE(delivery_method,''),data,created_at,updated_at FROM xy_orders WHERE `+where+` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, args...)
	if e != nil {
		externalWrite(w, false, 50001, "订单信息查询失败，请稍后重试", nil)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var order, item, buyer, status, account, delivery, raw, created, updated string
		_ = rows.Scan(&id, &order, &item, &buyer, &status, &account, &delivery, &raw, &created, &updated)
		m := map[string]any{"id": fmt.Sprint(id), "order_no": order, "order_id": order, "item_id": item, "status": strings.ToLower(status), "buyer_id": buyer, "account_id": account, "cookie_id": account, "delivery_method": delivery, "created_at": created, "updated_at": updated, "currency": "CNY"}
		mergeJSON(m, raw)
		items = append(items, m)
	}
	data := map[string]any{"list": items, "orders": items, "total": total, "page": p.Page, "page_size": p.PageSize, "total_pages": pages(total, p.PageSize)}
	externalWrite(w, true, 200, "订单查询成功", data)
}
func (s *Server) externalOrdersPost(w http.ResponseWriter, r *http.Request) {
	var p externalOrdersRequest
	if decode(r, &p) != nil {
		externalWrite(w, false, 40008, "请求参数不能为空", nil)
		return
	}
	s.queryExternalOrders(w, r, p)
}
func (s *Server) externalOrdersGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	s.queryExternalOrders(w, r, externalOrdersRequest{SecretKey: q.Get("secret_key"), ItemIDs: append(q["item_id"], q["item_ids"]...), OrderNo: q.Get("order_no"), Page: p, PageSize: size})
}

type externalLogsRequest struct {
	SecretKey   string `json:"secret_key"`
	ItemID      string `json:"item_id"`
	MessageType string `json:"message_type"`
	Page        int    `json:"page"`
	PageSize    int    `json:"page_size"`
}

func (s *Server) queryExternalLogs(w http.ResponseWriter, r *http.Request, p externalLogsRequest) {
	owner, code, msg := s.externalOwner(r, p.SecretKey)
	if code != "" {
		n, _ := strconv.Atoi(code)
		externalWrite(w, false, n, msg, nil)
		return
	}
	p.ItemID = strings.TrimSpace(p.ItemID)
	if p.ItemID == "" {
		externalWrite(w, false, 40008, "商品ID不能为空", nil)
		return
	}
	if len(p.ItemID) > 64 {
		externalWrite(w, false, 40008, "商品ID长度不能超过64位", nil)
		return
	}
	if p.MessageType == "" {
		p.MessageType = "auto_reply"
	}
	if p.MessageType != "auto_reply" && p.MessageType != "auto_delivery" {
		externalWrite(w, false, 40008, "请求参数格式不正确", nil)
		return
	}
	p.Page, p.PageSize = externalPaging(p.Page, p.PageSize)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM event_logs WHERE owner_id=? AND kind=? AND json_extract(data,'$.item_id')=?`, owner, p.MessageType, p.ItemID).Scan(&total)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,COALESCE(account_id,''),message,data,created_at FROM event_logs WHERE owner_id=? AND kind=? AND json_extract(data,'$.item_id')=? ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, owner, p.MessageType, p.ItemID, p.PageSize, (p.Page-1)*p.PageSize)
	if e != nil {
		externalWrite(w, false, 50001, "消息日志查询失败，请稍后重试", nil)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var account, message, raw, created string
		_ = rows.Scan(&id, &account, &message, &raw, &created)
		m := map[string]any{"id": id, "account_id": account, "source_message": message, "created_at": created}
		mergeJSON(m, raw)
		delete(m, "owner_id")
		items = append(items, m)
	}
	externalWrite(w, true, 200, "消息日志查询成功", map[string]any{"list": items, "logs": items, "total": total, "page": p.Page, "page_size": p.PageSize, "total_pages": pages(total, p.PageSize)})
}
func (s *Server) externalMessageLogsPost(w http.ResponseWriter, r *http.Request) {
	var p externalLogsRequest
	if decode(r, &p) != nil {
		externalWrite(w, false, 40008, "请求参数不能为空", nil)
		return
	}
	s.queryExternalLogs(w, r, p)
}
func (s *Server) externalMessageLogsGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	s.queryExternalLogs(w, r, externalLogsRequest{SecretKey: q.Get("secret_key"), ItemID: q.Get("item_id"), MessageType: q.Get("message_type"), Page: p, PageSize: size})
}
