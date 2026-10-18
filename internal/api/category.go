package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

type categoryRequest struct {
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	AccountID       string           `json:"account_id"`
	SecretKey       string           `json:"secret_key"`
	CurrentCardList []map[string]any `json:"current_card_list"`
	CardList        []map[string]any `json:"card_list"`
	SelectedList    []map[string]any `json:"selected_list"`
	Category        map[string]any   `json:"category"`
	CatID           string           `json:"cat_id"`
	CatName         string           `json:"cat_name"`
	ChannelCatID    string           `json:"channel_cat_id"`
}

func (s *Server) productCategoryRecommend(w http.ResponseWriter, r *http.Request) {
	var p categoryRequest
	if decode(r, &p) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	p.Title, p.Description = strings.TrimSpace(p.Title), strings.TrimSpace(p.Description)
	if p.Title == "" && p.Description == "" {
		write(w, 200, response{Success: false, Message: "请先填写商品标题或商品描述"})
		return
	}
	u := current(r)
	accounts := s.categoryAccounts(r, u.ID, p.AccountID, strings.EqualFold(u.Role, "ADMIN"))
	if len(accounts) == 0 {
		msg := "当前用户的闲鱼账号均缺少Cookie，请先添加账号或重新登录账号"
		if strings.TrimSpace(p.AccountID) != "" {
			msg = "指定的闲鱼账号不存在或无权使用"
		}
		write(w, 200, response{Success: false, Message: msg})
		return
	}
	if p.Title == "" {
		runes := []rune(p.Description)
		if len(runes) > 200 {
			runes = runes[:200]
		}
		p.Title = string(runes)
	}
	if p.Description == "" {
		p.Description = p.Title
	}
	last := "分类推荐失败，请稍后重试"
	for _, account := range accounts {
		data, res := s.requestCategory(r, account, p)
		if res.Success {
			ok(w, "分类推荐成功", data)
			return
		}
		if res.Error != "" {
			last = res.Error
		}
	}
	if p.AccountID == "" {
		last = "当前用户的闲鱼账号均不可用：" + last
	}
	write(w, 200, response{Success: false, Message: last})
}

func (s *Server) externalCategoryRecommend(w http.ResponseWriter, r *http.Request) {
	var p categoryRequest
	if decode(r, &p) != nil {
		externalWrite(w, false, 40004, "商品描述不能为空", nil)
		return
	}
	if !s.validateExternalCategory(w, r, &p) {
		return
	}
	account, ok := s.externalCategoryAccount(r, p.SecretKey, p.AccountID)
	if !ok {
		externalWrite(w, false, 40002, "闲鱼账号不存在或不属于该秘钥用户", nil)
		return
	}
	p.Title = truncateRunes(p.Description, 200)
	data, res := s.requestCategory(r, account, p)
	if !res.Success {
		externalWrite(w, false, 40005, firstNonemptyAPI(res.Error, "分类推荐失败，请稍后重试"), nil)
		return
	}
	externalWrite(w, true, 200, "分类推荐成功", data)
}

