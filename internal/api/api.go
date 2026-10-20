package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	"github.com/zhinianboke/xianyu-auto-go/internal/auth"
	"github.com/zhinianboke/xianyu-auto-go/internal/config"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type Server struct {
	db              *sql.DB
	cfg             config.Config
	log             *slog.Logger
	ws              *Hub
	online          func(string) bool
	triggerTask     func(context.Context, string) error
	taskRunning     func(string) bool
	chatSend        func(context.Context, string, string, string, string) (string, error)
	chatCreate      func(context.Context, string, string, string) (string, error)
	itemDetailFetch func(context.Context, platformAccount, string) xianyu.MTOPResult
	orderPlace      func(context.Context, platformAccount, string) (string, bool, error)
	cookieRefresh   func(context.Context, string) (string, []string, error)
	chatRecall      func(context.Context, string, string) error
	chatImage       func(context.Context, string, string, string, string, int, int) (string, error)
	accountRestart  func(string)
	serviceRestart  func(string) error
	crawlMu         sync.Mutex
	crawlJobs       map[int64]context.CancelFunc
	crawlTokens     map[int64]string
	qrLogin         *qrLoginManager
	geetestMu       sync.Mutex
	geetestStatus   map[string]geetestState
	agreePickupMu   sync.Mutex
	passwordMu      sync.Mutex
	passwordLogins  map[string]*passwordLoginSession
}
type response struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}
type userKey struct{}
type User struct {
	ID             int64
	Username, Role string
	AccountLimit   sql.NullInt64
}

func New(db *sql.DB, cfg config.Config, log *slog.Logger) *Server {
	return &Server{db: db, cfg: cfg, log: log, ws: NewHub(), crawlJobs: map[int64]context.CancelFunc{}, crawlTokens: map[int64]string{}, qrLogin: newQRLoginManager(), geetestStatus: map[string]geetestState{}, passwordLogins: map[string]*passwordLoginSession{}}
}
func (s *Server) SetOnlineChecker(fn func(string) bool) { s.online = fn }
func (s *Server) SetScheduler(trigger func(context.Context, string) error, running func(string) bool) {
	s.triggerTask = trigger
	s.taskRunning = running
}
func (s *Server) SetChatSender(send func(context.Context, string, string, string, string) (string, error)) {
	s.chatSend = send
}
func (s *Server) SetChatCreator(create func(context.Context, string, string, string) (string, error)) {
	s.chatCreate = create
}
func (s *Server) SetItemDetailFetcher(fetch func(context.Context, platformAccount, string) xianyu.MTOPResult) {
	s.itemDetailFetch = fetch
}
func (s *Server) SetOrderPlacer(place func(context.Context, platformAccount, string) (string, bool, error)) {
	s.orderPlace = place
}
func (s *Server) SetCookieRefresher(refresh func(context.Context, string) (string, []string, error)) {
	s.cookieRefresh = refresh
}
func (s *Server) SetChatRecaller(recall func(context.Context, string, string) error) {
	s.chatRecall = recall
}
func (s *Server) SetChatImageSender(send func(context.Context, string, string, string, string, int, int) (string, error)) {
	s.chatImage = send
}
func (s *Server) SetAccountRestarter(restart func(string))       { s.accountRestart = restart }
func (s *Server) SetServiceRestarter(restart func(string) error) { s.serviceRestart = restart }
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, s.cors, s.accessLog)
	r.Get("/health", s.health)
	r.Get("/api/v1/health", s.health)
	r.Get("/api/v1/health/ping", s.health)
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Post("/auth/refresh", s.refresh)
		r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) { ok(w, "已退出登录", nil) })
		r.Get("/auth/verify", s.verify)
		r.Get("/system-settings/public", s.publicSettings)
		r.Get("/agree-pickup/order", s.agreePickupOrder)
		r.Post("/agree-pickup/agree", s.agreePickupAgree)
		r.Get("/version/current", func(w http.ResponseWriter, r *http.Request) {
			write(w, 200, map[string]any{"version": "go-1.0.0", "name": "闲鱼自动回复 Go 版"})
		})
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/check-default-password", s.checkDefaultPassword)
			r.Post("/qr-login/generate", s.qrLoginGenerate)
			r.Get("/qr-login/status/{session_id}", s.qrLoginStatus)
			r.Get("/qr-login/cookie/{session_id}", s.qrLoginCookie)
			r.Post("/password-login", s.passwordLoginStart)
			r.Get("/password-login/check/{session_id}", s.passwordLoginCheck)
			r.Delete("/password-login/cancel/{session_id}", s.passwordLoginCancel)
			r.Get("/db-backup-logs", s.dbBackupLogs)
			r.Get("/db-backup-logs/{log_id}/download", s.dbBackupDownload)
			r.Get("/auto-reply-logs", s.autoReplyLogs)
			r.Get("/user-login-logs", s.userLoginLogs)
			r.Get("/account-login-logs", s.accountLoginLogs)
			r.Post("/items/search", s.searchItemsLegacy)
			r.Post("/search/items", s.searchItemsAPI)
			r.Post("/compass/goofish/search", s.goofishCompassSearch)
			r.Get("/cookie-refresh/cooldown/{account_id}", s.cookieRefreshCooldown)
			r.Post("/cookie-refresh/cooldown/{account_id}/reset", s.cookieRefreshCooldownReset)
			r.Post("/cookie-refresh/trigger/{account_id}", s.cookieRefreshTrigger)
			r.Get("/face-verification/notifications", s.faceVerificationNotifications)
			r.Get("/face-verification/notifications/{account_id}", s.faceVerificationAccount)
			r.Post("/face-verification/notifications/{notification_id}/read", s.faceVerificationRead)
			r.Get("/face-verification/screenshot/{account_id}", s.faceVerificationScreenshot)
			r.Delete("/face-verification/screenshot/{account_id}", s.faceVerificationScreenshotDelete)
			r.Post("/data-analysis/seller-summary", s.dataAnalysisSummary("mtop.alibaba.idle.seller.pc.datacompass.singleuser.seller.summary", false))
			r.Post("/data-analysis/browse-summary", s.dataAnalysisSummary("mtop.alibaba.idle.seller.pc.datacompass.singleuser.browse.summary", true))
			r.Get("/product-monitor/collect-fallback-accounts", s.fallbackAccountList("collect_fallback_account"))
			r.Put("/product-monitor/collect-fallback-accounts", s.fallbackAccountUpsert("collect_fallback_account", "兜底采集账号保存成功"))
			r.Delete("/product-monitor/collect-fallback-accounts", s.fallbackAccountDelete("collect_fallback_account"))
			r.Get("/product-monitor/order-fallback-accounts", s.fallbackAccountList("order_fallback_account"))
			r.Put("/product-monitor/order-fallback-accounts", s.fallbackAccountUpsert("order_fallback_account", "兜底下单账号保存成功"))
			r.Delete("/product-monitor/order-fallback-accounts", s.fallbackAccountDelete("order_fallback_account"))
			r.Get("/agree-deliver/pickup-url/suggestion", s.pickupURLSuggestion)
			r.Post("/user-settings/external-api/location-chat/test", s.locationChatRemoteTest)
			r.Get("/version/check", s.versionCheck)
			r.Post("/upload/upload-image", s.uploadGeneralImage)
			r.Post("/cards/upload-image", s.uploadCardImage)
			r.Post("/default-replies/{account_id}/upload-image", s.uploadDefaultReplyImage)
			r.Post("/confirm-receipt-messages/{account_id}/upload-image", s.uploadConfirmReceiptImage)
			r.Get("/system-control/status", s.systemControlStatus)
			r.Post("/system-control/restart/{service_key}", s.systemControlRestart)
			r.Route("/cookies", s.accountRoutes)
			r.Route("/keywords-with-item-id", s.keywordRoutes)
			r.Route("/items", s.itemRoutes)
			r.Route("/orders", s.orderRoutes)
			r.Route("/cards", s.cardRoutes)
			r.Route("/system-settings", s.settingRoutes)
			r.Post("/system-settings/test-email", s.testEmail)
			r.Route("/user-settings", s.userSettingRoutes)
			r.Route("/users", func(r chi.Router) {
				r.Get("/me", s.userMe)
				r.Post("/change-password", s.changePassword)
			})
			r.Route("/default-replies", s.defaultReplyRoutes)
			r.Route("/proxy", s.proxyRoutes)
			r.Route("/ai-reply-settings", func(r chi.Router) {
				s.accountConfigRoutes(r, "ai_reply", map[string]any{"enabled": false, "provider": "openai", "model": "", "api_url": "", "api_key": "", "prompt": ""})
				r.Put("/", s.bulkAccountConfigs("ai_reply"))
			})
			r.Route("/auto-rate", func(r chi.Router) {
				s.accountConfigRoutes(r, "auto_rate", map[string]any{"enabled": false})
				r.Post("/batch-rate", s.autoRateBatch)
			})
			r.Route("/refund-cancel", func(r chi.Router) {
				s.accountConfigRoutes(r, "refund_cancel", map[string]any{"enabled": false, "url": "", "timeout": 60})
			})
			r.Route("/agree-deliver", func(r chi.Router) {
				s.accountConfigRoutes(r, "agree_deliver", map[string]any{"enabled": false, "notify_message": "", "pickup_url": ""})
			})
			r.Route("/confirm-receipt-messages", func(r chi.Router) {
				s.accountConfigRoutes(r, "confirm_receipt", map[string]any{"enabled": false, "message_content": "", "image_url": ""})
			})
			s.registerBaseAliases(r)
			s.registerResources(r)
			s.registerChatRoutes(r)
			s.registerProductPublishRoutes(r)
			s.registerListingMonitorRoutes(r)
			s.registerAIRoutes(r)
			s.registerAIListingRoutes(r)
			s.registerBlacklistRoutes(r)
			s.registerNotificationRoutes(r)
			s.registerCrawlerRoutes(r)
			s.registerRiskRoutes(r)
			r.Get("/chat-new/ws/{accountID}", s.chatWS)
			r.Route("/admin", s.personalAdminRoutes)
			s.registerCompatibility(r)
		})
	})
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(s.cfg.StaticDir, "static")))))
	r.Handle("/uploads/*", http.StripPrefix("/uploads/", http.FileServer(http.Dir(filepath.Join(s.cfg.DataDir, "uploads")))))
	r.Get("/*", s.spa)
	return r
}

