package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

var (
	faceHTokenPattern      = regexp.MustCompile(`htoken=([A-Za-z0-9_-]+)`)
	faceVerifyModesPattern = regexp.MustCompile(`window\.location\.href\s*=\s*["'](https://[^"']*?/iv/mini/verify_modes\.htm\?[^"']*)["']`)
	faceQRCodePattern      = regexp.MustCompile(`new\s+Qrcode\(\{\s*text:\s*["']([^"']+)["']`)
)

func (m *qrLoginManager) runFaceVerification(s *qrLoginSession, iframeURL string) {
	fail := func(message string) {
		s.mu.Lock()
		s.Status = "failed"
		s.Error = message
		s.mu.Unlock()
	}
	if strings.TrimSpace(iframeURL) == "" {
		fail("人脸验证失败：闲鱼未返回验证地址")
		return
	}
	normalHTML, err := s.faceGet(iframeURL, "")
	if err != nil {
		fail("人脸验证页面加载失败：" + err.Error())
		return
	}
	htokenMatch := faceHTokenPattern.FindStringSubmatch(normalHTML)
	if len(htokenMatch) != 2 {
		fail("人脸验证失败：未能提取 htoken")
		return
	}
	htoken := htokenMatch[1]
	verifyMatch := faceVerifyModesPattern.FindStringSubmatch(normalHTML)
	if len(verifyMatch) != 2 {
		fail("人脸验证失败：未能提取验证模式链接")
		return
	}
	verifyURL := html.UnescapeString(verifyMatch[1])
	if strings.HasSuffix(verifyURL, "_umidfg=") {
		verifyURL += "1"
	}
	identityHTML, err := s.faceGet(verifyURL, "")
	if err != nil {
		fail("人脸验证模式加载失败：" + err.Error())
		return
	}
	qrMatch := faceQRCodePattern.FindStringSubmatch(identityHTML)
	if len(qrMatch) != 2 {
		fail("人脸验证失败：未能提取人脸二维码")
		return
	}
	qrContent := html.UnescapeString(strings.ReplaceAll(qrMatch[1], `\/`, `/`))
	png, err := qrcode.Encode(qrContent, qrcode.Low, 320)
	if err != nil {
		fail("人脸二维码生成失败：" + err.Error())
		return
	}
	s.mu.Lock()
	s.FaceQRContent = qrContent
	s.FaceQRURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	s.Error = ""
	s.mu.Unlock()

	checkURL := qrHost + "/iv/photoVerify/check.do?htoken=" + url.QueryEscape(htoken)
	referer := qrHost + "/iv/mini/identity_verify.htm?htoken=" + url.QueryEscape(htoken)
	var completionURL string
	for {
		s.mu.RLock()
		expired := time.Now().After(s.ExpiresAt)
		status := s.Status
		s.mu.RUnlock()
		if expired || status != "verification_required" {
			if expired {
				fail("人脸验证已超时，请重新扫码")
			}
			return
		}
		body, requestErr := s.faceGet(checkURL, referer)
		if requestErr == nil {
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
		time.Sleep(2 * time.Second)
	}
	if completionURL == "" {
		fail("人脸验证完成但闲鱼未返回登录地址")
		return
	}
	if _, err = s.faceGet(html.UnescapeString(completionURL), referer); err != nil {
		fail("人脸验证登录确认失败：" + err.Error())
		return
	}
	s.mu.Lock()
	s.captureCookies(nil)
	if s.UNB == "" {
		s.Status = "failed"
		s.Error = "人脸验证完成但未获取到账号 Cookie，请重新扫码"
	} else {
		s.Status = "success"
		s.Error = ""
	}
	s.mu.Unlock()
}

func (s *qrLoginSession) faceGet(target, referer string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	qrHeaders(req)
	if referer != "" {
		req.Header.Set("Referer", referer)
		req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	s.attachCookies(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	s.captureResponseCookies(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, compactResponse(body))
	}
	return string(body), nil
}