func (s *Server) externalCategoryProperties(w http.ResponseWriter, r *http.Request) {
	var p categoryRequest
	if decode(r, &p) != nil {
		externalWrite(w, false, 40004, "商品描述不能为空", nil)
		return
	}
	if !s.validateExternalCategory(w, r, &p) {
		return
	}
	if p.Category == nil {
		externalWrite(w, false, 40006, "category不能为空，请传入分类推荐接口返回的完整候选分类", nil)
		return
	}
	if p.CardList == nil {
		externalWrite(w, false, 40006, "card_list不能为空，请传入分类推荐接口返回的完整card_list", nil)
		return
	}
	if len(p.CardList) > 100 {
		externalWrite(w, false, 40006, "card_list数量不能超过100条", nil)
		return
	}
	selection, err := buildCategorySelection(p.CardList, p.Category)
	if err != "" {
		externalWrite(w, false, 40006, err, nil)
		return
	}
	p.CurrentCardList = selection.cards
	p.SelectedList = []map[string]any{selection.selected}
	p.CatID = selection.catID
	p.CatName = selection.catName
	p.ChannelCatID = selection.channelID
	p.Title = truncateRunes(p.Description, 200)
	account, found := s.externalCategoryAccount(r, p.SecretKey, p.AccountID)
	if !found {
		externalWrite(w, false, 40002, "闲鱼账号不存在或不属于该秘钥用户", nil)
		return
	}
	data, res := s.requestCategory(r, account, p)
	if !res.Success {
		externalWrite(w, false, 40005, firstNonemptyAPI(res.Error, "分类推荐失败，请稍后重试"), nil)
		return
	}
	externalWrite(w, true, 200, "分类属性获取成功", data)
}

func (s *Server) validateExternalCategory(w http.ResponseWriter, r *http.Request, p *categoryRequest) bool {
	p.SecretKey = strings.TrimSpace(p.SecretKey)
	p.AccountID = strings.TrimSpace(p.AccountID)
	p.Description = strings.TrimSpace(p.Description)
	if _, code, msg := s.externalOwner(r, p.SecretKey); code != "" {
		n := 40001
		fmt.Sscan(code, &n)
		externalWrite(w, false, n, msg, nil)
		return false
	}
	if p.AccountID == "" {
		externalWrite(w, false, 40002, "闲鱼账号ID不能为空", nil)
		return false
	}
	if p.Description == "" {
		externalWrite(w, false, 40004, "商品描述不能为空", nil)
		return false
	}
	if len([]rune(p.Description)) > 1500 {
		externalWrite(w, false, 40004, "商品描述长度不能超过1500位", nil)
		return false
	}
	return true
}

func (s *Server) externalCategoryAccount(r *http.Request, key, id string) (platformAccount, bool) {
	owner, code, _ := s.externalOwner(r, key)
	if code != "" {
		return platformAccount{}, false
	}
	var a platformAccount
	err := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, owner, id).Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass)
	return a, err == nil && strings.TrimSpace(a.Cookie) != ""
}

func (s *Server) categoryAccounts(r *http.Request, owner int64, requested string, admin bool) []platformAccount {
	query := `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE cookie<>''`
	args := []any{}
	if requested != "" {
		query += ` AND account_id=?`
		args = append(args, requested)
		if !admin {
			query += ` AND owner_id=?`
			args = append(args, owner)
		}
	} else {
		query += ` AND owner_id=? ORDER BY CASE WHEN status='active' THEN 0 ELSE 1 END,id`
		args = append(args, owner)
	}
	rows, e := s.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		return nil
	}
	defer rows.Close()
	out := []platformAccount{}
	for rows.Next() {
		var a platformAccount
		if rows.Scan(&a.ID, &a.OwnerID, &a.AccountID, &a.Cookie, &a.ProxyType, &a.ProxyHost, &a.ProxyPort, &a.ProxyUser, &a.ProxyPass) == nil {
			out = append(out, a)
		}
	}
	return out
}