// registerBaseAliases keeps compatibility with the original FastAPI routes,
// which accept collection URLs without a trailing slash. Chi intentionally
// distinguishes both forms, so the canonical aliases are registered here.
func (s *Server) registerBaseAliases(r chi.Router) {
	r.Get("/cookies", s.accountRawList)
	r.Post("/cookies", s.accountCreate)
	r.Get("/keywords-with-item-id", s.keywordsAll)
	r.Get("/items", s.items)
	r.Get("/orders", s.orders)
	r.Get("/cards", s.cards)
	r.Post("/cards", s.cardCreate)
	r.Get("/system-settings", s.settings)
	r.Get("/user-settings", s.userSettings)
	r.Get("/default-replies", s.defaultReplies)
	r.Get("/ai-reply-settings", s.accountConfigs("ai_reply"))
	r.Put("/ai-reply-settings", s.bulkAccountConfigs("ai_reply"))

	resources := []struct {
		path string
		spec resourceSpec
	}{
		{"/product-monitor/categories", resourceSpec{"listing_monitor_category", "category", false}},
		{"/product-publish/addresses", resourceSpec{"publish_address", "address", false}},
		{"/product-publish/personal-addresses", resourceSpec{"personal_address", "address", false}},
		{"/notification-channels", resourceSpec{"notification_channel", "channel", true}},
	}
	for _, item := range resources {
		r.Get(item.path, s.resourceList(item.spec))
		r.Post(item.path, s.resourceCreate(item.spec))
	}
}
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) accountRoutes(r chi.Router) {
	r.Get("/", s.accountRawList)
	r.Get("/options", s.accountOptions)
	r.Get("/details", s.accountRawList)
	r.Get("/details/paginated", s.accountList)
	r.Get("/stats", s.accountStats)
	r.Get("/stats/order-trend", s.accountOrderTrend)
	r.Post("/", s.accountCreate)
	r.Put("/status/batch", s.accountBatchStatus)
	r.Put("/close-notice/batch", s.accountCloseNoticeBatch)
	r.Put("/clear-token-cache/batch", s.accountClearTokenBatch)
	r.Post("/renew-login", s.accountRenewLogin)
	r.Get("/delivery-block-rules/available", s.deliveryBlockAvailable)
	r.Get("/{id}/delivery-block-rules", s.deliveryBlockRules)
	r.Put("/{id}/delivery-block-rules", s.deliveryBlockRulesPut)
	r.Post("/export", s.accountExportExcel)
	r.Post("/import", s.accountImportExcel)
	r.Put("/{id}", s.accountUpdateCookie)
	r.Put("/{id}/status", s.accountField("status", "enabled"))
	r.Put("/{id}/remark", s.accountField("remark", "remark"))
	r.Put("/{id}/auto-confirm", s.accountField("auto_confirm", "auto_confirm"))
	r.Put("/{id}/pause-duration", s.accountField("pause_duration", "pause_duration"))
	r.Put("/{id}/message-expire-time", s.accountField("message_expire_time", "message_expire_time"))
	r.Put("/{id}/reply-delay", s.accountField("reply_delay_seconds", "reply_delay_seconds"))
	r.Put("/{id}/scheduled-redelivery", s.accountField("scheduled_redelivery", "scheduled_redelivery"))
	r.Put("/{id}/scheduled-rate", s.accountField("scheduled_rate", "scheduled_rate"))
	r.Put("/{id}/auto-polish", s.accountField("auto_polish", "auto_polish"))
	r.Put("/{id}/confirm-before-send", s.accountField("confirm_before_send", "confirm_before_send"))
	r.Put("/{id}/send-before-confirm", s.accountField("send_before_confirm", "send_before_confirm"))
	r.Put("/{id}/only-send-card", s.accountField("only_send_card", "only_send_card"))
	r.Put("/{id}/auto-red-flower", s.accountField("auto_red_flower", "auto_red_flower"))
	r.Put("/{id}/ai-reply-block-ordered-users", s.accountField("ai_reply_block_ordered_users", "ai_reply_block_ordered_users"))
	r.Put("/{id}/delivery-disabled", s.accountDeliveryDisabled)
	r.Put("/{id}/login-info", s.accountLoginInfo)
	r.Delete("/{id}", s.accountDelete)
}
func (s *Server) keywordRoutes(r chi.Router) {
	r.Get("/", s.keywordsAll)
	r.Get("/{accountID}/export", s.keywordExportExcel)
	r.Post("/{accountID}/import", s.keywordImportExcel)
	r.Get("/{accountID}", s.keywords)
	r.Post("/{accountID}", s.keywordsReplace)
	r.Put("/{accountID}/{keyword}", s.keywordUpdate)
	r.Delete("/{accountID}/{keyword}", s.keywordDelete)
	r.Post("/{accountID}/image", s.keywordImage)
}
func (s *Server) itemRoutes(r chi.Router) {
	r.Get("/", s.items)
	r.Get("/paginated", s.itemsPaginated)
	r.Get("/cookie/{accountID}", s.itemsByAccount)
	r.Get("/selectable/all", s.itemSelectable)
	r.Get("/by-card/{cardID}", s.itemsByCard)
	r.Delete("/delete", s.itemDelete)
	r.Delete("/batch", s.itemBatchDelete)
	r.Post("/get-by-page", s.fetchItemsPage)
	r.Post("/get-all-from-account", s.fetchItemsAll)
	r.Post("/batch-offline", s.batchOfflineItems)
	r.Post("/batch-delete-xianyu", s.batchDeletePlatformItems)
	r.Post("/{accountID}/batch-default-reply", s.itemBatchSubPut("default_reply"))
	r.Post("/{accountID}/batch-delete-default-reply", s.itemBatchSubDelete("default_reply"))
	r.Post("/{accountID}/batch-ai-prompt", s.itemBatchSubPut("ai_prompt"))
	r.Post("/{accountID}/batch-delete-ai-prompt", s.itemBatchSubDelete("ai_prompt"))
	r.Post("/{accountID}/batch-default-reply/upload-image", s.itemReplyImage)
	r.Post("/{accountID}/{itemID}/default-reply/upload-image", s.itemReplyImage)
	r.Put("/{accountID}/{itemID}", s.itemUpdate)
	r.Get("/{accountID}/{itemID}", s.itemDetail)
	r.Delete("/{accountID}/{itemID}", s.itemDeletePath)
	r.Put("/{accountID}/{itemID}/multi-quantity-delivery", s.itemFlag("multi_quantity_delivery"))
	r.Put("/{accountID}/{itemID}/multi-spec", s.itemFlag("is_multi_spec"))
	r.Put("/{accountID}/{itemID}/price", s.itemMerge)
	r.Get("/{accountID}/{itemID}/seller-detail", s.sellerItemDetail)
	r.Put("/{accountID}/{itemID}/seller-edit", s.sellerItemEdit)
	r.Get("/{accountID}/{itemID}/ai-prompt", s.itemSubGet("ai_prompt"))
	r.Put("/{accountID}/{itemID}/ai-prompt", s.itemSubPut("ai_prompt"))
	r.Get("/{accountID}/{itemID}/default-reply", s.itemSubGet("default_reply"))
	r.Put("/{accountID}/{itemID}/default-reply", s.itemSubPut("default_reply"))
	r.Delete("/{accountID}/{itemID}/default-reply", s.itemSubDelete("default_reply"))
}
func (s *Server) orderRoutes(r chi.Router) {
	r.Get("/", s.orders)
	r.Post("/no-logistics-delivery", s.orderNoLogisticsDelivery)
	r.Post("/cancel", s.orderCancel)
	r.Post("/manual-delivery", s.orderManualDelivery)
	r.Get("/{orderNo}", s.order)
	r.Delete("/{id}", s.orderDelete)
	r.Post("/batch-delete", s.orderBatchDelete)
	r.Post("/fetch-xianyu", s.fetchOrders)
}
func (s *Server) cardRoutes(r chi.Router) {
	r.Get("/", s.cards)
	r.Post("/", s.cardCreate)
	r.Get("/selectable", s.cardSelectable)
	r.Get("/selectable/all", s.cardSelectable)
	r.Post("/batch-save", s.cardBatchSave)
	r.Post("/batch-bind", s.cardBatchBind)
	r.Post("/batch-clear-item-relations", s.cardBatchClearRelations)
	r.Post("/batch-delete", s.cardBatchDelete)
	r.Get("/item/{itemID}", s.cardsByItem)
	r.Put("/item/{itemID}/cards", s.itemCardsUpdate)
	r.Delete("/relation/{cardID}/{itemID}", s.cardRelationDelete)
	r.Get("/{id}", s.card)
	r.Put("/{id}", s.cardUpdate)
	r.Delete("/{id}", s.cardDelete)
	r.Get("/{id}/items", s.cardItems)
	r.Put("/{id}/items", s.cardItemsUpdate)
}

