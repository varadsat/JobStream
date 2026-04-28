package server

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/canonical"
	"github.com/varad/jobstream/services/intake/internal/dedup"
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/transform"
)

// TopicJobsSubmitted is the Kafka topic the outbox relay (Phase 6.2) will
// publish to. It lives here for now because intake is the only producer; it
// will move to a shared event-schema package once a second producer appears.
const TopicJobsSubmitted = "jobs.submitted"

// MaxBatchSize caps a single BatchSubmitApplication request. Set high enough
// for routine bulk imports (a typical scraper run is a few hundred), low
// enough to keep request size bounded and prevent a single caller from
// hogging the server for seconds at a time.
const MaxBatchSize = 500

type IntakeServer struct {
	intakev1.UnimplementedIntakeServiceServer
	repo    repo.ApplicationRepo
	deduper dedup.Deduper
}

func New(r repo.ApplicationRepo, d dedup.Deduper) *IntakeServer {
	return &IntakeServer{repo: r, deduper: d}
}

// errPendingDuplicate is returned by submitOne when another in-flight request
// holds the dedup claim and has not yet committed an ID. SubmitApplication
// surfaces this as AlreadyExists; BatchSubmitApplication records it as a
// per-item failure.
var errPendingDuplicate = errors.New("duplicate submission in progress")

// SubmitApplication records a single job application. Errors are mapped to
// gRPC codes here, while submitOne stays transport-agnostic so it can be
// reused by BatchSubmitApplication.
func (s *IntakeServer) SubmitApplication(ctx context.Context, req *intakev1.SubmitApplicationRequest) (*intakev1.SubmitApplicationResponse, error) {
	id, err := s.submitOne(ctx, req)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &intakev1.SubmitApplicationResponse{ApplicationId: id}, nil
}

// BatchSubmitApplication accepts up to MaxBatchSize applications. Per-item
// failures are normal — the RPC itself only fails for batch-level problems
// (size limits, missing payload). Each result is parallel to its input index.
func (s *IntakeServer) BatchSubmitApplication(ctx context.Context, req *intakev1.BatchSubmitApplicationRequest) (*intakev1.BatchSubmitApplicationResponse, error) {
	apps := req.GetApplications()
	if len(apps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "applications must not be empty")
	}
	if len(apps) > MaxBatchSize {
		return nil, status.Errorf(codes.InvalidArgument,
			"batch size %d exceeds maximum of %d", len(apps), MaxBatchSize)
	}

	results := make([]*intakev1.BatchSubmitResult, len(apps))
	for i, item := range apps {
		// Honor cancellation between items so a 500-item batch on a closed
		// connection stops promptly instead of churning through the rest.
		if err := ctx.Err(); err != nil {
			results[i] = errResult(err)
			continue
		}
		id, err := s.submitOne(ctx, item)
		if err != nil {
			results[i] = errResult(err)
			continue
		}
		results[i] = &intakev1.BatchSubmitResult{
			Outcome: &intakev1.BatchSubmitResult_ApplicationId{ApplicationId: id},
		}
	}
	return &intakev1.BatchSubmitApplicationResponse{Results: results}, nil
}

