package xianyu

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func soldOrder(no string) map[string]any {
	return map[string]any{"commonData": map[string]any{"orderId": json.Number(no), "itemId": "item", "orderStatus": "待发货"}, "buyerInfoVO": map[string]any{"buyerId": "buyer"}, "priceVO": map[string]any{"buyNum": "1", "totalPrice": "10"}}
}
func orderSpecResponse(sku string) MTOPResult {
	return MTOPResult{Success: true, Response: map[string]any{"data": map[string]any{"itemId": "item", "peerUserId": "buyer", "components": []any{map[string]any{"render": "orderInfoVO", "data": map[string]any{"buyerUserId": "buyer", "itemInfo": map[string]any{"itemId": "item", "skuInfo": sku}}}}}}}
}

func TestOrderSpecHydrationAndMatching(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "specs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','old'); INSERT INTO xy_catalog_items(owner_id,account_id,item_id,data) VALUES(1,'a','item','{"is_multi_spec":true}'); INSERT INTO cards(id,user_id,name,type,data) VALUES(1,1,'generic','text','{"text_content":"wrong"}'),(2,2,'other owner','text','{"is_multi_spec":true,"spec_name":"套餐","spec_value":"1"}'); INSERT INTO card_item_relations(card_id,item_id) VALUES(1,'item'),(2,'item')`)
	if err != nil {
		t.Fatal(err)
	}
	a := OrderSyncAccount{OwnerID: 1, AccountID: "a", Cookie: "old", Proxy: "http://proxy.invalid:8080"}
	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		no := fmt.Sprintf("900000000000000000%d", i)
		value := fmt.Sprint(i)
		data, _ := json.Marshal(map[string]any{"is_multi_spec": true, "spec_name": "套餐", "spec_value": value, "text_content": "card-" + value})
		res, err := db.Exec(`INSERT INTO cards(user_id,name,type,data) VALUES(1,?,'text',?)`, value, string(data))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err = db.Exec(`INSERT INTO card_item_relations(card_id,item_id) VALUES(?,'item')`, id); err != nil {
			t.Fatal(err)
		}
		call := func(_ context.Context, o MTOPOptions) MTOPResult {
			if o.API != "mtop.idle.web.trade.order.detail" || o.Version != "1.0" || o.Data["tid"] != no || o.Cookie != a.Cookie || o.Proxy != a.Proxy {
				t.Fatalf("invalid detail request: %+v", o)
			}
			r := orderSpecResponse("套餐：" + value)
			r.Cookie = "fresh-" + value
			return r
		}
		created, err := SyncSoldOrder(ctx, db, call, &a, soldOrder(no), "ALL")
		if err != nil || !created {
			t.Fatal(created, err)
		}
		if a.Cookie != "fresh-"+value {
			t.Fatal("cookie not propagated")
		}
		var raw string
		if err = db.QueryRow(`SELECT data FROM xy_orders WHERE order_no=?`, no).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		selected, _, _, err := SelectDeliveryCard(ctx, db, 1, "a", "item", raw)
		if err != nil || selected != id {
			t.Fatalf("selected %d want %d: %v", selected, id, err)
		}
		unexpected := func(context.Context, MTOPOptions) MTOPResult {
			t.Fatal("complete order was hydrated twice")
			return MTOPResult{}
		}
		if _, err = SyncSoldOrder(ctx, db, unexpected, &a, soldOrder(no), "NOT_SHIP"); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{}`, `{"spec_name":"套餐","spec_value":"missing"}`} {
		if _, _, _, err := SelectDeliveryCard(ctx, db, 1, "a", "item", raw); err == nil {
			t.Fatal("unmatched specification selected a card")
		}
	}
}

func TestOrderDetailsRetryPreservesBaseAndIsolation(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','old'); INSERT INTO xy_orders(owner_id,account_id,order_no,status,data) VALUES(2,'b','9000000000000000002','completed','{"spec_name":"private"}'),(1,'a','9000000000000000003','completed','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	a := OrderSyncAccount{OwnerID: 1, AccountID: "a", Cookie: "old"}
	ctx := context.Background()
	calls := 0
	call := func(_ context.Context, o MTOPOptions) MTOPResult {
		calls++
		if calls == 1 {
			return MTOPResult{Error: "network unavailable", Cookie: "renewed"}
		}
		if o.Cookie != "renewed" {
			t.Fatal("retry lost cookie")
		}
		return orderSpecResponse("套餐:年卡")
	}
	no := "9000000000000000001"
	if created, err := SyncSoldOrder(ctx, db, call, &a, soldOrder(no), "ALL"); !created || err != nil {
		t.Fatal(created, err)
	}
	var status string
	if err = db.QueryRow(`SELECT status FROM xy_orders WHERE order_no=?`, no).Scan(&status); err != nil || status != "pending_ship" {
		t.Fatal("base order was lost", err)
	}
	if _, err = SyncSoldOrder(ctx, db, call, &a, soldOrder(no), "ALL"); err != nil || calls != 2 {
		t.Fatal("ALL did not retry missing spec", calls, err)
	}
	if _, err = SyncSoldOrder(ctx, db, call, &a, soldOrder(no), "NOT_SHIP"); err != nil || calls != 2 {
		t.Fatal("completed spec was hydrated again", calls, err)
	}
	if _, err = SyncSoldOrder(ctx, db, call, &a, soldOrder("9000000000000000002"), "ALL"); err == nil {
		t.Fatal("cross-account order accepted")
	}
	var raw string
	if err = db.QueryRow(`SELECT data FROM xy_orders WHERE order_no='9000000000000000002'`).Scan(&raw); err != nil || raw != `{"spec_name":"private"}` {
		t.Fatal("other account modified", err)
	}
	if _, err = SyncSoldOrder(ctx, db, call, &a, soldOrder("9000000000000000003"), "ALL"); err != nil || calls != 3 {
		t.Fatal("pending order with missing spec was not hydrated", calls, err)
	}
	malformed := orderSpecResponse("invalid spec")
	if _, err := parseOrderSpec(malformed.Response, "item", "buyer"); err == nil {
		t.Fatal("invalid spec accepted")
	}
	valid := orderSpecResponse("套餐:年卡")
	if _, err := parseOrderSpec(valid.Response, "other-item", "buyer"); err == nil {
		t.Fatal("wrong item accepted")
	}
}

func TestSingleSpecOrdersDoNotFetchDetailsForever(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "single.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := OrderSyncAccount{OwnerID: 1, AccountID: "a"}
	calls := 0
	call := func(context.Context, MTOPOptions) MTOPResult { calls++; return orderSpecResponse("") }
	for i := 0; i < 3; i++ {
		if _, err := SyncSoldOrder(context.Background(), db, call, &a, soldOrder("9000000000000000001"), "NOT_SHIP"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("single-spec detail calls: %d", calls)
	}
}

func TestOrderHydrationEligibilityAcrossSyncEntrypoints(t *testing.T) {
	for _, query := range []string{"ALL", "NOT_SHIP"} {
		for _, tc := range []struct {
			name, status, data   string
			newOrder, wantDetail bool
		}{
			{"new order", "待发货", `{}`, true, true},
			{"IM base record", "待发货", `{"chat_id":"chat"}`, false, true},
			{"missing value", "待发货", `{"spec_name":"套餐"}`, false, true},
			{"missing name", "待发货", `{"spec_value":"月卡"}`, false, true},
			{"retry incomplete detail", "待发货", `{"order_detail_fetched":true,"spec_name":"套餐"}`, false, true},
			{"complete spec", "待发货", `{"spec_name":"套餐","spec_value":"月卡"}`, false, false},
			{"known single spec", "待发货", `{"order_detail_fetched":true}`, false, false},
			{"completed history", "交易成功", `{}`, false, false},
			{"shipped history", "已发货", `{}`, false, false},
			{"cancelled history", "交易关闭", `{}`, false, false},
			{"refund in progress", "退款中", `{}`, false, false},
		} {
			t.Run(query+"/"+tc.name, func(t *testing.T) {
				db, err := database.Open(filepath.Join(t.TempDir(), "eligibility.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				no := "9000000000000000001"
				if !tc.newOrder {
					if _, err := db.Exec(`INSERT INTO xy_orders(owner_id,account_id,order_no,status,data) VALUES(1,'a',?,'pending_ship',?)`, no, tc.data); err != nil {
						t.Fatal(err)
					}
				}
				item := soldOrder(no)
				item["commonData"].(map[string]any)["orderStatus"] = tc.status
				calls := 0
				call := func(context.Context, MTOPOptions) MTOPResult { calls++; return orderSpecResponse("套餐:月卡") }
				a := OrderSyncAccount{OwnerID: 1, AccountID: "a"}
				created, err := SyncSoldOrder(context.Background(), db, call, &a, item, query)
				if err != nil || created != tc.newOrder || (calls == 1) != tc.wantDetail {
					t.Fatalf("created=%v calls=%d err=%v", created, calls, err)
				}
			})
		}
	}
}
