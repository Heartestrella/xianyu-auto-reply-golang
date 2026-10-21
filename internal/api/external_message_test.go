package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/config"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestExternalMessageRequiresConfiguredKey(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range []struct {
		name, configured, supplied string
		allowed                    bool
	}{
		{"unconfigured", "", "xianyu_api_secret_2024", false},
		{"blank configuration", " \t", "xianyu_api_secret_2024", false},
		{"missing key", "test-secret", "", false},
		{"wrong key", "test-secret", "wrong", false},
		{"old default", "test-secret", "xianyu_api_secret_2024", false},
		{"configured key", "test-secret", "test-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := &Server{db: db, cfg: config.Config{APISecretKey: tc.configured}, chatSend: func(context.Context, string, string, string, string) (string, error) { calls++; return "sent", nil }}
			body, _ := json.Marshal(map[string]string{"api_key": tc.supplied, "cookie_id": "a", "chat_id": "chat", "to_user_id": "buyer", "message": "hello"})
			w := httptest.NewRecorder()
			s.externalSendMessage(w, httptest.NewRequest("POST", "/api/v1/messages/send", bytes.NewReader(body)))
			var result response
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.allowed || (calls == 1) != tc.allowed {
				t.Fatalf("success=%v calls=%d", result.Success, calls)
			}
		})
	}
}