// GetApplicationStatus fetches one application by ID, scoped to the user.
func (s *IntakeServer) GetApplicationStatus(ctx context.Context, req *intakev1.GetApplicationStatusRequest) (*intakev1.GetApplicationStatusResponse, error) {
	if req.GetApplicationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "application_id is required")
	}
	if req.GetUserId() == "" {
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

// submitOne is the canonical pipeline shared by both submit RPCs:
//
//	transform → validate → dedup-check → claim → insert → commit-dedup
//
// It returns plain Go errors; callers map them to gRPC codes (single submit)
// or to per-item error messages (batch).
func (s *IntakeServer) submitOne(ctx context.Context, req *intakev1.SubmitApplicationRequest) (string, error) {
	// Pick the per-source transformer up front so an unspecified/unknown
	// source surfaces as InvalidArgument rather than a validator error.
	tf, err := transform.For(req.GetSource())
	if err != nil {
		return "", err
	}

	event := tf(req)
	if err := event.Validate(); err != nil {
		return "", err
	}

	// Dedup: if a prior submission committed an ID, return it without
	// touching Postgres.
	if id, hit, err := s.deduper.Check(ctx, event.UserID, event.Company, event.JobTitle); err == nil && hit {
		return id, nil
	}

	// Claim the slot with SET NX so two concurrent inserts can't race past
	// each other. If Redis is unreachable (err != nil) we accept availability
	// over strict idempotency and proceed without a claim.
	if claimed, err := s.deduper.Claim(ctx, event.UserID, event.Company, event.JobTitle); err == nil && !claimed {
		// Another goroutine holds the slot. Re-check in case it just committed.
		if id, hit, _ := s.deduper.Check(ctx, event.UserID, event.Company, event.JobTitle); hit {
			return id, nil
		}
		return "", errPendingDuplicate
	}

	rec, err := s.repo.InsertWithOutbox(ctx, repo.InsertParams{
		UserID:        event.UserID,
		JobTitle:      event.JobTitle,
		Company:       event.Company,
		URL:           event.URL,
		Source:        event.Source,
		Status:        int32(intakev1.Status_STATUS_APPLIED),
		AppliedAt:     event.AppliedAt,
		SchemaVersion: event.SchemaVersion,
	}, TopicJobsSubmitted, buildJobsSubmittedPayload)
	if err != nil {
		// Free the pending claim so the next caller is not blocked on us.
		_ = s.deduper.Release(ctx, event.UserID, event.Company, event.JobTitle)
		return "", fmt.Errorf("insert: %w", err)
	}

	// Commit the real application ID so future Check calls return it.
	_ = s.deduper.Set(ctx, event.UserID, event.Company, event.JobTitle, rec.ID)

	return rec.ID, nil
}

// buildJobsSubmittedPayload serializes the canonical Application proto for
// the outbox. Using protobuf (not JSON) keeps the payload compact and locks
// the wire schema to the same .proto file consumers already depend on, so a
// breaking change shows up as a build failure rather than a parse error in
// production. Runs inside the dual-write tx; an error rolls everything back.
func buildJobsSubmittedPayload(rec repo.ApplicationRecord) ([]byte, error) {
	return proto.Marshal(&intakev1.Application{
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
	})
}

// toGRPCError maps the typed errors returned by submitOne to gRPC status
// codes. Validation/transform errors → InvalidArgument; pending-duplicate →
// AlreadyExists; everything else → Internal (with the underlying message
// preserved so logs/tests can surface it).
func toGRPCError(err error) error {
	switch {
	case errors.Is(err, errPendingDuplicate):
		return status.Error(codes.AlreadyExists, "duplicate submission in progress, retry shortly")
	case isValidationErr(err):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Errorf(codes.Internal, "%v", err)
	}
}

// errResult wraps a submitOne error into a per-item batch result. Batch
// callers see the validation message verbatim ("user_id is required") rather
// than a gRPC code, since the surrounding RPC succeeded.
func errResult(err error) *intakev1.BatchSubmitResult {
	return &intakev1.BatchSubmitResult{
		Outcome: &intakev1.BatchSubmitResult_ErrorMessage{ErrorMessage: err.Error()},
	}
}

// isValidationErr reports whether err originates from canonical.Validate or
// transform.For (both indicate bad input rather than a server fault).
func isValidationErr(err error) bool {
	switch {
	case errors.Is(err, canonical.ErrUserIDRequired),
		errors.Is(err, canonical.ErrJobTitleRequired),
		errors.Is(err, canonical.ErrCompanyRequired),
		errors.Is(err, canonical.ErrURLRequired),
		errors.Is(err, canonical.ErrInvalidURL),
		errors.Is(err, canonical.ErrSourceRequired),
		errors.Is(err, canonical.ErrSchemaRequired),
		errors.Is(err, transform.ErrUnsupportedSource):
		return true
	}
	return false
}
