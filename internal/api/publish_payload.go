package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zhinianboke/xianyu-auto-go/internal/proxy"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const publishImageUploadURL = "https://stream-upload.goofish.com/api/upload.api?floderId=0&appkey=fleamarket&_input_charset=utf-8"

// buildPublishPayload converts the public material/form contract into the
// exact idleitem.publish/idleitem.edit DTO shape used by the seller console.
// It deliberately does not accept the UI form itself as an MTOP payload.
func buildPublishPayload(ctx context.Context, item map[string]any, account platformAccount, snapshot map[string]any) (map[string]any, string, error) {
	title, description := textValue(item["title"]), textValue(item["description"])
	if title == "" || description == "" {
		return nil, account.Cookie, errors.New("商品标题和商品描述不能为空")
	}
	properties, skus, propertyImages, multi, err := buildPublishSKUs(item)
	if err != nil {
		return nil, account.Cookie, err
	}
	images := publishStringSlice(item["images"])
	if len(images) == 0 {
		return nil, account.Cookie, errors.New("请至少上传一张商品图片")
	}
	if len(images) > 9 {
		images = images[:9]
	}
	imageItems := make([]map[string]any, 0, len(images))
	for i, source := range images {
		uploaded, uploadErr := uploadPublishImage(ctx, source, account)
		if uploadErr != nil {
			return nil, account.Cookie, fmt.Errorf("第 %d 张图片上传失败：%w", i+1, uploadErr)
		}
		uploaded["major"] = i == 0
		imageItems = append(imageItems, uploaded)
	}

	propertyImageList := []map[string]any{}
	for _, specImage := range propertyImages {
		uploaded, uploadErr := uploadPublishImage(ctx, specImage.Source, account)
		if uploadErr != nil {
			return nil, account.Cookie, fmt.Errorf("规格 %s=%s 图片上传失败：%w", specImage.Property, specImage.Value, uploadErr)
		}
		delete(uploaded, "major")
		propertyImageList = append(propertyImageList, map[string]any{"property": map[string]any{"propertyText": specImage.Property, "valueText": specImage.Value}, "url": uploaded["url"]})
		for _, property := range properties {
			if property["propertyName"] != specImage.Property {
				continue
			}
			for _, value := range mapSlice(property["propertyValues"]) {
				if value["propertyValue"] == specImage.Value {
					value["propertyValueImg"] = uploaded
				}
			}
		}
	}

	address, err := resolvePublishAddress(ctx, item, snapshot)
	if err != nil {
		return nil, account.Cookie, err
	}
	postFee, err := buildPublishPostFee(item)
	if err != nil {
		return nil, account.Cookie, err
	}
	videoItems, pendingVideos := buildExistingVideoItems(item, snapshot)
	if len(pendingVideos) > 0 {
		uploadedVideos, refreshedCookie, videoErr := uploadPublishVideos(ctx, pendingVideos, account)
		if videoErr != nil {
			return nil, account.Cookie, videoErr
		}
		account.Cookie = refreshedCookie
		videoItems = append(videoItems, uploadedVideos...)
	}
	for i := range videoItems {
		videoItems[i]["major"] = i == 0
	}
	media := append(videoItems, imageItems...)
	payload := map[string]any{
		"freebies": false, "itemTypeStr": "b", "quantity": clampQuantity(item["quantity"]), "simpleItem": "true",
		"imageInfoDOList":  media,
		"itemTextDTO":      map[string]any{"desc": description, "title": title, "titleDescSeparate": true},
		"itemLabelExtList": buildPublishLabels(item), "userRightsProtocols": publishUserRights(snapshot),
		"itemPostFeeDTO": postFee, "itemAddrDTO": address,
		"defaultPrice": false, "uniqueCode": fmt.Sprintf("%d%s", time.Now().UnixMilli(), lastFour(account.AccountID)),
		"sourceId": "pcBackendPublish", "bizcode": "pcMainPublish", "publishScene": "pcBackendPublish",
		"itemCatDTO": map[string]any{"catId": textValue(item["platform_category_id"]), "catName": textValue(item["platform_category_name"]), "channelCatId": textValue(item["platform_channel_category_id"]), "leafId": textValue(item["platform_leaf_id"]), "tbCatId": textValue(item["platform_tb_category_id"])},
	}
	if multi {
		payload["quantity"] = "1"
		payload["itemProperties"], payload["itemSkuList"], payload["itemPriceDTO"] = properties, skus, map[string]any{}
	} else {
		price, e := cents(item["price"], "售价")
		if e != nil {
			return nil, account.Cookie, e
		}
		original := item["original_price"]
		if textValue(original) == "" {
			original = item["price"]
		}
		orig, e := cents(original, "原价")
		if e != nil {
			return nil, account.Cookie, e
		}
		payload["itemPriceDTO"] = map[string]any{"origPriceInCent": orig, "priceInCent": price}
	}
	if len(propertyImageList) > 0 {
		payload["propertyImageList"] = propertyImageList
	}
	return payload, account.Cookie, nil
}

