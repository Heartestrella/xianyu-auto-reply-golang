package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

var listingTaskSpec = resourceSpec{"listing_monitor_task", "task", false}

func (s *Server) registerListingMonitorRoutes(r chi.Router) {
	base := "/product-monitor/listing-tasks"
	r.Get(base, s.resourceList(listingTaskSpec))
	r.Post(base, s.resourceCreate(listingTaskSpec))
	r.Get(base+"/overview", s.listingMonitorOverview)
	r.Get(base+"/options", s.listingMonitorOptions)
	r.Get(base+"/remote-risk-config", s.listingRiskConfigGet)
	r.Put(base+"/remote-risk-config", s.listingRiskConfigPut)
	r.Post(base+"/batch-delete", s.resourceBatchDelete(listingTaskSpec))
	r.Post(base+"/batch-update-accounts", s.listingBatchUpdate("accounts"))
	r.Post(base+"/batch-update-category", s.listingBatchUpdate("category"))
	r.Post(base+"/batch-update-dm-content", s.listingBatchUpdate("dm"))
	r.Get(base+"/logs", s.listingMonitorLogs)
	r.Delete(base+"/logs/clear", s.listingMonitorLogsClear)
	r.Post(base+"/logs/copy-cookies", s.listingMonitorCopyCookies)
	r.Get(base+"/items", s.listingMonitorItems)
	r.Post(base+"/items/reset-dm", s.listingMonitorResetDM)
	r.Get(base+"/items/{item_pk}", s.listingMonitorItem)
	r.Put(base+"/{recordID}", s.resourceUpdate(listingTaskSpec))
	r.Put(base+"/{recordID}/status", s.listingMonitorStatus)
	r.Post(base+"/{recordID}/run", s.listingMonitorRun)
}

func (s *Server) listingMonitorOverview(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	data := map[string]any{"total_tasks": 0, "enabled_tasks": 0, "disabled_tasks": 0, "today_run_total": 0, "today_run_success": 0, "today_run_partial": 0, "today_run_failed": 0, "today_collected": 0, "today_new": 0, "today_dm": 0, "today_dm_failed": 0, "today_ordered": 0, "today_order_failed": 0, "today_order_duplicate": 0, "total_items": 0, "total_dm": 0, "total_ordered": 0}
	var totalTasks, enabledTasks, todayRuns, todaySuccess, todayFailed, totalItems, totalDM, totalOrdered int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*),COALESCE(sum(CASE WHEN enabled=1 THEN 1 ELSE 0 END),0) FROM resource_records WHERE owner_id=? AND kind='listing_monitor_task'`, u.ID).Scan(&totalTasks, &enabledTasks)
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*),COALESCE(sum(CASE WHEN level='INFO' THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN level='ERROR' THEN 1 ELSE 0 END),0) FROM event_logs WHERE owner_id=? AND kind='listing_monitor_run' AND date(created_at,'localtime')=date('now','localtime')`, u.ID).Scan(&todayRuns, &todaySuccess, &todayFailed)
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*),COALESCE(sum(CASE WHEN json_extract(data,'$.is_dm_sent')=1 THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN json_extract(data,'$.is_ordered')=1 THEN 1 ELSE 0 END),0) FROM resource_records WHERE owner_id=? AND kind='listing_monitor_item'`, u.ID).Scan(&totalItems, &totalDM, &totalOrdered)
	data["total_tasks"], data["enabled_tasks"], data["disabled_tasks"] = totalTasks, enabledTasks, totalTasks-enabledTasks
	data["today_run_total"], data["today_run_success"], data["today_run_failed"] = todayRuns, todaySuccess, todayFailed
	data["total_items"], data["total_dm"], data["total_ordered"] = totalItems, totalDM, totalOrdered
	ok(w, "查询成功", data)
}

func (s *Server) listingMonitorOptions(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,data FROM resource_records WHERE owner_id=? AND kind='listing_monitor_task' ORDER BY id DESC`, u.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var raw string
		_ = rows.Scan(&id, &raw)
		m := map[string]any{}
		mergeJSON(m, raw)
		list = append(list, map[string]any{"id": id, "keyword": m["keyword"], "monitor_type": valueOr(m["monitor_type"], "listing")})
	}
	ok(w, "查询成功", map[string]any{"list": list})
}

