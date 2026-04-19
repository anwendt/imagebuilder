// api/v1alpha1/vmimage_types_test.go
//
// Unit tests for VMImage type constants and helper invariants.
// DR-001: every public constant / exported value must have a test.

package v1alpha1_test

import (
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

// TestPhaseConstants verifies the phase string values match the kubebuilder
// enum annotation in VMImageStatus.Phase so that kubectl status columns work.
func TestPhaseConstants(t *testing.T) {
	cases := []struct {
		constant string
		want     string
	}{
		{v1alpha1.PhasePending, "Pending"},
		{v1alpha1.PhaseBuilding, "Building"},
		{v1alpha1.PhaseProvisioning, "Provisioning"},
		{v1alpha1.PhaseUploading, "Uploading"},
		{v1alpha1.PhaseReady, "Ready"},
		{v1alpha1.PhaseFailed, "Failed"},
	}
	for _, tc := range cases {
		if tc.constant != tc.want {
			t.Errorf("phase constant = %q, want %q", tc.constant, tc.want)
		}
	}
}

// TestPhaseConstantsAreDistinct ensures no two phase constants share the same value.
func TestPhaseConstantsAreDistinct(t *testing.T) {
	all := []string{
		v1alpha1.PhasePending,
		v1alpha1.PhaseBuilding,
		v1alpha1.PhaseProvisioning,
		v1alpha1.PhaseUploading,
		v1alpha1.PhaseReady,
		v1alpha1.PhaseFailed,
	}
	seen := make(map[string]struct{}, len(all))
	for _, p := range all {
		if _, dup := seen[p]; dup {
			t.Errorf("duplicate phase value %q", p)
		}
		seen[p] = struct{}{}
	}
}
