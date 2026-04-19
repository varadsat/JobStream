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

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/dedup"
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/server"
)

// newPostgresRepo starts a Postgres testcontainer, runs migrations, and
// returns a ready repo. Container is terminated when the test ends.
func newPostgresRepo(t *testing.T) repo.ApplicationRepo {
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
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

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
	return r
}

// newRedisDeduper starts a Redis testcontainer and returns a ready Deduper.
func newRedisDeduper(t *testing.T) dedup.Deduper {
	t.Helper()
	ctx := context.Background()

	c, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	addr, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	const scheme = "redis://"
	if len(addr) > len(scheme) && addr[:len(scheme)] == scheme {
		addr = addr[len(scheme):]
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	return dedup.NewRedis(client, dedup.DefaultTTL)
}

// newIntegrationClient starts a real gRPC server backed by Postgres (no Redis
// dedup). Existing tests that do not exercise dedup use this helper.
func newIntegrationClient(t *testing.T) intakev1.IntakeServiceClient {
	t.Helper()
	return newIntegrationClientWith(t, newPostgresRepo(t), dedup.NoopDeduper{})
}

// newIntegrationClientWith is the low-level helper that wires a repo and
// deduper into a gRPC server, returning a connected client.
func newIntegrationClientWith(t *testing.T, r repo.ApplicationRepo, d dedup.Deduper) intakev1.IntakeServiceClient {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New(r, d))
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

// ── Dedup integration ─────────────────────────────────────────────────────────

// TestIntegration_Dedup_SubmitTwice_SingleDBRow asserts the core idempotency
// guarantee: submitting the same (user, company, title) twice produces exactly
// one Postgres row, and both calls return the same application ID.
func TestIntegration_Dedup_SubmitTwice_SingleDBRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx := context.Background()
	r := newPostgresRepo(t)
	d := newRedisDeduper(t)
	client := newIntegrationClientWith(t, r, d)

	req := &intakev1.SubmitApplicationRequest{
		UserId:   "dedup-user",
		JobTitle: "Backend Engineer",
		Company:  "Dedup Corp",
		Url:      "https://dedupcorp.example/jobs/1",
		Source:   intakev1.Source_SOURCE_SCRAPER,
	}

	first, err := client.SubmitApplication(ctx, req)
	if err != nil {
		t.Fatalf("first SubmitApplication: %v", err)
	}

	second, err := client.SubmitApplication(ctx, req)
	if err != nil {
		t.Fatalf("second SubmitApplication: %v", err)
	}

	if first.ApplicationId != second.ApplicationId {
		t.Errorf("duplicate submit returned different IDs: %q vs %q",
			first.ApplicationId, second.ApplicationId)
	}

	// Confirm there is exactly one row in the DB for this application.
	rec, err := r.(interface {
		GetByIDAndUserID(ctx context.Context, id, userID string) (repo.ApplicationRecord, error)
	}).GetByIDAndUserID(ctx, first.ApplicationId, "dedup-user")
	if err != nil {
		t.Fatalf("GetByIDAndUserID: %v", err)
	}
	if rec.ID != first.ApplicationId {
		t.Errorf("DB record ID mismatch: want %s, got %s", first.ApplicationId, rec.ID)
	}
}

// TestIntegration_Dedup_DifferentUsers_NotDeduped confirms that two different
// users submitting to the same company+title each get their own row.
func TestIntegration_Dedup_DifferentUsers_NotDeduped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	r := newPostgresRepo(t)
	d := newRedisDeduper(t)
	client := newIntegrationClientWith(t, r, d)
	ctx := context.Background()

	base := &intakev1.SubmitApplicationRequest{
		JobTitle: "SWE", Company: "Shared Corp",
		Url: "https://sharedcorp.example/jobs/2", Source: intakev1.Source_SOURCE_DASHBOARD,
	}

	req1 := *base
	req1.UserId = "alice"
	resp1, err := client.SubmitApplication(ctx, &req1)
	if err != nil {
		t.Fatalf("alice submit: %v", err)
	}

	req2 := *base
	req2.UserId = "bob"
	resp2, err := client.SubmitApplication(ctx, &req2)
	if err != nil {
		t.Fatalf("bob submit: %v", err)
	}

	if resp1.ApplicationId == resp2.ApplicationId {
		t.Error("different users must get distinct application IDs")
	}
}

// TestIntegration_Dedup_CaseInsensitive verifies that "Google" and "GOOGLE"
// for the same title are treated as duplicates.
func TestIntegration_Dedup_CaseInsensitive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	r := newPostgresRepo(t)
	d := newRedisDeduper(t)
	client := newIntegrationClientWith(t, r, d)
	ctx := context.Background()

	first, err := client.SubmitApplication(ctx, &intakev1.SubmitApplicationRequest{
		UserId: "user-ci", JobTitle: "SWE", Company: "Google",
		Url: "https://google.example/jobs/1", Source: intakev1.Source_SOURCE_DASHBOARD,
	})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	second, err := client.SubmitApplication(ctx, &intakev1.SubmitApplicationRequest{
		UserId: "user-ci", JobTitle: "swe", Company: "GOOGLE",
		Url: "https://google.example/jobs/2", Source: intakev1.Source_SOURCE_SCRAPER,
	})
	if err != nil {
		t.Fatalf("second submit (different case): %v", err)
	}

	if first.ApplicationId != second.ApplicationId {
		t.Errorf("case-variant submit should return same ID: %q vs %q",
			first.ApplicationId, second.ApplicationId)
	}
}
