package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestProductPublishListContracts(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "publish.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db}
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		table   string
	}{
		{"materials", s.materialList, "product_materials"}, {"logs", s.publishLogList, "publish_logs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := func(want int) []map[string]any {
				r := httptest.NewRequest("GET", "/?page=1&page_size=10", nil)
				r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: "ADMIN"}))
				w := httptest.NewRecorder()
				test.handler(w, r)
				var body struct {
					Success bool `json:"success"`
					Data    struct {
						List  []map[string]any `json:"list"`
						Total int              `json:"total"`
						Page  int              `json:"page"`
					} `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || !body.Success || body.Data.List == nil || body.Data.Total != want || body.Data.Page != 1 {
					t.Fatalf("invalid page contract: %d %s err=%v", w.Code, w.Body.String(), err)
				}
				return body.Data.List
			}
			request(0)
			if test.table == "product_materials" {
				_, err = db.Exec(`INSERT INTO product_materials(owner_id,title,description,price,data) VALUES(1,'material','description','2.5','{"id":999,"images":null}'),(2,'other','other','1','{}')`)
			} else {
				_, err = db.Exec(`INSERT INTO publish_logs(owner_id,status,message,request_data) VALUES(1,'failed','publish rejected','{"title":"material","price":"2.5"}'),(2,'success','','{}')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			items := request(1)
			if len(items) != 1 || items[0]["title"] != "material" {
				t.Fatalf("invalid rows: %v", items)
			}
			if test.table == "product_materials" {
				if items[0]["id"] == float64(999) || items[0]["images"] == nil {
					t.Fatalf("invalid material defaults: %v", items[0])
				}
			} else if items[0]["error_message"] != "publish rejected" || items[0]["price"] != "2.5" {
				t.Fatalf("missing log details: %v", items[0])
			}
		})
	}
}
