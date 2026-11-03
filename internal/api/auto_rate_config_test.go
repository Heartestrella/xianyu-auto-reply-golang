package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestAutoRateConfigEnvelopeAndRoundTrip(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "rating.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{db: db}
	router := chi.NewRouter()
	s.accountConfigRoutes(router, "auto_rate", map[string]any{"enabled": false})
	request := func(method, body string, owner int64) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/account", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: owner, Role: "MEMBER"}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var result struct {
			Success bool           `json:"success"`
			Data    map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || !result.Success || result.Data == nil {
			t.Fatalf("frontend cannot load config: HTTP %d %s err=%v", w.Code, w.Body.String(), err)
		}
		return result.Data
	}
	if cfg := request("GET", "", 1); cfg["enabled"] != false {
		t.Fatal("missing configuration should be disabled", cfg)
	}
	for _, enabled := range []bool{true, false, true} {
		body, _ := json.Marshal(map[string]any{"enabled": enabled, "rate_type": "text", "text_content": "测试评价", "api_url": "", "thanks_enabled": false})
		request("PUT", string(body), 1)
		cfg := request("GET", "", 1)
		if cfg["enabled"] != enabled || cfg["rate_type"] != "text" || cfg["text_content"] != "测试评价" {
			t.Fatalf("saved setting changed on reload: %v", cfg)
		}
	}
	if cfg := request("GET", "", 2); cfg["enabled"] != false || cfg["text_content"] != nil {
		t.Fatal("another user's configuration was exposed", cfg)
	}
}
