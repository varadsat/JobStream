package middleware

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToGRPCStatus converts any error to a gRPC status error:
//   - nil                   → nil
//   - already a gRPC status → returned unchanged
//   - context.Canceled      → codes.Canceled
//   - context.DeadlineExceeded → codes.DeadlineExceeded
//   - anything else         → codes.Internal
//
// Without this, a plain Go error returned from a handler surfaces to the caller
// as codes.Unknown (grpc-go's default), which is misleading. Routing every
// unmapped error through here makes codes.Internal the explicit, intentional
// fallback rather than an accident of omission.
func ToGRPCStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
