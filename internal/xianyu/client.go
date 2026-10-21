package xianyu

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zhinianboke/xianyu-auto-go/internal/ai"
	"github.com/zhinianboke/xianyu-auto-go/internal/notify"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
)

const socketURL = "wss://wss-goofish.dingtalk.com/"

type Account struct {
	ID                                                                                int64
	OwnerID                                                                           int64
	AccountID, Cookie                                                                 string
	ProxyType, ProxyHost                                                              string
	ProxyPort                                                                         sql.NullInt64
	ProxyUser, ProxyPass                                                              sql.NullString
	AutoConfirm, ConfirmBeforeSend, SendBeforeConfirm, OnlySendCard, DeliveryDisabled bool
}
type Incoming struct {
	AccountID, SenderID, SenderName, ChatID, ItemID, Text, Type string
	Images                                                      []string
	// IsSelf marks IM echo messages sent by the currently connected seller.
	// The online-chat page uses it to render the bubble on the right.
	IsSelf     bool
	Raw        []byte
	ReceivedAt time.Time
}
type replyDecision struct {
	Text, ImageURL, RecordItemID string
	RecordOnce, Suppress         bool
	RawPayload                   []byte
}
type Manager struct {
	db           *sql.DB
	log          *slog.Logger
	onMessage    func(Incoming)
	mu           sync.Mutex
	running      map[string]context.CancelFunc
	online       map[string]bool
	blocked      map[string][32]byte
	tokenFetcher func(context.Context, Account, string) (string, map[string]string, error)
	writers      map[string]*activeWriter
	deliveryMu   sync.Mutex
}

type activeWriter struct {
	send      func(int, []byte) error
	pendingMu sync.Mutex
	pending   map[string]chan map[string]any
}

func NewManager(db *sql.DB, log *slog.Logger, onMessage func(Incoming)) *Manager {
	return &Manager{db: db, log: log, onMessage: onMessage, running: map[string]context.CancelFunc{}, online: map[string]bool{}, blocked: map[string][32]byte{}, writers: map[string]*activeWriter{}}
}
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}
func (m *Manager) Online(id string) bool { m.mu.Lock(); defer m.mu.Unlock(); return m.online[id] }
func (m *Manager) Restart(id string) {
	m.mu.Lock()
	if cancel := m.running[id]; cancel != nil {
		cancel()
	}
	delete(m.online, id)
	delete(m.blocked, id)
	m.mu.Unlock()
}
func (m *Manager) RestartAll() {
	m.mu.Lock()
	m.blocked = map[string][32]byte{}
	for _, cancel := range m.running {
		cancel()
	}
	m.mu.Unlock()
}

func (m *Manager) SendText(ctx context.Context, accountID, chatID, toUserID, text string) (string, error) {
	if strings.TrimSpace(text) == "" || chatID == "" || toUserID == "" {
		return "", errors.New("会话、收件人和消息内容不能为空")
	}
	var cookie string
	if err := m.db.QueryRowContext(ctx, `SELECT cookie FROM xy_accounts WHERE account_id=?`, accountID).Scan(&cookie); err != nil {
		return "", errors.New("账号不存在")
	}
	myID := ParseCookies(cookie)["unb"]
	if myID == "" {
		return "", errors.New("账号Cookie缺少unb")
	}
	m.mu.Lock()
	active := m.writers[accountID]
	m.mu.Unlock()
	if active == nil {
		return "", errors.New("账号IM未连接")
	}
	payload, messageID := TextMessage(chatID, toUserID, myID, text)
	if err := active.send(websocket.TextMessage, payload); err != nil {
		return "", err
	}
	return messageID, nil
}

