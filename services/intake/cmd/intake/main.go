package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/redis/go-redis/v9"
	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/auth"
	"github.com/varad/jobstream/services/intake/internal/config"
	"github.com/varad/jobstream/services/intake/internal/dedup"
	"github.com/varad/jobstream/services/intake/internal/httpgateway"
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

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer redisClient.Close()
	deduper := dedup.NewRedis(redisClient, dedup.DefaultTTL)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatalf("net.Listen: %v", err)
	}

	verifier := auth.NewHS256Verifier(cfg.JWTSecret)
	rl := ratelimit.New(cfg.RateLimitRPS, cfg.RateLimitBurst, ratelimit.WallClock)
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.UnaryServerInterceptor(logger),
			middleware.RecoveryUnaryInterceptor(logger),
			auth.UnaryServerInterceptor(verifier),
			ratelimit.UnaryServerInterceptor(rl, auth.UserFromContext),
		),
	)
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New(pgRepo, deduper))

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

	// REST→gRPC gateway. Dials the local gRPC port so every existing
	// interceptor (auth, rate-limit, logging, recovery) fires for browser
	// requests too. The grpc.NewClient call is lazy — no connection is
	// opened until the first request arrives.
	gwConn, err := grpc.NewClient(
		fmt.Sprintf("localhost:%d", cfg.GRPCPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("gateway dial: %v", err)
	}
	defer gwConn.Close()

	gw := httpgateway.New(intakev1.NewIntakeServiceClient(gwConn), logger, cfg.CORSOrigin)
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("intake HTTP gateway listening", "port", cfg.HTTPPort)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway stopped", "error", err)
		}
	}()

	<-quit
	logger.Info("shutting down gracefully")

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
	grpcSrv.GracefulStop()
}