func (s *Server) listingRiskConfigGet(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var raw string
	if s.db.QueryRowContext(r.Context(), `SELECT value FROM user_settings WHERE owner_id=? AND key='listing_monitor.remote_risk'`, u.ID).Scan(&raw) != nil {
		ok(w, "查询成功", map[string]any{"url": "", "secret_key": ""})
		return
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	ok(w, "查询成功", m)
}

func (s *Server) listingRiskConfigPut(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p := map[string]any{}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	raw, _ := json.Marshal(p)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO user_settings(owner_id,key,value,description) VALUES(?,'listing_monitor.remote_risk',?,'商品监控远程风控') ON CONFLICT(owner_id,key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, u.ID, string(raw))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "配置已保存", p)
}

func (s *Server) listingMonitorStatus(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		Enabled bool `json:"is_enabled"`
	}
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	res, err := s.db.ExecContext(r.Context(), `UPDATE resource_records SET enabled=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind='listing_monitor_task' AND id=?`, p.Enabled, u.ID, chi.URLParam(r, "recordID"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "任务不存在")
		return
	}
	ok(w, "状态更新成功", map[string]any{"task": map[string]any{"id": chi.URLParam(r, "recordID"), "is_enabled": p.Enabled}})
}

func (s *Server) listingBatchUpdate(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		p := map[string]any{}
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		rawIDs, okIDs := p["ids"].([]any)
		if !okIDs || len(rawIDs) == 0 {
			fail(w, 400, "请选择任务")
			return
		}
		updated := 0
		for _, rawID := range rawIDs {
			id := fmt.Sprint(rawID)
			var old string
			if s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND kind='listing_monitor_task' AND id=?`, u.ID, id).Scan(&old) != nil {
				continue
			}
			m := map[string]any{}
			_ = json.Unmarshal([]byte(old), &m)
			switch mode {
			case "accounts":
				field := fmt.Sprint(p["field"])
				if field != "order_account_ids" {
					field = "account_ids"
				}
				m[field] = p["account_ids"]
			case "category":
				m["category_id"] = p["category_id"]
			case "dm":
				m["dm_content"] = p["dm_content"]
			}
			encoded, _ := json.Marshal(m)
			if res, _ := s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND id=?`, string(encoded), u.ID, id); res != nil {
				updated++
			}
		}
		ok(w, "批量更新成功", map[string]any{"success_count": updated, "total_count": len(rawIDs)})
	}
}

