package grpcapi

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Status helpers for handlers.
//
// A gRPC client branches on the status code, so returning the right one is not
// cosmetic: NotFound tells a caller the request will never succeed, Unavailable
// tells it to retry, and Internal tells it to page someone. Collapsing them all
// into one error is what makes a gRPC API feel like a tunnel rather than an API.
//
// A handler may also return a status.Error directly, or any error at all — an
// unrecognised error becomes Internal with a generic message, because a raw error
// string can carry a DSN, a table name or an internal hostname.

// NotFound reports that the addressed resource does not exist.
func NotFound(format string, args ...any) error {
	return status.Errorf(codes.NotFound, format, args...)
}

// InvalidArgument reports a malformed or unacceptable request.
func InvalidArgument(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

// AlreadyExists reports a uniqueness conflict.
func AlreadyExists(format string, args ...any) error {
	return status.Errorf(codes.AlreadyExists, format, args...)
}

// PermissionDenied reports that the caller is known but not allowed.
func PermissionDenied(format string, args ...any) error {
	return status.Errorf(codes.PermissionDenied, format, args...)
}

// Unauthenticated reports missing or invalid credentials.
func Unauthenticated(format string, args ...any) error {
	return status.Errorf(codes.Unauthenticated, format, args...)
}

// FailedPrecondition reports that the system is not in a state for this call.
func FailedPrecondition(format string, args ...any) error {
	return status.Errorf(codes.FailedPrecondition, format, args...)
}

// Unavailable reports a transient failure the caller may retry.
func Unavailable(format string, args ...any) error {
	return status.Errorf(codes.Unavailable, format, args...)
}

// Internal reports a server-side failure without disclosing its detail.
//
// The message reaches the client, so keep it generic and log the cause. An
// unwrapped database error here is how a DSN ends up in someone's console.
func Internal(format string, args ...any) error {
	return status.Errorf(codes.Internal, format, args...)
}

// toStatus turns a handler error into a gRPC status.
//
// An error that already carries a status is passed through untouched; anything
// else becomes fallback with a generic message, because an arbitrary error string
// is not safe to hand to a caller.
func toStatus(err error, fallback codes.Code) error {
	if err == nil {
		return nil
	}

	// status.FromError also matches an error that wraps one, via the interface
	// grpc-go looks for.
	if s, ok := status.FromError(err); ok {
		return s.Err()
	}

	var withStatus interface{ GRPCStatus() *status.Status }
	if errors.As(err, &withStatus) {
		return withStatus.GRPCStatus().Err()
	}

	if fallback == codes.InvalidArgument {
		// Validation messages name fields the caller sent, so they are safe and
		// useful to return verbatim.
		return status.Error(fallback, err.Error())
	}
	return status.Error(fallback, "internal error")
}
