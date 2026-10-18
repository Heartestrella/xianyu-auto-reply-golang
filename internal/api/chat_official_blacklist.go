package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) chatOfficialBlacklistQuery(w http.ResponseWriter, r *http.Request) {
	s.chatOfficialBlacklist(w, r, "query")
}

func (s *Server) chatOfficialBlacklistChange(w http.ResponseWriter, r *http.Request) {
	action := chi.URLParam(r, "action")
	if action != "add" && action != "remove" {
		write(w, http.StatusOK, response{Success: false, Message: "无效操作"})
		return
	}
	s.chatOfficialBlacklist(w, r, action)
}

func (s *Server) chatOfficialBlacklist(w http.ResponseWriter, r *http.Request, action string) {
	u := current(r)
	accountID, cid := chi.URLParam(r, "account_id"), chi.URLParam(r, "cid")
	if strings.TrimSpace(cid) == "" {
		write(w, http.StatusOK, response{Success: false, Message: "会话ID不能为空"})
		return
	}
	var account platformAccount
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE account_id=? AND (owner_id=? OR ?='ADMIN')`, accountID, u.ID, strings.ToUpper(u.Role)).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass)
	if err != nil || strings.TrimSpace(account.Cookie) == "" {
		write(w, http.StatusOK, response{Success: false, Message: "账号不存在或Cookie为空"})
		return
	}
	versions := map[string]string{"query": "1.0", "add": "2.0", "remove": "1.0"}
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{
		API: "mtop.taobao.idlemessage.pc.blacklist." + action, Version: versions[action], Cookie: account.Cookie,
		Proxy: accountProxy(account), Data: map[string]any{"sessionId": cid},
	})
	if res.Cookie != "" && res.Cookie != account.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Cookie, account.ID)
	}
	if !res.Success {
		prefix := "黑名单操作失败"
		if action == "query" {
			prefix = "查询黑名单状态失败"
		}
		write(w, http.StatusOK, response{Success: false, Message: fmt.Sprintf("%s: %s", prefix, res.Error)})
		return
	}
	data, _ := res.Response["data"].(map[string]any)
	if action == "query" {
		blocked := data["isInBlack"] == true || strings.EqualFold(fmt.Sprint(data["isInBlack"]), "true") || fmt.Sprint(data["isInBlack"]) == "1"
		ok(w, "", map[string]any{"blocked": blocked})
		return
	}
	blocked := action == "add"
	message := "已解除闲鱼官方黑名单"
	if blocked {
		message = "已加入闲鱼官方黑名单"
	}
	ok(w, message, map[string]any{"blocked": blocked})
}
