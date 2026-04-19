package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryUnaryInterceptor returns a gRPC unary interceptor that does two things:
//
//  1. Recovers from any panic in downstream interceptors or handlers, logs the
//     panic value and full stack trace at Error level, and returns codes.Internal
//     to the caller. The raw panic detail is never forwarded to the client.
//
//  2. Passes every non-panic error through ToGRPCStatus so that plain Go errors
//     (which grpc-go would surface as codes.Unknown) are promoted to codes.Internal
//     and context errors are mapped to their proper codes.
//
// Position this immediately after the logging interceptor in the chain so that
// panics are recovered before the log record is written — the logging interceptor
// then sees codes.Internal and logs it at Error level, giving you a correlated
// pair: one structured log from this interceptor (with the panic value and stack)
// and one from the logging interceptor (with method/duration/status).
func RecoveryUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "panic recovered",
					slog.Any("panic", fmt.Sprintf("%v", r)),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", info.FullMethod),
				)
				resp = nil
				err = status.Error(codes.Internal, "internal server error")
			}
		}()

		resp, err = handler(ctx, req)
		return resp, ToGRPCStatus(err)
	}
}
