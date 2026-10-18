package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) chatAvatars(w http.ResponseWriter, r *http.Request) {
	account, found := s.ownedChatAccount(r, chi.URLParam(r, "account_id"))
	if !found {
		write(w, 200, response{Success: false, Message: "账号不存在或Cookie为空"})
		return
	}
	var p struct {
		Queries []struct {
			UserID string `json:"userId"`
			CID    string `json:"cid"`
		} `json:"queries"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	infos := map[string]any{}
	for _, q := range p.Queries {
		if strings.TrimSpace(q.CID) == "" || strings.TrimSpace(q.UserID) == "" {
			continue
		}
		if info, ok := s.queryChatUser(r, account, q.CID, false); ok {
			infos[q.UserID] = info
		}
	}
	ok(w, "", infos)
}

func (s *Server) chatAccountProfile(w http.ResponseWriter, r *http.Request) {
	account, found := s.ownedChatAccount(r, chi.URLParam(r, "account_id"))
	if !found {
		write(w, 200, response{Success: false, Message: "账号不存在或Cookie为空"})
		return
	}
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	if cid == "" {
		write(w, 200, response{Success: false, Message: "会话ID不能为空"})
		return
	}
	info, _ := s.queryChatUser(r, account, cid, true)
	if info == nil {
		info = map[string]any{}
	}
	if nick := strings.TrimSpace(toString(info["nick"])); nick != "" {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET display_name=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, nick, account.ID)
	}
	ok(w, "", info)
}

func (s *Server) ownedChatAccount(r *http.Request, id string) (platformAccount, bool) {
	u := current(r)
	var a platformAccount
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE account_id=? AND (owner_id=? OR ?='ADMIN')`, id, u.ID, strings.ToUpper(u.Role)).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
	return a, err == nil && strings.TrimSpace(a.Cookie) != ""
}

func (s *Server) queryChatUser(r *http.Request, a platformAccount, cid string, owner bool) (map[string]any, bool) {
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idlemessage.pc.user.query", Version: "4.0", Cookie: a.Cookie, Proxy: accountProxy(a), Data: map[string]any{"type": 0, "sessionType": 1, "sessionId": cid, "isOwner": owner}, ExtraParams: map[string]string{"spm_cnt": "a21ybx.im.0.0", "spm_pre": "a21ybx.home.sidebar.2.4c053da6MpVe1m"}})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Cookie, a.ID)
	}
	if !res.Success {
		return nil, false
	}
	data, _ := res.Response["data"].(map[string]any)
	user, _ := data["userInfo"].(map[string]any)
	if user == nil {
		return nil, false
	}
	return map[string]any{"avatar": firstText(user["logo"], user["avatar"]), "nick": firstText(user["nick"], user["nickname"])}, true
}
