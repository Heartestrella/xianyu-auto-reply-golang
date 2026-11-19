# 本地自动过滑块 / 人脸验证（纯 Go 实现）

本仓库补齐了父仓库（Python 版 `zhinianboke/xianyu-auto-reply`）的「自动过滑块 / 人脸验证」能力，
**全部用 Go 实现，不引入 Playwright / DrissionPage / Python 运行时**。

## 一、自动过滑块（Baxia / NC 滑块）

实现位置：`internal/captcha/`

| 文件 | 作用 |
| --- | --- |
| `trajectory.go` | 真人化轨迹生成，严格复刻父仓库 `trajectory.py` 参数 |
| `cdp.go` | 极简 CDP（Chrome DevTools Protocol）客户端，负责拉起/连接本地 Chrome |
| `slider.go` | 滑块求解主流程（查找元素 → 计算距离 → 派发鼠标事件 → 严格判定 x5sec） |
| `config.go` | 从 `system_settings` 读取配置 |

### 与父仓库对齐的关键行为

1. **触发点**：任意 MTOP 调用返回 `FAIL_SYS_USER_VALIDATE / RGV587 / punish / captcha` 且带
   `data.url`（punish 链接）时，自动调用本地求解器；成功后把新的 `x5*` Cookie 合并进账号
   Cookie 并**自动重试原请求**（最多 2 次）。
2. **滑动距离**：优先用 JS 读取 `trackRect.width - buttonRect.width`；普通滑块精确到轨道终点；
   刮刮乐（页面含 `scratch-captcha` 等特征）取 25%~35% 轨道宽度。
3. **轨迹参数**：20 个时间分位控制点、`preferred_x_progress` / `preferred_y_offsets`、
   总时长 0.40~0.48s、距离 ≥180px 时光标越界 45%~55%、每点 X 加 ±0.008 扰动并保证递增。
4. **严格成功判定**：URL 必须已离开 punish 页（`punish` / `x5step=2` / `action=captcha` /
   `pureCaptcha` / `/captcha`）或出现 `.nc_ok_icon` / `.nc-lang-cnt .nc_ok` /
   `#nc_1_n1z.nc_ok` 视觉成功标志，且 `x5sec` 为本次**新下发**（与滑动前快照不同）。
   只有 `x5secdata` / `x5sectag` 一律视为未通过。
5. **重试策略**：最多 3 次，失败时点击验证框刷新按钮或重载页面；刮刮乐松手前
   停顿 0.3~0.5s 模拟观察（与父仓库一致）。
6. **跨源 iframe**：滑块位于 baxia 跨源 iframe 时，通过收集各 frame 的 JS
   执行上下文（CDP `Runtime.executionContextCreated`）逐 frame 查找元素，并用主文档
   iframe 布局偏移换算页面坐标（等价于父仓库 Playwright 的 `page.frames` 遍历）。
7. **链接过期**：打开 punish 页出现「抱歉，页面访问出现了问题」时返回
   `captcha.URLEXpiredError`（对应父仓库 URL_EXPIRED 哨兵），由调用方刷新链接后重试。
6. **baxia 图形验证**（如"找出两个松鼠"）：脚本无法自动通过，求解器返回
   `*captcha.BaxiaPunishError`，上层写入 `baxia_punish_captcha` 通知日志（不误判账号失效）。

### 配置（`system_settings`）

| Key | 默认 | 说明 |
| --- | --- | --- |
| `captcha.local_slider_enabled` | `false` | 启用本地自动过滑块 |
| `captcha.local_slider_headless` | `true` | 浏览器无头模式 |
| `captcha.local_slider_chrome_path` | 空 | Chrome/Chromium 路径，空则自动探测（也可用 `CHROME_PATH` 环境变量） |
| `captcha.local_slider_timeout` | `90` | 单次求解超时（秒） |
| `captcha.local_slider_retries` | `3` | 滑动重试次数 |
| `captcha.local_slider_proxy` | 空 | 浏览器代理；为空时复用全局代理（`proxy.enabled` + `proxy.url`） |
| `captcha.local_slider_disabled` | `false` | 父仓库同款"本机滑块不处理"总开关，优先级最高 |

### 管理接口（管理员）

- `GET /api/captcha/local-config` 读取配置
- `PUT /api/captcha/local-config` 保存配置（字段：`enabled`/`headless`/`chrome_path`/`proxy`/`timeout`/`retries`）
- `POST /api/captcha/local-solve` 手工测试：`{"url": "<punish链接>", "cookie": "<账号Cookie>"}`

### 本地/远程优先级

- MTOP 自动重试链路：本地引擎（受上述开关控制）。
- 密码/协议登录 `login.do` 滑块：先走已有远程接口 `password_login.remote_url`；未配置时回退本地引擎。

### 部署提示

- 本机需可执行 Chrome/Chromium；Linux 容器可构建时加 `--build-arg WITH_CHROMIUM=true`
  让镜像内置 chromium（镜像体积会明显增大）。
- 首次启动建议先用 `POST /api/captcha/local-solve` 用一个旧 punish 链接做连通性验证。
- 风控日志页（`event_logs.kind='risk_control'`）会记录 `call_type=local` 的求解结果，
  便于与 `remote` 分开统计成功率。

## 二、人脸验证

两条链路均已为纯 Go 实现：

1. **扫码登录人脸**（`internal/api/qr_face_verification.go`）：跟随 `iframeRedirectUrl` →
   提取 htoken → `verify_modes.htm` → `identity_verify.htm` 提取二维码 → 轮询
   `photoVerify/check.do`（code=3）→ 跟随 `ivCheckLogin.htm` 收集 Cookie 与 `unb`。
2. **密码/协议登录人脸**（`internal/api/face_flow.go`，本次补齐）：与扫码登录同一套纯 API 链路，
   但复用密码登录的 cookie jar；二维码通过登录状态查询接口的 `face_qr_url` 字段下发给前端，
   用户手机完成刷脸后自动落地 Cookie、写入账号并返回成功，无需人工复制 Cookie。

协议登录会话总预算已由 3 分钟放宽到 10 分钟（覆盖多轮过滑块 + 用户刷脸耗时，对齐父仓库 600s）。

> 说明：闲鱼在部分风控场景会要求"人脸/身份验证"且明确绑定当前设备；此时无法脚本自动通过，
> 系统仍会下发 `face_verification` 通知，由用户在手机端完成验证。

## 三、测试

- 单元测试：`go test ./internal/captcha/`（轨迹形态/参数区间、Cookie 合并、x5 过滤、
  punish URL 判定、过期文案与成功标志选择器回归）。
- 端到端（需本机 Chrome）：`CAPTCHA_E2E=1 go test ./internal/captcha/`，包含三个真实
  浏览器用例：普通滑块页、跨源 iframe 滑块页（含通过后 302 跳转 + 主域 x5sec 落盘）、
  链接过期页（返回 URLEXpiredError）。
