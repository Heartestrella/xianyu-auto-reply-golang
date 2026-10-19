package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) registerProductPublishRoutes(r chi.Router) {
	r.Post("/product-publish/category/recommend", s.productCategoryRecommend)
	r.Get("/product-publish/accounts/{account_id}/capability", s.publishAccountCapability)
	r.Get("/product-publish/materials", s.materialList)
	r.Post("/product-publish/materials", s.materialCreate)
	r.Post("/product-publish/materials/batch-delete", s.materialBatchDelete)
	r.Get("/product-publish/materials/{material_id}", s.materialGet)
	r.Put("/product-publish/materials/{material_id}", s.materialUpdate)
	r.Delete("/product-publish/materials/{material_id}", s.materialDelete)
	r.Get("/product-publish/logs", s.publishLogList)
	r.Delete("/product-publish/logs/clear", s.publishLogClear)
	r.Post("/product-publish/publish/single", s.publishSingle)
	r.Post("/product-publish/publish/batch", s.publishBatch)
	r.Get("/product-publish/publish/batch/{batch_id}/status", s.publishBatchStatus)
	r.Post("/product-publish/upload/images", s.uploadPublishImages)
	r.Post("/product-publish/upload/videos", s.uploadPublishVideos)
	r.Get("/product-publish/materials/{material_id}/auto-relist", s.autoRelistGet)
	r.Put("/product-publish/materials/{material_id}/auto-relist", s.autoRelistSave)
	r.Get("/product-publish/materials/{material_id}/auto-relist/events", s.autoRelistEvents)
	r.Get("/product-publish/auto-relist/events", s.autoRelistAllEvents)
	r.Post("/product-publish/materials/{material_id}/auto-relist/events/{event_id}/reconcile", s.autoRelistReconcile)
	r.Post("/product-publish/materials/{material_id}/auto-relist/events/{event_id}/mark-failed", s.autoRelistMarkFailed)
}

func (s *Server) publishSingle(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	accountID := strings.TrimSpace(fmt.Sprint(p["account_id"]))
	if accountID == "" {
		fail(w, 400, "account_id不能为空")
		return
	}
	result := s.executePublish(r.Context(), u.ID, accountID, 0, "", p)
	write(w, 200, result)
}

func (s *Server) executePublish(ctx context.Context, ownerID int64, accountID string, materialID int64, batchID string, item map[string]any) map[string]any {
	var account platformAccount
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=? AND status='active'`, ownerID, accountID).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass)
	if err != nil {
		return map[string]any{"success": false, "message": "账号不存在或未启用"}
	}
	if err = validateMaterial(item); err != nil {
		return map[string]any{"success": false, "message": err.Error()}
	}
	payload, refreshedCookie, buildErr := buildPublishPayload(ctx, item, account, nil)
	if buildErr != nil {
		return map[string]any{"success": false, "message": buildErr.Error()}
	}
	if refreshedCookie != "" {
		account.Cookie = refreshedCookie
	}
	payloadBytes, _ := json.Marshal(payload)
	request := map[string]any{"inputJson": string(payloadBytes)}
	requestJSON, _ := json.Marshal(item)
	var logID int64
	if batchID != "" {
		logResult, _ := s.db.ExecContext(ctx, `INSERT INTO publish_logs(owner_id,account_id,material_id,batch_id,status,message,request_data) VALUES(?,?,?,?,?,?,?)`, ownerID, accountID, nullableInt(materialID), batchID, "publishing", "发布中", string(requestJSON))
		if logResult != nil {
			logID, _ = logResult.LastInsertId()
		}
	}
	res := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{
		API: "mtop.idle.pc.backend.idleitem.publish", Version: "1.0", Cookie: account.Cookie,
		Proxy: accountProxy(account), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO",
		Data: request, ExtraParams: map[string]string{"idle_site_biz_code": "COMMONPRO", "spm_cnt": "a21107h.42826273.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO"},
	})
	if res.Cookie != "" && res.Cookie != account.Cookie {
		_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Cookie, account.ID)
	}
	itemID, itemURL := findPublishedItem(res.Response)
	status := "failed"
	message := res.Error
	if res.Success {
		status = "success"
		message = "商品发布成功"
	}
	responseJSON, _ := json.Marshal(res.Response)
	if logID != 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE publish_logs SET status=?,item_id=?,item_url=?,message=?,response_data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, nullString(itemID), nullString(itemURL), message, string(responseJSON), logID)
	} else {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO publish_logs(owner_id,account_id,material_id,batch_id,status,item_id,item_url,message,request_data,response_data) VALUES(?,?,?,?,?,?,?,?,?,?)`, ownerID, accountID, nullableInt(materialID), nullString(batchID), status, nullString(itemID), nullString(itemURL), message, string(requestJSON), string(responseJSON))
	}
	if !res.Success {
		if message == "" {
			message = "闲鱼接口发布失败"
		}
		return map[string]any{"success": false, "message": message, "account_invalid": res.AccountInvalid, "punish_url": res.PunishURL, "request_status_unknown": res.StatusUnknown}
	}
	return map[string]any{"success": true, "message": message, "data": map[string]any{"item_id": itemID, "item_url": itemURL, "account_id": accountID}}
}

