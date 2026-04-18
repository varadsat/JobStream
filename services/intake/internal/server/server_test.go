package server_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/server"
)

func startTestServer(t *testing.T) *grpc.ClientConn {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New())

	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck

	return conn
}

func TestHealthCheck_Serving(t *testing.T) {
	conn := startTestServer(t)

	resp, err := grpc_health_v1.NewHealthClient(conn).Check(
		context.Background(),
		&grpc_health_v1.HealthCheckRequest{},
	)
	if err != nil {
		t.Fatalf("Health.Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("status: want SERVING, got %v", resp.Status)
	}
}

func TestSubmitApplication_Unimplemented(t *testing.T) {
	conn := startTestServer(t)

	_, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{},
	)
	if got := status.Code(err); got != codes.Unimplemented {
		t.Errorf("code: want Unimplemented, got %v", got)
	}
}

func TestGetApplicationStatus_Unimplemented(t *testing.T) {
	conn := startTestServer(t)

	_, err := intakev1.NewIntakeServiceClient(conn).GetApplicationStatus(
		context.Background(),
		&intakev1.GetApplicationStatusRequest{},
	)
	if got := status.Code(err); got != codes.Unimplemented {
		t.Errorf("code: want Unimplemented, got %v", got)
	}
}
