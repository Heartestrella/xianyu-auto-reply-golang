package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestRedeliveryFailureRetriesAfterCooldown(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,scheduled_redelivery,only_send_card) VALUES(1,'a','cookie',1,1);
	INSERT INTO xy_orders(owner_id,account_id,order_no,status) VALUES(1,'a','order','pending_ship')`)
	if err != nil {
		t.Fatal(err)
	}
	e := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	calls := 0
	e.SetRedelivery(func(context.Context, string) error {
		calls++
		if calls == 1 {
			return errors.New("connection temporarily unavailable")
		}
		return nil
	})
	ctx := context.Background()
	if err = e.redelivery(ctx, nil); err == nil || calls != 1 {
		t.Fatal(calls, err)
	}
	if err = e.redelivery(ctx, nil); err != nil || calls != 1 {
		t.Fatal("cooldown ignored", calls, err)
	}
	if _, err = db.Exec(`UPDATE xy_orders SET data=json_set(data,'$.last_redelivery_attempt',strftime('%s','now')-121)`); err != nil {
		t.Fatal(err)
	}
	if err = e.redelivery(ctx, nil); err != nil || calls != 2 {
		t.Fatal("failure never retried", calls, err)
	}
}
