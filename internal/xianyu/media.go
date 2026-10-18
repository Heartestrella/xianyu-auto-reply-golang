package xianyu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

func UploadChatImage(ctx context.Context, cookie, proxy, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "img_"+fmt.Sprint(time.Now().UnixNano())+filepath.Ext(filename))
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stream-upload.goofish.com/api/upload.api?floderId=0&appkey=xy_chat&_input_charset=utf-8", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Cookie", strings.ReplaceAll(strings.ReplaceAll(cookie, "\r", ""), "\n", ""))
	req.Header.Set("Referer", "https://www.goofish.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if proxy != "" {
		if parsed, e := url.Parse(proxy); e == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: transport}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("闲鱼图片上传失败: HTTP %d", resp.StatusCode)
	}
	if bytes.Contains(bytes.ToLower(raw), []byte("<html")) {
		return "", errors.New("闲鱼图片上传失败：Cookie已失效")
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return "", errors.New("闲鱼图片上传响应解析失败")
	}
	candidates := []any{out["url"]}
	for _, key := range []string{"data", "object", "result"} {
		if m, ok := out[key].(map[string]any); ok {
			candidates = append(candidates, m["url"], m["fileUrl"], m["file_url"])
		}
	}
	for _, v := range candidates {
		if value := strings.TrimSpace(fmt.Sprint(v)); value != "" && value != "<nil>" {
			return value, nil
		}
	}
	return "", errors.New("闲鱼图片上传响应缺少图片地址")
}
