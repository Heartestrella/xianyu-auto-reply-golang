package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Config 控制本地滑块求解器行为。
type Config struct {
	// Enabled 为 false 时 Solve 直接返回未启用错误。
	Enabled bool
	// ChromePath 为浏览器可执行文件路径，为空时自动探测。
	ChromePath string
	// Headless 是否无头运行。
	Headless bool
	// Proxy 为浏览器代理地址（如 http://127.0.0.1:7890），可为空。
	Proxy string
	// Timeout 为单次验证的总超时。
	Timeout time.Duration
	// MaxRetries 为滑动重试次数，默认 3（与父仓库一致）。
	MaxRetries int
}

// DefaultConfig 返回可直接使用的默认配置。
func DefaultConfig() Config {
	return Config{Enabled: true, Headless: true, Timeout: 90 * time.Second, MaxRetries: 3}
}

// Solver 是纯 Go 的 Baxia/NC 滑块求解器。
type Solver struct {
	cfg Config
}

// NewSolver 创建求解器。
func NewSolver(cfg Config) *Solver {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	return &Solver{cfg: cfg}
}

// SolveResult 是滑块求解结果。
type SolveResult struct {
	// Cookies 仅包含 x5* 系列且一定含 x5sec，可直接合并进账号 Cookie。
	Cookies map[string]string
	// Attempts 实际滑动尝试次数。
	Attempts int
	// Engine 固定为 "local-chrome"，便于日志区分远程引擎。
	Engine string
}

var (
	errSliderNotFound = errors.New("页面中未找到滑块元素")
	errNotSolved      = errors.New("滑块验证未通过")
)

// URLEXpiredError 表示 punish 验证链接已过期（页面显示"抱歉，页面访问出现
// 了问题"），调用方应刷新链接后重试（对应父仓库的 URL_EXPIRED 哨兵）。
type URLEXpiredError struct{}

func (URLEXpiredError) Error() string {
	return "验证链接已过期（页面显示：抱歉，页面访问出现了问题）"
}

// urlExpiredText 是闲鱼 punish 链接过期时页面出现的固定文案。
const urlExpiredText = "抱歉，页面访问出现了问题"

// BaxiaPunishError 表示页面弹出 baxia-punish 图形识别验证（如"找出两个松鼠"）。
//
// 与普通 NC 滑块不同，这类验证需要人眼识别图形，脚本无法自动通过。
// 父仓库对此的处理是：不禁用账号、设置 5 小时冷却并发送通知。上层可用
// errors.As 识别本类型后走对应策略。
type BaxiaPunishError struct {
	Detail string
}

func (e *BaxiaPunishError) Error() string {
	if e.Detail != "" {
		return "检测到 baxia-punish 图形验证（需人工识别，脚本无法自动通过）: " + e.Detail
	}
	return "检测到 baxia-punish 图形验证（需人工识别，脚本无法自动通过）"
}

const detectBaxiaJS = `(() => {
  const dialog = document.querySelector('iframe#baxia-dialog-content');
  const punish = document.querySelector('#baxia-punish, .baxia-punish');
  const canvas = document.querySelector('canvas#captcha-question');
  let text = '';
  try { text = (document.body ? document.body.innerText : '').slice(0, 2000); } catch (e) {}
  const hint = /请按照说明进行验证|松鼠|拖动滑块出现完整/.test(text);
  return {baxia: !!(punish || canvas || (dialog && hint)), hint: hint, text: text.slice(0, 120)};
})()`