func (m *Manager) CreateChat(ctx context.Context, accountID, toUserID, itemID string) (string, error) {
	if accountID == "" || toUserID == "" || itemID == "" {
		return "", errors.New("账号、对方用户和商品不能为空")
	}
	var cookie string
	if err := m.db.QueryRowContext(ctx, `SELECT cookie FROM xy_accounts WHERE account_id=? AND status='active'`, accountID).Scan(&cookie); err != nil {
		return "", errors.New("账号不存在或未启用")
	}
	myID := ParseCookies(cookie)["unb"]
	if myID == "" {
		return "", errors.New("账号Cookie缺少unb")
	}
	m.mu.Lock()
	active := m.writers[accountID]
	m.mu.Unlock()
	if active == nil {
		return "", errors.New("账号IM未连接")
	}
	payload, mid := CreateChat(toUserID, myID, itemID)
	response := make(chan map[string]any, 1)
	active.pendingMu.Lock()
	active.pending[mid] = response
	active.pendingMu.Unlock()
	defer func() { active.pendingMu.Lock(); delete(active.pending, mid); active.pendingMu.Unlock() }()
	if err := active.send(websocket.TextMessage, payload); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(30 * time.Second):
		return "", errors.New("创建会话超时")
	case result := <-response:
		if cid := findChatCID(result); cid != "" {
			return cid, nil
		}
		return "", fmt.Errorf("创建会话失败: %s", responseError(result))
	}
}
func (m *Manager) RecallMessage(ctx context.Context, accountID, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return errors.New("缺少消息ID，无法撤回")
	}
	var exists int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM xy_accounts WHERE account_id=?`, accountID).Scan(&exists); err != nil || exists == 0 {
		return errors.New("账号不存在")
	}
	m.mu.Lock()
	active := m.writers[accountID]
	m.mu.Unlock()
	if active == nil {
		return errors.New("账号未连接")
	}
	return active.send(websocket.TextMessage, RecallMessage(messageID))
}
func (m *Manager) SendImage(ctx context.Context, accountID, chatID, toUserID, imageURL string, width, height int) (string, error) {
	if chatID == "" || toUserID == "" || strings.TrimSpace(imageURL) == "" {
		return "", errors.New("会话、收件人和图片地址不能为空")
	}
	var cookie string
	if err := m.db.QueryRowContext(ctx, `SELECT cookie FROM xy_accounts WHERE account_id=?`, accountID).Scan(&cookie); err != nil {
		return "", errors.New("账号不存在")
	}
	myID := ParseCookies(cookie)["unb"]
	if myID == "" {
		return "", errors.New("账号Cookie缺少unb")
	}
	m.mu.Lock()
	active := m.writers[accountID]
	m.mu.Unlock()
	if active == nil {
		return "", errors.New("账号IM未连接")
	}
	payload, messageID := ImageMessage(chatID, toUserID, myID, imageURL, width, height)
	if err := active.send(websocket.TextMessage, payload); err != nil {
		return "", err
	}
	return messageID, nil
}
func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cancel := range m.running {
		cancel()
	}
	m.running = map[string]context.CancelFunc{}
}
func (m *Manager) reconcile(ctx context.Context) {
	rows, e := m.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass,auto_confirm,confirm_before_send,send_before_confirm,only_send_card,delivery_disabled FROM xy_accounts WHERE status='active'`)
	if e != nil {
		m.log.Error("load accounts", "error", e)
		return
	}
	defer rows.Close()
	wanted := map[string]Account{}
	for rows.Next() {
		var a Account
		if rows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass, &a.AutoConfirm, &a.ConfirmBeforeSend, &a.SendBeforeConfirm, &a.OnlySendCard, &a.DeliveryDisabled) == nil {
			wanted[a.AccountID] = a
		}
	}
	m.mu.Lock()
	for id, cancel := range m.running {
		if _, ok := wanted[id]; !ok {
			cancel()
			delete(m.online, id)
		}
	}
	for id, a := range wanted {
		if _, ok := m.running[id]; ok {
			continue
		}
		child, cancel := context.WithCancel(ctx)
		m.running[id] = cancel
		go m.accountLoop(child, a)
	}
	m.mu.Unlock()
}
func (m *Manager) setOnline(id string, v bool) { m.mu.Lock(); m.online[id] = v; m.mu.Unlock() }
func (m *Manager) accountLoop(ctx context.Context, a Account) {
	defer func() {
		m.setOnline(a.AccountID, false)
		m.mu.Lock()
		delete(m.running, a.AccountID)
		delete(m.blocked, a.AccountID)
		m.mu.Unlock()
	}()
	fails := 0
	for ctx.Err() == nil {
		if err := m.reloadConnectionAccount(ctx, &a); err != nil {
			return
		}
		started := time.Now()
		err := m.connect(ctx, a)
		m.setOnline(a.AccountID, false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			fails = 0
		}
		fails++
		errText := "连接已结束"
		if err != nil {
			errText = err.Error()
		}
		reason := ConnectionFailureReason(err)
		requiresAction := reason == "platform_verification_required" || reason == "session_expired" || reason == "invalid_cookie"
		if err := m.reloadConnectionAccount(ctx, &a); err != nil {
			return
		}
		failureData, _ := json.Marshal(map[string]any{"login_status": "failed", "login_method": "cookie", "trigger_reason": "聊天连接重连", "failure_reason": reason, "error_message": errText, "duration_ms": time.Since(started).Milliseconds(), "account_status": "active", "requires_verification": requiresAction})
		_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'account_login',?,'ERROR',?,?)`, a.OwnerID, a.AccountID, errText, string(failureData))
		if requiresAction {
			m.log.Warn("xianyu connection requires account action", "account", a.AccountID, "reason", reason)
			if err := m.waitForConnectionAccountUpdate(ctx, a); err != nil {
				return
			}
			fails = 0
			continue
		}
		delay := time.Duration(math.Min(math.Pow(2, float64(fails)), 60))*time.Second + time.Duration(rand.IntN(3000))*time.Millisecond
		m.log.Warn("xianyu disconnected", "account", a.AccountID, "error", err, "retry", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
func (m *Manager) connect(ctx context.Context, a Account) error {
	cookies := ParseCookies(a.Cookie)
	uid := cookies["unb"]
	if uid == "" {
		return errors.New("Cookie缺少unb")
	}
	device, e := m.connectionDeviceID(ctx, a)
	if e != nil {
		return e
	}
	token, newCookies, e := m.getConnectionToken(ctx, a, device)
	if cookieErr := m.saveConnectionCookies(ctx, &a, newCookies); cookieErr != nil {
		return cookieErr
	}
	if e != nil {
		return e
	}
	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second, Proxy: proxy.ProxyFunc()}
	if p := proxyURL(a); p != nil {
		dialer.Proxy = http.ProxyURL(p)
	}
	conn, resp, e := dialer.DialContext(ctx, socketURL, nil)
	if e != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return e
	}
	defer conn.Close()
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	defer cancelConnection()
	stopClose := context.AfterFunc(connectionCtx, func() { _ = conn.Close() })
	defer stopClose()
	var writeMu sync.Mutex
	write := func(messageType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(messageType, data)
	}
	active := &activeWriter{send: write, pending: map[string]chan map[string]any{}}
	defer func() {
		m.mu.Lock()
		if m.writers[a.AccountID] == active {
			delete(m.writers, a.AccountID)
		}
		m.mu.Unlock()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	if e = write(websocket.TextMessage, Register(token, device)); e != nil {
		return e
	}
	time.Sleep(time.Second)
	if e = write(websocket.TextMessage, Sync()); e != nil {
		return e
	}
	m.mu.Lock()
	m.writers[a.AccountID] = active
	m.mu.Unlock()
	m.setOnline(a.AccountID, true)
	loginData, _ := json.Marshal(map[string]any{"login_status": "success", "login_method": "cookie"})
	_, _ = m.db.ExecContext(ctx, `UPDATE xy_accounts SET last_login_at=CURRENT_TIMESTAMP WHERE id=?`, a.ID)
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'account_login',?,'INFO','IM连接成功',?)`, a.OwnerID, a.AccountID, string(loginData))
	m.log.Info("xianyu connected", "account", a.AccountID)
	errCh := make(chan error, 1)
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-connectionCtx.Done():
				errCh <- connectionCtx.Err()
				return
			case <-tick.C:
				hb, _ := json.Marshal(map[string]any{"lwp": "/!", "headers": map[string]string{"mid": Mid()}})
				if e := write(websocket.TextMessage, hb); e != nil {
					errCh <- e
					return
				}
			}
		}
	}()
	for {
		select {
		case e := <-errCh:
			return e
		default:
		}
		_, raw, e := conn.ReadMessage()
		if e != nil {
			return e
		}
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		var response map[string]any
		if json.Unmarshal(raw, &response) == nil {
			if mid := responseMID(response); mid != "" {
				active.pendingMu.Lock()
				waiter := active.pending[mid]
				active.pendingMu.Unlock()
				if waiter != nil {
					select {
					case waiter <- response:
					default:
					}
					continue
				}
			}
		}
		incomings := ParseIncomingMany(a.AccountID, raw)
		if len(incomings) == 0 {
			m.persistUnparsedIncoming(ctx, a, raw)
			continue
		}
		for _, incoming := range incomings {
			if e = m.handleIncoming(ctx, a, uid, incoming, write); e != nil {
				return e
			}
		}
	}
}

