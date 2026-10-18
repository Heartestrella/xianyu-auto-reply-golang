package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/ai"
)

const aiListingConfigKind = "ai_listing_config"
const aiListingTaskKind = "ai_listing_task"

func (s *Server) registerAIListingRoutes(r chi.Router) {
	r.Get("/ai-listing/configs", s.aiListingConfigs)
	r.Post("/ai-listing/configs", s.aiListingConfigCreate)
	r.Post("/ai-listing/configs/models", s.aiListingModels)
	r.Put("/ai-listing/configs/{config_id}", s.aiListingConfigUpdate)
	r.Delete("/ai-listing/configs/{config_id}", s.aiListingConfigDelete)
	r.Post("/ai-listing/configs/{config_id}/test", s.aiListingConfigTest)
	r.Get("/ai-listing/tasks", s.aiListingTasks)
	r.Post("/ai-listing/tasks", s.aiListingTaskCreate)
	r.Get("/ai-listing/tasks/{task_id}", s.aiListingTaskGet)
	r.Post("/ai-listing/tasks/{task_id}/cancel", s.aiListingTaskCancel)
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func (s *Server) aiListingConfigs(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	query, args := ` FROM resource_records WHERE owner_id=? AND kind=?`, []any{u.ID, aiListingConfigKind}
	if name != "" {
		query += ` AND json_extract(data,'$.name') LIKE ?`
		args = append(args, "%"+name+"%")
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total)
	args = append(args, size, (p-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,data,created_at,updated_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var raw, created, updated string
		if rows.Scan(&id, &raw, &created, &updated) == nil {
			m := map[string]any{"id": id, "created_at": created, "updated_at": updated}
			mergeJSON(m, raw)
			m["text_api_key"] = maskSecret(firstString(m, "text_api_key"))
			m["image_api_key"] = maskSecret(firstString(m, "image_api_key"))
			items = append(items, m)
		}
	}
	ok(w, "查询成功", map[string]any{"list": items, "items": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}

func validateAIListingConfig(p map[string]any, creating bool) error {
	if firstString(p, "name") == "" || firstString(p, "text_base_url") == "" || firstString(p, "text_model") == "" {
		return fmt.Errorf("配置名称、文案接口地址和模型不能为空")
	}
	if creating && firstString(p, "text_api_key") == "" {
		return fmt.Errorf("请填写文案接口密钥")
	}
	if compatibilityEnabled(map[string]any{"enabled": p["image_enabled"]}, false) && (firstString(p, "image_base_url") == "" || firstString(p, "image_model") == "") {
		return fmt.Errorf("启用AI图片生成时必须填写图片接口地址和模型")
	}
	return nil
}

func (s *Server) aiListingConfigCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := validateAIListingConfig(p, true); err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	if firstString(p, "provider_type") == "" {
		p["provider_type"] = "openai_compatible"
	}
	raw, _ := json.Marshal(p)
	res, err := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,?,1,?)`, u.ID, aiListingConfigKind, string(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	ok(w, "配置已保存", map[string]any{"id": id})
}

func (s *Server) loadAIListingConfig(ctx context.Context, owner int64, id string) (int64, map[string]any, error) {
	var rowID int64
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT id,data FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, owner, aiListingConfigKind, id).Scan(&rowID, &raw)
	m := map[string]any{}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &m)
	}
	return rowID, m, err
}

func (s *Server) aiListingConfigUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := chi.URLParam(r, "config_id")
	rowID, old, err := s.loadAIListingConfig(r.Context(), u.ID, id)
	if err != nil {
		write(w, 200, response{Success: false, Message: "配置不存在"})
		return
	}
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	for k, v := range p {
		if (k == "text_api_key" || k == "image_api_key") && strings.TrimSpace(fmt.Sprint(v)) == "" {
			continue
		}
		old[k] = v
	}
	if err = validateAIListingConfig(old, false); err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	raw, _ := json.Marshal(old)
	_, err = s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND id=?`, string(raw), u.ID, rowID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "配置已更新", map[string]any{"id": rowID})
}

func (s *Server) aiListingConfigDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, aiListingConfigKind, chi.URLParam(r, "config_id"))
	affected(w, res, err, "配置已删除")
}

