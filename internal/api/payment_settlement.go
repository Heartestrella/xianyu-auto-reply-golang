package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

func decimalValue(v string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(v))
	return r, ok
}
func decimalString(r *big.Rat) string { return r.FloatString(2) }

func (s *Server) withdrawCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		Amount string `json:"amount"`
	}
	if decode(r, &p) != nil {
		write(w, 200, response{Success: false, Message: "提现金额格式不正确"})
		return
	}
	amount, valid := decimalValue(p.Amount)
	if !valid {
		write(w, 200, response{Success: false, Message: "提现金额格式不正确"})
		return
	}
	if amount.Sign() <= 0 {
		write(w, 200, response{Success: false, Message: "提现金额必须大于0"})
		return
	}
	if amount.Cmp(big.NewRat(10000, 1)) > 0 {
		write(w, 200, response{Success: false, Message: "单次提现金额不能超过10000元"})
		return
	}
	settings := map[string]string{}
	rows, e := s.db.QueryContext(r.Context(), `SELECT key,value FROM user_settings WHERE owner_id=? AND key IN ('balance','payment_qrcode','payment_type','alipay_id')`, u.ID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		settings[k] = v
	}
	rows.Close()
	var notify, min string
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key='withdraw.notify_email'`).Scan(&notify)
	if strings.TrimSpace(notify) == "" {
		write(w, 200, response{Success: false, Message: "系统未配置提现通知邮箱，暂时无法提现，请联系管理员"})
		return
	}
	if strings.TrimSpace(settings["payment_qrcode"]) == "" {
		write(w, 200, response{Success: false, Message: "请先上传收款码"})
		return
	}
	_ = s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key='withdraw.min_amount'`).Scan(&min)
	if minimum, ok := decimalValue(min); ok && minimum.Sign() > 0 && amount.Cmp(minimum) < 0 {
		write(w, 200, response{Success: false, Message: "提现金额不能低于最低提现金额 ¥" + decimalString(minimum)})
		return
	}
	balance, okv := decimalValue(settings["balance"])
	if !okv {
		if strings.TrimSpace(settings["balance"]) != "" {
			write(w, 200, response{Success: false, Message: "当前余额数据异常，请联系管理员处理"})
			return
		}
		balance = new(big.Rat)
	}
	if amount.Cmp(balance) > 0 {
		write(w, 200, response{Success: false, Message: "提现金额不能大于当前余额"})
		return
	}
	after := new(big.Rat).Sub(balance, amount)
	paymentType := strings.TrimSpace(settings["payment_type"])
	if paymentType == "" {
		paymentType = "alipay"
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,?,?,'用户余额') ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, u.ID, "balance", decimalString(after))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	res, e := tx.ExecContext(r.Context(), `INSERT INTO xy_settlement_records(user_id,alipay_id,payment_type,payment_qrcode,amount,status,remark) VALUES(?,?,?,?,?,'pending_review','用户发起提现申请')`, u.ID, nullString(settings["alipay_id"]), paymentType, settings["payment_qrcode"], decimalString(amount))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	id, _ := res.LastInsertId()
	_, e = tx.ExecContext(r.Context(), `INSERT INTO fund_flows(owner_id,operator_id,flow_type,amount,balance_before,balance_after,remark) VALUES(?,?,'expense',?,?,?,?)`, u.ID, u.ID, decimalString(amount), decimalString(balance), decimalString(after), fmt.Sprintf("余额提现申请，结算记录ID: %d", id))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "提现申请已提交，等待审核", map[string]any{"id": id, "amount": decimalString(amount), "status": "pending_review", "alipay_id": settings["alipay_id"], "balance": decimalString(after)})
}

func (s *Server) settlementRecords(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := pageParams(r)
	if size != 10 && size != 20 && size != 50 && size != 100 {
		size = 20
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_settlement_records WHERE user_id=?`, u.ID).Scan(&total)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,COALESCE(alipay_id,''),COALESCE(payment_type,''),COALESCE(payment_qrcode,''),amount,status,COALESCE(remark,''),COALESCE(reject_reason,''),created_at,updated_at FROM xy_settlement_records WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, u.ID, size, (p-1)*size)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var alipay, typ, qr, amount, status, remark, reject, created, updated string
		_ = rows.Scan(&id, &alipay, &typ, &qr, &amount, &status, &remark, &reject, &created, &updated)
		items = append(items, map[string]any{"id": id, "alipay_id": alipay, "payment_type": typ, "payment_qrcode": qr, "amount": amount, "status": status, "remark": remark, "reject_reason": reject, "created_at": created, "updated_at": updated})
	}
	pages := (total + size - 1) / size
	ok(w, "", map[string]any{"list": items, "total": total, "page": p, "page_size": size, "total_pages": pages})
}

func (s *Server) withdrawToken(id int64, action string) string {
	h := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	fmt.Fprintf(h, "%d:%s", id, action)
	return hex.EncodeToString(h.Sum(nil))[:32]
}
func (s *Server) validWithdrawToken(id int64, action, token string) bool {
	expected := s.withdrawToken(id, action)
	return len(token) == len(expected) && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
func (s *Server) withdrawReviewPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	action := r.URL.Query().Get("action")
	token := r.URL.Query().Get("token")
	if (action != "approve" && action != "reject") || !s.validWithdrawToken(id, action, token) {
		http.Error(w, "<!doctype html><meta charset=utf-8><h2>无效的审核令牌</h2>", http.StatusBadRequest)
		return
	}
	title := "通过提现申请"
	button := "确认通过"
	reason := ""
	if action == "reject" {
		title = "拒绝提现申请"
		button = "确认拒绝"
		reason = `<textarea name="reject_reason" placeholder="拒绝原因（可选）"></textarea>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title></head><body><main><h2>%s #%d</h2><form method="post" action="/api/v1/payment/withdraw/%s"><input type="hidden" name="id" value="%d"><input type="hidden" name="token" value="%s">%s<button type="submit">%s</button></form></main></body></html>`, title, title, id, action, id, html.EscapeString(token), reason, button)
}
func (s *Server) withdrawReviewSubmit(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if !s.validWithdrawToken(id, action, r.FormValue("token")) {
			write(w, 200, response{Success: false, Message: "无效的审核令牌"})
			return
		}
		var status, amount, remark string
		e := s.db.QueryRowContext(r.Context(), `SELECT status,amount,COALESCE(remark,'') FROM xy_settlement_records WHERE id=?`, id).Scan(&status, &amount, &remark)
		if e == sql.ErrNoRows {
			write(w, 200, response{Success: false, Message: "结算记录不存在"})
			return
		}
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		if status != "pending_review" {
			names := map[string]string{"approved": "已通过", "rejected": "已拒绝", "paid": "已打款"}
			if names[status] != "" {
				status = names[status]
			}
			write(w, 200, response{Success: false, Message: "该记录已处理，当前状态：" + status})
			return
		}
		next, msg := "approved", "提现申请已通过审核"
		line := "\n管理员已通过审核"
		reason := ""
		if action == "reject" {
			next = "rejected"
			msg = "提现申请已拒绝"
			line = "\n管理员已拒绝审核"
			reason = strings.TrimSpace(r.FormValue("reject_reason"))
		}
		_, e = s.db.ExecContext(r.Context(), `UPDATE xy_settlement_records SET status=?,remark=?,reject_reason=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending_review'`, next, remark+line, nullString(reason), id)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		ok(w, msg, map[string]any{"id": id, "status": next, "amount": amount})
	}
}
