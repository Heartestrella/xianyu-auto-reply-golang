package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestRedFlowerModesAndOwnership(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "modes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','cookie'),(2,'other','cookie')`); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	for _, tc := range []struct {
		field, body, account string
		status               int
		auto, after          bool
	}{
		{"red_flower_after_shipment", `{"red_flower_after_shipment":true}`, "a", 200, true, true},
		{"auto_red_flower", `{"auto_red_flower":true}`, "a", 200, true, false},
		{"red_flower_after_shipment", `{"red_flower_after_shipment":true}`, "a", 200, true, true},
		{"red_flower_after_shipment", `{"red_flower_after_shipment":false}`, "a", 200, false, false},
		{"auto_red_flower", `{}`, "a", 400, false, false},
		{"auto_red_flower", `{"auto_red_flower":true}`, "other", 404, false, false},
	} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(tc.body))
		ctx := context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: "MEMBER"})
		route := chi.NewRouteContext()
		route.URLParams.Add("id", tc.account)
		r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		s.accountField(tc.field, tc.field)(w, r)
		var auto, after bool
		if err := db.QueryRow(`SELECT auto_red_flower,red_flower_after_shipment FROM xy_accounts WHERE account_id=?`, tc.account).Scan(&auto, &after); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || auto != tc.auto || after != tc.after {
			t.Fatalf("%s: status=%d auto=%v after=%v", tc.body, w.Code, auto, after)
		}
	}
}
