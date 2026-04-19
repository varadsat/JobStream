package middleware_test

import (
	"context"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/varad/jobstream/services/intake/internal/middleware"
)

// captureHandler collects slog records for inspection.
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *captureHandler) last() (slog.Record, bool) {
	if len(h.records) == 0 {
		return slog.Record{}, false
	}
	return h.records[len(h.records)-1], true
}

func attrString(r slog.Record, key string) (string, bool) {
	var val string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value.String()
			found = true
			return false
		}
		return true
	})
	return val, found
}

var dummyInfo = &grpc.UnaryServerInfo{FullMethod: "/jobstream.v1.IntakeService/SubmitApplication"}

func newInterceptor() (*captureHandler, grpc.UnaryServerInterceptor) {
	h := &captureHandler{}
	return h, middleware.UnaryServerInterceptor(slog.New(h))
}

func TestLogging_OKRequestLogsInfo(t *testing.T) {
	h, i := newInterceptor()
	_, err := i(context.Background(), nil, dummyInfo, func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec, ok := h.last()
	if !ok {
		t.Fatal("expected a log record")
	}
	if rec.Level != slog.LevelInfo {
		t.Errorf("level: want Info, got %v", rec.Level)
	}
	if v, ok := attrString(rec, "status"); !ok || v != codes.OK.String() {
		t.Errorf("status: want %q, got %q", codes.OK.String(), v)
	}
	if _, ok := attrString(rec, "duration"); !ok {
		t.Error("missing duration attr")
	}
	if _, ok := attrString(rec, "request_id"); !ok {
		t.Error("missing request_id attr")
	}
	if v, ok := attrString(rec, "method"); !ok || v != dummyInfo.FullMethod {
		t.Errorf("method: want %q, got %q", dummyInfo.FullMethod, v)
	}
}

func TestLogging_ClientErrorLogsWarn(t *testing.T) {
	cases := []struct {
		code codes.Code
	}{
		{codes.InvalidArgument},
		{codes.NotFound},
		{codes.Unauthenticated},
		{codes.PermissionDenied},
		{codes.ResourceExhausted},
		{codes.AlreadyExists},
		{codes.Canceled},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.code.String(), func(t *testing.T) {
			h, i := newInterceptor()
			_, _ = i(context.Background(), nil, dummyInfo, func(_ context.Context, _ any) (any, error) {
				return nil, status.Error(tc.code, "client problem")
			})
			rec, _ := h.last()
			if rec.Level != slog.LevelWarn {
				t.Errorf("level: want Warn, got %v", rec.Level)
			}
		})
	}
}

func TestLogging_ServerErrorLogsError(t *testing.T) {
	h, i := newInterceptor()
	_, _ = i(context.Background(), nil, dummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, status.Error(codes.Internal, "db exploded")
	})
	rec, _ := h.last()
	if rec.Level != slog.LevelError {
		t.Errorf("level: want Error, got %v", rec.Level)
	}
}

func TestLogging_ErrorAttrPresentOnFailure(t *testing.T) {
	h, i := newInterceptor()
	_, _ = i(context.Background(), nil, dummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, status.Error(codes.Internal, "something broke")
	})
	rec, _ := h.last()
	if _, ok := attrString(rec, "error"); !ok {
		t.Error("expected error attr when handler returns an error")
	}
}

func TestLogging_NoErrorAttrOnSuccess(t *testing.T) {
	h, i := newInterceptor()
	_, _ = i(context.Background(), nil, dummyInfo, func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	})
	rec, _ := h.last()
	if _, ok := attrString(rec, "error"); ok {
		t.Error("unexpected error attr on successful call")
	}
}

func TestLogging_RequestIDPropagatedFromMetadata(t *testing.T) {
	const wantID = "test-request-id-xyz"
	_, i := newInterceptor()

	md := metadata.Pairs("x-request-id", wantID)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var gotID string
	_, _ = i(ctx, nil, dummyInfo, func(ctx context.Context, _ any) (any, error) {
		gotID = middleware.RequestIDFromContext(ctx)
		return nil, nil
	})

	if gotID != wantID {
		t.Errorf("context request_id: want %q, got %q", wantID, gotID)
	}
}

func TestLogging_RequestIDFromMetadataAppearsInLog(t *testing.T) {
	const wantID = "log-check-id"
	h, i := newInterceptor()

	md := metadata.Pairs("x-request-id", wantID)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, _ = i(ctx, nil, dummyInfo, func(_ context.Context, _ any) (any, error) { return nil, nil })

	rec, _ := h.last()
	if v, ok := attrString(rec, "request_id"); !ok || v != wantID {
		t.Errorf("log request_id: want %q, got %q", wantID, v)
	}
}

func TestLogging_RequestIDGeneratedWhenAbsent(t *testing.T) {
	_, i := newInterceptor()

	ids := make([]string, 2)
	for j := range ids {
		var id string
		_, _ = i(context.Background(), nil, dummyInfo, func(ctx context.Context, _ any) (any, error) {
			id = middleware.RequestIDFromContext(ctx)
			return nil, nil
		})
		ids[j] = id
	}

	if ids[0] == "" || ids[1] == "" {
		t.Error("generated request IDs must be non-empty")
	}
	if ids[0] == ids[1] {
		t.Errorf("generated request IDs must be unique; both were %q", ids[0])
	}
}

func TestLogging_RequestIDFromContextEmptyWithoutInterceptor(t *testing.T) {
	if got := middleware.RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("want empty string from bare context, got %q", got)
	}
}
