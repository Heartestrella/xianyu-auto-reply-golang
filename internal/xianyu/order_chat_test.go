package xianyu

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestOrderChatRecoveryRetryAndIsolation(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES(1,'a','order','item','buyer','pending_ship','{"chat_id":"FAILED_buyer","spec_name":"套餐"}');
	INSERT INTO event_logs(owner_id,account_id,kind,message,data) VALUES
	(2,'b','incoming_message','','{"SenderID":"buyer","ChatID":"wrong-owner"}'),
	(1,'other','incoming_message','','{"SenderID":"buyer","ChatID":"wrong-account"}'),
	(1,'a','incoming_message','','{"SenderID":"buyer","ChatID":"wrong-item","ItemID":"another-item"}')`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	calls := 0
	create := func(_ context.Context, account, buyer, item string) (string, error) {
		calls++
		if account != "a" || buyer != "buyer" || item != "item" {
			t.Fatal("wrong recipient")
		}
		if calls == 1 {
			return "", errors.New("IM disconnected")
		}
		return "created-chat", nil
	}
	if _, err := EnsureOrderChat(ctx, db, 2, "a", "order", create); err == nil || calls != 0 {
		t.Fatal("cross-owner order accepted")
	}
	if _, err := EnsureOrderChat(ctx, db, 1, "a", "order", create); err == nil || !strings.Contains(err.Error(), "IM disconnected") {
		t.Fatal("missing actual failure", err)
	}
	if chat, err := EnsureOrderChat(ctx, db, 1, "a", "order", create); err != nil || chat != "created-chat" || calls != 2 {
		t.Fatal(chat, calls, err)
	}
	if _, err := EnsureOrderChat(ctx, db, 1, "a", "order", create); err != nil || calls != 2 {
		t.Fatal("recreated existing chat", err)
	}
	var spec string
	if err := db.QueryRow(`SELECT json_extract(data,'$.spec_name') FROM xy_orders`).Scan(&spec); err != nil || spec != "套餐" {
		t.Fatal("spec lost", err)
	}
	_, err = db.Exec(`UPDATE xy_orders SET data='{"chatId":"FAILED_buyer"}'; INSERT INTO event_logs(owner_id,account_id,kind,message,data) VALUES(1,'a','outgoing_message','','{"senderId":"self","toUserId":"buyer","cid":"history-chat"}')`)
	if err != nil {
		t.Fatal(err)
	}
	if chat, err := EnsureOrderChat(ctx, db, 1, "a", "order", create); err != nil || chat != "history-chat" || calls != 2 {
		t.Fatal("outgoing history not reused", chat, err)
	}
}

func TestRedeliveryRecoversChatWithoutSendingCardTwice(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "deliver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie,only_send_card) VALUES(1,'a','unb=123',1);
	INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data) VALUES(1,'a','order','item','buyer','pending_ship','{"chat_id":"FAILED_buyer"}');
	INSERT INTO event_logs(owner_id,account_id,kind,message,data) VALUES(1,'a','incoming_message','','{"SenderID":"buyer","ChatID":"history-chat","ItemID":"item"}');
	INSERT INTO cards(id,user_id,name,type,data) VALUES(1,1,'stock','data','{"data_content":"first\nsecond"}');
	INSERT INTO card_item_relations(card_id,item_id) VALUES(1,'item')`)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	sent := 0
	m.writers["a"] = &activeWriter{send: func(_ int, body []byte) error {
		sent++
		if !strings.Contains(string(body), "history-chat") {
			t.Fatal("invalid chat sent")
		}
		return nil
	}}
	for i := 0; i < 2; i++ {
		if err := m.Redeliver(context.Background(), "order"); err != nil {
			t.Fatal(err)
		}
	}
	if sent != 1 {
		t.Fatalf("sent %d cards", sent)
	}
	var stock string
	if err := db.QueryRow(`SELECT json_extract(data,'$.data_content') FROM cards WHERE id=1`).Scan(&stock); err != nil || stock != "second" {
		t.Fatal(stock, err)
	}
}
