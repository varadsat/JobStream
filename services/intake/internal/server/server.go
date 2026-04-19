package server

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/dedup"
	"github.com/varad/jobstream/services/intake/internal/repo"
)

type IntakeServer struct {
	intakev1.UnimplementedIntakeServiceServer
	repo    repo.ApplicationRepo
	deduper dedup.Deduper
}

func New(r repo.ApplicationRepo, d dedup.Deduper) *IntakeServer {
	return &IntakeServer{repo: r, deduper: d}
}

func (s *IntakeServer) SubmitApplication(ctx context.Context, req *intakev1.SubmitApplicationRequest) (*intakev1.SubmitApplicationResponse, error) {
	if err := validateSubmit(req); err != nil {
		return nil, err
	}

	// Check whether this (user, company, title) was already submitted.
	if id, hit, err := s.deduper.Check(ctx, req.UserId, req.Company, req.JobTitle); err == nil && hit {
		return &intakev1.SubmitApplicationResponse{ApplicationId: id}, nil
	}

	// Claim the slot with SET NX to prevent a concurrent duplicate insert.
	// If Redis is down (err != nil), we skip the claim and proceed — preferring
	// availability over strict idempotency during an outage.
	if claimed, err := s.deduper.Claim(ctx, req.UserId, req.Company, req.JobTitle); err == nil && !claimed {
		// Another goroutine holds the claim. Re-check in case it already committed.
		if id, hit, _ := s.deduper.Check(ctx, req.UserId, req.Company, req.JobTitle); hit {
			return &intakev1.SubmitApplicationResponse{ApplicationId: id}, nil
		}
		// Slot is still pending. The client should retry once the concurrent
		// insert completes (typically < 1 s).
		return nil, status.Error(codes.AlreadyExists, "duplicate submission in progress, retry shortly")
	}

	appliedAt := time.Now().UTC()
	if req.AppliedAt != nil {
		appliedAt = req.AppliedAt.AsTime()
	}

	rec, err := s.repo.Insert(ctx, repo.InsertParams{
		UserID:        req.UserId,
		JobTitle:      req.JobTitle,
		Company:       req.Company,
		URL:           req.Url,
		Source:        int32(req.Source),
		Status:        int32(intakev1.Status_STATUS_APPLIED),
		AppliedAt:     appliedAt,
		SchemaVersion: "1.0",
	})
	if err != nil {
		// Release the pending claim so the next caller is not blocked.
		_ = s.deduper.Release(ctx, req.UserId, req.Company, req.JobTitle)
		return nil, status.Errorf(codes.Internal, "insert: %v", err)
	}

	// Commit the real application ID so future Check calls return it.
	_ = s.deduper.Set(ctx, req.UserId, req.Company, req.JobTitle, rec.ID)

	return &intakev1.SubmitApplicationResponse{ApplicationId: rec.ID}, nil
}

func (s *IntakeServer) GetApplicationStatus(ctx context.Context, req *intakev1.GetApplicationStatusRequest) (*intakev1.GetApplicationStatusResponse, error) {
	if req.ApplicationId == "" {
		return nil, status.Error(codes.InvalidArgument, "application_id is required")
	}
	if req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	rec, err := s.repo.GetByIDAndUserID(ctx, req.ApplicationId, req.UserId)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "application not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get application: %v", err)
	}

	return &intakev1.GetApplicationStatusResponse{
		Application: &intakev1.Application{
			Id:            rec.ID,
			UserId:        rec.UserID,
			JobTitle:      rec.JobTitle,
			Company:       rec.Company,
			Url:           rec.URL,
			Source:        intakev1.Source(rec.Source),
			Status:        intakev1.Status(rec.Status),
			AppliedAt:     timestamppb.New(rec.AppliedAt),
			CreatedAt:     timestamppb.New(rec.CreatedAt),
			SchemaVersion: rec.SchemaVersion,
		},
	}, nil
}

func validateSubmit(req *intakev1.SubmitApplicationRequest) error {
	switch {
	case req.UserId == "":
		return status.Error(codes.InvalidArgument, "user_id is required")
	case req.JobTitle == "":
		return status.Error(codes.InvalidArgument, "job_title is required")
	case req.Company == "":
		return status.Error(codes.InvalidArgument, "company is required")
	case req.Url == "":
		return status.Error(codes.InvalidArgument, "url is required")
	case req.Source == intakev1.Source_SOURCE_UNSPECIFIED:
		return status.Error(codes.InvalidArgument, "source must be specified")
	}
	return nil
}
