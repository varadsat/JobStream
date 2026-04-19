package middleware_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/varad/jobstream/services/intake/internal/middleware"
)

// panicCapture is a slog.Handler that records every log record for inspection.
// Reuses the captureHandler from logging_test.go — but since tests are in the
// same package (_test) we must redeclare it. Use a different name to avoid
// a redeclaration error across test files in the same package.
type recCapture struct {
	records []slog.Record
}

func (h *recCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *recCapture) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *recCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recCapture) WithGroup(_ string) slog.Handler      { return h }

func (h *recCapture) last() (slog.Record, bool) {
	if len(h.records) == 0 {
		return slog.Record{}, false
	}
	return h.records[len(h.records)-1], true
}

func recAttrString(r slog.Record, key string) (string, bool) {
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

var recoveryDummyInfo = &grpc.UnaryServerInfo{FullMethod: "/jobstream.v1.IntakeService/SubmitApplication"}

func newRecoveryInterceptor() (*recCapture, grpc.UnaryServerInterceptor) {
	h := &recCapture{}
	return h, middleware.RecoveryUnaryInterceptor(slog.New(h))
}

// --- RecoveryUnaryInterceptor tests ---

func TestRecovery_NoPanicPassesThrough(t *testing.T) {
	_, i := newRecoveryInterceptor()

	resp, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("response: want %q, got %v", "ok", resp)
	}
}

func TestRecovery_PanicStringReturnsInternal(t *testing.T) {
	_, i := newRecoveryInterceptor()

	resp, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic("something went very wrong")
	})
	if resp != nil {
		t.Errorf("resp should be nil after panic, got %v", resp)
	}
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("code: want Internal, got %v", code)
	}
}

func TestRecovery_PanicErrorReturnsInternal(t *testing.T) {
	_, i := newRecoveryInterceptor()

	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic(fmt.Errorf("db connection lost"))
	})
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("code: want Internal, got %v", code)
	}
}

func TestRecovery_PanicNilReturnsInternal(t *testing.T) {
	// In Go 1.21+, panic(nil) produces a *runtime.PanicNilError so recover()
	// returns a non-nil value and our interceptor correctly catches it.
	_, i := newRecoveryInterceptor()

	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic(nil)
	})
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("panic(nil) should return Internal, got %v", code)
	}
}

func TestRecovery_PanicLogsAtError(t *testing.T) {
	h, i := newRecoveryInterceptor()

	_, _ = i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic("boom")
	})

	rec, ok := h.last()
	if !ok {
		t.Fatal("expected a log record after panic")
	}
	if rec.Level != slog.LevelError {
		t.Errorf("log level: want Error, got %v", rec.Level)
	}
}

func TestRecovery_PanicLogContainsPanicValue(t *testing.T) {
	h, i := newRecoveryInterceptor()

	_, _ = i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic("the-panic-message")
	})

	rec, _ := h.last()
	v, ok := recAttrString(rec, "panic")
	if !ok {
		t.Fatal("log record missing 'panic' attr")
	}
	if v != "the-panic-message" {
		t.Errorf("panic attr: want %q, got %q", "the-panic-message", v)
	}
}

func TestRecovery_PanicLogContainsStack(t *testing.T) {
	h, i := newRecoveryInterceptor()

	_, _ = i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic("with-stack")
	})

	rec, _ := h.last()
	if _, ok := recAttrString(rec, "stack"); !ok {
		t.Error("log record missing 'stack' attr")
	}
}

func TestRecovery_PanicLogContainsMethod(t *testing.T) {
	h, i := newRecoveryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/foo.Bar/Baz"}

	_, _ = i(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		panic("method-check")
	})

	rec, _ := h.last()
	if v, ok := recAttrString(rec, "method"); !ok || v != "/foo.Bar/Baz" {
		t.Errorf("method attr: want %q, got %q", "/foo.Bar/Baz", v)
	}
}

func TestRecovery_DoesNotRepanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("interceptor should have absorbed the panic, but it escaped: %v", r)
		}
	}()

	_, i := newRecoveryInterceptor()
	_, _ = i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		panic("escape-test")
	})
}

// --- Error mapping via ToGRPCStatus (applied on non-panic path) ---

func TestRecovery_PlainErrorBecomesInternal(t *testing.T) {
	_, i := newRecoveryInterceptor()

	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, errors.New("unmapped domain error")
	})
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("plain error: want Internal, got %v", code)
	}
}

func TestRecovery_GRPCStatusPassesThrough(t *testing.T) {
	_, i := newRecoveryInterceptor()

	want := status.Error(codes.NotFound, "not found")
	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, want
	})
	if err != want {
		t.Errorf("gRPC status error should pass through unchanged: want %v, got %v", want, err)
	}
}

func TestRecovery_CanceledContextMapped(t *testing.T) {
	_, i := newRecoveryInterceptor()

	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, context.Canceled
	})
	if code := status.Code(err); code != codes.Canceled {
		t.Errorf("context.Canceled: want Canceled, got %v", code)
	}
}

func TestRecovery_DeadlineExceededMapped(t *testing.T) {
	_, i := newRecoveryInterceptor()

	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, context.DeadlineExceeded
	})
	if code := status.Code(err); code != codes.DeadlineExceeded {
		t.Errorf("context.DeadlineExceeded: want DeadlineExceeded, got %v", code)
	}
}

func TestRecovery_NilErrorPassesThrough(t *testing.T) {
	_, i := newRecoveryInterceptor()

	_, err := i(context.Background(), nil, recoveryDummyInfo, func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Errorf("nil error should remain nil, got %v", err)
	}
}

// --- ToGRPCStatus unit tests (the mapping function in isolation) ---

func TestToGRPCStatus_Nil(t *testing.T) {
	if err := middleware.ToGRPCStatus(nil); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestToGRPCStatus_AlreadyGRPCStatus(t *testing.T) {
	orig := status.Error(codes.NotFound, "gone")
	if got := middleware.ToGRPCStatus(orig); got != orig {
		t.Errorf("gRPC status should be returned unchanged: want %v, got %v", orig, got)
	}
}

func TestToGRPCStatus_PlainErrorBecomesInternal(t *testing.T) {
	err := middleware.ToGRPCStatus(errors.New("raw error"))
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("want Internal, got %v", code)
	}
}

func TestToGRPCStatus_ContextCanceled(t *testing.T) {
	err := middleware.ToGRPCStatus(context.Canceled)
	if code := status.Code(err); code != codes.Canceled {
		t.Errorf("want Canceled, got %v", code)
	}
}

func TestToGRPCStatus_ContextDeadlineExceeded(t *testing.T) {
	err := middleware.ToGRPCStatus(context.DeadlineExceeded)
	if code := status.Code(err); code != codes.DeadlineExceeded {
		t.Errorf("want DeadlineExceeded, got %v", code)
	}
}

func TestToGRPCStatus_WrappedContextCanceled(t *testing.T) {
	wrapped := fmt.Errorf("operation failed: %w", context.Canceled)
	err := middleware.ToGRPCStatus(wrapped)
	if code := status.Code(err); code != codes.Canceled {
		t.Errorf("wrapped context.Canceled: want Canceled, got %v", code)
	}
}

func TestToGRPCStatus_WrappedDeadlineExceeded(t *testing.T) {
	wrapped := fmt.Errorf("timed out: %w", context.DeadlineExceeded)
	err := middleware.ToGRPCStatus(wrapped)
	if code := status.Code(err); code != codes.DeadlineExceeded {
		t.Errorf("wrapped context.DeadlineExceeded: want DeadlineExceeded, got %v", code)
	}
}
