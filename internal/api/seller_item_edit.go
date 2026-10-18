package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

const sellerOrigin = "https://seller.goofish.com"

// sellerItemDetail calls the same seller-console editdetail MTOP API as the
// original service and maps its wire representation to the existing UI form.
func (s *Server) sellerItemDetail(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.loadPlatformAccounts(r, chi.URLParam(r, "accountID"))
	if err != nil || len(accounts) == 0 {
		fail(w, 404, "账号不存在或未启用")
		return
	}
	a := accounts[0]
	result := callSellerItemAPI(r, a, "mtop.idle.pc.backend.idleitem.editdetail", map[string]any{"itemId": chi.URLParam(r, "itemID")})
	persistPlatformCookie(s, r, a, result.Cookie)
	if !result.Success {
		write(w, 200, response{Success: false, Message: "获取闲鱼商品详情失败：" + result.Error})
		return
	}
	detail := nestedMap(result.Response, "data")
	if strings.TrimSpace(fmt.Sprint(detail["itemId"])) == "" {
		write(w, 200, response{Success: false, Message: "闲鱼未返回商品详情，无法编辑"})
		return
	}
	ok(w, "获取商品详情成功", map[string]any{"form": mapSellerItemForm(detail)})
}

func (s *Server) sellerItemEdit(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.loadPlatformAccounts(r, chi.URLParam(r, "accountID"))
	if err != nil || len(accounts) == 0 {
		fail(w, 404, "账号不存在或未启用")
		return
	}
	var form map[string]any
	if decode(r, &form) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if strings.TrimSpace(fmt.Sprint(form["title"])) == "" || strings.TrimSpace(fmt.Sprint(form["description"])) == "" {
		fail(w, 400, "标题和描述不能为空")
		return
	}
	a := accounts[0]
	detailResult := callSellerItemAPI(r, a, "mtop.idle.pc.backend.idleitem.editdetail", map[string]any{"itemId": chi.URLParam(r, "itemID")})
	persistPlatformCookie(s, r, a, detailResult.Cookie)
	if !detailResult.Success {
		write(w, 200, response{Success: false, Message: "编辑前获取闲鱼商品快照失败：" + detailResult.Error})
		return
	}
	snapshot := nestedMap(detailResult.Response, "data")
	payload, refreshedCookie, buildErr := buildPublishPayload(r.Context(), form, a, snapshot)
	if buildErr != nil {
		write(w, 200, response{Success: false, Message: buildErr.Error()})
		return
	}
	if refreshedCookie != "" {
		a.Cookie = refreshedCookie
	}
	payload["itemId"] = chi.URLParam(r, "itemID")
	b, _ := json.Marshal(payload)
	result := callSellerItemAPI(r, a, "mtop.idle.pc.backend.idleitem.edit", map[string]any{"inputJson": string(b)})
	persistPlatformCookie(s, r, a, result.Cookie)
	if !result.Success {
		write(w, 200, response{Success: false, Message: "闲鱼接口编辑失败：" + result.Error})
		return
	}
	if message := sellerBusinessFailure(result.Response); message != "" {
		write(w, 200, response{Success: false, Message: "闲鱼接口编辑失败：" + message})
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE xy_catalog_items SET title=COALESCE(?,title),description=COALESCE(?,description),price=COALESCE(?,price),data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, form["title"], form["description"], form["price"], string(b), current(r).ID, a.AccountID, chi.URLParam(r, "itemID"))
	ok(w, "商品编辑成功", nil)
}

func callSellerItemAPI(r *http.Request, a platformAccount, api string, data map[string]any) xianyu.MTOPResult {
	return xianyu.MTOPCall(r.Context(), xianyu.MTOPOptions{API: api, Version: "1.0", Cookie: a.Cookie, Proxy: accountProxy(a), Origin: sellerOrigin, Referer: sellerOrigin + "/?site=COMMONPRO", Data: data, ExtraParams: map[string]string{"idle_site_biz_code": "COMMONPRO", "spm_cnt": "a21107h.42826273.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
}

func sellerBusinessFailure(res map[string]any) string {
	d := nestedMap(res, "data")
	if len(d) == 0 {
		return ""
	}
	msg := firstMapString(d, "msg", "message", "errorMsg", "errMsg", "resultMsg", "tips")
	if d["success"] == false || d["isSuccess"] == false {
		if msg != "" {
			return msg
		}
		return "闲鱼返回编辑失败"
	}
	code := firstMapString(d, "code", "errorCode")
	if code != "" && code != "0" && code != "200" && !strings.EqualFold(code, "success") {
		if msg != "" {
			return msg
		}
		return "闲鱼返回编辑失败（code=" + code + "）"
	}
	return ""
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(fmt.Sprint(m[k])); v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}
func mapInt(v any) int { n, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v))); return n }
func centYuan(v any) any {
	n := mapInt(v)
	if n <= 0 {
		return nil
	}
	return float64(n) / 100
}
func mapBool(v any) bool { b, _ := strconv.ParseBool(strings.TrimSpace(fmt.Sprint(v))); return b }