func (s *Server) cardSelectable(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, n := page(r)
	search := r.URL.Query().Get("search")
	q := ` FROM cards WHERE 1=1`
	args := []any{}
	if u.Role != "ADMIN" {
		q += ` AND user_id=?`
		args = append(args, u.ID)
	}
	if search != "" {
		q += ` AND name LIKE ?`
		args = append(args, "%"+search+"%")
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+q, args...).Scan(&total)
	if strings.HasSuffix(r.URL.Path, "/all") {
		n = 9999
		p = 1
	}
	args = append(args, n, (p-1)*n)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,name,type,enabled,data`+q+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, typ, data string
		var enabled bool
		_ = rows.Scan(&id, &name, &typ, &enabled, &data)
		m := map[string]any{"id": id, "name": name, "type": typ, "enabled": enabled, "source": "own", "unique_key": fmt.Sprintf("own:%d", id)}
		mergeJSON(m, data)
		list = append(list, m)
	}
	write(w, 200, map[string]any{"list": list, "total": total, "page": p, "page_size": n, "total_pages": pages(total, n)})
}
func (s *Server) cardsByItem(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query, args := `SELECT c.id,c.name,c.type,c.enabled,c.data FROM cards c JOIN card_item_relations r ON r.card_id=c.id WHERE r.item_id=?`, []any{chi.URLParam(r, "itemID")}
	if u.Role != "ADMIN" {
		query += ` AND c.user_id=?`
		args = append(args, u.ID)
	}
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, typ, data string
		var enabled bool
		_ = rows.Scan(&id, &name, &typ, &enabled, &data)
		m := map[string]any{"id": id, "name": name, "type": typ, "enabled": enabled}
		mergeJSON(m, data)
		list = append(list, m)
	}
	write(w, 200, list)
}
func (s *Server) itemCardsUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		Items []struct {
			CardID int64  `json:"card_id"`
			Source string `json:"source"`
			DockID *int64 `json:"dock_record_id"`
		} `json:"card_items"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	item := chi.URLParam(r, "itemID")
	tx, _ := s.db.BeginTx(r.Context(), nil)
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM card_item_relations WHERE item_id=? AND card_id IN (SELECT id FROM cards WHERE user_id=?)`, item, u.ID)
	for _, x := range p.Items {
		_, _ = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO card_item_relations(card_id,item_id,source,dock_record_id) SELECT id,?,?,? FROM cards WHERE id=? AND user_id=?`, item, x.Source, x.DockID, x.CardID, u.ID)
	}
	_ = tx.Commit()
	ok(w, "关联更新成功", nil)
}
func (s *Server) cardRelationDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM card_item_relations WHERE card_id=? AND item_id=? AND card_id IN (SELECT id FROM cards WHERE user_id=?)`, chi.URLParam(r, "cardID"), chi.URLParam(r, "itemID"), u.ID)
	affected(w, res, e, "关联删除成功")
}
func (s *Server) cardBatchClearRelations(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []string `json:"item_ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择商品")
		return
	}
	args := []any{u.ID}
	for _, id := range p.IDs {
		args = append(args, id)
	}
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM card_item_relations WHERE card_id IN (SELECT id FROM cards WHERE user_id=?) AND item_id IN (`+placeholders(len(p.IDs))+`)`, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	n, _ := res.RowsAffected()
	ok(w, "关联已清空", map[string]any{"deleted_count": n})
}
func (s *Server) cardBatchBind(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		CardIDs []int64  `json:"card_ids"`
		ItemIDs []string `json:"item_ids"`
	}
	if decode(r, &p) != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(p.CardIDs) == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "请至少选择一个卡券"})
		return
	}
	if len(p.ItemIDs) == 0 {
		write(w, http.StatusOK, response{Success: false, Message: "请至少选择一个商品"})
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	success, failed, relations := 0, 0, 0
	for _, cardID := range p.CardIDs {
		var owned int
		_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM cards WHERE id=? AND user_id=?`, cardID, u.ID).Scan(&owned)
		if owned == 0 {
			failed++
			continue
		}
		for _, itemID := range p.ItemIDs {
			res, e := tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO card_item_relations(card_id,item_id,source) VALUES(?,?,'own')`, cardID, itemID)
			if e != nil {
				failed++
				continue
			}
			n, _ := res.RowsAffected()
			relations += int(n)
		}
		success++
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, fmt.Sprintf("批量绑定完成（成功 %d 个，失败 %d 个）", success, failed), map[string]any{"success_count": success, "fail_count": failed, "relation_count": relations})
}
func (s *Server) cardBatchSave(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		Cards []map[string]any `json:"cards"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	ids := []int64{}
	for _, c := range p.Cards {
		name, typ := fmt.Sprint(c["name"]), fmt.Sprint(c["type"])
		b, _ := json.Marshal(c)
		res, e := s.db.ExecContext(r.Context(), `INSERT INTO cards(user_id,name,type,data) VALUES(?,?,?,?)`, u.ID, name, typ, string(b))
		if e == nil {
			id, _ := res.LastInsertId()
			ids = append(ids, id)
		}
	}
	ok(w, "批量保存完成", map[string]any{"ids": ids, "success_count": len(ids)})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "elapsed", time.Since(start))
	})
}
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, e := auth.Bearer(r.Header.Get("Authorization"))
		if e != nil && r.URL.Query().Get("token") != "" {
			t, e = r.URL.Query().Get("token"), nil
		}
		if e != nil {
			fail(w, 401, "未登录")
			return
		}
		c, e := auth.Parse(t, s.cfg.JWTSecret)
		if e != nil || c.Type != "access" {
			fail(w, 401, "登录已失效")
			return
		}
		var u User
		e = s.db.QueryRowContext(r.Context(), `SELECT id,username,role,account_limit FROM xy_users WHERE id=? AND status='ACTIVE'`, c.Sub).Scan(&u.ID, &u.Username, &u.Role, &u.AccountLimit)
		if e != nil {
			fail(w, 401, "用户不存在或已禁用")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}
func current(r *http.Request) User { return r.Context().Value(userKey{}).(User) }
func write(w http.ResponseWriter, status int, v any) {
	now := time.Now()
	_, offset := now.Zone()
	w.Header().Set("X-Server-Time", now.Format(time.RFC3339))
	w.Header().Set("X-Server-UTC-Offset", strconv.Itoa(offset/60))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func ok(w http.ResponseWriter, msg string, data any)     { write(w, 200, response{true, msg, data}) }
func fail(w http.ResponseWriter, status int, msg string) { write(w, status, response{false, msg, nil}) }
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}
func page(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	n, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if p < 1 {
		p = 1
	}
	if n < 1 {
		n = 20
	}
	if n > 9999 {
		n = 9999
	}
	return p, n
}
func placeholders(n int) string { return strings.TrimRight(strings.Repeat("?,", n), ",") }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if e := s.db.PingContext(r.Context()); e != nil {
		fail(w, 503, e.Error())
		return
	}
	write(w, 200, map[string]any{"status": "healthy", "service": "xianyu-go", "database": "sqlite"})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Username         string `json:"username"`
		Email            string `json:"email"`
		Password         string `json:"password"`
		VerificationCode string `json:"verification_code"`
		GeetestChallenge string `json:"geetest_challenge"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	identity := strings.TrimSpace(p.Username)
	method := "username_password"
	failLogin := func(message string) {
		s.recordUserLogin(r, 0, identity, method, false)
		write(w, 200, map[string]any{"success": false, "message": message})
	}
	if identity == "" {
		failLogin("请提供有效的登录信息")
		return
	}
	var u User
	var hash, status string
	var failCount int
	var lockedUntil sql.NullString
	e := s.db.QueryRowContext(r.Context(), `SELECT id,username,role,account_limit,password_hash,status,login_fail_count,login_locked_until FROM xy_users WHERE lower(username)=lower(?) AND role='ADMIN' ORDER BY id LIMIT 1`, identity).Scan(&u.ID, &u.Username, &u.Role, &u.AccountLimit, &hash, &status, &failCount, &lockedUntil)
	{
		if e != nil {
			failLogin("用户名或密码错误")
			return
		}
		if lockedUntil.Valid {
			locked, parseErr := time.ParseInLocation("2006-01-02 15:04:05", lockedUntil.String, time.UTC)
			if parseErr == nil && locked.After(time.Now().UTC()) {
				minutes := int(time.Until(locked).Minutes())
				if minutes < 1 {
					minutes = 1
				}
				if minutes > 60 {
					failLogin(fmt.Sprintf("账号已被锁定，请%d小时%d分钟后再试", minutes/60, minutes%60))
				} else {
					failLogin(fmt.Sprintf("账号已被锁定，请%d分钟后再试", minutes))
				}
				return
			}
		}
		if !auth.Verify(hash, p.Password) {
			failCount++
			if failCount >= 3 {
				_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_users SET login_fail_count=?,login_locked_until=datetime('now','+2 hours'),updated_at=CURRENT_TIMESTAMP WHERE id=?`, failCount, u.ID)
				failLogin("密码错误次数过多，账号已被锁定2小时")
			} else {
				_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_users SET login_fail_count=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, failCount, u.ID)
				failLogin(fmt.Sprintf("用户名或密码错误，还剩%d次尝试机会", 3-failCount))
			}
			return
		}
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_users SET login_fail_count=0,login_locked_until=NULL WHERE id=?`, u.ID)
	}
	if status != "ACTIVE" {
		failLogin("账号已禁用，请联系管理员")
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_users SET last_login_at=CURRENT_TIMESTAMP WHERE id=?`, u.ID)
	s.recordUserLogin(r, u.ID, identity, method, true)
	s.tokens(w, u, "登录成功")
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var enabled string
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key='registration_enabled'`).Scan(&enabled)
	if enabled != "true" && enabled != "1" {
		write(w, 200, response{false, "系统未开放注册", nil})
		return
	}
	var p struct {
		Username         string `json:"username"`
		Email            string `json:"email"`
		Password         string `json:"password"`
		VerificationCode string `json:"verification_code"`
	}
	if decode(r, &p) != nil || len(strings.TrimSpace(p.Username)) < 2 || len(p.Password) < 6 {
		fail(w, 400, "用户名至少2位且密码至少6位")
		return
	}
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	var verificationID int64
	if p.Email != "" {
		if strings.TrimSpace(p.VerificationCode) == "" {
			fail(w, 400, "请输入邮箱验证码")
			return
		}
		if err := s.db.QueryRowContext(r.Context(), `SELECT id FROM verification_codes WHERE email=? AND code=? AND purpose='register' AND used=0 AND expires_at>CURRENT_TIMESTAMP ORDER BY id DESC LIMIT 1`, p.Email, strings.TrimSpace(p.VerificationCode)).Scan(&verificationID); err != nil {
			fail(w, 400, "验证码无效或已过期")
			return
		}
	}
	h, e := auth.Hash(p.Password)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(r.Context(), `INSERT INTO xy_users(username,email,password_hash,status,role) VALUES(?,?,?,'ACTIVE','MEMBER')`, strings.TrimSpace(p.Username), nullString(p.Email), h)
	if e != nil {
		write(w, 409, response{false, "用户名或邮箱已存在", nil})
		return
	}
	if verificationID > 0 {
		_, _ = tx.ExecContext(r.Context(), `UPDATE verification_codes SET used=1 WHERE id=?`, verificationID)
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 201, response{true, "注册成功", nil})
}
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Email, Code, New string `json:"-"`
	}
	var raw map[string]any
	if decode(r, &raw) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	p.Email = fmt.Sprint(raw["email"])
	p.Code = fmt.Sprint(raw["verification_code"])
	p.New = fmt.Sprint(raw["new_password"])
	if len(p.New) < 6 {
		write(w, 200, response{false, "新密码长度不能少于6位", nil})
		return
	}
	var valid int
	e := s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM verification_codes WHERE email=? AND code=? AND purpose='reset_password' AND used=0 AND expires_at>CURRENT_TIMESTAMP`, p.Email, p.Code).Scan(&valid)
	if e != nil || valid == 0 {
		write(w, 200, response{false, "验证码无效或已过期", nil})
		return
	}
	h, _ := auth.Hash(p.New)
	tx, _ := s.db.BeginTx(r.Context(), nil)
	defer tx.Rollback()
	res, e := tx.ExecContext(r.Context(), `UPDATE xy_users SET password_hash=?,updated_at=CURRENT_TIMESTAMP WHERE email=?`, h, p.Email)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{false, "该邮箱未注册", nil})
		return
	}
	_, _ = tx.ExecContext(r.Context(), `UPDATE verification_codes SET used=1 WHERE email=? AND code=?`, p.Email, p.Code)
	_ = tx.Commit()
	ok(w, "密码重置成功", nil)
}
func (s *Server) tokens(w http.ResponseWriter, u User, msg string) {
	c := auth.Claims{Sub: u.ID, Username: u.Username, Admin: u.Role == "ADMIN"}
	c.Type = "access"
	a := auth.Sign(c, s.cfg.JWTSecret, s.cfg.AccessTTL)
	c.Type = "refresh"
	b := auth.Sign(c, s.cfg.JWTSecret, s.cfg.RefreshTTL)
	write(w, 200, map[string]any{"success": true, "message": msg, "token": a, "refresh_token": b, "user_id": u.ID, "username": u.Username, "is_admin": u.Role == "ADMIN", "account_limit": nullable(u.AccountLimit)})
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	t, e := auth.Bearer(r.Header.Get("Authorization"))
	if e != nil {
		fail(w, 401, "未提供刷新令牌")
		return
	}
	c, e := auth.Parse(t, s.cfg.JWTSecret)
	if e != nil || c.Type != "refresh" {
		fail(w, 401, "刷新令牌无效")
		return
	}
	var u User
	e = s.db.QueryRowContext(r.Context(), `SELECT id,username,role,account_limit FROM xy_users WHERE id=? AND status='ACTIVE'`, c.Sub).Scan(&u.ID, &u.Username, &u.Role, &u.AccountLimit)
	if e != nil {
		fail(w, 401, "用户不存在或已禁用")
		return
	}
	s.tokens(w, u, "令牌刷新成功")
}
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	token, err := auth.Bearer(r.Header.Get("Authorization"))
	if err != nil {
		write(w, 200, map[string]any{"authenticated": false})
		return
	}
	claims, err := auth.Parse(token, s.cfg.JWTSecret)
	if err != nil || claims.Type != "access" {
		write(w, 200, map[string]any{"authenticated": false})
		return
	}
	var u User
	if err = s.db.QueryRowContext(r.Context(), `SELECT id,username,role,account_limit FROM xy_users WHERE id=? AND status='ACTIVE'`, claims.Sub).Scan(&u.ID, &u.Username, &u.Role, &u.AccountLimit); err != nil {
		write(w, 200, map[string]any{"authenticated": false})
		return
	}
	write(w, 200, map[string]any{"authenticated": true, "user_id": u.ID, "username": u.Username, "is_admin": u.Role == "ADMIN", "account_limit": nullable(u.AccountLimit)})
}
func (s *Server) checkDefaultPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	u := current(r)
	var h string
	_ = s.db.QueryRowContext(r.Context(), `SELECT password_hash FROM xy_users WHERE id=?`, u.ID).Scan(&h)
	ok(w, "检查完成", map[string]bool{"is_default": auth.Verify(h, "admin123")})
}
func nullable(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func (s *Server) publicSettings(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.QueryContext(r.Context(), `SELECT key,value FROM system_settings WHERE is_public=1`)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		out[k] = smart(v)
	}
	write(w, 200, out)
}
func smart(v string) any {
	if v == "true" {
		return true
	}
	if v == "false" {
		return false
	}
	var x any
	if json.Unmarshal([]byte(v), &x) == nil {
		return x
	}
	return v
}