func (s *Server) listingMonitorRun(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	id := chi.URLParam(r, "recordID")
	var raw string
	if s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND kind='listing_monitor_task' AND id=?`, u.ID, id).Scan(&raw) != nil {
		fail(w, 404, "任务不存在")
		return
	}
	data := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &data)
	logData, _ := json.Marshal(map[string]any{"monitor_task_id": id, "monitor_type": valueOr(data["monitor_type"], "listing"), "keyword": data["keyword"], "trigger_type": "manual", "pages": 0, "fetched_count": 0, "inserted_count": 0, "updated_count": 0, "status": "running"})
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO event_logs(owner_id,kind,level,message,data) VALUES(?,'listing_monitor_run','INFO','监控任务已加入执行队列',?)`, u.ID, string(logData))
	_, _ = s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=json_set(data,'$.last_run_at',CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND id=?`, u.ID, id)
	go s.executeListingMonitor(context.Background(), u.ID, id, data, "manual")
	ok(w, "监控任务已加入执行队列", nil)
}

func monitorAccountIDs(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if id := strings.TrimSpace(fmt.Sprint(item)); id != "" {
				result = append(result, id)
			}
		}
	case []string:
		result = append(result, typed...)
	case string:
		for _, item := range strings.Split(typed, ",") {
			if id := strings.TrimSpace(item); id != "" {
				result = append(result, id)
			}
		}
	}
	return result
}

func (s *Server) executeListingMonitor(ctx context.Context, owner int64, taskID string, task map[string]any, trigger string) error {
	accountIDs := monitorAccountIDs(task["account_ids"])
	if len(accountIDs) == 0 {
		rows, _ := s.db.QueryContext(ctx, `SELECT account_id FROM xy_accounts WHERE owner_id=? AND status='active' ORDER BY id LIMIT 1`, owner)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				accountIDs = append(accountIDs, id)
			}
		}
	}
	status, message := "success", "监控采集完成"
	pagesDone, fetched, inserted, updated := 0, 0, 0, 0
	if len(accountIDs) == 0 {
		status, message = "failed", "没有可用采集账号"
	} else {
		var cookie, proxyType, host string
		var port sql.NullInt64
		var proxyUser, proxyPass sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, owner, accountIDs[0]).Scan(&cookie, &proxyType, &host, &port, &proxyUser, &proxyPass)
		if err != nil {
			status, message = "failed", "采集账号不存在"
		} else {
			pagesN := intNumber(task["collect_pages"])
			if pagesN < 1 {
				pagesN = intNumber(task["pages"])
			}
			if pagesN < 1 {
				pagesN = 1
			}
			if pagesN > 50 {
				pagesN = 50
			}
			keyword := firstString(task, "keyword")
			for pageNo := 1; pageNo <= pagesN; pageNo++ {
				data := map[string]any{"pageNumber": pageNo, "keyword": keyword, "fromFilter": true, "rowsPerPage": 30, "sortValue": firstString(task, "sort_value"), "sortField": firstString(task, "sort_field"), "customDistance": "", "gps": "", "propValueStr": map[string]any{"searchFilter": "quickFilter:filterPersonal;"}, "customGps": "", "searchReqFromPage": "pcSearch", "extraFilterValue": "{}", "userPositionJson": "{}"}
				result := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idlemtopsearch.pc.search", Version: "1.0", Cookie: cookie, Proxy: crawlerProxy(proxyType, host, port, proxyUser, proxyPass), Data: data, ExtraParams: map[string]string{"spm_cnt": "a21ybx.search.0.0"}})
				if !result.Success {
					status, message = "failed", firstNonemptyAPI(result.Error, "采集接口调用失败")
					break
				}
				cookie = result.Cookie
				items := crawlerResultItems(result.Response)
				pagesDone++
				fetched += len(items)
				for _, item := range items {
					itemID := firstString(item, "item_id")
					if itemID == "" {
						continue
					}
					item["monitor_task_id"] = taskID
					item["keyword"] = keyword
					item["is_dm_sent"] = false
					item["is_ordered"] = false
					item["collected_at"] = time.Now().Format(time.RFC3339)
					raw, _ := json.Marshal(item)
					var recordID int64
					err = s.db.QueryRowContext(ctx, `SELECT id FROM resource_records WHERE owner_id=? AND kind='listing_monitor_item' AND json_extract(data,'$.monitor_task_id')=? AND json_extract(data,'$.item_id')=?`, owner, taskID, itemID).Scan(&recordID)
					if err == sql.ErrNoRows {
						if _, err = s.db.ExecContext(ctx, `INSERT INTO resource_records(owner_id,kind,account_id,enabled,data) VALUES(?,'listing_monitor_item',?,1,?)`, owner, accountIDs[0], string(raw)); err == nil {
							inserted++
						}
					} else if err == nil {
						delete(item, "is_dm_sent")
						delete(item, "is_ordered")
						raw, _ = json.Marshal(item)
						if _, err = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_patch(data,?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), recordID); err == nil {
							updated++
						}
					}
				}
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=?`, cookie, owner, accountIDs[0])
		}
	}
	level := "INFO"
	if status == "failed" {
		level = "ERROR"
	}
	logData, _ := json.Marshal(map[string]any{"monitor_task_id": taskID, "monitor_type": valueOr(task["monitor_type"], "listing"), "keyword": task["keyword"], "trigger_type": trigger, "pages": pagesDone, "fetched_count": fetched, "inserted_count": inserted, "updated_count": updated, "status": status})
	_, _ = s.db.ExecContext(ctx, `INSERT INTO event_logs(owner_id,kind,level,message,data) VALUES(?,'listing_monitor_run',?,?,?)`, owner, level, message, string(logData))
	_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.last_run_at',CURRENT_TIMESTAMP,'$.last_status',?,'$.last_message',?),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind='listing_monitor_task' AND id=?`, status, message, owner, taskID)
	if status == "failed" {
		return errors.New(message)
	}
	return nil
}