func (m *Manager) handleIncoming(ctx context.Context, a Account, uid string, incoming Incoming, write func(int, []byte) error) error {
	incoming.IsSelf = incoming.SenderID == uid
	m.persistIncoming(ctx, a, incoming)
	if m.onMessage != nil {
		m.onMessage(incoming)
	}
	if incoming.SenderID != uid {
		go m.notifyIncoming(context.Background(), a, incoming)
	}
	if incoming.SenderID == uid || incoming.Type != "text" || strings.TrimSpace(incoming.Text) == "" || isSystemNotice(incoming) {
		return nil
	}
	if m.isBlacklisted(ctx, a, incoming) || m.filteredMessage(ctx, a, incoming.Text, "skip_reply") {
		_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'blacklist_skip',?,?,?)`, a.OwnerID, a.AccountID, incoming.SenderID, `{"status":"skipped"}`)
		return nil
	}
	reply := m.matchReply(ctx, a, incoming)
	if reply.Text == "" && reply.ImageURL == "" && len(reply.RawPayload) == 0 {
		return nil
	}
	for _, segment := range replySegments(reply.Text) {
		payload, _ := TextMessage(incoming.ChatID, incoming.SenderID, uid, segment)
		if err := write(websocket.TextMessage, payload); err != nil {
			return err
		}
	}
	if reply.ImageURL != "" {
		payload, _ := ImageMessage(incoming.ChatID, incoming.SenderID, uid, reply.ImageURL, 0, 0)
		if err := write(websocket.TextMessage, payload); err != nil {
			return err
		}
	}
	if len(reply.RawPayload) > 0 {
		if err := write(websocket.TextMessage, reply.RawPayload); err != nil {
			return err
		}
	}
	if reply.RecordOnce {
		_, _ = m.db.ExecContext(ctx, `INSERT OR IGNORE INTO default_reply_records(owner_id,account_id,item_id,user_id) VALUES(?,?,?,?)`, a.OwnerID, a.AccountID, reply.RecordItemID, incoming.SenderID)
	}
	logData, _ := json.Marshal(map[string]any{"status": "sent", "image_url": reply.ImageURL, "external_contact": len(reply.RawPayload) > 0, "default_reply_item_id": reply.RecordItemID, "reply_once": reply.RecordOnce})
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'auto_reply',?,?,?)`, a.OwnerID, a.AccountID, reply.Text, string(logData))
	return nil
}

func (m *Manager) persistUnparsedIncoming(ctx context.Context, a Account, raw []byte) {
	// 心跳和普通 RPC 响应不属于聊天推送，避免制造无意义日志。
	if len(raw) == 0 || bytes.Contains(raw, []byte(`"lwp":"/!"`)) ||
		(!bytes.Contains(raw, []byte(`"body"`)) && !bytes.Contains(raw, []byte(`"syncPush`)) && !bytes.Contains(raw, []byte(`reminderContent`))) {
		return
	}
	var count int
	_ = m.db.QueryRowContext(ctx, `SELECT count(*) FROM event_logs WHERE owner_id=? AND account_id=? AND kind='incoming_unparsed'`, a.OwnerID, a.AccountID).Scan(&count)
	if count >= 100 {
		return
	}
	if len(raw) > 32768 {
		raw = raw[:32768]
	}
	payload, _ := json.Marshal(map[string]any{"raw": json.RawMessage(raw)})
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'incoming_unparsed',?,'WARN','收到未识别的闲鱼消息帧',?)`, a.OwnerID, a.AccountID, string(payload))
}

func replySegments(text string) []string {
	parts := strings.Split(text, "######")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func responseMID(value map[string]any) string {
	if headers, ok := value["headers"].(map[string]any); ok {
		return strings.TrimSpace(fmt.Sprint(headers["mid"]))
	}
	return ""
}

func findChatCID(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.EqualFold(key, "cid") {
				return strings.TrimSuffix(strings.TrimSpace(fmt.Sprint(child)), "@goofish")
			}
			if found := findChatCID(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := findChatCID(child); found != "" {
				return found
			}
		}
	}
	return ""
}

func responseError(value map[string]any) string {
	for _, key := range []string{"message", "msg", "code"} {
		if text := strings.TrimSpace(fmt.Sprint(value[key])); text != "" && text != "<nil>" {
			return text
		}
	}
	return "响应中没有会话ID"
}

func tokenCacheExpiry() time.Time {
	// Match the original staggered 6-15 hour cache window so large account sets
	// do not all hit the Token endpoint at the same instant.
	return time.Now().UTC().Add(time.Duration(6+rand.IntN(10)) * time.Hour)
}

func (m *Manager) getConnectionToken(ctx context.Context, a Account, device string) (string, map[string]string, error) {
	userID := ParseCookies(a.Cookie)["unb"]
	var token string
	err := m.db.QueryRowContext(ctx, `SELECT token FROM xy_token_cache WHERE user_id=? AND device_id=? AND COALESCE(renew_expire_at,expire_at)>datetime('now','+5 minutes')`, userID, device).Scan(&token)
	if err == nil && strings.TrimSpace(token) != "" {
		return token, nil, nil
	}
	token, cookies, err := m.fetchConnectionToken(ctx, a, device)
	if err != nil {
		return "", cookies, err
	}
	expires := tokenCacheExpiry().Format("2006-01-02 15:04:05")
	_, err = m.db.ExecContext(ctx, `INSERT INTO xy_token_cache(user_id,token,device_id,expire_at,renew_expire_at) VALUES(?,?,?,?,NULL) ON CONFLICT(user_id) DO UPDATE SET token=excluded.token,device_id=excluded.device_id,expire_at=excluded.expire_at,renew_expire_at=NULL,updated_at=CURRENT_TIMESTAMP`, userID, token, device, expires)
	if err != nil {
		return "", cookies, fmt.Errorf("保存Token缓存: %w", err)
	}
	return token, cookies, nil
}

// RenewTokens prefetches IM tokens that are missing or expire within one hour.
// Results are persisted in SQLite and consumed by the next websocket connect.
func (m *Manager) RenewTokens(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass,auto_confirm,confirm_before_send,send_before_confirm,only_send_card,delivery_disabled FROM xy_accounts WHERE status='active' ORDER BY id`)
	if err != nil {
		return err
	}
	accounts := []Account{}
	for rows.Next() {
		var a Account
		if rows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass, &a.AutoConfirm, &a.ConfirmBeforeSend, &a.SendBeforeConfirm, &a.OnlySendCard, &a.DeliveryDisabled) == nil {
			accounts = append(accounts, a)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	batchID := fmt.Sprintf("token-%d", time.Now().UnixMilli())
	seen := map[string]bool{}
	failures := []string{}
	for _, a := range accounts {
		userID := ParseCookies(a.Cookie)["unb"]
		if userID == "" || seen[userID] {
			continue
		}
		seen[userID] = true
		var reusable int
		_ = m.db.QueryRowContext(ctx, `SELECT count(*) FROM xy_token_cache WHERE user_id=? AND COALESCE(renew_expire_at,expire_at)>datetime('now','+1 hour')`, userID).Scan(&reusable)
		if reusable > 0 {
			continue
		}
		if m.connectionBlocked(a) {
			failures = append(failures, a.AccountID+": 需要完成闲鱼验证或更新登录")
			continue
		}
		device, deviceErr := m.connectionDeviceID(ctx, a)
		if deviceErr != nil {
			failures = append(failures, a.AccountID+": "+deviceErr.Error())
			continue
		}
		token, cookies, renewErr := m.fetchConnectionToken(ctx, a, device)
		if cookieErr := m.saveConnectionCookies(ctx, &a, cookies); cookieErr != nil {
			renewErr = cookieErr
		}
		status, level, message := "success", "INFO", "IM Token续期成功"
		var expires any
		if renewErr == nil {
			expires = tokenCacheExpiry().Format("2006-01-02 15:04:05")
			_, renewErr = m.db.ExecContext(ctx, `INSERT INTO xy_token_cache(user_id,token,device_id,expire_at,renew_expire_at) VALUES(?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET token=excluded.token,device_id=excluded.device_id,renew_expire_at=excluded.renew_expire_at,updated_at=CURRENT_TIMESTAMP`, userID, token, device, expires, expires)
		}
		if renewErr != nil {
			status, level, message = "failed", "ERROR", renewErr.Error()
			failures = append(failures, a.AccountID+": "+renewErr.Error())
		}
		data, _ := json.Marshal(map[string]any{"batch_id": batchID, "status": status, "token_user_id": userID, "renew_expire_at": expires})
		_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'token_renewal',?,?,?,?)`, a.OwnerID, a.AccountID, level, message, string(data))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) notifyIncoming(ctx context.Context, a Account, v Incoming) {
	if m.filteredMessage(ctx, a, v.Text, "skip_notify") {
		return
	}
	rows, err := m.db.QueryContext(ctx, `SELECT c.data FROM resource_records n JOIN resource_records c ON c.owner_id=n.owner_id AND c.kind='notification_channel' AND c.id=CAST(json_extract(n.data,'$.channel_id') AS INTEGER) WHERE n.owner_id=? AND n.kind='message_notification' AND n.account_id=? AND n.enabled=1 AND c.enabled=1`, a.OwnerID, a.AccountID)
	if err != nil {
		return
	}
	defer rows.Close()
	message := fmt.Sprintf("收到闲鱼消息\n账号: %s\n买家: %s\n内容: %s", a.AccountID, firstNonempty(v.SenderName, v.SenderID), v.Text)
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		data := map[string]any{}
		if json.Unmarshal([]byte(raw), &data) != nil {
			continue
		}
		channel := notify.Channel{Type: stringSetting(data, "type", "channel_type"), Config: notify.ParseConfig(data["config"])}
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = notify.Send(sendCtx, channel, message)
		cancel()
		if err != nil {
			m.log.Warn("message notification failed", "account", a.AccountID, "error", err)
		}
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (m *Manager) filteredMessage(ctx context.Context, a Account, message, filterType string) bool {
	rows, err := m.db.QueryContext(ctx, `SELECT COALESCE(json_extract(data,'$.keyword'),'') FROM resource_records WHERE owner_id=? AND kind='message_filter' AND enabled=1 AND (COALESCE(account_id,'')='' OR account_id=?) AND COALESCE(json_extract(data,'$.filter_type'),'skip_reply')=?`, a.OwnerID, a.AccountID, filterType)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var keyword string
		if rows.Scan(&keyword) == nil && keyword != "" && strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}

func (m *Manager) isBlacklisted(ctx context.Context, a Account, v Incoming) bool {
	var count int
	_ = m.db.QueryRowContext(ctx, `SELECT count(*) FROM resource_records WHERE enabled=1 AND ((owner_id=? AND kind='personal_blacklist' AND (COALESCE(account_id,'')='' OR account_id=?) AND json_extract(data,'$.buyer_id')=? AND (COALESCE(json_extract(data,'$.item_id'),'')='' OR json_extract(data,'$.item_id')=?)) OR (kind='platform_blacklist' AND json_extract(data,'$.buyer_id')=?))`, a.OwnerID, a.AccountID, v.SenderID, v.ItemID, v.SenderID).Scan(&count)
	return count > 0
}
func proxyURL(a Account) *url.URL {
	if proxy.Enabled() {
		u, _ := url.Parse(proxy.URL())
		return u
	}
	if a.ProxyType == "" || a.ProxyType == "none" || a.ProxyHost == "" || !a.ProxyPort.Valid {
		return nil
	}
	u := &url.URL{Scheme: a.ProxyType, Host: fmt.Sprintf("%s:%d", a.ProxyHost, a.ProxyPort.Int64)}
	if a.ProxyUser.Valid {
		u.User = url.UserPassword(a.ProxyUser.String, a.ProxyPass.String)
	}
	return u
}
func mergeCookies(raw string, updates map[string]string) string {
	m := ParseCookies(raw)
	for k, v := range updates {
		m[k] = v
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}
func (m *Manager) persistIncoming(ctx context.Context, a Account, v Incoming) {
	data, _ := json.Marshal(v)
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'incoming_message',?,?,?)`, a.OwnerID, a.AccountID, v.Text, string(data))
	m.persistOrderEvent(ctx, a, v)
	lower := strings.ToLower(v.Text)
	if strings.Contains(v.Text, "人脸验证") || strings.Contains(v.Text, "身份验证") || strings.Contains(lower, "face verification") {
		extra, _ := json.Marshal(map[string]any{"title": "需要人脸验证", "is_read": false, "account_db_id": a.ID, "raw": v.Raw})
		_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'face_verification',?,'WARN',?,?)`, a.OwnerID, a.AccountID, v.Text, string(extra))
	}
}

var orderIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)orderId[=:\\"']+(\d{10,})`),
	regexp.MustCompile(`(?i)order_detail\?[^\"']*?id=(\d{10,})`),
	regexp.MustCompile(`(?i)bizOrderId[=:\\"']+(\d{10,})`),
	regexp.MustCompile(`(?i)\"(?:orderId|bizOrderId)\"\s*:\s*\"?(\d{10,})`),
}

