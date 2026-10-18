package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"
)

const personalBlacklistKind = "personal_blacklist"

func (s *Server) registerBlacklistRoutes(r chi.Router) {
	r.Get("/blacklist/personal", s.personalBlacklistList)
	r.Post("/blacklist/personal", s.personalBlacklistCreate)
	r.Post("/blacklist/personal/batch-delete", s.personalBlacklistBatchDelete)
	r.Get("/blacklist/personal/export", s.personalBlacklistExport)
	r.Post("/blacklist/personal/import", s.personalBlacklistImport)
	r.Delete("/blacklist/personal/{record_id}", s.personalBlacklistDelete)
	r.Patch("/blacklist/personal/{record_id}/toggle", s.personalBlacklistToggle)
	r.Get("/blacklist/platform", s.platformBlacklistList)
}

func (s *Server) personalBlacklistList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	query := ` FROM resource_records WHERE owner_id=? AND kind=?`
	args := []any{u.ID, personalBlacklistKind}
	if buyer := strings.TrimSpace(r.URL.Query().Get("buyer_id")); buyer != "" {
		query += ` AND json_extract(data,'$.buyer_id') LIKE ?`
		args = append(args, "%"+buyer+"%")
	}
	if nick := strings.TrimSpace(r.URL.Query().Get("buyer_nick")); nick != "" {
		query += ` AND json_extract(data,'$.buyer_nick') LIKE ?`
		args = append(args, "%"+nick+"%")
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total)
	args = append(args, size, (p-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,COALESCE(account_id,''),enabled,data,created_at,updated_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var account, raw, created, updated string
		var enabled bool
		if rows.Scan(&id, &account, &enabled, &raw, &created, &updated) == nil {
			m := map[string]any{"id": id, "owner_id": u.ID, "account_id": account, "is_enabled": enabled, "created_at": created, "updated_at": updated}
			mergeJSON(m, raw)
			items = append(items, m)
		}
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": total, "page": p, "page_size": size})
}

func splitBuyerIDs(value any) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	switch v := value.(type) {
	case string:
		for _, x := range strings.Split(v, ",") {
			add(x)
		}
	case []any:
		for _, x := range v {
			add(fmt.Sprint(x))
		}
	case []string:
		for _, x := range v {
			add(x)
		}
	}
	return out
}

func (s *Server) personalBlacklistCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	ids := splitBuyerIDs(p["buyer_ids"])
	if len(ids) == 0 {
		fail(w, 400, "买家ID不能为空")
		return
	}
	account := firstString(p, "account_id")
	enabled := compatibilityEnabled(map[string]any{"enabled": p["is_enabled"]}, true)
	created := 0
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	for _, buyer := range ids {
		var exists int
		_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind=? AND COALESCE(account_id,'')=? AND json_extract(data,'$.buyer_id')=? AND COALESCE(json_extract(data,'$.item_id'),'')=?`, u.ID, personalBlacklistKind, account, buyer, firstString(p, "item_id")).Scan(&exists)
		if exists > 0 {
			continue
		}
		raw, _ := jsonMarshalMap(map[string]any{"buyer_id": buyer, "buyer_nick": p["buyer_nick"], "item_id": p["item_id"], "reason": p["reason"]})
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,?,?)`, u.ID, personalBlacklistKind, nullString(account), enabled, raw); err != nil {
			fail(w, 500, err.Error())
			return
		}
		created++
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, fmt.Sprintf("成功添加 %d 条黑名单，%d 条已存在已跳过", created, len(ids)-created), map[string]any{"count": created, "skipped": len(ids) - created})
}

func jsonMarshalMap(value map[string]any) (string, error) {
	b, e := json.Marshal(value)
	return string(b), e
}

func (s *Server) personalBlacklistDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, personalBlacklistKind, chi.URLParam(r, "record_id"))
	affected(w, res, err, "删除成功")
}
func (s *Server) personalBlacklistBatchDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择要删除的记录")
		return
	}
	args := []any{u.ID, personalBlacklistKind}
	for _, id := range p.IDs {
		args = append(args, id)
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id IN (`+placeholders(len(p.IDs))+`)`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	ok(w, fmt.Sprintf("成功删除 %d 条记录", n), map[string]any{"deleted": n})
}
func (s *Server) personalBlacklistToggle(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	enabled := compatibilityEnabled(map[string]any{"enabled": p["is_enabled"]}, false)
	res, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind=? AND id=?`, enabled, u.ID, personalBlacklistKind, chi.URLParam(r, "record_id"))
	affected(w, res, err, "操作成功")
}