func queryBool(r *http.Request, key string) (bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return false, false
	}
	value, err := strconv.ParseBool(raw)
	return value, err == nil
}

func accountStatusEnabled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "inactive", "disabled", "suspended", "deleted":
		return false
	default:
		return true
	}
}

func (s *Server) accountOptions(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query, args := `SELECT id,account_id,status,remark,show_browser FROM xy_accounts`, []any{}
	if u.Role != "ADMIN" {
		query += ` WHERE owner_id=?`
		args = append(args, u.ID)
	}
	query += ` ORDER BY id`
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	a := []any{}
	for rows.Next() {
		var id int64
		var aid, status string
		var remark sql.NullString
		var show bool
		_ = rows.Scan(&id, &aid, &status, &remark, &show)
		a = append(a, map[string]any{"pk": id, "id": aid, "enabled": accountStatusEnabled(status), "remark": remark.String, "show_browser": show})
	}
	write(w, 200, a)
}
func (s *Server) accountRawList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query, args := `SELECT account_id,cookie,status,remark FROM xy_accounts`, []any{}
	if u.Role != "ADMIN" {
		query += ` WHERE owner_id=?`
		args = append(args, u.ID)
	}
	query += ` ORDER BY id`
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, cookie, status string
		var remark sql.NullString
		_ = rows.Scan(&id, &cookie, &status, &remark)
		out = append(out, map[string]any{"id": id, "value": cookie, "enabled": accountStatusEnabled(status), "remark": remark.String})
	}
	write(w, 200, out)
}
func (s *Server) accountStats(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountWhere, accountArgs := "", []any{}
	if u.Role != "ADMIN" {
		accountWhere, accountArgs = " WHERE owner_id=?", append(accountArgs, u.ID)
	}
	var total, active, keywords, orders int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*),COALESCE(sum(status NOT IN ('inactive','disabled','suspended','deleted')),0) FROM xy_accounts`+accountWhere, accountArgs...).Scan(&total, &active); err != nil {
		fail(w, 500, err.Error())
		return
	}
	keywordSQL := `SELECT count(*) FROM xy_keyword_rules`
	orderSQL := `SELECT count(*) FROM xy_orders`
	if u.Role != "ADMIN" {
		keywordSQL += ` WHERE owner_id=?`
		orderSQL += ` WHERE owner_id=?`
	}
	if err := s.db.QueryRowContext(r.Context(), keywordSQL, accountArgs...).Scan(&keywords); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := s.db.QueryRowContext(r.Context(), orderSQL, accountArgs...).Scan(&orders); err != nil {
		fail(w, 500, err.Error())
		return
	}
	var todayReplies, yesterdayReplies int
	_ = s.db.QueryRowContext(r.Context(), `SELECT
		COALESCE(sum(date(created_at,'localtime')=date('now','localtime')),0),
		COALESCE(sum(date(created_at,'localtime')=date('now','localtime','-1 day')),0)
		FROM event_logs WHERE owner_id=? AND kind IN ('auto_reply','ai_reply') AND upper(level)='INFO'`, u.ID).Scan(&todayReplies, &yesterdayReplies)
	var limit any
	var remaining any
	if u.AccountLimit.Valid {
		limit = u.AccountLimit.Int64
		left := u.AccountLimit.Int64 - int64(total)
		if left < 0 {
			left = 0
		}
		remaining = left
	}
	ok(w, "获取统计数据成功", map[string]any{
		"total_accounts": total, "active_accounts": active,
		"total_keywords": keywords, "total_orders": orders,
		"today_reply_count": todayReplies, "yesterday_reply_count": yesterdayReplies,
		"account_limit": limit, "used_account_count": total, "remaining_account_count": remaining,
	})
}
func (s *Server) accountOrderTrend(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query := `SELECT data FROM xy_orders WHERE status NOT IN ('cancelled','已关闭','refunded','退款成功','已退款')`
	args := []any{}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	type trendValue struct {
		amount float64
		count  int
	}
	values := map[string]trendValue{}
	now := time.Now().In(time.Local)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -29)
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		payload := map[string]any{}
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		placed, valid := orderPlacedAt(payload["placed_at"])
		if !valid || placed.Before(start) {
			continue
		}
		key := placed.In(time.Local).Format("01-02")
		amount, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(fmt.Sprint(payload["amount"])), ",", ""), 64)
		v := values[key]
		v.amount += amount
		v.count++
		values[key] = v
	}
	out := make([]map[string]any, 0, 30)
	for day := 0; day < 30; day++ {
		key := start.AddDate(0, 0, day).Format("01-02")
		v := values[key]
		out = append(out, map[string]any{"date": key, "amount": math.Round(v.amount*100) / 100, "count": v.count})
	}
	ok(w, "获取订单金额趋势成功", map[string]any{"trend": out})
}

func orderPlacedAt(value any) (time.Time, bool) {
	switch v := value.(type) {
	case float64:
		milliseconds := int64(v)
		if milliseconds > 0 {
			return time.UnixMilli(milliseconds), true
		}
	case json.Number:
		if milliseconds, err := v.Int64(); err == nil && milliseconds > 0 {
			return time.UnixMilli(milliseconds), true
		}
	case string:
		text := strings.TrimSpace(v)
		if milliseconds, err := strconv.ParseInt(text, 10, 64); err == nil && milliseconds > 0 {
			if milliseconds < 100000000000 {
				return time.Unix(milliseconds, 0), true
			}
			return time.UnixMilli(milliseconds), true
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
			if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}
func (s *Server) accountList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, n := page(r)
	where := []string{}
	args := []any{}
	if u.Role != "ADMIN" {
		where = append(where, "a.owner_id=?")
		args = append(args, u.ID)
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status == "active" {
		where = append(where, "a.status NOT IN ('inactive','disabled','suspended','deleted')")
	} else if status == "inactive" {
		where = append(where, "a.status IN ('inactive','disabled','suspended','deleted')")
	}
	for key, column := range map[string]string{"scheduled_redelivery": "a.scheduled_redelivery", "scheduled_rate": "a.scheduled_rate", "auto_polish": "a.auto_polish", "auto_confirm": "a.auto_confirm"} {
		if value, ok := queryBool(r, key); ok {
			where = append(where, column+"=?")
			args = append(args, value)
		}
	}
	if value, ok := queryBool(r, "has_password"); ok {
		if value {
			where = append(where, "COALESCE(a.username,'')<>'' AND COALESCE(a.login_password,'')<>''")
		} else {
			where = append(where, "(COALESCE(a.username,'')='' OR COALESCE(a.login_password,'')='')")
		}
	}
	if value, ok := queryBool(r, "ai_reply"); ok {
		expression := "COALESCE(json_extract(a.metadata,'$.ai_reply_settings.ai_enabled'),json_extract(a.metadata,'$.ai_reply_settings.enabled'),0)"
		where = append(where, expression+"=?")
		args = append(args, value)
	}
	if keyword := strings.TrimSpace(r.URL.Query().Get("account_id")); keyword != "" {
		where = append(where, "a.account_id LIKE ?")
		args = append(args, "%"+keyword+"%")
	}
	if keyword := strings.TrimSpace(r.URL.Query().Get("disable_reason")); keyword != "" {
		where = append(where, "COALESCE(a.disable_reason,'') LIKE ?")
		args = append(args, "%"+keyword+"%")
	}
	if keyword := strings.TrimSpace(r.URL.Query().Get("owner_username")); keyword != "" {
		where = append(where, "COALESCE(owner.username,'') LIKE ?")
		args = append(args, "%"+keyword+"%")
	}
	if online, ok := queryBool(r, "online"); ok {
		where = append(where, "1=0")
		if !online {
			where[len(where)-1] = "1=1"
		}
		// Online state lives in memory. Replace the neutral predicate with a
		// parameterized account-id set so pagination and totals stay accurate.
		onlineIDs := []string{}
		candidateRows, _ := s.db.QueryContext(r.Context(), `SELECT account_id FROM xy_accounts`)
		if candidateRows != nil {
			for candidateRows.Next() {
				var id string
				_ = candidateRows.Scan(&id)
				if s.online != nil && s.online(id) {
					onlineIDs = append(onlineIDs, id)
				}
			}
			_ = candidateRows.Close()
		}
		if len(onlineIDs) > 0 {
			marks := strings.TrimRight(strings.Repeat("?,", len(onlineIDs)), ",")
			operator := " IN ("
			if !online {
				operator = " NOT IN ("
			}
			where[len(where)-1] = "a.account_id" + operator + marks + ")"
			for _, id := range onlineIDs {
				args = append(args, id)
			}
		}
	}
	from := ` FROM xy_accounts a LEFT JOIN xy_users owner ON owner.id=a.owner_id`
	if len(where) > 0 {
		from += ` WHERE ` + strings.Join(where, ` AND `)
	}
	var total int
	if err := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		fail(w, 500, err.Error())
		return
	}
	pageArgs := append(append([]any{}, args...), n, (p-1)*n)
	rows, e := s.db.QueryContext(r.Context(), `SELECT a.id,a.account_id,a.cookie,a.status,a.remark,a.auto_confirm,a.show_browser,a.pause_duration,a.message_expire_time,a.reply_delay_seconds,a.scheduled_redelivery,a.scheduled_rate,a.auto_polish,a.confirm_before_send,a.send_before_confirm,a.only_send_card,a.auto_red_flower,a.ai_reply_block_ordered_users,a.delivery_disabled,a.delivery_disabled_reason,a.auto_close_order,a.delivery_only_card_after_close,a.delivery_disabled_excluded_items,a.disable_reason,a.created_at,a.updated_at,a.owner_id,COALESCE(owner.username,''),COALESCE(a.username,''),COALESCE(a.login_password,''),COALESCE(a.metadata,'{}'),
		(SELECT count(*) FROM resource_records f WHERE f.owner_id=a.owner_id AND f.kind='message_filter' AND COALESCE(f.account_id,'')=a.account_id),
		(SELECT count(*) FROM xy_keyword_rules k WHERE k.owner_id=a.owner_id AND k.account_id=a.account_id),
		(SELECT count(*) FROM event_logs l WHERE l.owner_id=a.owner_id AND l.account_id=a.account_id AND l.kind IN ('auto_reply','ai_reply') AND upper(l.level)='INFO' AND date(l.created_at,'localtime')=date('now','localtime')),
		COALESCE((SELECT CASE WHEN upper(l.level)='ERROR' THEN l.message ELSE '' END FROM event_logs l WHERE l.owner_id=a.owner_id AND l.account_id=a.account_id AND l.kind='account_login' ORDER BY l.id DESC LIMIT 1),'')`+from+` ORDER BY CASE WHEN a.status IN ('inactive','disabled','suspended','deleted') THEN 1 ELSE 0 END,a.created_at DESC LIMIT ? OFFSET ?`, pageArgs...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, ownerID int64
		var aid, cookie, status, created, updated string
		var remark, ddr, dr sql.NullString
		var ownerUsername, username, loginPassword, metadata, connectionError string
		var filterCount, keywordCount, todayReplyCount int
		var ac, sb, sr, rate, polish, cbs, sbc, osc, flower, aib, dd, aco, doc bool
		var pause, expire, delay int
		var excluded string
		e = rows.Scan(&id, &aid, &cookie, &status, &remark, &ac, &sb, &pause, &expire, &delay, &sr, &rate, &polish, &cbs, &sbc, &osc, &flower, &aib, &dd, &ddr, &aco, &doc, &excluded, &dr, &created, &updated, &ownerID, &ownerUsername, &username, &loginPassword, &metadata, &filterCount, &keywordCount, &todayReplyCount, &connectionError)
		if e != nil {
			continue
		}
		online := s.online != nil && s.online(aid)
		if online {
			connectionError = ""
		}
		meta := map[string]any{}
		_ = json.Unmarshal([]byte(metadata), &meta)
		aiEnabled := false
		if ai, ok := meta["ai_reply_settings"].(map[string]any); ok {
			aiEnabled, _ = ai["ai_enabled"].(bool)
			if !aiEnabled {
				aiEnabled, _ = ai["enabled"].(bool)
			}
		}
		list = append(list, map[string]any{"pk": id, "id": aid, "value": cookie, "enabled": accountStatusEnabled(status), "online": online, "connection_error": connectionError, "remark": remark.String, "auto_confirm": ac, "show_browser": sb, "pause_duration": pause, "message_expire_time": expire, "reply_delay_seconds": delay, "scheduled_redelivery": sr, "scheduled_rate": rate, "auto_polish": polish, "confirm_before_send": cbs, "send_before_confirm": sbc, "only_send_card": osc, "auto_red_flower": flower, "ai_reply_block_ordered_users": aib, "delivery_disabled": dd, "delivery_disabled_reason": ddr.String, "auto_close_order": aco, "delivery_only_card_after_close": doc, "delivery_disabled_excluded_item_ids": smart(excluded), "disable_reason": dr.String, "username": username, "login_password": loginPassword, "ai_enabled": aiEnabled, "filter_count": filterCount, "keyword_count": keywordCount, "today_reply_count": todayReplyCount, "owner_id": ownerID, "owner_username": ownerUsername, "created_at": created, "updated_at": updated})
	}
	write(w, 200, map[string]any{"success": true, "data": list, "total": total, "page": p, "page_size": n, "total_pages": pages(total, n)})
}
func (s *Server) accountCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if decode(r, &p) != nil || p.ID == "" || p.Value == "" {
		fail(w, 400, "账号ID和Cookie不能为空")
		return
	}
	_, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_accounts(owner_id,account_id,cookie) VALUES(?,?,?)`, u.ID, p.ID, p.Value)
	if e != nil {
		fail(w, 409, "账号已存在")
		return
	}
	ok(w, "账号添加成功", nil)
}
func (s *Server) accountUpdateCookie(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	v, _ := p["value"].(string)
	accountID := chi.URLParam(r, "id")
	ownerID, e := s.accountOwnerForAccess(r.Context(), u, accountID)
	if e != nil {
		fail(w, 404, "账号不存在或无权限访问")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, v, ownerID, accountID)
	affected(w, res, e, "账号更新成功")
}
func (s *Server) accountField(column, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		var p map[string]any
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		v := p[key]
		if key == "enabled" {
			if b, _ := v.(bool); b {
				v = "active"
			} else {
				v = "inactive"
			}
		}
		accountID := chi.URLParam(r, "id")
		ownerID, e := s.accountOwnerForAccess(r.Context(), u, accountID)
		if e != nil {
			fail(w, 404, "账号不存在或无权限访问")
			return
		}
		res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET `+column+`=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, v, ownerID, accountID)
		affected(w, res, e, "设置更新成功")
	}
}
func (s *Server) accountBatchStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs     []string `json:"account_ids"`
		Enabled bool     `json:"enabled"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	state := "inactive"
	if p.Enabled {
		state = "active"
	}
	good := []string{}
	bad := []any{}
	for _, id := range p.IDs {
		ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, id)
		if accessErr != nil {
			bad = append(bad, map[string]string{"account_id": id, "message": "账号不存在或无权限访问"})
			continue
		}
		res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET status=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, state, ownerID, id)
		n := int64(0)
		if e == nil {
			n, _ = res.RowsAffected()
		}
		if n == 1 {
			good = append(good, id)
		} else {
			bad = append(bad, map[string]string{"account_id": id, "message": "账号不存在"})
		}
	}
	ok(w, "批量更新完成", map[string]any{"success_count": len(good), "failed_count": len(bad), "success_ids": good, "failed_items": bad})
}
func (s *Server) accountDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "id")
	ownerID, e := s.accountOwnerForAccess(r.Context(), u, accountID)
	if e != nil {
		fail(w, 404, "账号不存在或无权限访问")
		return
	}
	if e := s.deleteAccountData(r.Context(), ownerID, accountID); e != nil {
		fail(w, 500, "账号删除失败")
		return
	}
	if s.accountRestart != nil {
		s.accountRestart(accountID)
	}
	ok(w, "账号删除成功", nil)
}
func (s *Server) accountLoginInfo(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	accountID := chi.URLParam(r, "id")
	ownerID, e := s.accountOwnerForAccess(r.Context(), u, accountID)
	if e != nil {
		fail(w, 404, "账号不存在或无权限访问")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET username=COALESCE(?,username),login_password=COALESCE(?,login_password),show_browser=COALESCE(?,show_browser),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, p["username"], p["login_password"], p["show_browser"], ownerID, accountID)
	affected(w, res, e, "登录信息更新成功")
}
func (s *Server) accountDeliveryDisabled(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	excluded, _ := json.Marshal(p["delivery_disabled_excluded_item_ids"])
	accountID := chi.URLParam(r, "id")
	ownerID, e := s.accountOwnerForAccess(r.Context(), u, accountID)
	if e != nil {
		fail(w, 404, "账号不存在或无权限访问")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET delivery_disabled=COALESCE(?,delivery_disabled),delivery_disabled_reason=COALESCE(?,delivery_disabled_reason),auto_close_order=COALESCE(?,auto_close_order),delivery_only_card_after_close=COALESCE(?,delivery_only_card_after_close),delivery_disabled_excluded_items=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, p["delivery_disabled"], p["delivery_disabled_reason"], p["auto_close_order"], p["delivery_only_card_after_close"], string(excluded), ownerID, accountID)
	affected(w, res, e, "禁止发货配置更新成功")
}

func (s *Server) accountOwnerForAccess(ctx context.Context, user User, accountID string) (int64, error) {
	if user.Role != "ADMIN" {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM xy_accounts WHERE owner_id=? AND account_id=?`, user.ID, accountID).Scan(&exists); err != nil {
			return 0, err
		}
		return user.ID, nil
	}
	var ownerID int64
	if err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM xy_accounts WHERE account_id=?`, accountID).Scan(&ownerID); err != nil {
		return 0, err
	}
	return ownerID, nil
}
func affected(w http.ResponseWriter, res sql.Result, e error, msg string) {
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "记录不存在")
		return
	}
	ok(w, msg, nil)
}
func pages(total, size int) int {
	if total == 0 {
		return 0
	}
	return (total + size - 1) / size
}

func (s *Server) keywordsAll(w http.ResponseWriter, r *http.Request) { s.keywordQuery(w, r, "") }
func (s *Server) keywords(w http.ResponseWriter, r *http.Request) {
	s.keywordQuery(w, r, chi.URLParam(r, "accountID"))
}
func (s *Server) keywordQuery(w http.ResponseWriter, r *http.Request, account string) {
	u := current(r)
	q := `SELECT keyword,reply,item_id,type,extra FROM xy_keyword_rules WHERE 1=1`
	args := []any{}
	if u.Role != "ADMIN" {
		q += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if account != "" {
		if _, err := s.accountOwnerForAccess(r.Context(), u, account); err != nil {
			fail(w, 404, "账号不存在")
			return
		}
		q += ` AND account_id=?`
		args = append(args, account)
	}
	q += ` ORDER BY id`
	rows, e := s.db.QueryContext(r.Context(), q, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var k, reply, item, typ, extra string
		_ = rows.Scan(&k, &reply, &item, &typ, &extra)
		m := map[string]any{"keyword": k, "reply": reply, "item_id": item, "type": typ}
		var ex map[string]any
		_ = json.Unmarshal([]byte(extra), &ex)
		for k, v := range ex {
			m[k] = v
		}
		out = append(out, m)
	}
	write(w, 200, out)
}
func (s *Server) keywordsReplace(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	account := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, account)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	var p struct {
		Keywords []map[string]any `json:"keywords"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(r.Context(), `DELETE FROM xy_keyword_rules WHERE owner_id=? AND account_id=? AND type!='image'`, ownerID, account); e != nil {
		fail(w, 500, e.Error())
		return
	}
	for _, x := range p.Keywords {
		k := fmt.Sprint(x["keyword"])
		reply := fmt.Sprint(x["reply"])
		item := fmt.Sprint(x["item_id"])
		typ := fmt.Sprint(x["type"])
		delete(x, "keyword")
		delete(x, "reply")
		delete(x, "item_id")
		delete(x, "type")
		extra, _ := json.Marshal(x)
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO xy_keyword_rules(owner_id,account_id,keyword,reply,item_id,type,extra) VALUES(?,?,?,?,?,?,?)`, ownerID, account, k, reply, item, typ, string(extra)); e != nil {
			fail(w, 500, e.Error())
			return
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "关键词保存成功", nil)
}
func (s *Server) keywordUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	old := chi.URLParam(r, "keyword")
	sourceAccount := chi.URLParam(r, "accountID")
	sourceOwnerID, accessErr := s.accountOwnerForAccess(r.Context(), u, sourceAccount)
	if accessErr != nil {
		fail(w, 404, "原所属账号不存在")
		return
	}
	targetAccount := strings.TrimSpace(fmt.Sprint(p["account_id"]))
	if targetAccount == "" || targetAccount == "<nil>" {
		targetAccount = sourceAccount
	}
	targetOwnerID, accessErr := s.accountOwnerForAccess(r.Context(), u, targetAccount)
	if accessErr != nil || targetOwnerID != sourceOwnerID {
		fail(w, 404, "目标所属账号不存在")
		return
	}
	reply := fmt.Sprint(p["reply"])
	next := fmt.Sprint(p["keyword"])
	if next == "" {
		next = old
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_keyword_rules SET account_id=?,keyword=?,reply=?,item_id=COALESCE(?,item_id),type=COALESCE(?,type),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND keyword=?`, targetAccount, next, reply, p["item_id"], p["type"], sourceOwnerID, sourceAccount, old)
	affected(w, res, e, "关键词更新成功")
}
func (s *Server) keywordDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM xy_keyword_rules WHERE owner_id=? AND account_id=? AND keyword=?`, ownerID, accountID, chi.URLParam(r, "keyword"))
	affected(w, res, e, "关键词删除成功")
}
func (s *Server) keywordImage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	b, _ := json.Marshal(p)
	_, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_keyword_rules(owner_id,account_id,keyword,reply,item_id,type,extra) VALUES(?,?,?,?,?,'image',?)`, ownerID, accountID, fmt.Sprint(p["keyword"]), fmt.Sprint(p["reply"]), fmt.Sprint(p["item_id"]), string(b))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "图片关键词添加成功", nil)
}

