package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

const (
	videoBizCode    = "s_upload_xy_shequ"
	videoAppKey     = "12574478"
	videoSDKVersion = "0.6.3"
	videoUserSite   = 77
)

// uploadPublishVideos implements the seller console's init -> OSS multipart ->
// complete protocol. The optional second-transfer optimisation is intentionally
// skipped: normal multipart upload is authoritative and works for every file.
func uploadPublishVideos(ctx context.Context, videos []map[string]any, account platformAccount) ([]map[string]any, string, error) {
	out := make([]map[string]any, 0, len(videos))
	for i, video := range videos {
		if i == 3 {
			break
		}
		payload, cookie, err := uploadPublishVideo(ctx, video, account)
		if err != nil {
			return nil, account.Cookie, fmt.Errorf("第 %d 个视频上传失败：%w", i+1, err)
		}
		account.Cookie = cookie
		payload["major"] = i == 0
		out = append(out, payload)
	}
	return out, account.Cookie, nil
}

func uploadPublishVideo(ctx context.Context, video map[string]any, account platformAccount) (map[string]any, string, error) {
	content, name, mimeType, err := readPublishVideo(ctx, video)
	if err != nil {
		return nil, account.Cookie, err
	}
	width, height := publishIntValue(video["width"]), publishIntValue(video["height"])
	if width == 0 || height == 0 {
		width, height = mp4Dimensions(content)
	}
	config, err := fetchVideoConfig(ctx, account)
	if err != nil {
		return nil, account.Cookie, err
	}
	apiInfo := childMap(config, "apiInfo")
	initAPI := firstNonemptyAPI(textValue(apiInfo["init"]), "mtop.video.upload.init.xianyu")
	init := videoMTOP(ctx, account, initAPI, http.MethodGet, map[string]any{"bizCode": videoBizCode, "fileSize": len(content), "mimeType": mimeType, "localFileName": name, "sdkVersion": videoSDKVersion, "userSite": videoUserSite})
	if !init.Success {
		return nil, init.Cookie, fmt.Errorf("视频上传初始化失败：%s", init.Error)
	}
	account.Cookie = firstNonemptyAPI(init.Cookie, account.Cookie)
	model := videoModel(init.Response)
	strategy := mapCopy(model)
	for k, v := range childMap(model, "uploadStrategy") {
		strategy[k] = v
	}
	uploadID := textValue(strategy["uploadId"])
	uploadURLs := anySlice(strategy["uploadUrlList"])
	if uploadID == "" || len(uploadURLs) == 0 {
		return nil, account.Cookie, errors.New("视频上传初始化未返回 uploadId 或分片地址")
	}
	policy := childMap(strategy, "videoUploadPolicy")
	sliceSize := parseVideoSliceSize(policy["sliceSize"])
	chunkCount := (len(content) + sliceSize - 1) / sliceSize
	if chunkCount > len(uploadURLs) {
		return nil, account.Cookie, fmt.Errorf("视频分片数量超过授权上限：需要 %d 片，仅返回 %d 个地址", chunkCount, len(uploadURLs))
	}
	started := time.Now()
	parts := make([]string, 0, chunkCount)
	for i := 0; i < chunkCount; i++ {
		end := (i + 1) * sliceSize
		if end > len(content) {
			end = len(content)
		}
		entry := uploadURLs[i]
		partURL := textValue(entry)
		if m, ok := entry.(map[string]any); ok {
			partURL = textValue(m["url"])
		}
		sum, putErr := putVideoPart(ctx, partURL, content[i*sliceSize:end], account)
		if putErr != nil {
			return nil, account.Cookie, fmt.Errorf("第 %d 个视频分片上传失败：%w", i+1, putErr)
		}
		raw, _ := json.Marshal(map[string]any{"partNumber": i + 1, "md5": sum})
		parts = append(parts, string(raw))
	}
	partJSON, _ := json.Marshal(parts)
	netSpeed := float64(len(content)) / 1024 / time.Since(started).Seconds()
	completeAPI := firstNonemptyAPI(textValue(apiInfo["complete"]), "mtop.video.upload.complete.xianyu")
	complete := videoMTOP(ctx, account, completeAPI, http.MethodPost, map[string]any{"bizCode": videoBizCode, "uploadId": uploadID, "partList": string(partJSON), "netSpeed": netSpeed, "sdkVersion": videoSDKVersion, "userSite": videoUserSite})
	if !complete.Success {
		return nil, complete.Cookie, fmt.Errorf("视频上传完成接口失败：%s", complete.Error)
	}
	account.Cookie = firstNonemptyAPI(complete.Cookie, account.Cookie)
	done := videoModel(complete.Response)
	fileID, ossURL := textValue(done["fileId"]), textValue(done["ossUrl"])
	if fileID == "" || ossURL == "" {
		return nil, account.Cookie, errors.New("视频上传完成接口未返回 fileId 或 ossUrl")
	}
	poster := firstNonemptyAPI(textValue(video["thumbnail_url"]), textValue(video["cover_url"]))
	if !strings.HasPrefix(poster, "http") {
		poster = ossURL
	}
	return map[string]any{"heightSize": height, "widthSize": width, "major": true, "mediaCloudFileId": fileID, "type": 10000, "url": poster, "videoMD5": "0000", "videoObject": "", "videoUrl": ossURL}, account.Cookie, nil
}

