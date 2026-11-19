# 商品列表分页上限说明

## 背景

部分账号在「获取商品（个人版）」时返回：

```
FAIL_BIZ_FORBIDDEN::||最大可查看页数或者每页最大可查看商品数超限
```

## 原因

闲鱼个人版接口 `mtop.idle.web.xyh.item.list` 对分页参数有服务端上限：

1. **每页条数上限 20**：`pageSize` 超过 20 直接返回上述错误（与账号商品数量无关，纯粹是参数越限）。
2. **可查看页数上限**：`pageNumber` 超过该账号可查看页数后同样返回该错误码。

旧版本 Go 实现把 `pageSize` 写死为 `30`（鱼小铺接口习惯值），因此普通个人账号一律返回该错误；鱼小铺账号走卖家接口不受影响，所以表现为「部分账号失败」。

## 修复

- 个人版请求体改为 `pageSize = 20`（`xianyu.ItemPageSize`），并补齐原版 Python 项目的请求字段：
  `groupId = "58877261"`、额外参数 `spm_pre = a21ybx.collection.menu.1.272b5141NafCNK`。
- `userId` 优先取 Cookie 中的 `unb`，缺失时回落到 `account_id`（与账号真实身份一致）。
- 翻页结束判定改为「本页原始条目数 < 每页条数即结束」，不再用去重后的条数比较（去重会误判为最后一页）。
- 第 2 页及以后出现该错误码时，视为「已到接口可查看页数上限」正常结束，不再把整个账号同步标记为失败；第 1 页出现则给出可读提示（分页参数超出接口限制）。
- 鱼小铺卖家接口 `mtop.alibaba.idle.seller.pc.common.item.search` 的每页条数同步统一为 20，与个人版保持一致。

## 涉及代码

| 位置 | 说明 |
| --- | --- |
| `internal/xianyu/itemlist.go` | 每页条数常量、请求体构造、超限错误识别、`userId` 解析 |
| `internal/api/platform.go` | 手动/接口触发的商品同步（`fetchPersonalItems`/`fetchSellerItems`） |
| `internal/scheduler/platform_sync.go` | 定时任务商品同步（`fetchPersonalItemsPage`/`fetchSellerItemsPage`） |
| `internal/scheduler/auto_relist.go` | 自动擦亮前的在售商品抓取 |