func (s *Server) items(w http.ResponseWriter, r *http.Request) {
	s.itemQuery(w, r, r.URL.Query().Get("cookie_id"), false)
}
func (s *Server) itemsByAccount(w http.ResponseWriter, r *http.Request) {
	s.itemQuery(w, r, chi.URLParam(r, "accountID"), false)
}
func (s *Server) itemsPaginated(w http.ResponseWriter, r *http.Request) {
	s.itemQuery(w, r, r.URL.Query().Get("cookie_id"), true)
}
func (s *Server) itemQuery(w http.ResponseWriter, r *http.Request, account string, paged bool) {
	u := current(r)
	p, n := page(r)
	q := ` FROM xy_catalog_items WHERE 1=1`
	args := []any{}
	if u.Role != "ADMIN" {
		q += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if account != "" {
		q += ` AND account_id=?`
		args = append(args, account)
	}
	if k := strings.TrimSpace(r.URL.Query().Get("keyword")); k != "" {
		q += ` AND (item_id LIKE ? OR title LIKE ? OR description LIKE ?)`
		like := "%" + k + "%"
		args = append(args, like, like, like)
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+q, args...).Scan(&total)
	qa := append([]any{}, args...)
	if paged {
		q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
		qa = append(qa, n, (p-1)*n)
	}
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,account_id,item_id,COALESCE(title,''),COALESCE(description,''),COALESCE(price,''),COALESCE(status,''),COALESCE(data,'{}'),created_at,updated_at`+q, qa...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var recordID int64
		var ac, id, title, desc, price, status, data, created, updated string
		if e = rows.Scan(&recordID, &ac, &id, &title, &desc, &price, &status, &data, &created, &updated); e != nil {
			fail(w, 500, e.Error())
			return
		}
		m := map[string]any{}
		mergeJSON(m, data)
		m["id"], m["cookie_id"], m["item_id"] = fmt.Sprint(recordID), ac, id
		m["title"], m["description"], m["price"], m["status"] = title, desc, price, status
		m["created_at"], m["updated_at"] = created, updated
		defaultReply, hasDefaultReply := m["default_reply"].(map[string]any)
		m["has_default_reply"] = hasDefaultReply && len(defaultReply) > 0
		m["default_reply_enabled"] = hasDefaultReply && defaultReply["enabled"] == true
		m["has_ai_prompt"] = strings.TrimSpace(firstString(m, "ai_prompt")) != ""
		list = append(list, m)
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	if paged {
		write(w, 200, map[string]any{"success": true, "data": list, "total": total, "page": p, "page_size": n, "total_pages": pages(total, n)})
	} else {
		write(w, 200, map[string]any{"items": list})
	}
}
func (s *Server) itemDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		Account *string `json:"cookie_id"`
		Item    string  `json:"item_id"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	q := `DELETE FROM xy_catalog_items WHERE item_id=?`
	a := []any{p.Item}
	if u.Role != "ADMIN" {
		q += ` AND owner_id=?`
		a = append(a, u.ID)
	}
	if p.Account != nil && *p.Account != "" {
		q += ` AND account_id=?`
		a = append(a, *p.Account)
	}
	res, e := s.db.ExecContext(r.Context(), q, a...)
	affected(w, res, e, "商品删除成功")
}
func (s *Server) itemBatchDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var raw struct {
		Items []map[string]string `json:"items"`
	}
	if decode(r, &raw) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	tx, _ := s.db.BeginTx(r.Context(), nil)
	defer tx.Rollback()
	for _, x := range raw.Items {
		ownerID, err := s.accountOwnerForAccess(r.Context(), u, x["cookie_id"])
		if err != nil {
			continue
		}
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, ownerID, x["cookie_id"], x["item_id"])
	}
	_ = tx.Commit()
	ok(w, "批量删除成功", nil)
}
func (s *Server) itemUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	b, _ := json.Marshal(p)
	accountID := chi.URLParam(r, "accountID")
	ownerID, e := s.accountOwnerForAccess(r.Context(), u, accountID)
	if e != nil {
		fail(w, 404, "账号不存在或无权限访问")
		return
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_catalog_items SET data=?,title=COALESCE(?,title),description=COALESCE(?,description),price=COALESCE(?,price),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, string(b), p["title"], p["description"], p["price"], ownerID, accountID, chi.URLParam(r, "itemID"))
	affected(w, res, e, "商品更新成功")
}
func (s *Server) itemRecord(r *http.Request) (map[string]any, error) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, err := s.accountOwnerForAccess(r.Context(), u, accountID)
	if err != nil {
		return nil, err
	}
	var ac, id, title, desc, price, status, data string
	e := s.db.QueryRowContext(r.Context(), `SELECT account_id,item_id,COALESCE(title,''),COALESCE(description,''),COALESCE(price,''),COALESCE(status,''),COALESCE(data,'{}') FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, ownerID, accountID, chi.URLParam(r, "itemID")).Scan(&ac, &id, &title, &desc, &price, &status, &data)
	if e != nil {
		return nil, e
	}
	m := map[string]any{"cookie_id": ac, "item_id": id, "title": title, "description": desc, "price": price, "status": status}
	mergeJSON(m, data)
	return m, nil
}
func (s *Server) itemDetail(w http.ResponseWriter, r *http.Request) {
	m, e := s.itemRecord(r)
	if e != nil {
		fail(w, 404, "商品不存在")
		return
	}
	write(w, 200, m)
}
func (s *Server) itemMerge(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	old, e := s.itemRecord(r)
	if e != nil {
		fail(w, 404, "商品不存在")
		return
	}
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	for k, v := range p {
		old[k] = v
	}
	b, _ := json.Marshal(old)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在或无权限访问")
		return
	}
	_, e = s.db.ExecContext(r.Context(), `UPDATE xy_catalog_items SET data=?,price=COALESCE(?,price),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, string(b), p["price"], ownerID, accountID, chi.URLParam(r, "itemID"))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "商品更新成功", old)
}
func (s *Server) itemFlag(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.itemMerge(w, r) }
}
func (s *Server) itemSubGet(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, e := s.itemRecord(r)
		if e != nil {
			fail(w, 404, "商品不存在")
			return
		}
		v := m[key]
		if v == nil {
			v = map[string]any{}
		}
		write(w, 200, v)
	}
}
func (s *Server) itemSubPut(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		accountID := chi.URLParam(r, "accountID")
		ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
		if accessErr != nil {
			fail(w, 404, "账号不存在或无权限访问")
			return
		}
		m, e := s.itemRecord(r)
		if e != nil {
			fail(w, 404, "商品不存在")
			return
		}
		var p map[string]any
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		if key == "default_reply" && firstString(p, "reply_type") == "api" {
			if message := validateDefaultReplyAPIURL(r.Context(), firstString(p, "api_url")); message != "" {
				write(w, 200, response{Success: false, Message: message})
				return
			}
		}
		if key == "default_reply" && firstString(p, "reply_type") == "external_contact" {
			if message := s.validateExternalContact(r.Context(), ownerID, p); message != "" {
				write(w, 200, response{Success: false, Message: message})
				return
			}
		}
		m[key] = p
		b, _ := json.Marshal(m)
		_, e = s.db.ExecContext(r.Context(), `UPDATE xy_catalog_items SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, string(b), ownerID, accountID, chi.URLParam(r, "itemID"))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		ok(w, "保存成功", p)
	}
}
func (s *Server) itemSubDelete(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		m, e := s.itemRecord(r)
		if e != nil {
			fail(w, 404, "商品不存在")
			return
		}
		delete(m, key)
		b, _ := json.Marshal(m)
		accountID := chi.URLParam(r, "accountID")
		ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
		if accessErr != nil {
			fail(w, 404, "账号不存在或无权限访问")
			return
		}
		_, e = s.db.ExecContext(r.Context(), `UPDATE xy_catalog_items SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, string(b), ownerID, accountID, chi.URLParam(r, "itemID"))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		ok(w, "删除成功", nil)
	}
}
func (s *Server) itemSelectable(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	q := `SELECT item_id,title,price FROM xy_catalog_items WHERE 1=1`
	args := []any{}
	if u.Role != "ADMIN" {
		q += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	if k := strings.TrimSpace(r.URL.Query().Get("keyword")); k != "" {
		q += ` AND (item_id LIKE ? OR title LIKE ?)`
		args = append(args, "%"+k+"%", "%"+k+"%")
	}
	rows, e := s.db.QueryContext(r.Context(), q, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, title, price string
		_ = rows.Scan(&id, &title, &price)
		list = append(list, map[string]any{"item_id": id, "title": title, "price": price})
	}
	write(w, 200, map[string]any{"list": list, "total": len(list)})
}
func (s *Server) itemsByCard(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, e := s.db.QueryContext(r.Context(), `SELECT DISTINCT i.item_id,i.title,i.price FROM xy_catalog_items i JOIN card_item_relations r ON r.item_id=i.item_id JOIN cards c ON c.id=r.card_id WHERE i.owner_id=? AND c.user_id=? AND c.id=?`, u.ID, u.ID, chi.URLParam(r, "cardID"))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, title, price string
		_ = rows.Scan(&id, &title, &price)
		list = append(list, map[string]any{"item_id": id, "title": title, "price": price})
	}
	write(w, 200, map[string]any{"list": list, "total": len(list)})
}
func mergeJSON(m map[string]any, s string) {
	var x map[string]any
	if json.Unmarshal([]byte(s), &x) == nil {
		for k, v := range x {
			m[k] = v
		}
	}
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	u := current(r)
	p, n := page(r)
	q := ` FROM xy_orders o LEFT JOIN xy_catalog_items ci ON ci.account_id=o.account_id AND ci.item_id=o.item_id WHERE 1=1`
	a := []any{}
	if u.Role != "ADMIN" {
		q += ` AND o.owner_id=?`
		a = append(a, u.ID)
	}
	if v := r.URL.Query().Get("cookie_id"); v != "" {
		q += ` AND o.account_id=?`
		a = append(a, v)
	}
	if v := r.URL.Query().Get("status"); v != "" {
		q += ` AND o.status=?`
		a = append(a, v)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("search")); v != "" {
		// 搜索条件同时用于总数与分页查询，并保留账号、状态及用户权限限制。
		keyword := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v) + "%"
		q += ` AND (o.order_no LIKE ? ESCAPE '\' OR o.item_id LIKE ? ESCAPE '\' OR o.buyer_id LIKE ? ESCAPE '\')`
		a = append(a, keyword, keyword, keyword)
	}
	var total int
	if e := s.db.QueryRowContext(r.Context(), `SELECT count(*)`+q, a...).Scan(&total); e != nil {
		fail(w, 500, e.Error())
		return
	}
	a = append(a, n, (p-1)*n)
	rows, e := s.db.QueryContext(r.Context(), `SELECT o.id,o.account_id,o.order_no,o.item_id,o.buyer_id,o.status,COALESCE(o.delivery_method,''),o.data,o.created_at,o.updated_at,COALESCE(json_extract(ci.data,'$.title'),json_extract(ci.data,'$.item_title'),json_extract(ci.data,'$.name'),'')`+q+` ORDER BY o.id DESC LIMIT ? OFFSET ?`, a...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var ac, no, item, buyer, status, delivery, data, created, updated, catalogTitle string
		if e = rows.Scan(&id, &ac, &no, &item, &buyer, &status, &delivery, &data, &created, &updated, &catalogTitle); e != nil {
			fail(w, 500, e.Error())
			return
		}
		m := map[string]any{}
		mergeJSON(m, data)
		// 数据库列是订单的规范值。必须在 JSON 合并后写入，避免历史 data 中的
		// 浮点数/空值覆盖 ID，也确保旧订单始终具有前端使用的 order_id。
		m["id"], m["cookie_id"] = id, ac
		m["order_id"], m["order_no"] = no, no
		m["item_id"], m["buyer_id"] = item, buyer
		m["status"], m["delivery_method"] = status, delivery
		if v, ok := m["item_title"].(string); !ok || strings.TrimSpace(v) == "" {
			m["item_title"] = catalogTitle
		}
		m["created_at"], m["updated_at"] = created, updated
		list = append(list, m)
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 200, map[string]any{"success": true, "data": list, "total": total, "page": p, "page_size": n, "total_pages": pages(total, n)})
}
func (s *Server) order(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var data string
	query, args := `SELECT data FROM xy_orders WHERE order_no=?`, []any{chi.URLParam(r, "orderNo")}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	e := s.db.QueryRowContext(r.Context(), query, args...).Scan(&data)
	if e != nil {
		fail(w, 404, "订单不存在")
		return
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(data), &m)
	ok(w, "", m)
}
func (s *Server) orderDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query, args := `DELETE FROM xy_orders WHERE (id=? OR order_no=?)`, []any{chi.URLParam(r, "id"), chi.URLParam(r, "id")}
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		args = append(args, u.ID)
	}
	res, e := s.db.ExecContext(r.Context(), query, args...)
	affected(w, res, e, "订单删除成功")
}
func (s *Server) orderBatchDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择订单")
		return
	}
	a := []any{}
	for _, id := range p.IDs {
		a = append(a, id)
	}
	query := `DELETE FROM xy_orders WHERE id IN (` + placeholders(len(p.IDs)) + `)`
	if u.Role != "ADMIN" {
		query += ` AND owner_id=?`
		a = append(a, u.ID)
	}
	res, e := s.db.ExecContext(r.Context(), query, a...)
	affected(w, res, e, "批量删除成功")
}