// ExecuteScheduledPublish exposes the same platform publishing path used by the
// HTTP API to background jobs.  Keeping one implementation prevents scheduled
// relists from drifting away from the frontend/API payload contract.
func (s *Server) ExecuteScheduledPublish(ctx context.Context, ownerID, materialID int64, accountID string) (string, bool, error) {
	material, err := s.loadMaterialByOwner(ctx, ownerID, materialID)
	if err != nil {
		return "", false, fmt.Errorf("加载发布素材: %w", err)
	}
	result := s.executePublish(ctx, ownerID, accountID, materialID, "", material)
	if result["success"] != true {
		unknown, _ := result["request_status_unknown"].(bool)
		message := strings.TrimSpace(fmt.Sprint(result["message"]))
		if message == "" || message == "<nil>" {
			message = "闲鱼接口发布失败"
		}
		return "", unknown, errors.New(message)
	}
	data, _ := result["data"].(map[string]any)
	itemID := strings.TrimSpace(fmt.Sprint(data["item_id"]))
	if itemID == "" || itemID == "<nil>" {
		return "", true, errors.New("发布接口成功但未返回商品ID")
	}
	return itemID, false, nil
}

func (s *Server) publishBatch(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		AccountIDs  []string `json:"account_ids"`
		MaterialIDs []int64  `json:"material_ids"`
	}
	if decode(r, &p) != nil || len(p.AccountIDs) == 0 || len(p.MaterialIDs) == 0 {
		fail(w, 400, "请选择账号和素材")
		return
	}
	batchID := fmt.Sprintf("pub-%d-%s", time.Now().UnixMilli(), randomHex(8))
	jobs := 0
	for _, materialID := range p.MaterialIDs {
		material, err := s.loadMaterialByOwner(r.Context(), u.ID, materialID)
		if err != nil {
			continue
		}
		for _, accountID := range p.AccountIDs {
			jobs++
			go s.executePublish(context.Background(), u.ID, accountID, materialID, batchID, material)
		}
	}
	if jobs == 0 {
		write(w, 200, response{Success: false, Message: "未找到可发布的素材"})
		return
	}
	ok(w, "批量发布任务已创建", map[string]any{"batch_id": batchID, "total": jobs})
}

