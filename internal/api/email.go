package api

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type smtpSettings struct {
	Host, User, Password, From string
	Port                       int
}

func loadSMTPSettings(ctx context.Context, db *sql.DB) (smtpSettings, error) {
	rows, err := db.QueryContext(ctx, `SELECT key,value FROM system_settings WHERE key IN ('smtp_server','smtp_port','smtp_user','smtp_password','smtp_from')`)
	if err != nil {
		return smtpSettings{}, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return smtpSettings{}, err
		}
		values[key] = strings.TrimSpace(value)
	}
	port, _ := strconv.Atoi(values["smtp_port"])
	if port == 0 {
		port = 587
	}
	cfg := smtpSettings{Host: values["smtp_server"], Port: port, User: values["smtp_user"], Password: values["smtp_password"], From: values["smtp_from"]}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	if cfg.Host == "" || cfg.User == "" || cfg.Password == "" || cfg.From == "" {
		return smtpSettings{}, errors.New("邮件服务未配置，请联系管理员")
	}
	return cfg, rows.Err()
}

func sendSMTP(ctx context.Context, cfg smtpSettings, recipient, subject, htmlBody string) error {
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if cfg.Port == 465 || cfg.Port == 994 {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("SMTP连接失败: %w", err)
	}
	defer conn.Close()
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP握手失败: %w", err)
	}
	defer client.Close()
	if cfg.Port == 587 {
		if err = client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("SMTP STARTTLS失败: %w", err)
		}
	}
	if err = client.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
		return fmt.Errorf("SMTP认证失败: %w", err)
	}
	if err = client.Mail(cfg.From); err != nil {
		return err
	}
	if err = client.Rcpt(recipient); err != nil {
		return err
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	encodedSubject := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?="
	message := "From: " + cfg.From + "\r\nTo: " + recipient + "\r\nSubject: " + encodedSubject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(htmlBody))
	if _, err = wc.Write([]byte(message)); err != nil {
		_ = wc.Close()
		return err
	}
	if err = wc.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func verificationEmail(code, purpose string) (string, string) {
	subject, action := "验证码", "完成操作"
	if purpose == "register" {
		subject, action = "注册验证码", "注册账号"
	} else if purpose == "login" {
		subject, action = "登录验证码", "登录账号"
	} else if purpose == "reset_password" {
		subject, action = "重置密码验证码", "重置密码"
	}
	body := fmt.Sprintf(`<div style="font-family:sans-serif;max-width:560px;margin:auto"><h2>%s</h2><p>您正在%s，验证码为：</p><p style="font-size:30px;font-weight:bold;letter-spacing:6px">%s</p><p>验证码10分钟内有效。如非本人操作，请忽略本邮件。</p></div>`, subject, action, code)
	return subject, body
}

func (s *Server) testEmail(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if u.Role != "ADMIN" {
		fail(w, http.StatusForbidden, "需要管理员权限")
		return
	}
	recipient := strings.TrimSpace(r.URL.Query().Get("email"))
	if !emailPattern.MatchString(recipient) {
		fail(w, http.StatusBadRequest, "邮箱格式无效")
		return
	}
	cfg, err := loadSMTPSettings(r.Context(), s.db)
	if err == nil {
		err = sendSMTP(r.Context(), cfg, recipient, "邮件服务测试", `<h2>邮件服务配置成功</h2><p>这是一封来自闲鱼自动回复系统的测试邮件。</p>`)
	}
	if err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	ok(w, "测试邮件发送成功", nil)
}