func listingAIConfig(m map[string]any) ai.Config {
	return ai.Config{Provider: firstString(m, "provider_type"), BaseURL: firstString(m, "text_base_url"), APIKey: firstString(m, "text_api_key"), Model: firstString(m, "text_model"), SystemPrompt: firstString(m, "prompt_template"), Timeout: 90 * time.Second}
}

func (s *Server) aiListingModels(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if firstString(p, "api_key") == "" && firstString(p, "config_id") != "" {
		_, saved, _ := s.loadAIListingConfig(r.Context(), u.ID, firstString(p, "config_id"))
		p["api_key"] = saved["text_api_key"]
	}
	models, err := ai.New().Models(r.Context(), ai.Config{Provider: firstString(p, "provider_type"), BaseURL: firstString(p, "base_url"), APIKey: firstString(p, "api_key")})
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	ok(w, "获取成功", map[string]any{"models": models})
}

func (s *Server) aiListingConfigTest(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	_, cfg, err := s.loadAIListingConfig(r.Context(), u.ID, chi.URLParam(r, "config_id"))
	if err != nil {
		write(w, 200, response{Success: false, Message: "配置不存在"})
		return
	}
	reply, err := ai.New().Generate(r.Context(), listingAIConfig(cfg), "你好，请只回复：连接成功")
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	ok(w, "连接成功", map[string]any{"reply": reply})
}

func (s *Server) aiListingTaskCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	count := intNumber(p["count"])
	if count < 1 || count > 50 || firstString(p, "keyword") == "" {
		write(w, 200, response{Success: false, Message: "关键词不能为空，生成数量须为1到50"})
		return
	}
	_, cfg, err := s.loadAIListingConfig(r.Context(), u.ID, firstString(p, "config_id"))
	if err != nil {
		write(w, 200, response{Success: false, Message: "AI铺货配置不存在，请先新增配置"})
		return
	}
	var active int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.status') IN ('pending','running')`, u.ID, aiListingTaskKind).Scan(&active)
	if active >= 3 {
		write(w, 200, response{Success: false, Message: "最多同时运行 3 个铺货任务"})
		return
	}
	taskID := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), u.ID)
	p["task_id"] = taskID
	p["status"] = "pending"
	p["total_count"] = count
	p["completed_count"] = 0
	p["failed_count"] = 0
	p["items"] = []any{}
	raw, _ := json.Marshal(p)
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,?,1,?)`, u.ID, aiListingTaskKind, string(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	go s.runAIListingTask(context.Background(), u.ID, taskID, cfg)
	ok(w, "任务已开始执行", map[string]any{"task_id": taskID, "total": count, "image_enabled": p["image_enabled"]})
}

func (s *Server) runAIListingTask(ctx context.Context, owner int64, taskID string, cfg map[string]any) {
	var rowID int64
	var raw string
	if s.db.QueryRowContext(ctx, `SELECT id,data FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.task_id')=?`, owner, aiListingTaskKind, taskID).Scan(&rowID, &raw) != nil {
		return
	}
	task := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &task)
	task["status"] = "running"
	s.saveAIListingTask(ctx, rowID, task)
	count := intNumber(task["total_count"])
	keyword := firstString(task, "keyword")
	defaults, _ := task["material_defaults"].(map[string]any)
	items := []any{}
	for i := 0; i < count; i++ {
		if s.aiTaskCanceled(ctx, rowID) {
			return
		}
		prompt := fmt.Sprintf("为闲鱼二手交易平台生成第%d条商品文案，主题：%s。只输出JSON对象，字段title和description；标题简洁真实，描述清楚且不要虚构品牌参数。", i+1, keyword)
		text, err := ai.New().Generate(ctx, listingAIConfig(cfg), prompt)
		item := map[string]any{"index": i + 1}
		if err != nil {
			item["status"] = "failed"
			item["error"] = err.Error()
			task["failed_count"] = intNumber(task["failed_count"]) + 1
		} else {
			generated := parseAIListingJSON(text)
			title := firstString(generated, "title")
			description := firstString(generated, "description")
			if title == "" {
				title = keyword
			}
			if description == "" {
				description = text
			}
			price := listingPrice(task, i, count)
			material := map[string]any{}
			for k, v := range defaults {
				material[k] = v
			}
			material["title"] = title
			material["description"] = description
			material["price"] = price
			material["ai_task_id"] = taskID
			encoded, _ := json.Marshal(material)
			result, e := s.db.ExecContext(ctx, `INSERT INTO product_materials(owner_id,title,description,price,category,data) VALUES(?,?,?,?,?,?)`, owner, title, description, fmt.Sprint(price), nullString(firstString(material, "category")), string(encoded))
			if e != nil {
				item["status"] = "failed"
				item["error"] = e.Error()
				task["failed_count"] = intNumber(task["failed_count"]) + 1
			} else {
				mid, _ := result.LastInsertId()
				item = map[string]any{"index": i + 1, "status": "success", "material_id": mid, "title": title}
				task["completed_count"] = intNumber(task["completed_count"]) + 1
			}
		}
		items = append(items, item)
		task["items"] = items
		task["progress"] = float64(i+1) / float64(count) * 100
		s.saveAIListingTask(ctx, rowID, task)
	}
	if intNumber(task["failed_count"]) > 0 {
		task["status"] = "partial"
	} else {
		task["status"] = "completed"
	}
	task["finished_at"] = time.Now().Format(time.RFC3339)
	s.saveAIListingTask(ctx, rowID, task)
}