func (s *Server) loadMaterialByOwner(ctx context.Context, ownerID, materialID int64) (map[string]any, error) {
	var id, owner int64
	var title, description, price, raw, created, updated string
	var category sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,title,description,price,category,data,created_at,updated_at FROM product_materials WHERE owner_id=? AND id=?`, ownerID, materialID).Scan(&id, &owner, &title, &description, &price, &category, &raw, &created, &updated)
	if err != nil {
		return nil, err
	}
	return materialRecord(id, owner, title, description, price, category, raw, created, updated), nil
}

func (s *Server) publishBatchStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	batchID := chi.URLParam(r, "batch_id")
	rows, err := s.db.QueryContext(r.Context(), `SELECT status,count(*) FROM publish_logs WHERE owner_id=? AND batch_id=? GROUP BY status`, u.ID, batchID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	counts := map[string]int{"success": 0, "failed": 0, "publishing": 0}
	total := 0
	for rows.Next() {
		var status string
		var count int
		_ = rows.Scan(&status, &count)
		counts[status] = count
		total += count
	}
	write(w, 200, map[string]any{"success": true, "batch_id": batchID, "total": total, "completed": counts["success"] + counts["failed"], "success_count": counts["success"], "failed_count": counts["failed"], "status": func() string {
		if total == 0 || counts["publishing"] > 0 {
			return "running"
		}
		return "completed"
	}()})
}

func findPublishedItem(root map[string]any) (string, string) {
	var walk func(any)
	var id, rawURL string
	walk = func(value any) {
		if id != "" && rawURL != "" {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				lower := strings.ToLower(key)
				if id == "" && (lower == "itemid" || lower == "item_id") {
					id = fmt.Sprint(child)
				}
				if rawURL == "" && (lower == "itemurl" || lower == "item_url" || lower == "url") && strings.Contains(fmt.Sprint(child), "goofish") {
					rawURL = fmt.Sprint(child)
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(root)
	if id != "" {
		rawURL = "https://www.goofish.com/item?id=" + id
	}
	return id, rawURL
}

func nullableInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func materialRecord(id, owner int64, title, description, price string, category sql.NullString, raw, created, updated string) map[string]any {
	m := map[string]any{}
	mergeJSON(m, raw)
	m["id"], m["user_id"], m["title"], m["description"] = id, owner, title, description
	m["price"], m["category"], m["created_at"], m["updated_at"] = smart(price), nil, created, updated
	if category.Valid {
		m["category"] = category.String
	}
	for _, key := range []string{"images", "videos", "specifications", "sku_rows", "platform_category_path", "platform_attributes"} {
		if _, ok := m[key].([]any); !ok {
			m[key] = []any{}
		}
	}
	return m
}

func (s *Server) materialList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	query := ` FROM product_materials WHERE owner_id=?`
	args := []any{u.ID}
	if title := strings.TrimSpace(r.URL.Query().Get("title")); title != "" {
		query += ` AND title LIKE ?`
		args = append(args, "%"+title+"%")
	}
	for _, key := range []string{"category", "platform_category_id", "condition"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			if key == "category" {
				query += ` AND category=?`
			} else {
				query += ` AND json_extract(data,?)=?`
				args = append(args, "$."+key)
			}
			args = append(args, value)
		}
	}
	var total int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total); err != nil {
		fail(w, 500, err.Error())
		return
	}
	args = append(args, size, (p-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,owner_id,title,description,price,category,data,created_at,updated_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, owner int64
		var title, description, price, raw, created, updated string
		var category sql.NullString
		if err := rows.Scan(&id, &owner, &title, &description, &price, &category, &raw, &created, &updated); err != nil {
			fail(w, 500, err.Error())
			return
		}
		items = append(items, materialRecord(id, owner, title, description, price, category, raw, created, updated))
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "查询素材成功", map[string]any{"list": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}

func validateMaterial(p map[string]any) error {
	if strings.TrimSpace(fmt.Sprint(p["title"])) == "" || strings.TrimSpace(fmt.Sprint(p["description"])) == "" {
		return fmt.Errorf("商品标题和商品描述不能为空")
	}
	price := strings.TrimSpace(fmt.Sprint(p["price"]))
	if price == "" || price == "<nil>" {
		return fmt.Errorf("商品价格不能为空")
	}
	if _, err := strconv.ParseFloat(price, 64); err != nil {
		return fmt.Errorf("商品价格格式无效")
	}
	return nil
}

func (s *Server) materialCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := validateMaterial(p); err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	raw, _ := json.Marshal(p)
	res, err := s.db.ExecContext(r.Context(), `INSERT INTO product_materials(owner_id,title,description,price,category,data) VALUES(?,?,?,?,?,?)`, u.ID, p["title"], p["description"], fmt.Sprint(p["price"]), nullString(fmt.Sprint(p["category"])), string(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	p["id"], p["user_id"] = id, u.ID
	ok(w, "素材创建成功", p)
}

func (s *Server) loadMaterial(r *http.Request, id string) (map[string]any, error) {
	u := current(r)
	var rowID, owner int64
	var title, description, price, raw, created, updated string
	var category sql.NullString
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,title,description,price,category,data,created_at,updated_at FROM product_materials WHERE owner_id=? AND id=?`, u.ID, id).Scan(&rowID, &owner, &title, &description, &price, &category, &raw, &created, &updated)
	if err != nil {
		return nil, err
	}
	return materialRecord(rowID, owner, title, description, price, category, raw, created, updated), nil
}