func extractOrderID(raw []byte) string {
	text := string(raw)
	for _, pattern := range orderIDPatterns {
		if match := pattern.FindStringSubmatch(text); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

func orderStatusFromMessage(message string) string {
	message = strings.TrimSpace(message)
	switch message {
	case "[我已拍下，待付款]":
		return "pending_payment"
	case "[我已付款，等待你发货]", "[买家已付款]", "[付款完成]", "[已付款，待发货]", "[我已付款，请添加自提信息]":
		return "pending_ship"
	case "[我发起了退款申请]":
		return "refunding"
	}
	return ""
}

func isSystemNotice(v Incoming) bool {
	name := strings.TrimSpace(v.SenderName)
	return name == "交易消息" || name == "系统消息" || name == "闲鱼消息"
}

// persistOrderEvent mirrors the original websocket service's card-message
// ingestion. It creates the order before auto-delivery runs and preserves
// terminal states when delayed/duplicated DingTalk messages arrive.
func (m *Manager) persistOrderEvent(ctx context.Context, a Account, v Incoming) {
	status := orderStatusFromMessage(v.Text)
	if status == "" {
		return
	}
	orderNo := extractOrderID(v.Raw)
	if orderNo == "" {
		m.log.Warn("order card did not contain order id", "account", a.AccountID, "message", v.Text)
		return
	}
	raw := map[string]any{"chat_id": v.ChatID, "buyer_name": v.SenderName, "source_message": v.Text, "received_at": v.ReceivedAt.Format(time.RFC3339Nano)}
	encoded, _ := json.Marshal(raw)
	// Terminal orders must never be moved backwards by an out-of-order card.
	_, err := m.db.ExecContext(ctx, `INSERT INTO xy_orders(owner_id,account_id,order_no,item_id,buyer_id,status,data)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(order_no) DO UPDATE SET
		 account_id=excluded.account_id,
		 item_id=CASE WHEN excluded.item_id<>'' THEN excluded.item_id ELSE xy_orders.item_id END,
		 buyer_id=CASE WHEN excluded.buyer_id<>'' THEN excluded.buyer_id ELSE xy_orders.buyer_id END,
		 status=CASE WHEN lower(xy_orders.status) IN ('shipped','completed','cancelled','closed','refunded') THEN xy_orders.status ELSE excluded.status END,
		 data=json_patch(COALESCE(xy_orders.data,'{}'),excluded.data),updated_at=CURRENT_TIMESTAMP`,
		a.OwnerID, a.AccountID, orderNo, v.ItemID, v.SenderID, status, string(encoded))
	if err != nil {
		m.log.Error("persist websocket order", "account", a.AccountID, "order", orderNo, "error", err)
		return
	}
	event, _ := json.Marshal(map[string]any{"order_no": orderNo, "status": status, "item_id": v.ItemID, "buyer_id": v.SenderID})
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'order_status',?,?,?)`, a.OwnerID, a.AccountID, "订单状态已同步", string(event))
	if status == "pending_ship" {
		go m.autoDeliver(context.Background(), a, orderNo, v)
	}
}

type deliveryCard struct {
	ID                           int64
	Content, Original, Remaining string
	Consumable                   bool
}

func (m *Manager) autoDeliver(ctx context.Context, a Account, orderNo string, v Incoming) {
	m.deliveryMu.Lock()
	defer m.deliveryMu.Unlock()
	var status, itemID, buyerID, raw string
	if m.db.QueryRowContext(ctx, `SELECT COALESCE(status,''),COALESCE(item_id,''),COALESCE(buyer_id,''),data FROM xy_orders WHERE owner_id=? AND account_id=? AND order_no=?`, a.OwnerID, a.AccountID, orderNo).Scan(&status, &itemID, &buyerID, &raw) != nil {
		return
	}
	if strings.EqualFold(status, "shipped") || strings.EqualFold(status, "completed") {
		return
	}
	deliveryState := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &deliveryState)
	if boolSetting(deliveryState, "auto_delivered") || boolSetting(deliveryState, "card_only_delivered") {
		return
	}
	if a.DeliveryDisabled || itemID == "" || buyerID == "" || v.ChatID == "" {
		m.recordDeliveryFailure(ctx, a, orderNo, "账号已禁用自动发货，或订单缺少商品、买家、会话信息")
		return
	}
	card, err := m.takeDeliveryCard(ctx, a.OwnerID, a.AccountID, itemID, raw)
	if err != nil {
		m.recordDeliveryFailure(ctx, a, orderNo, err.Error())
		return
	}
	confirmFirst := a.AutoConfirm && a.ConfirmBeforeSend && !a.OnlySendCard
	if confirmFirst {
		if err = m.confirmDelivery(ctx, a, orderNo, itemID, buyerID, raw); err != nil {
			m.restoreDeliveryCard(ctx, card)
			m.recordDeliveryFailure(ctx, a, orderNo, err.Error())
			return
		}
	}
	if _, err = m.SendText(ctx, a.AccountID, v.ChatID, buyerID, card.Content); err != nil {
		m.restoreDeliveryCard(ctx, card)
		m.recordDeliveryFailure(ctx, a, orderNo, "发货消息发送失败: "+err.Error())
		return
	}
	if a.AutoConfirm && !a.OnlySendCard && !confirmFirst {
		if err = m.confirmDelivery(ctx, a, orderNo, itemID, buyerID, raw); err != nil {
			m.recordDeliveryFailure(ctx, a, orderNo, "卡券已发送，但确认发货失败: "+err.Error())
			return
		}
	}
	data := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &data)
	data["delivery_content"] = card.Content
	data["delivery_fail_reason"] = ""
	data["auto_delivered"] = true
	if a.OnlySendCard {
		data["card_only_delivered"] = true
	}
	encoded, _ := json.Marshal(data)
	nextStatus := "shipped"
	if a.OnlySendCard || !a.AutoConfirm {
		nextStatus = status
	}
	_, _ = m.db.ExecContext(ctx, `UPDATE xy_orders SET status=?,delivery_method='auto',data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND order_no=?`, nextStatus, string(encoded), a.OwnerID, orderNo)
	_, _ = m.db.ExecContext(ctx, `UPDATE cards SET data=json_set(data,'$.delivery_count',COALESCE(json_extract(data,'$.delivery_count'),0)+1),updated_at=CURRENT_TIMESTAMP WHERE id=?`, card.ID)
	event, _ := json.Marshal(map[string]any{"order_no": orderNo, "card_id": card.ID, "confirmed": a.AutoConfirm && !a.OnlySendCard})
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'auto_delivery',?,'自动发货成功',?)`, a.OwnerID, a.AccountID, string(event))
}

