package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestOrdersSearch(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "orders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES
		(1,'account-a','9000000000000000001','item-a','buyer-a','completed','{"order_id":9000000000000000000}'),
		(1,'account-a','9000000000000000002','item-a','buyer-b','shipped','{}'),
		(1,'account-b','9000000000000000003','item-b','buyer-a','completed','{}'),
		(2,'account-c','9000000000000000004','item-a','buyer-a','completed','{}'),
		(1,'account-a','literal_%','item-c','buyer-c','completed','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	admin := User{ID: 1, Role: "ADMIN"}
	member := User{ID: 1, Role: "MEMBER"}
	cases := []struct {
		name, query  string
		user         User
		total, pages int
		ids          []string
	}{
		{"all orders", "", admin, 5, 1, []string{"literal_%", "9000000000000000004", "9000000000000000003", "9000000000000000002", "9000000000000000001"}},
		{"full order number", "search=9000000000000000001", admin, 1, 1, []string{"9000000000000000001"}},
		{"trimmed order number", "search=" + url.QueryEscape(" 9000000000000000001 "), admin, 1, 1, []string{"9000000000000000001"}},
		{"missing order", "search=nonexistent", admin, 0, 0, []string{}},
		{"partial search pagination", "search=9000000000000&page=2&page_size=2", admin, 4, 2, []string{"9000000000000000002", "9000000000000000001"}},
		{"item search", "search=item-a", admin, 3, 1, []string{"9000000000000000004", "9000000000000000002", "9000000000000000001"}},
		{"buyer search", "search=buyer-a", admin, 3, 1, []string{"9000000000000000004", "9000000000000000003", "9000000000000000001"}},
		{"account and status", "search=900000&cookie_id=account-a&status=completed", admin, 1, 1, []string{"9000000000000000001"}},
		{"member search isolation", "search=buyer-a", member, 2, 1, []string{"9000000000000000003", "9000000000000000001"}},
		{"other owner's order", "search=9000000000000000004", member, 0, 0, []string{}},
		{"literal wildcard characters", "search=" + url.QueryEscape("_%"), admin, 1, 1, []string{"literal_%"}},
		{"query remains parameterized", "search=" + url.QueryEscape("' OR 1=1 --"), admin, 0, 0, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/orders?"+tc.query, nil)
			r = r.WithContext(context.WithValue(r.Context(), userKey{}, tc.user))
			w := httptest.NewRecorder()
			s.orders(w, r)
			if w.Code != 200 {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			var body struct {
				Success    bool `json:"success"`
				Total      int  `json:"total"`
				TotalPages int  `json:"total_pages"`
				Data       []struct {
					OrderID string `json:"order_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, order := range body.Data {
				ids = append(ids, order.OrderID)
			}
			if !body.Success || body.Total != tc.total || body.TotalPages != tc.pages || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("got success=%v total=%d pages=%d ids=%v; want total=%d pages=%d ids=%v", body.Success, body.Total, body.TotalPages, ids, tc.total, tc.pages, tc.ids)
			}
		})
	}
	// 清空查询后回到完整列表，并能看到同步期间新增的订单。
	if _, err := db.Exec(`INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status) VALUES(1,'account-a','new-synced-order','item-a','buyer-a','completed')`); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/orders?search=%20%20", nil)
	r = r.WithContext(context.WithValue(r.Context(), userKey{}, admin))
	w := httptest.NewRecorder()
	s.orders(w, r)
	var body struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Total != 6 {
		t.Fatalf("cleared search did not return updated total: status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
}