// ExecuteScheduledListingMonitor runs due enabled monitor definitions through
// the exact collector used by the manual endpoint.
func (s *Server) ExecuteScheduledListingMonitor(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_id,data FROM resource_records WHERE kind='listing_monitor_task' AND enabled=1 AND COALESCE(json_extract(data,'$.status'),'enabled') NOT IN ('disabled','paused') AND (json_extract(data,'$.last_run_at') IS NULL OR datetime(json_extract(data,'$.last_run_at'),'+'||MAX(COALESCE(json_extract(data,'$.interval_seconds'),json_extract(data,'$.interval'),300),30)||' seconds')<=CURRENT_TIMESTAMP) ORDER BY id LIMIT 50`)
	if err != nil {
		return err
	}
	type dueTask struct {
		id, owner int64
		raw       string
	}
	due := []dueTask{}
	for rows.Next() {
		var task dueTask
		if rows.Scan(&task.id, &task.owner, &task.raw) == nil {
			due = append(due, task)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	failures := []string{}
	for _, row := range due {
		task := map[string]any{}
		if json.Unmarshal([]byte(row.raw), &task) != nil {
			failures = append(failures, fmt.Sprintf("任务%d配置无效", row.id))
			continue
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.last_run_at',CURRENT_TIMESTAMP,'$.last_status','running'),updated_at=CURRENT_TIMESTAMP WHERE id=?`, row.id)
		if err = s.executeListingMonitor(ctx, row.owner, strconv.FormatInt(row.id, 10), task, "scheduled"); err != nil {
			failures = append(failures, fmt.Sprintf("任务%d: %v", row.id, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// ExecuteScheduledDMSend sends configured follow-up messages only after a
// monitored item was successfully ordered, matching the original safety gate.
func (s *Server) ExecuteScheduledDMSend(ctx context.Context) error {
	if s.chatCreate == nil || s.chatSend == nil {
		return errors.New("聊天创建器或消息发送器未配置")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.owner_id,i.data,t.data FROM resource_records i JOIN resource_records t ON t.owner_id=i.owner_id AND t.kind='listing_monitor_task' AND CAST(t.id AS TEXT)=CAST(json_extract(i.data,'$.monitor_task_id') AS TEXT) WHERE i.kind='listing_monitor_item' AND i.enabled=1 AND t.enabled=1 AND COALESCE(json_extract(i.data,'$.order_status'),'')='success' AND COALESCE(json_extract(i.data,'$.is_dm_sent'),0)=0 AND COALESCE(json_extract(i.data,'$.dm_attempts'),0)<3 AND COALESCE(json_extract(i.data,'$.ordered_at'),i.updated_at)>=datetime('now','-2 days') AND trim(COALESCE(json_extract(i.data,'$.seller_user_id'),json_extract(i.data,'$.seller_id'),''))<>'' ORDER BY i.id LIMIT 500`)
	if err != nil {
		return err
	}
	type dmJob struct {
		id, owner        int64
		itemRaw, taskRaw string
	}
	jobs := []dmJob{}
	for rows.Next() {
		var job dmJob
		if rows.Scan(&job.id, &job.owner, &job.itemRaw, &job.taskRaw) == nil {
			jobs = append(jobs, job)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	failures := []string{}
	perTask := map[string]int{}
	dmRotation := map[string]int{}
	candidateCache := map[string]monitoredOrderAccounts{}
	disabledDMAccounts := map[string]bool{}
	for _, job := range jobs {
		item, task := map[string]any{}, map[string]any{}
		if json.Unmarshal([]byte(job.itemRaw), &item) != nil || json.Unmarshal([]byte(job.taskRaw), &task) != nil {
			continue
		}
		content := strings.TrimSpace(firstString(task, "dm_content"))
		if content == "" {
			continue
		}
		taskID := fmt.Sprint(item["monitor_task_id"])
		limit := intNumber(task["dm_batch_size"])
		if limit < 1 {
			limit = 5
		}
		if perTask[taskID] >= limit {
			continue
		}
		accountID := strings.TrimSpace(firstString(item, "order_account_id"))
		strict := accountID != ""
		candidates := []platformAccount{}
		if strict {
			account, unavailable, exists, loadErr := s.loadMonitoredOrderAccount(ctx, accountID)
			if loadErr != nil {
				failures = append(failures, fmt.Sprintf("商品%d: 查询下单账号失败: %v", job.id, loadErr))
				continue
			}
			if !exists {
				_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','failed','$.dm_fail_reason','下单账号已删除','$.dm_attempts',3),updated_at=CURRENT_TIMESTAMP WHERE id=?`, job.id)
				failures = append(failures, fmt.Sprintf("商品%d下单账号已删除", job.id))
				perTask[taskID]++
				continue
			}
			if unavailable != "" {
				_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','waiting','$.dm_fail_reason',?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, "下单账号暂不可用："+unavailable, job.id)
				continue
			}
			if s.online != nil && !s.online(accountID) {
				_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','waiting','$.dm_fail_reason','下单账号IM未连接'),updated_at=CURRENT_TIMESTAMP WHERE id=?`, job.id)
				continue
			}
			candidates = []platformAccount{account}
		} else {
			resolved, cached := candidateCache[taskID]
			if !cached {
				var resolveErr error
				resolved, resolveErr = s.resolveMonitoredOrderAccounts(ctx, job.owner, fallbackInt64(task["category_id"]), monitorAccountIDs(task["order_account_ids"]))
				if resolveErr != nil {
					failures = append(failures, fmt.Sprintf("商品%d: 加载下单账号失败: %v", job.id, resolveErr))
					continue
				}
				candidateCache[taskID] = resolved
			}
			candidates = append(candidates, resolved.task...)
			candidates = append(candidates, resolved.fallback...)
			if len(candidates) > 1 {
				start := dmRotation[taskID] % len(candidates)
				candidates = append(append([]platformAccount{}, candidates[start:]...), candidates[:start]...)
			}
			dmRotation[taskID]++
			if len(candidates) == 0 {
				reason := "无可用下单账号，等待下次重试"
				if len(resolved.unavailable) > 0 {
					reason = "无可用下单账号：" + strings.Join(resolved.unavailable, "；")
				}
				_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','waiting','$.dm_fail_reason',?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(reason, 500), job.id)
				continue
			}
		}
		sellerID := strings.TrimSpace(firstString(item, "seller_user_id", "seller_id"))
		itemID := strings.TrimSpace(firstString(item, "item_id"))
		var chatID string
		var sendErr error
		usedAccount := ""
		unavailableReasons := []string{}
		for _, candidate := range candidates {
			candidateID := candidate.AccountID
			if !strict {
				if disabledDMAccounts[candidateID] {
					unavailableReasons = append(unavailableReasons, candidateID+" 本轮已判不可用")
					continue
				}
				if s.online != nil && !s.online(candidateID) {
					disabledDMAccounts[candidateID] = true
					unavailableReasons = append(unavailableReasons, candidateID+" IM未连接")
					continue
				}
			}
			chatID, sendErr = s.chatCreate(ctx, candidateID, sellerID, itemID)
			if sendErr == nil {
				_, sendErr = s.chatSend(ctx, candidateID, chatID, sellerID, content)
			}
			if sendErr == nil {
				usedAccount = candidateID
				break
			}
			if strict {
				break
			}
			disabledDMAccounts[candidateID] = true
			unavailableReasons = append(unavailableReasons, candidateID+": "+sendErr.Error())
		}
		if usedAccount == "" && sendErr == nil {
			reason := "所有候选下单账号IM未连接，等待下次重试"
			if len(unavailableReasons) > 0 {
				reason = strings.Join(unavailableReasons, "；")
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','waiting','$.dm_fail_reason',?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(reason, 500), job.id)
			continue
		}
		if sendErr != nil {
			if strict {
				_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','waiting','$.dm_fail_reason',?,'$.dm_account_id',NULL),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(sendErr.Error(), 500), job.id)
				continue
			}
			reason := sendErr.Error()
			if len(unavailableReasons) > 0 {
				reason = strings.Join(unavailableReasons, "；")
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.dm_status','waiting','$.dm_fail_reason',?,'$.dm_account_id',NULL),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(reason, 500), job.id)
			continue
		}
		perTask[taskID]++
		_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.is_dm_sent',true,'$.dm_status','success','$.dm_fail_reason',NULL,'$.dm_account_id',?,'$.dm_chat_id',?,'$.dm_sent_at',CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=?`, usedAccount, chatID, job.id)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) fetchMonitorItemDetail(ctx context.Context, account platformAccount, itemID string) xianyu.MTOPResult {
	if s.itemDetailFetch != nil {
		return s.itemDetailFetch(ctx, account, itemID)
	}
	return xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.taobao.idle.pc.detail", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/item?id=" + itemID, Data: map[string]any{"itemId": itemID}, ExtraParams: map[string]string{"spm_cnt": "a21ybx.item.0.0"}})
}

// ExecuteScheduledSellerFill resolves the seller's stable numeric user ID
// from the item-detail API. Search result seller fields alone are insufficient
// for creating an IM conversation.
func (s *Server) ExecuteScheduledSellerFill(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.owner_id,i.data,COALESCE(t.data,'{}') FROM resource_records i LEFT JOIN resource_records t ON t.owner_id=i.owner_id AND t.kind='listing_monitor_task' AND CAST(t.id AS TEXT)=CAST(json_extract(i.data,'$.monitor_task_id') AS TEXT) WHERE i.kind='listing_monitor_item' AND i.created_at>=datetime('now','-2 days') AND trim(COALESCE(json_extract(i.data,'$.seller_user_id'),''))='' AND COALESCE(json_extract(i.data,'$.seller_fill_status'),'')<>'failed' AND COALESCE(json_extract(i.data,'$.order_status'),'')<>'duplicate' ORDER BY i.owner_id,i.id LIMIT 500`)
	if err != nil {
		return err
	}
	type fillJob struct {
		id, owner        int64
		itemRaw, taskRaw string
	}
	jobs := []fillJob{}
	for rows.Next() {
		var job fillJob
		if rows.Scan(&job.id, &job.owner, &job.itemRaw, &job.taskRaw) == nil {
			jobs = append(jobs, job)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	disabled := map[string]bool{}
	failures := []string{}
	for _, job := range jobs {
		item, task := map[string]any{}, map[string]any{}
		if json.Unmarshal([]byte(job.itemRaw), &item) != nil {
			continue
		}
		_ = json.Unmarshal([]byte(job.taskRaw), &task)
		itemID := strings.TrimSpace(firstString(item, "item_id"))
		if itemID == "" {
			continue
		}
		accountIDs := monitorAccountIDs(task["account_ids"])
		query := `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND status='active'`
		args := []any{job.owner}
		if len(accountIDs) > 0 {
			query += ` AND account_id IN (` + placeholders(len(accountIDs)) + `)`
			for _, id := range accountIDs {
				args = append(args, id)
			}
		}
		query += ` ORDER BY id`
		accountRows, qerr := s.db.QueryContext(ctx, query, args...)
		if qerr != nil {
			failures = append(failures, qerr.Error())
			continue
		}
		accounts := []platformAccount{}
		for accountRows.Next() {
			var a platformAccount
			if accountRows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass) == nil && !disabled[a.AccountID] {
				accounts = append(accounts, a)
			}
		}
		accountRows.Close()
		if len(accounts) == 0 {
			continue
		}
		filled, terminal := false, false
		lastError := ""
		for _, account := range accounts {
			result := s.fetchMonitorItemDetail(ctx, account, itemID)
			if result.Cookie != "" && result.Cookie != account.Cookie {
				_, _ = s.db.ExecContext(ctx, `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, result.Cookie, account.ID)
			}
			if result.AccountInvalid {
				disabled[account.AccountID] = true
				lastError = result.Error
				continue
			}
			if !result.Success {
				lastError = firstNonemptyAPI(result.Error, "商品详情获取失败")
				if len(result.Response) > 0 {
					terminal = true
					_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.seller_fill_status','failed','$.seller_fill_fail_reason',?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(lastError, 500), job.id)
				}
				break
			}
			detail := nestedMap(result.Response, "data")
			seller := nestedMap(detail, "sellerDO")
			sellerID := strings.TrimSpace(firstMapString(seller, "sellerId"))
			sellerNick := strings.TrimSpace(firstMapString(seller, "nick"))
			if sellerID == "" {
				lastError = "商品详情缺少sellerDO.sellerId"
				terminal = true
				_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.seller_fill_status','failed','$.seller_fill_fail_reason',?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, lastError, job.id)
				break
			}
			detailJSON, _ := json.Marshal(detail)
			if len(detailJSON) > 10<<20 {
				detailJSON = []byte("null")
			}
			_, err = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.seller_user_id',?,'$.seller_nick',?,'$.seller_fill_status','filled','$.seller_fill_fail_reason',NULL,'$.detail',json(?)),updated_at=CURRENT_TIMESTAMP WHERE id=?`, sellerID, sellerNick, string(detailJSON), job.id)
			if err != nil {
				lastError = err.Error()
				break
			}
			filled = true
			break
		}
		if !filled && !terminal && lastError != "" {
			_, _ = s.db.ExecContext(ctx, `UPDATE resource_records SET data=json_set(data,'$.seller_fill_last_error',?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(lastError, 500), job.id)
			failures = append(failures, fmt.Sprintf("商品%s: %s", itemID, lastError))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) listingMonitorLogs(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	p, size := page(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM event_logs WHERE owner_id=? AND kind='listing_monitor_run'`, u.ID).Scan(&total)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,level,message,data,created_at FROM event_logs WHERE owner_id=? AND kind='listing_monitor_run' ORDER BY id DESC LIMIT ? OFFSET ?`, u.ID, size, (p-1)*size)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var level, message, raw, created string
		_ = rows.Scan(&id, &level, &message, &raw, &created)
		m := map[string]any{"id": id, "status": strings.ToLower(level), "message": message, "created_at": created}
		mergeJSON(m, raw)
		list = append(list, m)
	}
	ok(w, "查询成功", map[string]any{"list": list, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}

func (s *Server) listingMonitorLogsClear(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM event_logs WHERE owner_id=? AND kind='listing_monitor_run' AND created_at<datetime('now','-10 days')`, u.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	ok(w, "已清理10天前的监控日志", map[string]any{"deleted_count": n})
}

