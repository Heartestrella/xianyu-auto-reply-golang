package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestAccountLoginLogsExplainLegacyFailures(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "login.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,username) VALUES(1,'a','cookie','test-user'); INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(1,'account_login','a','ERROR','token获取失败: FAIL_SYS_USER_VALIDATE,RGV587_ERROR','{"login_status":"failed","error":"FAIL_SYS_USER_VALIDATE,RGV587_ERROR"}')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), userKey{}, User{ID: 1, Role: "ADMIN"}))
	w := httptest.NewRecorder()
	s.accountLoginLogs(w, r)
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body.Data) != 1 {
		t.Fatalf("%s err=%v", w.Body.String(), err)
	}
	log := body.Data[0]
	if log["account_status"] != "active" || log["username"] != "test-user" || log["error_message"] == "" || log["failure_reason"] != "platform_verification_required" || log["trigger_reason"] == "" {
		t.Fatalf("missing diagnostic fields: %v", log)
	}
}
