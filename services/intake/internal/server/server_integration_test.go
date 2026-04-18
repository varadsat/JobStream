package server_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/server"
)

// newIntegrationClient starts a real gRPC server backed by a testcontainers Postgres instance
// and returns a connected client. Everything is torn down when the test ends.
func newIntegrationClient(t *testing.T) intakev1.IntakeServiceClient {
	t.Helper()
	ctx := context.Background()

	ctr, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("testdb"),
		pgmodule.WithUsername("test"),
		pgmodule.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	if err := repo.RunMigrations(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	r, err := repo.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(r.Close)

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New(r))
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck

	return intakev1.NewIntakeServiceClient(conn)
}

func TestIntegration_SubmitApplication_HappyPath(t *testing.T) {
	client := newIntegrationClient(t)

	resp, err := client.SubmitApplication(context.Background(), &intakev1.SubmitApplicationRequest{
		UserId:   "user-42",
		JobTitle: "Staff Engineer",
		Company:  "Globex",
		Url:      "https://globex.example/jobs/100",
		Source:   intakev1.Source_SOURCE_DASHBOARD,
	})
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	if resp.ApplicationId == "" {
		t.Error("ApplicationId should be non-empty UUID")
	}
}

func TestIntegration_SubmitApplication_ValidationStillFires(t *testing.T) {
	client := newIntegrationClient(t)

	_, err := client.SubmitApplication(context.Background(), &intakev1.SubmitApplicationRequest{
		// user_id missing on purpose
		JobTitle: "SWE",
		Company:  "Co",
		Url:      "https://co.example",
		Source:   intakev1.Source_SOURCE_SCRAPER,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code: want InvalidArgument, got %v", got)
	}
}
