package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhinianboke/xianyu-auto-go/internal/config"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

func TestCardImageRemoval(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "cards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := filepath.Join(dir, "uploads", "cards")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"removed.png", "shared.png", "keep.png"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("image"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO cards(id,user_id,name,type,data) VALUES
	(1,1,'mine','image','{"image_url":"/uploads/cards/removed.png","image_urls":["/uploads/cards/removed.png","/uploads/cards/shared.png"],"delivery_count":3}'),
	(2,2,'other','image','{"image_urls":["/uploads/cards/shared.png"]}')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, cfg: config.Config{DataDir: dir}}
	if err := s.updateCardData(context.Background(), 2, "1", map[string]any{"image_urls": []any{}}); err == nil {
		t.Fatal("other owner updated card")
	}
	if err := s.updateCardData(context.Background(), 1, "1", map[string]any{"image_urls": []any{}}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM cards WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	if len(cardImages(data)) != 0 || data["delivery_count"] != float64(3) {
		t.Fatalf("bad persisted data: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(root, "removed.png")); !os.IsNotExist(err) {
		t.Fatalf("removed image remains: %v", err)
	}
	for _, path := range []string{"/uploads/cards/../cards/keep.png", "/uploads/cards/%2e%2e/keep.png", "https://example.com/uploads/cards/keep.png"} {
		s.removeUnusedCardImage(context.Background(), path)
	}
	for _, name := range []string{"shared.png", "keep.png"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("protected image %s removed: %v", name, err)
		}
	}
	_, err = db.Exec(`UPDATE cards SET data='{"image_urls":["/uploads/cards/keep.png"]}' WHERE id=1;
	CREATE TRIGGER reject_card_update BEFORE UPDATE ON cards BEGIN SELECT RAISE(ABORT,'blocked'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateCardData(context.Background(), 1, "1", map[string]any{"image_urls": []any{}}); err == nil {
		t.Fatal("expected failed update")
	}
	if _, err := os.Stat(filepath.Join(root, "keep.png")); err != nil {
		t.Fatal("failed save deleted image", err)
	}
}
