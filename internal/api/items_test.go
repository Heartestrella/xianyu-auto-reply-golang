package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestItemsAcrossAccounts(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "items.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO xy_catalog_items(owner_id,account_id,item_id,title,data) VALUES(1,'a','same','First','{"id":"same","cookie_id":null}'),(1,'b','same','Second','{}'),(2,'c','private','Other','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	for _, tc := range []struct {
		query, role string
		total       int
	}{{"", "ADMIN", 3}, {"&cookie_id=a", "ADMIN", 1}, {"", "MEMBER", 2}, {"&cookie_id=c", "MEMBER", 0}} {
		r := httptest.NewRequest("GET", "/api/v1/items/paginated?page=1&page_size=20"+tc.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: tc.role}))
		w := httptest.NewRecorder()
		s.itemsPaginated(w, r)
		var body struct {
			Success bool             `json:"success"`
			Total   int              `json:"total"`
			Data    []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || !body.Success || body.Total != tc.total || len(body.Data) != tc.total {
			t.Fatalf("unexpected list: %s err=%v", w.Body.String(), err)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("list can be cached")
		}
		seen := map[string]bool{}
		for _, item := range body.Data {
			id, ok := item["id"].(string)
			if !ok || id == "" || seen[id] || item["cookie_id"] == nil {
				t.Fatalf("invalid item identity: %v", item)
			}
			seen[id] = true
		}
	}
}
