package errors_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

type codedError struct{ code string }

func (e codedError) Error() string     { return e.code }
func (e codedError) ErrorCode() string { return e.code }

func TestIsTransientRecognizesWrappedTransportFailures(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		fmt.Errorf("wrapped: %w", status.Error(codes.Unavailable, "down")),
		fmt.Errorf("wrapped: %w", status.Error(codes.ResourceExhausted, "rate limited")),
		fmt.Errorf("wrapped: %w", codedError{code: "ThrottlingException"}),
	} {
		if !providererrors.IsTransient(err) {
			t.Errorf("error %v was not classified as transient", err)
		}
	}
}

func TestUnknownAndValidationErrorsRemainTerminal(t *testing.T) {
	for _, err := range []error{
		stderrors.New("invalid provider configuration"),
		status.Error(codes.InvalidArgument, "invalid"),
		status.Error(codes.PermissionDenied, "denied"),
	} {
		if providererrors.IsTransient(err) {
			t.Errorf("error %v was classified as transient", err)
		}
	}
}

func TestExplicitTerminalOverridesWrappedTransient(t *testing.T) {
	err := providererrors.Terminal(context.DeadlineExceeded)
	if providererrors.IsTransient(err) {
		t.Fatal("explicit terminal classification was ignored")
	}
}

func TestTransientCarriesRetryAfter(t *testing.T) {
	err := providererrors.Transient(stderrors.New("busy"), 42*time.Second)
	if !providererrors.IsTransient(err) || providererrors.RetryAfter(err) != 42*time.Second {
		t.Fatalf("classification = transient %t retryAfter %s", providererrors.IsTransient(err), providererrors.RetryAfter(err))
	}
}