func (s *Server) requestCategory(r *http.Request, a platformAccount, p categoryRequest) (map[string]any, xianyu.MTOPResult) {
	payload := map[string]any{"title": p.Title, "description": p.Description, "lockCpv": false, "multiSKU": false, "publishScene": "pcBackendPublish", "scene": "shopPcPublish", "uniqueCode": fmt.Sprintf("%d%s", time.Now().UnixMilli(), lastChars(a.AccountID, 4))}
	if len(p.CurrentCardList) > 0 {
		payload["currentCardList"] = p.CurrentCardList
	}
	if len(p.SelectedList) > 0 {
		payload["selectedList"] = p.SelectedList
	}
	if p.CatID != "" {
		payload["catId"] = p.CatID
	}
	if p.CatName != "" {
		payload["catName"] = p.CatName
	}
	if p.ChannelCatID != "" {
		payload["channelCatId"] = p.ChannelCatID
	}
	res := xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: "mtop.taobao.idle.kgraph.pc.property.recommend", Version: "2.0", Cookie: a.Cookie, Proxy: accountProxy(a), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/", Data: payload, ExtraParams: map[string]string{"spm_cnt": "a21107h.42829679.0.0", "spm_pre": "a21107h.42829791.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	if res.Cookie != "" && res.Cookie != a.Cookie {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_accounts SET cookie=?,last_refresh_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Cookie, a.ID)
	}
	if !res.Success {
		return nil, res
	}
	data := parseCategoryResponse(res.Response, a.AccountID)
	if len(data["candidates"].([]map[string]any)) == 0 {
		res.Success = false
		res.Error = "接口未返回可用的商品分类"
		return nil, res
	}
	return data, res
}

func parseCategoryResponse(body map[string]any, account string) map[string]any {
	data, _ := body["data"].(map[string]any)
	raw := data["cardList"]
	cards := []map[string]any{}
	switch x := raw.(type) {
	case []any:
		for _, v := range x {
			if m, ok := v.(map[string]any); ok {
				if cd, ok := m["cardData"].(map[string]any); ok {
					cards = append(cards, cd)
				}
			}
		}
	case string:
		var list []map[string]any
		_ = json.Unmarshal([]byte(x), &list)
		for _, m := range list {
			if cd, ok := m["cardData"].(map[string]any); ok {
				cards = append(cards, cd)
			}
		}
	}
	candidates := []map[string]any{}
	properties := []map[string]any{}
	for _, card := range cards {
		pid := text(card["propertyId"])
		values, _ := card["valuesList"].([]any)
		if pid == "-10000" {
			for _, v := range values {
				m, ok := v.(map[string]any)
				if !ok {
					continue
				}
				tr, _ := m["transportData"].(map[string]any)
				channel := firstText(m["channelCatId"], tr["channelCateId"], m["channelCategoryId"])
				cat := firstText(m["catId"], tr["catId"], m["categoryId"])
				name := firstText(m["catName"], m["text"], tr["valueName"], tr["text"], m["channelCatName"])
				if channel == "" && cat == "" {
					continue
				}
				path := []map[string]string{}
				if name != "" {
					path = append(path, map[string]string{"id": firstNonemptyAPI(channel, cat), "name": name})
				}
				candidates = append(candidates, map[string]any{"cat_id": nilIfEmpty(cat), "cat_name": nilIfEmpty(name), "channel_cat_id": nilIfEmpty(channel), "channel_cat_name": nilIfEmpty(firstText(m["channelCatName"], tr["channelCateName"], name)), "leaf_id": nilIfEmpty(firstText(m["leafId"], tr["leafId"])), "tb_cat_id": nilIfEmpty(firstText(m["tbCatId"], tr["tbCatId"])), "path": path, "score": m["score"], "is_selected": truthy(m["isClicked"]) || truthy(m["isUserClick"])})
			}
		} else if pid != "" && text(card["propertyName"]) != "" {
			opts := []map[string]any{}
			for _, v := range values {
				m, ok := v.(map[string]any)
				if !ok {
					continue
				}
				tr, _ := m["transportData"].(map[string]any)
				name := firstText(m["valueName"], m["text"], tr["valueName"], tr["text"])
				if name != "" {
					opts = append(opts, map[string]any{"property_id": pid, "property_name": text(card["propertyName"]), "value_id": nilIfEmpty(firstText(m["valueId"], tr["valueId"])), "value_name": name, "properties": nilIfEmpty(firstText(m["properties"], tr["properties"])), "is_selected": truthy(m["isClicked"]) || truthy(m["isUserClick"])})
				}
			}
			properties = append(properties, map[string]any{"property_id": pid, "property_name": text(card["propertyName"]), "input_word": nilIfEmpty(text(card["inputWord"])), "is_multiple": truthy(card["isMultiple"]), "is_decisive_property": truthy(card["isDecisiveProperty"]), "options": opts})
		}
	}
	return map[string]any{"candidates": candidates, "properties": properties, "card_list": cards, "account_id": account}
}

