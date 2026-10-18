package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

func (s *Server) personalAddressExport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT json_extract(data,'$.address') FROM resource_records WHERE owner_id=? AND kind='personal_address' ORDER BY id`, u.ID)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	book := excelize.NewFile()
	defer book.Close()
	sheet := "个人地址库"
	book.SetSheetName("Sheet1", sheet)
	_ = book.SetCellValue(sheet, "A1", "地址")
	rowNumber := 2
	for rows.Next() {
		var address string
		if rows.Scan(&address) == nil {
			_ = book.SetCellValue(sheet, fmt.Sprintf("A%d", rowNumber), address)
			rowNumber++
		}
	}
	_ = book.SetColWidth(sheet, "A", "A", 62)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="personal_addresses_%d.xlsx"`, time.Now().Unix()))
	if err = book.Write(w); err != nil {
		s.log.Error("write personal address export", "error", err)
	}
}

func (s *Server) personalAddressImport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		write(w, 200, response{Success: false, Message: "请上传Excel文件(.xlsx)"})
		return
	}
	defer file.Close()
	book, err := excelize.OpenReader(file)
	if err != nil {
		write(w, 200, response{Success: false, Message: "Excel文件读取失败: " + err.Error()})
		return
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		write(w, 200, response{Success: false, Message: "Excel文件为空"})
		return
	}
	rows, err := book.GetRows(sheets[0])
	if err != nil || len(rows) == 0 {
		write(w, 200, response{Success: false, Message: "Excel文件为空"})
		return
	}
	addressColumn := -1
	for index, header := range rows[0] {
		if strings.TrimSpace(header) == "地址" {
			addressColumn = index
			break
		}
	}
	if addressColumn < 0 {
		write(w, 200, response{Success: false, Message: "Excel缺少必要的列: 地址"})
		return
	}
	addresses := []string{}
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		if addressColumn >= len(row) {
			continue
		}
		address := strings.TrimSpace(row[addressColumn])
		if address != "" && !seen[address] {
			seen[address] = true
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		write(w, 200, response{Success: false, Message: "未读取到有效的地址数据"})
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	created, updated := 0, 0
	for _, address := range addresses {
		var id int64
		err = tx.QueryRowContext(r.Context(), `SELECT id FROM resource_records WHERE owner_id=? AND kind='personal_address' AND json_extract(data,'$.address')=? LIMIT 1`, u.ID, address).Scan(&id)
		data, _ := json.Marshal(map[string]any{"address": address})
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE resource_records SET data=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(data), id)
			updated++
		} else {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,enabled,data) VALUES(?,'personal_address',1,?)`, u.ID, string(data))
			created++
		}
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, fmt.Sprintf("导入完成，新增 %d 条，更新 %d 条", created, updated), map[string]any{"created": created, "updated": updated})
}
