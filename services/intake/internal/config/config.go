package config

import (
	"os"
	"strconv"
)

type Config struct {
	GRPCPort    int
	LogLevel    string
	DatabaseURL string
}

func Load() Config {
	return Config{
		GRPCPort:    envInt("GRPC_PORT", 50051),
		LogLevel:    envStr("LOG_LEVEL", "info"),
		DatabaseURL: envStr("DATABASE_URL", "postgres://jobstream:jobstream@localhost:5432/jobstream?sslmode=disable"),
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
