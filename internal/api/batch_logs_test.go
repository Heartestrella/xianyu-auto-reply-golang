package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestBatchLogPages(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "batches.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db}
	for _, kind := range []string{"rate", "polish", "cookie_refresh", "api_cookie_renew", "close_notice", "red_flower", "token_renewal", "login_renew", "redelivery"} {
		t.Run(kind, func(t *testing.T) {
			call := func(handler http.HandlerFunc, path string) map[string]any {
				r := httptest.NewRequest("GET", path, nil)
				ctx := chi.NewRouteContext()
				ctx.URLParams.Add("batch_id", "batch-"+kind)
				r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
				w := httptest.NewRecorder()
				handler(w, r)
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body["success"] != true {
					t.Fatalf("invalid response %d %s err=%v", w.Code, w.Body.String(), err)
				}
				return body["data"].(map[string]any)
			}
			empty := call(s.adminBatchList(kind), "/?page=1")
			if len(empty["items"].([]any)) != 0 {
				t.Fatal("empty list is not an array")
			}
			for i, status := range []string{"success", "failed"} {
				data, _ := json.Marshal(map[string]any{"batch_id": "batch-" + kind, "success": status == "success", "error": map[bool]string{true: "rejected", false: ""}[i == 1]})
				_, err := db.Exec(`INSERT INTO event_logs(owner_id,kind,account_id,level,message,data,created_at) VALUES(1,?,'account',?,'result',?,'2026-10-19 12:00:00')`, "scheduled_"+kind, map[bool]string{true: "ERROR", false: "INFO"}[i == 1], string(data))
				if err != nil {
					t.Fatal(err)
				}
			}
			list := call(s.adminBatchList(kind), "/?page=1&page_size=10&start_date=2026-10-19&end_date=2026-10-19")
			if list["total"] != float64(1) || len(list["items"].([]any)) != 1 {
				t.Fatalf("missing scheduled events: %v", list)
			}
			batch := list["items"].([]any)[0].(map[string]any)
			if batch["total_orders"] != float64(2) || batch["success_count"] != float64(1) || batch["failed_count"] != float64(1) || batch["executed_at"] == nil {
				t.Fatalf("invalid summary: %v", batch)
			}
			outside := call(s.adminBatchList(kind), "/?start_date=2026-10-21")
			if outside["total"] != float64(0) {
				t.Fatal("date filter ignored")
			}
			detail := call(s.adminBatchDetail(kind), "/detail")
			logs := detail["logs"].([]any)
			if len(logs) != 2 || logs[1].(map[string]any)["error_message"] != "rejected" || detail["failed_count"] != float64(1) {
				t.Fatalf("invalid detail: %v", detail)
			}
		})
	}
}

func TestBatchLogClearRetainsRecentEvents(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "clear.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO event_logs(kind,message,created_at) VALUES('scheduled_rate','old',datetime('now','-11 days')),('rate','recent',CURRENT_TIMESTAMP),('polish','other',datetime('now','-11 days'))`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	w := httptest.NewRecorder()
	s.adminEventKindClear("rate")(w, httptest.NewRequest("DELETE", "/?days=10", nil))
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM event_logs`).Scan(&remaining); err != nil || remaining != 2 || w.Code != 200 {
		t.Fatalf("recent or unrelated events deleted: %d %s %v", remaining, w.Body.String(), err)
	}
}
