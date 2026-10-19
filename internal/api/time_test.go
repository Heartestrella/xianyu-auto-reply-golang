package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestServerTimeAndSystemLogs(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("CST", 8*3600)
	t.Cleanup(func() { time.Local = previous })
	if got := databaseTimeInServerZone("2026-10-19 16:04:34"); got != "2026-10-20T00:04:34+08:00" {
		t.Fatal(got)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "time.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO event_logs(kind,level,message,created_at) VALUES('account_login','ERROR','failed','2026-10-19 16:04:34'),('account_login','INFO','connected','2026-10-19 16:04:35')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	w := httptest.NewRecorder()
	s.adminLogs(w, httptest.NewRequest("GET", "/?level=ERROR", nil))
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data) != 1 || body.Data[0]["created_at"] != "2026-10-20T00:04:34+08:00" || body.Data[0]["message"] != "failed" {
		t.Fatalf("invalid logs: %s err=%v", w.Body.String(), err)
	}
	if w.Header().Get("X-Server-UTC-Offset") != "480" || w.Header().Get("X-Server-Time") == "" {
		t.Fatal("missing server time headers")
	}
	query, args := appendDateFilters(httptest.NewRequest("GET", "/?start_date=2026-10-20&end_date=2026-10-20", nil), ` FROM event_logs WHERE 1=1`, nil)
	var count int
	if err := db.QueryRow(`SELECT count(*)`+query, args...).Scan(&count); err != nil || count != 2 {
		t.Fatalf("local date filter lost midnight records: %d err=%v", count, err)
	}
}
