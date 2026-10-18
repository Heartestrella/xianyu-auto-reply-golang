package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type compassSearchPayload struct {
	CookieID    string `json:"cookie_id"`
	AccountID   any    `json:"account_id"`
	Keyword     string `json:"keyword"`
	StartPage   int    `json:"start_page"`
	Pages       int    `json:"pages"`
	PageSize    int    `json:"page_size"`
	FetchDetail bool   `json:"fetch_detail"`
	DetailLimit int    `json:"detail_limit"`
}

// goofishCompassSearch is the Go-native replacement for the former
// Playwright-backed compass endpoint. It uses the authenticated MTOP search
// protocol directly, so no browser container is required.
func (s *Server) goofishCompassSearch(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p compassSearchPayload
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	p.Keyword = strings.TrimSpace(p.Keyword)
	if p.Keyword == "" || len([]rune(p.Keyword)) > 100 {
		fail(w, 400, "关键词长度必须在1-100之间")
		return
	}
	if p.StartPage == 0 {
		p.StartPage = 1
	}
	if p.Pages == 0 {
		p.Pages = 1
	}
	if p.PageSize == 0 {
		p.PageSize = 20
	}
	if p.StartPage < 1 || p.StartPage > 50 || p.Pages < 1 || p.Pages > 10 || p.PageSize < 1 || p.PageSize > 50 {
		fail(w, 400, "分页参数超出允许范围")
		return
	}
	accountKey := strings.TrimSpace(p.CookieID)
	if accountKey == "" && p.AccountID != nil {
		accountKey = strings.TrimSpace(fmt.Sprint(p.AccountID))
	}
	var a platformAccount
	query := `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND status='active' AND (account_id=? OR CAST(id AS TEXT)=?) LIMIT 1`
	if err := s.db.QueryRowContext(r.Context(), query, u.ID, accountKey, accountKey).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass); err != nil {
		write(w, 200, response{Success: false, Message: "账号不存在"})
		return
	}
	items := []map[string]any{}
	currentCookie := a.Cookie
	for pageNo := p.StartPage; pageNo < p.StartPage+p.Pages; pageNo++ {
		data := map[string]any{"pageNumber": pageNo, "keyword": p.Keyword, "fromFilter": true, "rowsPerPage": p.PageSize, "sortValue": "", "sortField": "", "customDistance": "", "gps": "", "propValueStr": map[string]any{"searchFilter": "quickFilter:filterPersonal;"}, "customGps": "", "searchReqFromPage": "pcSearch", "extraFilterValue": "{}", "userPositionJson": "{}"}
		result := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idlemtopsearch.pc.search", Version: "1.0", Cookie: currentCookie, Proxy: accountProxy(a), Data: data, ExtraParams: map[string]string{"spm_cnt": "a21ybx.search.0.0", "spm_pre": "a21ybx.home.searchInput.0"}})
		if result.Cookie != "" {
			currentCookie = result.Cookie
		}
		if !result.Success {
			persistPlatformCookie(s, r, a, currentCookie)
			write(w, 200, response{Success: false, Message: result.Error, Data: map[string]any{"items": items, "total": len(items), "error": result.Error}})
			return
		}
		pageItems := crawlerResultItems(result.Response)
		if len(pageItems) > p.PageSize {
			pageItems = pageItems[:p.PageSize]
		}
		items = append(items, pageItems...)
		if len(pageItems) < p.PageSize {
			break
		}
	}
	persistPlatformCookie(s, r, a, currentCookie)
	ok(w, "搜索成功", map[string]any{"success": true, "items": items, "total": len(items), "total_available": len(items)})
}

type itemSearchPayload struct {
	Keyword  string `json:"keyword"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

func (s *Server) searchItemsLegacy(w http.ResponseWriter, r *http.Request) {
	p, items, err := s.executeItemSearch(r)
	if err != "" {
		write(w, 200, map[string]any{"success": false, "data": []any{}, "error": err})
		return
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": len(items), "page": p.Page, "page_size": p.PageSize})
}

func (s *Server) searchItemsAPI(w http.ResponseWriter, r *http.Request) {
	p, items, err := s.executeItemSearch(r)
	if err != "" {
		write(w, 200, response{Success: false, Message: err})
		return
	}
	ok(w, "搜索成功", map[string]any{"items": items, "total": len(items), "page": p.Page, "page_size": p.PageSize})
}

func (s *Server) executeItemSearch(r *http.Request) (itemSearchPayload, []map[string]any, string) {
	u := current(r)
	var p itemSearchPayload
	if decode(r, &p) != nil {
		return p, nil, "请求格式错误"
	}
	p.Keyword = strings.TrimSpace(p.Keyword)
	if p.Keyword == "" || len([]rune(p.Keyword)) > 100 {
		return p, nil, "关键词长度必须在1-100之间"
	}
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 {
		p.PageSize = 20
	}
	if p.PageSize > 100 {
		return p, nil, "page_size必须在1-100之间"
	}
	var account platformAccount
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND status='active' ORDER BY id LIMIT 1`, u.ID).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass)
	if err == sql.ErrNoRows {
		return p, nil, "未找到有效的cookies账户"
	}
	if err != nil {
		return p, nil, "读取搜索账号失败: " + err.Error()
	}
	data := map[string]any{"pageNumber": p.Page, "keyword": p.Keyword, "fromFilter": true, "rowsPerPage": p.PageSize, "sortValue": "", "sortField": "", "customDistance": "", "gps": "", "propValueStr": map[string]any{"searchFilter": "quickFilter:filterPersonal;"}, "customGps": "", "searchReqFromPage": "pcSearch", "extraFilterValue": "{}", "userPositionJson": "{}"}
	result := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idlemtopsearch.pc.search", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Data: data, ExtraParams: map[string]string{"spm_cnt": "a21ybx.search.0.0", "spm_pre": "a21ybx.home.searchInput.0"}})
	if result.Cookie != "" && result.Cookie != account.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, result.Cookie, account.ID)
	}
	if !result.Success {
		message := result.Error
		if message == "" {
			message = "闲鱼商品搜索失败"
		}
		return p, nil, message
	}
	items := crawlerResultItems(result.Response)
	if len(items) > p.PageSize {
		items = items[:p.PageSize]
	}
	return p, items, ""
}
