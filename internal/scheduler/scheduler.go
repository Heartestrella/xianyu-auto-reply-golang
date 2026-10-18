package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	db              *sql.DB
	log             *slog.Logger
	mu              sync.Mutex
	running         map[string]bool
	handlers        map[string]func(context.Context, map[string]any) error
	redeliver       func(context.Context, string) error
	publish         func(context.Context, int64, int64, string) (string, bool, error)
	relistLiveCheck func(context.Context, int64, string, string) (bool, error)
	cookieRenew     func(context.Context) error
	cookiesRefresh  func(context.Context) error
	tokenRenew      func(context.Context) error
	loginRenew      func(context.Context) error
	listingMonitor  func(context.Context) error
	dmSend          func(context.Context) error
	sellerFill      func(context.Context) error
	autoOrder       func(context.Context) error
}

func New(db *sql.DB, log *slog.Logger) *Engine {
	e := &Engine{db: db, log: log, running: map[string]bool{}}
	e.handlers = map[string]func(context.Context, map[string]any) error{
		"log_retention": e.logRetention, "sqlite_optimize": e.optimize,
		"db_backup": e.dbBackup, "delivery_timeout": e.deliveryTimeout,
		"day_switch": e.daySwitch, "image_cleanup": e.imageCleanup,
		"fetch_items": e.fetchItems, "fetch_orders": e.fetchOrders,
		"fetch_pending_orders": e.fetchPendingOrders, "fetch_refund_orders": e.fetchRefundOrders,
		"polish": e.polishItems, "rate": e.autoRate,
		"close_notice": e.closeNotice, "red_flower": e.redFlower,
		"redelivery":                        e.redelivery,
		"auto_relist":                       e.autoRelist,
		"auto_relist_scan":                  e.autoRelist,
		"api_cookie_renew":                  e.apiCookieRenew,
		"cookies_refresh":                   e.cookiesRefreshRun,
		"cleanup_browser_data":              e.browserDataNoop,
		"cleanup_unconfigured_browser_data": e.browserDataNoop,
		"token_renewal":                     e.tokenRenewal,
		"login_renew":                       e.loginRenewal,
		"listing_monitor":                   e.runListingMonitor,
		"dm_send":                           e.runDMSend,
		"seller_fill":                       e.runSellerFill,
		"auto_order":                        e.runAutoOrder,
	}
	return e
}
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	e.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.scan(ctx)
		}
	}
}
func (e *Engine) scan(ctx context.Context) {
	rows, err := e.db.QueryContext(ctx, `SELECT task_code,interval_seconds,last_run_at,data FROM scheduled_tasks WHERE enabled=1`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var code, data string
		var interval int
		var last sql.NullString
		if rows.Scan(&code, &interval, &last, &data) != nil {
			continue
		}
		due := true
		if last.Valid {
			if t, err := time.Parse("2006-01-02 15:04:05", last.String); err == nil {
				due = time.Since(t) >= time.Duration(interval)*time.Second
			}
		}
		if due {
			go e.Trigger(context.Background(), code)
		}
	}
}
func (e *Engine) Trigger(ctx context.Context, code string) error {
	e.mu.Lock()
	if e.running[code] {
		e.mu.Unlock()
		return fmt.Errorf("任务正在运行")
	}
	handler := e.handlers[code]
	if handler == nil {
		e.mu.Unlock()
		return fmt.Errorf("未知任务: %s", code)
	}
	e.running[code] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.running, code); e.mu.Unlock() }()
	var raw string
	_ = e.db.QueryRowContext(ctx, `SELECT data FROM scheduled_tasks WHERE task_code=?`, code).Scan(&raw)
	cfg := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &cfg)
	err := handler(ctx, cfg)
	_, _ = e.db.ExecContext(ctx, `UPDATE scheduled_tasks SET last_run_at=CURRENT_TIMESTAMP,next_run_at=datetime('now','+'||interval_seconds||' seconds') WHERE task_code=?`, code)
	if err != nil {
		e.log.Error("scheduled task", "task", code, "error", err)
	} else {
		e.log.Info("scheduled task", "task", code, "status", "success")
	}
	return err
}
func (e *Engine) Running(code string) bool                             { e.mu.Lock(); defer e.mu.Unlock(); return e.running[code] }
func (e *Engine) Rescan()                                              { e.scan(context.Background()) }
func (e *Engine) SetRedelivery(fn func(context.Context, string) error) { e.redeliver = fn }
func (e *Engine) SetPublisher(fn func(context.Context, int64, int64, string) (string, bool, error)) {
	e.publish = fn
}
func (e *Engine) SetRelistLiveChecker(fn func(context.Context, int64, string, string) (bool, error)) {
	e.relistLiveCheck = fn
}
func (e *Engine) SetCookieRenewer(fn func(context.Context) error)    { e.cookieRenew = fn }
func (e *Engine) SetCookiesRefresher(fn func(context.Context) error) { e.cookiesRefresh = fn }
func (e *Engine) SetTokenRenewer(fn func(context.Context) error)     { e.tokenRenew = fn }
func (e *Engine) SetLoginRenewer(fn func(context.Context) error)     { e.loginRenew = fn }
func (e *Engine) SetListingMonitor(fn func(context.Context) error)   { e.listingMonitor = fn }
func (e *Engine) SetDMSender(fn func(context.Context) error)         { e.dmSend = fn }
func (e *Engine) SetSellerFiller(fn func(context.Context) error)     { e.sellerFill = fn }
func (e *Engine) SetAutoOrder(fn func(context.Context) error)        { e.autoOrder = fn }
func (e *Engine) apiCookieRenew(ctx context.Context, _ map[string]any) error {
	if e.cookieRenew == nil {
		return errors.New("Cookie续期执行器未配置")
	}
	return e.cookieRenew(ctx)
}
func (e *Engine) cookiesRefreshRun(ctx context.Context, _ map[string]any) error {
	if e.cookiesRefresh == nil {
		return errors.New("COOKIES续期执行器未配置")
	}
	return e.cookiesRefresh(ctx)
}
func (e *Engine) browserDataNoop(context.Context, map[string]any) error {
	// The Go monolith never creates Playwright profile directories, so there is
	// no browser state to remove. Keeping the task codes preserves admin API
	// compatibility with installations migrated from the four-container build.
	return nil
}
func (e *Engine) tokenRenewal(ctx context.Context, _ map[string]any) error {
	if e.tokenRenew == nil {
		return errors.New("Token续期执行器未配置")
	}
	return e.tokenRenew(ctx)
}
func (e *Engine) loginRenewal(ctx context.Context, _ map[string]any) error {
	if e.loginRenew == nil {
		return errors.New("登录续期执行器未配置")
	}
	return e.loginRenew(ctx)
}
func (e *Engine) runListingMonitor(ctx context.Context, _ map[string]any) error {
	if e.listingMonitor == nil {
		return errors.New("商品监控执行器未配置")
	}
	return e.listingMonitor(ctx)
}
func (e *Engine) runDMSend(ctx context.Context, _ map[string]any) error {
	if e.dmSend == nil {
		return errors.New("监控私信执行器未配置")
	}
	return e.dmSend(ctx)
}
func (e *Engine) runSellerFill(ctx context.Context, _ map[string]any) error {
	if e.sellerFill == nil {
		return errors.New("卖家ID补全执行器未配置")
	}
	return e.sellerFill(ctx)
}
func (e *Engine) runAutoOrder(ctx context.Context, _ map[string]any) error {
	if e.autoOrder == nil {
		return errors.New("自动下单执行器未配置")
	}
	return e.autoOrder(ctx)
}
func (e *Engine) logRetention(ctx context.Context, cfg map[string]any) error {
	days := 30
	if v, ok := cfg["retention_days"].(float64); ok && v > 0 {
		days = int(v)
	}
	_, err := e.db.ExecContext(ctx, `DELETE FROM event_logs WHERE created_at < datetime('now',?)`, fmt.Sprintf("-%d day", days))
	return err
}
func (e *Engine) optimize(ctx context.Context, cfg map[string]any) error {
	_, err := e.db.ExecContext(ctx, `PRAGMA optimize`)
	return err
}

