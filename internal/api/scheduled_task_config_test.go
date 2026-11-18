package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestPolishWindowConfigurationIsValidatedAndPreserved(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE scheduled_tasks SET data='{"existing_option":42}' WHERE task_code='polish'`); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	for _, tc := range []struct {
		code, query string
		status      int
	}{
		{"polish", "interval_seconds=90&run_start_time=25:00", 400},
		{"red_flower", "run_start_time=11:00", 400},
		{"missing", "enabled=true", 404},
		{"polish", "interval_seconds=90&run_start_time=11:00&run_end_time=12:30", 200},
		{"polish", "enabled=false", 200},
		{"polish", "interval_seconds=100&run_end_time=09:00", 400},
		{"db_backup", "retention_days=0", 400},
		{"db_backup", "retention_days=366", 400},
		{"db_backup", "retention_days=abc", 400},
		{"polish", "retention_days=7", 400},
		{"db_backup", "interval_seconds=1800&retention_days=14", 200},
		{"db_backup", "retention_days=7", 200},
	} {
		r := httptest.NewRequest("PUT", "/?"+tc.query, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("code", tc.code)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		s.scheduledTaskUpdate(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
	}
	var raw string
	var enabled bool
	var interval int
	if err := db.QueryRow(`SELECT data,enabled,interval_seconds FROM scheduled_tasks WHERE task_code='polish'`).Scan(&raw, &enabled, &interval); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal([]byte(raw), &cfg)
	if enabled || interval != 90 || cfg["run_start_time"] != "11:00" || cfg["run_end_time"] != "12:30" || cfg["existing_option"] != float64(42) {
		t.Fatalf("unexpected config: %s enabled=%v interval=%d", raw, enabled, interval)
	}
	var backupRaw string
	var backupInterval int
	if err := db.QueryRow(`SELECT data,interval_seconds FROM scheduled_tasks WHERE task_code='db_backup'`).Scan(&backupRaw, &backupInterval); err != nil {
		t.Fatal(err)
	}
	var backupCfg map[string]any
	_ = json.Unmarshal([]byte(backupRaw), &backupCfg)
	if backupInterval != 1800 || backupCfg["retention_days"] != float64(7) {
		t.Fatalf("unexpected db_backup config: %s interval=%d", backupRaw, backupInterval)
	}
}
