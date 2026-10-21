package database

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
)

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		return nil, err
	}
	return db, Migrate(context.Background(), db)
}

func Migrate(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_users(id INTEGER PRIMARY KEY AUTOINCREMENT, external_id TEXT, username TEXT NOT NULL UNIQUE, email TEXT UNIQUE, phone TEXT, password_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'ACTIVE', role TEXT NOT NULL DEFAULT 'MEMBER', account_limit INTEGER, last_login_at TEXT, login_fail_count INTEGER NOT NULL DEFAULT 0, login_locked_until TEXT, dock_code TEXT UNIQUE, secret_key TEXT UNIQUE, expire_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_accounts(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT NOT NULL UNIQUE, display_name TEXT, unb TEXT, cookie TEXT NOT NULL, login_method TEXT NOT NULL DEFAULT 'cookie', status TEXT NOT NULL DEFAULT 'active', username TEXT, login_password TEXT, remark TEXT, pause_duration INTEGER DEFAULT 10, auto_confirm INTEGER DEFAULT 0, show_browser INTEGER DEFAULT 0, metadata TEXT, last_login_at TEXT, last_refresh_at TEXT, proxy_type TEXT DEFAULT 'none', proxy_host TEXT, proxy_port INTEGER, proxy_user TEXT, proxy_pass TEXT, message_expire_time INTEGER DEFAULT 3600, reply_delay_seconds INTEGER DEFAULT 0, scheduled_redelivery INTEGER DEFAULT 0, scheduled_rate INTEGER DEFAULT 0, auto_polish INTEGER DEFAULT 0, confirm_before_send INTEGER DEFAULT 0, send_before_confirm INTEGER DEFAULT 0, only_send_card INTEGER DEFAULT 0, auto_red_flower INTEGER DEFAULT 0, red_flower_after_shipment INTEGER NOT NULL DEFAULT 0, ai_reply_block_ordered_users INTEGER DEFAULT 0, delivery_disabled INTEGER DEFAULT 0, delivery_disabled_reason TEXT, auto_close_order INTEGER DEFAULT 0, delivery_only_card_after_close INTEGER DEFAULT 0, delivery_disabled_excluded_items TEXT DEFAULT '[]', disable_reason TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_keyword_rules(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT NOT NULL, keyword TEXT NOT NULL, reply TEXT DEFAULT '', item_id TEXT DEFAULT '', type TEXT DEFAULT 'text', extra TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_catalog_items(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT NOT NULL, item_id TEXT NOT NULL, title TEXT, description TEXT, price TEXT, status TEXT, data TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, UNIQUE(account_id,item_id))`,
		`CREATE TABLE IF NOT EXISTS xy_orders(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT, order_no TEXT NOT NULL UNIQUE, item_id TEXT, buyer_id TEXT, status TEXT, delivery_method TEXT, data TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS cards(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL, enabled INTEGER DEFAULT 1, data TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS card_item_relations(id INTEGER PRIMARY KEY AUTOINCREMENT, card_id INTEGER NOT NULL, item_id TEXT NOT NULL, source TEXT DEFAULT 'own', dock_record_id INTEGER, UNIQUE(card_id,item_id,source))`,
		`CREATE TABLE IF NOT EXISTS default_replies(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT NOT NULL, reply TEXT DEFAULT '', enabled INTEGER DEFAULT 1, data TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS default_reply_records(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT NOT NULL, item_id TEXT NOT NULL DEFAULT '', user_id TEXT NOT NULL, created_at TEXT DEFAULT CURRENT_TIMESTAMP, UNIQUE(owner_id,account_id,item_id,user_id))`,
		`CREATE TABLE IF NOT EXISTS system_settings(key TEXT PRIMARY KEY, value TEXT, description TEXT, is_public INTEGER DEFAULT 0, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS user_settings(owner_id INTEGER NOT NULL, key TEXT NOT NULL, value TEXT, description TEXT, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(owner_id,key))`,
		`CREATE TABLE IF NOT EXISTS scheduled_tasks(task_code TEXT PRIMARY KEY, enabled INTEGER DEFAULT 1, cron TEXT, interval_seconds INTEGER, last_run_at TEXT, next_run_at TEXT, data TEXT DEFAULT '{}')`,
		`CREATE TABLE IF NOT EXISTS cookie_refresh_schedules(account_id TEXT PRIMARY KEY, expire_at TEXT NOT NULL, last_refresh_at TEXT, last_status TEXT NOT NULL DEFAULT 'initialized', last_error_message TEXT, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS event_logs(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER, kind TEXT NOT NULL, account_id TEXT, level TEXT DEFAULT 'INFO', message TEXT, data TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS resource_records(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, kind TEXT NOT NULL, account_id TEXT, enabled INTEGER DEFAULT 1, data TEXT NOT NULL DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS verification_codes(id INTEGER PRIMARY KEY AUTOINCREMENT,email TEXT NOT NULL,code TEXT NOT NULL,purpose TEXT NOT NULL,used INTEGER DEFAULT 0,expires_at TEXT NOT NULL,created_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS captcha_sessions(session_id TEXT PRIMARY KEY, code_hash TEXT NOT NULL, verified INTEGER DEFAULT 0, attempts INTEGER DEFAULT 0, expires_at TEXT NOT NULL, created_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS fund_flows(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, operator_id INTEGER, flow_type TEXT NOT NULL, amount TEXT NOT NULL, balance_before TEXT NOT NULL, balance_after TEXT NOT NULL, remark TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS product_materials(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL, price TEXT NOT NULL, category TEXT, data TEXT NOT NULL DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS publish_logs(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL, account_id TEXT, material_id INTEGER, batch_id TEXT, status TEXT NOT NULL, item_id TEXT, item_url TEXT, message TEXT, request_data TEXT DEFAULT '{}', response_data TEXT DEFAULT '{}', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_auto_relist_rules(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, material_id INTEGER NOT NULL UNIQUE, account_id TEXT NOT NULL, current_item_id TEXT NOT NULL, card_id INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 0, delay_seconds INTEGER NOT NULL DEFAULT 60, status TEXT NOT NULL DEFAULT 'disabled', version INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 0, next_retry_at TEXT, last_order_no TEXT, last_order_id INTEGER, last_order_updated_at TEXT, last_old_item_id TEXT, last_new_item_id TEXT, last_error TEXT, paused_reason TEXT, last_relisted_at TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_auto_relist_events(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, rule_id INTEGER NOT NULL, material_id INTEGER NOT NULL, account_id TEXT NOT NULL, order_no TEXT NOT NULL, old_item_id TEXT NOT NULL, new_item_id TEXT, status TEXT NOT NULL DEFAULT 'pending', attempt_count INTEGER NOT NULL DEFAULT 0, next_retry_at TEXT, error_message TEXT, claim_token TEXT, claimed_at TEXT, lease_expires_at TEXT, publish_request_id TEXT UNIQUE, publish_state TEXT NOT NULL DEFAULT 'not_started', publish_item_id TEXT, last_checked_at TEXT, result_unknown INTEGER NOT NULL DEFAULT 0, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, UNIQUE(rule_id,order_no))`,
		`CREATE TABLE IF NOT EXISTS xy_token_cache(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL UNIQUE, token TEXT NOT NULL, device_id TEXT NOT NULL, expire_at TEXT NOT NULL, renew_expire_at TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS shared_scan_sessions(session_id TEXT PRIMARY KEY, owner_id INTEGER NOT NULL, owner_username TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', expires_at TEXT NOT NULL, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS shared_scan_workers(sub_session_id TEXT PRIMARY KEY, shared_session_id TEXT NOT NULL, xianyu_session_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'waiting', qr_code_url TEXT, account_id TEXT, cookie_saved INTEGER DEFAULT 0, visitor_token TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY(shared_session_id) REFERENCES shared_scan_sessions(session_id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS xy_advertisements(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, title TEXT NOT NULL, content TEXT, link TEXT, expire_date TEXT, image_url TEXT, ad_type TEXT NOT NULL DEFAULT 'text', months INTEGER, total_amount TEXT, status TEXT NOT NULL DEFAULT 'unpaid', created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_advertisements_user ON xy_advertisements(user_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_advertisements_public ON xy_advertisements(status,expire_date)`,
		`CREATE TABLE IF NOT EXISTS xy_recharge_orders(id INTEGER PRIMARY KEY AUTOINCREMENT, order_no TEXT NOT NULL UNIQUE, user_id INTEGER NOT NULL, amount TEXT NOT NULL, order_type TEXT NOT NULL DEFAULT 'recharge', reference_id INTEGER, status TEXT NOT NULL DEFAULT 'pending', trade_no TEXT, qr_code TEXT, paid_at TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS xy_settlement_records(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, alipay_id TEXT, payment_type TEXT, payment_qrcode TEXT, amount TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending_review', remark TEXT, reject_reason TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_recharge_order_user ON xy_recharge_orders(user_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_settlement_user ON xy_settlement_records(user_id,created_at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS xy_activation_logs(id INTEGER PRIMARY KEY AUTOINCREMENT, machine_id TEXT NOT NULL, code_type TEXT NOT NULL, generated_code TEXT NOT NULL, days INTEGER NOT NULL, ip_address TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_activation_machine ON xy_activation_logs(machine_id,code_type,created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS xy_db_backup_log(id INTEGER PRIMARY KEY AUTOINCREMENT, status TEXT NOT NULL, file_name TEXT, file_path TEXT, file_size INTEGER, table_count INTEGER, total_rows INTEGER, duration_ms INTEGER, error_message TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_db_backup_log_created ON xy_db_backup_log(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_shared_scan_owner ON shared_scan_sessions(owner_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_shared_scan_workers ON shared_scan_workers(shared_session_id,created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_product_materials_owner ON product_materials(owner_id,id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_publish_logs_owner ON publish_logs(owner_id,id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_auto_relist_user ON xy_auto_relist_rules(user_id,enabled,next_retry_at)`,
		`CREATE INDEX IF NOT EXISTS idx_auto_relist_events_user ON xy_auto_relist_events(user_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_token_cache_expiries ON xy_token_cache(expire_at,renew_expire_at)`,
		`CREATE INDEX IF NOT EXISTS idx_fund_flows_owner ON fund_flows(owner_id,id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_resources_owner_kind ON resource_records(owner_id,kind,id)`,
		`CREATE INDEX IF NOT EXISTS idx_default_reply_records_account ON default_reply_records(owner_id,account_id,item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_owner ON xy_accounts(owner_id)`, `CREATE INDEX IF NOT EXISTS idx_keywords_account ON xy_keyword_rules(account_id)`, `CREATE INDEX IF NOT EXISTS idx_items_account ON xy_catalog_items(account_id)`, `CREATE INDEX IF NOT EXISTS idx_orders_owner ON xy_orders(owner_id)`,
		`INSERT OR IGNORE INTO system_settings(key,value,description,is_public) VALUES ('personal_mode','true','个人自用模式',1),('show_default_login_info','true','显示默认登录信息',1),('login_captcha_enabled','false','登录验证码',1),('proxy.enabled','false','全局网络代理开关',0),('proxy.url','','全局网络代理地址',0)`,
		`INSERT OR IGNORE INTO scheduled_tasks(task_code,enabled,interval_seconds,data) VALUES ('log_retention',1,86400,'{"retention_days":30}'),('sqlite_optimize',1,86400,'{}'),('db_backup',1,3600,'{"retention_days":7}'),('delivery_timeout',1,60,'{"timeout_minutes":5}'),('day_switch',1,60,'{}'),('image_cleanup',1,1200,'{"retention_hours":24}'),('fetch_items',1,1200,'{}'),('fetch_orders',1,600,'{}'),('fetch_pending_orders',1,60,'{}'),('fetch_refund_orders',1,120,'{}'),('polish',1,60,'{}'),('rate',1,20,'{}'),('close_notice',0,600,'{}'),('red_flower',0,300,'{}'),('redelivery',1,5,'{"cooldown_seconds":120}'),('auto_relist_scan',1,30,'{"batch_size":20,"lease_seconds":300,"max_retries":3}'),('api_cookie_renew',1,3600,'{}'),('cookies_refresh',1,600,'{}'),('cleanup_browser_data',0,600,'{}'),('cleanup_unconfigured_browser_data',0,10800,'{}'),('token_renewal',1,20,'{}'),('login_renew',0,600,'{}'),('listing_monitor',1,30,'{}'),('dm_send',1,60,'{}'),('seller_fill',1,15,'{}'),('auto_order',1,60,'{}')`,
		`UPDATE scheduled_tasks SET interval_seconds=COALESCE((SELECT interval_seconds FROM scheduled_tasks old WHERE old.task_code='auto_relist'),interval_seconds),data=COALESCE((SELECT data FROM scheduled_tasks old WHERE old.task_code='auto_relist'),data) WHERE task_code='auto_relist_scan' AND EXISTS(SELECT 1 FROM scheduled_tasks old WHERE old.task_code='auto_relist')`,
		`UPDATE scheduled_tasks SET enabled=0 WHERE task_code='auto_relist'`,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, s := range stmts {
		if _, err = tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err := ensureColumns(ctx, db, "xy_accounts", map[string]string{
		"last_password_login_time":  "TEXT",
		"red_flower_after_shipment": "INTEGER NOT NULL DEFAULT 0",
		"last_account_error_time":   "TEXT",
	}); err != nil {
		return err
	}
	return ensureColumns(ctx, db, "xy_recharge_orders", map[string]string{"reference_id": "INTEGER"})
}

func ensureColumns(ctx context.Context, db *sql.DB, table string, definitions map[string]string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk) == nil {
			existing[name] = true
		}
	}
	rows.Close()
	for name, definition := range definitions {
		if existing[name] {
			continue
		}
		if _, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+name+` `+definition); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, name, err)
		}
	}
	return nil
}