func fetchVideoConfig(ctx context.Context, account platformAccount) (map[string]any, error) {
	q := url.Values{"bizCode": {videoBizCode}, "userSite": {strconv.Itoa(videoUserSite)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://upload.media.aliyun.com/api/gateway/bizConfig?"+q.Encode(), nil)
	req.Header.Set("Cookie", account.Cookie)
	req.Header.Set("Origin", sellerOrigin)
	req.Header.Set("Referer", sellerOrigin+"/?site=COMMONPRO")
	client := publishHTTPClient(account, 90*time.Second)
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("获取视频上传配置失败：HTTP %d", resp.StatusCode)
	}
	var body map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body) != nil {
		return nil, errors.New("视频上传配置不是有效JSON")
	}
	data, ok := body["data"].(map[string]any)
	if !ok || len(childMap(data, "apiInfo")) == 0 {
		return nil, errors.New("视频上传配置缺少 API 映射")
	}
	return data, nil
}
func videoMTOP(ctx context.Context, a platformAccount, api, method string, data map[string]any) xianyu.MTOPResult {
	return xianyu.MTOPCall(ctx, xianyu.MTOPOptions{API: api, Version: "1.0", AppKey: videoAppKey, Cookie: a.Cookie, Proxy: accountProxy(a), Origin: sellerOrigin, Referer: sellerOrigin + "/?site=COMMONPRO", Method: method, Data: data, ExtraParams: map[string]string{"preventFallback": "true", "dataType": "jsonp"}, ExtraHeaders: map[string]string{"idle_site_biz_code": "COMMONPRO"}})
}
func videoModel(res map[string]any) map[string]any {
	data := childMap(res, "data")
	if model := childMap(data, "model"); len(model) > 0 {
		return model
	}
	if model := childMap(res, "model"); len(model) > 0 {
		return model
	}
	return data
}
func putVideoPart(ctx context.Context, rawURL string, content []byte, a platformAccount) (string, error) {
	if rawURL == "" {
		return "", errors.New("分片地址为空")
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(content))
		req.Header.Set("Origin", sellerOrigin)
		req.Header.Set("Referer", sellerOrigin+"/?site=COMMONPRO")
		resp, e := publishHTTPClient(a, 90*time.Second).Do(req)
		if e == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				etag := strings.Trim(resp.Header.Get("ETag"), "\"")
				if etag != "" {
					return etag, nil
				}
				sum := md5.Sum(content)
				return strings.ToUpper(hex.EncodeToString(sum[:])), nil
			}
			e = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		last = e
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	return "", last
}
func readPublishVideo(ctx context.Context, v map[string]any) ([]byte, string, string, error) {
	source := firstNonemptyAPI(textValue(v["path"]), textValue(v["url"]))
	if source == "" {
		return nil, "", "", errors.New("视频缺少本地路径或视频地址")
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return nil, "", "", e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, "", "", fmt.Errorf("远程视频下载失败：HTTP %d", resp.StatusCode)
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, 210<<20))
		name := filepath.Base(req.URL.Path)
		if name == "" {
			name = "publish-video.mp4"
		}
		mime := strings.Split(resp.Header.Get("Content-Type"), ";")[0]
		if mime == "" {
			mime = "video/mp4"
		}
		return b, name, mime, e
	}
	b, e := os.ReadFile(source)
	if e != nil {
		return nil, "", "", fmt.Errorf("视频文件不存在：%s", source)
	}
	mime := "video/mp4"
	if strings.EqualFold(filepath.Ext(source), ".mov") {
		mime = "video/quicktime"
	}
	if strings.EqualFold(filepath.Ext(source), ".webm") {
		mime = "video/webm"
	}
	return b, filepath.Base(source), mime, nil
}
func parseVideoSliceSize(v any) int {
	raw := strings.ToLower(textValue(v))
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "mb"))
	n, e := strconv.ParseFloat(raw, 64)
	if e != nil || n <= 0 {
		return 2 << 20
	}
	size := int(n * 1024 * 1024)
	if size < 256<<10 {
		return 256 << 10
	}
	return size
}
func mp4Dimensions(b []byte) (int, int) {
	for offset := 0; offset+96 < len(b); {
		size := int(uint32(b[offset])<<24 | uint32(b[offset+1])<<16 | uint32(b[offset+2])<<8 | uint32(b[offset+3]))
		if size < 8 || offset+size > len(b) {
			break
		}
		if string(b[offset+4:offset+8]) == "tkhd" {
			version := b[offset+8]
			pos := offset + 84
			if version == 1 {
				pos = offset + 96
			}
			if pos+8 <= offset+size {
				w := int(uint32(b[pos])<<24|uint32(b[pos+1])<<16|uint32(b[pos+2])<<8|uint32(b[pos+3])) >> 16
				h := int(uint32(b[pos+4])<<24|uint32(b[pos+5])<<16|uint32(b[pos+6])<<8|uint32(b[pos+7])) >> 16
				if w > 0 && h > 0 {
					return w, h
				}
			}
		}
		offset += size
	}
	return 0, 0
}
func mapCopy(src map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range src {
		out[k] = v
	}
	return out
}
func publishHTTPClient(a platformAccount, timeout time.Duration) *http.Client {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if p := accountProxy(a); p != "" {
		if u, e := url.Parse(p); e == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}
