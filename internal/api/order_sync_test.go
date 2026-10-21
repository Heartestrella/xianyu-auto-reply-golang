package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func TestManualOrderSyncHydratesAndCarriesCookies(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','old'); INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES(1,'a','9000000000000000001','item','buyer','pending_ship','{}')`); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	calls := 0
	s.orderMTOPCall = func(_ context.Context, o xianyu.MTOPOptions) xianyu.MTOPResult {
		calls++
		switch calls {
		case 1:
			if o.Data["queryCode"] != "ALL" || o.Data["pageNumber"] != 1 {
				t.Fatalf("invalid list request: %v", o.Data)
			}
			return xianyu.MTOPResult{Success: true, Cookie: "list-cookie", Response: map[string]any{"data": map[string]any{"module": map[string]any{"nextPage": true, "items": []any{map[string]any{"commonData": map[string]any{"orderId": "9000000000000000001", "itemId": "item", "orderStatus": "待发货"}, "buyerInfoVO": map[string]any{"buyerId": "buyer"}}}}}}}
		case 2:
			if o.API != "mtop.idle.web.trade.order.detail" || o.Cookie != "list-cookie" {
				t.Fatal("details did not use list cookie")
			}
			return xianyu.MTOPResult{Success: true, Cookie: "detail-cookie", Response: map[string]any{"data": map[string]any{"components": []any{map[string]any{"render": "orderInfoVO", "data": map[string]any{"itemInfo": map[string]any{"skuInfo": "套餐:月卡"}}}}}}}
		case 3:
			if o.Data["queryCode"] != "ALL" || o.Data["pageNumber"] != 2 {
				t.Fatalf("invalid next page: %v", o.Data)
			}
			if o.Cookie != "detail-cookie" {
				t.Fatal("pagination lost detail cookie")
			}
			return xianyu.MTOPResult{Success: true, Response: map[string]any{"data": map[string]any{"module": map[string]any{"nextPage": false, "items": []any{}}}}}
		default:
			t.Fatal("unexpected request")
			return xianyu.MTOPResult{}
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"cookie_id":"a"}`))
	r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: "MEMBER"}))
	w := httptest.NewRecorder()
	s.fetchOrders(w, r)
	if w.Code != 200 || calls != 3 {
		t.Fatalf("calls=%d response=%s", calls, w.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Fetched int `json:"total_fetched"`
			New     int `json:"new_inserted"`
			Updated int `json:"updated"`
			Failed  int `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.Success || body.Data.Fetched != 1 || body.Data.New != 0 || body.Data.Updated != 1 || body.Data.Failed != 0 {
		t.Fatalf("bad sync summary: %s err=%v", w.Body.String(), err)
	}
	var spec string
	if err = db.QueryRow(`SELECT json_extract(data,'$.spec_value') FROM xy_orders`).Scan(&spec); err != nil || spec != "月卡" {
		t.Fatal(spec, err)
	}
}
