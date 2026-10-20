package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestAccountDeletionIsScopedAndAtomic(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "rollback"}[rollback], func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "delete.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.Exec(`INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(1,'a','cookie'),(1,'b','cookie'),(2,'c','cookie');
			INSERT INTO xy_catalog_items(owner_id,account_id,item_id) VALUES(1,'a','unique'),(1,'a','shared'),(1,'b','shared'),(2,'c','unique');
			INSERT INTO cards(id,user_id,name,type) VALUES(1,1,'mine','text'),(2,2,'other','text');
			INSERT INTO card_item_relations(card_id,item_id,source) VALUES(1,'unique','own'),(1,'shared','own'),(2,'unique','own'),(1,'unique','dock');
			INSERT INTO xy_keyword_rules(owner_id,account_id,keyword) VALUES(1,'a','hello'),(2,'c','hello');
			INSERT INTO default_replies(owner_id,account_id) VALUES(1,'a'),(2,'c');
			INSERT INTO default_reply_records(owner_id,account_id,user_id) VALUES(1,'a','buyer'),(2,'c','buyer');
			INSERT INTO resource_records(owner_id,account_id,kind) VALUES(1,'a','ai_reply'),(2,'c','ai_reply');
			INSERT INTO xy_orders(owner_id,account_id,order_no) VALUES(1,'a','history')`)
			if err != nil {
				t.Fatal(err)
			}
			if rollback {
				if _, err = db.Exec(`CREATE TRIGGER reject_account_delete BEFORE DELETE ON xy_accounts BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err = (&Server{db: db}).deleteAccountData(context.Background(), 1, "a")
			if (err != nil) != rollback {
				t.Fatalf("rollback=%v err=%v", rollback, err)
			}
			counts := map[string]int{"xy_accounts": 2, "xy_catalog_items": 2, "card_item_relations": 3, "xy_keyword_rules": 1, "default_replies": 1, "default_reply_records": 1, "resource_records": 1, "xy_orders": 1}
			if rollback {
				counts["xy_accounts"] = 3
				counts["xy_catalog_items"] = 4
				counts["card_item_relations"] = 4
				for _, table := range []string{"xy_keyword_rules", "default_replies", "default_reply_records", "resource_records"} {
					counts[table] = 2
				}
			}
			for table, want := range counts {
				var got int
				if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
					t.Fatalf("%s got %d want %d: %v", table, got, want, err)
				}
			}
		})
	}
}
