package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Addr, DataDir, Database, StaticDir, JWTSecret, APISecretKey string
	AccessTTL, RefreshTTL                                       time.Duration
}

func Load() Config {
	data := env("DATA_DIR", "data")
	return Config{
		Addr: env("LISTEN_ADDR", ":8089"), DataDir: data,
		Database:  env("DATABASE_PATH", filepath.Join(data, "xianyu.db")),
		StaticDir: env("STATIC_DIR", "frontend/dist"), JWTSecret: env("JWT_SECRET", "change-this-secret"),
		APISecretKey: env("API_SECRET_KEY", "xianyu_api_secret_2024"),
		AccessTTL:    minutes("ACCESS_TOKEN_EXPIRE_MINUTES", 1440), RefreshTTL: minutes("REFRESH_TOKEN_EXPIRE_MINUTES", 10080),
	}
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