func (s *Server) cards(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, n := page(r)
	search := r.URL.Query().Get("search")
	typ := r.URL.Query().Get("type")
	q := ` FROM cards WHERE 1=1`
	a := []any{}
	if u.Role != "ADMIN" {
		q += ` AND user_id=?`
		a = append(a, u.ID)
	}
	if search != "" {
		q += ` AND name LIKE ?`
		a = append(a, "%"+search+"%")
	}
	if typ != "" {
		q += ` AND type=?`
		a = append(a, typ)
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*)`+q, a...).Scan(&total)
	a = append(a, n, (p-1)*n)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,name,type,enabled,data,created_at,updated_at`+q+` ORDER BY id DESC LIMIT ? OFFSET ?`, a...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, typ, data, created, updated string
		var enabled bool
		_ = rows.Scan(&id, &name, &typ, &enabled, &data, &created, &updated)
		m := map[string]any{"id": id, "name": name, "type": typ, "enabled": enabled, "created_at": created, "updated_at": updated}
		mergeJSON(m, data)
		list = append(list, m)
	}
	write(w, 200, map[string]any{"list": list, "total": total, "page": p, "page_size": n, "total_pages": pages(total, n)})
}
func (s *Server) cardCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	name, typ := fmt.Sprint(p["name"]), fmt.Sprint(p["type"])
	b, _ := json.Marshal(p)
	res, e := s.db.ExecContext(r.Context(), `INSERT INTO cards(user_id,name,type,data) VALUES(?,?,?,?)`, u.ID, name, typ, string(b))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	id, _ := res.LastInsertId()
	write(w, 200, map[string]any{"id": id, "message": "卡券创建成功"})
}
func (s *Server) card(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var id int64
	var name, typ, data, created, updated string
	var enabled bool
	query, args := `SELECT id,name,type,enabled,data,created_at,updated_at FROM cards WHERE id=?`, []any{chi.URLParam(r, "id")}
	if u.Role != "ADMIN" {
		query += ` AND user_id=?`
		args = append(args, u.ID)
	}
	e := s.db.QueryRowContext(r.Context(), query, args...).Scan(&id, &name, &typ, &enabled, &data, &created, &updated)
	if e != nil {
		fail(w, 404, "卡券不存在")
		return
	}
	m := map[string]any{"id": id, "name": name, "type": typ, "enabled": enabled, "created_at": created, "updated_at": updated}
	mergeJSON(m, data)
	write(w, 200, m)
}
func (s *Server) cardUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	b, _ := json.Marshal(p)
	res, e := s.db.ExecContext(r.Context(), `UPDATE cards SET name=COALESCE(?,name),type=COALESCE(?,type),data=?,updated_at=CURRENT_TIMESTAMP WHERE user_id=? AND id=?`, p["name"], p["type"], string(b), u.ID, chi.URLParam(r, "id"))
	affected(w, res, e, "卡券更新成功")
}
func (s *Server) cardDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM cards WHERE user_id=? AND id=?`, u.ID, chi.URLParam(r, "id"))
	affected(w, res, e, "卡券删除成功")
}
func (s *Server) cardBatchDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择卡券")
		return
	}
	a := []any{u.ID}
	for _, id := range p.IDs {
		a = append(a, id)
	}
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM cards WHERE user_id=? AND id IN (`+placeholders(len(p.IDs))+`)`, a...)
	affected(w, res, e, "批量删除成功")
}
func (s *Server) cardItems(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, e := s.db.QueryContext(r.Context(), `SELECT r.item_id FROM card_item_relations r JOIN cards c ON c.id=r.card_id WHERE c.user_id=? AND c.id=?`, u.ID, chi.URLParam(r, "id"))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var x string
		_ = rows.Scan(&x)
		ids = append(ids, x)
	}
	ok(w, "", map[string]any{"item_ids": ids})
}
func (s *Server) cardItemsUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []string `json:"item_ids"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	var owned int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM cards WHERE user_id=? AND id=?`, u.ID, chi.URLParam(r, "id")).Scan(&owned)
	if owned == 0 {
		fail(w, 404, "卡券不存在")
		return
	}
	tx, _ := s.db.BeginTx(r.Context(), nil)
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM card_item_relations WHERE card_id=?`, chi.URLParam(r, "id"))
	for _, id := range p.IDs {
		_, _ = tx.ExecContext(r.Context(), `INSERT INTO card_item_relations(card_id,item_id) VALUES(?,?)`, chi.URLParam(r, "id"), id)
	}
	_ = tx.Commit()
	ok(w, "关联更新成功", nil)
}

func (s *Server) settingRoutes(r chi.Router) {
	r.Get("/", s.settings)
	r.Post("/test-token-remote", s.testTokenRemote)
	r.Post("/test-password-login-remote", s.testPasswordLoginRemote)
	r.Get("/{key}", s.settingGet)
	r.Put("/{key}", s.settingPut)
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.QueryContext(r.Context(), `SELECT key,value,description FROM system_settings`)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k, v, d string
		_ = rows.Scan(&k, &v, &d)
		out[k] = smart(v)
	}
	write(w, 200, out)
}
func (s *Server) settingGet(w http.ResponseWriter, r *http.Request) {
	var v, d string
	e := s.db.QueryRowContext(r.Context(), `SELECT value,description FROM system_settings WHERE key=?`, chi.URLParam(r, "key")).Scan(&v, &d)
	if e != nil {
		fail(w, 404, "设置不存在")
		return
	}
	write(w, 200, map[string]any{"key": chi.URLParam(r, "key"), "value": smart(v), "description": d})
}
func (s *Server) settingPut(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if u.Role != "ADMIN" {
		fail(w, 403, "需要管理员权限")
		return
	}
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	v := p["value"]
	var str string
	if x, okv := v.(string); okv {
		str = x
	} else {
		b, _ := json.Marshal(v)
		str = string(b)
	}
	_, e := s.db.ExecContext(r.Context(), `INSERT INTO system_settings(key,value,description) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,description=excluded.description,updated_at=CURRENT_TIMESTAMP`, chi.URLParam(r, "key"), str, fmt.Sprint(p["description"]))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "设置更新成功", nil)
}
func (s *Server) userSettingRoutes(r chi.Router) {
	r.Get("/", s.userSettings)
	r.Get("/{key}", s.userSettingGet)
	r.Put("/{key}", s.userSettingPut)
	r.Delete("/{key}", s.userSettingDelete)
}
func (s *Server) userSettings(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, e := s.db.QueryContext(r.Context(), `SELECT key,value FROM user_settings WHERE owner_id=?`, u.ID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		out[k] = smart(v)
	}
	write(w, 200, out)
}
func (s *Server) userSettingDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM user_settings WHERE owner_id=? AND key=?`, u.ID, chi.URLParam(r, "key"))
	affected(w, res, e, "设置删除成功")
}
func (s *Server) userSettingGet(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var v, d string
	e := s.db.QueryRowContext(r.Context(), `SELECT value,description FROM user_settings WHERE owner_id=? AND key=?`, u.ID, chi.URLParam(r, "key")).Scan(&v, &d)
	if e != nil {
		fail(w, 404, "设置不存在")
		return
	}
	write(w, 200, map[string]any{"key": chi.URLParam(r, "key"), "value": v, "description": d})
}
func (s *Server) userSettingPut(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p map[string]any
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	b, _ := json.Marshal(p["value"])
	v := string(b)
	if x, ok := p["value"].(string); ok {
		v = x
	}
	_, e := s.db.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,?,?,?) ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,description=excluded.description,updated_at=CURRENT_TIMESTAMP`, u.ID, chi.URLParam(r, "key"), v, fmt.Sprint(p["description"]))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "设置更新成功", nil)
}
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	names := []string{"xy_accounts", "xy_catalog_items", "xy_orders", "cards"}
	out := map[string]int{}
	for _, t := range names {
		var n int
		field := "owner_id"
		if t == "cards" {
			field = "user_id"
		}
		_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM `+t+` WHERE `+field+`=?`, u.ID).Scan(&n)
		out[t] = n
	}
	ok(w, "", out)
}

