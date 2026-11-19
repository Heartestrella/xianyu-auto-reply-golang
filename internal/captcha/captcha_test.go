package captcha

import (
	"math/rand"
	"testing"
)

func TestGenerateTrajectoryShape(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for _, distance := range []float64{60, 180, 258} {
		trajectory := GenerateTrajectory(distance, r)
		if len(trajectory) != 20 {
			t.Fatalf("distance=%.0f: 控制点数量应为 20，实际 %d", distance, len(trajectory))
		}
		prevX := -1.0
		var total float64
		for i, p := range trajectory {
			if p.X < prevX {
				t.Fatalf("distance=%.0f: 第 %d 个点 X 回退（%.3f < %.3f）", distance, i, p.X, prevX)
			}
			prevX = p.X
			total += p.Delay.Seconds()
		}
		// 总时长应在 0.40~0.48s 区间（允许毫秒级取整误差）。
		if total < 0.38 || total > 0.50 {
			t.Fatalf("distance=%.0f: 总时长 %.3fs 超出父仓库参数范围", distance, total)
		}
		last := trajectory[len(trajectory)-1]
		if distance >= 180 {
			// 普通滑块需越过轨道终点 45%~55%。
			if last.X < distance*1.40 || last.X > distance*1.60 {
				t.Fatalf("distance=%.0f: 光标落点 %.1f 未落在越界区间", distance, last.X)
			}
		} else if last.X < distance || last.X > distance*1.02 {
			t.Fatalf("小距离应精确落点，实际 %.1f", last.X)
		}
	}
}

func TestScratchDistanceRange(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 20; i++ {
		d := ScratchDistance(258, r)
		if d < 258*0.25 || d > 258*0.35 {
			t.Fatalf("刮刮乐距离 %.2f 超出 25%%~35%% 区间", d)
		}
	}
}

func TestMergeCookies(t *testing.T) {
	base := "unb=123; _m_h5_tk=abc_1; cookie2=xyz"
	merged := MergeCookies(base, map[string]string{"x5sec": "new", "bx-pp": "pp"})
	// 原有 Cookie 的键顺序必须保留；新增键按字典序追加，结果可复现。
	want := "unb=123; _m_h5_tk=abc_1; cookie2=xyz; bx-pp=pp; x5sec=new"
	if merged != want {
		t.Fatalf("合并结果不符合预期:\n got=%s\nwant=%s", merged, want)
	}
	// 重复调用必须得到完全一致的结果（map 迭代顺序随机不应影响输出）。
	for i := 0; i < 20; i++ {
		if again := MergeCookies(base, map[string]string{"x5sec": "new", "bx-pp": "pp"}); again != want {
			t.Fatalf("第 %d 次合并结果不稳定:\n got=%s\nwant=%s", i+1, again, want)
		}
	}
	// 覆盖已有 x5sec 时应保留顺序并更新值。
	merged = MergeCookies("x5sec=old; unb=1", map[string]string{"x5sec": "fresh"})
	if merged != "x5sec=fresh; unb=1" {
		t.Fatalf("覆盖合并结果错误: %s", merged)
	}
}

func TestFilterX5RequiresX5Sec(t *testing.T) {
	all := map[string]string{"unb": "1", "x5sec": "v", "x5secdata": "d", "cookie2": "c", "bx-pp": "p", "X5STEP": "2"}
	out := filterX5(all)
	if out["x5sec"] != "v" || out["x5secdata"] != "d" || out["X5STEP"] != "2" {
		t.Fatalf("x5 过滤结果错误: %#v", out)
	}
	if _, ok := out["unb"]; ok {
		t.Fatalf("不应包含非 x5 cookie: %#v", out)
	}
	if _, ok := out["bx-pp"]; ok {
		t.Fatalf("不应包含非 x5 cookie: %#v", out)
	}
}

func TestIsPunishURL(t *testing.T) {
	cases := map[string]bool{
		"https://www.goofish.com/punish?x5secdata=abc": true,
		"https://www.goofish.com/?x5step=2":            true,
		"https://www.goofish.com/?action=captcha":      true,
		"https://www.goofish.com/iv/pureCaptcha":       true,
		"https://www.goofish.com/":                     false,
	}
	for url, want := range cases {
		if got := isPunishURL(url); got != want {
			t.Fatalf("isPunishURL(%q)=%v, want %v", url, got, want)
		}
	}
}

func TestParseCookiePairs(t *testing.T) {
	pairs := parseCookiePairs("a=1; b=2=3;; c=;\n d=4")
	if pairs["a"] != "1" || pairs["b"] != "2=3" || pairs["d"] != "4" {
		t.Fatalf("Cookie 解析异常: %#v", pairs)
	}
}