type publishSpecImage struct{ Property, Value, Source string }

func buildPublishSKUs(item map[string]any) ([]map[string]any, []map[string]any, []publishSpecImage, bool, error) {
	rawSpecs, rawRows := mapSlice(item["specifications"]), mapSlice(item["sku_rows"])
	if len(rawSpecs) == 0 && len(rawRows) == 0 {
		return nil, nil, nil, false, nil
	}
	if len(rawSpecs) == 0 {
		return nil, nil, nil, false, errors.New("商品包含 SKU，但未提供规格定义")
	}
	if len(rawRows) == 0 {
		return nil, nil, nil, false, errors.New("请选择规格组合并填写每个 SKU 的价格和库存")
	}
	properties, names, allowed, images := []map[string]any{}, []string{}, map[string]map[string]bool{}, []publishSpecImage{}
	expected := 1
	imageGroups := map[string]bool{}
	for i, spec := range rawSpecs {
		name := textValue(spec["name"])
		if name == "" {
			return nil, nil, nil, false, fmt.Errorf("第 %d 个规格名称不能为空", i+1)
		}
		if _, exists := allowed[name]; exists {
			return nil, nil, nil, false, fmt.Errorf("规格名称重复：%s", name)
		}
		values, seen := []map[string]any{}, map[string]bool{}
		for _, value := range anySlice(spec["values"]) {
			vm, _ := value.(map[string]any)
			valueName := textValue(value)
			if vm != nil {
				valueName = textValue(vm["name"])
			}
			if valueName == "" {
				continue
			}
			if seen[valueName] {
				return nil, nil, nil, false, fmt.Errorf("规格“%s”存在重复规格值：%s", name, valueName)
			}
			seen[valueName] = true
			values = append(values, map[string]any{"propertyValue": valueName})
			if vm != nil && textValue(vm["image"]) != "" {
				images = append(images, publishSpecImage{name, valueName, textValue(vm["image"])})
				imageGroups[name] = true
			}
		}
		if len(values) == 0 {
			return nil, nil, nil, false, fmt.Errorf("规格“%s”至少需要一个规格值", name)
		}
		expected *= len(values)
		names = append(names, name)
		allowed[name] = seen
		properties = append(properties, map[string]any{"propertyName": name, "supportImage": boolValue(spec["support_image"]) || imageGroups[name], "propertyValues": values})
	}
	if len(imageGroups) > 1 {
		return nil, nil, nil, false, errors.New("闲鱼只允许一组规格带规格图，请只保留一组")
	}
	kept := ""
	for name := range imageGroups {
		kept = name
	}
	for _, p := range properties {
		if boolValue(p["supportImage"]) {
			if kept == "" {
				kept = textValue(p["propertyName"])
			}
			p["supportImage"] = textValue(p["propertyName"]) == kept
		}
	}
	if len(rawRows) != expected {
		return nil, nil, nil, false, fmt.Errorf("规格组合数量不完整：应有 %d 组，实际收到 %d 组", expected, len(rawRows))
	}
	rows, combinations := []map[string]any{}, map[string]bool{}
	for i, row := range rawRows {
		specMap, ok := row["specs"].(map[string]any)
		if !ok {
			return nil, nil, nil, false, fmt.Errorf("第 %d 个 SKU 的规格值格式不正确", i+1)
		}
		list, combo := []map[string]any{}, []string{}
		for _, name := range names {
			value := textValue(specMap[name])
			if value == "" || !allowed[name][value] {
				return nil, nil, nil, false, fmt.Errorf("第 %d 个 SKU 的规格值无效：%s=%s", i+1, name, value)
			}
			combo = append(combo, value)
			list = append(list, map[string]any{"propertyText": name, "valueText": value})
		}
		key := strings.Join(combo, "\x00")
		if combinations[key] {
			return nil, nil, nil, false, fmt.Errorf("第 %d 个 SKU 与已有规格组合重复", i+1)
		}
		combinations[key] = true
		stock, e := integer(row["stock"])
		if e != nil || stock < 0 || stock > 999999 {
			return nil, nil, nil, false, fmt.Errorf("第 %d 个 SKU 库存必须在 0 到 999999 之间", i+1)
		}
		price, e := cents(row["price"], fmt.Sprintf("第 %d 个 SKU 售价", i+1))
		if e != nil {
			return nil, nil, nil, false, e
		}
		rows = append(rows, map[string]any{"priceInCent": price, "quantity": stock, "propertyList": list})
	}
	return properties, rows, images, true, nil
}

