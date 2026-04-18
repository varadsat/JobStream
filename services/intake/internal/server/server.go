package server

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/repo"
)

type IntakeServer struct {
	intakev1.UnimplementedIntakeServiceServer
	repo repo.ApplicationRepo
}

func New(r repo.ApplicationRepo) *IntakeServer {
	return &IntakeServer{repo: r}
}

func (s *IntakeServer) SubmitApplication(ctx context.Context, req *intakev1.SubmitApplicationRequest) (*intakev1.SubmitApplicationResponse, error) {
	if err := validateSubmit(req); err != nil {
		return nil, err
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
		return nil, status.Errorf(codes.Internal, "insert: %v", err)
	}

	return &intakev1.SubmitApplicationResponse{ApplicationId: rec.ID}, nil
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
