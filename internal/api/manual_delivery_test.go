package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestManualDeliveryRepairsPlaceholderBeforeSelectingCards(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "manual.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','unb=123'); INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES(1,'a','order','item','buyer','pending_ship','{"chat_id":"FAILED_buyer"}')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, online: func(string) bool { return true }, chatSend: func(context.Context, string, string, string, string) (string, error) {
		t.Fatal("unexpected send without a card")
		return "", nil
	}}
	for _, success := range []bool{false, true} {
		calls := 0
		s.chatCreate = func(_ context.Context, account, buyer, item string) (string, error) {
			calls++
			if account != "a" || buyer != "buyer" || item != "item" {
				t.Fatal("wrong conversation")
			}
			if !success {
				return "", errors.New("temporary disconnect")
			}
			return "recovered", nil
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"order_no":"order"}`))
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: "MEMBER"}))
		w := httptest.NewRecorder()
		s.orderManualDelivery(w, r)
		if calls != 1 {
			t.Fatalf("placeholder did not cause conversation creation: %s", w.Body.String())
		}
		if !success && !strings.Contains(w.Body.String(), "temporary disconnect") {
			t.Fatal("failure reason lost", w.Body.String())
		}
		var chat string
		if err = db.QueryRow(`SELECT json_extract(data,'$.chat_id') FROM xy_orders`).Scan(&chat); err != nil {
			t.Fatal(err)
		}
		if success && chat != "recovered" {
			t.Fatal("repaired chat not persisted")
		}
	}
}
