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
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/server"
)

// stubRepo is a configurable in-memory implementation of repo.ApplicationRepo
// for use in unit tests.
type stubRepo struct {
	insertFn            func(ctx context.Context, p repo.InsertParams) (repo.ApplicationRecord, error)
	getByIDAndUserIDFn  func(ctx context.Context, id, userID string) (repo.ApplicationRecord, error)
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

func startTestServer(t *testing.T, r repo.ApplicationRepo) *grpc.ClientConn {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	intakev1.RegisterIntakeServiceServer(grpcSrv, server.New(r))

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
