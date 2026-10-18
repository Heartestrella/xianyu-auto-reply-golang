package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/ai"
)

func (s *Server) registerAIRoutes(r chi.Router) {
	r.Post("/ai-reply-settings/models", s.aiModels)
	r.Post("/ai-reply-test/{cookie_id}", s.aiTest)
}

func aiConfigFromMap(m map[string]any) ai.Config {
	timeout := 45 * time.Second
	if seconds := intNumber(m["timeout"]); seconds > 0 && seconds <= 300 {
		timeout = time.Duration(seconds) * time.Second
	}
	provider := firstString(m, "provider_type", "provider")
	if provider == "" {
		provider = "openai_compatible"
	}
	return ai.Config{
		Provider:     provider,
		BaseURL:      firstString(m, "base_url", "api_url"),
		APIKey:       firstString(m, "api_key"),
		Model:        firstString(m, "model_name", "model"),
		AppID:        firstString(m, "app_id"),
		SystemPrompt: firstString(m, "custom_prompts", "prompt"),
		Timeout:      timeout,
	}
}

func (s *Server) aiModels(w http.ResponseWriter, r *http.Request) {
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	models, err := ai.New().Models(r.Context(), aiConfigFromMap(p))
	if err != nil {
		write(w, 200, response{Success: false, Message: "获取模型列表失败：" + err.Error(), Data: map[string]any{"models": []any{}}})
		return
	}
	if len(models) == 0 {
		write(w, 200, response{Success: false, Message: "该服务商未返回模型列表，请直接输入模型名称", Data: map[string]any{"models": models}})
		return
	}
	ok(w, fmt.Sprintf("获取模型列表成功，共 %d 个模型", len(models)), map[string]any{"models": models})
}

func (s *Server) aiTest(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "cookie_id")
	var settings string
	err := s.db.QueryRowContext(r.Context(), `SELECT rr.data FROM resource_records rr JOIN xy_accounts a ON a.owner_id=rr.owner_id AND a.account_id=rr.account_id WHERE rr.owner_id=? AND rr.kind='ai_reply' AND rr.account_id=? ORDER BY rr.id DESC LIMIT 1`, u.ID, accountID).Scan(&settings)
	if err == sql.ErrNoRows {
		write(w, 200, response{Success: false, Message: "请先配置AI回复设置"})
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(settings), &m)
	reply, err := ai.New().Generate(r.Context(), aiConfigFromMap(m), "这是连接测试。请只回复：连接成功")
	if err != nil {
		write(w, 200, response{Success: false, Message: "AI连接测试失败：" + err.Error()})
		return
	}
	ok(w, "AI连接测试成功", map[string]any{"reply": reply})
}
