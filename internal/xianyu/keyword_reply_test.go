package xianyu

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestKeywordReplyMultiline(t *testing.T) {
	for _, tc := range []struct {
		name, keywords, message, typ, item, incomingItem, wantText, wantImage string
	}{
		{name: "first keyword", keywords: "区别\n临时", message: "这两种有什么区别？", wantText: "matched"},
		{name: "second keyword", keywords: "区别\n临时", message: "临时套餐怎么用", wantText: "matched"},
		{name: "middle keyword", keywords: "不能用\n耗尽\n下载魔法", message: "额度耗尽了", wantText: "matched"},
		{name: "last keyword", keywords: "不能用\n耗尽\n下载魔法", message: "如何下载魔法", wantText: "matched"},
		{name: "windows newlines and blanks", keywords: " \r\n 区别 \r\n\r\n 临时 \r\n", message: "临时", wantText: "matched"},
		{name: "single keyword", keywords: "过期", message: "这个会过期吗", wantText: "matched"},
		{name: "no matching line", keywords: "区别\n临时", message: "你好", wantText: "fallback"},
		{name: "blank lines never match", keywords: "\n \r\n", message: "你好", wantText: "fallback"},
		{name: "image rule", keywords: "图片\n照片", message: "发张照片", typ: "image", wantImage: "https://example.invalid/reply.png"},
		{name: "product restriction", keywords: "区别\n临时", message: "临时", item: "product-a", incomingItem: "product-b", wantText: "fallback"},
		{name: "matching product", keywords: "区别\n临时", message: "临时", item: "product-a", incomingItem: "product-a", wantText: "matched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "reply.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			typ := tc.typ
			if typ == "" {
				typ = "text"
			}
			if _, err = db.Exec(`INSERT INTO xy_keyword_rules(owner_id,account_id,keyword,reply,item_id,type,extra) VALUES(1,'account',?,'matched',?,?,?)`, tc.keywords, tc.item, typ, `{"image_url":"https://example.invalid/reply.png"}`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO default_replies(owner_id,account_id,enabled,reply,data) VALUES(1,'account',1,'fallback','{"enabled":true,"reply_content":"fallback"}')`); err != nil {
				t.Fatal(err)
			}
			m := NewManager(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			got := m.matchReply(context.Background(), Account{OwnerID: 1, AccountID: "account"}, Incoming{Text: tc.message, ItemID: tc.incomingItem, SenderID: "buyer"})
			if got.Text != tc.wantText || got.ImageURL != tc.wantImage {
				t.Fatalf("got text=%q image=%q; want text=%q image=%q", got.Text, got.ImageURL, tc.wantText, tc.wantImage)
			}
		})
	}
}
