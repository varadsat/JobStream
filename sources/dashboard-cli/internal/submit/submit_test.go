package submit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/sources/dashboard-cli/internal/submit"
)

func validArgs() submit.Args {
	return submit.Args{
		UserID:   "u-1",
		JobTitle: "Senior Engineer",
		Company:  "Acme",
		URL:      "https://jobs.example.com/123",
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func TestBuildRequest_happyPathPopulatesAllFields(t *testing.T) {
	req, err := submit.BuildRequest(validArgs())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if req.GetUserId() != "u-1" {
		t.Errorf("user id: %q", req.GetUserId())
	}
	if req.GetJobTitle() != "Senior Engineer" {
		t.Errorf("job title: %q", req.GetJobTitle())
	}
	if req.GetCompany() != "Acme" {
		t.Errorf("company: %q", req.GetCompany())
	}
	if req.GetUrl() != "https://jobs.example.com/123" {
		t.Errorf("url: %q", req.GetUrl())
	}
	if req.GetSource() != intakev1.Source_SOURCE_DASHBOARD {
		t.Errorf("source: %v (want SOURCE_DASHBOARD)", req.GetSource())
	}
}

func TestBuildRequest_emptyAppliedAtUsesNow(t *testing.T) {
	args := validArgs()
	req, err := submit.BuildRequest(args)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := req.GetAppliedAt().AsTime()
	want := args.Now()
	if !got.Equal(want) {
		t.Errorf("applied_at: got %s, want %s", got, want)
	}
}

func TestBuildRequest_explicitAppliedAtParses(t *testing.T) {
	args := validArgs()
	args.AppliedAt = "2025-06-01T12:34:56Z"
	req, err := submit.BuildRequest(args)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := req.GetAppliedAt().AsTime()
	want, _ := time.Parse(time.RFC3339, "2025-06-01T12:34:56Z")
	if !got.Equal(want) {
		t.Errorf("applied_at: got %s, want %s", got, want)
	}
}

func TestBuildRequest_invalidAppliedAtErrors(t *testing.T) {
	args := validArgs()
	args.AppliedAt = "yesterday"
	if _, err := submit.BuildRequest(args); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestBuildRequest_missingFieldsReturnSentinelErrors(t *testing.T) {
	cases := map[string]struct {
		mut  func(*submit.Args)
		want error
	}{
		"user":     {func(a *submit.Args) { a.UserID = "" }, submit.ErrUserIDRequired},
		"title":    {func(a *submit.Args) { a.JobTitle = "" }, submit.ErrJobTitleRequired},
		"company":  {func(a *submit.Args) { a.Company = "" }, submit.ErrCompanyRequired},
		"url":      {func(a *submit.Args) { a.URL = "" }, submit.ErrURLRequired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := validArgs()
			tc.mut(&a)
			_, err := submit.BuildRequest(a)
			if !errors.Is(err, tc.want) {
				t.Errorf("err: got %v, want %v", err, tc.want)
			}
		})
	}
}

type fakeSubmitter struct {
	gotReq *intakev1.SubmitApplicationRequest
	resp   *intakev1.SubmitApplicationResponse
	err    error
}

func (f *fakeSubmitter) Submit(_ context.Context, req *intakev1.SubmitApplicationRequest) (*intakev1.SubmitApplicationResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func TestRun_returnsApplicationID(t *testing.T) {
	fake := &fakeSubmitter{resp: &intakev1.SubmitApplicationResponse{ApplicationId: "app-123"}}
	id, err := submit.Run(context.Background(), fake, validArgs())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if id != "app-123" {
		t.Errorf("id: got %q, want app-123", id)
	}
	if fake.gotReq == nil {
		t.Fatal("submitter was not called")
	}
}

func TestRun_propagatesBuildError(t *testing.T) {
	fake := &fakeSubmitter{}
	args := validArgs()
	args.UserID = ""
	if _, err := submit.Run(context.Background(), fake, args); !errors.Is(err, submit.ErrUserIDRequired) {
		t.Errorf("err: got %v, want %v", err, submit.ErrUserIDRequired)
	}
	if fake.gotReq != nil {
		t.Error("submitter should not be called when build fails")
	}
}

func TestRun_propagatesRPCError(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &fakeSubmitter{err: wantErr}
	_, err := submit.Run(context.Background(), fake, validArgs())
	if !errors.Is(err, wantErr) {
		t.Errorf("err: got %v, want %v", err, wantErr)
	}
}
