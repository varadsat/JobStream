package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/varad/jobstream/services/intake/internal/auth"
)

const testSecret = "super-secret-for-tests"

// makeToken builds a signed HS256 token with the given user_id and expiry.
func makeToken(t *testing.T, userID string, expiresIn time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     time.Now().Add(expiresIn).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

func makeExpiredToken(t *testing.T, userID string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     time.Now().Add(-time.Hour).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	return tok
}

func bearerCtx(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

var dummyInfo = &grpc.UnaryServerInfo{FullMethod: "/jobstream.v1.IntakeService/SubmitApplication"}

// --- HS256Verifier unit tests ---

func TestHS256Verifier_ValidToken(t *testing.T) {
	v := auth.NewHS256Verifier(testSecret)
	tok := makeToken(t, "user-abc", time.Hour)

	claims, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if claims.UserID != "user-abc" {
		t.Errorf("UserID: want %q, got %q", "user-abc", claims.UserID)
	}
}

func TestHS256Verifier_WrongSecret(t *testing.T) {
	tok := makeToken(t, "user-abc", time.Hour)
	v := auth.NewHS256Verifier("wrong-secret")

	_, err := v.Verify(tok)
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestHS256Verifier_ExpiredToken(t *testing.T) {
	v := auth.NewHS256Verifier(testSecret)
	tok := makeExpiredToken(t, "user-abc")

	_, err := v.Verify(tok)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestHS256Verifier_RS256TokenRejected(t *testing.T) {
	// Tokens signed with an unexpected algorithm must be rejected even if we
	// could verify them, to prevent algorithm-confusion attacks.
	// Generate a throw-away RSA key for signing; the verifier must reject it
	// regardless of whether the signature itself is valid.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	claims := jwt.MapClaims{
		"user_id": "user-abc",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(rsaKey)
	if err != nil {
		t.Fatalf("sign RS256 token: %v", err)
	}

	v := auth.NewHS256Verifier(testSecret)
	_, verifyErr := v.Verify(tok)
	if verifyErr == nil {
		t.Fatal("HS256Verifier must reject RS256-signed tokens")
	}
}

func TestHS256Verifier_MalformedToken(t *testing.T) {
	v := auth.NewHS256Verifier(testSecret)
	_, err := v.Verify("not.a.jwt")
	if err == nil {
		t.Fatal("expected error for malformed token")
	}
}

// --- UnaryServerInterceptor tests ---

func TestAuth_ValidTokenPassesThrough(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	tok := makeToken(t, "user-123", time.Hour)
	ctx := bearerCtx(tok)

	var gotUID string
	resp, err := i(ctx, nil, dummyInfo, func(ctx context.Context, _ any) (any, error) {
		gotUID, _ = auth.UserFromContext(ctx)
		return gotUID, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "user-123" {
		t.Errorf("handler response: want %q, got %v", "user-123", resp)
	}
	if gotUID != "user-123" {
		t.Errorf("UserFromContext: want %q, got %q", "user-123", gotUID)
	}
}

func TestAuth_MissingMetadata(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	_, err := i(context.Background(), nil, dummyInfo, nil)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", code)
	}
}

func TestAuth_MissingAuthorizationHeader(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	_, err := i(ctx, nil, dummyInfo, nil)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", code)
	}
}

func TestAuth_WrongScheme(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	md := metadata.Pairs("authorization", "Basic dXNlcjpwYXNz")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := i(ctx, nil, dummyInfo, nil)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", code)
	}
}

func TestAuth_ExpiredToken(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	ctx := bearerCtx(makeExpiredToken(t, "user-123"))
	_, err := i(ctx, nil, dummyInfo, nil)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", code)
	}
}

func TestAuth_WrongSecret(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier("different-secret"))
	ctx := bearerCtx(makeToken(t, "user-123", time.Hour))
	_, err := i(ctx, nil, dummyInfo, nil)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", code)
	}
}

func TestAuth_TokenMissingUserID(t *testing.T) {
	claims := jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))

	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	ctx := bearerCtx(tok)
	_, err := i(ctx, nil, dummyInfo, nil)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", code)
	}
}

func TestAuth_HealthCheckExempt(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	info := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}

	called := false
	_, err := i(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("health check must not require auth, got: %v", err)
	}
	if !called {
		t.Error("handler was not called for health check")
	}
}

func TestAuth_HealthWatchExempt(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	info := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Watch"}

	called := false
	_, err := i(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("health watch must not require auth, got: %v", err)
	}
	if !called {
		t.Error("handler was not called for health watch")
	}
}

// --- UserFromContext tests ---

func TestUserFromContext_EmptyWithoutInterceptor(t *testing.T) {
	uid, ok := auth.UserFromContext(context.Background())
	if ok || uid != "" {
		t.Errorf("want empty/false from bare context, got %q/%v", uid, ok)
	}
}

func TestUserFromContext_SetByInterceptor(t *testing.T) {
	i := auth.UnaryServerInterceptor(auth.NewHS256Verifier(testSecret))
	tok := makeToken(t, "user-xyz", time.Hour)
	ctx := bearerCtx(tok)

	var got string
	_, _ = i(ctx, nil, dummyInfo, func(ctx context.Context, _ any) (any, error) {
		got, _ = auth.UserFromContext(ctx)
		return nil, nil
	})
	if got != "user-xyz" {
		t.Errorf("UserFromContext: want %q, got %q", "user-xyz", got)
	}
}