func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	root, err := filepath.Abs(s.cfg.StaticDir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "静态目录不可用")
		return
	}
	requested := strings.TrimLeft(filepath.Clean(strings.ReplaceAll(r.URL.Path, "\\", "/")), "/\\")
	p := filepath.Join(root, requested)
	relative, err := filepath.Rel(root, p)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		fail(w, http.StatusBadRequest, "文件路径无效")
		return
	}
	if st, e := os.Stat(p); e == nil && !st.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	idx := filepath.Join(root, "index.html")
	if _, e := os.Stat(idx); e != nil {
		write(w, 200, map[string]any{"service": "xianyu-auto-go", "message": "frontend/dist 未构建，请执行 make frontend"})
		return
	}
	http.ServeFile(w, r, idx)
}

type Hub struct {
	clients  map[string]map[*websocket.Conn]struct{}
	add, del chan peer
	send     chan event
	mu       sync.RWMutex
}
type peer struct {
	account string
	conn    *websocket.Conn
}
type event struct {
	account string
	data    []byte
}

func NewHub() *Hub {
	h := &Hub{clients: map[string]map[*websocket.Conn]struct{}{}, add: make(chan peer), del: make(chan peer), send: make(chan event, 256)}
	go h.run()
	return h
}
func (h *Hub) run() {
	for {
		select {
		case p := <-h.add:
			h.mu.Lock()
			if h.clients[p.account] == nil {
				h.clients[p.account] = map[*websocket.Conn]struct{}{}
			}
			h.clients[p.account][p.conn] = struct{}{}
			h.mu.Unlock()
		case p := <-h.del:
			h.mu.Lock()
			if m := h.clients[p.account]; m != nil {
				delete(m, p.conn)
				_ = p.conn.Close()
				if len(m) == 0 {
					delete(h.clients, p.account)
				}
			}
			h.mu.Unlock()
		case e := <-h.send:
			h.mu.RLock()
			for c := range h.clients[e.account] {
				_ = c.WriteMessage(websocket.TextMessage, e.data)
			}
			h.mu.RUnlock()
		}
	}
}
func (h *Hub) Online(a string) bool { h.mu.RLock(); defer h.mu.RUnlock(); return len(h.clients[a]) > 0 }

func (s *Server) Broadcast(account string, data []byte) { s.ws.send <- event{account, data} }

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) chatWS(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	if _, found := s.ownedChatAccount(r, accountID); !found {
		fail(w, http.StatusNotFound, "账号不存在或无权操作")
		return
	}
	c, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	connected, _ := json.Marshal(map[string]any{"event": "connected", "account_id": accountID})
	if e = c.WriteMessage(websocket.TextMessage, connected); e != nil {
		_ = c.Close()
		return
	}
	p := peer{accountID, c}
	s.ws.add <- p
	defer func() { s.ws.del <- p }()
	for {
		_, _, e := c.ReadMessage()
		if e != nil {
			return
		}
	}
}

var _ = errors.Is