// Redeliver retries the same idempotent delivery path used by realtime paid
// messages. It is called by the monolith scheduler for messages missed while
// an account was disconnected.
func (m *Manager) Redeliver(ctx context.Context, orderNo string) error {
	var ownerID int64
	var accountID, buyerID, raw string
	if err := m.db.QueryRowContext(ctx, `SELECT owner_id,COALESCE(account_id,''),COALESCE(buyer_id,''),data FROM xy_orders WHERE order_no=?`, orderNo).Scan(&ownerID, &accountID, &buyerID, &raw); err != nil {
		return errors.New("订单不存在")
	}
	var a Account
	if err := m.db.QueryRowContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass,auto_confirm,confirm_before_send,send_before_confirm,only_send_card,delivery_disabled FROM xy_accounts WHERE owner_id=? AND account_id=? AND status='active'`, ownerID, accountID).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass, &a.AutoConfirm, &a.ConfirmBeforeSend, &a.SendBeforeConfirm, &a.OnlySendCard, &a.DeliveryDisabled); err != nil {
		return errors.New("账号不存在或未启用")
	}
	data := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &data)
	if boolSetting(data, "auto_delivered") || boolSetting(data, "card_only_delivered") {
		return nil
	}
	chatID, err := EnsureOrderChat(ctx, m.db, ownerID, accountID, orderNo, m.CreateChat)
	if err != nil {
		m.recordDeliveryFailure(ctx, a, orderNo, err.Error())
		return err
	}
	m.autoDeliver(ctx, a, orderNo, Incoming{AccountID: accountID, SenderID: buyerID, ChatID: chatID, ReceivedAt: time.Now()})
	var updated string
	_ = m.db.QueryRowContext(ctx, `SELECT data FROM xy_orders WHERE order_no=?`, orderNo).Scan(&updated)
	state := map[string]any{}
	_ = json.Unmarshal([]byte(updated), &state)
	if boolSetting(state, "auto_delivered", "card_only_delivered") {
		return nil
	}
	if reason := stringSetting(state, "delivery_fail_reason"); reason != "" {
		return errors.New(reason)
	}
	return errors.New("补发未完成")
}

func (m *Manager) takeDeliveryCard(ctx context.Context, ownerID int64, accountID, itemID, orderData string) (deliveryCard, error) {
	var c deliveryCard
	id, typ, data, err := SelectDeliveryCard(ctx, m.db, ownerID, accountID, itemID, orderData)
	if err != nil {
		return c, err
	}
	c.ID = id
	values := map[string]any{}
	_ = json.Unmarshal([]byte(data), &values)
	switch typ {
	case "text":
		c.Content = strings.TrimSpace(firstNonempty(stringSetting(values, "text_content"), stringSetting(values, "content")))
		if c.Content == "" {
			return c, errors.New("文本卡券内容为空")
		}
	case "data":
		lines := nonemptyDeliveryLines(firstNonempty(stringSetting(values, "data_content"), stringSetting(values, "content")))
		if len(lines) == 0 {
			return c, errors.New("卡密库存不足")
		}
		c.Content, c.Consumable, c.Original = lines[0], true, data
		values["data_content"] = strings.Join(lines[1:], "\n")
		b, _ := json.Marshal(values)
		c.Remaining = string(b)
		res, e := m.db.ExecContext(ctx, `UPDATE cards SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND data=?`, c.Remaining, c.ID, c.Original)
		if e != nil {
			return c, e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return c, errors.New("卡密库存状态已变化")
		}
	default:
		return c, errors.New("自动发货当前仅支持文本或卡密卡券")
	}
	return c, nil
}
func (m *Manager) restoreDeliveryCard(ctx context.Context, c deliveryCard) {
	if c.Consumable {
		_, _ = m.db.ExecContext(ctx, `UPDATE cards SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND data=?`, c.Original, c.ID, c.Remaining)
	}
}
func (m *Manager) confirmDelivery(ctx context.Context, a Account, orderNo, itemID, buyerID, raw string) error {
	data := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &data)
	api := "mtop.taobao.idle.logistic.consign.dummy"
	payload := map[string]any{"orderId": orderNo, "tradeText": "", "picList": []any{}, "newUnconsign": true}
	if boolSetting(data, "is_bargain") {
		api = "mtop.idle.groupon.activity.seller.freeshipping"
		payload = map[string]any{"bizOrderId": orderNo, "itemId": json.Number(itemID), "buyerId": json.Number(buyerID)}
	}
	res := MTOPCall(ctx, MTOPOptions{API: api, Version: "1.0", Cookie: a.Cookie, Proxy: func() string {
		if p := proxyURL(a); p != nil {
			return p.String()
		}
		return ""
	}(), Data: payload})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		_, _ = m.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Cookie, a.ID)
	}
	if !res.Success && !strings.Contains(res.Error, "ORDER_ALREADY_DELIVERY") && !strings.Contains(res.Error, "已发货成功") {
		return errors.New(firstNonempty(res.Error, "确认发货失败"))
	}
	return nil
}
func (m *Manager) recordDeliveryFailure(ctx context.Context, a Account, orderNo, reason string) {
	var raw string
	_ = m.db.QueryRowContext(ctx, `SELECT data FROM xy_orders WHERE owner_id=? AND order_no=?`, a.OwnerID, orderNo).Scan(&raw)
	data := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &data)
	data["delivery_fail_reason"] = reason
	b, _ := json.Marshal(data)
	_, _ = m.db.ExecContext(ctx, `UPDATE xy_orders SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND order_no=?`, string(b), a.OwnerID, orderNo)
	event, _ := json.Marshal(map[string]any{"order_no": orderNo, "error": reason})
	_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,level,message,data) VALUES(?,'auto_delivery',?,'ERROR',?,?)`, a.OwnerID, a.AccountID, reason, string(event))
}
func nonemptyDeliveryLines(value string) []string {
	out := []string{}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
func (m *Manager) matchReply(ctx context.Context, a Account, v Incoming) replyDecision {
	rows, e := m.db.QueryContext(ctx, `SELECT keyword,reply,item_id,type,extra FROM xy_keyword_rules WHERE owner_id=? AND account_id=? ORDER BY CASE WHEN item_id=? THEN 0 WHEN item_id='' THEN 1 ELSE 2 END,id`, a.OwnerID, a.AccountID, v.ItemID)
	if e == nil {
		defer rows.Close()
		for rows.Next() {
			var k, reply, item, typ, extraRaw string
			_ = rows.Scan(&k, &reply, &item, &typ, &extraRaw)
			if item != "" && item != v.ItemID {
				continue
			}
			if k != "" && strings.Contains(v.Text, k) {
				switch typ {
				case "image":
					extra := map[string]any{}
					_ = json.Unmarshal([]byte(extraRaw), &extra)
					imageURL := stringSetting(extra, "image_url", "reply_image", "url")
					if imageURL == "" {
						imageURL = strings.TrimSpace(reply)
					}
					return replyDecision{ImageURL: imageURL}
				case "text", "":
					return replyDecision{Text: reply}
				default:
					return replyDecision{Suppress: true}
				}
			}
		}
	}
	if reply := m.aiReply(ctx, a, v); reply != "" {
		return replyDecision{Text: reply}
	}
	if v.ItemID != "" {
		var raw string
		if m.db.QueryRowContext(ctx, `SELECT data FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, a.OwnerID, a.AccountID, v.ItemID).Scan(&raw) == nil {
			item := map[string]any{}
			_ = json.Unmarshal([]byte(raw), &item)
			if settings, ok := item["default_reply"].(map[string]any); ok {
				if decision := m.defaultReplyDecision(ctx, a, v, settings, v.ItemID); decision.Text != "" || decision.ImageURL != "" || len(decision.RawPayload) > 0 || decision.Suppress {
					return decision
				}
			}
		}
	}
	var raw, legacy string
	var enabled bool
	if m.db.QueryRowContext(ctx, `SELECT data,reply,enabled FROM default_replies WHERE owner_id=? AND account_id=? ORDER BY id LIMIT 1`, a.OwnerID, a.AccountID).Scan(&raw, &legacy, &enabled) != nil || !enabled {
		return replyDecision{}
	}
	settings := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &settings)
	if stringSetting(settings, "reply_content") == "" && legacy != "" {
		settings["reply_content"] = legacy
	}
	return m.defaultReplyDecision(ctx, a, v, settings, "")
}

func (m *Manager) defaultReplyDecision(ctx context.Context, a Account, v Incoming, settings map[string]any, itemID string) replyDecision {
	if !boolSetting(settings, "enabled") {
		return replyDecision{}
	}
	replyType := stringSetting(settings, "reply_type")
	if replyType == "" {
		replyType = "text"
	}
	once := boolSetting(settings, "reply_once")
	if once {
		var exists int
		_ = m.db.QueryRowContext(ctx, `SELECT count(*) FROM default_reply_records WHERE owner_id=? AND account_id=? AND item_id=? AND user_id=?`, a.OwnerID, a.AccountID, itemID, v.SenderID).Scan(&exists)
		if exists > 0 {
			return replyDecision{Suppress: true}
		}
	}
	text := ""
	switch replyType {
	case "text":
		text = stringSetting(settings, "reply_content")
	case "api":
		text = m.callDefaultReplyAPI(ctx, a, v, settings)
	case "external_contact":
		payload := m.callRemoteLocationMessage(ctx, a, v, settings)
		if len(payload) == 0 {
			return replyDecision{Suppress: true}
		}
		return replyDecision{RawPayload: payload, RecordOnce: once, RecordItemID: itemID}
	default:
		return replyDecision{Suppress: true}
	}
	if text == "" && stringSetting(settings, "reply_image") == "" {
		return replyDecision{Suppress: true}
	}
	return replyDecision{Text: text, ImageURL: stringSetting(settings, "reply_image"), RecordOnce: once, RecordItemID: itemID}
}

func (m *Manager) callRemoteLocationMessage(ctx context.Context, a Account, v Incoming, settings map[string]any) []byte {
	var remoteURL, secret string
	rows, err := m.db.QueryContext(ctx, `SELECT key,value FROM user_settings WHERE owner_id=? AND key IN ('location_chat.remote_url','location_chat.remote_secret_key')`, a.OwnerID)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var key, value string
		_ = rows.Scan(&key, &value)
		if key == "location_chat.remote_url" {
			remoteURL = strings.TrimSpace(value)
		} else {
			secret = strings.TrimSpace(value)
		}
	}
	_ = rows.Close()
	parsed, err := url.Parse(remoteURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || secret == "" {
		return nil
	}
	port := parsed.Port()
	if port != "" && port != "80" && port != "443" {
		return nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil
	}
	for _, address := range addresses {
		if !address.IP.IsGlobalUnicast() || address.IP.IsPrivate() || address.IP.IsLoopback() || address.IP.IsLinkLocalUnicast() || address.IP.IsUnspecified() {
			return nil
		}
	}
	longitude, longErr := strconv.ParseFloat(stringSetting(settings, "location_longitude"), 64)
	latitude, latErr := strconv.ParseFloat(stringSetting(settings, "location_latitude"), 64)
	title := stringSetting(settings, "location_title")
	if longErr != nil || latErr != nil || longitude < -180 || longitude > 180 || latitude < -90 || latitude > 90 || title == "" || v.ChatID == "" || v.SenderID == "" || a.AccountID == "" {
		return nil
	}
	mid, uuid := Mid(), UUID()
	location := map[string]any{"longitude": stringSetting(settings, "location_longitude"), "latitude": stringSetting(settings, "location_latitude"), "title": title}
	if subtitle := stringSetting(settings, "location_subtitle"); subtitle != "" {
		location["subtitle"] = subtitle
	}
	requestBody, _ := json.Marshal(map[string]any{"type": "xianyu_location_message", "data": map[string]any{"protocol_version": "xianyu-location-envelope-v1", "mid": mid, "uuid": uuid, "chat_id": trimGoofish(v.ChatID), "send_user_id": trimGoofish(v.SenderID), "sender_user_id": trimGoofish(a.AccountID), "location": location}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(requestBody))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", secret)
	client := &http.Client{Timeout: 15 * time.Second, Transport: proxy.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		m.log.Warn("remote location reply failed", "account", a.AccountID, "error", err)
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil
	}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil || !boolSetting(envelope, "success") {
		return nil
	}
	data, _ := envelope["data"].(map[string]any)
	message, _ := data["message"].(map[string]any)
	headers, _ := message["headers"].(map[string]any)
	messageBody, _ := message["body"].([]any)
	if len(message) == 0 || fmt.Sprint(headers["mid"]) != mid || len(messageBody) < 1 {
		return nil
	}
	first, _ := messageBody[0].(map[string]any)
	if fmt.Sprint(first["uuid"]) != uuid {
		return nil
	}
	payload, _ := json.Marshal(message)
	return payload
}

func (m *Manager) callDefaultReplyAPI(ctx context.Context, a Account, v Incoming, settings map[string]any) string {
	rawURL := stringSetting(settings, "api_url")
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return ""
	}
	for _, address := range addresses {
		if !address.IP.IsGlobalUnicast() || address.IP.IsPrivate() || address.IP.IsLoopback() || address.IP.IsLinkLocalUnicast() || address.IP.IsUnspecified() {
			m.log.Warn("default reply API blocked unsafe address", "account", a.AccountID, "host", parsed.Hostname())
			return ""
		}
	}
	timeout := intSetting(settings, "api_timeout")
	if timeout < 1 {
		timeout = 80
	}
	if timeout > 120 {
		timeout = 120
	}
	payload, _ := json.Marshal(map[string]any{"account_id": a.AccountID, "message": v.Text, "chat_id": v.ChatID, "item_id": v.ItemID, "send_user_id": v.SenderID, "send_user_name": v.SenderName, "msg_time": v.ReceivedAt.Format(time.RFC3339)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second, Transport: proxy.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		m.log.Warn("default reply API failed", "account", a.AccountID, "error", err)
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(body))
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return text
	}
	switch value := decoded.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		if success, exists := value["success"]; exists && !boolSetting(map[string]any{"success": success}, "success") {
			return ""
		}
		return stringSetting(value, "reply", "data", "content", "message")
	default:
		return ""
	}
}

func (m *Manager) aiReply(ctx context.Context, a Account, v Incoming) string {
	var raw string
	var enabled bool
	err := m.db.QueryRowContext(ctx, `SELECT data,enabled FROM resource_records WHERE owner_id=? AND kind='ai_reply' AND account_id=? ORDER BY id DESC LIMIT 1`, a.OwnerID, a.AccountID).Scan(&raw, &enabled)
	if err != nil || !enabled {
		return ""
	}
	settings := map[string]any{}
	if json.Unmarshal([]byte(raw), &settings) != nil || !boolSetting(settings, "ai_enabled", "enabled") {
		return ""
	}
	provider := stringSetting(settings, "provider_type", "provider")
	if provider == "" {
		provider = "openai_compatible"
	}
	systemPrompt := stringSetting(settings, "custom_prompts", "prompt")
	if strings.HasPrefix(systemPrompt, "{") {
		var prompts map[string]any
		if json.Unmarshal([]byte(systemPrompt), &prompts) == nil {
			systemPrompt = stringSetting(prompts, "system_prompt", "prompt", "default")
		}
	}
	prompt := fmt.Sprintf("买家消息：%s", v.Text)
	if v.ItemID != "" {
		prompt += "\n商品ID：" + v.ItemID
	}
	reply, err := ai.New().Generate(ctx, ai.Config{
		Provider:     provider,
		BaseURL:      stringSetting(settings, "base_url", "api_url"),
		APIKey:       stringSetting(settings, "api_key"),
		Model:        stringSetting(settings, "model_name", "model"),
		AppID:        stringSetting(settings, "app_id"),
		SystemPrompt: systemPrompt,
		Timeout:      45 * time.Second,
	}, prompt)
	if err != nil {
		m.log.Warn("AI reply failed", "account", a.AccountID, "error", err)
		data, _ := json.Marshal(map[string]any{"status": "failed", "error": err.Error(), "item_id": v.ItemID})
		_, _ = m.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,account_id,message,data) VALUES(?,'ai_reply',?,?,?)`, a.OwnerID, a.AccountID, "AI回复失败", string(data))
		return ""
	}
	return strings.TrimSpace(reply)
}

func stringSetting(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func boolSetting(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case float64:
			return typed != 0
		case string:
			return typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
		}
	}
	return false
}

