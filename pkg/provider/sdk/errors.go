package sdk

import (
	"time"

	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

// TransientError marks an external provider error as retryable by the core
// controller. retryAfter may be zero to use the controller's exponential
// backoff policy.
func TransientError(err error, retryAfter time.Duration) error {
	return providererrors.Transient(err, retryAfter)
}

// TerminalError explicitly prevents retries for an error that wraps a timeout
// or another generically transient transport error.
func TerminalError(err error) error {
	return providererrors.Terminal(err)
}
