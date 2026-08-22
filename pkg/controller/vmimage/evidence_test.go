package vmimage

import (
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

func TestValidateRemoteEvidence(t *testing.T) {
	digest := strings.Repeat("a", 64)
	repository := "oci://registry.example.com/platform/evidence"
	valid := v1alpha1.EvidenceStatus{
		Status:                 "passed",
		SBOMRef:                repository + "/sbom/image@sha256:" + digest,
		VulnerabilityReportRef: repository + "/vulnerability/image@sha256:" + digest,
		ProvenanceRef:          repository + "/provenance/image@sha256:" + digest,
		SignatureRef:           repository + "/signature/image@sha256:" + digest,
	}
	policy := &v1alpha1.EvidenceSpec{Required: true, RegistryRepository: repository}

	tests := []struct {
		name      string
		policy    *v1alpha1.EvidenceSpec
		evidence  v1alpha1.EvidenceStatus
		wantError string
	}{
		{name: "complete immutable evidence", policy: policy, evidence: valid},
		{name: "optional missing evidence", policy: &v1alpha1.EvidenceSpec{}, evidence: v1alpha1.EvidenceStatus{}},
		{name: "required status missing", policy: policy, evidence: v1alpha1.EvidenceStatus{}, wantError: "want passed"},
		{
			name:   "mutable reference",
			policy: policy,
			evidence: func() v1alpha1.EvidenceStatus {
				result := valid
				result.SBOMRef = repository + "/sbom/image:latest"
				return result
			}(),
			wantError: "immutable oci:// reference",
		},
		{
			name:   "reference outside managed repository",
			policy: policy,
			evidence: func() v1alpha1.EvidenceStatus {
				result := valid
				result.SignatureRef = "oci://registry.example.com/other/signature@sha256:" + digest
				return result
			}(),
			wantError: "outside registry repository",
		},
		{
			name:     "optional collection failure does not block readiness",
			policy:   &v1alpha1.EvidenceSpec{},
			evidence: v1alpha1.EvidenceStatus{Status: "failed", Message: "scanner unavailable"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRemoteEvidence(tt.policy, tt.evidence)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateRemoteEvidence returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateRemoteEvidence error = %v, want %q", err, tt.wantError)
			}
		})
	}
}
