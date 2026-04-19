package server_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/dedup"
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/server"
)

// stubRepo is a configurable in-memory implementation of repo.ApplicationRepo
// for use in unit tests.
type stubRepo struct {
	insertFn           func(ctx context.Context, p repo.InsertParams) (repo.ApplicationRecord, error)
	getByIDAndUserIDFn func(ctx context.Context, id, userID string) (repo.ApplicationRecord, error)
}

func (s *stubRepo) Insert(ctx context.Context, p repo.InsertParams) (repo.ApplicationRecord, error) {
	if s.insertFn != nil {
		return s.insertFn(ctx, p)
	}
	return repo.ApplicationRecord{}, errors.New("insertFn not set")
}

func (s *stubRepo) GetByIDAndUserID(ctx context.Context, id, userID string) (repo.ApplicationRecord, error) {
	if s.getByIDAndUserIDFn != nil {
		return s.getByIDAndUserIDFn(ctx, id, userID)
	}
	return repo.ApplicationRecord{}, repo.ErrNotFound
}

// stubDeduper is a configurable implementation of dedup.Deduper for unit tests.
// Nil function fields fall back to no-op / always-miss / always-claim behaviour.
type stubDeduper struct {
	checkFn   func(ctx context.Context, userID, company, jobTitle string) (string, bool, error)
	claimFn   func(ctx context.Context, userID, company, jobTitle string) (bool, error)
	setFn     func(ctx context.Context, userID, company, jobTitle, appID string) error
	releaseFn func(ctx context.Context, userID, company, jobTitle string) error
}

func (s *stubDeduper) Check(ctx context.Context, u, c, t string) (string, bool, error) {
	if s.checkFn != nil {
		return s.checkFn(ctx, u, c, t)
	}
	return "", false, nil
}

func (s *stubDeduper) Claim(ctx context.Context, u, c, t string) (bool, error) {
	if s.claimFn != nil {
		return s.claimFn(ctx, u, c, t)
	}
	return true, nil
}

func (s *stubDeduper) Set(ctx context.Context, u, c, t, id string) error {
	if s.setFn != nil {
		return s.setFn(ctx, u, c, t, id)
	}
	return nil
}

func (s *stubDeduper) Release(ctx context.Context, u, c, t string) error {
	if s.releaseFn != nil {
		return s.releaseFn(ctx, u, c, t)
	}
	return nil
}

func startTestServer(t *testing.T, r repo.ApplicationRepo) *grpc.ClientConn {
	t.Helper()
	return startTestServerWithDeduper(t, r, dedup.NoopDeduper{})
}

func startTestServerWithDeduper(t *testing.T, r repo.ApplicationRepo, d dedup.Deduper) *grpc.ClientConn {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New(r, d))

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

// ── Health ────────────────────────────────────────────────────────────────────

func TestHealthCheck_Serving(t *testing.T) {
	conn := startTestServer(t, &stubRepo{})

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

// ── SubmitApplication ─────────────────────────────────────────────────────────

func TestSubmitApplication_HappyPath(t *testing.T) {
	wantID := "aaaabbbb-cccc-dddd-eeee-ffffffffffff"
	stub := &stubRepo{
		insertFn: func(_ context.Context, p repo.InsertParams) (repo.ApplicationRecord, error) {
			if p.UserID != "user-1" {
				t.Errorf("UserID: want user-1, got %s", p.UserID)
			}
			if p.Source != int32(intakev1.Source_SOURCE_DASHBOARD) {
				t.Errorf("Source: want %d, got %d", intakev1.Source_SOURCE_DASHBOARD, p.Source)
			}
			if p.Status != int32(intakev1.Status_STATUS_APPLIED) {
				t.Errorf("Status: want STATUS_APPLIED, got %d", p.Status)
			}
			if p.SchemaVersion != "1.0" {
				t.Errorf("SchemaVersion: want 1.0, got %s", p.SchemaVersion)
			}
			return repo.ApplicationRecord{ID: wantID}, nil
		},
	}

	conn := startTestServer(t, stub)
	resp, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId:   "user-1",
			JobTitle: "Software Engineer",
			Company:  "Acme Corp",
			Url:      "https://acme.example/jobs/1",
			Source:   intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	if resp.ApplicationId != wantID {
		t.Errorf("ApplicationId: want %s, got %s", wantID, resp.ApplicationId)
	}
}

func TestSubmitApplication_AppliedAtPropagated(t *testing.T) {
	wantTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	var gotAppliedAt time.Time

	stub := &stubRepo{
		insertFn: func(_ context.Context, p repo.InsertParams) (repo.ApplicationRecord, error) {
			gotAppliedAt = p.AppliedAt
			return repo.ApplicationRecord{ID: "id-x"}, nil
		},
	}

	conn := startTestServer(t, stub)
	_, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId:    "user-1",
			JobTitle:  "SRE",
			Company:   "Corp",
			Url:       "https://corp.example/jobs/2",
			Source:    intakev1.Source_SOURCE_SCRAPER,
			AppliedAt: timestamppb.New(wantTime),
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	if !gotAppliedAt.Equal(wantTime) {
		t.Errorf("AppliedAt: want %v, got %v", wantTime, gotAppliedAt)
	}
}

func TestSubmitApplication_DefaultsAppliedAtWhenUnset(t *testing.T) {
	before := time.Now().UTC()
	var gotAppliedAt time.Time

	stub := &stubRepo{
		insertFn: func(_ context.Context, p repo.InsertParams) (repo.ApplicationRecord, error) {
			gotAppliedAt = p.AppliedAt
			return repo.ApplicationRecord{ID: "id-y"}, nil
		},
	}

	conn := startTestServer(t, stub)
	_, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId:   "user-1",
			JobTitle: "SWE",
			Company:  "Co",
			Url:      "https://co.example/jobs/3",
			Source:   intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	after := time.Now().UTC()
	if gotAppliedAt.Before(before) || gotAppliedAt.After(after) {
		t.Errorf("AppliedAt %v not in [%v, %v]", gotAppliedAt, before, after)
	}
}

func TestSubmitApplication_RepoError(t *testing.T) {
	stub := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{}, errors.New("db down")
		},
	}

	conn := startTestServer(t, stub)
	_, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId:   "user-1",
			JobTitle: "SWE",
			Company:  "Co",
			Url:      "https://co.example/jobs/4",
			Source:   intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code: want Internal, got %v", got)
	}
}

