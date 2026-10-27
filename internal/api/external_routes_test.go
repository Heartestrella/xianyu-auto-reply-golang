package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/config"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

// TestExternalRoutesAreRegistered guards the public external API surface.
//
// The handlers in external.go / external_publish.go / category.go have existed
// for a while, but Router() never mounted them, so every documented
// /api/v1/external/* call returned 404. This test fails if any route is
// dropped again.
func TestExternalRoutesAreRegistered(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	server := New(db, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := server.Router()

	// code 0 means "any JSON error envelope"; media rejects the empty multipart
	// body before it reads secret_key, so it answers 40007 instead of 40001.
	cases := []struct {
		method, path string
		code         int
	}{
		{http.MethodPost, "/api/v1/external/enabled-accounts", 40001},
		{http.MethodPost, "/api/v1/external/enabled-accounts/capability", 40001},
		{http.MethodPost, "/api/v1/external/category/recommend", 40001},
		{http.MethodPost, "/api/v1/external/category/properties", 40001},
		{http.MethodPost, "/api/v1/external/publish/media", 0},
		{http.MethodPost, "/api/v1/external/publish/single", 40001},
		{http.MethodPost, "/api/v1/external/orders", 40001},
		{http.MethodPost, "/api/v1/external/account-cookie/sync", 0},
		{http.MethodPost, "/api/v1/external/message-logs", 40001},
		{http.MethodGet, "/api/v1/external/orders", 40001},
		{http.MethodGet, "/api/v1/external/message-logs", 40001},
	}
	for _, tc := range cases {
		assertExternalJSON(t, router, tc.method, tc.path, tc.code)
	}
}

func assertExternalJSON(t *testing.T, handler http.Handler, method, path string, wantCode int) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusNotFound {
		t.Fatalf("%s %s is not registered (404)", method, path)
	}
	var body struct {
		Success bool `json:"success"`
		Code    int  `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s returned non-JSON: %s", method, path, recorder.Body.String())
	}
	if body.Success {
		t.Fatalf("%s %s without secret_key unexpectedly succeeded", method, path)
	}
	if wantCode != 0 && body.Code != wantCode {
		t.Fatalf("%s %s without secret_key: code=%d, want %d", method, path, body.Code, wantCode)
	}
}
