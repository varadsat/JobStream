// Package submit builds and dispatches a single SubmitApplication RPC.
//
// The package is gRPC-free in its core: BuildRequest is pure, and Run
// depends on a Submitter interface so tests can fake the wire call.
package submit

import (
	"context"
	"errors"
	"fmt"
	"time"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Args is the user-facing input shape — one field per CLI flag.
// Now is injectable so tests can pin time without monkey-patching.
type Args struct {
	UserID    string
	JobTitle  string
	Company   string
	URL       string
	AppliedAt string // RFC3339; empty means "now".
	Now       func() time.Time
}

// Submitter is the seam over intakev1.IntakeServiceClient.SubmitApplication.
type Submitter interface {
	Submit(ctx context.Context, req *intakev1.SubmitApplicationRequest) (*intakev1.SubmitApplicationResponse, error)
}

// Validation errors. Returned synchronously so the CLI can complain about
// missing flags without paying for a round trip.
var (
	ErrUserIDRequired   = errors.New("--user-id is required")
	ErrJobTitleRequired = errors.New("--job-title is required")
	ErrCompanyRequired  = errors.New("--company is required")
	ErrURLRequired      = errors.New("--url is required")
)

// BuildRequest converts CLI args into a SubmitApplicationRequest.
// It performs cheap client-side validation and parses the optional
// applied_at timestamp. Heavier rules (URL format, dedup, etc.) are the
// server's job.
func BuildRequest(a Args) (*intakev1.SubmitApplicationRequest, error) {
	switch {
	case a.UserID == "":
		return nil, ErrUserIDRequired
	case a.JobTitle == "":
		return nil, ErrJobTitleRequired
	case a.Company == "":
		return nil, ErrCompanyRequired
	case a.URL == "":
		return nil, ErrURLRequired
	}

	now := a.Now
	if now == nil {
		now = time.Now
	}
	appliedAt := now()
	if a.AppliedAt != "" {
		t, err := time.Parse(time.RFC3339, a.AppliedAt)
		if err != nil {
			return nil, fmt.Errorf("--applied-at: %w", err)
		}
		appliedAt = t
	}

	return &intakev1.SubmitApplicationRequest{
		UserId:    a.UserID,
		JobTitle:  a.JobTitle,
		Company:   a.Company,
		Url:       a.URL,
		Source:    intakev1.Source_SOURCE_DASHBOARD,
		AppliedAt: timestamppb.New(appliedAt),
	}, nil
}

// Run builds the request and dispatches it. It returns the application_id
// produced by the server, or the first error along the way.
func Run(ctx context.Context, sub Submitter, args Args) (string, error) {
	req, err := BuildRequest(args)
	if err != nil {
		return "", err
	}
	resp, err := sub.Submit(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.GetApplicationId(), nil
}