func mapSellerItemForm(d map[string]any) map[string]any {
	text, price, cat, addr, fee := childMap(d, "itemTextDTO"), childMap(d, "itemPriceDTO"), childMap(d, "itemCatDTO"), childMap(d, "itemAddrDTO"), childMap(d, "itemPostFeeDTO")
	images, videos := []string{}, []map[string]any{}
	for _, x := range mapSlice(d["imageInfoDOList"]) {
		if mapInt(x["type"]) == 0 {
			if u := firstMapString(x, "url"); u != "" {
				images = append(images, u)
			}
		} else if u, id := firstMapString(x, "videoUrl"), firstMapString(x, "mediaCloudFileId"); u != "" && id != "" {
			videos = append(videos, map[string]any{"url": u, "path": "", "name": "平台视频", "file_id": id, "width": mapInt(x["widthSize"]), "height": mapInt(x["heightSize"])})
		}
	}
	specs, rows := mapSellerSKUs(d)
	shipping := "template"
	if mapBool(fee["canFreeShipping"]) {
		shipping = "free"
	} else if !mapBool(fee["supportFreight"]) {
		shipping = "none"
	} else if firstMapString(fee, "templateId") == "0" {
		shipping = "fixed"
	} else if firstMapString(fee, "templateId") == "-100" {
		shipping = "distance"
	}
	quantity := mapInt(d["quantity"])
	if len(rows) > 0 {
		quantity = 1
	}
	delivery := "express"
	if shipping == "none" {
		delivery = "pickup"
	}
	return map[string]any{"item_id": firstMapString(d, "itemId"), "title": firstMapString(text, "title"), "description": firstMapString(text, "desc"), "price": centYuan(price["priceInCent"]), "original_price": centYuan(price["origPriceInCent"]), "category": firstMapString(cat, "catName"), "quantity": quantity, "images": images, "videos": videos, "specifications": specs, "sku_rows": rows, "platform_category_id": firstMapString(cat, "catId"), "platform_category_name": firstMapString(cat, "catName"), "platform_channel_category_id": firstMapString(cat, "channelCatId"), "platform_channel_category_name": firstMapString(cat, "catName"), "platform_leaf_id": firstMapString(cat, "leafId"), "platform_tb_category_id": firstMapString(cat, "tbCatId"), "platform_category_path": []map[string]any{{"id": firstMapString(cat, "catId"), "name": firstMapString(cat, "catName")}}, "platform_attributes": []any{}, "category_source": "manual", "address": firstMapString(addr, "poiName"), "address_expected_text": firstMapString(addr, "poiName"), "shipping_method": shipping, "postage": float64(mapInt(fee["postPriceInCent"])) / 100, "support_pickup": mapBool(fee["onlyTakeSelf"]), "delivery_method": delivery}
}

func childMap(m map[string]any, k string) map[string]any {
	if x, ok := m[k].(map[string]any); ok {
		return x
	}
	return map[string]any{}
}
func mapSlice(v any) []map[string]any {
	out := []map[string]any{}
	if xs, ok := v.([]any); ok {
		for _, v := range xs {
			if m, ok := v.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}
func mapSellerSKUs(d map[string]any) ([]map[string]any, []map[string]any) {
	values := map[string][]string{}
	rows := []map[string]any{}
	for _, sku := range mapSlice(d["itemSkuList"]) {
		sm := map[string]string{}
		for _, p := range mapSlice(sku["propertyList"]) {
			n, v := firstMapString(p, "propertyText"), firstMapString(p, "valueText", "actualValueText")
			if n != "" && v != "" {
				sm[n] = v
				found := false
				for _, old := range values[n] {
					if old == v {
						found = true
					}
				}
				if !found {
					values[n] = append(values[n], v)
				}
			}
		}
		if len(sm) > 0 {
			rows = append(rows, map[string]any{"specs": sm, "price": centYuan(sku["priceInCent"]), "stock": mapInt(sku["quantity"])})
		}
	}
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	specs := []map[string]any{}
	for _, n := range names {
		vs := []map[string]any{}
		for _, v := range values[n] {
			vs = append(vs, map[string]any{"name": v, "image": nil})
		}
		specs = append(specs, map[string]any{"name": n, "support_image": false, "values": vs})
	}
	return specs, rows
}