func TestSubmitApplication_Validation(t *testing.T) {
	conn := startTestServer(t, &stubRepo{})
	client := intakev1.NewIntakeServiceClient(conn)

	tests := []struct {
		name string
		req  *intakev1.SubmitApplicationRequest
	}{
		{
			name: "missing user_id",
			req: &intakev1.SubmitApplicationRequest{
				JobTitle: "SWE", Company: "Co", Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
			},
		},
		{
			name: "missing job_title",
			req: &intakev1.SubmitApplicationRequest{
				UserId: "user-1", Company: "Co", Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
			},
		},
		{
			name: "missing company",
			req: &intakev1.SubmitApplicationRequest{
				UserId: "user-1", JobTitle: "SWE", Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
			},
		},
		{
			name: "missing url",
			req: &intakev1.SubmitApplicationRequest{
				UserId: "user-1", JobTitle: "SWE", Company: "Co", Source: intakev1.Source_SOURCE_DASHBOARD,
			},
		},
		{
			name: "source unspecified",
			req: &intakev1.SubmitApplicationRequest{
				UserId: "user-1", JobTitle: "SWE", Company: "Co", Url: "https://co.example",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.SubmitApplication(context.Background(), tc.req)
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("code: want InvalidArgument, got %v", got)
			}
		})
	}
}

// ── GetApplicationStatus ──────────────────────────────────────────────────────

func TestGetApplicationStatus_Found(t *testing.T) {
	appliedAt := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	createdAt := time.Date(2025, 3, 1, 9, 0, 1, 0, time.UTC)

	stub := &stubRepo{
		getByIDAndUserIDFn: func(_ context.Context, id, userID string) (repo.ApplicationRecord, error) {
			if id != "app-uuid-1" || userID != "user-1" {
				t.Errorf("args: want (app-uuid-1, user-1), got (%s, %s)", id, userID)
			}
			return repo.ApplicationRecord{
				ID:            "app-uuid-1",
				UserID:        "user-1",
				JobTitle:      "Backend Engineer",
				Company:       "Globex",
				URL:           "https://globex.example/jobs/7",
				Source:        int32(intakev1.Source_SOURCE_SCRAPER),
				Status:        int32(intakev1.Status_STATUS_APPLIED),
				AppliedAt:     appliedAt,
				CreatedAt:     createdAt,
				SchemaVersion: "1.0",
			}, nil
		},
	}

	conn := startTestServer(t, stub)
	resp, err := intakev1.NewIntakeServiceClient(conn).GetApplicationStatus(
		context.Background(),
		&intakev1.GetApplicationStatusRequest{
			ApplicationId: "app-uuid-1",
			UserId:        "user-1",
		},
	)
	if err != nil {
		t.Fatalf("GetApplicationStatus: %v", err)
	}

	app := resp.Application
	if app == nil {
		t.Fatal("Application should not be nil")
	}
	if app.Id != "app-uuid-1" {
		t.Errorf("Id: want app-uuid-1, got %s", app.Id)
	}
	if app.UserId != "user-1" {
		t.Errorf("UserId: want user-1, got %s", app.UserId)
	}
	if app.JobTitle != "Backend Engineer" {
		t.Errorf("JobTitle: want Backend Engineer, got %s", app.JobTitle)
	}
	if app.Company != "Globex" {
		t.Errorf("Company: want Globex, got %s", app.Company)
	}
	if app.Source != intakev1.Source_SOURCE_SCRAPER {
		t.Errorf("Source: want SOURCE_SCRAPER, got %v", app.Source)
	}
	if app.Status != intakev1.Status_STATUS_APPLIED {
		t.Errorf("Status: want STATUS_APPLIED, got %v", app.Status)
	}
	if !app.AppliedAt.AsTime().Equal(appliedAt) {
		t.Errorf("AppliedAt: want %v, got %v", appliedAt, app.AppliedAt.AsTime())
	}
	if !app.CreatedAt.AsTime().Equal(createdAt) {
		t.Errorf("CreatedAt: want %v, got %v", createdAt, app.CreatedAt.AsTime())
	}
	if app.SchemaVersion != "1.0" {
		t.Errorf("SchemaVersion: want 1.0, got %s", app.SchemaVersion)
	}
}