func buildPublishLabels(item map[string]any) []map[string]any {
	channelID, categoryName := textValue(item["platform_channel_category_id"]), textValue(item["platform_category_name"])
	channelName := textValue(item["platform_channel_category_name"])
	if channelName == "" {
		channelName = categoryName
	}
	labels := []map[string]any{{"channelCateName": channelName, "valueId": nil, "channelCateId": channelID, "valueName": nil, "tbCatId": textValue(item["platform_tb_category_id"]), "subPropertyId": nil, "labelType": "common", "subValueId": nil, "labelId": nil, "propertyName": "分类", "isUserClick": "1", "isUserCancel": nil, "from": "newPublishChoice", "propertyId": "-10000", "labelFrom": "newPublish", "text": firstNonemptyAPI(categoryName, channelName), "properties": fmt.Sprintf("-10000##分类:%s##%s", channelID, firstNonemptyAPI(categoryName, channelName))}}
	for _, a := range mapSlice(item["platform_attributes"]) {
		name, id, value := textValue(a["property_name"]), textValue(a["property_id"]), firstNonemptyAPI(textValue(a["value_name"]), textValue(a["text"]))
		if name == "" || name == "分类" || id == "" || value == "" {
			continue
		}
		valueID := textValue(a["value_id"])
		properties := textValue(a["properties"])
		if properties == "" {
			properties = fmt.Sprintf("%s##%s:%s##%s", id, name, valueID, value)
		}
		labels = append(labels, map[string]any{"channelCateName": channelName, "valueId": valueID, "channelCateId": channelID, "valueName": value, "tbCatId": textValue(item["platform_tb_category_id"]), "subPropertyId": nil, "labelType": "common", "subValueId": nil, "labelId": nil, "propertyName": name, "isUserClick": "1", "isUserCancel": nil, "from": "newPublishChoice", "propertyId": id, "labelFrom": "newPublish", "text": value, "properties": properties})
	}
	return labels
}

func buildPublishPostFee(item map[string]any) (map[string]any, error) {
	method := textValue(item["shipping_method"])
	if method == "" {
		method = "free"
	}
	pickup := boolValue(item["support_pickup"])
	switch method {
	case "free":
		return map[string]any{"canFreeShipping": true, "supportFreight": true, "onlyTakeSelf": pickup}, nil
	case "distance", "template":
		return map[string]any{"canFreeShipping": false, "supportFreight": true, "onlyTakeSelf": pickup, "templateId": "-100"}, nil
	case "fixed":
		p, e := strconv.ParseFloat(textValue(item["postage"]), 64)
		if e != nil || p < 0 || p > 1000 {
			return nil, errors.New("宝贝运费必须在0到1000元之间")
		}
		return map[string]any{"canFreeShipping": false, "supportFreight": true, "onlyTakeSelf": pickup, "postPriceInCent": strconv.FormatInt(int64(p*100+0.5), 10), "templateId": "0"}, nil
	case "none":
		return map[string]any{"canFreeShipping": false, "supportFreight": false, "onlyTakeSelf": pickup}, nil
	}
	return nil, errors.New("发货方式无效，请重新选择")
}

