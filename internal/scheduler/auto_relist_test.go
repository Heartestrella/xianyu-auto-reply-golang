package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func newRelistEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "relist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// seedRelistBase 写入一个启用规则、其监听商品、账号与两条已发货自动发货订单。
func seedRelistBase(t *testing.T, e *Engine, ruleStatus string) {
	t.Helper()
	_, err := e.db.Exec(`
		INSERT INTO xy_accounts(owner_id,account_id,cookie,status) VALUES(1,'a','cookie','active');
		INSERT INTO product_materials(id,owner_id,title,description,price,data) VALUES(1,1,'素材','','0','{}');
		INSERT INTO cards(id,user_id,name,type,enabled,data) VALUES(1,1,'卡券','card',1,'{}');
		INSERT INTO xy_auto_relist_rules(id,user_id,material_id,account_id,current_item_id,card_id,enabled,delay_seconds,status,version)
		  VALUES(1,1,1,'a','item-1',1,1,60,?,0);
		INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,delivery_method,data) VALUES
		  (1,'a','o-1','item-1','buyer','shipped','auto','{"delivery_content":"卡密A"}'),
		  (1,'a','o-2','item-1','buyer','shipped','auto','{"delivery_content":"卡密B"}')`, ruleStatus)
	if err != nil {
		t.Fatal(err)
	}
}

func relistEventCount(e *Engine, t *testing.T) int {
	t.Helper()
	var count int
	if err := e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM xy_auto_relist_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// 同一商品已有未终结事件时，发现阶段不得再为后续订单创建并发续售事件。
func TestRelistDiscoverySkipsActiveEvent(t *testing.T) {
	e := newRelistEngine(t)
	seedRelistBase(t, e, "active")

	ctx := context.Background()
	if err := e.discoverRelistEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if got := relistEventCount(e, t); got != 1 {
		t.Fatalf("首次发现应为 1 条事件，实际 %d", got)
	}
	// 第二次扫描时已有活跃 pending 事件，第二条合格订单不应再触发新事件。
	if err := e.discoverRelistEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if got := relistEventCount(e, t); got != 1 {
		t.Fatalf("活跃事件去重后仍应为 1 条事件，实际 %d", got)
	}
}

// status=error 的规则不应被发现阶段反复扫描触发。
func TestRelistDiscoveryIgnoresErrorRule(t *testing.T) {
	e := newRelistEngine(t)
	seedRelistBase(t, e, "error")

	if err := e.discoverRelistEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := relistEventCount(e, t); got != 0 {
		t.Fatalf("error 规则不应产生事件，实际 %d", got)
	}
}

// 旧商品仍在售时事件暂缓回 pending 并延后确认，规则置 waiting，不消耗重试次数。
func TestRelistDeferWhileLive(t *testing.T) {
	e := newRelistEngine(t)
	seedRelistBase(t, e, "active")

	const token = "claim-token"
	_, err := e.db.Exec(`INSERT INTO xy_auto_relist_events(id,user_id,rule_id,material_id,account_id,order_no,old_item_id,status,claim_token,attempt_count,publish_state)
		VALUES(1,1,1,1,'a','o-1','item-1','claimed',?,0,'started')`, token)
	if err != nil {
		t.Fatal(err)
	}

	ev := relistEvent{ID: 1, UserID: 1, RuleID: 1, MaterialID: 1, AccountID: "a", OrderNo: "o-1", OldItemID: "item-1"}
	if err := e.deferRelistWhileLive(context.Background(), ev, token); err != nil {
		t.Fatal(err)
	}

	var status, tokenDB string
	var nextRetry string
	if err := e.db.QueryRowContext(context.Background(), `SELECT status,COALESCE(claim_token,''),COALESCE(next_retry_at,'') FROM xy_auto_relist_events WHERE id=1`).Scan(&status, &tokenDB, &nextRetry); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("事件应回 pending，实际 %s", status)
	}
	if tokenDB != "" {
		t.Fatal("暂缓后应释放租约")
	}
	next, err := time.Parse("2006-01-02 15:04:05", nextRetry)
	if err != nil || !next.After(time.Now()) {
		t.Fatalf("next_retry_at 应为未来时间，实际 %q", nextRetry)
	}

	var ruleStatus string
	if err := e.db.QueryRowContext(context.Background(), `SELECT status FROM xy_auto_relist_rules WHERE id=1`).Scan(&ruleStatus); err != nil {
		t.Fatal(err)
	}
	if ruleStatus != "waiting" {
		t.Fatalf("规则应置 waiting，实际 %s", ruleStatus)
	}
}
