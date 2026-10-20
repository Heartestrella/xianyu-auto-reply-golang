package xianyu

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestConnectionDeviceAndTokenReuse(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "connection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO xy_accounts(id,owner_id,account_id,cookie) VALUES(1,1,'a','unb=123; session=old')`)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	a := Account{ID: 1, OwnerID: 1, AccountID: "a", Cookie: "unb=123; session=old"}
	ctx := context.Background()
	first, err := m.connectionDeviceID(ctx, a)
	if err != nil || first == "" {
		t.Fatal(first, err)
	}
	second, err := m.connectionDeviceID(ctx, a)
	if err != nil || second != first {
		t.Fatal("device changed on reconnect", err)
	}
	calls := 0
	m.tokenFetcher = func(context.Context, Account, string) (string, map[string]string, error) {
		calls++
		return "cached-token", nil, nil
	}
	for i := 0; i < 2; i++ {
		token, _, err := m.getConnectionToken(ctx, a, first)
		if err != nil || token != "cached-token" {
			t.Fatal(token, err)
		}
	}
	if calls != 1 {
		t.Fatalf("token endpoint called %d times", calls)
	}
	// A response for the old login must not overwrite a newly supplied Cookie.
	_, err = db.Exec(`UPDATE xy_accounts SET cookie='unb=123; session=fresh' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.saveConnectionCookies(ctx, &a, map[string]string{"_m_h5_tk": "old-request-token"}); err != nil {
		t.Fatal(err)
	}
	if a.Cookie != "unb=123; session=fresh" {
		t.Fatal("new login was overwritten")
	}
	old := a
	old.Cookie = "unb=123; session=old"
	if err := m.waitForConnectionAccountUpdate(ctx, old); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.waitForConnectionAccountUpdate(canceled, a); !errors.Is(err, context.Canceled) {
		t.Fatal("account wait does not cancel", err)
	}
	if m.connectionBlocked(a) {
		t.Fatal("cancelled wait left account blocked")
	}
	if _, err := m.connectionDeviceID(ctx, Account{}); err == nil {
		t.Fatal("missing account identity accepted")
	}
}

func TestVerificationFailureClassification(t *testing.T) {
	for message, want := range map[string]string{
		"FAIL_SYS_USER_VALIDATE,RGV587_ERROR::SM::哎哟喂,被挤爆啦": "platform_verification_required",
		"FAIL_SYS_SESSION_EXPIRED": "session_expired", "Cookie缺少unb": "invalid_cookie",
		"FAIL_SYS_TOKEN_EXPIRED": "token_expired", "network timeout": "connection_failed",
	} {
		if got := ConnectionFailureReason(errors.New(message)); got != want {
			t.Errorf("%s: %s", message, got)
		}
	}
}

func TestConnectionBlockWaitsWithoutRequestingTokens(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "blocked.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO xy_accounts(id,owner_id,account_id,cookie) VALUES(1,1,'a','unb=123')`)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	a := Account{ID: 1, OwnerID: 1, AccountID: "a", Cookie: "unb=123"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.waitForConnectionAccountUpdate(ctx, a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