func resolvePublishAddress(ctx context.Context, item map[string]any, snapshot map[string]any) (map[string]any, error) {
	if snapshot != nil && textValue(item["address"]) == textValue(childMap(snapshot, "itemAddrDTO")["poiName"]) {
		return childMap(snapshot, "itemAddrDTO"), nil
	}
	keyword := textValue(item["address"])
	if keyword == "" {
		return nil, errors.New("请先选择带有效坐标的宝贝所在地")
	}
	q := url.Values{"s": {"rsv3"}, "key": {"c9b68d4ce9a2a97f22a4a439404488ca"}, "platform": {"JS"}, "keywords": {keyword}, "city": {"全国"}, "citylimit": {"false"}, "datatype": {"all"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://restapi.amap.com/v3/assistant/inputtips?"+q.Encode(), nil)
	resp, e := proxy.Client(90 * time.Second).Do(req)
	if e != nil {
		return nil, fmt.Errorf("宝贝所在地解析失败：%w", e)
	}
	defer resp.Body.Close()
	var body map[string]any
	if json.NewDecoder(resp.Body).Decode(&body) != nil || textValue(body["status"]) != "1" {
		return nil, errors.New("宝贝所在地解析失败")
	}
	expected := strings.ReplaceAll(textValue(item["address_expected_text"]), " ", "")
	tips := mapSlice(body["tips"])
	sort.SliceStable(tips, func(i, j int) bool { return addressScore(tips[i], expected) < addressScore(tips[j], expected) })
	for _, tip := range tips {
		id, ad, loc := textValue(tip["id"]), textValue(tip["adcode"]), textValue(tip["location"])
		parts := strings.Split(loc, ",")
		if id == "" || ad == "" || len(parts) != 2 {
			continue
		}
		lng, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		lat, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if e1 == nil && e2 == nil {
			return map[string]any{"divisionId": ad, "gps": fmt.Sprintf("%.6f,%.6f", lat, lng), "poiId": id, "poiName": textValue(tip["name"])}, nil
		}
	}
	return nil, errors.New("宝贝所在地未返回POI编号、行政区划或坐标，请重新选择地址")
}
func addressScore(t map[string]any, expected string) int {
	if expected == "" {
		return 3
	}
	candidate := strings.ReplaceAll(firstNonemptyAPI(textValue(t["expected_text"]), textValue(t["name"])+"/"+textValue(t["address"])), " ", "")
	if candidate == expected {
		return 0
	}
	if strings.HasPrefix(candidate, expected) {
		return 1
	}
	if strings.Contains(candidate, expected) {
		return 2
	}
	return 4
}

func uploadPublishImage(ctx context.Context, source string, account platformAccount) (map[string]any, error) {
	content, name, contentType, e := readPublishImage(ctx, source)
	if e != nil {
		return nil, e
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(content))
	if e != nil {
		return nil, fmt.Errorf("图片无法解析：%w", e)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, e := mw.CreateFormFile("file", name)
	if e != nil {
		return nil, e
	}
	if _, e = part.Write(content); e != nil {
		return nil, e
	}
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, publishImageUploadURL, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Cookie", account.Cookie)
	req.Header.Set("Origin", sellerOrigin)
	req.Header.Set("Referer", sellerOrigin+"/?site=COMMONPRO")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	_ = contentType
	client := publishHTTPClient(account, 90*time.Second)
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("闲鱼图片上传失败：HTTP %d", resp.StatusCode)
	}
	var result map[string]any
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return nil, errors.New("闲鱼图片上传返回不是有效JSON")
	}
	obj, ok := result["object"].(map[string]any)
	if !ok || !boolValue(result["success"]) || textValue(obj["url"]) == "" {
		return nil, errors.New("闲鱼图片上传失败：接口未返回有效图片地址")
	}
	w, h := cfg.Width, cfg.Height
	if pix := strings.Split(strings.ToLower(textValue(obj["pix"])), "x"); len(pix) == 2 {
		if n, x := strconv.Atoi(pix[0]); x == nil {
			w = n
		}
		if n, x := strconv.Atoi(pix[1]); x == nil {
			h = n
		}
	}
	return map[string]any{"extraInfo": map[string]any{"isH": "false", "isT": "false", "raw": "false"}, "isQrCode": false, "url": textValue(obj["url"]), "heightSize": h, "widthSize": w, "major": false, "type": 0, "status": "done"}, nil
}
func readPublishImage(ctx context.Context, source string) ([]byte, string, string, error) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("Referer", "https://www.goofish.com/")
		resp, e := proxy.Client(90 * time.Second).Do(req)
		if e != nil {
			return nil, "", "", e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, "", "", fmt.Errorf("远程图片下载失败：HTTP %d", resp.StatusCode)
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		name := filepath.Base(req.URL.Path)
		if name == "" || name == "/" {
			name = "publish-image.jpg"
		}
		return b, name, resp.Header.Get("Content-Type"), e
	}
	path := source
	if strings.HasPrefix(strings.ReplaceAll(source, "\\", "/"), "/static/") {
		path = filepath.Join("data", "static", strings.TrimPrefix(strings.ReplaceAll(source, "\\", "/"), "/static/"))
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, "", "", fmt.Errorf("图片文件不存在：%s", path)
	}
	return b, filepath.Base(path), "application/octet-stream", nil
}

