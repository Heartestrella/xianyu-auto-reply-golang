package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
)

func (s *Server) itemBatchSubPut(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		accountID := chi.URLParam(r, "accountID")
		p := map[string]any{}
		if decode(r, &p) != nil {
			fail(w, 400, "请求格式错误")
			return
		}
		rawIDs, hasIDs := p["item_ids"].([]any)
		if !hasIDs || len(rawIDs) == 0 {
			fail(w, 400, "请选择商品")
			return
		}
		config := map[string]any{}
		for field, value := range p {
			if field != "item_ids" {
				config[field] = value
			}
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		defer tx.Rollback()
		updated := 0
		for _, rawID := range rawIDs {
			itemID, _ := rawID.(string)
			var old string
			if tx.QueryRowContext(r.Context(), `SELECT data FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, u.ID, accountID, itemID).Scan(&old) != nil {
				continue
			}
			item := map[string]any{}
			_ = json.Unmarshal([]byte(old), &item)
			item[key] = config
			encoded, _ := json.Marshal(item)
			if _, err = tx.ExecContext(r.Context(), `UPDATE xy_catalog_items SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, string(encoded), u.ID, accountID, itemID); err == nil {
				updated++
			}
		}
		if err = tx.Commit(); err != nil {
			fail(w, 500, err.Error())
			return
		}
		ok(w, "批量保存成功", map[string]any{"updated_count": updated})
	}
}

func (s *Server) itemBatchSubDelete(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := current(r)
		accountID := chi.URLParam(r, "accountID")
		var p struct {
			ItemIDs []string `json:"item_ids"`
		}
		if decode(r, &p) != nil || len(p.ItemIDs) == 0 {
			fail(w, 400, "请选择商品")
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		defer tx.Rollback()
		updated := 0
		for _, itemID := range p.ItemIDs {
			var old string
			if tx.QueryRowContext(r.Context(), `SELECT data FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, u.ID, accountID, itemID).Scan(&old) != nil {
				continue
			}
			item := map[string]any{}
			_ = json.Unmarshal([]byte(old), &item)
			delete(item, key)
			encoded, _ := json.Marshal(item)
			if _, err = tx.ExecContext(r.Context(), `UPDATE xy_catalog_items SET data=?,updated_at=CURRENT_TIMESTAMP WHERE owner_id=? AND account_id=? AND item_id=?`, string(encoded), u.ID, accountID, itemID); err == nil {
				updated++
			}
		}
		if err = tx.Commit(); err != nil {
			fail(w, 500, err.Error())
			return
		}
		ok(w, "批量删除成功", map[string]any{"updated_count": updated})
	}
}

func (s *Server) itemReplyImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		fail(w, 400, "图片格式无效或文件过大")
		return
	}
	header, _, err := r.FormFile("image")
	if err != nil {
		fail(w, 400, "请选择图片")
		return
	}
	_ = header.Close()
	fileHeaders := r.MultipartForm.File["image"]
	if len(fileHeaders) == 0 {
		fail(w, 400, "请选择图片")
		return
	}
	root := filepath.Join(s.cfg.DataDir, "uploads", "item-replies")
	if err = os.MkdirAll(root, 0755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	path, url, err := saveUploadedFile(root, "item-replies", fileHeaders[0], imageTypes, 10<<20)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	write(w, 200, map[string]any{"success": true, "message": "图片上传成功", "image_url": url, "data": map[string]any{"image_url": url, "path": path}})
}

func (s *Server) itemDeletePath(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM xy_catalog_items WHERE owner_id=? AND account_id=? AND item_id=?`, u.ID, chi.URLParam(r, "accountID"), chi.URLParam(r, "itemID"))
	affected(w, res, err, "商品删除成功")
}