func TestGetApplicationStatus_NotFound(t *testing.T) {
	stub := &stubRepo{
		getByIDAndUserIDFn: func(_ context.Context, _, _ string) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{}, repo.ErrNotFound
		},
	}

	conn := startTestServer(t, stub)
	_, err := intakev1.NewIntakeServiceClient(conn).GetApplicationStatus(
		context.Background(),
		&intakev1.GetApplicationStatusRequest{
			ApplicationId: "00000000-0000-0000-0000-000000000000",
			UserId:        "user-1",
		},
	)
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code: want NotFound, got %v", got)
	}
}

func TestGetApplicationStatus_RepoError(t *testing.T) {
	stub := &stubRepo{
		getByIDAndUserIDFn: func(_ context.Context, _, _ string) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{}, errors.New("connection reset")
		},
	}

	conn := startTestServer(t, stub)
	_, err := intakev1.NewIntakeServiceClient(conn).GetApplicationStatus(
		context.Background(),
		&intakev1.GetApplicationStatusRequest{
			ApplicationId: "some-id",
			UserId:        "user-1",
		},
	)
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code: want Internal, got %v", got)
	}
}

func TestGetApplicationStatus_Validation(t *testing.T) {
	conn := startTestServer(t, &stubRepo{})
	client := intakev1.NewIntakeServiceClient(conn)

	tests := []struct {
		name string
		req  *intakev1.GetApplicationStatusRequest
	}{
		{
			name: "missing application_id",
			req:  &intakev1.GetApplicationStatusRequest{UserId: "user-1"},
		},
		{
			name: "missing user_id",
			req:  &intakev1.GetApplicationStatusRequest{ApplicationId: "some-id"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.GetApplicationStatus(context.Background(), tc.req)
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("code: want InvalidArgument, got %v", got)
			}
		})
	}
}

// ── Deduplication ─────────────────────────────────────────────────────────────

func TestSubmitApplication_DedupHit_ReturnsExistingID(t *testing.T) {
	// When the deduper returns a cached ID, Insert must not be called.
	insertCalled := false
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			insertCalled = true
			return repo.ApplicationRecord{}, nil
		},
	}
	d := &stubDeduper{
		checkFn: func(_ context.Context, _, _, _ string) (string, bool, error) {
			return "cached-id-123", true, nil
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	resp, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	if resp.ApplicationId != "cached-id-123" {
		t.Errorf("ApplicationId: want cached-id-123, got %s", resp.ApplicationId)
	}
	if insertCalled {
		t.Error("Insert should not be called on a dedup hit")
	}
}

func TestSubmitApplication_FirstSubmit_SetsDedup(t *testing.T) {
	// On a cache miss the real ID must be committed to the deduper after insert.
	var setCalled bool
	var setID string
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{ID: "new-id-456"}, nil
		},
	}
	d := &stubDeduper{
		setFn: func(_ context.Context, _, _, _, id string) error {
			setCalled = true
			setID = id
			return nil
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	resp, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	if resp.ApplicationId != "new-id-456" {
		t.Errorf("ApplicationId: want new-id-456, got %s", resp.ApplicationId)
	}
	if !setCalled || setID != "new-id-456" {
		t.Errorf("deduper.Set: called=%v id=%q", setCalled, setID)
	}
}

func TestSubmitApplication_ClaimLost_ThenHit_ReturnsExistingID(t *testing.T) {
	// Claim fails (another goroutine holds the slot), but that goroutine finishes
	// before our re-check, so Check returns the real ID.
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			t.Error("Insert must not be called when claim is lost")
			return repo.ApplicationRecord{}, nil
		},
	}
	checkCalls := 0
	d := &stubDeduper{
		checkFn: func(_ context.Context, _, _, _ string) (string, bool, error) {
			checkCalls++
			if checkCalls == 1 {
				return "", false, nil // first call: miss
			}
			return "raced-id-789", true, nil // second call (after claim lost): hit
		},
		claimFn: func(_ context.Context, _, _, _ string) (bool, error) {
			return false, nil // claim lost
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	resp, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication: %v", err)
	}
	if resp.ApplicationId != "raced-id-789" {
		t.Errorf("ApplicationId: want raced-id-789, got %s", resp.ApplicationId)
	}
}

