package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"
)

var keywordHeaders = []string{"关键词", "商品ID", "关键词内容", "回复类型", "定位名称", "经度", "纬度", "位置标题", "位置副标题"}

func (s *Server) keywordExportExcel(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT keyword,item_id,reply,type,extra FROM xy_keyword_rules WHERE owner_id=? AND account_id=? AND type IN ('text','external_contact') ORDER BY id`, ownerID, accountID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	book := excelize.NewFile()
	defer book.Close()
	sheet := "关键词数据"
	book.SetSheetName("Sheet1", sheet)
	for i, header := range keywordHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = book.SetCellValue(sheet, cell, header)
	}
	rowNumber := 2
	for rows.Next() {
		var keyword, itemID, reply, replyType, raw string
		if rows.Scan(&keyword, &itemID, &reply, &replyType, &raw) != nil {
			continue
		}
		extra := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &extra)
		values := []any{keyword, itemID, reply, replyType, extra["location_name"], extra["location_longitude"], extra["location_latitude"], extra["location_title"], extra["location_subtitle"]}
		for i, value := range values {
			cell, _ := excelize.CoordinatesToCellName(i+1, rowNumber)
			_ = book.SetCellValue(sheet, cell, value)
		}
		rowNumber++
	}
	_ = book.SetColWidth(sheet, "A", "I", 20)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="keywords_%s_%d.xlsx"`, accountID, time.Now().Unix()))
	if err = book.Write(w); err != nil {
		s.log.Error("write keyword export", "error", err)
	}
}

func (s *Server) keywordImportExcel(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	accountID := chi.URLParam(r, "accountID")
	ownerID, accessErr := s.accountOwnerForAccess(r.Context(), u, accountID)
	if accessErr != nil {
		fail(w, 404, "账号不存在")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请上传Excel文件(.xlsx)")
		return
	}
	defer file.Close()
	book, err := excelize.OpenReader(file)
	if err != nil {
		fail(w, 400, "Excel文件读取失败: "+err.Error())
		return
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		fail(w, 400, "Excel文件为空")
		return
	}
	rows, err := book.GetRows(sheets[0])
	if err != nil || len(rows) < 2 {
		fail(w, 400, "Excel文件中没有有效的关键词数据")
		return
	}
	headers := map[string]int{}
	for i, name := range rows[0] {
		headers[strings.TrimSpace(name)] = i
	}
	for _, required := range []string{"关键词", "商品ID", "关键词内容"} {
		if _, exists := headers[required]; !exists {
			fail(w, 400, "Excel文件缺少必要的列: "+required)
			return
		}
	}
	value := func(row []string, name string) string {
		index, exists := headers[name]
		if !exists || index >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[index])
	}
	records := make([]map[string]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		keyword := value(row, "关键词")
		if keyword == "" {
			continue
		}
		itemID := strings.TrimSuffix(value(row, "商品ID"), ".0")
		replyType := value(row, "回复类型")
		if replyType == "" {
			replyType = "text"
		}
		if replyType != "text" && replyType != "external_contact" {
			replyType = "text"
		}
		records = append(records, map[string]string{"keyword": keyword, "item_id": itemID, "reply": value(row, "关键词内容"), "type": replyType, "location_name": value(row, "定位名称"), "location_longitude": value(row, "经度"), "location_latitude": value(row, "纬度"), "location_title": value(row, "位置标题"), "location_subtitle": value(row, "位置副标题")})
	}
	if len(records) == 0 {
		fail(w, 400, "Excel文件中没有有效的关键词数据")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM xy_keyword_rules WHERE owner_id=? AND account_id=? AND type IN ('text','external_contact')`, ownerID, accountID); err != nil {
		fail(w, 500, err.Error())
		return
	}
	for _, record := range records {
		extra, _ := json.Marshal(map[string]string{"location_name": record["location_name"], "location_longitude": record["location_longitude"], "location_latitude": record["location_latitude"], "location_title": record["location_title"], "location_subtitle": record["location_subtitle"]})
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO xy_keyword_rules(owner_id,account_id,keyword,reply,item_id,type,extra) VALUES(?,?,?,?,?,?,?)`, ownerID, accountID, record["keyword"], record["reply"], record["item_id"], record["type"], string(extra)); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "导入成功", map[string]any{"added": len(records), "updated": 0})
}
