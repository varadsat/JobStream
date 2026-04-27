package config

import (
	"os"
	"strconv"
)

type Config struct {
	GRPCPort       int
	HTTPPort       int    // REST→gRPC gateway for browser clients (Chrome ext, etc.)
	CORSOrigin     string // origin allowed by the gateway; "*" for dev
	LogLevel       string
	DatabaseURL    string
	JWTSecret      string
	RateLimitRPS   float64
	RateLimitBurst int
	RedisAddr      string
}

func Load() Config {
	return Config{
		GRPCPort:       envInt("GRPC_PORT", 50051),
		HTTPPort:       envInt("HTTP_PORT", 8081),
		CORSOrigin:     envStr("CORS_ORIGIN", "*"),
		LogLevel:       envStr("LOG_LEVEL", "info"),
		DatabaseURL:    envStr("DATABASE_URL", "postgres://jobstream:jobstream@localhost:5432/jobstream?sslmode=disable"),
		JWTSecret:      envStr("JWT_SECRET", "dev-secret-change-in-production"),
		RateLimitRPS:   envFloat("RATE_LIMIT_RPS", 10),
		RateLimitBurst: envInt("RATE_LIMIT_BURST", 20),
		RedisAddr:      envStr("REDIS_ADDR", "localhost:6379"),
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

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
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
