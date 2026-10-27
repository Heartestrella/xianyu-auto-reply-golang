package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Some OpenAI-compatible gateways (for example self-hosted routers) return
// {"data": {"choices": [...]}} instead of the standard top-level
// {"choices": [...]}. Generate must accept both, otherwise a working gateway
// is reported as "AI服务未返回回复内容".
func TestOpenAIChoicesNestedUnderData(t *testing.T) {
	reply, err := generateAgainst(t, `{"data":{"choices":[{"message":{"content":"连接成功"}}]}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != "连接成功" {
		t.Fatalf("reply = %q, want 连接成功", reply)
	}
}

func TestOpenAIStandardChoicesEnvelope(t *testing.T) {
	reply, err := generateAgainst(t, `{"choices":[{"message":{"content":"连接成功"}}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != "连接成功" {
		t.Fatalf("reply = %q, want 连接成功", reply)
	}
}

func TestOpenAIEmptyChoicesReportsError(t *testing.T) {
	if _, err := generateAgainst(t, `{"data":{"choices":[]}}`); err == nil {
		t.Fatal("expected an error for an empty choices list")
	}
}

func TestOpenAISendsBearerTokenAndPath(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	cfg := Config{Provider: "openai_compatible", BaseURL: server.URL, APIKey: "test-key", Model: "test-model", Timeout: 5 * time.Second}
	if _, err := client.Generate(context.Background(), cfg, "hi"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotModel != "test-model" {
		t.Fatalf("model = %q", gotModel)
	}
}

func generateAgainst(t *testing.T, response string) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	cfg := Config{Provider: "openai_compatible", BaseURL: server.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second}
	return client.Generate(context.Background(), cfg, "ping")
}
