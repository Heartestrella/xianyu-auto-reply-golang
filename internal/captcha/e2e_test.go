package captcha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const mockSliderPage = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>mock</title>
<style>
#nc_1_n1t{position:relative;width:258px;height:34px;background:#e8e8e8;margin:80px auto;border-radius:4px}
#nc_1_n1z{position:absolute;left:0;top:0;width:40px;height:34px;background:#555;border-radius:4px;cursor:pointer}
</style></head><body>
<div id="nc_1_n1t" class="nc_scale"><div id="nc_1_n1z"></div></div>
<script>
(function(){
  var btn=document.getElementById('nc_1_n1z'), track=document.getElementById('nc_1_n1t');
  var dragging=false, startX=0, left=0;
  btn.addEventListener('mousedown', function(e){ dragging=true; startX=e.clientX; });
  window.addEventListener('mousemove', function(e){
    if(!dragging) return;
    left=Math.max(0, Math.min(e.clientX-startX, 218));
    btn.style.left=left+'px';
  });
  window.addEventListener('mouseup', function(){
    if(!dragging) return; dragging=false;
    if(left>=210){
      document.cookie='x5sec=mock-pass-'+Date.now()+'; path=/';
      track.style.visibility='hidden';
      document.title='passed';
    }
  });
})();
</script></body></html>`

// mockExpiredPage 复现父仓库的"链接已过期"页面（固定文案）。
const mockExpiredPage = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>expired</title></head>
<body><div>抱歉，页面访问出现了问题</div></body></html>`

// TestSolveAgainstExpiredURL 验证 URLEXpiredError 判定与调用方可识别。
func TestSolveAgainstExpiredURL(t *testing.T) {
	if os.Getenv("CAPTCHA_E2E") != "1" {
		t.Skip("设置 CAPTCHA_E2E=1 才运行真实浏览器端到端用例")
	}
	executable, err := findChrome(os.Getenv("CAPTCHA_SLIDER_CHROME_PATH"))
	if err != nil {
		t.Skipf("本机没有浏览器，跳过: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mockExpiredPage))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	solver := NewSolver(Config{Enabled: true, ChromePath: executable, Headless: true, Timeout: 25 * time.Second, MaxRetries: 3})
	_, err = solver.Solve(ctx, server.URL+"/punish?x5secdata=expired", "")
	var expired URLEXpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("过期页面应返回 URLEXpiredError，实际: %v", err)
	}
}

// TestURLEXpiredText 确认过期文案常量与父仓库一致。
func TestURLEXpiredText(t *testing.T) {
	if urlExpiredText != "抱歉，页面访问出现了问题" {
		t.Fatalf("过期文案常量被改动: %q", urlExpiredText)
	}
}

// TestSuccessMarkersJS 确认成功标志选择器与父仓库 SUCCESS_SELECTORS 一致。
func TestSuccessMarkersJS(t *testing.T) {
	for _, selector := range []string{".nc_ok_icon", ".nc-lang-cnt .nc_ok", "#nc_1_n1z.nc_ok"} {
		if !strings.Contains(successMarkersJS, selector) {
			t.Fatalf("成功标志选择器缺失: %s", selector)
		}
	}
}

// mockFrameHost 把滑块页放在"另一个源"上（不同端口），用 iframe 嵌进主文档，
// 复现跨源 baxia iframe 场景。滑块通过后 frame postMessage 通知父页面，
// 父页面跳转离开 punish 路径并写 x5sec，模拟真实 punish 页放行后的
// 302 跳转与 Set-Cookie。
const mockFramePage = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>frame-host</title>
<style>iframe{border:0;margin:60px auto;display:block}</style></head><body>
<iframe id="baxia-dialog-content" width="340" height="120"></iframe>
<script>
document.querySelector('iframe').src = window.__FRAME_SRC__;
window.addEventListener('message', function(ev){
  if (ev.data && ev.data.type === 'slider-passed') {
    document.cookie = 'x5sec=' + ev.data.token + '; path=/';
    setTimeout(function(){ window.location.href = '/passed'; }, 300);
  }
});
</script>
</body></html>`

// TestSolveAgainstCrossOriginFrame 验证跨源 iframe 里的滑块也能找到并滑过。
func TestSolveAgainstCrossOriginFrame(t *testing.T) {
	if os.Getenv("CAPTCHA_E2E") != "1" {
		t.Skip("设置 CAPTCHA_E2E=1 才运行真实浏览器端到端用例")
	}
	executable, err := findChrome(os.Getenv("CAPTCHA_SLIDER_CHROME_PATH"))
	if err != nil {
		t.Skipf("本机没有浏览器，跳过: %v", err)
	}
	// 滑块页（子源）：成功后向 document.cookie 写 x5sec 并通知父页面。
	frameServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.Replace(mockSliderPage, "document.title='passed';", "document.title='passed'; parent.postMessage({type:'slider-passed',token:document.cookie.split('=').pop()},'*');", 1)))
	}))
	defer frameServer.Close()
	// 主文档（父源）：iframe 指向滑块页；/passed 表示已离开 punish 路径。
	hostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/passed":
			_, _ = w.Write([]byte("<html><body>ok</body></html>"))
		default:
			page := strings.Replace(mockFramePage, "window.__FRAME_SRC__", "'"+frameServer.URL+"/verify.htm'", 1)
			_, _ = w.Write([]byte(page))
		}
	}))
	defer hostServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	solver := NewSolver(Config{Enabled: true, ChromePath: executable, Headless: true, Timeout: 45 * time.Second, MaxRetries: 3})
	result, err := solver.Solve(ctx, hostServer.URL+"/punish?x5secdata=frame", "")
	if err != nil {
		t.Fatalf("跨源 iframe 端到端求解失败: %v", err)
	}
	if result.Cookies["x5sec"] == "" {
		t.Fatalf("未取到 x5sec: %#v", result.Cookies)
	}
	t.Logf("跨源 iframe 端到端通过：attempts=%d x5sec=%s", result.Attempts, result.Cookies["x5sec"])
}

// TestSolveAgainstMockSlider 是端到端用例：只有显式设置 CAPTCHA_E2E=1
// 且本机存在 Chrome/Chromium 时才执行（CI 环境默认跳过）。
func TestSolveAgainstMockSlider(t *testing.T) {
	if os.Getenv("CAPTCHA_E2E") != "1" {
		t.Skip("设置 CAPTCHA_E2E=1 才运行真实浏览器端到端用例")
	}
	executable, err := findChrome(os.Getenv("CAPTCHA_SLIDER_CHROME_PATH"))
	if err != nil {
		t.Skipf("本机没有浏览器，跳过: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mockSliderPage))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	solver := NewSolver(Config{Enabled: true, ChromePath: executable, Headless: true, Timeout: 45 * time.Second, MaxRetries: 3})
	result, err := solver.Solve(ctx, server.URL+"/verify.htm", "")
	if err != nil {
		t.Fatalf("端到端求解失败: %v", err)
	}
	if result.Cookies["x5sec"] == "" {
		t.Fatalf("未取到 x5sec: %#v", result.Cookies)
	}
	t.Logf("端到端通过：attempts=%d engine=%s x5sec=%s", result.Attempts, result.Engine, result.Cookies["x5sec"])
}
