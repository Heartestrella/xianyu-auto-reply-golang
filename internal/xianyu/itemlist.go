package xianyu

import "strings"

// 商品列表接口的每页条数上限。
//
// 闲鱼个人版接口 mtop.idle.web.xyh.item.list 每页最多 20 条；超过时服务端直接返回
// FAIL_BIZ_FORBIDDEN::||最大可查看页数或者每页最大可查看条数超限。
// 原版 Python 项目 ItemInfoManager.get_item_list_info 的 page_size 默认值即为 20，
// 这里对齐同一取值，鱼小铺卖家接口也统一用 20（原版对两个 manager 传的是同一个
// page_size=20）。
const ItemPageSize = 20

// 个人版在售分组的固定 groupId（原版 Python 项目请求体中的硬编码值）。
const PersonalItemGroupID = "58877261"

// PersonalItemPageLimitError 判断是否为"分页/每页条数超限"类业务错误。
//
// 该错误既可能来自 pageSize 超过接口上限，也可能来自请求页码超过账号可查看页数上限。
// 调用方在翻页过程中遇到它时，应视为"已到接口分页上限"正常结束，而不是账号失败。
func PersonalItemPageLimitError(errText string) bool {
	if !strings.Contains(errText, "FAIL_BIZ_FORBIDDEN") {
		return false
	}
	return strings.Contains(errText, "最大可查看页数") ||
		strings.Contains(errText, "每页最大可查看") ||
		strings.Contains(errText, "每页最大可查看商品数") ||
		strings.Contains(errText, "ItemCountExceed")
}

// AccountUserID 解析请求体 userId：优先取 Cookie 里的 unb（与账号真实身份一致），
// 其次回落到库内 account_id。原版 Python 项目同样优先按 Cookie 中的账号身份抓取，
// 避免库内记录与 Cookie 不一致时抓到别人的商品列表。
func AccountUserID(cookie, fallback string) string {
	if id := strings.TrimSpace(ParseCookies(cookie)["unb"]); id != "" {
		return id
	}
	return strings.TrimSpace(fallback)
}

// PersonalItemListData 构造个人版商品列表请求体。
func PersonalItemListData(page int, userID string) map[string]any {
	return map[string]any{
		"needGroupInfo": false,
		"pageNumber":    page,
		"pageSize":      ItemPageSize,
		"groupName":     "在售",
		"groupId":       PersonalItemGroupID,
		"defaultGroup":  true,
		"userId":        userID,
	}
}

// PersonalItemListParams 个人版商品列表的额外查询参数（与原版一致）。
func PersonalItemListParams() map[string]string {
	return map[string]string{
		"spm_cnt": "a21ybx.im.0.0",
		"spm_pre": "a21ybx.collection.menu.1.272b5141NafCNK",
	}
}

// PageHasMore 根据本页条目数判断是否还有下一页（对齐原版"少于每页条数即结束"）。
func PageHasMore(pageItemCount int) bool {
	return pageItemCount >= ItemPageSize
}
