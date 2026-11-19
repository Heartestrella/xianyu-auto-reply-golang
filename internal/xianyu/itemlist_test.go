package xianyu

import "testing"

func TestPersonalItemListDataUsesSafePageSize(t *testing.T) {
	data := PersonalItemListData(2, "123456")
	if data["pageSize"] != ItemPageSize {
		t.Fatalf("pageSize=%v want %d", data["pageSize"], ItemPageSize)
	}
	if ItemPageSize > 20 {
		t.Fatalf("个人版接口每页上限为 20，当前 %d 会触发 FAIL_BIZ_FORBIDDEN 超限", ItemPageSize)
	}
	if data["pageNumber"] != 2 || data["groupName"] != "在售" || data["groupName"] == "" {
		t.Fatalf("请求体字段异常: %#v", data)
	}
	if data["groupId"] != PersonalItemGroupID || data["defaultGroup"] != true || data["needGroupInfo"] != false {
		t.Fatalf("请求体字段异常: %#v", data)
	}
	if data["userId"] != "123456" {
		t.Fatalf("userId=%v", data["userId"])
	}
}

func TestPersonalItemListParamsAlignParent(t *testing.T) {
	p := PersonalItemListParams()
	if p["spm_cnt"] != "a21ybx.im.0.0" || p["spm_pre"] != "a21ybx.collection.menu.1.272b5141NafCNK" {
		t.Fatalf("spm 参数与原版不一致: %#v", p)
	}
}

func TestPersonalItemPageLimitError(t *testing.T) {
	cases := map[string]bool{
		"FAIL_BIZ_FORBIDDEN::||最大可查看页数或者每页最大可查看商品数超限": true,
		"FAIL_BIZ_FORBIDDEN::||最大可查看页数超限":             true,
		"FAIL_BIZ_IDLE_USER_UNAUTHORIZED::无权限访问":      false,
		"FAIL_SYS_SESSION_EXPIRED::Session过期":         false,
		"FAIL_BIZ_FORBIDDEN::||其他原因":                  false,
	}
	for in, want := range cases {
		if got := PersonalItemPageLimitError(in); got != want {
			t.Fatalf("PersonalItemPageLimitError(%q)=%v want %v", in, got, want)
		}
	}
}

func TestAccountUserIDPrefersCookieUnb(t *testing.T) {
	cookie := "unb=2200000001; cookie2=abc; _m_h5_tk=token_123"
	if got := AccountUserID(cookie, "848556xxxxx"); got != "2200000001" {
		t.Fatalf("应优先取 Cookie unb，得到 %q", got)
	}
	if got := AccountUserID("cookie2=abc", "848556xxxxx"); got != "848556xxxxx" {
		t.Fatalf("无 unb 时应回落到 account_id，得到 %q", got)
	}
}

func TestPageHasMore(t *testing.T) {
	if !PageHasMore(ItemPageSize) {
		t.Fatal("满页应继续翻页")
	}
	if PageHasMore(ItemPageSize - 1) {
		t.Fatal("不足一页应结束")
	}
}
