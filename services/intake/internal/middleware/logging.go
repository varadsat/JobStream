package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type contextKey string

const requestIDKey contextKey = "request-id"

const metadataRequestIDKey = "x-request-id"

// RequestIDFromContext returns the request ID stored in ctx, or empty string.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// UnaryServerInterceptor returns a gRPC unary interceptor that logs each RPC
// with method, duration, status code, peer address, and a request ID.
// The request ID is read from the x-request-id incoming metadata key; if
// absent, a UUID is generated. Either way it is stored in ctx so downstream
// handlers can read it via RequestIDFromContext.
func UnaryServerInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()

		reqID := requestIDFromMetadata(ctx)
		if reqID == "" {
			reqID = uuid.NewString()
		}
		ctx = context.WithValue(ctx, requestIDKey, reqID)

		resp, err := handler(ctx, req)

		code := status.Code(err)
		attrs := []slog.Attr{
			slog.String("method", info.FullMethod),
			slog.String("request_id", reqID),
			slog.String("peer", peerAddr(ctx)),
			slog.Duration("duration", time.Since(start)),
			slog.String("status", code.String()),
		}
		if err != nil {
			attrs = append(attrs, slog.String("error", err.Error()))
		}

		logger.LogAttrs(ctx, levelForCode(code), "rpc", attrs...)

		return resp, err
	}
}

func requestIDFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if vals := md.Get(metadataRequestIDKey); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func peerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}
	return ""
}

// levelForCode maps gRPC status codes to log levels.
// Client errors (bad input, not found, auth) log at Warn; server faults at Error.
func levelForCode(code codes.Code) slog.Level {
	switch code {
	case codes.OK:
		return slog.LevelInfo
	case codes.NotFound, codes.InvalidArgument, codes.AlreadyExists,
		codes.PermissionDenied, codes.Unauthenticated, codes.Canceled,
		codes.ResourceExhausted:
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}