type categorySelection struct {
	cards                     []map[string]any
	selected                  map[string]any
	catID, catName, channelID string
}

func buildCategorySelection(cards []map[string]any, cat map[string]any) (categorySelection, string) {
	if len(cards) == 0 {
		return categorySelection{}, "card_list不能为空，请传入分类推荐接口返回的完整card_list"
	}
	wantName := firstText(cat["cat_name"], cat["channel_cat_name"])
	wantChannel := text(cat["channel_cat_id"])
	wantTB := text(cat["tb_cat_id"])
	wantCat := text(cat["cat_id"])
	if wantName == "" && wantChannel == "" && wantTB == "" && wantCat == "" {
		return categorySelection{}, "所选分类缺少标识，cat_name、cat_id、channel_cat_id、tb_cat_id 至少需要一个"
	}
	foundCard := false
	var best map[string]any
	bestScore := 0
	for _, card := range cards {
		if text(card["propertyId"]) != "-10000" {
			continue
		}
		foundCard = true
		values, _ := card["valuesList"].([]any)
		for _, v := range values {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			tr, _ := m["transportData"].(map[string]any)
			score := 0
			conflict := false
			for _, pair := range [][3]string{{wantChannel, firstText(m["channelCatId"], tr["channelCateId"]), "8"}, {wantTB, firstText(m["tbCatId"], tr["tbCatId"]), "4"}, {wantCat, firstText(m["catId"], tr["catId"]), "2"}} {
				if pair[0] != "" && pair[1] != "" {
					if pair[0] != pair[1] {
						conflict = true
						break
					}
					var n int
					fmt.Sscan(pair[2], &n)
					score += n
				}
			}
			name := firstText(m["catName"], m["text"], tr["valueName"], tr["text"], m["channelCatName"])
			if !conflict && wantName != "" && name == wantName {
				score++
			}
			if !conflict && score > bestScore {
				bestScore = score
				best = m
			}
		}
	}
	if !foundCard {
		return categorySelection{}, "card_list中缺少分类卡，请重新调用分类推荐接口"
	}
	if best == nil {
		return categorySelection{}, "所选分类不在card_list中，请使用同一次分类推荐返回的数据"
	}
	var selected map[string]any
	for _, card := range cards {
		if text(card["propertyId"]) != "-10000" {
			continue
		}
		values, _ := card["valuesList"].([]any)
		for _, v := range values {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			yes := fmt.Sprintf("%p", m) == fmt.Sprintf("%p", best)
			m["isClicked"] = map[bool]string{true: "1", false: "0"}[yes]
			m["isUserClick"] = m["isClicked"]
			if yes {
				tr, _ := m["transportData"].(map[string]any)
				selected = tr
				if selected == nil {
					selected = map[string]any{}
				}
				selected["isUserClick"] = "1"
				selected["propertyId"] = "-10000"
				selected["propertyName"] = "分类"
			}
		}
	}
	tr, _ := best["transportData"].(map[string]any)
	return categorySelection{cards: cards, selected: selected, catID: firstText(best["catId"], tr["catId"], wantCat), catName: firstText(best["catName"], best["text"], tr["valueName"], wantName), channelID: firstText(best["channelCatId"], tr["channelCateId"], wantChannel)}, ""
}

func text(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
func firstText(v ...any) string {
	for _, x := range v {
		if s := text(x); s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}
func truthy(v any) bool { s := strings.ToLower(text(v)); return s == "1" || s == "true" || s == "yes" }
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
func lastChars(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}