// detectBaxia 检测当前页面是否为 baxia 图形识别验证。
func detectBaxia(ctx context.Context, conn *cdpConn, session string) (bool, string) {
	raw, err := evalInPage(ctx, conn, session, detectBaxiaJS)
	if err != nil {
		return false, ""
	}
	var res struct {
		Baxia bool   `json:"baxia"`
		Hint  bool   `json:"hint"`
		Text  string `json:"text"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return false, ""
	}
	if res.Baxia {
		return true, strings.TrimSpace(res.Text)
	}
	return false, ""
}

// Solve 打开 punish 验证页并自动完成滑块。
//
// 流程对齐父仓库 common/services/captcha/slider_stealth.py：
//  1. 启动本地浏览器，注入账号 Cookie（.goofish.com 域）；
//  2. 导航到 punish?x5secdata=... 链接；
//  3. 查找滑块按钮 #nc_1_n1z 与轨道 #nc_1_n1t（支持同源 iframe）；
//  4. 计算距离（刮刮乐取 25%~35% 轨道宽度，普通滑块精确到轨道终点）；
//  5. 派发真人化鼠标轨迹并松手；
//  6. 严格判定：URL 需离开 punish 页且 x5sec 被新下发；
//  7. 最多重试 3 次，失败时点击刷新按钮或重新导航。
func (s *Solver) Solve(ctx context.Context, punishURL, cookie string) (*SolveResult, error) {
	if !s.cfg.Enabled {
		return nil, errors.New("本地滑块求解未启用")
	}
	punishURL = strings.TrimSpace(punishURL)
	if punishURL == "" {
		return nil, errors.New("验证链接为空")
	}
	executable, err := findChrome(s.cfg.ChromePath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	proc, err := launchChrome(ctx, executable, s.cfg.Proxy, s.cfg.Headless)
	if err != nil {
		return nil, err
	}
	defer proc.Close()

	conn := proc.conn
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return nil, fmt.Errorf("创建页面失败: %w", err)
	}
	defer func() {
		_ = conn.call(context.Background(), "", "Target.closeTarget", map[string]any{"targetId": target.TargetID}, nil)
	}()
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return nil, fmt.Errorf("连接页面失败: %w", err)
	}
	session := attached.SessionID
	if session == "" {
		return nil, errors.New("页面会话创建失败")
	}

	for _, method := range []string{"Page.enable", "Network.enable", "Runtime.enable"} {
		if err := conn.call(ctx, session, method, nil, nil); err != nil {
			return nil, fmt.Errorf("初始化 %s 失败: %w", method, err)
		}
	}
	// 与 MTOP 请求一致的浏览器标识，避免 Cookie/UA 指纹明显冲突。
	_ = conn.call(ctx, session, "Network.setUserAgentOverride", map[string]any{
		"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	}, nil)
	_ = conn.call(ctx, session, "Emulation.setAutomationOverride", map[string]any{"enabled": false}, nil)

	// 注入账号 Cookie，让 punish 页识别为已登录账号。
	cookies := parseCookiePairs(cookie)
	if len(cookies) > 0 {
		payload := make([]map[string]any, 0, len(cookies))
		for name, value := range cookies {
			payload = append(payload, map[string]any{"name": name, "value": value, "domain": ".goofish.com", "path": "/"})
		}
		_ = conn.call(ctx, session, "Network.setCookies", map[string]any{"cookies": payload}, nil)
	}

	preX5 := currentX5Sec(ctx, conn, session)
	attempts := s.cfg.MaxRetries
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt == 1 || lastErr != nil {
			if navErr := navigate(ctx, conn, session, punishURL); navErr != nil {
				return nil, fmt.Errorf("打开验证页失败: %w", navErr)
			}
			waitForSliderAllFrames(ctx, conn, session, 12*time.Second)
		}

		// 导航后先检测链接是否过期（父仓库：页面显示"抱歉，页面访问出现了问题"）。
		if expired := detectURLExpired(ctx, conn, session); expired {
			return nil, URLEXpiredError{}
		}

		if baxia, detail := detectBaxia(ctx, conn, session); baxia {
			return nil, &BaxiaPunishError{Detail: detail}
		}
		element, found, findErr := findSliderAllFrames(ctx, conn, session)
		if !found {
			if baxia, detail := detectBaxia(ctx, conn, session); baxia {
				return nil, &BaxiaPunishError{Detail: detail}
			}
			// 页面已无滑块：若 x5sec 已出现且为本次新下发，视为已通过（人工或已放行）。
			if x5 := currentX5Sec(ctx, conn, session); x5 != "" && x5 != preX5 {
				if all := collectX5Cookies(ctx, conn, session); all["x5sec"] != "" {
					return &SolveResult{Cookies: all, Attempts: attempt, Engine: "local-chrome"}, nil
				}
			}
			if findErr != nil {
				lastErr = findErr
			} else {
				lastErr = errSliderNotFound
			}
			if refreshErr := refreshSlider(ctx, conn, session); refreshErr != nil {
				return nil, fmt.Errorf("查找滑块失败: %w", lastErr)
			}
			continue
		}

		distance, scratch, distErr := slideDistance(ctx, conn, session, element)
		if distErr != nil || distance <= 0 {
			lastErr = fmt.Errorf("滑动距离计算失败: %v", distErr)
			continue
		}
		r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(attempt)))
		if scratch {
			distance = ScratchDistance(element.TrackWidth, r)
		}
		trajectory := GenerateTrajectory(distance, r)
		if err := performSlide(ctx, conn, session, element, trajectory, attempt); err != nil {
			lastErr = err
			_ = refreshSlider(ctx, conn, session)
			continue
		}

		if ok := confirmSuccess(ctx, conn, session, preX5); ok {
			all := collectX5Cookies(ctx, conn, session)
			if all["x5sec"] != "" {
				return &SolveResult{Cookies: all, Attempts: attempt, Engine: "local-chrome"}, nil
			}
		}
		lastErr = errNotSolved
		_ = refreshSlider(ctx, conn, session)
	}
	if lastErr == nil {
		lastErr = errNotSolved
	}
	return nil, fmt.Errorf("滑块自动验证失败（%d 次尝试）: %w", attempts, lastErr)
}

// sliderElement 描述页面中的滑块按钮与轨道。
type sliderElement struct {
	ButtonX, ButtonY     float64
	ButtonWidth, ButtonH float64
	TrackWidth           float64
	PageURL              string
	Scratch              bool
	// IsMain 表示滑块在主文档（或同源 iframe）上下文中。
	IsMain bool
}

type findResult struct {
	Found      bool    `json:"found"`
	ButtonX    float64 `json:"buttonX"`
	ButtonY    float64 `json:"buttonY"`
	ButtonW    float64 `json:"buttonW"`
	ButtonH    float64 `json:"buttonH"`
	TrackWidth float64 `json:"trackWidth"`
	URL        string  `json:"url"`
	Scratch    bool    `json:"scratch"`
}

const findSliderJS = `(() => {
  const scan = (doc) => {
    const btn = doc.querySelector('#nc_1_n1z') || doc.querySelector('.nc_iconfont') || doc.querySelector('.btn_slide') || doc.querySelector('#scratch-captcha-btn');
    const trk = doc.querySelector('#nc_1_n1t') || doc.querySelector('.nc_scale') || doc.querySelector('.nc_1_n1t');
    if (!btn || !trk) return null;
    const br = btn.getBoundingClientRect(), tr = trk.getBoundingClientRect();
    if (br.width <= 0 || tr.width <= 0) return null;
    return {br, tr};
  };
  let hit = scan(document), ox = 0, oy = 0;
  if (!hit) {
    for (const f of Array.from(document.querySelectorAll('iframe'))) {
      let doc = null;
      try { doc = f.contentDocument; } catch (e) { doc = null; }
      if (!doc) continue;
      const inner = scan(doc);
      if (inner) {
        const fr = f.getBoundingClientRect();
        ox = fr.left + (f.clientLeft || 0); oy = fr.top + (f.clientTop || 0); hit = inner; break;
      }
    }
  }
  if (!hit) return {found: false, url: location.href};
  let text = '';
  try { text = document.documentElement.outerHTML.slice(0, 4000); } catch (e) {}
  const scratch = /scratch-captcha/i.test(text);
  return {found: true, buttonX: hit.br.x + ox, buttonY: hit.br.y + oy, buttonW: hit.br.width, buttonH: hit.br.height, trackWidth: hit.tr.width, url: location.href, scratch: scratch};
})()`

// frameScanJS 在单个（可能是跨源的）frame 上下文里查找滑块，并把本 frame
// 的坐标原点（window.screenX/Y + outerWidth/innerWidth 边框差）一并返回，
// 供外层换算成页面级坐标。跨源 iframe 无法从主文档访问，只能靠各 frame
// 自己的执行上下文执行本脚本（对应父仓库 page.frames 遍历）。
const frameScanJS = `(() => {
  const btn = document.querySelector('#nc_1_n1z') || document.querySelector('.nc_iconfont') || document.querySelector('.btn_slide');
  const trk = document.querySelector('#nc_1_n1t') || document.querySelector('.nc_scale') || document.querySelector('.nc_1_n1t');
  const found = !!(btn && trk);
  let bx = 0, by = 0, bw = 0, bh = 0, tw = 0;
  if (found) {
    const br = btn.getBoundingClientRect(), tr = trk.getBoundingClientRect();
    bw = br.width; bh = br.height; tw = tr.width;
    bx = br.x; by = br.y;
  }
  let scratch = false;
  try { scratch = /scratch-captcha/i.test(document.documentElement.outerHTML.slice(0, 4000)); } catch (e) {}
  return {found: found, bx: bx, by: by, bw: bw, bh: bh, tw: tw, url: location.href, scratch: scratch};
})()`

// frameOffsetJS 在主文档上下文执行：按 iframe 的 bounding rect 换算指定
// frame 的坐标偏移（跨源 frame 也适用，因为偏移量只依赖主文档的布局）。
const frameOffsetJS = `(() => {
  const out = [];
  for (const f of Array.from(document.querySelectorAll('iframe'))) {
    const r = f.getBoundingClientRect();
    out.push({id: f.id || '', name: f.name || '', src: (f.src || '').slice(0, 200),
      ox: r.left + (f.clientLeft || 0), oy: r.top + (f.clientTop || 0)});
  }
  return out;
})()`

type frameHit struct {
	Found   bool    `json:"found"`
	BX      float64 `json:"bx"`
	BY      float64 `json:"by"`
	BW      float64 `json:"bw"`
	BH      float64 `json:"bh"`
	TW      float64 `json:"tw"`
	URL     string  `json:"url"`
	Scratch bool    `json:"scratch"`
}

// evalInContext 在指定执行上下文里求值（跨源 frame 只能用此方式访问）。
func evalInContext(ctx context.Context, conn *cdpConn, session string, contextID int64, expression string) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	err := conn.call(ctx, session, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true, "contextId": contextID,
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.ExceptionDetails) > 0 && string(out.ExceptionDetails) != "null" {
		return nil, fmt.Errorf("frame 脚本异常: %s", string(out.ExceptionDetails))
	}
	return out.Result.Value, nil
}

// findSliderAllFrames 先扫主文档（含同源 iframe），再遍历全部执行上下文
// 覆盖跨源 iframe（对应父仓库 SliderElementFinder 的 frames 遍历）。
func findSliderAllFrames(ctx context.Context, conn *cdpConn, session string) (sliderElement, bool, error) {
	// 1. 主文档 + 同源 iframe（一条脚本完成，含 iframe 偏移换算）。
	if raw, err := evalInPage(ctx, conn, session, findSliderJS); err == nil {
		var fr findResult
		if json.Unmarshal(raw, &fr) == nil && fr.Found {
			return sliderElement{ButtonX: fr.ButtonX, ButtonY: fr.ButtonY, ButtonWidth: fr.ButtonW, ButtonH: fr.ButtonH, TrackWidth: fr.TrackWidth, PageURL: fr.URL, Scratch: fr.Scratch, IsMain: true}, true, nil
		}
	}
	// 2. 跨源 iframe：取执行上下文快照（store 持续缓存，导航后会收到
	//    executionContextsCleared 并重建），逐个扫描后换算页面坐标。
	contexts := conn.collectExecutionContexts(ctx, session, 0)
	if len(contexts) == 0 {
		return sliderElement{}, false, nil
	}
	type frameOffset struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Src     string  `json:"src"`
		OffsetX float64 `json:"ox"`
		OffsetY float64 `json:"oy"`
	}
	offsets := []frameOffset{}
	if raw, err := evalInPage(ctx, conn, session, frameOffsetJS); err == nil {
		_ = json.Unmarshal(raw, &offsets)
	}
	for _, execCtx := range contexts {
		if execCtx.IsMain {
			continue
		}
		raw, err := evalInContext(ctx, conn, session, execCtx.ID, frameScanJS)
		if err != nil {
			continue
		}
		var hit frameHit
		if json.Unmarshal(raw, &hit) != nil || !hit.Found {
			continue
		}
		// 换算到页面坐标：闲鱼 punish 页通常只有一个 baxia iframe，取首个
		// 偏移；无 iframe 偏移信息（异常布局）时按零偏移处理。
		offX, offY := 0.0, 0.0
		if len(offsets) > 0 {
			offX, offY = offsets[0].OffsetX, offsets[0].OffsetY
		}
		return sliderElement{
			ButtonX: hit.BX + offX, ButtonY: hit.BY + offY,
			ButtonWidth: hit.BW, ButtonH: hit.BH, TrackWidth: hit.TW,
			PageURL: hit.URL, Scratch: hit.Scratch, IsMain: false,
		}, true, nil
	}
	return sliderElement{}, false, nil
}

func evalInPage(ctx context.Context, conn *cdpConn, session, expression string) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	err := conn.call(ctx, session, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true,
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.ExceptionDetails) > 0 && string(out.ExceptionDetails) != "null" {
		return nil, fmt.Errorf("页面脚本异常: %s", string(out.ExceptionDetails))
	}
	return out.Result.Value, nil
}

func navigate(ctx context.Context, conn *cdpConn, session, url string) error {
	var res struct {
		FrameID string `json:"frameId"`
		Error   struct {
			Text string `json:"text"`
		} `json:"errorText"`
	}
	if err := conn.call(ctx, session, "Page.navigate", map[string]any{"url": url, "transitionType": "typed"}, &res); err != nil {
		return err
	}
	if res.Error.Text != "" {
		return errors.New(res.Error.Text)
	}
	// 等待加载稳定，具体元素由 waitForSlider 轮询。
	return nil
}

func waitForSliderAllFrames(ctx context.Context, conn *cdpConn, session string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		if _, found, _ := findSliderAllFrames(ctx, conn, session); found {
			return
		}
		// 页面已出现过期文案时无需再等滑块。
		if detectURLExpired(ctx, conn, session) {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// detectURLExpired 检测 punish 页是否显示"抱歉，页面访问出现了问题"
// （父仓库判定验证链接已过期的固定文案）。
func detectURLExpired(ctx context.Context, conn *cdpConn, session string) bool {
	raw, err := evalInPage(ctx, conn, session, `document.body ? document.body.innerText.slice(0,2000) : ''`)
	if err != nil {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return false
	}
	return strings.Contains(text, urlExpiredText)
}

// successMarkersJS 检查 NC 滑块的"已通过"视觉标志（与父仓库 SUCCESS_SELECTORS 一致）。
const successMarkersJS = `(() => {
  const sels = ['.nc_ok_icon', '.nc-lang-cnt .nc_ok', '#nc_1_n1z.nc_ok'];
  for (const s of sels) { const el = document.querySelector(s); if (el) return true; }
  return false;
})()`

func hasSuccessMarkers(ctx context.Context, conn *cdpConn, session string) bool {
	raw, err := evalInPage(ctx, conn, session, successMarkersJS)
	if err != nil {
		return false
	}
	var hit bool
	return json.Unmarshal(raw, &hit) == nil && hit
}

func slideDistance(ctx context.Context, conn *cdpConn, session string, el sliderElement) (float64, bool, error) {
	// 与父仓库一致：优先用 JS 精确读取 trackRect.width - buttonRect.width。
	expr := `(() => {
          const b = document.querySelector('#nc_1_n1z') || document.querySelector('.nc_iconfont');
          const t = document.querySelector('#nc_1_n1t') || document.querySelector('.nc_scale');
          if (b && t) { const r1 = b.getBoundingClientRect(), r2 = t.getBoundingClientRect(); return r2.width - r1.width; }
          return null;
        })()`
	raw, _ := evalInPage(ctx, conn, session, expr)
	var precise float64
	if raw != nil && json.Unmarshal(raw, &precise) == nil && precise > 0 {
		return precise, el.Scratch, nil
	}
	if el.TrackWidth <= 0 || el.ButtonWidth <= 0 {
		return 0, el.Scratch, errors.New("轨道或按钮尺寸无效")
	}
	return el.TrackWidth - el.ButtonWidth, el.Scratch, nil
}

func mouseEvent(ctx context.Context, conn *cdpConn, session, typ string, x, y float64, buttons int) error {
	params := map[string]any{
		"type": typ, "x": x, "y": y, "button": "left", "clickCount": 1, "buttons": buttons,
	}
	return conn.call(ctx, session, "Input.dispatchMouseEvent", params, nil)
}

// performSlide 依据父仓库 _simulate_slide 的做法派发鼠标事件：
// 先悬停接近、按下，再按轨迹逐点移动，最后松手。
func performSlide(ctx context.Context, conn *cdpConn, session string, el sliderElement, trajectory []Point, attempt int) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	startX := el.ButtonX + el.ButtonWidth/2
	startY := el.ButtonY + el.ButtonH/2

	time.Sleep(time.Duration(100+r.Intn(200)) * time.Millisecond)
	// 第一阶段：从侧后方接近，模拟真人移动。
	approachX := startX + (-30 + r.Float64()*20)
	approachY := startY + (-15 + r.Float64()*30)
	if err := mouseEvent(ctx, conn, session, "mouseMoved", approachX, approachY, 0); err != nil {
		return err
	}
	time.Sleep(time.Duration(150+r.Intn(150)) * time.Millisecond)
	if err := mouseEvent(ctx, conn, session, "mouseMoved", startX, startY, 0); err != nil {
		return err
	}
	time.Sleep(time.Duration(100+r.Intn(150)) * time.Millisecond)
	// 第二阶段：按下。
	if err := mouseEvent(ctx, conn, session, "mousePressed", startX, startY, 1); err != nil {
		return err
	}
	time.Sleep(time.Duration(50+r.Intn(40)) * time.Millisecond)
	// 第三阶段：按轨迹移动。
	currentX, currentY := startX, startY
	for _, p := range trajectory {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		currentX = startX + p.X
		currentY = startY + p.Y
		if err := mouseEvent(ctx, conn, session, "mouseMoved", currentX, currentY, 1); err != nil {
			return err
		}
		if p.Delay > 0 {
			time.Sleep(p.Delay)
		}
	}
	// 刮刮乐松手前按父仓库停顿 0.3~0.5s（模拟"观察"）。
	if el.Scratch {
		pause := 300 + r.Intn(200)
		time.Sleep(time.Duration(pause) * time.Millisecond)
	}
	// 第四阶段：松手，稍作停留让页面结算。
	time.Sleep(time.Duration(80+r.Intn(60)) * time.Millisecond)
	return mouseEvent(ctx, conn, session, "mouseReleased", currentX, currentY, 0)
}

func refreshSlider(ctx context.Context, conn *cdpConn, session string) error {
	// 先点验证框的刷新按钮，失败时退回整页重载。
	expr := `(() => {
      const sels = ['#nc_1_refresh', '.nc_iconfont.btn_refresh', '.nc-lang-cnt .nc_1_n1z', '[class*="refresh"]'];
      for (const s of sels) { const el = document.querySelector(s); if (el) { el.click(); return true; } }
      return false;
    })()`
	raw, err := evalInPage(ctx, conn, session, expr)
	var clicked bool
	if err == nil && json.Unmarshal(raw, &clicked) == nil && clicked {
		time.Sleep(1500 * time.Millisecond)
		return nil
	}
	if err := conn.call(ctx, session, "Page.reload", map[string]any{}, nil); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	return nil
}

func currentX5Sec(ctx context.Context, conn *cdpConn, session string) string {
	cookies := allCookies(ctx, conn, session)
	return cookies["x5sec"]
}

// allCookies 读取浏览器上下文中的全部 Cookie。
func allCookies(ctx context.Context, conn *cdpConn, session string) map[string]string {
	var out struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if err := conn.call(ctx, session, "Network.getAllCookies", map[string]any{}, &out); err != nil {
		return map[string]string{}
	}
	m := make(map[string]string, len(out.Cookies))
	for _, c := range out.Cookies {
		m[c.Name] = c.Value
	}
	return m
}

// collectX5Cookies 返回全部 x5* Cookie（与父仓库一致：必须含 x5sec 才算通过）。
func collectX5Cookies(ctx context.Context, conn *cdpConn, session string) map[string]string {
	all := allCookies(ctx, conn, session)
	return filterX5(all)
}

func filterX5(all map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range all {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x5") || strings.Contains(lower, "x5sec") {
			out[name] = value
		}
	}
	return out
}

func pageURL(ctx context.Context, conn *cdpConn, session string) string {
	raw, err := evalInPage(ctx, conn, session, "location.href")
	if err != nil {
		return ""
	}
	var url string
	_ = json.Unmarshal(raw, &url)
	return url
}

func isPunishURL(url string) bool {
	for _, kw := range []string{"punish", "x5step=2", "action=captcha", "pureCaptcha", "/captcha"} {
		if strings.Contains(url, kw) {
			return true
		}
	}
	return false
}

// confirmSuccess 严格判定：URL 已离开 punish 页，且 x5sec 为本次新下发。
//
// 与父仓库 check_verification_success_fast 的层次一致：
//  1. 出现 .nc_ok_icon 等视觉成功标志 → 进入二次确认；
//  2. URL 已离开 punish/x5step=2/pureCaptcha；
//  3. x5sec 与滑动前快照不同（真正新下发）。
func confirmSuccess(ctx context.Context, conn *cdpConn, session, preX5 string) bool {
	// 给页面结算与 Set-Cookie 落盘留出窗口，逐步放宽。
	for i := 0; i < 8; i++ {
		if ctx.Err() != nil {
			return false
		}
		time.Sleep(time.Duration(300+400*i) * time.Millisecond)
		x5 := currentX5Sec(ctx, conn, session)
		if x5 == "" {
			continue
		}
		if x5 == preX5 {
			continue
		}
		// x5sec 已新下发：URL 离开 punish 或出现成功标志即判定通过。
		url := pageURL(ctx, conn, session)
		if url == "" || !isPunishURL(url) {
			return true
		}
		if hasSuccessMarkers(ctx, conn, session) {
			return true
		}
	}
	return false
}

// parseCookiePairs 解析 "a=b; c=d" 形式的 Cookie 字符串。
func parseCookiePairs(raw string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(strings.ReplaceAll(raw, "\n", ";"), ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		name := strings.TrimSpace(kv[0])
		if name == "" {
			continue
		}
		m[name] = strings.TrimSpace(kv[1])
	}
	return m
}

// MergeCookies 将求解得到的 x5* Cookie 合并进原始 Cookie 字符串。
func MergeCookies(raw string, updates map[string]string) string {
	existing := parseCookiePairs(raw)
	order := []string{}
	for _, part := range strings.Split(raw, ";") {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if name != "" {
			order = append(order, name)
		}
	}
	for k, v := range updates {
		existing[k] = v
	}
	seen := map[string]bool{}
	parts := make([]string, 0, len(existing))
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true
		if v, ok := existing[name]; ok {
			parts = append(parts, name+"="+v)
		}
	}
	// 原 Cookie 中没有的键（新增的 x5* 等）按字典序输出，避免 map 迭代顺序
	// 随机导致合并结果不可复现（同一份 Cookie 每次拼接顺序都不同）。
	extra := make([]string, 0, len(existing))
	for name := range existing {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		parts = append(parts, name+"="+existing[name])
	}
	return strings.Join(parts, "; ")
}
