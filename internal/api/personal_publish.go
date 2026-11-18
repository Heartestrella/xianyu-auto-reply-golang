package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

// detectPublishCapability 检测账号是否开通鱼小铺（卖家专业版）。
// 与 HTTP 接口 publishAccountCapability 的检测方式一致：优先调用鱼小铺后台
// mtop.idle.pc.backend.idleitem.preget，不可用或结论不可靠时回落个人版
// mtop.idle.pc.idleitem.preget。返回能力字段与刷新后的 Cookie。
func (s *Server) detectPublishCapability(ctx context.Context, account platformAccount) (map[string]any, string, error) {
	backend := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.idle.pc.backend.idleitem.preget", Version: "1.0", Cookie: account.Cookie, Proxy: accountProxy(account), Origin: "https://seller.goofish.com", Referer: "https://seller.goofish.com/?site=COMMONPRO", Data: map[string]any{"publishScene": "pcBackendPublish"}, ExtraParams: map[string]string{"idle_site_biz_code": "COMMONPRO", "spm_cnt": "a21107h.42826273.0.0"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO", "idle_user_group_member_id": ""}})
	latestCookie := backend.Cookie
	if backend.Success {
		if capability, recognized := parsePublishCapability(backend.Response); recognized {
			return capability, latestCookie, nil
		}
	}
	personal := xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: "mtop.idle.pc.idleitem.preget", Version: "1.0", Cookie: latestCookie, Proxy: accountProxy(account), Origin: "https://www.goofish.com", Referer: "https://www.goofish.com/publish", Data: map[string]any{}, ExtraParams: map[string]string{"spm_cnt": "a21ybx.publish.0.0"}})
	if personal.Cookie != "" {
		latestCookie = personal.Cookie
	}
	if !personal.Success {
		return nil, latestCookie, fmt.Errorf("账号发布能力检测失败：%s", firstNonemptyAPI(personal.Error, "闲鱼接口调用失败"))
	}
	capability, recognized := parsePublishCapability(personal.Response)
	if !recognized {
		return nil, latestCookie, fmt.Errorf("账号发布能力检测失败：无法识别账号是否开通鱼小铺")
	}
	return capability, latestCookie, nil
}

// buildPersonalPublishPayload 构造普通个人卖家（非鱼小铺）的单品发布载荷，
// 调用 mtop.idle.pc.idleitem.publish。请求结构参照原版 Python 项目的
// XianyuPersonalPublisher.publish_item：
//   - 禁止多规格与独立库存（普通卖家接口不支持）；
//   - 数量固定为 1；
//   - 不含视频（普通卖家网页发布不提供视频能力，忽略提交的视频）。
func buildPersonalPublishPayload(ctx context.Context, item map[string]any, account platformAccount) (map[string]any, string, error) {
	title, description := textValue(item["title"]), textValue(item["description"])
	if title == "" || description == "" {
		return nil, account.Cookie, fmt.Errorf("商品标题和商品描述不能为空")
	}
	if len(mapSlice(item["specifications"])) > 0 || len(mapSlice(item["sku_rows"])) > 0 {
		return nil, account.Cookie, fmt.Errorf("当前账号未开通鱼小铺，不能发布多规格和独立库存商品")
	}
	postFee, err := buildPublishPostFee(item)
	if err != nil {
		return nil, account.Cookie, err
	}
	images := publishStringSlice(item["images"])
	if len(images) == 0 {
		return nil, account.Cookie, fmt.Errorf("请至少上传一张商品图片")
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
	address, err := resolvePublishAddress(ctx, item, nil)
	if err != nil {
		return nil, account.Cookie, err
	}
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
	payload := map[string]any{
		"freebies": false, "itemTypeStr": "b", "quantity": "1", "simpleItem": "true",
		"imageInfoDOList": imageItems,
		"itemTextDTO":     map[string]any{"desc": description, "title": title, "titleDescSeparate": true},
		"itemLabelExtList": buildPublishLabels(item),
		"itemPriceDTO":    map[string]any{"origPriceInCent": orig, "priceInCent": price},
		"userRightsProtocols": []map[string]any{},
		"itemPostFeeDTO":  postFee, "itemAddrDTO": address,
		"defaultPrice": false,
		"itemCatDTO": map[string]any{"catId": textValue(item["platform_category_id"]), "catName": textValue(item["platform_category_name"]), "channelCatId": textValue(item["platform_channel_category_id"]), "tbCatId": textValue(item["platform_tb_category_id"])},
		"uniqueCode":  fmt.Sprintf("%d%s", time.Now().UnixMilli(), lastFour(strings.TrimSpace(account.AccountID))),
		"sourceId":    "pcMainPublish", "bizcode": "pcMainPublish", "publishScene": "pcMainPublish",
	}
	return payload, account.Cookie, nil
}
