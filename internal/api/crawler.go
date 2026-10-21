package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

const crawlerJobKind = "goofish_crawl_job"
const crawlerItemKind = "goofish_crawl_item"

func (s *Server) registerCrawlerRoutes(r chi.Router) {
	r.Get("/goofish/crawler/jobs", s.crawlerJobsList)
	r.Post("/goofish/crawler/jobs", s.crawlerJobCreate)
	r.Post("/goofish/crawler/jobs/{job_id}/start", s.crawlerJobStart)
	r.Post("/goofish/crawler/jobs/{job_id}/stop", s.crawlerJobStop)
	r.Get("/goofish/crawler/jobs/{job_id}/status", s.crawlerJobStatus)
	r.Post("/goofish/crawler/jobs/{job_id}/run-once", s.crawlerJobRunOnce)
	r.Get("/goofish/crawler/jobs/{job_id}/items", s.crawlerJobItems)
	r.Delete("/goofish/crawler/jobs/{job_id}", s.crawlerJobDelete)
}

func (s *Server) crawlerJobsList(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind=?`, u.ID, crawlerJobKind).Scan(&total)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,enabled,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind=? ORDER BY id DESC LIMIT ? OFFSET ?`, u.ID, crawlerJobKind, size, (p-1)*size)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var enabled bool
		var raw, created, updated string
		_ = rows.Scan(&id, &enabled, &raw, &created, &updated)
		m := map[string]any{"id": id, "enabled": enabled, "created_at": created, "updated_at": updated, "running": s.crawlerRunning(id)}
		mergeJSON(m, raw)
		items = append(items, m)
	}
	ok(w, "查询成功", map[string]any{"items": items, "list": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}

func validateCrawlerJob(p map[string]any) error {
	if firstString(p, "cookie_id", "account_id") == "" || firstString(p, "keyword") == "" {
		return fmt.Errorf("账号和搜索关键词不能为空")
	}
	pagesN := intNumber(p["pages"])
	if pagesN == 0 {
		p["pages"] = 1
	} else if pagesN < 1 || pagesN > 50 {
		return fmt.Errorf("采集页数须为1到50")
	}
	interval := intNumber(p["interval_seconds"])
	if interval == 0 {
		p["interval_seconds"] = 900
	} else if interval < 60 {
		return fmt.Errorf("执行间隔不能少于60秒")
	}
	if intNumber(p["page_size"]) == 0 {
		p["page_size"] = 30
	}
	if intNumber(p["start_page"]) == 0 {
		p["start_page"] = 1
	}
	return nil
}
func (s *Server) crawlerJobCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := validateCrawlerJob(p); err != nil {
		write(w, 200, response{Success: false, Message: err.Error()})
		return
	}
	aid := firstString(p, "cookie_id", "account_id")
	var exists int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, aid).Scan(&exists)
	if exists == 0 {
		write(w, 200, response{Success: false, Message: "账号不存在"})
		return
	}
	p["cookie_id"] = aid
	p["last_error"] = nil
	p["last_run_at"] = nil
	enabled := compatibilityEnabled(p, true)
	raw, _ := json.Marshal(p)
	res, err := s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,?,?)`, u.ID, crawlerJobKind, aid, enabled, string(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	if enabled {
		s.startCrawler(id, u.ID)
	}
	ok(w, "采集任务已创建", map[string]any{"id": id})
}

func (s *Server) loadCrawlerJob(ctx context.Context, owner, id int64) (map[string]any, bool, error) {
	var raw string
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT data,enabled FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, owner, crawlerJobKind, id).Scan(&raw, &enabled)
	m := map[string]any{}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &m)
	}
	return m, enabled, err
}
func crawlerID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "job_id"), 10, 64)
}
func (s *Server) crawlerRunning(id int64) bool {
	s.crawlMu.Lock()
	defer s.crawlMu.Unlock()
	_, ok := s.crawlJobs[id]
	return ok
}
func (s *Server) startCrawler(id, owner int64) {
	s.crawlMu.Lock()
	if cancel := s.crawlJobs[id]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	token := fmt.Sprint(time.Now().UnixNano())
	s.crawlJobs[id] = cancel
	s.crawlTokens[id] = token
	s.crawlMu.Unlock()
	go func() {
		defer func() {
			s.crawlMu.Lock()
			if s.crawlTokens[id] == token {
				delete(s.crawlJobs, id)
				delete(s.crawlTokens, id)
			}
			s.crawlMu.Unlock()
		}()
		for {
			job, enabled, err := s.loadCrawlerJob(ctx, owner, id)
			if err != nil || !enabled {
				return
			}
			_ = s.runCrawlerOnce(ctx, owner, id, job)
			interval := intNumber(job["interval_seconds"])
			if interval < 60 {
				interval = 900
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(interval) * time.Second):
			}
		}
	}()
}
func (s *Server) stopCrawler(id int64) {
	s.crawlMu.Lock()
	if cancel := s.crawlJobs[id]; cancel != nil {
		cancel()
		delete(s.crawlJobs, id)
		delete(s.crawlTokens, id)
	}
	s.crawlMu.Unlock()
}

func (s *Server) crawlerJobStart(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, err := crawlerID(r)
	if err != nil {
		fail(w, 400, "任务ID无效")
		return
	}
	res, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=1,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind=? AND id=?`, u.ID, crawlerJobKind, id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "任务不存在")
		return
	}
	s.startCrawler(id, u.ID)
	ok(w, "任务已启动", nil)
}
func (s *Server) crawlerJobStop(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, err := crawlerID(r)
	if err != nil {
		fail(w, 400, "任务ID无效")
		return
	}
	res, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=0,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind=? AND id=?`, u.ID, crawlerJobKind, id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "任务不存在")
		return
	}
	s.stopCrawler(id)
	ok(w, "任务已停止", nil)
}
func (s *Server) crawlerJobStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, err := crawlerID(r)
	if err != nil {
		fail(w, 400, "任务ID无效")
		return
	}
	job, enabled, err := s.loadCrawlerJob(r.Context(), u.ID, id)
	if err != nil {
		fail(w, 404, "任务不存在")
		return
	}
	job["id"], job["enabled"], job["running"] = id, enabled, s.crawlerRunning(id)
	ok(w, "查询成功", job)
}
func (s *Server) crawlerJobRunOnce(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, err := crawlerID(r)
	if err != nil {
		fail(w, 400, "任务ID无效")
		return
	}
	job, _, err := s.loadCrawlerJob(r.Context(), u.ID, id)
	if err != nil {
		fail(w, 404, "任务不存在")
		return
	}
	result := s.runCrawlerOnce(r.Context(), u.ID, id, job)
	if result["success"] != true {
		write(w, 200, response{Success: false, Message: fmt.Sprint(result["error"]), Data: result})
		return
	}
	ok(w, "采集完成", result)
}

func crawlerProxy(proxyType, host string, port sql.NullInt64, user, pass sql.NullString) string {
	if proxy.Enabled() {
		return proxy.URL()
	}
	if host == "" || !port.Valid || proxyType == "" || proxyType == "none" {
		return ""
	}
	u := &url.URL{Scheme: proxyType, Host: fmt.Sprintf("%s:%d", host, port.Int64)}
	if user.Valid {
		u.User = url.UserPassword(user.String, pass.String)
	}
	return u.String()
}
func (s *Server) runCrawlerOnce(ctx context.Context, owner, id int64, job map[string]any) map[string]any {
	aid := firstString(job, "cookie_id")
	var cookie, proxyType, host string
	var port sql.NullInt64
	var user, pass sql.NullString
	if s.db.QueryRowContext(ctx, `SELECT cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, owner, aid).Scan(&cookie, &proxyType, &host, &port, &user, &pass) != nil {
		return map[string]any{"success": false, "error": "account_cookie_missing"}
	}
	pagesN := intNumber(job["pages"])
	start := intNumber(job["start_page"])
	pageSize := intNumber(job["page_size"])
	if pagesN < 1 {
		pagesN = 1
	}
	if start < 1 {
		start = 1
	}
	if pageSize < 1 {
		pageSize = 30
	}
	upserted, total := 0, 0
	var runErr string
	for pageNo := start; pageNo < start+pagesN; pageNo++ {
		data := map[string]any{"pageNumber": pageNo, "keyword": firstString(job, "keyword"), "fromFilter": true, "rowsPerPage": pageSize, "sortValue": firstString(job, "sort_value"), "sortField": firstString(job, "sort_field"), "customDistance": "", "gps": "", "propValueStr": map[string]any{"searchFilter": "quickFilter:filterPersonal;"}, "customGps": "", "searchReqFromPage": "pcSearch", "extraFilterValue": "{}", "userPositionJson": "{}"}
		result := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idlemtopsearch.pc.search", Version: "1.0", Cookie: cookie, Proxy: crawlerProxy(proxyType, host, port, user, pass), Data: data, ExtraParams: map[string]string{"spm_cnt": "a21ybx.search.0.0", "spm_pre": "a21ybx.home.searchInput.0"}})
		if result.Cookie != cookie {
			cookie = result.Cookie
			_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, cookie, owner, aid)
		}
		if !result.Success {
			runErr = result.Error
			break
		}
		items := crawlerResultItems(result.Response)
		total += len(items)
		for _, item := range items {
			itemID := firstString(item, "item_id")
			if itemID == "" {
				continue
			}
			item["job_id"] = id
			item["fetched_at"] = time.Now().Format(time.RFC3339)
			raw, _ := json.Marshal(item)
			var existing int64
			err := s.db.QueryRowContext(ctx, `SELECT id FROM resource_records WHERE owner_id=? AND kind=? AND account_id=? AND json_extract(data,'$.job_id')=? AND json_extract(data,'$.item_id')=?`, owner, crawlerItemKind, aid, id, itemID).Scan(&existing)
			if err == sql.ErrNoRows {
				_, err = s.db.ExecContext(ctx, `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,?,?,1,?)`, owner, crawlerItemKind, aid, string(raw))
			} else if err == nil {
				_, err = s.db.ExecContext(ctx, `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), existing)
			}
			if err == nil {
				upserted++
			}
		}
	}
	job["last_run_at"] = time.Now().Format(time.RFC3339)
	if runErr != "" {
		job["last_error"] = runErr
	} else {
		job["last_error"] = nil
	}
	raw, _ := json.Marshal(job)
	_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND id=?`, string(raw), owner, id)
	return map[string]any{"success": runErr == "", "upserted": upserted, "total": total, "error": runErr}
}

func crawlerResultItems(response map[string]any) []map[string]any {
	data, _ := response["data"].(map[string]any)
	list, _ := data["resultList"].([]any)
	out := []map[string]any{}
	for _, entry := range list {
		root, _ := entry.(map[string]any)
		d, _ := root["data"].(map[string]any)
		item, _ := d["item"].(map[string]any)
		main, _ := item["main"].(map[string]any)
		ex, _ := main["exContent"].(map[string]any)
		click, _ := main["clickParam"].(map[string]any)
		args, _ := click["args"].(map[string]any)
		id := firstString(ex, "itemId")
		if id == "" {
			id = firstString(args, "item_id", "id")
		}
		if id == "" {
			continue
		}
		price := firstString(args, "price", "displayPrice")
		if price == "" {
			detail, _ := ex["detailParams"].(map[string]any)
			price = firstString(detail, "soldPrice")
		}
		out = append(out, map[string]any{"item_id": id, "title": ex["title"], "price": price, "area": ex["area"], "main_image": ex["picUrl"], "seller_name": ex["userNickName"], "seller_id": args["seller_id"], "publish_time": args["publishTime"], "item_url": main["targetUrl"], "raw": main})
	}
	return out
}

func (s *Server) crawlerJobItems(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, err := crawlerID(r)
	if err != nil {
		fail(w, 400, "任务ID无效")
		return
	}
	p, size := page(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.job_id')=?`, u.ID, crawlerItemKind, id).Scan(&total)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.job_id')=? ORDER BY id DESC LIMIT ? OFFSET ?`, u.ID, crawlerItemKind, id, size, (p-1)*size)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var rowID int64
		var raw, created, updated string
		_ = rows.Scan(&rowID, &raw, &created, &updated)
		m := map[string]any{"id": rowID, "created_at": created, "updated_at": updated}
		mergeJSON(m, raw)
		items = append(items, m)
	}
	ok(w, "查询成功", map[string]any{"items": items, "list": items, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}
func (s *Server) crawlerJobDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id, err := crawlerID(r)
	if err != nil {
		fail(w, 400, "任务ID无效")
		return
	}
	s.stopCrawler(id)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND json_extract(data,'$.job_id')=?`, u.ID, crawlerItemKind, id)
	res, err := tx.ExecContext(r.Context(), `DELETE FROM resource_records WHERE owner_id=? AND kind=? AND id=?`, u.ID, crawlerJobKind, id)
	if err == nil {
		err = tx.Commit()
	}
	affected(w, res, err, "任务已删除")
}
