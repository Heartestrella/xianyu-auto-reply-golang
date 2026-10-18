package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) publishAddressAccountOptions(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	query := `SELECT account_id,COALESCE(display_name,''),COALESCE(remark,''),owner_id FROM xy_accounts`
	args := []any{}
	if u.Role != "ADMIN" {
		query += ` WHERE owner_id=?`
		args = append(args, u.ID)
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var accountID, name, remark string
		var ownerID int64
		if rows.Scan(&accountID, &name, &remark, &ownerID) != nil {
			continue
		}
		label := name
		if label == "" {
			label = remark
		}
		if label == "" {
			label = accountID
		}
		items = append(items, map[string]any{"account_id": accountID, "label": label, "owner_id": ownerID})
	}
	ok(w, "查询成功", map[string]any{"list": items})
}

func (s *Server) publishAddressInputTips(w http.ResponseWriter, r *http.Request) {
	keywords := strings.TrimSpace(r.URL.Query().Get("keywords"))
	city := strings.TrimSpace(r.URL.Query().Get("city"))
	if keywords == "" || len([]rune(keywords)) > 100 {
		write(w, 200, response{Success: false, Message: "请输入所在地关键词"})
		return
	}
	if city == "" {
		city = "全国"
	}
	params := url.Values{"s": {"rsv3"}, "key": {"c9b68d4ce9a2a97f22a4a439404488ca"}, "platform": {"JS"}, "logversion": {"2.0"}, "sdkversion": {"2.0"}, "appname": {"https://seller.goofish.com"}, "keywords": {keywords}, "city": {city}, "citylimit": {"false"}, "datatype": {"all"}}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://restapi.amap.com/v3/assistant/inputtips?"+params.Encode(), nil)
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Origin", "https://seller.goofish.com")
	req.Header.Set("Referer", "https://seller.goofish.com/")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		write(w, 200, response{Success: false, Message: "所在地搜索接口请求失败，请稍后重试"})
		return
	}
	defer resp.Body.Close()
	var payload map[string]any
	if json.NewDecoder(resp.Body).Decode(&payload) != nil || fmt.Sprint(payload["status"]) != "1" {
		write(w, 200, response{Success: false, Message: "所在地搜索失败：" + firstNonemptyAPI(fmt.Sprint(payload["info"]), "高德接口未返回成功状态")})
		return
	}
	result := []map[string]any{}
	tips, _ := payload["tips"].([]any)
	for _, entry := range tips {
		tip, _ := entry.(map[string]any)
		name := cleanText(tip["name"])
		if name == "" {
			continue
		}
		district, address := cleanText(tip["district"]), cleanText(tip["address"])
		detail := district + address
		expected := name
		if detail != "" {
			expected += " / " + detail
		}
		result = append(result, map[string]any{"id": cleanText(tip["id"]), "name": name, "district": district, "adcode": cleanText(tip["adcode"]), "location": cleanText(tip["location"]), "address": address, "typecode": cleanText(tip["typecode"]), "city": cleanText(tip["city"]), "search_keyword": name, "expected_text": expected})
	}
	ok(w, "所在地搜索成功", map[string]any{"tips": result, "count": len(result), "keywords": keywords})
}

func cleanText(value any) string {
	if value == nil {
		return ""
	}
	if _, isSlice := value.([]any); isSlice {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