func (e *Engine) databasePath(ctx context.Context) (string, error) {
	rows, err := e.db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, path string
		if rows.Scan(&seq, &name, &path) == nil && name == "main" && path != "" {
			return filepath.Clean(path), nil
		}
	}
	return "", errors.New("无法确定SQLite数据库路径")
}

func (e *Engine) dbBackup(ctx context.Context, cfg map[string]any) error {
	databasePath, err := e.databasePath(ctx)
	if err != nil {
		return err
	}
	backupDir := filepath.Join(filepath.Dir(databasePath), "backups")
	if configured := strings.TrimSpace(fmt.Sprint(cfg["directory"])); configured != "" && configured != "<nil>" {
		if filepath.IsAbs(configured) {
			backupDir = configured
		} else {
			backupDir = filepath.Join(filepath.Dir(databasePath), configured)
		}
	}
	if err = os.MkdirAll(backupDir, 0755); err != nil {
		return err
	}
	name := "xianyu-" + time.Now().Format("20060102-150405") + ".db"
	target := filepath.Join(backupDir, name)
	started := time.Now()
	escaped := strings.ReplaceAll(target, "'", "''")
	if _, err = e.db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	_, _ = e.db.ExecContext(ctx, `INSERT INTO xy_db_backup_log(status,file_name,file_path,file_size,duration_ms) VALUES('success',?,?,?,?)`, name, target, info.Size(), time.Since(started).Milliseconds())
	retention := 7
	if n, ok := cfg["retention_days"].(float64); ok && n > 0 {
		retention = int(n)
	}
	cutoff := time.Now().Add(-time.Duration(retention) * 24 * time.Hour)
	entries, _ := os.ReadDir(backupDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "xianyu-") || filepath.Ext(entry.Name()) != ".db" {
			continue
		}
		if stat, x := entry.Info(); x == nil && stat.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(backupDir, entry.Name()))
		}
	}
	return nil
}

