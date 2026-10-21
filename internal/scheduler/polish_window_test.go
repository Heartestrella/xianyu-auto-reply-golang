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

func TestPolishWindowUsesServerTimeAndInclusiveMinutes(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("server", 8*3600)
	defer func() { time.Local = old }()
	cfg := map[string]any{"run_start_time": "11:00", "run_end_time": "12:30"}
	for stamp, want := range map[string]bool{"2026-10-20T02:59:59Z": false, "2026-10-20T03:00:00Z": true, "2026-10-20T04:30:59Z": true, "2026-10-20T04:31:00Z": false} {
		now, _ := time.Parse(time.RFC3339, stamp)
		if got := canRunAutomatically("polish", cfg, now); got != want {
			t.Errorf("%s: %v", stamp, got)
		}
		if !canRunAutomatically("red_flower", cfg, now) {
			t.Fatal("other task restricted")
		}
		if !canRunAutomatically("polish", nil, now) {
			t.Fatal("legacy default restricted")
		}
	}
	for _, pair := range [][2]string{{"25:00", "23:59"}, {"1:00", "23:59"}, {"23:00", "01:00"}, {"", "23:59"}} {
		if ValidatePolishWindow(pair[0], pair[1]) == nil {
			t.Fatalf("invalid range accepted: %v", pair)
		}
	}
}

func TestManualPolishBypassesAutomaticWindow(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE scheduled_tasks SET enabled=0; UPDATE scheduled_tasks SET enabled=1,data='{"run_start_time":"invalid"}' WHERE task_code='polish'`)
	if err != nil {
		t.Fatal(err)
	}
	e := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	calls := make(chan struct{}, 2)
	e.handlers["polish"] = func(context.Context, map[string]any) error { calls <- struct{}{}; return nil }
	e.scan(context.Background())
	select {
	case <-calls:
		t.Fatal("automatic job ignored invalid window")
	case <-time.After(50 * time.Millisecond):
	}
	if err = e.Trigger(context.Background(), "polish"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	default:
		t.Fatal("manual task did not run")
	}
}
