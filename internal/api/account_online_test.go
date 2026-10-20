package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestAccountOnlineUsesPlatformConnection(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "online.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','cookie'); INSERT INTO event_logs(owner_id,kind,account_id,level,message) VALUES(1,'account_login','a','ERROR','FAIL_SYS_USER_VALIDATE')`)
	if err != nil {
		t.Fatal(err)
	}
	hub := &Hub{clients: map[string]map[*websocket.Conn]struct{}{"a": {nil: {}}}}
	connected := false
	s := &Server{db: db, ws: hub, online: func(string) bool { return connected }}
	for _, tc := range []struct {
		online bool
		filter string
		total  int
	}{{false, "", 1}, {false, "&online=true", 0}, {false, "&online=false", 1}, {true, "&online=true", 1}} {
		connected = tc.online
		r := httptest.NewRequest("GET", "/?page=1"+tc.filter, nil)
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: "ADMIN"}))
		w := httptest.NewRecorder()
		s.accountList(w, r)
		var body struct {
			Total int `json:"total"`
			Data  []struct {
				Online bool   `json:"online"`
				Error  string `json:"connection_error"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Total != tc.total || len(body.Data) != tc.total {
			t.Fatalf("invalid response: %d %s err=%v", w.Code, w.Body.String(), err)
		}
		if tc.total > 0 && (body.Data[0].Online != tc.online || (!tc.online && body.Data[0].Error == "") || (tc.online && body.Data[0].Error != "")) {
			t.Fatalf("incorrect online state: %s", w.Body.String())
		}
	}
}
