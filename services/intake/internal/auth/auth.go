package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type contextKey string

const userIDKey contextKey = "user-id"

// Claims is the JWT payload. UserID is the one application-level field;
// standard expiry/issued-at live in RegisteredClaims.
type Claims struct {
	jwt.RegisteredClaims
	UserID string `json:"user_id"`
}

// Verifier validates a raw JWT string and returns its claims.
// The interface is the seam for swapping algorithms: HS256Verifier for local
// dev; an RS256/JWKS implementation can be dropped in without touching callers.
type Verifier interface {
	Verify(tokenString string) (Claims, error)
}

// HS256Verifier validates HS256-signed tokens with a symmetric secret.
type HS256Verifier struct {
	secret []byte
}

// NewHS256Verifier creates a verifier using the given secret.
func NewHS256Verifier(secret string) *HS256Verifier {
	return &HS256Verifier{secret: []byte(secret)}
}

func (v *HS256Verifier) Verify(tokenString string) (Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(tokenString, &claims, func(t *jwt.Token) (any, error) {
		// Reject tokens signed with any algorithm other than HS256.
		// Without this check an attacker could craft an "alg: none" token.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.secret, nil
	}, jwt.WithExpirationRequired())
	return claims, err
}

// UserFromContext returns the user_id stored in ctx by the auth interceptor.
// The second return value is false if the context has not been enriched by the
// interceptor (e.g., in tests that skip auth).
func UserFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(userIDKey).(string)
	return v, ok && v != ""
}

// UnaryServerInterceptor returns a gRPC unary interceptor that enforces JWT
// authentication. The health check endpoint is exempt so liveness probes work
// without credentials. All other methods must supply a valid Bearer token in
// the authorization metadata key; on success the token's user_id claim is
// stored in the context for downstream handlers via UserFromContext.
func UnaryServerInterceptor(v Verifier) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isExempt(info.FullMethod) {
			return handler(ctx, req)
		}

		raw, err := bearerFromMetadata(ctx)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}

		claims, err := v.Verify(raw)
		if err != nil {
			// Don't echo the parsing error; it may reveal signing internals.
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		if claims.UserID == "" {
			return nil, status.Error(codes.Unauthenticated, "token missing user_id claim")
		}

		ctx = context.WithValue(ctx, userIDKey, claims.UserID)
		return handler(ctx, req)
	}
}

// isExempt covers all methods under the gRPC health service so both Check and
// Watch work without credentials.
func isExempt(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/")
}

func bearerFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", errors.New("missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", errors.New("missing authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(vals[0], prefix) {
		return "", errors.New("authorization must use Bearer scheme")
	}
	return strings.TrimPrefix(vals[0], prefix), nil
}
