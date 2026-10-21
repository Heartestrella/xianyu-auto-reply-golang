package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func pendingSoldPage(page int) xianyu.MTOPResult {
	no := fmt.Sprintf("900000000000000000%d", page)
	return xianyu.MTOPResult{Success: true, Response: map[string]any{"data": map[string]any{"module": map[string]any{
		"nextPage": page == 1, "items": []any{map[string]any{
			"commonData":  map[string]any{"orderId": json.Number(no), "itemId": "item", "orderStatus": "待发货"},
			"buyerInfoVO": map[string]any{"buyerId": "buyer"},
		}},
	}}}}
}

func TestScheduledOrderSyncUsesMerchantFiltersAndRetriesDetails(t *testing.T) {
	for _, code := range []string{"ALL", "NOT_SHIP"} {
		t.Run(code, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "sync.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,proxy_type,proxy_host,proxy_port) VALUES(1,'a','initial','http','proxy.invalid',8080);
			INSERT INTO xy_accounts(owner_id,account_id,cookie,status) VALUES(1,'disabled','do-not-use','inactive');
			INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES
			(1,'a','9000000000000000001','item','buyer','pending_ship','{"chat_id":"from-im"}'),
			(1,'a','9000000000000000002','item','buyer','pending_ship','{"spec_name":"套餐"}')`)
			if err != nil {
				t.Fatal(err)
			}
			e := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
			pass, page, details := 0, 0, 0
			expectedCookie := "initial"
			e.mtopCall = func(_ context.Context, opt xianyu.MTOPOptions) xianyu.MTOPResult {
				if opt.Cookie != expectedCookie || opt.Proxy != "http://proxy.invalid:8080" {
					t.Fatalf("account cookie/proxy was not carried to next request")
				}
				if opt.API == "mtop.taobao.idle.trade.merchant.sold.get" {
					page++
					if opt.Data["queryCode"] != code || opt.Data["pageNumber"] != page || opt.Data["rowsPerPage"] != 30 {
						t.Fatalf("invalid order request: %v", opt.Data)
					}
					res := pendingSoldPage(page)
					expectedCookie = fmt.Sprintf("list-%d-%d", pass, page)
					res.Cookie = expectedCookie
					return res
				}
				if opt.API != "mtop.idle.web.trade.order.detail" {
					t.Fatalf("unexpected API: %s", opt.API)
				}
				details++
				expectedCookie = fmt.Sprintf("detail-%d", details)
				if pass == 0 && page == 1 {
					return xianyu.MTOPResult{Error: "temporary failure", Cookie: expectedCookie}
				}
				return xianyu.MTOPResult{Success: true, Cookie: expectedCookie, Response: map[string]any{"data": map[string]any{"components": []any{map[string]any{"render": "orderInfoVO", "data": map[string]any{"itemInfo": map[string]any{"skuInfo": "套餐:月卡"}}}}}}}
			}
			for pass = 0; pass < 2; pass++ {
				page = 0
				if code == "NOT_SHIP" {
					err = e.fetchPendingOrders(context.Background(), nil)
				} else {
					err = e.fetchOrders(context.Background(), nil)
				}
				if err != nil || page != 2 {
					t.Fatalf("page=%d error=%v", page, err)
				}
				var count int
				if err = db.QueryRow(`SELECT count(*) FROM xy_orders WHERE json_extract(data,'$.spec_value')='月卡'`).Scan(&count); err != nil || count != pass+1 {
					t.Fatalf("hydrated=%d err=%v", count, err)
				}
			}
			if details != 3 {
				t.Fatalf("unexpected detail calls: %d", details)
			}
			var chat string
			if err = db.QueryRow(`SELECT json_extract(data,'$.chat_id') FROM xy_orders WHERE order_no='9000000000000000001'`).Scan(&chat); err != nil || chat != "from-im" {
				t.Fatal("IM data lost", err)
			}
		})
	}
}

func TestEmptyPendingOrderSyncStopsAfterFirstPage(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','cookie')`); err != nil {
		t.Fatal(err)
	}
	e := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	calls := 0
	e.mtopCall = func(_ context.Context, opt xianyu.MTOPOptions) xianyu.MTOPResult {
		calls++
		if opt.API != "mtop.taobao.idle.trade.merchant.sold.get" || opt.Data["queryCode"] != "NOT_SHIP" || opt.Data["pageNumber"] != 1 {
			t.Fatalf("invalid empty query: %v", opt.Data)
		}
		return xianyu.MTOPResult{Success: true, Response: map[string]any{"data": map[string]any{"module": map[string]any{"nextPage": false, "totalCount": 0, "items": []any{}}}}}
	}
	if err = e.fetchPendingOrders(context.Background(), nil); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
}
