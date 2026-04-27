package httpgateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"github.com/varad/jobstream/services/intake/internal/httpgateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeClient struct {
	gotCtx context.Context
	gotReq *intakev1.SubmitApplicationRequest
	resp   *intakev1.SubmitApplicationResponse
	err    error
}

func (f *fakeClient) SubmitApplication(ctx context.Context, req *intakev1.SubmitApplicationRequest, _ ...grpc.CallOption) (*intakev1.SubmitApplicationResponse, error) {
	f.gotCtx = ctx
	f.gotReq = req
	return f.resp, f.err
}

func newServer(t *testing.T, fc *fakeClient) *httptest.Server {
	t.Helper()
	gw := httpgateway.New(fc, nil, "*")
	return httptest.NewServer(gw.Handler())
}

func post(t *testing.T, url string, headers map[string]string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestSubmit_happyPath(t *testing.T) {
	fc := &fakeClient{resp: &intakev1.SubmitApplicationResponse{ApplicationId: "app-1"}}
	srv := newServer(t, fc)
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer tok"},
		`{"user_id":"u","job_title":"SE","company":"Acme","url":"https://x/1"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, body)
	}
	var got struct {
		ApplicationID string `json:"application_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ApplicationID != "app-1" {
		t.Errorf("application_id: %q", got.ApplicationID)
	}
	if fc.gotReq.GetSource() != intakev1.Source_SOURCE_CHROME_EXT {
		t.Errorf("default source should be CHROME_EXT, got %v", fc.gotReq.GetSource())
	}
	if fc.gotReq.GetAppliedAt() == nil {
		t.Error("applied_at should default to now, got nil")
	}
}

func TestSubmit_forwardsAuthToGRPC(t *testing.T) {
	fc := &fakeClient{resp: &intakev1.SubmitApplicationResponse{ApplicationId: "id"}}
	srv := newServer(t, fc)
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer xyz"},
		`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y"}`)
	defer resp.Body.Close()

	md, ok := metadata.FromOutgoingContext(fc.gotCtx)
	if !ok {
		t.Fatal("expected outgoing metadata on forwarded ctx")
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer xyz" {
		t.Errorf("authorization metadata: got %v", got)
	}
}

func TestSubmit_missingAuthHeader_401(t *testing.T) {
	fc := &fakeClient{}
	srv := newServer(t, fc)
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications", nil,
		`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
	if fc.gotReq != nil {
		t.Error("gRPC should not be called when auth missing")
	}
}

func TestSubmit_invalidJSON_400(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer t"}, `{not json`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestSubmit_invalidAppliedAt_400(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer t"},
		`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y","applied_at":"yesterday"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestSubmit_invalidSourceString_400(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer t"},
		`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y","source":"NOPE"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestSubmit_explicitSourceParsed(t *testing.T) {
	fc := &fakeClient{resp: &intakev1.SubmitApplicationResponse{ApplicationId: "x"}}
	srv := newServer(t, fc)
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer t"},
		`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y","source":"SOURCE_DASHBOARD"}`)
	defer resp.Body.Close()

	if fc.gotReq.GetSource() != intakev1.Source_SOURCE_DASHBOARD {
		t.Errorf("source: got %v, want SOURCE_DASHBOARD", fc.gotReq.GetSource())
	}
}

func TestSubmit_grpcErrorsMapToHTTPCodes(t *testing.T) {
	cases := []struct {
		name     string
		grpcCode codes.Code
		wantHTTP int
	}{
		{"unauthenticated", codes.Unauthenticated, http.StatusUnauthorized},
		{"invalid_arg", codes.InvalidArgument, http.StatusBadRequest},
		{"rate_limit", codes.ResourceExhausted, http.StatusTooManyRequests},
		{"internal", codes.Internal, http.StatusInternalServerError},
		{"not_found", codes.NotFound, http.StatusNotFound},
		{"unavailable", codes.Unavailable, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClient{err: status.Error(tc.grpcCode, "boom")}
			srv := newServer(t, fc)
			defer srv.Close()

			resp := post(t, srv.URL+"/v1/applications",
				map[string]string{"Authorization": "Bearer t"},
				`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y"}`)
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantHTTP {
				t.Errorf("status: got %d, want %d", resp.StatusCode, tc.wantHTTP)
			}
		})
	}
}

func TestSubmit_nonGRPCErrorMapsTo500(t *testing.T) {
	fc := &fakeClient{err: errors.New("plain error")}
	srv := newServer(t, fc)
	defer srv.Close()

	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer t"},
		`{"user_id":"u","job_title":"t","company":"c","url":"https://x/y"}`)
	defer resp.Body.Close()

	// status.FromError on a plain error returns codes.Unknown → 500.
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", resp.StatusCode)
	}
}

func TestCORS_preflightReturns204(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/applications", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("CORS origin: %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestSubmit_methodNotAllowed(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/applications")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", resp.StatusCode)
	}
}

func TestSubmit_bodyTooLarge_413(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	big := strings.Repeat("A", 200*1024) // 200 KiB > 64 KiB cap
	resp := post(t, srv.URL+"/v1/applications",
		map[string]string{"Authorization": "Bearer t"},
		`{"user_id":"u","junk":"`+big+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", resp.StatusCode)
	}
}

func TestHealth_returnsOK(t *testing.T) {
	srv := newServer(t, &fakeClient{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}
