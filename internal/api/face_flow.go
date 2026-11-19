package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// passportHost 是闲鱼/淘系登录域。
const passportHost = "https://passport.goofish.com"

// faceFlowHeaders 与父仓库 face_verification.py 的 DEFAULT_HEADERS 保持一致。
var faceFlowHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Accept":          "application/json, text/plain, */*",
	"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	"Referer":         passportHost + "/",
	"Origin":          passportHost,
}

// FaceFlowResult 是纯 API 人脸验证链路的结果。
type FaceFlowResult struct {
	Cookies map[string]string
	UNB     string
}

// runFaceFlow 以纯 API 方式复现闲鱼人脸验证链路（移植自父仓库
// common/services/xianyu_login/face_verification.py）：
//
//  1. 跟随 iframeRedirectUrl 落到 normal_validate.htm，提取 htoken 与
//     verify_modes.htm 下一跳链接；
//  2. 请求 verify_modes.htm（跟随 302）落到 identity_verify.htm，提取人脸
//     验证二维码内容，渲染成 data-url 回调给前端展示；
//  3. 轮询 photoVerify/check.do 直到用户手机完成人脸（code=3）；
//  4. 跟随返回的 ivCheckLogin.htm 完成登录，收集 Cookie 与 unb。
func runFaceFlow(
	ctx context.Context,
	client *http.Client,
	jar *cookiejar.Jar,
	iframeURL string,
	onQRReady func(dataURL string),
	shouldContinue func() bool,
) (*FaceFlowResult, error) {
	if strings.TrimSpace(iframeURL) == "" {
		return nil, fmt.Errorf("人脸验证：闲鱼未返回验证地址")
	}
	normalHTML, err := faceFlowGet(ctx, client, iframeURL, "")
	if err != nil {
		return nil, fmt.Errorf("人脸验证：加载验证页失败: %w", err)
	}
	htokenMatch := faceHTokenPattern.FindStringSubmatch(normalHTML)
	if len(htokenMatch) != 2 {
		return nil, fmt.Errorf("人脸验证：未能提取 htoken")
	}
	htoken := htokenMatch[1]
	verifyMatch := faceVerifyModesPattern.FindStringSubmatch(normalHTML)
	if len(verifyMatch) != 2 {
		return nil, fmt.Errorf("人脸验证：未能提取 verify_modes 链接")
	}
	verifyURL := html.UnescapeString(verifyMatch[1])
	// 页面里链接以 _umidfg= 结尾，后接 JS 变量 window._iv_umidfg(值为 1)。
	if strings.HasSuffix(verifyURL, "_umidfg=") {
		verifyURL += "1"
	}
	identityHTML, err := faceFlowGet(ctx, client, verifyURL, "")
	if err != nil {
		return nil, fmt.Errorf("人脸验证：加载验证模式失败: %w", err)
	}
	qrMatch := faceQRCodePattern.FindStringSubmatch(identityHTML)
	if len(qrMatch) != 2 {
		return nil, fmt.Errorf("人脸验证：未能提取人脸验证二维码")
	}
	qrContent := html.UnescapeString(strings.ReplaceAll(qrMatch[1], `\/`, `/`))
	if onQRReady != nil {
		png, qrErr := qrcode.Encode(qrContent, qrcode.Low, 320)
		if qrErr != nil {
			return nil, fmt.Errorf("人脸验证：二维码生成失败: %w", qrErr)
		}
		onQRReady("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
	}

	checkURL := passportHost + "/iv/photoVerify/check.do?htoken=" + url.QueryEscape(htoken)
	referer := passportHost + "/iv/mini/identity_verify.htm?htoken=" + url.QueryEscape(htoken)
	var completionURL string
	for {
		if shouldContinue != nil && !shouldContinue() {
			return nil, fmt.Errorf("人脸验证已超时或已取消")
		}
		body, reqErr := faceFlowGet(ctx, client, checkURL, referer)
		if reqErr == nil {
			var result struct {
				Content struct {
					Code any    `json:"code"`
					URL  string `json:"url"`
				} `json:"content"`
			}
			if json.Unmarshal([]byte(body), &result) == nil && fmt.Sprint(result.Content.Code) == "3" {
				completionURL = result.Content.URL
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("人脸验证已超时或已取消")
		case <-time.After(2 * time.Second):
		}
	}
	if strings.TrimSpace(completionURL) == "" {
		return nil, fmt.Errorf("人脸验证完成但闲鱼未返回登录地址")
	}
	if _, err = faceFlowGet(ctx, client, html.UnescapeString(completionURL), referer); err != nil {
		return nil, fmt.Errorf("人脸验证登录确认失败: %w", err)
	}

	res := &FaceFlowResult{Cookies: map[string]string{}}
	for _, raw := range []string{"https://www.goofish.com/", passportHost + "/", "https://h5api.m.goofish.com/"} {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			continue
		}
		for _, c := range jar.Cookies(u) {
			res.Cookies[c.Name] = c.Value
			if c.Name == "unb" {
				res.UNB = c.Value
			}
		}
	}
	if res.UNB == "" {
		return nil, fmt.Errorf("人脸验证完成但未获取到 unb")
	}
	return res, nil
}

func faceFlowGet(ctx context.Context, client *http.Client, target, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	for k, v := range faceFlowHeaders {
		req.Header.Set(k, v)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
		req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}
