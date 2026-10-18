package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/zhinianboke/xianyu-auto-go/internal/api"
	"github.com/zhinianboke/xianyu-auto-go/internal/config"
	"github.com/zhinianboke/xianyu-auto-go/internal/database"
)

type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func main() {
	dir, err := os.MkdirTemp("", "xianyu-route-audit-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := database.Open(filepath.Join(dir, "audit.db"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	srv := api.New(db, config.Config{JWTSecret: "audit", StaticDir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	routes := []Route{}
	err = chi.Walk(srv.Router().(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, Route{method, path})
		return nil
	})
	if err != nil {
		panic(err)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})
	b, _ := json.MarshalIndent(routes, "", "  ")
	fmt.Println(string(b))
}
