package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func newMonitorOrderTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "monitor-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{db: db}
}

func insertMonitorTestUser(t *testing.T, s *Server, id int64, role string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO xy_users(id,username,password_hash,role) VALUES(?,?,?,?)`, id, "u"+toTestString(id), "hash", role); err != nil {
		t.Fatal(err)
	}
}

func insertMonitorTestAccount(t *testing.T, s *Server, owner int64, id, cookie, status string) {
	t.Helper()
	if status == "" {
		status = "active"
	}
	if _, err := s.db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,status) VALUES(?,?,?,?)`, owner, id, cookie, status); err != nil {
		t.Fatal(err)
	}
}

func insertOrderFallback(t *testing.T, s *Server, owner int64, category any, ids []string) {
	t.Helper()
	data := map[string]any{"category_id": category, "account_ids": ids}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,'order_fallback_account',1,?)`, owner, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func insertMonitorTask(t *testing.T, s *Server, owner int64, data map[string]any) int64 {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,'listing_monitor_task',1,?)`, owner, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertMonitoredItem(t *testing.T, s *Server, owner, taskID int64, data map[string]any) int64 {
	t.Helper()
	data["monitor_task_id"] = taskID
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,'listing_monitor_item',1,?)`, owner, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func readItemString(t *testing.T, s *Server, itemID int64, path string) string {
	t.Helper()
	var value sql.NullString
	if err := s.db.QueryRow(`SELECT json_extract(data,?) FROM resource_records WHERE id=?`, path, itemID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if !value.Valid {
		return ""
	}
	return value.String
}

func readItemInt(t *testing.T, s *Server, itemID int64, path string) int {
	t.Helper()
	var value sql.NullInt64
	if err := s.db.QueryRow(`SELECT json_extract(data,?) FROM resource_records WHERE id=?`, path, itemID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if !value.Valid {
		return 0
	}
	return int(value.Int64)
}

func TestResolveMonitoredOrderAccountsPriorityDedupAndValidity(t *testing.T) {
	s := newMonitorOrderTestServer(t)
	insertMonitorTestUser(t, s, 1, "ADMIN")
	insertMonitorTestUser(t, s, 2, "ADMIN")
	insertMonitorTestUser(t, s, 10, "MEMBER")
	for _, account := range []struct {
		owner  int64
		id     string
		cookie string
		status string
	}{
		{10, "task", "cookie", "active"},
		{10, "user-cat", "cookie", "active"},
		{10, "user-global", "cookie", "active"},
		{1, "admin-cat", "cookie", "active"},
		{2, "admin-global", "cookie", "active"},
		{10, "disabled", "cookie", "disabled"},
		{10, "no-cookie", "", "active"},
	} {
		insertMonitorTestAccount(t, s, account.owner, account.id, account.cookie, account.status)
	}
	insertOrderFallback(t, s, 10, 7, []string{"user-cat", "user-cat"})
	insertOrderFallback(t, s, 10, nil, []string{"user-global"})
	insertOrderFallback(t, s, 1, 7, []string{"admin-cat", "user-global"})
	insertOrderFallback(t, s, 2, nil, []string{"admin-global", "user-cat"})

	got, err := s.resolveMonitoredOrderAccounts(context.Background(), 10, 7, []string{"task", "task", "disabled", "no-cookie", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	var taskIDs, fallbackIDs []string
	var fallbackOwners []int64
	for _, account := range got.task {
		taskIDs = append(taskIDs, account.AccountID)
	}
	for _, account := range got.fallback {
		fallbackIDs = append(fallbackIDs, account.AccountID)
		fallbackOwners = append(fallbackOwners, account.OwnerID)
	}
	if !reflect.DeepEqual(taskIDs, []string{"task"}) {
		t.Fatalf("task candidates = %v", taskIDs)
	}
	if !reflect.DeepEqual(fallbackIDs, []string{"user-cat", "user-global", "admin-cat", "admin-global"}) {
		t.Fatalf("fallback order = %v", fallbackIDs)
	}
	if !reflect.DeepEqual(fallbackOwners, []int64{10, 10, 1, 2}) {
		t.Fatalf("fallback owners = %v", fallbackOwners)
	}
	if len(got.unavailable) != 3 {
		t.Fatalf("unavailable candidates = %v", got.unavailable)
	}
}

func TestScheduledAutoOrderUsesCrossOwnerAdminFallback(t *testing.T) {
	s := newMonitorOrderTestServer(t)
	insertMonitorTestUser(t, s, 1, "ADMIN")
	insertMonitorTestUser(t, s, 10, "MEMBER")
	insertMonitorTestAccount(t, s, 1, "admin-order", "cookie", "active")
	insertOrderFallback(t, s, 1, 5, []string{"admin-order"})
	taskID := insertMonitorTask(t, s, 10, map[string]any{"order_account_ids": []string{}, "category_id": 5, "order_batch_size": 1})
	itemID := insertMonitoredItem(t, s, 10, taskID, map[string]any{"item_id": "item-1", "is_ordered": false})
	usedOwner := int64(0)
	s.orderPlace = func(_ context.Context, account platformAccount, _ string) (string, bool, error) {
		usedOwner = account.OwnerID
		if account.AccountID != "admin-order" {
			t.Fatalf("unexpected account %q", account.AccountID)
		}
		return "order-1", false, nil
	}
	if err := s.ExecuteScheduledAutoOrder(context.Background()); err != nil {
		t.Fatal(err)
	}
	if usedOwner != 1 {
		t.Fatalf("order account owner = %d, want admin owner 1", usedOwner)
	}
	if status := readItemString(t, s, itemID, "$.order_status"); status != "success" {
		t.Fatalf("order status = %q", status)
	}
	if accountID := readItemString(t, s, itemID, "$.order_account_id"); accountID != "admin-order" {
		t.Fatalf("stored order account = %q", accountID)
	}
}

func TestScheduledDMStrictBindingNeverFallsBack(t *testing.T) {
	s := newMonitorOrderTestServer(t)
	insertMonitorTestUser(t, s, 10, "MEMBER")
	insertMonitorTestAccount(t, s, 10, "strict", "cookie", "active")
	insertMonitorTestAccount(t, s, 10, "pool", "cookie", "active")
	taskID := insertMonitorTask(t, s, 10, map[string]any{"dm_content": "hello", "order_account_ids": []string{"pool"}, "dm_batch_size": 5})
	strictItem := insertMonitoredItem(t, s, 10, taskID, map[string]any{"item_id": "strict-item", "seller_user_id": "seller", "order_status": "success", "order_account_id": "strict", "is_dm_sent": false, "dm_attempts": 1})
	deletedItem := insertMonitoredItem(t, s, 10, taskID, map[string]any{"item_id": "deleted-item", "seller_user_id": "seller", "order_status": "success", "order_account_id": "deleted", "is_dm_sent": false, "dm_attempts": 0})
	var used []string
	s.online = func(string) bool { return true }
	s.chatCreate = func(_ context.Context, accountID, _, _ string) (string, error) {
		used = append(used, accountID)
		return "chat", nil
	}
	s.chatSend = func(_ context.Context, accountID, _, _, _ string) (string, error) {
		used = append(used, accountID)
		return "message", nil
	}
	if err := s.ExecuteScheduledDMSend(context.Background()); err == nil {
		t.Fatal("deleted strict account should be reported")
	}
	if !reflect.DeepEqual(used, []string{"strict", "strict"}) {
		t.Fatalf("accounts used = %v; strict item must not switch to pool", used)
	}
	if status := readItemString(t, s, strictItem, "$.dm_status"); status != "success" {
		t.Fatalf("strict DM status = %q", status)
	}
	if status := readItemString(t, s, deletedItem, "$.dm_status"); status != "failed" || readItemInt(t, s, deletedItem, "$.dm_attempts") != 3 {
		t.Fatalf("deleted strict account status=%q attempts=%d", status, readItemInt(t, s, deletedItem, "$.dm_attempts"))
	}
}

func TestScheduledDMHistoricalPoolSkipsOfflineAndWaitsWhenAllUnavailable(t *testing.T) {
	s := newMonitorOrderTestServer(t)
	insertMonitorTestUser(t, s, 1, "ADMIN")
	insertMonitorTestUser(t, s, 10, "MEMBER")
	insertMonitorTestAccount(t, s, 10, "offline", "cookie", "active")
	insertMonitorTestAccount(t, s, 1, "admin-online", "cookie", "active")
	insertOrderFallback(t, s, 1, nil, []string{"admin-online"})
	taskID := insertMonitorTask(t, s, 10, map[string]any{"dm_content": "hello", "order_account_ids": []string{"offline"}})
	itemID := insertMonitoredItem(t, s, 10, taskID, map[string]any{"item_id": "legacy-item", "seller_user_id": "seller", "order_status": "success", "is_dm_sent": false, "dm_attempts": 1})
	s.online = func(accountID string) bool { return accountID == "admin-online" }
	used := ""
	s.chatCreate = func(_ context.Context, accountID, _, _ string) (string, error) {
		used = accountID
		return "chat", nil
	}
	s.chatSend = func(_ context.Context, accountID, _, _, _ string) (string, error) {
		used = accountID
		return "message", nil
	}
	if err := s.ExecuteScheduledDMSend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if used != "admin-online" || readItemString(t, s, itemID, "$.dm_status") != "success" {
		t.Fatalf("historical candidate did not rotate to admin fallback: used=%q status=%q", used, readItemString(t, s, itemID, "$.dm_status"))
	}

	// A second historical item has only offline candidates; it remains retryable
	// and does not consume the bounded send-attempt counter.
	allOfflineTask := insertMonitorTask(t, s, 10, map[string]any{"dm_content": "hello", "order_account_ids": []string{"offline"}})
	allOfflineItem := insertMonitoredItem(t, s, 10, allOfflineTask, map[string]any{"item_id": "waiting-item", "seller_user_id": "seller", "order_status": "success", "is_dm_sent": false, "dm_attempts": 1})
	// Disable the admin fallback for this item's category by assigning a category
	// with no admin configuration, leaving just the offline task account.
	if _, err := s.db.Exec(`UPDATE resource_records SET data=json_set(data,'$.category_id',99) WHERE id=?`, allOfflineTask); err != nil {
		t.Fatal(err)
	}
	s.online = func(string) bool { return false }
	if err := s.ExecuteScheduledDMSend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := readItemString(t, s, allOfflineItem, "$.dm_status"); status != "waiting" || readItemInt(t, s, allOfflineItem, "$.dm_attempts") != 1 {
		t.Fatalf("all-unavailable historical item status=%q attempts=%d", status, readItemInt(t, s, allOfflineItem, "$.dm_attempts"))
	}
}

func TestScheduledDMStrictOfflineWaitsWithoutAttemptOrFallback(t *testing.T) {
	s := newMonitorOrderTestServer(t)
	insertMonitorTestUser(t, s, 10, "MEMBER")
	insertMonitorTestAccount(t, s, 10, "strict", "cookie", "active")
	insertMonitorTestAccount(t, s, 10, "pool", "cookie", "active")
	taskID := insertMonitorTask(t, s, 10, map[string]any{"dm_content": "hello", "order_account_ids": []string{"pool"}})
	itemID := insertMonitoredItem(t, s, 10, taskID, map[string]any{"item_id": "offline-strict", "seller_user_id": "seller", "order_status": "success", "order_account_id": "strict", "is_dm_sent": false, "dm_attempts": 2})
	s.online = func(string) bool { return false }
	s.chatCreate = func(context.Context, string, string, string) (string, error) {
		t.Fatal("offline strict account must not be replaced by a pool account")
		return "", errors.New("unexpected call")
	}
	s.chatSend = func(context.Context, string, string, string, string) (string, error) {
		t.Fatal("chat send should not be called for an offline strict account")
		return "", errors.New("unexpected call")
	}
	if err := s.ExecuteScheduledDMSend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := readItemString(t, s, itemID, "$.dm_status"); status != "waiting" || readItemInt(t, s, itemID, "$.dm_attempts") != 2 {
		t.Fatalf("offline strict item status=%q attempts=%d", status, readItemInt(t, s, itemID, "$.dm_attempts"))
	}
}

func toTestString(value int64) string {
	return fmt.Sprintf("%d", value)
}
