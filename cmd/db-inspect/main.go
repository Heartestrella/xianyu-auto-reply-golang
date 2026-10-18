package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
	_ "modernc.org/sqlite"
)

func main() {
	path := "data/xianyu.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT created_at,level,message,COALESCE(data,'{}') FROM event_logs WHERE kind='qr_login' ORDER BY id DESC LIMIT 30`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var created, level, message, data string
		if rows.Scan(&created, &level, &message, &data) == nil {
			fmt.Printf("%s\t%s\t%s\t%s\n", created, level, message, data)
		}
	}
	_ = rows.Close()
	fmt.Println("-- accounts --")
	accounts, err := db.Query(`SELECT account_id,status,cookie,COALESCE(disable_reason,'') FROM xy_accounts ORDER BY id`)
	if err != nil {
		panic(err)
	}
	defer accounts.Close()
	for accounts.Next() {
		var accountID, status, cookie, reason string
		if accounts.Scan(&accountID, &status, &cookie, &reason) == nil {
			parsed := xianyu.ParseCookies(cookie)
			keys := make([]string, 0, len(parsed))
			for key := range parsed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			fmt.Printf("%s\tstatus=%s\tunb=%t\tcookie_keys=%v\tdisable=%s\n", accountID, status, parsed["unb"] != "", keys, reason)
		}
	}
	_ = accounts.Close()
	fmt.Println("-- recent account/item errors --")
	errors, err := db.Query(`SELECT created_at,kind,COALESCE(account_id,''),message,COALESCE(data,'{}') FROM event_logs WHERE kind IN ('account_login','fetch_items') OR level='ERROR' ORDER BY id DESC LIMIT 30`)
	if err != nil {
		panic(err)
	}
	defer errors.Close()
	for errors.Next() {
		var created, kind, account, message, data string
		if errors.Scan(&created, &kind, &account, &message, &data) == nil {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\n", created, kind, account, message, data)
		}
	}
	fmt.Println("-- recent messages and replies --")
	messages, err := db.Query(`SELECT id,created_at,kind,COALESCE(account_id,''),COALESCE(message,''),substr(COALESCE(data,'{}'),1,1200) FROM event_logs WHERE kind IN ('incoming_message','incoming_unparsed','auto_reply','blacklist_skip','outgoing_message') ORDER BY id DESC LIMIT 40`)
	if err != nil {
		panic(err)
	}
	defer messages.Close()
	for messages.Next() {
		var id int64
		var created, kind, account, message, data string
		if messages.Scan(&id, &created, &kind, &account, &message, &data) == nil {
			fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\n", id, created, kind, account, message, data)
		}
	}
	if len(os.Args) > 2 && os.Args[2] == "--check-items" {
		checkItems(db)
	}
	if len(os.Args) > 2 && os.Args[2] == "--unlock-admin" {
		result, execErr := db.Exec(`UPDATE xy_users SET login_fail_count=0,login_locked_until=NULL WHERE username='admin'`)
		if execErr != nil {
			fmt.Println("-- unlock admin --", execErr)
		} else {
			changed, _ := result.RowsAffected()
			fmt.Printf("-- unlock admin -- changed=%d\n", changed)
		}
	}
}

func checkItems(db *sql.DB) {
	var accountID, cookie string
	if err := db.QueryRow(`SELECT account_id,cookie FROM xy_accounts WHERE status='active' ORDER BY id LIMIT 1`).Scan(&accountID, &cookie); err != nil {
		fmt.Println("-- live items --", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{
		API: "mtop.alibaba.idle.seller.pc.common.item.search", Version: "1.0", Cookie: cookie,
		Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO",
		Data:         map[string]any{"pageNo": 1, "pageSize": 30, "bizType": "commonPro", "searchRequest": "{}", "itemStatus": "0"},
		ExtraParams:  map[string]string{"spm_cnt": "a21107h.42826273.0.0", "needLoginPC": "true", "accountSite": "xianyu"},
		ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""},
	})
	count := 0
	if outer, ok := result.Response["data"].(map[string]any); ok {
		if inner, ok := outer["data"].(map[string]any); ok {
			if items, ok := inner["itemSearchResponseList"].([]any); ok {
				count = len(items)
			}
		}
	}
	fmt.Printf("-- live items -- account=%s success=%t count=%d error=%s\n", accountID, result.Success, count, result.Error)
}
