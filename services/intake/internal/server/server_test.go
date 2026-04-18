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
	insertFn func(ctx context.Context, p repo.InsertParams) (repo.ApplicationRecord, error)
}

func (s *stubRepo) Insert(ctx context.Context, p repo.InsertParams) (repo.ApplicationRecord, error) {
	if s.insertFn != nil {
		return s.insertFn(ctx, p)
	}
	return repo.ApplicationRecord{}, errors.New("insertFn not set")
}

func (s *stubRepo) GetByIDAndUserID(_ context.Context, _, _ string) (repo.ApplicationRecord, error) {
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

func TestGetApplicationStatus_Unimplemented(t *testing.T) {
	conn := startTestServer(t, &stubRepo{})

	_, err := intakev1.NewIntakeServiceClient(conn).GetApplicationStatus(
		context.Background(),
		&intakev1.GetApplicationStatusRequest{},
	)
	if got := status.Code(err); got != codes.Unimplemented {
		t.Errorf("code: want Unimplemented, got %v", got)
	}
}

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
