package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

var accountExportHeaders = []string{"account_id", "cookie", "status", "remark", "username", "login_password", "show_browser", "proxy_type", "proxy_host", "proxy_port", "proxy_user", "proxy_pass"}

func (s *Server) accountExportExcel(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	var filter struct {
		AccountIDs  []string `json:"account_ids"`
		Status      string   `json:"status"`
		AccountID   string   `json:"account_id"`
		HasPassword *bool    `json:"has_password"`
	}
	_ = decode(r, &filter)
	query := `SELECT account_id,cookie,status,COALESCE(remark,''),COALESCE(username,''),COALESCE(login_password,''),show_browser,proxy_type,COALESCE(proxy_host,''),proxy_port,COALESCE(proxy_user,''),COALESCE(proxy_pass,'') FROM xy_accounts WHERE owner_id=?`
	args := []any{u.ID}
	if len(filter.AccountIDs) > 0 {
		query += ` AND account_id IN (` + placeholders(len(filter.AccountIDs)) + `)`
		for _, id := range filter.AccountIDs {
			args = append(args, id)
		}
	}
	if filter.Status != "" {
		query += ` AND status=?`
		args = append(args, filter.Status)
	}
	if filter.AccountID != "" {
		query += ` AND account_id LIKE ?`
		args = append(args, "%"+filter.AccountID+"%")
	}
	if filter.HasPassword != nil {
		if *filter.HasPassword {
			query += ` AND COALESCE(login_password,'')<>''`
		} else {
			query += ` AND COALESCE(login_password,'')=''`
		}
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	book := excelize.NewFile()
	sheet := "账号"
	book.SetSheetName("Sheet1", sheet)
	for column, header := range accountExportHeaders {
		cell, _ := excelize.CoordinatesToCellName(column+1, 1)
		_ = book.SetCellValue(sheet, cell, header)
	}
	rowNumber := 2
	for rows.Next() {
		var accountID, cookie, status, remark, username, password, proxyType, proxyHost, proxyUser, proxyPass string
		var showBrowser bool
		var proxyPort any
		if err = rows.Scan(&accountID, &cookie, &status, &remark, &username, &password, &showBrowser, &proxyType, &proxyHost, &proxyPort, &proxyUser, &proxyPass); err != nil {
			continue
		}
		values := []any{accountID, cookie, status, remark, username, password, showBrowser, proxyType, proxyHost, proxyPort, proxyUser, proxyPass}
		for column, value := range values {
			cell, _ := excelize.CoordinatesToCellName(column+1, rowNumber)
			_ = book.SetCellValue(sheet, cell, value)
		}
		rowNumber++
	}
	_ = book.SetColWidth(sheet, "A", "A", 20)
	_ = book.SetColWidth(sheet, "B", "B", 70)
	_ = book.SetColWidth(sheet, "C", "L", 18)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="xianyu-accounts-%s.xlsx"`, time.Now().Format("20060102-150405")))
	if err = book.Write(w); err != nil {
		s.log.Error("write account export", "error", err)
	}
	_ = book.Close()
}

func (s *Server) accountImportExcel(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		fail(w, 400, "导入文件过大或格式无效")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择Excel文件")
		return
	}
	defer file.Close()
	book, err := excelize.OpenReader(file)
	if err != nil {
		fail(w, 400, "无法读取Excel文件: "+err.Error())
		return
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		fail(w, 400, "Excel文件没有工作表")
		return
	}
	rows, err := book.GetRows(sheets[0])
	if err != nil || len(rows) < 2 {
		fail(w, 400, "Excel文件没有账号数据")
		return
	}
	headerIndex := map[string]int{}
	for index, header := range rows[0] {
		headerIndex[strings.ToLower(strings.TrimSpace(header))] = index
	}
	find := func(row []string, names ...string) string {
		for _, name := range names {
			if index, ok := headerIndex[name]; ok && index < len(row) {
				return strings.TrimSpace(row[index])
			}
		}
		return ""
	}
	enableAll, _ := strconv.ParseBool(r.FormValue("enable_all"))
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	inserted, updated, failed := 0, 0, 0
	errorsList := []string{}
	for rowIndex, row := range rows[1:] {
		accountID := find(row, "account_id", "id", "账号id", "账号")
		cookie := find(row, "cookie", "value", "cookies", "cookie值")
		if accountID == "" || cookie == "" {
			failed++
			errorsList = append(errorsList, fmt.Sprintf("第%d行缺少account_id或cookie", rowIndex+2))
			continue
		}
		status := find(row, "status", "状态")
		if enableAll {
			status = "active"
		}
		if status != "inactive" {
			status = "active"
		}
		var exists int
		_ = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_accounts WHERE owner_id=? AND account_id=?`, u.ID, accountID).Scan(&exists)
		showBrowser, _ := strconv.ParseBool(find(row, "show_browser"))
		port, _ := strconv.Atoi(find(row, "proxy_port"))
		result, execErr := tx.ExecContext(r.Context(), `INSERT INTO xy_accounts(owner_id,account_id,cookie,status,remark,username,login_password,show_browser,proxy_type,proxy_host,proxy_port,proxy_user,proxy_pass) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET cookie=excluded.cookie,status=excluded.status,remark=excluded.remark,username=excluded.username,login_password=excluded.login_password,show_browser=excluded.show_browser,proxy_type=excluded.proxy_type,proxy_host=excluded.proxy_host,proxy_port=excluded.proxy_port,proxy_user=excluded.proxy_user,proxy_pass=excluded.proxy_pass,updated_at=CURRENT_TIMESTAMP WHERE xy_accounts.owner_id=excluded.owner_id`, u.ID, accountID, cookie, status, nullString(find(row, "remark", "备注")), nullString(find(row, "username", "用户名")), nullString(find(row, "login_password", "password", "密码")), showBrowser, valueOr(find(row, "proxy_type"), "none"), nullString(find(row, "proxy_host")), func() any {
			if port > 0 {
				return port
			}
			return nil
		}(), nullString(find(row, "proxy_user")), nullString(find(row, "proxy_pass")))
		if execErr != nil {
			failed++
			errorsList = append(errorsList, fmt.Sprintf("第%d行导入失败: %s", rowIndex+2, execErr.Error()))
			continue
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			failed++
			errorsList = append(errorsList, fmt.Sprintf("第%d行账号已属于其他用户", rowIndex+2))
			continue
		}
		if exists == 0 {
			inserted++
		} else {
			updated++
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "账号导入完成", map[string]any{"inserted": inserted, "updated": updated, "started": 0, "failed": failed, "errors": errorsList})
}
