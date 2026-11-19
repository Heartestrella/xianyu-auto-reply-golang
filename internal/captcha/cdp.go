package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// cdpMessage 是 CDP 协议报文（命令与事件共用）。
type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message) }

// cdpEvent 是收到的事件。
type cdpEvent struct {
	Method    string
	Params    json.RawMessage
	SessionID string
}

// cdpConn 是对一条 DevTools WebSocket 连接的最小封装。
type cdpConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	nextID  atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan *cdpMessage

	events       chan cdpEvent
	closeOnce    sync.Once
	done         chan struct{}
	closeErr     error
	contextStore contextStore
}

func dialCDP(wsURL string) (*cdpConn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	c := &cdpConn{ws: ws, pending: map[int64]chan *cdpMessage{}, events: make(chan cdpEvent, 256), done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *cdpConn) readLoop() {
	defer c.shutdown(errors.New("cdp connection closed"))
	for {
		var m cdpMessage
		if err := c.ws.ReadJSON(&m); err != nil {
			return
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- &m
			}
			continue
		}
		if m.Method != "" {
			ev := cdpEvent{Method: m.Method, Params: m.Params, SessionID: m.SessionID}
			// 执行上下文事件持续缓存到 store（跨源 frame 查找依赖），其余事件
			// 走 channel；积压时丢弃，不影响命令应答。
			if m.Method == "Runtime.executionContextCreated" || m.Method == "Runtime.executionContextsCleared" {
				if m.Method == "Runtime.executionContextsCleared" {
					c.executionContextsCleared(m.SessionID)
				} else {
					c.recordExecutionContext(ev)
				}
			}
			select {
			case c.events <- ev:
			default: // 事件积压时丢弃，不影响命令应答
			}
		}
	}
}

func (c *cdpConn) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		close(c.done)
		_ = c.ws.Close()
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	})
}