func TestSubmitApplication_ClaimLost_StillPending_ReturnsAlreadyExists(t *testing.T) {
	// Claim fails and the re-check also misses (slot still in-flight).
	r := &stubRepo{}
	d := &stubDeduper{
		claimFn: func(_ context.Context, _, _, _ string) (bool, error) {
			return false, nil
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	_, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Errorf("code: want AlreadyExists, got %v", got)
	}
}

func TestSubmitApplication_InsertError_ReleasesClaimAndReturnsInternal(t *testing.T) {
	var releaseCalled bool
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{}, errors.New("pg down")
		},
	}
	d := &stubDeduper{
		releaseFn: func(_ context.Context, _, _, _ string) error {
			releaseCalled = true
			return nil
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	_, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code: want Internal, got %v", got)
	}
	if !releaseCalled {
		t.Error("deduper.Release must be called on insert failure")
	}
}

func TestSubmitApplication_DedupCheckError_Proceeds(t *testing.T) {
	// Redis down on Check — insert should still succeed (graceful degradation).
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{ID: "id-fallback"}, nil
		},
	}
	d := &stubDeduper{
		checkFn: func(_ context.Context, _, _, _ string) (string, bool, error) {
			return "", false, errors.New("redis connection refused")
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	resp, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication should succeed even when Check fails: %v", err)
	}
	if resp.ApplicationId != "id-fallback" {
		t.Errorf("ApplicationId: want id-fallback, got %s", resp.ApplicationId)
	}
}

func TestSubmitApplication_DedupClaimError_Proceeds(t *testing.T) {
	// Redis down on Claim — insert should still succeed (graceful degradation).
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{ID: "id-fallback2"}, nil
		},
	}
	d := &stubDeduper{
		claimFn: func(_ context.Context, _, _, _ string) (bool, error) {
			return false, errors.New("redis timeout")
		},
	}

	conn := startTestServerWithDeduper(t, r, d)
	resp, err := intakev1.NewIntakeServiceClient(conn).SubmitApplication(
		context.Background(),
		&intakev1.SubmitApplicationRequest{
			UserId: "u1", JobTitle: "SWE", Company: "Co",
			Url: "https://co.example", Source: intakev1.Source_SOURCE_DASHBOARD,
		},
	)
	if err != nil {
		t.Fatalf("SubmitApplication should succeed even when Claim fails: %v", err)
	}
	if resp.ApplicationId != "id-fallback2" {
		t.Errorf("ApplicationId: want id-fallback2, got %s", resp.ApplicationId)
	}
}

// ── GetApplicationStatus ──────────────────────────────────────────────────────

// TestGetApplicationStatus_WrongUserIsNotFound verifies that the ownership
// check (WHERE id=$1 AND user_id=$2) surfaces as NotFound, not Internal.
// The stub simulates the same behaviour the Postgres repo enforces atomically.
func TestGetApplicationStatus_WrongUserIsNotFound(t *testing.T) {
	stub := &stubRepo{
		getByIDAndUserIDFn: func(_ context.Context, _, userID string) (repo.ApplicationRecord, error) {
			if userID != "owner" {
				return repo.ApplicationRecord{}, repo.ErrNotFound
			}
			return repo.ApplicationRecord{ID: "app-id"}, nil
		},
	}

	conn := startTestServer(t, stub)
	_, err := intakev1.NewIntakeServiceClient(conn).GetApplicationStatus(
		context.Background(),
		&intakev1.GetApplicationStatusRequest{
			ApplicationId: "app-id",
			UserId:        "attacker",
		},
	)
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code: want NotFound for wrong user, got %v", got)
	}
}