func (s *Server) materialGet(w http.ResponseWriter, r *http.Request) {
	m, err := s.loadMaterial(r, chi.URLParam(r, "material_id"))
	if err != nil {
		fail(w, 404, "素材不存在")
		return
	}
	ok(w, "查询成功", m)
}

func (s *Server) materialUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := chi.URLParam(r, "material_id")
	old, err := s.loadMaterial(r, id)
	if err != nil {
		fail(w, 404, "素材不存在")
		return
	}
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	for key, value := range p {
		old[key] = value
	}
	if err = validateMaterial(old); err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	raw, _ := json.Marshal(old)
	_, err = s.db.ExecContext(r.Context(), `UPDATE product_materials SET title=?,description=?,price=?,category=?,data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND id=?`, old["title"], old["description"], fmt.Sprint(old["price"]), nullString(fmt.Sprint(old["category"])), string(raw), u.ID, id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "素材更新成功", old)
}

func (s *Server) materialDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM product_materials WHERE owner_id=? AND id=?`, u.ID, chi.URLParam(r, "material_id"))
	affected(w, res, err, "素材删除成功")
}

func (s *Server) materialBatchDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择素材")
		return
	}
	args := []any{u.ID}
	for _, id := range p.IDs {
		args = append(args, id)
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM product_materials WHERE owner_id=? AND id IN (`+placeholders(len(p.IDs))+`)`, args...)
	affected(w, res, err, "批量删除成功")
}

func (s *Server) publishLogList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	query := ` FROM publish_logs WHERE owner_id=?`
	args := []any{u.ID}
	for _, key := range []string{"account_id", "status"} {
		if value := r.URL.Query().Get(key); value != "" {
			query += ` AND ` + key + `=?`
			args = append(args, value)
		}
	}
	var total int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total); err != nil {
		fail(w, 500, err.Error())
		return
	}
	args = append(args, size, (p-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,COALESCE(account_id,''),material_id,COALESCE(batch_id,''),COALESCE(status,''),item_id,item_url,message,COALESCE(request_data,'{}'),COALESCE(response_data,'{}'),created_at,updated_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var account, batch, status, request, responseData, created, updated string
		var material sql.NullInt64
		var itemID, itemURL, message sql.NullString
		if err := rows.Scan(&id, &account, &material, &batch, &status, &itemID, &itemURL, &message, &request, &responseData, &created, &updated); err != nil {
			fail(w, 500, err.Error())
			return
		}
		payload, result := map[string]any{}, map[string]any{}
		mergeJSON(payload, request)
		mergeJSON(result, responseData)
		item := map[string]any{"id": id, "user_id": u.ID, "account_id": account, "material_id": nullable(material), "batch_id": batch, "status": status, "item_id": nullText(itemID), "item_url": nullText(itemURL), "message": nullText(message), "error_message": nil, "title": text(payload["title"]), "description": text(payload["description"]), "price": text(payload["price"]), "created_at": created, "updated_at": updated}
		if status == "failed" {
			item["error_message"] = firstText(message.String, result["error_message"], result["message"])
		}
		for _, key := range []string{"resolved_address_id", "resolved_address_text", "address_source"} {
			item[key] = result[key]
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "查询发布日志成功", map[string]any{"list": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}

func (s *Server) publishLogClear(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM publish_logs WHERE owner_id=?`, u.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	ok(w, "发布日志已清空", map[string]any{"deleted_count": n})
}
