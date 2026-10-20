package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func TestRedFlowerV1AndOrderState(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "failure"
		if success {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "flower.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,status,auto_red_flower) VALUES
				(1,'active','old-cookie','active',1),(1,'disabled','disabled-cookie','inactive',1),(1,'off','off-cookie','active',0);
				INSERT INTO xy_orders(owner_id,account_id,order_no,status,data) VALUES
				(1,'active','9000000000000000001','completed','{}'),
				(1,'active','already-requested','completed','{"is_red_flower":true}'),
				(1,'active','cancelled','cancelled','{}'),
				(1,'active','refunding','refunding','{}'),
				(1,'disabled','disabled-order','completed','{}'),
				(1,'off','off-order','completed','{}'),
				(2,'active','other-owner','completed','{}');
				INSERT INTO xy_orders(owner_id,account_id,order_no,status,created_at) VALUES(1,'active','too-old','completed',datetime('now','-11 days'))`)
			if err != nil {
				t.Fatal(err)
			}
			e := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
			calls := 0
			e.mtopCall = func(_ context.Context, options xianyu.MTOPOptions) xianyu.MTOPResult {
				calls++
				if options.API != "mtop.taobao.idlemessage.red.flower" || options.Version != "1.0" || options.ExtraParams["v"] != "" || options.Data["orderId"] != "9000000000000000001" || options.Data["channel"] != "list" || options.Cookie != "old-cookie" {
					t.Fatalf("incorrect flower request: api=%s version=%s data=%v", options.API, options.Version, options.Data)
				}
				result := xianyu.MTOPResult{Success: success, Cookie: "updated-cookie"}
				if !success {
					result.Error = "FAIL_SYS_API_NOT_FOUND"
				}
				return result
			}
			err = e.redFlower(context.Background(), nil)
			if (err == nil) != success || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			var requested int
			if err := db.QueryRow(`SELECT COALESCE(json_extract(data,'$.is_red_flower'),0) FROM xy_orders WHERE order_no='9000000000000000001'`).Scan(&requested); err != nil || (requested == 1) != success {
				t.Fatalf("incorrect request state: %d err=%v", requested, err)
			}
			var cookie, raw string
			if err := db.QueryRow(`SELECT cookie FROM xy_accounts WHERE account_id='active'`).Scan(&cookie); err != nil || cookie != "updated-cookie" {
				t.Fatal("refreshed cookie not persisted", err)
			}
			if err := db.QueryRow(`SELECT data FROM event_logs WHERE kind='scheduled_red_flower'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var log map[string]any
			if err := json.Unmarshal([]byte(raw), &log); err != nil || log["success"] != success || log["order_no"] != "9000000000000000001" {
				t.Fatalf("invalid result log: %s err=%v", raw, err)
			}
			if !success && log["error"] != "FAIL_SYS_API_NOT_FOUND" {
				t.Fatal("failure reason was lost")
			}
			if success {
				if err := e.redFlower(context.Background(), nil); err != nil || calls != 1 {
					t.Fatal("already requested order was sent again", err)
				}
			}
		})
	}
}

func TestRedFlowerAfterShipmentAndMigration(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "shipment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`ALTER TABLE xy_accounts DROP COLUMN red_flower_after_shipment`); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,auto_red_flower,red_flower_after_shipment) VALUES(1,'a','cookie',1,1);
	INSERT INTO xy_orders(owner_id,account_id,order_no,status) VALUES(1,'a','unpaid','pending_payment'),(1,'a','waiting','pending_ship'),(1,'a','shipped','shipped'),(1,'a','complete','completed'),(1,'a','cancelled','cancelled')`)
	if err != nil {
		t.Fatal(err)
	}
	e := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seen := map[string]bool{}
	e.mtopCall = func(_ context.Context, o xianyu.MTOPOptions) xianyu.MTOPResult {
		seen[o.Data["orderId"].(string)] = true
		return xianyu.MTOPResult{Success: true}
	}
	if err = e.redFlower(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !seen["shipped"] || !seen["complete"] {
		t.Fatalf("unexpected orders: %v", seen)
	}
	if _, err = db.Exec(`UPDATE xy_accounts SET red_flower_after_shipment=0`); err != nil {
		t.Fatal(err)
	}
	if err = e.redFlower(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 || !seen["unpaid"] || !seen["waiting"] {
		t.Fatalf("normal mode changed: %v", seen)
	}
}
