package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
)

type alipayConfig struct{ AppID, PrivateKey, PublicKey, Gateway, NotifyURL string }

func (s *Server) loadAlipay(ctx context.Context) (alipayConfig, error) {
	keys := []string{"alipay.app_id", "alipay.private_key", "alipay.alipay_public_key", "alipay.gateway_url", "alipay.notify_url"}
	args := make([]any, len(keys))
	marks := make([]string, len(keys))
	for i, k := range keys {
		args[i] = k
		marks[i] = "?"
	}
	rows, e := s.db.QueryContext(ctx, `SELECT key,value FROM system_settings WHERE key IN (`+strings.Join(marks, ",")+`)`, args...)
	if e != nil {
		return alipayConfig{}, e
	}
	values := map[string]string{}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		values[k] = v
	}
	rows.Close()
	cfg := alipayConfig{values[keys[0]], values[keys[1]], values[keys[2]], values[keys[3]], values[keys[4]]}
	if cfg.Gateway == "" {
		cfg.Gateway = "https://openapi.alipay.com/gateway.do"
	}
	missing := []string{}
	if cfg.AppID == "" {
		missing = append(missing, "app_id")
	}
	if cfg.PrivateKey == "" {
		missing = append(missing, "private_key")
	}
	if cfg.PublicKey == "" {
		missing = append(missing, "alipay_public_key")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("支付宝配置缺少必要字段: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}
func pemBlock(raw, typ string) *pem.Block {
	clean := strings.TrimSpace(raw)
	if block, _ := pem.Decode([]byte(clean)); block != nil {
		return block
	}
	clean = strings.NewReplacer(" ", "", "\r", "", "\n", "").Replace(clean)
	decoded, e := base64.StdEncoding.DecodeString(clean)
	if e != nil {
		return nil
	}
	return &pem.Block{Type: typ, Bytes: decoded}
}
func parsePrivate(raw string) (*rsa.PrivateKey, error) {
	b := pemBlock(raw, "RSA PRIVATE KEY")
	if b == nil {
		return nil, fmt.Errorf("支付宝私钥格式无效")
	}
	if key, e := x509.ParsePKCS1PrivateKey(b.Bytes); e == nil {
		return key, nil
	}
	parsed, e := x509.ParsePKCS8PrivateKey(b.Bytes)
	if e != nil {
		return nil, e
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("支付宝私钥不是RSA密钥")
	}
	return key, nil
}
func parsePublic(raw string) (*rsa.PublicKey, error) {
	b := pemBlock(raw, "PUBLIC KEY")
	if b == nil {
		return nil, fmt.Errorf("支付宝公钥格式无效")
	}
	if parsed, e := x509.ParsePKIXPublicKey(b.Bytes); e == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			return key, nil
		}
	}
	return x509.ParsePKCS1PublicKey(b.Bytes)
}
func alipaySignText(values url.Values) string {
	keys := make([]string, 0, len(values))
	for k, v := range values {
		if k != "sign" && len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}
	return strings.Join(parts, "&")
}
func alipaySign(raw string, values url.Values) (string, error) {
	key, e := parsePrivate(raw)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(alipaySignText(values)))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return base64.StdEncoding.EncodeToString(sig), e
}
func alipayVerify(raw string, values url.Values) bool {
	sig, e := base64.StdEncoding.DecodeString(values.Get("sign"))
	if e != nil {
		return false
	}
	key, e := parsePublic(raw)
	if e != nil {
		return false
	}
	sum := sha256.Sum256([]byte(alipaySignText(values)))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) == nil
}
func paymentOrderNo() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "RC" + time.Now().In(beijingTZ).Format("20060102150405") + fmt.Sprintf("%06d", int(b[0])<<16|int(b[1])<<8|int(b[2]))[:6]
}
func (s *Server) createAlipayOrder(ctx context.Context, userID int64, amount, orderType string, referenceID any, subject, body string) (map[string]any, error) {
	cfg, e := s.loadAlipay(ctx)
	if e != nil {
		return nil, e
	}
	orderNo := paymentOrderNo()
	biz, _ := json.Marshal(map[string]any{"out_trade_no": orderNo, "total_amount": amount, "subject": subject, "body": body, "timeout_express": "30m"})
	values := url.Values{"app_id": {cfg.AppID}, "method": {"alipay.trade.precreate"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(beijingTZ).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "biz_content": {string(biz)}}
	if cfg.NotifyURL != "" {
		values.Set("notify_url", cfg.NotifyURL)
	}
	signature, e := alipaySign(cfg.PrivateKey, values)
	if e != nil {
		return nil, e
	}
	values.Set("sign", signature)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Gateway, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	resp, e := proxy.Client(30 * time.Second).Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	var root map[string]json.RawMessage
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if json.Unmarshal(payload, &root) != nil {
		return nil, fmt.Errorf("无法解析支付宝API响应")
	}
	raw := root["alipay_trade_precreate_response"]
	var result struct {
		Code   string `json:"code"`
		Msg    string `json:"msg"`
		SubMsg string `json:"sub_msg"`
		QRCode string `json:"qr_code"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Code != "10000" || result.QRCode == "" {
		message := result.SubMsg
		if message == "" {
			message = result.Msg
		}
		if message == "" {
			message = "生成支付二维码失败"
		}
		return nil, fmt.Errorf("%s", message)
	}
	res, e := s.db.ExecContext(ctx, `INSERT INTO xy_recharge_orders(order_no,user_id,amount,order_type,reference_id,status,qr_code) VALUES(?,?,?,?,?,'pending',?)`, orderNo, userID, amount, orderType, referenceID, result.QRCode)
	if e != nil {
		return nil, e
	}
	id, _ := res.LastInsertId()
	return map[string]any{"order_id": id, "order_no": orderNo, "amount": amount, "qr_code": result.QRCode}, nil
}

func validPaymentAmount(raw string) (string, string) {
	amount, ok := decimalValue(raw)
	if !ok {
		return "", "金额格式不正确"
	}
	if amount.Sign() <= 0 {
		return "", "充值金额必须大于0"
	}
	if amount.Cmp(bigRat10000) > 0 {
		return "", "单次充值金额不能超过10000元"
	}
	return decimalString(amount), ""
}

var bigRat10000, _ = decimalValue("10000")

func (s *Server) paymentRecharge(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Amount string `json:"amount"`
	}
	if decode(r, &p) != nil {
		write(w, 200, response{Success: false, Message: "充值金额格式不正确"})
		return
	}
	amount, msg := validPaymentAmount(p.Amount)
	if msg != "" {
		if strings.HasPrefix(msg, "金额") {
			msg = "充值" + msg
		}
		write(w, 200, response{Success: false, Message: msg})
		return
	}
	data, e := s.createAlipayOrder(r.Context(), current(r).ID, amount, "recharge", nil, "余额充值", "用户余额充值")
	if e != nil {
		write(w, 200, response{Success: false, Message: "支付宝配置错误: " + e.Error()})
		return
	}
	ok(w, "", data)
}
func (s *Server) paymentRechargeStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var id int64
	var order, amount, status string
	var trade, paid sql.NullString
	var created string
	e := s.db.QueryRowContext(r.Context(), `SELECT id,order_no,amount,status,trade_no,paid_at,created_at FROM xy_recharge_orders WHERE order_no=? AND user_id=?`, chi.URLParam(r, "order_no"), u.ID).Scan(&id, &order, &amount, &status, &trade, &paid, &created)
	if e != nil {
		write(w, 200, map[string]any{"success": false, "code": 0, "message": "订单不存在", "data": nil})
		return
	}
	write(w, 200, map[string]any{"success": true, "code": 0, "message": nil, "data": map[string]any{"order_id": id, "order_no": order, "amount": amount, "status": status, "trade_no": nullText(trade), "paid_at": nullText(paid), "created_at": created}})
}

func (s *Server) alipayNotify(w http.ResponseWriter, r *http.Request) {
	if e := r.ParseMultipartForm(2 << 20); e != nil {
		_ = r.ParseForm()
	}
	cfg, e := s.loadAlipay(r.Context())
	if e != nil || r.Form.Get("app_id") != cfg.AppID || !alipayVerify(cfg.PublicKey, r.Form) {
		w.Write([]byte("failure"))
		return
	}
	orderNo, tradeNo, status := r.Form.Get("out_trade_no"), r.Form.Get("trade_no"), r.Form.Get("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" {
		w.Write([]byte("success"))
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		w.Write([]byte("failure"))
		return
	}
	defer tx.Rollback()
	var id, userID int64
	var amount, orderType, currentStatus string
	e = tx.QueryRowContext(r.Context(), `SELECT id,user_id,amount,order_type,status FROM xy_recharge_orders WHERE order_no=?`, orderNo).Scan(&id, &userID, &amount, &orderType, &currentStatus)
	if e != nil {
		w.Write([]byte("failure"))
		return
	}
	if currentStatus == "paid" {
		w.Write([]byte("success"))
		return
	}
	if orderType == "recharge" {
		var raw string
		e = tx.QueryRowContext(r.Context(), `SELECT COALESCE((SELECT value FROM user_settings WHERE owner_id=? AND key='balance'),'0')`, userID).Scan(&raw)
		before, valid := decimalValue(raw)
		delta, valid2 := decimalValue(amount)
		if e != nil || !valid || !valid2 {
			w.Write([]byte("failure"))
			return
		}
		after := new(big.Rat).Add(before, delta)
		_, e = tx.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,'balance',?,'用户余额') ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, userID, decimalString(after))
		if e == nil {
			_, e = tx.ExecContext(r.Context(), `INSERT INTO fund_flows(owner_id,operator_id,flow_type,amount,balance_before,balance_after,remark) VALUES(?,?,'income',?,?,?,?)`, userID, userID, decimalString(delta), decimalString(before), decimalString(after), "余额充值（支付宝当面付）订单号: "+orderNo)
		}
		if e != nil {
			w.Write([]byte("failure"))
			return
		}
	}
	_, e = tx.ExecContext(r.Context(), `UPDATE xy_recharge_orders SET status='paid',trade_no=?,paid_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, tradeNo, id)
	if e != nil || tx.Commit() != nil {
		w.Write([]byte("failure"))
		return
	}
	w.Write([]byte("success"))
}

func (s *Server) advertisementPay(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	adID, _ := strconv.ParseInt(chi.URLParam(r, "ad_id"), 10, 64)
	var amount, title, status string
	e := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(total_amount,''),title,status FROM xy_advertisements WHERE id=? AND user_id=?`, adID, u.ID).Scan(&amount, &title, &status)
	if e != nil {
		write(w, 200, response{Success: false, Message: "广告不存在"})
		return
	}
	if status != "unpaid" {
		write(w, 200, response{Success: false, Message: "该广告当前状态不可付款"})
		return
	}
	if amount == "" {
		write(w, 200, response{Success: false, Message: "广告费用信息异常"})
		return
	}
	data, e := s.createAlipayOrder(r.Context(), u.ID, amount, "ad", adID, "广告申请付款 - "+title, fmt.Sprintf("广告申请付款 ID:%d", adID))
	if e != nil {
		write(w, 200, response{Success: false, Message: e.Error()})
		return
	}
	data["ad_id"] = adID
	ok(w, "", data)
}
func (s *Server) advertisementPayStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	adID, _ := strconv.ParseInt(chi.URLParam(r, "ad_id"), 10, 64)
	orderNo := r.URL.Query().Get("order_no")
	var adStatus, amount string
	if s.db.QueryRowContext(r.Context(), `SELECT status,COALESCE(total_amount,'0') FROM xy_advertisements WHERE id=? AND user_id=?`, adID, u.ID).Scan(&adStatus, &amount) != nil {
		write(w, 200, response{Success: false, Message: "广告不存在"})
		return
	}
	if adStatus != "unpaid" {
		ok(w, "", map[string]any{"status": adStatus})
		return
	}
	var paid string
	if s.db.QueryRowContext(r.Context(), `SELECT status FROM xy_recharge_orders WHERE order_no=? AND user_id=? AND order_type='ad' AND reference_id=?`, orderNo, u.ID, adID).Scan(&paid) != nil || paid != "paid" {
		ok(w, "", map[string]any{"status": "pending"})
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(r.Context(), `UPDATE xy_advertisements SET status='approved',updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND status='unpaid'`, adID, u.ID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		var adminID int64
		if tx.QueryRowContext(r.Context(), `SELECT id FROM xy_users WHERE role='ADMIN' ORDER BY id LIMIT 1`).Scan(&adminID) == nil {
			var raw string
			_ = tx.QueryRowContext(r.Context(), `SELECT COALESCE((SELECT value FROM user_settings WHERE owner_id=? AND key='balance'),'0')`, adminID).Scan(&raw)
			before, _ := decimalValue(raw)
			delta, _ := decimalValue(amount)
			after := new(big.Rat).Add(before, delta)
			_, _ = tx.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,'balance',?,'用户余额') ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value`, adminID, decimalString(after))
			_, _ = tx.ExecContext(r.Context(), `INSERT INTO fund_flows(owner_id,operator_id,flow_type,amount,balance_before,balance_after,remark) VALUES(?,?,'income',?,?,?,?)`, adminID, u.ID, decimalString(delta), decimalString(before), decimalString(after), fmt.Sprintf("广告申请收入（广告ID:%d）", adID))
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "", map[string]any{"status": "approved"})
}
