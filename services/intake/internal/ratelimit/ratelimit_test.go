package ratelimit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/varad/jobstream/services/intake/internal/ratelimit"
)

// manualClock is a fake Clock for deterministic tests — no sleeping required.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *manualClock {
	return &manualClock{now: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ctxWithUser injects a user ID into a plain context for interceptor tests.
// Using a local typed key avoids collision with any production context keys.
type ctxKey struct{}

func withUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, userID)
}

func extractUser(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok && v != ""
}

var passThrough grpc.UnaryHandler = func(_ context.Context, _ any) (any, error) { return nil, nil }
var dummyInfo = &grpc.UnaryServerInfo{FullMethod: "/jobstream.v1.IntakeService/SubmitApplication"}

// --- UserRateLimiter unit tests ---

func TestAllow_BurstPassesThrough(t *testing.T) {
	const burst = 3
	clk := newClock()
	l := ratelimit.New(1, burst, clk)

	for i := range burst {
		if !l.Allow("alice") {
			t.Errorf("request %d/%d should be allowed within burst", i+1, burst)
		}
	}
}

func TestAllow_ExceedingBurstBlocks(t *testing.T) {
	clk := newClock()
	l := ratelimit.New(1, 2, clk) // burst=2

	l.Allow("alice") // token 1
	l.Allow("alice") // token 2
	if l.Allow("alice") {
		t.Error("request beyond burst should be denied")
	}
}

func TestAllow_TokensRefillOverTime(t *testing.T) {
	const rps = 2.0 // 2 tokens/sec → 1 token per 500ms
	clk := newClock()
	l := ratelimit.New(rps, 1, clk) // burst=1

	if !l.Allow("alice") {
		t.Fatal("first request should be allowed (full burst)")
	}
	if l.Allow("alice") {
		t.Fatal("second request should be denied (bucket empty)")
	}

	clk.Advance(time.Second / time.Duration(rps)) // advance exactly 1 token's worth
	if !l.Allow("alice") {
		t.Error("request after refill period should be allowed")
	}
}

func TestAllow_UsersAreIndependent(t *testing.T) {
	clk := newClock()
	l := ratelimit.New(1, 1, clk) // burst=1

	l.Allow("alice") // drains alice's bucket

	if !l.Allow("bob") {
		t.Error("bob's bucket should be independent of alice's")
	}
}

func TestAllow_SameUserReusesLimiter(t *testing.T) {
	clk := newClock()
	l := ratelimit.New(1, 2, clk) // burst=2

	l.Allow("alice") // token 1
	l.Allow("alice") // token 2
	// alice's bucket is now empty; a new limiter would have reset it
	if l.Allow("alice") {
		t.Error("alice's bucket should still be empty on third consecutive call")
	}
}

// --- UnaryServerInterceptor tests ---

func TestInterceptor_AllowsWithinBurst(t *testing.T) {
	clk := newClock()
	i := ratelimit.UnaryServerInterceptor(ratelimit.New(1, 3, clk), extractUser)
	ctx := withUser(context.Background(), "alice")

	for n := range 3 {
		if _, err := i(ctx, nil, dummyInfo, passThrough); err != nil {
			t.Errorf("request %d should succeed, got %v", n+1, err)
		}
	}
}

func TestInterceptor_ReturnsResourceExhausted(t *testing.T) {
	clk := newClock()
	i := ratelimit.UnaryServerInterceptor(ratelimit.New(1, 1, clk), extractUser)
	ctx := withUser(context.Background(), "alice")

	_, _ = i(ctx, nil, dummyInfo, passThrough) // uses the one token

	_, err := i(ctx, nil, dummyInfo, passThrough)
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Errorf("code: want ResourceExhausted, got %v", code)
	}
}

func TestInterceptor_AllowsAfterRefill(t *testing.T) {
	const rps = 1.0
	clk := newClock()
	i := ratelimit.UnaryServerInterceptor(ratelimit.New(rps, 1, clk), extractUser)
	ctx := withUser(context.Background(), "alice")

	_, _ = i(ctx, nil, dummyInfo, passThrough) // drain

	clk.Advance(time.Second / time.Duration(rps))

	if _, err := i(ctx, nil, dummyInfo, passThrough); err != nil {
		t.Errorf("expected success after refill, got %v", err)
	}
}

func TestInterceptor_IndependentPerUser(t *testing.T) {
	clk := newClock()
	i := ratelimit.UnaryServerInterceptor(ratelimit.New(1, 1, clk), extractUser)

	ctxA := withUser(context.Background(), "alice")
	ctxB := withUser(context.Background(), "bob")

	_, _ = i(ctxA, nil, dummyInfo, passThrough) // drain alice

	if _, err := i(ctxB, nil, dummyInfo, passThrough); err != nil {
		t.Errorf("bob should be unaffected by alice's exhausted bucket, got %v", err)
	}
}

func TestInterceptor_PassesThroughUnauthenticated(t *testing.T) {
	clk := newClock()
	// Use an extractor that always returns false (no user in context)
	noUser := func(ctx context.Context) (string, bool) { return "", false }
	i := ratelimit.UnaryServerInterceptor(ratelimit.New(1, 0, clk), noUser) // burst=0 would block any user

	called := false
	handler := func(_ context.Context, _ any) (any, error) {
		called = true
		return nil, nil
	}
	if _, err := i(context.Background(), nil, dummyInfo, handler); err != nil {
		t.Fatalf("unauthenticated request should pass through, got %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}
}

func TestInterceptor_HandlerErrorPropagated(t *testing.T) {
	clk := newClock()
	i := ratelimit.UnaryServerInterceptor(ratelimit.New(10, 10, clk), extractUser)
	ctx := withUser(context.Background(), "alice")

	wantErr := status.Error(codes.Internal, "something broke")
	_, err := i(ctx, nil, dummyInfo, func(_ context.Context, _ any) (any, error) {
		return nil, wantErr
	})
	if err != wantErr {
		t.Errorf("handler error should propagate unchanged: want %v, got %v", wantErr, err)
	}
}
