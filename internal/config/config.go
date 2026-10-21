package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr, DataDir, Database, StaticDir, JWTSecret, APISecretKey string
	AccessTTL, RefreshTTL                                       time.Duration
}

func Load() Config {
	data := env("DATA_DIR", "data")
	jwtSecret := env("JWT_SECRET", "")
	if jwtSecret == "" {
		jwtSecret = loadOrCreateSecret(filepath.Join(data, ".jwt_secret"))
	}
	return Config{
		Addr: env("LISTEN_ADDR", ":8089"), DataDir: data,
		Database:  env("DATABASE_PATH", filepath.Join(data, "xianyu.db")),
		StaticDir: env("STATIC_DIR", "frontend/dist"), JWTSecret: jwtSecret,
		APISecretKey: strings.TrimSpace(env("API_SECRET_KEY", "")),
		AccessTTL:    minutes("ACCESS_TOKEN_EXPIRE_MINUTES", 1440), RefreshTTL: minutes("REFRESH_TOKEN_EXPIRE_MINUTES", 10080),
	}
}

func loadOrCreateSecret(path string) string {
	if raw, err := os.ReadFile(path); err == nil {
		if secret := strings.TrimSpace(string(raw)); secret != "" {
			return secret
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("generate JWT secret: %w", err))
	}
	secret := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		panic(fmt.Errorf("create secret directory: %w", err))
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
		panic(fmt.Errorf("persist JWT secret: %w", err))
	}
	return secret
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func minutes(k string, d int) time.Duration {
	v, _ := strconv.Atoi(env(k, strconv.Itoa(d)))
	return time.Duration(v) * time.Minute
}
