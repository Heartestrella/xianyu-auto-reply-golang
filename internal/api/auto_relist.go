package api

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

var relistStatusText = map[string]string{"active": "监听中", "disabled": "已关闭", "pending": "等待执行", "retry": "稍后重试", "success": "续售成功", "failed": "执行失败", "unknown": "结果未知，待对账", "migration_retry": "关联迁移重试"}

func (s *Server) ownedMaterial(r *http.Request, id int64) (int64, bool) {
	u := current(r)
	var owner int64
	err := s.db.QueryRowContext(r.Context(), `SELECT owner_id FROM product_materials WHERE id=? AND (owner_id=? OR ?='ADMIN')`, id, u.ID, strings.ToUpper(u.Role)).Scan(&owner)
	return owner, err == nil
}

func (s *Server) autoRelistGet(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "material_id"), 10, 64)
	owner, found := s.ownedMaterial(r, id)
	if !found {
		write(w, 200, response{Success: false, Message: "素材不存在或无权访问"})
		return
	}
	row, ok := s.relistRule(r, id, owner)
	if !ok {
		okResponse(w, "查询成功", nil)
		return
	}
	row["can_configure"] = owner == current(r).ID
	okResponse(w, "查询成功", row)
}

func (s *Server) autoRelistSave(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "material_id"), 10, 64)
	owner, found := s.ownedMaterial(r, id)
	if !found || owner != current(r).ID {
		write(w, 200, response{Success: false, Message: "素材不存在或无权访问"})
		return
	}
	var p struct {
		AccountID       string `json:"account_id"`
		CurrentItemID   string `json:"current_item_id"`
		CardID          int64  `json:"card_id"`
		Enabled         bool   `json:"enabled"`
		DelaySeconds    int    `json:"delay_seconds"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if p.DelaySeconds < 1 {
		p.DelaySeconds = 60
	}
	if p.Enabled && (strings.TrimSpace(p.AccountID) == "" || strings.TrimSpace(p.CurrentItemID) == "" || p.CardID < 1) {
		write(w, 200, response{Success: false, Message: "启用自动续售时账号、商品和卡券不能为空"})
		return
	}
	var version int64
	err := s.db.QueryRowContext(r.Context(), `SELECT version FROM xy_auto_relist_rules WHERE material_id=? AND user_id=?`, id, owner).Scan(&version)
	status := "disabled"
	if p.Enabled {
		status = "active"
	}
	if err == sql.ErrNoRows {
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO xy_auto_relist_rules(user_id,material_id,account_id,current_item_id,card_id,enabled,delay_seconds,status) VALUES(?,?,?,?,?,?,?,?)`, owner, id, p.AccountID, p.CurrentItemID, p.CardID, p.Enabled, p.DelaySeconds, status)
	} else if err == nil {
		if p.ExpectedVersion != nil && *p.ExpectedVersion != version {
			write(w, 200, response{Success: false, Message: "自动续售配置已被其他操作更新，请刷新后重试"})
			return
		}
		_, err = s.db.ExecContext(r.Context(), `UPDATE xy_auto_relist_rules SET account_id=?,current_item_id=?,card_id=?,enabled=?,delay_seconds=?,status=?,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE material_id=? AND user_id=?`, p.AccountID, p.CurrentItemID, p.CardID, p.Enabled, p.DelaySeconds, status, id, owner)
	}
	if err != nil {
		fail(w, 500, "保存自动续售配置失败")
		return
	}
	row, _ := s.relistRule(r, id, owner)
	msg := "自动续售已关闭"
	if p.Enabled {
		msg = "自动续售已启用"
	}
	okResponse(w, msg, row)
}

func (s *Server) relistRule(r *http.Request, materialID, owner int64) (map[string]any, bool) {
	var id, card, version, retries int64
	var account, item, status, created, updated string
	var enabled int
	var delay int
	err := s.db.QueryRowContext(r.Context(), `SELECT id,account_id,current_item_id,card_id,enabled,delay_seconds,status,version,retry_count,created_at,updated_at FROM xy_auto_relist_rules WHERE material_id=? AND user_id=?`, materialID, owner).Scan(&id, &account, &item, &card, &enabled, &delay, &status, &version, &retries, &created, &updated)
	if err != nil {
		return nil, false
	}
	return map[string]any{"id": id, "material_id": materialID, "owner_id": owner, "account_id": account, "current_item_id": item, "card_id": card, "enabled": enabled != 0, "delay_seconds": delay, "status": status, "status_text": relistStatusText[status], "version": version, "retry_count": retries, "created_at": created, "updated_at": updated, "can_configure": true, "publish_state": "not_started", "result_unknown": false}, true
}

