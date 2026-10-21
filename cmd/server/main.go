package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/api"
	"github.com/zhinianboke/xianyu-auto-go/internal/auth"
	"github.com/zhinianboke/xianyu-auto-go/internal/config"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"github.com/zhinianboke/xianyu-auto-go/internal/scheduler"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	db, err := database.Open(cfg.Database)
	if err != nil {
		log.Error("database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	var proxyEnabled, proxyURL string
	_ = db.QueryRow(`SELECT COALESCE(value,'') FROM system_settings WHERE key='proxy.enabled'`).Scan(&proxyEnabled)
	_ = db.QueryRow(`SELECT COALESCE(value,'') FROM system_settings WHERE key='proxy.url'`).Scan(&proxyURL)
	if err := proxy.Apply(strings.EqualFold(strings.TrimSpace(proxyEnabled), "true"), proxyURL); err != nil {
		log.Warn("invalid global proxy setting", "error", err)
	}
	// Make every HTTP client that relies on Go's default transport use the same
	// configured proxy; there is no environment fallback once a proxy is enabled.
	http.DefaultTransport = proxy.Transport()
	http.DefaultClient.Transport = http.DefaultTransport
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM xy_users`).Scan(&n)
	if n == 0 {
		h, e := auth.Hash("admin123")
		if e != nil {
			panic(e)
		}
		_, e = db.Exec(`INSERT INTO xy_users(username,email,password_hash,status,role) VALUES('admin','admin@local',?,'ACTIVE','ADMIN')`, h)
		if e != nil {
			panic(e)
		}
		log.Warn("created default administrator", "username", "admin", "password", "admin123")
	}
	// 个人版只允许本地管理员使用。保留旧用户数据便于回滚，但停用其登录权限。
	if _, err = db.Exec(`UPDATE xy_users SET status='INACTIVE',updated_at=CURRENT_TIMESTAMP WHERE role<>'ADMIN' AND status='ACTIVE'`); err != nil {
		log.Error("disable legacy users", "error", err)
		os.Exit(1)
	}
	app := api.New(db, cfg, log)
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	xy := xianyu.NewManager(db, log, func(msg xianyu.Incoming) { app.Broadcast(msg.AccountID, incomingChatPush(msg)) })
	app.SetOnlineChecker(xy.Online)
	app.SetChatSender(xy.SendText)
	app.SetChatCreator(xy.CreateChat)
	app.SetChatRecaller(xy.RecallMessage)
	app.SetChatImageSender(xy.SendImage)
	app.SetAccountRestarter(xy.Restart)
	go xy.Run(rootCtx)
	jobs := scheduler.New(db, log)
	jobs.SetRedelivery(xy.Redeliver)
	jobs.SetPublisher(app.ExecuteScheduledPublish)
	jobs.SetCookieRenewer(app.ExecuteScheduledCookieRenewal)
	jobs.SetCookiesRefresher(app.ExecuteScheduledCookiesRefresh)
	jobs.SetTokenRenewer(xy.RenewTokens)
	jobs.SetLoginRenewer(app.ExecuteScheduledLoginRenewal)
	jobs.SetListingMonitor(app.ExecuteScheduledListingMonitor)
	jobs.SetDMSender(app.ExecuteScheduledDMSend)
	jobs.SetSellerFiller(app.ExecuteScheduledSellerFill)
	jobs.SetAutoOrder(app.ExecuteScheduledAutoOrder)
	app.SetScheduler(jobs.Trigger, jobs.Running)
	app.SetServiceRestarter(func(key string) error {
		switch key {
		case "websocket":
			xy.RestartAll()
			return nil
		case "scheduler":
			jobs.Rescan()
			return nil
		case "backend-web":
			return errors.New("单体进程重启需要由 systemd、Docker 或其他进程管理器执行")
		default:
			return errors.New("未知服务")
		}
	})
	go jobs.Run(rootCtx)
	h := app.Router()
	srv := &http.Server{Addr: cfg.Addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 90 * time.Second}
	go func() {
		log.Info("server started", "addr", cfg.Addr, "database", cfg.Database)
		if e := srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			log.Error("server", "error", e)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	rootCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// incomingChatPush translates the platform message into the stable browser
// protocol consumed by the online-chat page. Forwarding msg.Raw directly makes
// the page silently ignore every message because it is not a new_message event.
func incomingChatPush(msg xianyu.Incoming) []byte {
	messageType := msg.Type
	if messageType == "" {
		messageType = "text"
	}
	receivedAt := msg.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	payload, _ := json.Marshal(map[string]any{
		"event":      "new_message",
		"account_id": msg.AccountID,
		"cid":        msg.ChatID,
		"message": map[string]any{
			"messageId":  fmt.Sprintf("incoming-%d", receivedAt.UnixNano()),
			"senderId":   msg.SenderID,
			"senderName": msg.SenderName,
			"isSelf":     msg.IsSelf,
			"type":       messageType,
			"text":       msg.Text,
			"images":     msg.Images,
			"time":       receivedAt.UnixMilli(),
		},
	})
	return payload
}
