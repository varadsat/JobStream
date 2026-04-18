package jobstreamv1_test

import (
	"testing"
	"time"

	jobstreamv1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestApplicationFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	app := &jobstreamv1.Application{
		Id:            "app-1",
		UserId:        "user-1",
		JobTitle:      "Software Engineer",
		Company:       "Acme Corp",
		Url:           "https://acme.example/jobs/1",
		Source:        jobstreamv1.Source_SOURCE_DASHBOARD,
		Status:        jobstreamv1.Status_STATUS_APPLIED,
		AppliedAt:     timestamppb.New(now),
		CreatedAt:     timestamppb.New(now),
		SchemaVersion: "1.0",
	}

	if app.GetId() != "app-1" {
		t.Errorf("id: got %q, want %q", app.GetId(), "app-1")
	}
	if app.GetSource() != jobstreamv1.Source_SOURCE_DASHBOARD {
		t.Errorf("source: got %v", app.GetSource())
	}
	if app.GetStatus() != jobstreamv1.Status_STATUS_APPLIED {
		t.Errorf("status: got %v", app.GetStatus())
	}
	if got := app.GetAppliedAt().AsTime(); !got.Equal(now) {
		t.Errorf("applied_at: got %v, want %v", got, now)
	}
	if app.GetSchemaVersion() != "1.0" {
		t.Errorf("schema_version: got %q", app.GetSchemaVersion())
	}
}

func TestSourceEnumCoverage(t *testing.T) {
	sources := []jobstreamv1.Source{
		jobstreamv1.Source_SOURCE_UNSPECIFIED,
		jobstreamv1.Source_SOURCE_DASHBOARD,
		jobstreamv1.Source_SOURCE_CHROME_EXT,
		jobstreamv1.Source_SOURCE_SCRAPER,
	}
	for _, s := range sources {
		if s.String() == "" {
			t.Errorf("Source(%d).String() is empty", s)
		}
	}
}

func TestStatusEnumCoverage(t *testing.T) {
	statuses := []jobstreamv1.Status{
		jobstreamv1.Status_STATUS_UNSPECIFIED,
		jobstreamv1.Status_STATUS_APPLIED,
		jobstreamv1.Status_STATUS_INTERVIEWING,
		jobstreamv1.Status_STATUS_OFFER,
		jobstreamv1.Status_STATUS_REJECTED,
	}
	for _, s := range statuses {
		if s.String() == "" {
			t.Errorf("Status(%d).String() is empty", s)
		}
	}
}

func TestBatchSubmitResultOneof(t *testing.T) {
	success := &jobstreamv1.BatchSubmitResult{
		Outcome: &jobstreamv1.BatchSubmitResult_ApplicationId{ApplicationId: "app-42"},
	}
	failure := &jobstreamv1.BatchSubmitResult{
		Outcome: &jobstreamv1.BatchSubmitResult_ErrorMessage{ErrorMessage: "duplicate"},
	}

	if _, ok := success.Outcome.(*jobstreamv1.BatchSubmitResult_ApplicationId); !ok {
		t.Error("success result: expected ApplicationId oneof")
	}
	if _, ok := failure.Outcome.(*jobstreamv1.BatchSubmitResult_ErrorMessage); !ok {
		t.Error("failure result: expected ErrorMessage oneof")
	}
}

func TestSubmitApplicationRequest(t *testing.T) {
	req := &jobstreamv1.SubmitApplicationRequest{
		UserId:   "user-1",
		JobTitle: "SRE",
		Company:  "Initech",
		Url:      "https://initech.example/jobs/2",
		Source:   jobstreamv1.Source_SOURCE_SCRAPER,
	}
	if req.GetUserId() != "user-1" {
		t.Errorf("user_id: got %q", req.GetUserId())
	}
	if req.GetSource() != jobstreamv1.Source_SOURCE_SCRAPER {
		t.Errorf("source: got %v", req.GetSource())
	}
}
