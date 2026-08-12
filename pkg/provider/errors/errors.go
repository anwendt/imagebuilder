package errors

import (
	"context"
	stderrors "errors"
	"net"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// classifiedError carries retry semantics without changing provider interfaces.
type classifiedError struct {
	err        error
	transient  bool
	retryAfter time.Duration
}

func (e *classifiedError) Error() string             { return e.err.Error() }
func (e *classifiedError) Unwrap() error             { return e.err }
func (e *classifiedError) Transient() bool           { return e.transient }
func (e *classifiedError) RetryAfter() time.Duration { return e.retryAfter }

// Transient marks an error as safe to retry. retryAfter may be zero to let the
// controller calculate exponential backoff.
func Transient(err error, retryAfter time.Duration) error {
	if err == nil {
		return nil
	}
	return &classifiedError{err: err, transient: true, retryAfter: retryAfter}
}

// Terminal marks an error as non-retryable even when a wrapped error would
// otherwise match a generic transient classifier.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &classifiedError{err: err}
}

// Classify marks common transport, throttling, and temporary service failures
// as transient. Unknown errors remain terminal by default.
func Classify(err error) error {
	if err == nil || isExplicitlyClassified(err) {
		return err
	}
	if genericTransient(err) {
		return Transient(err, 0)
	}
	return err
}

func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var classified interface{ Transient() bool }
	if stderrors.As(err, &classified) {
		return classified.Transient()
	}
	return genericTransient(err)
}

func RetryAfter(err error) time.Duration {
	var retryable interface{ RetryAfter() time.Duration }
	if stderrors.As(err, &retryable) {
		return retryable.RetryAfter()
	}
	if grpcStatus, ok := status.FromError(err); ok {
		for _, detail := range grpcStatus.Details() {
			if retryInfo, ok := detail.(*errdetails.RetryInfo); ok && retryInfo.GetRetryDelay() != nil {
				return retryInfo.GetRetryDelay().AsDuration()
			}
		}
	}
	return 0
}

func isExplicitlyClassified(err error) bool {
	var classified interface{ Transient() bool }
	return stderrors.As(err, &classified)
}

func genericTransient(err error) bool {
	if stderrors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if stderrors.Is(err, context.Canceled) {
		return false
	}
	var networkError net.Error
	if stderrors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}

	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	case codes.OK, codes.Unknown:
	default:
		return false
	}

	var httpStatus interface{ HTTPStatusCode() int }
	if stderrors.As(err, &httpStatus) && transientHTTPStatus(httpStatus.HTTPStatusCode()) {
		return true
	}
	var statusGetter interface{ GetStatusCode() int }
	if stderrors.As(err, &statusGetter) && transientHTTPStatus(statusGetter.GetStatusCode()) {
		return true
	}
	var apiError interface{ ErrorCode() string }
	return stderrors.As(err, &apiError) && transientErrorCode(apiError.ErrorCode())
}

func transientHTTPStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func transientErrorCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, marker := range []string{
		"throttl", "requestlimitexceeded", "requesttimeout", "requestexpired",
		"serviceunavailable", "temporarilyunavailable", "toomanyrequests",
		"internalerror", "internalfailure", "serverbusy", "operationtimedout",
	} {
		if strings.Contains(code, marker) {
			return true
		}
	}
	return false
}
