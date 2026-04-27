// Package httpgateway exposes a thin REST endpoint that proxies to the
// intake gRPC server. Browsers (and the Chrome extension in Phase 5.3)
// cannot speak gRPC natively, so this gateway accepts JSON over HTTP and
// forwards each request to the local gRPC port — preserving the
// Authorization header so every existing interceptor (auth, rate-limit,
// logging, recovery) fires unchanged.
//
// The alternative paths considered:
//   - grpc-gateway codegen: requires extra protoc plugins and an annotated
//     proto file; heavy for one endpoint.
//   - Connect-RPC: requires protoc-gen-connect-go and a code-generation step.
//
// A hand-rolled proxy is the smallest thing that works for the one
// endpoint the extension needs (SubmitApplication).
package httpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxBodyBytes caps incoming request payloads so a misbehaving client
// can't exhaust memory. 64 KiB is generous for one application JSON.
const maxBodyBytes = 64 * 1024

// SubmitClient is the slice of intakev1.IntakeServiceClient the gateway
// actually uses. Narrowing the interface keeps tests trivial to fake.
type SubmitClient interface {
	SubmitApplication(ctx context.Context, req *intakev1.SubmitApplicationRequest, opts ...grpc.CallOption) (*intakev1.SubmitApplicationResponse, error)
}

// Gateway turns JSON-over-HTTP requests into gRPC calls.
type Gateway struct {
	client     SubmitClient
	logger     *slog.Logger
	corsOrigin string
}

// New constructs a Gateway. corsOrigin is echoed in the
// Access-Control-Allow-Origin header; use "*" for dev only.
func New(client SubmitClient, logger *slog.Logger, corsOrigin string) *Gateway {
	if logger == nil {
		logger = slog.Default()
	}
	return &Gateway{client: client, logger: logger, corsOrigin: corsOrigin}
}

// Handler returns an http.Handler ready to be mounted on a server.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/applications", g.handleSubmit)
	mux.HandleFunc("/healthz", g.handleHealth)
	return g.withCORS(mux)
}

// submitRequest is the wire shape for POST /v1/applications. Field names
// use snake_case to match the proto JSON conventions the dashboard will
// follow in Phase 9.
type submitRequest struct {
	UserID    string `json:"user_id"`
	JobTitle  string `json:"job_title"`
	Company   string `json:"company"`
	URL       string `json:"url"`
	Source    string `json:"source"`     // optional; defaults to SOURCE_CHROME_EXT
	AppliedAt string `json:"applied_at"` // optional RFC3339; defaults to now
}

type submitResponse struct {
	ApplicationID string `json:"application_id"`
}

func (g *Gateway) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (g *Gateway) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		writeError(w, http.StatusUnauthorized, "missing authorization header")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	var in submitRequest
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	source, err := parseSource(in.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	appliedAt, err := parseAppliedAt(in.AppliedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Forward the bearer token so the gRPC auth interceptor can verify it.
	ctx := metadata.AppendToOutgoingContext(r.Context(), "authorization", authHeader)

	resp, err := g.client.SubmitApplication(ctx, &intakev1.SubmitApplicationRequest{
		UserId:    in.UserID,
		JobTitle:  in.JobTitle,
		Company:   in.Company,
		Url:       in.URL,
		Source:    source,
		AppliedAt: appliedAt,
	})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcCodeToHTTP(st.Code()), st.Message())
		return
	}

	writeJSON(w, http.StatusOK, submitResponse{ApplicationID: resp.GetApplicationId()})
}

func (g *Gateway) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", g.corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// parseSource maps the optional JSON "source" field to the proto enum.
// Empty string defaults to CHROME_EXT — the gateway's primary client.
func parseSource(s string) (intakev1.Source, error) {
	if s == "" {
		return intakev1.Source_SOURCE_CHROME_EXT, nil
	}
	v, ok := intakev1.Source_value[s]
	if !ok || intakev1.Source(v) == intakev1.Source_SOURCE_UNSPECIFIED {
		return 0, errors.New("invalid source value")
	}
	return intakev1.Source(v), nil
}

func parseAppliedAt(s string) (*timestamppb.Timestamp, error) {
	if s == "" {
		return timestamppb.Now(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, errors.New("applied_at must be RFC3339")
	}
	return timestamppb.New(t), nil
}

// grpcCodeToHTTP follows the canonical mapping documented in google.api.Code.
func grpcCodeToHTTP(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
