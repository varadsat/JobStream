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

// ── SubmitApplication ─────────────────────────────────────────────────────────

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

// ── GetApplicationStatus ──────────────────────────────────────────────────────

func TestIntegration_GetApplicationStatus_Found(t *testing.T) {
	client := newIntegrationClient(t)
	ctx := context.Background()

	// First, submit an application to get a real ID.
	submitResp, err := client.SubmitApplication(ctx, &intakev1.SubmitApplicationRequest{
		UserId:   "user-10",
		JobTitle: "DevOps Engineer",
		Company:  "Initech",
		Url:      "https://initech.example/jobs/5",
		Source:   intakev1.Source_SOURCE_CHROME_EXT,
	})
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}

	// Now fetch it back.
	getResp, err := client.GetApplicationStatus(ctx, &intakev1.GetApplicationStatusRequest{
		ApplicationId: submitResp.ApplicationId,
		UserId:        "user-10",
	})
	if err != nil {
		t.Fatalf("GetApplicationStatus: %v", err)
	}

	app := getResp.Application
	if app.Id != submitResp.ApplicationId {
		t.Errorf("Id: want %s, got %s", submitResp.ApplicationId, app.Id)
	}
	if app.UserId != "user-10" {
		t.Errorf("UserId: want user-10, got %s", app.UserId)
	}
	if app.Company != "Initech" {
		t.Errorf("Company: want Initech, got %s", app.Company)
	}
	if app.Status != intakev1.Status_STATUS_APPLIED {
		t.Errorf("Status: want STATUS_APPLIED, got %v", app.Status)
	}
}

func TestIntegration_GetApplicationStatus_NotFound(t *testing.T) {
	client := newIntegrationClient(t)

	_, err := client.GetApplicationStatus(context.Background(), &intakev1.GetApplicationStatusRequest{
		ApplicationId: "00000000-0000-0000-0000-000000000000",
		UserId:        "user-10",
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code: want NotFound, got %v", got)
	}
}

func TestIntegration_GetApplicationStatus_WrongOwner(t *testing.T) {
	client := newIntegrationClient(t)
	ctx := context.Background()

	submitResp, err := client.SubmitApplication(ctx, &intakev1.SubmitApplicationRequest{
		UserId:   "user-owner",
		JobTitle: "PM",
		Company:  "Umbrella",
		Url:      "https://umbrella.example/jobs/9",
		Source:   intakev1.Source_SOURCE_DASHBOARD,
	})
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}

	// Valid ID but wrong user — must look like NotFound (ownership is opaque).
	_, err = client.GetApplicationStatus(ctx, &intakev1.GetApplicationStatusRequest{
		ApplicationId: submitResp.ApplicationId,
		UserId:        "attacker",
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code: want NotFound for wrong owner, got %v", got)
	}
}
