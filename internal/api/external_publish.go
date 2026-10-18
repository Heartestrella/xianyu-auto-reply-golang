package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) externalAccountBySecret(r *http.Request, secret, accountID string) (platformAccount, int, string) {
	owner, code, msg := s.externalOwner(r, secret)
	if code != "" {
		n, _ := strconv.Atoi(code)
		return platformAccount{}, n, msg
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return platformAccount{}, 40002, "闲鱼账号ID不能为空"
	}
	var account platformAccount
	e := s.db.QueryRowContext(r.Context(), `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass FROM xy_accounts WHERE owner_id=? AND account_id=?`, owner, accountID).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass)
	if e != nil {
		return platformAccount{}, 40002, "闲鱼账号不存在或不属于该秘钥用户"
	}
	return account, 0, ""
}

func (s *Server) externalPublishMedia(w http.ResponseWriter, r *http.Request) {
	if e := r.ParseMultipartForm(64 << 20); e != nil {
		externalWrite(w, false, 40007, "上传内容无效", nil)
		return
	}
	account, code, msg := s.externalAccountBySecret(r, r.FormValue("secret_key"), r.FormValue("account_id"))
	if code != 0 {
		externalWrite(w, false, code, msg, nil)
		return
	}
	typ := strings.TrimSpace(r.FormValue("media_type"))
	if typ != "image" && typ != "spec_image" && typ != "video" {
		externalWrite(w, false, 40007, "media_type必须为image、spec_image或video", nil)
		return
	}
	file, header, e := r.FormFile("file")
	if e != nil || header.Filename == "" {
		externalWrite(w, false, 40007, "请选择要上传的媒体文件", nil)
		return
	}
	defer file.Close()
	max := int64(20 << 20)
	if typ == "video" {
		max = 200 << 20
	}
	limited := io.LimitReader(file, max+1)
	content, e := io.ReadAll(limited)
	if e != nil || int64(len(content)) > max {
		externalWrite(w, false, 40007, "媒体文件过大", nil)
		return
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	allowed := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}
	if typ == "video" {
		allowed = map[string]bool{".mp4": true, ".mov": true, ".webm": true}
	}
	if !allowed[ext] {
		externalWrite(w, false, 40007, "媒体文件格式不支持", nil)
		return
	}
	mediaID := uuid.NewString()
	directory := filepath.Join(s.cfg.DataDir, "uploads", "external-publish", account.AccountID)
	if e = os.MkdirAll(directory, 0755); e != nil {
		externalWrite(w, false, 50001, "保存媒体失败", nil)
		return
	}
	name := mediaID + ext
	path := filepath.Join(directory, name)
	if e = os.WriteFile(path, content, 0644); e != nil {
		externalWrite(w, false, 50001, "保存媒体失败", nil)
		return
	}
	publicURL := "/uploads/external-publish/" + account.AccountID + "/" + name
	raw, _ := json.Marshal(map[string]any{"media_id": mediaID, "media_type": typ, "path": path, "url": publicURL, "filename": header.Filename, "size": len(content)})
	_, e = s.db.ExecContext(r.Context(), `INSERT INTO resource_records(owner_id,kind,account_id,data) VALUES(?,'external_publish_media',?,?)`, account.OwnerID, account.AccountID, string(raw))
	if e != nil {
		_ = os.Remove(path)
		externalWrite(w, false, 50001, "保存媒体失败", nil)
		return
	}
	externalWrite(w, true, 200, "媒体上传成功", map[string]any{"media_id": mediaID, "media_type": typ, "url": publicURL, "file_name": header.Filename, "size": len(content)})
}

func (s *Server) resolveExternalMedia(r *http.Request, account platformAccount, ids []string, expected string) ([]string, string) {
	out := []string{}
	for _, id := range ids {
		var raw string
		e := s.db.QueryRowContext(r.Context(), `SELECT data FROM resource_records WHERE owner_id=? AND account_id=? AND kind='external_publish_media' AND json_extract(data,'$.media_id')=? ORDER BY id DESC LIMIT 1`, account.OwnerID, account.AccountID, id).Scan(&raw)
		if e != nil {
			return nil, "媒体ID不存在或不属于指定账号: " + id
		}
		var data map[string]any
		if json.Unmarshal([]byte(raw), &data) != nil {
			return nil, "媒体记录无效: " + id
		}
		typ := fmt.Sprint(data["media_type"])
		if expected == "image" && typ != "image" && typ != "spec_image" {
			return nil, "媒体类型不匹配: " + id
		}
		if expected == "video" && typ != "video" {
			return nil, "媒体类型不匹配: " + id
		}
		path := fmt.Sprint(data["path"])
		if _, e = os.Stat(path); e != nil {
			return nil, "媒体文件不存在: " + id
		}
		out = append(out, path)
	}
	return out, ""
}

func (s *Server) externalPublishSingle(w http.ResponseWriter, r *http.Request) {
	var p map[string]any
	if decode(r, &p) != nil {
		externalWrite(w, false, 40008, "请求参数不能为空", nil)
		return
	}
	account, code, msg := s.externalAccountBySecret(r, fmt.Sprint(p["secret_key"]), fmt.Sprint(p["account_id"]))
	if code != 0 {
		externalWrite(w, false, code, msg, nil)
		return
	}
	title := strings.TrimSpace(fmt.Sprint(p["title"]))
	description := strings.TrimSpace(fmt.Sprint(p["description"]))
	if title == "" {
		externalWrite(w, false, 40008, "商品标题不能为空", nil)
		return
	}
	if description == "" {
		externalWrite(w, false, 40008, "商品描述不能为空", nil)
		return
	}
	if p["price"] == nil {
		externalWrite(w, false, 40008, "商品售价不能为空", nil)
		return
	}
	if strings.TrimSpace(fmt.Sprint(p["address"])) == "" {
		externalWrite(w, false, 40008, "宝贝所在地不能为空，请传入外部系统已选择的地址关键词", nil)
		return
	}
	imageIDs := stringSlice(p["image_media_ids"])
	if len(imageIDs) == 0 || len(imageIDs) > 9 {
		externalWrite(w, false, 40008, "商品图片数量必须为1到9张", nil)
		return
	}
	images, mediaErr := s.resolveExternalMedia(r, account, imageIDs, "image")
	if mediaErr != "" {
		externalWrite(w, false, 40007, mediaErr, nil)
		return
	}
	videoIDs := stringSlice(p["video_media_ids"])
	videos, mediaErr := s.resolveExternalMedia(r, account, videoIDs, "video")
	if mediaErr != "" {
		externalWrite(w, false, 40007, mediaErr, nil)
		return
	}
	p["images"] = images
	p["videos"] = videos
	p["account_id"] = account.AccountID
	delete(p, "secret_key")
	delete(p, "image_media_ids")
	delete(p, "video_media_ids")
	result := s.executePublish(r.Context(), account.OwnerID, account.AccountID, 0, "", p)
	if result["success"] != true {
		externalWrite(w, false, 40009, fmt.Sprint(result["message"]), nil)
		return
	}
	data, _ := result["data"].(map[string]any)
	externalWrite(w, true, 200, fmt.Sprint(result["message"]), map[string]any{"item_url": data["item_url"], "item_id": data["item_id"], "log_id": nil, "sync_status": nil, "sync_message": nil, "sync_total_count": nil, "sync_saved_count": nil})
}
