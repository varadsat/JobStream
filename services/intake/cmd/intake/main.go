package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/auth"
	"github.com/varad/jobstream/services/intake/internal/config"
	"github.com/varad/jobstream/services/intake/internal/middleware"
	"github.com/varad/jobstream/services/intake/internal/ratelimit"
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/server"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		logLevel = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	if err := repo.RunMigrations(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	pgRepo, err := repo.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pgRepo.Close()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatalf("net.Listen: %v", err)
	}

	verifier := auth.NewHS256Verifier(cfg.JWTSecret)
	rl := ratelimit.New(cfg.RateLimitRPS, cfg.RateLimitBurst, ratelimit.WallClock)
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.UnaryServerInterceptor(logger),
			auth.UnaryServerInterceptor(verifier),
			ratelimit.UnaryServerInterceptor(rl, auth.UserFromContext),
		),
	)
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New(pgRepo))

	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	reflection.Register(grpcSrv)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("intake gRPC listening", "port", cfg.GRPCPort)
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Error("server stopped", "error", err)
		}
	}()

	<-quit
	logger.Info("shutting down gracefully")
	grpcSrv.GracefulStop()
}
