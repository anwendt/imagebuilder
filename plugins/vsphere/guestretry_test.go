package vsphere

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestGuestRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"process vanished", errors.New("vSphere guest provisioner 3 process 7 disappeared"), true},
		{"tools unavailable", errors.New("ServerFaultCode: GuestOperationsUnavailable"), true},
		{"script failed", fmt.Errorf("provisioner: %w", &guestExitError{code: 2}), false},
		{"canceled", context.Canceled, false},
		{"deadline", fmt.Errorf("wrap: %w", context.DeadlineExceeded), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guestRetryable(tt.err); got != tt.want {
				t.Fatalf("guestRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
