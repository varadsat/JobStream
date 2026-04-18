package config_test

import (
	"testing"

	"github.com/varad/jobstream/services/intake/internal/config"
)

func TestLoad_defaults(t *testing.T) {
	t.Setenv("GRPC_PORT", "")
	t.Setenv("LOG_LEVEL", "")

	cfg := config.Load()

	if cfg.GRPCPort != 50051 {
		t.Errorf("GRPCPort: want 50051, got %d", cfg.GRPCPort)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel: want info, got %s", cfg.LogLevel)
	}
}

func TestLoad_fromEnv(t *testing.T) {
	t.Setenv("GRPC_PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")

	cfg := config.Load()

	if cfg.GRPCPort != 9090 {
		t.Errorf("GRPCPort: want 9090, got %d", cfg.GRPCPort)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: want debug, got %s", cfg.LogLevel)
	}
}

func TestLoad_invalidPortFallsToDefault(t *testing.T) {
	t.Setenv("GRPC_PORT", "not-a-number")

	cfg := config.Load()

	if cfg.GRPCPort != 50051 {
		t.Errorf("GRPCPort: want default 50051 on bad input, got %d", cfg.GRPCPort)
	}
}