func intSetting(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	default:
		return 0
	}
}

func ParseIncoming(account string, raw []byte) (Incoming, bool) {
	values := ParseIncomingMany(account, raw)
	for _, incoming := range values {
		if incoming.Type == "text" && strings.TrimSpace(incoming.Text) != "" {
			return incoming, true
		}
	}
	if len(values) > 0 {
		return values[0], true
	}
	return Incoming{}, false
}

func ParseIncomingMany(account string, raw []byte) []Incoming {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	// Current Goofish IM wraps actual messages in Base64 MessagePack entries.
	// Decode every entry because one frame can contain multiple buyer messages.
	result := []Incoming{}
	if body := object(root["body"]); body != nil {
		if pack := object(body["syncPushPackage"]); pack != nil {
			if entries, ok := pack["data"].([]any); ok {
				for _, entry := range entries {
					encoded := text(object(entry)["data"])
					packed, err := base64.StdEncoding.DecodeString(encoded)
					if err != nil {
						continue
					}
					decoded, err := decodeMessagePack(packed)
					if err != nil {
						continue
					}
					if candidate := object(decoded); candidate != nil {
						if incoming, ok := parseIncomingObject(account, candidate, raw); ok {
							result = append(result, incoming)
						}
					}
				}
			}
		}
	}
	if len(result) > 0 {
		return result
	}
	if incoming, ok := parseIncomingObject(account, root, raw); ok {
		return []Incoming{incoming}
	}
	return nil
}