func (e *Engine) deliveryTimeout(ctx context.Context, cfg map[string]any) error {
	minutes := 5
	if n, ok := cfg["timeout_minutes"].(float64); ok && n > 0 {
		minutes = int(n)
	}
	_, err := e.db.ExecContext(ctx, `UPDATE event_logs SET data=json_set(COALESCE(data,'{}'),'$.send_status','timeout') WHERE kind='auto_delivery' AND json_extract(data,'$.send_status')='unknown' AND created_at<datetime('now',?)`, fmt.Sprintf("-%d minutes", minutes))
	return err
}

func (e *Engine) daySwitch(ctx context.Context, _ map[string]any) error {
	today := time.Now().Format("2006-01-02")
	var old string
	_ = e.db.QueryRowContext(ctx, `SELECT COALESCE(value,'') FROM system_settings WHERE key='platform_day'`).Scan(&old)
	if old == today {
		return nil
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE xy_catalog_items SET data=json_set(COALESCE(data,'{}'),'$.is_polished',false),updated_at=CURRENT_TIMESTAMP WHERE json_extract(data,'$.is_polished')=1`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO system_settings(key,value,description) VALUES('platform_day',?,'平台日') ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, today); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) imageCleanup(ctx context.Context, cfg map[string]any) error {
	databasePath, err := e.databasePath(ctx)
	if err != nil {
		return err
	}
	root := filepath.Join(filepath.Dir(databasePath), "uploads")
	hours := 24
	if n, ok := cfg["retention_hours"].(float64); ok && n > 0 {
		hours = int(n)
	}
	references := map[string]bool{}
	collect := func(query string) error {
		rows, x := e.db.QueryContext(ctx, query)
		if x != nil {
			return x
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if rows.Scan(&raw) != nil {
				continue
			}
			var value any
			if json.Unmarshal([]byte(raw), &value) != nil {
				continue
			}
			collectImageNames(value, references)
		}
		return rows.Err()
	}
	if err = collect(`SELECT data FROM cards`); err != nil {
		return err
	}
	if err = collect(`SELECT data FROM product_materials`); err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	allowed := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}
	for _, dirName := range []string{"cards", "images"} {
		dir := filepath.Join(root, dirName)
		entries, x := os.ReadDir(dir)
		if os.IsNotExist(x) {
			continue
		}
		if x != nil {
			return x
		}
		for _, entry := range entries {
			if entry.IsDir() || !allowed[strings.ToLower(filepath.Ext(entry.Name()))] || references[entry.Name()] {
				continue
			}
			stat, x := entry.Info()
			if x == nil && stat.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
	return nil
}

func collectImageNames(value any, out map[string]bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "image") || strings.Contains(lower, "cover") || strings.Contains(lower, "url") {
				if text, ok := child.(string); ok && text != "" {
					out[filepath.Base(strings.Split(text, "?")[0])] = true
				}
			}
			collectImageNames(child, out)
		}
	case []any:
		for _, child := range v {
			collectImageNames(child, out)
		}
	}
}
