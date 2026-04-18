package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/config"
	"github.com/varad/jobstream/services/intake/internal/server"
)

func main() {
	cfg := config.Load()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatalf("net.Listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New())

	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("intake gRPC listening on :%d", cfg.GRPCPort)
		if err := grpcSrv.Serve(lis); err != nil {
			log.Printf("Serve stopped: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down gracefully…")
	grpcSrv.GracefulStop()
}
