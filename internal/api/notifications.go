package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/notify"
)

const messageNotificationKind = "message_notification"

func (s *Server) registerNotificationRoutes(r chi.Router) {
	r.Post("/notification-channels/{channel_id}/test", s.notificationChannelTest)
	r.Get("/message-notifications", s.messageNotifications)
	r.Get("/message-notifications/{cookie_id}", s.accountMessageNotifications)
	r.Post("/message-notifications/{cookie_id}", s.setMessageNotification)
	r.Delete("/message-notifications/{notification_id}", s.deleteMessageNotification)
	r.Delete("/message-notifications/account/{cookie_id}", s.deleteAccountMessageNotifications)
}

func channelFromData(raw string) (notify.Channel, map[string]any, error) {
	m := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return notify.Channel{}, nil, err
	}
	return notify.Channel{Type: firstString(m, "type", "channel_type"), Config: notify.ParseConfig(m["config"])}, m, nil
}

func (s *Server) loadNotificationChannel(ctx context.Context, owner int64, id string) (notify.Channel, map[string]any, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM resource_records WHERE owner_id=? AND kind='notification_channel' AND id=? AND enabled=1`, owner, id).Scan(&raw)
	if err != nil {
		return notify.Channel{}, nil, err
	}
	return channelFromData(raw)
}

func (s *Server) notificationChannelTest(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	channel, m, err := s.loadNotificationChannel(r.Context(), u.ID, chi.URLParam(r, "channel_id"))
	if err == sql.ErrNoRows {
		fail(w, 404, "通知渠道不存在")
		return
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		message := fmt.Sprintf("🔔 通知渠道测试\n\n渠道名称: %s\n渠道类型: %s\n测试时间: %s\n\n如果您收到此消息，说明通知渠道配置正确！", firstString(m, "name"), channel.Type, time.Now().Format("2006-01-02 15:04:05"))
		err = notify.Send(ctx, channel, message)
	}
	if err != nil {
		write(w, 200, response{Success: false, Message: "发送失败: " + err.Error()})
		return
	}
	ok(w, "测试消息发送成功", nil)
}

func notificationRecord(id int64, enabled bool, account string, channelID int64, raw string) map[string]any {
	_, channel, _ := channelFromData(raw)
	m := map[string]any{"id": id, "channel_id": channelID, "enabled": enabled, "channel_name": firstString(channel, "name"), "channel_type": firstString(channel, "type", "channel_type"), "channel_config": notify.ParseConfig(channel["config"]), "account_id": account}
	return m
}

func (s *Server) queryMessageNotifications(ctx context.Context, owner int64, account string) (map[string][]map[string]any, error) {
	query := `SELECT n.id,n.enabled,COALESCE(n.account_id,''),CAST(json_extract(n.data,'$.channel_id') AS INTEGER),c.data FROM resource_records n JOIN resource_records c ON c.owner_id=n.owner_id AND c.kind='notification_channel' AND c.id=CAST(json_extract(n.data,'$.channel_id') AS INTEGER) AND c.enabled=1 WHERE n.owner_id=? AND n.kind=?`
	args := []any{owner, messageNotificationKind}
	if account != "" {
		query += ` AND n.account_id=?`
		args = append(args, account)
	}
	query += ` ORDER BY n.account_id,n.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]map[string]any{}
	for rows.Next() {
		var id, channelID int64
		var enabled bool
		var aid, raw string
		if rows.Scan(&id, &enabled, &aid, &channelID, &raw) == nil {
			out[aid] = append(out[aid], notificationRecord(id, enabled, aid, channelID, raw))
		}
	}
	return out, rows.Err()
}

func (s *Server) messageNotifications(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	out, err := s.queryMessageNotifications(r.Context(), u.ID, "")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	write(w, 200, out)
}
func (s *Server) accountMessageNotifications(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	aid := chi.URLParam(r, "cookie_id")
	out, err := s.queryMessageNotifications(r.Context(), u.ID, aid)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	write(w, 200, out[aid])
}

func (s *Server) setMessageNotification(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	aid := chi.URLParam(r, "cookie_id")
	var p struct {
		ChannelID int64 `json:"channel_id"`
		Enabled   bool  `json:"enabled"`
	}
	if decode(r, &p) != nil || p.ChannelID < 1 {
		fail(w, 400, "请求格式错误")
		return
	}
	var accounts, channels int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, aid).Scan(&accounts)
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind='notification_channel' AND id=?`, u.ID, p.ChannelID).Scan(&channels)
	if accounts == 0 || channels == 0 {
		fail(w, 404, "账号或通知渠道不存在")
		return
	}
	raw, _ := json.Marshal(map[string]any{"channel_id": p.ChannelID})
	var id int64
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM resource_records WHERE owner_id=? AND kind=? AND account_id=? AND CAST(json_extract(data,'$.channel_id') AS INTEGER)=?`, u.ID, messageNotificationKind, aid, p.ChannelID).Scan(&id)
	if err == sql.ErrNoRows {
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,?,?)`, u.ID, messageNotificationKind, aid, p.Enabled, string(raw))
	} else if err == nil {
		_, err = s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, p.Enabled, id)
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "消息通知配置已更新", nil)
}
func (s *Server) deleteMessageNotification(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, messageNotificationKind, chi.URLParam(r, "notification_id"))
	affected(w, res, err, "消息通知已删除")
}
func (s *Server) deleteAccountMessageNotifications(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND account_id=?`, u.ID, messageNotificationKind, chi.URLParam(r, "cookie_id"))
	affected(w, res, err, "账号通知配置已清理")
}

func sendAccountNotifications(ctx context.Context, db *sql.DB, owner int64, account, message string) error {
	rows, err := db.QueryContext(ctx, `SELECT c.data FROM resource_records n JOIN resource_records c ON c.owner_id=n.owner_id AND c.kind='notification_channel' AND c.id=CAST(json_extract(n.data,'$.channel_id') AS INTEGER) WHERE n.owner_id=? AND n.kind=? AND n.account_id=? AND n.enabled=1 AND c.enabled=1`, owner, messageNotificationKind, account)
	if err != nil {
		return err
	}
	defer rows.Close()
	errs := []string{}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		channel, _, e := channelFromData(raw)
		if e == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			e = notify.Send(sendCtx, channel, message)
			cancel()
		}
		if e != nil {
			errs = append(errs, e.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return rows.Err()
}
