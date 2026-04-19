package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Clock is the time source for the token bucket. Injecting it lets tests
// advance time deterministically instead of sleeping.
type Clock interface {
	Now() time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// WallClock is the production Clock implementation.
var WallClock Clock = wallClock{}

// UserExtractor pulls the authenticated user ID out of a request context.
// Returning ("", false) signals an unauthenticated request; those are passed
// through without rate limiting (the auth interceptor already guards them).
type UserExtractor func(ctx context.Context) (string, bool)

// UserRateLimiter maintains one token-bucket limiter per user.
// Limiters are created on first use and never evicted — fine for a user base
// in the thousands. Add LRU eviction if that changes.
type UserRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
	clock    Clock
}

// New returns a UserRateLimiter. rps is the steady-state refill rate (tokens/second);
// burst is the initial token count and the maximum the bucket can hold.
func New(rps float64, burst int, clock Clock) *UserRateLimiter {
	return &UserRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rate.Limit(rps),
		burst:    burst,
		clock:    clock,
	}
}

// Allow reports whether userID may make a request right now, consuming one token.
func (u *UserRateLimiter) Allow(userID string) bool {
	return u.limiterFor(userID).AllowN(u.clock.Now(), 1)
}

// limiterFor returns the per-user limiter, creating it on first access.
func (u *UserRateLimiter) limiterFor(userID string) *rate.Limiter {
	u.mu.Lock()
	defer u.mu.Unlock()
	l, ok := u.limiters[userID]
	if !ok {
		l = rate.NewLimiter(u.rps, u.burst)
		u.limiters[userID] = l
	}
	return l
}

// UnaryServerInterceptor returns a gRPC unary interceptor that enforces per-user
// rate limits. The extract function determines user identity from the context —
// pass auth.UserFromContext in production.
func UnaryServerInterceptor(l *UserRateLimiter, extract UserExtractor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		userID, ok := extract(ctx)
		if !ok {
			return handler(ctx, req)
		}
		if !l.Allow(userID) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}