func (c *cdpConn) call(ctx context.Context, sessionID, method string, params any, out any) error {
	select {
	case <-c.done:
		return fmt.Errorf("cdp closed: %v", c.closeErr)
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := c.nextID.Add(1)
	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ch := make(chan *cdpMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, raw)
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("cdp closed: %v", c.closeErr)
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// chromeProcess 表示一个被拉起的浏览器进程及其 DevTools 连接。
type chromeProcess struct {
	cmd      *exec.Cmd
	conn     *cdpConn
	userData string
	port     int
}

var browserCandidates = []string{
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
	"chrome", "microsoft-edge", "msedge",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
}

// findChrome 依配置路径、环境变量与常见安装位置定位浏览器可执行文件。
func findChrome(configured string) (string, error) {
	if p := strings.TrimSpace(configured); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		if _, err := exec.LookPath(p); err == nil {
			return p, nil
		}
	}
	for _, env := range []string{"CHROME_PATH", "CHROME_BIN", "CAPTCHA_SLIDER_CHROME_PATH"} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	for _, name := range browserCandidates {
		if _, err := exec.LookPath(name); err == nil {
			return name, nil
		}
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return "", errors.New("未找到可用的 Chrome/Chromium，可配置 captcha.slider_chrome_path 或设置 CHROME_PATH")
}

// launchChrome 以远程调试模式拉起一个干净的浏览器实例。
func launchChrome(ctx context.Context, executable, proxyURL string, headless bool) (*chromeProcess, error) {
	userData, err := os.MkdirTemp("", "xy-captcha-profile-*")
	if err != nil {
		return nil, err
	}
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + userData,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-blink-features=AutomationControlled",
		"--window-size=1280,900",
		"about:blank",
	}
	if headless {
		args = append(args, "--headless=new", "--hide-scrollbars", "--mute-audio")
	}
	if runtime.GOOS == "linux" {
		args = append(args, "--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu")
	}
	if p := strings.TrimSpace(proxyURL); p != "" {
		args = append(args, "--proxy-server="+p)
	}
	cmd := exec.Command(executable, args...)
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(userData)
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}
	proc := &chromeProcess{cmd: cmd, userData: userData}
	// 进程退出后自动回收，避免僵尸进程。
	go func() { _ = cmd.Wait() }()

	// 等待 DevToolsActivePort 就绪，读取端口与 ws 路径。
	var port int
	var wsPath string
	deadline := time.Now().Add(30 * time.Second)
	portFile := filepath.Join(userData, "DevToolsActivePort")
	for {
		if content, readErr := os.ReadFile(portFile); readErr == nil {
			lines := strings.Split(strings.TrimSpace(string(content)), "\n")
			if len(lines) >= 2 {
				fmt.Sscanf(strings.TrimSpace(lines[0]), "%d", &port)
				wsPath = strings.TrimSpace(lines[1])
				if port > 0 {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			proc.Close()
			return nil, errors.New("等待浏览器调试端口超时")
		}
		select {
		case <-ctx.Done():
			proc.Close()
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	proc.port = port
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath)
	conn, err := dialCDP(wsURL)
	if err != nil {
		proc.Close()
		return nil, fmt.Errorf("连接浏览器调试端口失败: %w", err)
	}
	proc.conn = conn
	return proc, nil
}

func (p *chromeProcess) Close() {
	if p == nil {
		return
	}
	if p.conn != nil {
		_ = p.conn.call(context.Background(), "", "Browser.close", nil, nil)
		p.conn.shutdown(errors.New("browser closed"))
	}
	if p.cmd != nil && p.cmd.Process != nil {
		// 给浏览器一点时间自行退出，超时后强制结束。
		time.Sleep(300 * time.Millisecond)
		_ = p.cmd.Process.Kill()
	}
	if p.userData != "" {
		_ = os.RemoveAll(p.userData)
	}
}

// waitForBody 简单判定 HTTP 服务可用。
func waitPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("调试端口未就绪")
}

// httpGetJSON 供目标发现等只读场景使用。
func httpGetJSON(url string, out any) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// executionContext 描述页面的一个 JS 执行上下文（主文档或某个 frame）。
type executionContext struct {
	ID      int64
	FrameID string
	IsMain  bool
}

// contextsMu 保护 contexts 快照：readLoop 每次收到
// Runtime.executionContextCreated 事件就追加，丢弃 channel 缓存。
type contextStore struct {
	mu       sync.Mutex
	contexts []executionContext
	seen     map[int64]bool
}

// collectExecutionContexts 返回当前已知的全部执行上下文快照。
//
// 事件在 readLoop 中持续缓存（不依赖 channel 缓冲，避免事件积压被丢弃），
// 调用方导航后稍等加载再取快照即可覆盖跨源 iframe。
func (c *cdpConn) collectExecutionContexts(ctx context.Context, session string, settle time.Duration) []executionContext {
	if settle > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(settle):
		}
	}
	c.contextStore.mu.Lock()
	defer c.contextStore.mu.Unlock()
	out := make([]executionContext, len(c.contextStore.contexts))
	copy(out, c.contextStore.contexts)
	return out
}

func (c *cdpConn) recordExecutionContext(ev cdpEvent) {
	if ev.Method != "Runtime.executionContextCreated" {
		return
	}
	var params struct {
		Context struct {
			ID      int64           `json:"id"`
			AuxData json.RawMessage `json:"auxData"`
		} `json:"context"`
	}
	if json.Unmarshal(ev.Params, &params) != nil || params.Context.ID == 0 {
		return
	}
	frameID := ""
	isMain := false
	var aux struct {
		IsDefault bool   `json:"isDefault"`
		FrameID   string `json:"frameId"`
		Type      string `json:"type"`
	}
	if json.Unmarshal(params.Context.AuxData, &aux) == nil {
		frameID = aux.FrameID
		// isDefault 对每个 frame 的默认上下文都为 true，不能据此判"主文档"。
		// 主文档 = 无 frameId；worker/isolated 上下文一律跳过。
		isMain = aux.IsDefault && aux.Type != "worker" && aux.Type != "isolated" && frameID == ""
	}
	c.contextStore.mu.Lock()
	if c.contextStore.seen == nil {
		c.contextStore.seen = map[int64]bool{}
	}
	if !c.contextStore.seen[params.Context.ID] {
		c.contextStore.seen[params.Context.ID] = true
		c.contextStore.contexts = append(c.contextStore.contexts, executionContext{ID: params.Context.ID, FrameID: frameID, IsMain: isMain})
	}
	c.contextStore.mu.Unlock()
}

// executionContextsCleared 导航后旧上下文失效：清空快照。
func (c *cdpConn) executionContextsCleared(session string) {
	c.contextStore.mu.Lock()
	c.contextStore.contexts = nil
	c.contextStore.seen = map[int64]bool{}
	c.contextStore.mu.Unlock()
}
