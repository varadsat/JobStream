package server_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/repo"
	"github.com/varad/jobstream/services/intake/internal/server"
)

// TestBatchSubmit_AllSucceed verifies the happy path: every item gets an
// application_id, results are parallel to the input.
func TestBatchSubmit_AllSucceed(t *testing.T) {
	var calls atomic.Int32
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			n := calls.Add(1)
			return repo.ApplicationRecord{ID: fmt.Sprintf("id-%d", n)}, nil
		},
	}
	conn := startTestServer(t, r)
	client := intakev1.NewIntakeServiceClient(conn)

	resp, err := client.BatchSubmitApplication(context.Background(), &intakev1.BatchSubmitApplicationRequest{
		Applications: []*intakev1.SubmitApplicationRequest{
			validReq("u1", "Acme"),
			validReq("u1", "Globex"),
			validReq("u1", "Initech"),
		},
	})
	if err != nil {
		t.Fatalf("BatchSubmitApplication: %v", err)
	}
	if got, want := len(resp.Results), 3; got != want {
		t.Fatalf("results: want %d, got %d", want, got)
	}
	for i, res := range resp.Results {
		if res.GetErrorMessage() != "" {
			t.Errorf("result[%d]: unexpected error %q", i, res.GetErrorMessage())
		}
		if res.GetApplicationId() == "" {
			t.Errorf("result[%d]: empty application_id", i)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("Insert calls: want 3, got %d", calls.Load())
	}
}

// TestBatchSubmit_PartialFailures shows the headline behaviour: bad items
// surface as per-item errors while neighbouring valid items still succeed.
func TestBatchSubmit_PartialFailures(t *testing.T) {
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{ID: "ok-id"}, nil
		},
	}
	conn := startTestServer(t, r)
	client := intakev1.NewIntakeServiceClient(conn)

	apps := []*intakev1.SubmitApplicationRequest{
		validReq("u1", "Acme"),                // 0: ok
		{UserId: "u1", JobTitle: "T", Company: "C", Url: "https://x.example"}, // 1: source unspecified
		{UserId: "u1", JobTitle: "T", Company: "C", Source: intakev1.Source_SOURCE_DASHBOARD}, // 2: missing url
		validReq("u1", "Globex"),              // 3: ok
		{UserId: "u1", JobTitle: "T", Company: "C", Url: "not-a-url", Source: intakev1.Source_SOURCE_DASHBOARD}, // 4: bad url
	}
	resp, err := client.BatchSubmitApplication(context.Background(), &intakev1.BatchSubmitApplicationRequest{
		Applications: apps,
	})
	if err != nil {
		t.Fatalf("BatchSubmitApplication: %v", err)
	}
	if len(resp.Results) != len(apps) {
		t.Fatalf("results: want %d, got %d", len(apps), len(resp.Results))
	}

	// Indices 0 and 3 succeed.
	if resp.Results[0].GetApplicationId() == "" {
		t.Error("results[0]: want success, got error: " + resp.Results[0].GetErrorMessage())
	}
	if resp.Results[3].GetApplicationId() == "" {
		t.Error("results[3]: want success, got error: " + resp.Results[3].GetErrorMessage())
	}

	// Indices 1, 2, 4 fail with descriptive messages.
	for _, i := range []int{1, 2, 4} {
		if msg := resp.Results[i].GetErrorMessage(); msg == "" {
			t.Errorf("results[%d]: want error, got success id %q", i, resp.Results[i].GetApplicationId())
		}
	}
	// Sanity: the messages should hint at what was wrong.
	if !strings.Contains(resp.Results[2].GetErrorMessage(), "url") {
		t.Errorf("results[2]: error should mention url, got %q", resp.Results[2].GetErrorMessage())
	}
}

// TestBatchSubmit_EmptyRejected — an empty batch is a caller bug, not a
// per-item failure, so it gets a top-level InvalidArgument.
func TestBatchSubmit_EmptyRejected(t *testing.T) {
	conn := startTestServer(t, &stubRepo{})
	_, err := intakev1.NewIntakeServiceClient(conn).BatchSubmitApplication(
		context.Background(),
		&intakev1.BatchSubmitApplicationRequest{},
	)
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code: want InvalidArgument, got %v", got)
	}
}

// TestBatchSubmit_OverLimitRejected — protect the server from megabatch DoS.
func TestBatchSubmit_OverLimitRejected(t *testing.T) {
	conn := startTestServer(t, &stubRepo{})
	apps := make([]*intakev1.SubmitApplicationRequest, server.MaxBatchSize+1)
	for i := range apps {
		apps[i] = validReq("u1", fmt.Sprintf("Co-%d", i))
	}
	_, err := intakev1.NewIntakeServiceClient(conn).BatchSubmitApplication(
		context.Background(),
		&intakev1.BatchSubmitApplicationRequest{Applications: apps},
	)
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code: want InvalidArgument, got %v", got)
	}
}