func (s *Server) personalBlacklistExport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT COALESCE(account_id,''),COALESCE(json_extract(data,'$.buyer_id'),''),COALESCE(json_extract(data,'$.buyer_nick'),''),COALESCE(json_extract(data,'$.item_id'),''),COALESCE(json_extract(data,'$.reason'),''),enabled FROM resource_records WHERE owner_id=? AND kind=? ORDER BY id`, u.ID, personalBlacklistKind)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	headers := []string{"账号ID", "买家ID", "买家昵称", "商品ID", "拉黑原因", "是否启用"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	row := 2
	for rows.Next() {
		var account, buyer, nick, item, reason string
		var enabled bool
		_ = rows.Scan(&account, &buyer, &nick, &item, &reason, &enabled)
		values := []any{account, buyer, nick, item, reason, map[bool]string{true: "是", false: "否"}[enabled]}
		for i, v := range values {
			cell, _ := excelize.CoordinatesToCellName(i+1, row)
			_ = f.SetCellValue(sheet, cell, v)
		}
		row++
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="personal_blacklist_%d.xlsx"`, time.Now().Unix()))
	if err = f.Write(w); err != nil {
		s.log.Error("export blacklist", "error", err)
	}
	_ = f.Close()
}

func (s *Server) personalBlacklistImport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		fail(w, 400, "上传文件无效")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请上传Excel文件")
		return
	}
	defer file.Close()
	f, err := excelize.OpenReader(file)
	if err != nil {
		fail(w, 400, "Excel文件读取失败")
		return
	}
	defer f.Close()
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil || len(rows) == 0 {
		fail(w, 400, "Excel文件为空")
		return
	}
	cols := map[string]int{}
	for i, v := range rows[0] {
		cols[strings.TrimSpace(v)] = i
	}
	if _, ok := cols["买家ID"]; !ok {
		fail(w, 400, "Excel缺少必要的列: 买家ID")
		return
	}
	created, skipped := 0, 0
	tx, _ := s.db.BeginTx(r.Context(), nil)
	defer tx.Rollback()
	cell := func(row []string, name string) string {
		i, ok := cols[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(strings.TrimSuffix(row[i], ".0"))
	}
	for _, row := range rows[1:] {
		buyer := cell(row, "买家ID")
		if buyer == "" {
			continue
		}
		account := cell(row, "账号ID")
		item := cell(row, "商品ID")
		var exists int
		_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind=? AND COALESCE(account_id,'')=? AND json_extract(data,'$.buyer_id')=? AND COALESCE(json_extract(data,'$.item_id'),'')=?`, u.ID, personalBlacklistKind, account, buyer, item).Scan(&exists)
		if exists > 0 {
			skipped++
			continue
		}
		raw, _ := jsonMarshalMap(map[string]any{"buyer_id": buyer, "buyer_nick": cell(row, "买家昵称"), "item_id": item, "reason": cell(row, "拉黑原因")})
		_, err = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,?,?)`, u.ID, personalBlacklistKind, nullString(account), cell(row, "是否启用") != "否", raw)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		created++
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, fmt.Sprintf("导入完成，新增 %d 条，%d 条已存在已跳过", created, skipped), map[string]any{"created": created, "skipped": skipped})
}

func (s *Server) platformBlacklistList(w http.ResponseWriter, r *http.Request) {
	p, size := page(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE kind='platform_blacklist'`).Scan(&total)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,data,created_at,updated_at FROM resource_records WHERE kind='platform_blacklist' ORDER BY id DESC LIMIT ? OFFSET ?`, size, (p-1)*size)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var raw, created, updated string
		_ = rows.Scan(&id, &raw, &created, &updated)
		m := map[string]any{"id": id, "created_at": created, "updated_at": updated}
		mergeJSON(m, raw)
		items = append(items, m)
	}
	write(w, 200, map[string]any{"success": true, "data": items, "total": total, "page": p, "page_size": size})
}