func parseAIListingJSON(text string) map[string]any {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	out := map[string]any{}
	_ = json.Unmarshal([]byte(text), &out)
	return out
}
func listingPrice(task map[string]any, index, count int) any {
	if firstString(task, "price_mode") == "range" {
		min, _ := strconv.ParseFloat(firstString(task, "price_min"), 64)
		max, _ := strconv.ParseFloat(firstString(task, "price_max"), 64)
		if count <= 1 {
			return min
		}
		return min + (max-min)*float64(index)/float64(count-1)
	}
	return task["price"]
}
func (s *Server) saveAIListingTask(ctx context.Context, id int64, task map[string]any) {
	raw, _ := json.Marshal(task)
	_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), id)
}
func (s *Server) aiTaskCanceled(ctx context.Context, id int64) bool {
	var status string
	_ = s.db.QueryRowContext(ctx, `SELECT json_extract(data,'$.status') FROM resource_records WHERE id=?`, id).Scan(&status)
	return status == "canceled"
}

func (s *Server) aiListingTasks(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	query := ` FROM resource_records WHERE owner_id=? AND kind=?`
	args := []any{u.ID, aiListingTaskKind}
	if status := r.URL.Query().Get("status"); status != "" {
		query += ` AND json_extract(data,'$.status')=?`
		args = append(args, status)
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+query, args...).Scan(&total)
	args = append(args, size, (p-1)*size)
	rows, err := s.db.QueryContext(r.Context(), `SELECT data,created_at,updated_at`+query+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var raw, created, updated string
		_ = rows.Scan(&raw, &created, &updated)
		m := map[string]any{"created_at": created, "updated_at": updated}
		mergeJSON(m, raw)
		delete(m, "items")
		items = append(items, m)
	}
	ok(w, "查询成功", map[string]any{"list": items, "items": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}
func (s *Server) aiListingTaskGet(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var raw string
	err := s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.task_id')=?`, u.ID, aiListingTaskKind, chi.URLParam(r, "task_id")).Scan(&raw)
	if err == sql.ErrNoRows {
		write(w, 200, response{Success: false, Message: "任务不存在"})
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	ok(w, "查询成功", m)
}
func (s *Server) aiListingTaskCancel(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=json_set(data,'$.status','canceled','$.finished_at',CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind=? AND json_extract(data,'$.task_id')=? AND json_extract(data,'$.status') IN ('pending','running')`, u.ID, aiListingTaskKind, chi.URLParam(r, "task_id"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{Success: false, Message: "任务不存在或已结束"})
		return
	}
	ok(w, "任务已取消", nil)
}