// TestBatchSubmit_AtLimitAccepted — a batch right at the cap must still work.
// Uses a shared atomic so the no-op insert still returns unique IDs.
func TestBatchSubmit_AtLimitAccepted(t *testing.T) {
	var n atomic.Int32
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			return repo.ApplicationRecord{ID: fmt.Sprintf("id-%d", n.Add(1))}, nil
		},
	}
	conn := startTestServer(t, r)

	apps := make([]*intakev1.SubmitApplicationRequest, server.MaxBatchSize)
	for i := range apps {
		apps[i] = validReq("u1", fmt.Sprintf("Co-%d", i))
	}
	resp, err := intakev1.NewIntakeServiceClient(conn).BatchSubmitApplication(
		context.Background(),
		&intakev1.BatchSubmitApplicationRequest{Applications: apps},
	)
	if err != nil {
		t.Fatalf("BatchSubmitApplication at limit: %v", err)
	}
	if len(resp.Results) != server.MaxBatchSize {
		t.Errorf("results: want %d, got %d", server.MaxBatchSize, len(resp.Results))
	}
}

// TestBatchSubmit_RepoErrorIsPerItem — a transient DB failure for one item
// must not abort the whole batch.
func TestBatchSubmit_RepoErrorIsPerItem(t *testing.T) {
	var calls atomic.Int32
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			n := calls.Add(1)
			if n == 2 {
				return repo.ApplicationRecord{}, errors.New("pg conn reset")
			}
			return repo.ApplicationRecord{ID: fmt.Sprintf("id-%d", n)}, nil
		},
	}
	conn := startTestServer(t, r)
	client := intakev1.NewIntakeServiceClient(conn)

	resp, err := client.BatchSubmitApplication(context.Background(), &intakev1.BatchSubmitApplicationRequest{
		Applications: []*intakev1.SubmitApplicationRequest{
			validReq("u1", "Acme"),
			validReq("u1", "Globex"), // this one fails the insert
			validReq("u1", "Initech"),
		},
	})
	if err != nil {
		t.Fatalf("BatchSubmitApplication: %v", err)
	}
	if resp.Results[0].GetApplicationId() == "" {
		t.Error("results[0]: want success")
	}
	if resp.Results[1].GetErrorMessage() == "" {
		t.Error("results[1]: want error from repo")
	}
	if resp.Results[2].GetApplicationId() == "" {
		t.Error("results[2]: want success after a sibling failed")
	}
}

// TestBatchSubmit_DedupHitDoesNotInsert — dedup hits inside a batch must
// short-circuit the repo just as they do for the single submit RPC.
func TestBatchSubmit_DedupHitDoesNotInsert(t *testing.T) {
	insertCalled := false
	r := &stubRepo{
		insertFn: func(_ context.Context, _ repo.InsertParams) (repo.ApplicationRecord, error) {
			insertCalled = true
			return repo.ApplicationRecord{}, nil
		},
	}
	d := &stubDeduper{
		checkFn: func(_ context.Context, _, _, _ string) (string, bool, error) {
			return "cached-batch-id", true, nil
		},
	}
	conn := startTestServerWithDeduper(t, r, d)

	resp, err := intakev1.NewIntakeServiceClient(conn).BatchSubmitApplication(
		context.Background(),
		&intakev1.BatchSubmitApplicationRequest{
			Applications: []*intakev1.SubmitApplicationRequest{validReq("u1", "Acme")},
		},
	)
	if err != nil {
		t.Fatalf("BatchSubmitApplication: %v", err)
	}
	if got := resp.Results[0].GetApplicationId(); got != "cached-batch-id" {
		t.Errorf("ApplicationId: want cached-batch-id, got %s", got)
	}
	if insertCalled {
		t.Error("Insert must not be called on dedup hit inside a batch")
	}
}

// validReq is a small builder that returns a well-formed submit request for
// the given user and company. Job title varies with company so dedup keys
// don't collide across cases.
func validReq(userID, company string) *intakev1.SubmitApplicationRequest {
	return &intakev1.SubmitApplicationRequest{
		UserId:   userID,
		JobTitle: "Software Engineer at " + company,
		Company:  company,
		Url:      "https://" + strings.ToLower(company) + ".example/jobs/1",
		Source:   intakev1.Source_SOURCE_DASHBOARD,
	}
}
