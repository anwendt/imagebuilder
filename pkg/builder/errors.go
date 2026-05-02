package builder

import (
	"errors"
	"fmt"
)

const (
	ReasonSourceFetchFailed     = "SourceFetchFailed"
	ReasonBootFailed            = "BootFailed"
	ReasonGuestReadinessTimeout = "GuestReadinessTimeout"
	ReasonProvisionerFailed     = "ProvisionerFailed"
	ReasonFinalHygieneFailed    = "FinalHygieneFailed"
	ReasonArtifactConvertFailed = "ArtifactConvertFailed"
	ReasonBuildFailed           = "BuildFailed"
)

type ClassifiedError struct {
	Reason string
	Err    error
}

func (e *ClassifiedError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Reason + ": " + e.Err.Error()
}

func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Classify(reason string, err error) error {
	if err == nil {
		return nil
	}
	if reason == "" {
		reason = ReasonBuildFailed
	}
	return &ClassifiedError{Reason: reason, Err: err}
}

func ErrorReason(err error) string {
	var classified *ClassifiedError
	if errors.As(err, &classified) && classified.Reason != "" {
		return classified.Reason
	}
	return ReasonBuildFailed
}

func ErrorDetail(err error) string {
	var classified *ClassifiedError
	if errors.As(err, &classified) && classified.Err != nil {
		return classified.Err.Error()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func classifiedf(reason, format string, args ...any) error {
	return Classify(reason, fmt.Errorf(format, args...))
}
