package api

import (
	"context"
	"database/sql"
	"html"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type advertisement struct {
	ID                                  int64
	UserID                              int64
	Title                               string
	Content, Link, ExpireDate, ImageURL sql.NullString
	AdType                              string
	Months                              sql.NullInt64
	TotalAmount                         sql.NullString
	Status, CreatedAt, UpdatedAt        string
}

func (a advertisement) json() map[string]any {
	return map[string]any{"id": a.ID, "user_id": a.UserID, "title": a.Title, "content": nullText(a.Content), "link": nullText(a.Link), "expire_date": nullText(a.ExpireDate), "image_url": nullText(a.ImageURL), "ad_type": a.AdType, "months": nullable(a.Months), "total_amount": nullText(a.TotalAmount), "status": a.Status, "source": "local", "created_at": a.CreatedAt, "updated_at": a.UpdatedAt}
}
func scanAdvertisement(scanner interface{ Scan(...any) error }) (advertisement, error) {
	var a advertisement
	e := scanner.Scan(&a.ID, &a.UserID, &a.Title, &a.Content, &a.Link, &a.ExpireDate, &a.ImageURL, &a.AdType, &a.Months, &a.TotalAmount, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	return a, e
}

const advertisementColumns = `id,user_id,title,content,link,expire_date,image_url,ad_type,months,total_amount,status,created_at,updated_at`

func (s *Server) registerAdvertisementRoutes(r chi.Router) {
	r.Get("/advertisements/prices", s.advertisementPrices)
	r.Get("/advertisements/admin", s.advertisementsAdmin)
	r.Put("/advertisements/admin/{ad_id}/approve", s.advertisementAdminStatus("approved", "复核成功"))
	r.Put("/advertisements/admin/{ad_id}/reject", s.advertisementAdminStatus("pending", "已取消复核"))
	r.Delete("/advertisements/admin/{ad_id}", s.advertisementAdminDelete)
	r.Put("/advertisements/admin/{ad_id}", s.advertisementAdminUpdate)
	r.Get("/advertisements", s.advertisementsMine)
	r.Post("/advertisements", s.advertisementCreate)
	r.Put("/advertisements/{ad_id}", s.advertisementUpdate)
	r.Delete("/advertisements/{ad_id}", s.advertisementDelete)
}

func (s *Server) advertisementsPublic(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.QueryContext(r.Context(), `SELECT `+advertisementColumns+` FROM xy_advertisements WHERE status='approved' AND (expire_date IS NULL OR expire_date>=date('now')) ORDER BY created_at DESC`)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	carousel := []map[string]any{}
	text := []map[string]any{}
	for rows.Next() {
		a, e := scanAdvertisement(rows)
		if e != nil {
			continue
		}
		if a.AdType == "carousel" {
			carousel = append(carousel, a.json())
		} else {
			text = append(text, a.json())
		}
	}
	ok(w, "", map[string]any{"carousel": carousel, "text": text})
}
func (s *Server) advertisementPrices(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{"carousel": "0", "text": "0"}
	for k := range out {
		var v string
		if s.db.QueryRowContext(r.Context(), `SELECT value FROM system_settings WHERE key=?`, "ad_price."+k).Scan(&v) == nil {
			out[k] = v
		}
	}
	ok(w, "", out)
}
func pageParams(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return p, size
}

func (s *Server) advertisementList(w http.ResponseWriter, r *http.Request, admin bool) {
	u := current(r)
	if admin && u.Role != "ADMIN" {
		write(w, 403, response{Success: false, Message: "需要管理员权限"})
		return
	}
	p, size := pageParams(r)
	where := "1=1"
	args := []any{}
	if !admin {
		where = "user_id=?"
		args = append(args, u.ID)
	} else {
		if v := r.URL.Query().Get("status"); v == "unpaid" || v == "pending" || v == "approved" {
			where += " AND status=?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("ad_type"); v == "text" || v == "carousel" {
			where += " AND ad_type=?"
			args = append(args, v)
		}
	}
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_advertisements WHERE `+where, args...).Scan(&total)
	args = append(args, size, (p-1)*size)
	rows, e := s.db.QueryContext(r.Context(), `SELECT `+advertisementColumns+` FROM xy_advertisements WHERE `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		a, e := scanAdvertisement(rows)
		if e == nil {
			items = append(items, a.json())
		}
	}
	ok(w, "", map[string]any{"items": items, "total": total, "page": p, "page_size": size})
}
func (s *Server) advertisementsAdmin(w http.ResponseWriter, r *http.Request) {
	s.advertisementList(w, r, true)
}
func (s *Server) advertisementsMine(w http.ResponseWriter, r *http.Request) {
	s.advertisementList(w, r, false)
}
func validAdType(v string) bool   { return v == "text" || v == "carousel" }
func validAdStatus(v string) bool { return v == "unpaid" || v == "pending" || v == "approved" }
func adPrice(ctx context.Context, db *sql.DB, typ string) (string, error) {
	var v string
	e := db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key=?`, "ad_price."+typ).Scan(&v)
	return v, e
}
func multiplyPrice(price string, months int) (string, bool) {
	r, ok := new(big.Rat).SetString(price)
	if !ok {
		return "", false
	}
	r.Mul(r, big.NewRat(int64(months), 1))
	digits := 0
	if i := strings.IndexByte(price, '.'); i >= 0 {
		digits = len(price) - i - 1
	}
	return r.FloatString(digits), true
}
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	target := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := target.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(target.Year(), target.Month(), d, 0, 0, 0, 0, time.UTC)
}
func adParams(r *http.Request) (string, string, int, string, string, string) {
	q := r.URL.Query()
	months, _ := strconv.Atoi(q.Get("months"))
	return q.Get("title"), q.Get("ad_type"), months, q.Get("content"), q.Get("link"), q.Get("image_url")
}
func (s *Server) advertisementCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	title, typ, months, content, link, image := adParams(r)
	if !validAdType(typ) {
		write(w, 200, response{Success: false, Message: "无效的广告类型"})
		return
	}
	if months <= 0 {
		write(w, 200, response{Success: false, Message: "月数必须大于0"})
		return
	}
	price, e := adPrice(r.Context(), s.db, typ)
	if e != nil {
		write(w, 200, response{Success: false, Message: "该广告类型尚未配置价格，请联系管理员"})
		return
	}
	total, valid := multiplyPrice(price, months)
	if !valid {
		write(w, 200, response{Success: false, Message: "广告价格配置异常，请联系管理员"})
		return
	}
	expires := addMonths(time.Now().UTC(), months).Format("2006-01-02")
	res, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_advertisements(user_id,title,content,link,expire_date,image_url,ad_type,months,total_amount,status) VALUES(?,?,?,?,?,?,?,?,?,'unpaid')`, u.ID, html.EscapeString(title), nullString(html.EscapeString(content)), nullString(link), expires, nullString(image), typ, months, total)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	id, _ := res.LastInsertId()
	ok(w, "提交成功", map[string]any{"id": id, "total_amount": total, "expire_date": expires})
}
func (s *Server) advertisementUpdate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := chi.URLParam(r, "ad_id")
	var status string
	if s.db.QueryRowContext(r.Context(), `SELECT status FROM xy_advertisements WHERE id=? AND user_id=?`, id, u.ID).Scan(&status) != nil {
		write(w, 200, response{Success: false, Message: "广告不存在或无权修改"})
		return
	}
	if status == "approved" {
		write(w, 200, response{Success: false, Message: "已复核的广告禁止修改"})
		return
	}
	title, typ, months, content, link, image := adParams(r)
	if months <= 0 {
		write(w, 200, response{Success: false, Message: "月数必须大于0"})
		return
	}
	if !validAdType(typ) {
		write(w, 200, response{Success: false, Message: "无效的广告类型"})
		return
	}
	price, e := adPrice(r.Context(), s.db, typ)
	if e != nil {
		write(w, 200, response{Success: false, Message: "该广告类型尚未配置价格，请联系管理员"})
		return
	}
	total, valid := multiplyPrice(price, months)
	if !valid {
		write(w, 200, response{Success: false, Message: "广告价格配置异常，请联系管理员"})
		return
	}
	expires := addMonths(time.Now().UTC(), months).Format("2006-01-02")
	_, e = s.db.ExecContext(r.Context(), `UPDATE xy_advertisements SET title=?,content=?,link=?,expire_date=?,image_url=?,ad_type=?,months=?,total_amount=?,status='unpaid',updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, html.EscapeString(title), nullString(html.EscapeString(content)), nullString(link), expires, nullString(image), typ, months, total, id, u.ID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "修改成功", nil)
}
func (s *Server) advertisementDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM xy_advertisements WHERE id=? AND user_id=?`, chi.URLParam(r, "ad_id"), u.ID)
	adAffected(w, res, e, "广告不存在或无权删除")
}
func adAffected(w http.ResponseWriter, res sql.Result, e error, notFound string) {
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{Success: false, Message: notFound})
		return
	}
	ok(w, "删除成功", nil)
}
func (s *Server) advertisementAdminStatus(status, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if current(r).Role != "ADMIN" {
			write(w, 403, response{Success: false, Message: "需要管理员权限"})
			return
		}
		res, e := s.db.ExecContext(r.Context(), `UPDATE xy_advertisements SET status=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, chi.URLParam(r, "ad_id"))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			write(w, 200, response{Success: false, Message: "广告不存在"})
			return
		}
		ok(w, msg, nil)
	}
}
func (s *Server) advertisementAdminDelete(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "ADMIN" {
		write(w, 403, response{Success: false, Message: "需要管理员权限"})
		return
	}
	res, e := s.db.ExecContext(r.Context(), `DELETE FROM xy_advertisements WHERE id=?`, chi.URLParam(r, "ad_id"))
	adAffected(w, res, e, "广告不存在")
}
func (s *Server) advertisementAdminUpdate(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "ADMIN" {
		write(w, 403, response{Success: false, Message: "需要管理员权限"})
		return
	}
	q := r.URL.Query()
	typ := q.Get("ad_type")
	if !validAdType(typ) {
		write(w, 200, response{Success: false, Message: "无效的广告类型"})
		return
	}
	expire := nullString(q.Get("expire_date"))
	if q.Get("expire_date") != "" {
		if _, e := time.Parse("2006-01-02", q.Get("expire_date")); e != nil {
			write(w, 200, response{Success: false, Message: "日期格式错误"})
			return
		}
	}
	status := q.Get("status")
	if !validAdStatus(status) {
		status = ""
	}
	res, e := s.db.ExecContext(r.Context(), `UPDATE xy_advertisements SET title=?,ad_type=?,content=?,link=?,expire_date=?,image_url=?,status=CASE WHEN ?='' THEN status ELSE ? END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, html.EscapeString(q.Get("title")), typ, nullString(html.EscapeString(q.Get("content"))), nullString(q.Get("link")), expire, nullString(q.Get("image_url")), status, status, chi.URLParam(r, "ad_id"))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		write(w, 200, response{Success: false, Message: "广告不存在"})
		return
	}
	ok(w, "修改成功", nil)
}
