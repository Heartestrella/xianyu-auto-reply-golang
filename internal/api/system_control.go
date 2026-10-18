package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) systemControlStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	online := s.db.PingContext(r.Context()) == nil
	services := []map[string]any{
		{"key": "backend-web", "label": "后端服务", "port": 8000, "online": online},
		{"key": "websocket", "label": "消息服务", "port": 8001, "online": online},
		{"key": "scheduler", "label": "定时任务服务", "port": 8002, "online": online},
	}
	ok(w, "查询成功", map[string]any{"runtime": "go-monolith", "services": services})
}

func (s *Server) systemControlRestart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	key := chi.URLParam(r, "service_key")
	if key != "backend-web" && key != "websocket" && key != "scheduler" {
		write(w, 200, response{Success: false, Message: "未知服务：" + key})
		return
	}
	if s.serviceRestart == nil {
		write(w, 200, response{Success: false, Message: "服务控制器未初始化"})
		return
	}
	if err := s.serviceRestart(key); err != nil {
		write(w, 200, response{Success: false, Message: err.Error(), Data: map[string]any{"mode": "go-monolith"}})
		return
	}
	ok(w, "服务组件已重新加载", map[string]any{"mode": "go-monolith"})
}