func buildExistingVideoItems(item, snapshot map[string]any) ([]map[string]any, []map[string]any) {
	videos := mapSlice(item["videos"])
	out := []map[string]any{}
	pending := []map[string]any{}
	for i, v := range videos {
		fileID := firstNonemptyAPI(textValue(v["file_id"]), textValue(v["mediaCloudFileId"]))
		if fileID == "" {
			pending = append(pending, v)
			continue
		}
		out = append(out, map[string]any{"mediaCloudFileId": fileID, "url": textValue(v["url"]), "videoUrl": textValue(v["url"]), "widthSize": publishIntValue(v["width"]), "heightSize": publishIntValue(v["height"]), "major": i == 0, "type": 1, "status": "done"})
	}
	if item["videos"] == nil && snapshot != nil {
		for _, v := range mapSlice(snapshot["imageInfoDOList"]) {
			if publishIntValue(v["type"]) != 0 {
				out = append(out, v)
			}
		}
	}
	return out, pending
}
func publishUserRights(snapshot map[string]any) []map[string]any {
	if snapshot != nil {
		if v := mapSlice(snapshot["userRightsProtocols"]); len(v) > 0 {
			return v
		}
	}
	return []map[string]any{{"enable": false, "serviceCode": "FAST_DELIVERY_48_HOUR"}, {"enable": false, "serviceCode": "FAST_DELIVERY_24_HOUR"}, {"enable": false, "serviceCode": "NONCONFORMITY_FREE_REFUND"}}
}
func textValue(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
func anySlice(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return nil
}
func publishStringSlice(v any) []string {
	out := []string{}
	for _, x := range anySlice(v) {
		if s := textValue(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}
func boolValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	}
	b, _ := strconv.ParseBool(textValue(v))
	return b
}
func integer(v any) (int, error) {
	switch x := v.(type) {
	case float64:
		return int(x), nil
	case int:
		return x, nil
	}
	return strconv.Atoi(textValue(v))
}
func publishIntValue(v any) int { n, _ := integer(v); return n }
func cents(v any, name string) (string, error) {
	n, e := strconv.ParseFloat(textValue(v), 64)
	if e != nil {
		return "", fmt.Errorf("%s格式不正确", name)
	}
	c := int64(n*100 + 0.5)
	if c <= 0 {
		return "", fmt.Errorf("%s必须大于0", name)
	}
	return strconv.FormatInt(c, 10), nil
}
func clampQuantity(v any) int {
	n, e := integer(v)
	if e != nil {
		return 1
	}
	if n < 0 {
		return 0
	}
	if n > 999999 {
		return 999999
	}
	return n
}
func lastFour(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return s
	}
	return string(r[len(r)-4:])
}