func (s *Server) autoRelistEvents(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "material_id"), 10, 64)
	owner, found := s.ownedMaterial(r, id)
	if !found {
		write(w, 200, response{Success: false, Message: "素材不存在或无权访问"})
		return
	}
	s.writeRelistEvents(w, r, owner, id)
}
func (s *Server) autoRelistAllEvents(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	owner := u.ID
	if strings.ToUpper(u.Role) == "ADMIN" {
		owner = 0
	}
	s.writeRelistEvents(w, r, owner, 0)
}
func (s *Server) writeRelistEvents(w http.ResponseWriter, r *http.Request, owner, material int64) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 || size > 100 {
		size = 20
	}
	where := " WHERE 1=1"
	args := []any{}
	if owner > 0 {
		where += " AND user_id=?"
		args = append(args, owner)
	}
	if material > 0 {
		where += " AND material_id=?"
		args = append(args, material)
	}
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		where += " AND status=?"
		args = append(args, st)
	}
	var total int
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM xy_auto_relist_events"+where, args...).Scan(&total)
	args = append(args, size, (page-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,order_no,old_item_id,COALESCE(new_item_id,publish_item_id,''),status,publish_state,result_unknown,attempt_count,COALESCE(error_message,''),created_at,updated_at FROM xy_auto_relist_events`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, "查询自动续售事件失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var order, old, newID, status, pub, errmsg, created, updated string
		var unknown, attempt int
		rows.Scan(&id, &order, &old, &newID, &status, &pub, &unknown, &attempt, &errmsg, &created, &updated)
		items = append(items, map[string]any{"id": id, "order_no": order, "old_item_id": old, "new_item_id": newID, "status": status, "status_text": relistStatusText[status], "publish_state": pub, "result_unknown": unknown != 0, "attempt_count": attempt, "error_message": errmsg, "created_at": created, "updated_at": updated})
	}
	okResponse(w, "查询成功", map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

func (s *Server) autoRelistReconcile(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Outcome   string `json:"outcome"`
		NewItemID string `json:"new_item_id"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if p.Outcome == "" {
		p.Outcome = "published"
	}
	status := "failed"
	pub := "not_published"
	unknown := 0
	if p.Outcome == "published" {
		if strings.TrimSpace(p.NewItemID) == "" {
			write(w, 200, response{Success: false, Message: "确认已发布时必须提供新商品ID"})
			return
		}
		status = "success"
		pub = "published"
	}
	s.updateRelistEvent(w, r, status, pub, unknown, p.NewItemID, "人工对账完成")
}
func (s *Server) autoRelistMarkFailed(w http.ResponseWriter, r *http.Request) {
	s.updateRelistEvent(w, r, "failed", "not_published", 0, "", "已由用户标记失败")
}
func (s *Server) updateRelistEvent(w http.ResponseWriter, r *http.Request, status, pub string, unknown int, newID, msg string) {
	material, _ := strconv.ParseInt(chi.URLParam(r, "material_id"), 10, 64)
	event, _ := strconv.ParseInt(chi.URLParam(r, "event_id"), 10, 64)
	owner, found := s.ownedMaterial(r, material)
	if !found || owner != current(r).ID {
		write(w, 200, response{Success: false, Message: "素材不存在或无权访问"})
		return
	}
	res, err := s.db.ExecContext(r.Context(), `UPDATE xy_auto_relist_events SET status=?,publish_state=?,result_unknown=?,new_item_id=CASE WHEN ?<>'' THEN ? ELSE new_item_id END,error_message=?,claim_token=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND material_id=? AND user_id=? AND result_unknown=1`, status, pub, unknown, newID, newID, msg, event, material, owner)
	if err != nil {
		fail(w, 500, "更新自动续售事件失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{Success: false, Message: "事件不存在或当前状态不允许操作"})
		return
	}
	okResponse(w, msg, map[string]any{"id": event, "status": status, "publish_state": pub, "result_unknown": false, "new_item_id": newID})
}

func okResponse(w http.ResponseWriter, msg string, data any) {
	write(w, 200, response{Success: true, Message: msg, Data: data})
}