func (s *Server) listingMonitorCopyCookies(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择日志")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT account_id,cookie FROM xy_accounts WHERE owner_id=? AND status='active'`, u.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var accountID, cookie string
		_ = rows.Scan(&accountID, &cookie)
		list = append(list, map[string]any{"account_id": accountID, "cookies": cookie, "secret_key": ""})
	}
	ok(w, "查询成功", map[string]any{"list": list})
}

func (s *Server) listingMonitorItems(w http.ResponseWriter, r *http.Request) {
	s.resourceList(resourceSpec{"listing_monitor_item", "item", false})(w, r)
}
func (s *Server) listingMonitorResetDM(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var p struct {
		IDs []int64 `json:"ids"`
	}
	if decode(r, &p) != nil || len(p.IDs) == 0 {
		fail(w, 400, "请选择商品")
		return
	}
	updated := 0
	for _, id := range p.IDs {
		res, _ := s.db.ExecContext(r.Context(), `UPDATE resource_records SET data=json_set(data,'$.is_dm_sent',false,'$.dm_status',NULL,'$.dm_fail_reason',NULL),updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND kind='listing_monitor_item' AND id=?`, u.ID, id)
		if res != nil {
			n, _ := res.RowsAffected()
			updated += int(n)
		}
	}
	ok(w, "重置成功", map[string]any{"success_count": updated, "total_count": len(p.IDs)})
}

func (s *Server) listingMonitorItem(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var id int64
	var enabled bool
	var raw, created, updated string
	err := s.db.QueryRowContext(r.Context(), `SELECT id,enabled,data,created_at,updated_at FROM resource_records WHERE owner_id=? AND kind='listing_monitor_item' AND id=?`, u.ID, chi.URLParam(r, "item_pk")).Scan(&id, &enabled, &raw, &created, &updated)
	if err == sql.ErrNoRows {
		fail(w, 404, "商品不存在")
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "查询成功", map[string]any{"item": compatibilityRecord(id, enabled, raw, created, updated)})
}