func parseIncomingObject(account string, root map[string]any, raw []byte) (Incoming, bool) {
	msg := object(root["body"])
	if msg == nil {
		msg = root
	}
	one := object(msg["1"])
	if one == nil {
		return Incoming{}, false
	}
	ten := object(one["10"])
	v := Incoming{AccountID: account, Raw: append([]byte(nil), raw...), ReceivedAt: time.Now(), Type: "text"}
	v.ChatID = trimGoofish(firstNonempty(text(one["3"]), text(one["2"])))
	if ten != nil {
		v.SenderID = text(ten["senderUserId"])
		v.SenderName = text(ten["senderNick"])
		if v.SenderName == "" {
			v.SenderName = text(ten["reminderTitle"])
		}
		v.Text = text(ten["reminderContent"])
		v.ItemID = itemID(ten)
		if sid := fieldQueryValue(ten, "sid"); sid != "" {
			v.ChatID = sid
		}
	} else {
		sender := object(one["1"])
		if sender != nil {
			v.SenderID = trimGoofish(text(sender["1"]))
		}
		v.SenderName = "系统"
	}
	six := object(one["6"])
	three := object(nil)
	if six != nil {
		three = object(six["3"])
	}
	if three != nil {
		if c := decodeContent(text(three["5"])); c != nil {
			applyContent(&v, c)
		} else if c := decodeContent(text(three["1"])); c != nil {
			applyContent(&v, c)
		}
		if v.Text == "" {
			v.Text = text(three["2"])
		}
		if v.ItemID == "" {
			v.ItemID = itemIDDeep(three)
		}
	}
	return v, v.ChatID != "" && v.SenderID != ""
}
func object(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case []any:
		if len(x) > 0 {
			return object(x[0])
		}
	case string:
		var decoded any
		if json.Unmarshal([]byte(x), &decoded) == nil {
			return object(decoded)
		}
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(x, &decoded) == nil {
			return object(decoded)
		}
	case []byte:
		var decoded any
		if json.Unmarshal(x, &decoded) == nil {
			return object(decoded)
		}
	}
	return nil
}
func text(v any) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}
func trimGoofish(v string) string { return strings.Split(v, "@")[0] }
func decodeContent(v string) map[string]any {
	if v == "" {
		return nil
	}
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(v), "{") && json.Unmarshal([]byte(v), &out) == nil {
		return out
	}
	b, e := base64.StdEncoding.DecodeString(v)
	if e == nil && json.Unmarshal(b, &out) == nil {
		return out
	}
	return nil
}
func applyContent(v *Incoming, c map[string]any) {
	ct := int64(0)
	switch x := c["contentType"].(type) {
	case float64:
		ct = int64(x)
	case int64:
		ct = x
	}
	if ct == 3 || c["audio"] != nil {
		v.Type = "audio"
		v.Text = "[语音消息]"
		return
	}
	if im := object(c["image"]); im != nil {
		v.Type = "image"
		if pics, ok := im["pics"].([]any); ok {
			for _, p := range pics {
				if u := text(object(p)["url"]); u != "" {
					v.Images = append(v.Images, u)
				}
			}
		}
		v.Text = strings.Join(v.Images, "\n")
		return
	}
	if p := text(c["picUrl"]); p != "" {
		v.Type = "image"
		v.Images = []string{p}
		v.Text = p
		return
	}
	if t := c["text"]; t != nil {
		if o := object(t); o != nil {
			v.Text = text(o["text"])
		} else {
			v.Text = text(t)
		}
		v.Type = "text"
	}
}
func itemID(m map[string]any) string {
	for _, k := range []string{"reminderUrl", "bizTag", "extJson"} {
		s := text(m[k])
		if strings.Contains(s, "itemId=") {
			return strings.Split(strings.Split(s, "itemId=")[1], "&")[0]
		}
		var v map[string]any
		if json.Unmarshal([]byte(s), &v) == nil {
			if id := text(v["itemId"]); id != "" {
				return id
			}
		}
	}
	return ""
}

func fieldQueryValue(m map[string]any, name string) string {
	for _, key := range []string{"reminderUrl", "bizTag", "extJson"} {
		raw := text(m[key])
		if parsed, err := url.Parse(raw); err == nil {
			if value := strings.TrimSpace(parsed.Query().Get(name)); value != "" {
				return value
			}
		}
	}
	return ""
}
func itemIDDeep(m map[string]any) string {
	b, _ := json.Marshal(m)
	s := string(b)
	if i := strings.Index(s, "itemId="); i >= 0 {
		x := s[i+7:]
		for j, c := range x {
			if c == '&' || c == '"' || c == '\\' {
				return x[:j]
			}
		}
	}
	return ""
}
