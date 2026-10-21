package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func cardImages(data map[string]any) map[string]bool {
	out := map[string]bool{}
	if value, ok := data["image_url"].(string); ok && value != "" {
		out[value] = true
	}
	value := data["image_urls"]
	if text, ok := value.(string); ok {
		_ = json.Unmarshal([]byte(text), &value)
	}
	if values, ok := value.([]any); ok {
		for _, v := range values {
			if text, ok := v.(string); ok && text != "" {
				out[text] = true
			}
		}
	}
	return out
}

func (s *Server) updateCardData(ctx context.Context, owner int64, id string, patch map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT data FROM cards WHERE user_id=? AND id=?`, owner, id).Scan(&raw); err != nil {
		return err
	}
	data := map[string]any{}
	if err = json.Unmarshal([]byte(raw), &data); err != nil {
		return err
	}
	oldImages := cardImages(data)
	for key, value := range patch {
		data[key] = value
	}
	// The array is authoritative when supplied; clear the legacy single-image
	// field as well so reopening a card cannot bring back its deleted image.
	if images, ok := patch["image_urls"]; ok {
		data["image_url"] = ""
		if values, ok := images.([]any); ok && len(values) > 0 {
			data["image_url"] = values[0]
		}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cards SET name=COALESCE(?,name),type=COALESCE(?,type),data=?,updated_at=CURRENT_TIMESTAMP WHERE user_id=? AND id=?`, patch["name"], patch["type"], string(encoded), owner, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	current := cardImages(data)
	for image := range oldImages {
		if !current[image] {
			s.removeUnusedCardImage(ctx, image)
		}
	}
	return nil
}

func (s *Server) removeUnusedCardImage(ctx context.Context, image string) {
	// Only generated files directly inside uploads/cards may be removed.
	imagePath := strings.TrimPrefix(image, "/static")
	if !strings.HasPrefix(imagePath, "/uploads/cards/") {
		return
	}
	name := strings.TrimPrefix(imagePath, "/uploads/cards/")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\%?#") {
		return
	}
	root := filepath.Join(s.cfg.DataDir, "uploads", "cards")
	for _, dir := range []string{filepath.Join(s.cfg.DataDir, "uploads"), root} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	// A card image can be reused by another card or pasted into other content.
	for _, table := range []string{"cards", "resource_records", "xy_catalog_items", "product_materials", "default_replies", "xy_orders"} {
		var references int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE instr(data,?)>0 OR instr(data,?)>0`, image, imagePath).Scan(&references); err != nil || references > 0 {
			return
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) && s.log != nil {
		s.log.Warn("remove unused card image", "error", err)
	}
}
