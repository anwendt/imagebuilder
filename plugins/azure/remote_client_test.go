package azure

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestAzureRunCommandProvisioners(t *testing.T) {
	tests := []struct {
		name      string
		input     azureRemoteBuildInput
		spec      v1alpha1.ProvisionerSpec
		wantID    string
		wantPart  string
		wantError bool
	}{
		{
			name:     "linux shell",
			input:    azureRemoteBuildInput{OSFamily: platform.OSFamilyLinux},
			spec:     v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"},
			wantID:   "RunShellScript",
			wantPart: "echo ok",
		},
		{
			name:     "windows powershell",
			input:    azureRemoteBuildInput{OSFamily: platform.OSFamilyWindows},
			spec:     v1alpha1.ProvisionerSpec{Type: "powershell", Inline: "Write-Host ok"},
			wantID:   "RunPowerShellScript",
			wantPart: "Write-Host ok",
		},
		{
			name: "linux evidence receives policy",
			input: azureRemoteBuildInput{
				BuildID:   "build-123",
				ImageName: "ubuntu-golden",
				OSFamily:  platform.OSFamilyLinux,
				SourceMarketplace: &v1alpha1.MarketplaceRef{
					Publisher: "Canonical",
					Offer:     "ubuntu-24_04-lts",
					SKU:       "server",
					Version:   "latest",
				},
				Evidence: &v1alpha1.EvidenceSpec{
					RegistryRepository: "oci://registry.example.com/evidence",
					FailOnSeverity:     []string{"HIGH", "CRITICAL"},
				},
				EvidenceCosignKey: "azurekms://imagebuilder-vault/golden-image-signing",
			},
			spec:     v1alpha1.ProvisionerSpec{Type: "evidence", Inline: "echo collect"},
			wantID:   "RunShellScript",
			wantPart: "export IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY='oci://registry.example.com/evidence'",
		},
		{
			name:     "linux file",
			input:    azureRemoteBuildInput{OSFamily: platform.OSFamilyLinux},
			spec:     v1alpha1.ProvisionerSpec{Type: "file", Inline: "hello", Args: []string{"/etc/imagebuilder"}},
			wantID:   "RunShellScript",
			wantPart: "base64 -d > '/etc/imagebuilder'",
		},
		{
			name:     "windows file",
			input:    azureRemoteBuildInput{OSFamily: platform.OSFamilyWindows},
			spec:     v1alpha1.ProvisionerSpec{Type: "file", Inline: "hello", Args: []string{`C:\imagebuilder.txt`}},
			wantID:   "RunPowerShellScript",
			wantPart: "[IO.File]::WriteAllBytes",
		},
		{
			name:      "cloud-init unsupported",
			input:     azureRemoteBuildInput{OSFamily: platform.OSFamilyLinux},
			spec:      v1alpha1.ProvisionerSpec{Type: "cloud-init", Inline: "#cloud-config"},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commandID, script, err := azureRunCommand(tt.input, tt.spec)
			if tt.wantError {
				if err == nil {
					t.Fatal("azureRunCommand should fail")
				}
				return
			}
			if err != nil {
				t.Fatalf("azureRunCommand returned error: %v", err)
			}
			if commandID != tt.wantID {
				t.Fatalf("commandID = %q, want %q", commandID, tt.wantID)
			}
			if len(script) != 1 || !strings.Contains(script[0], tt.wantPart) {
				t.Fatalf("script = %#v, want part %q", script, tt.wantPart)
			}
		})
	}
}

func TestAzureRemoteOperationRefRoundTrip(t *testing.T) {
	digest := strings.Repeat("a", 64)
	ref := azureRemoteOperationRef{
		BuildID:          "build-123",
		VMName:           "ib-build-123-vm",
		VMID:             "/subscriptions/000/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/ib-build-123-vm",
		DiskName:         "ib-build-123-osdisk",
		ProvisionerIndex: 2,
		EvidenceStatus:   "passed",
		EvidenceMessage:  "evidence published",
		SBOMRef:          "oci://registry.example.com/evidence/sbom/image@sha256:" + digest,
		VulnerabilityRef: "oci://registry.example.com/evidence/vulnerability/image@sha256:" + digest,
		ProvenanceRef:    "oci://registry.example.com/evidence/provenance/image@sha256:" + digest,
		SignatureRef:     "oci://registry.example.com/evidence/signature/image@sha256:" + digest,
	}
	parsed, err := parseAzureRemoteOperationRef(ref.String())
	if err != nil {
		t.Fatalf("parseAzureRemoteOperationRef returned error: %v", err)
	}
	if parsed.BuildID != ref.BuildID ||
		parsed.VMName != ref.VMName ||
		parsed.VMID != ref.VMID ||
		parsed.DiskName != ref.DiskName ||
		parsed.ProvisionerIndex != ref.ProvisionerIndex ||
		parsed.EvidenceStatus != ref.EvidenceStatus ||
		parsed.EvidenceMessage != ref.EvidenceMessage ||
		parsed.SBOMRef != ref.SBOMRef ||
		parsed.VulnerabilityRef != ref.VulnerabilityRef ||
		parsed.ProvenanceRef != ref.ProvenanceRef ||
		parsed.SignatureRef != ref.SignatureRef {
		t.Fatalf("parsed ref = %#v, want %#v", parsed, ref)
	}
}

func TestAzureEvidenceFromRunCommand(t *testing.T) {
	digest := strings.Repeat("b", 64)
	want := &platform.RemoteEvidenceResult{
		Status:                 "passed",
		Message:                "published",
		SBOMRef:                "oci://registry.example.com/evidence/sbom/image@sha256:" + digest,
		VulnerabilityReportRef: "oci://registry.example.com/evidence/vulnerability/image@sha256:" + digest,
		ProvenanceRef:          "oci://registry.example.com/evidence/provenance/image@sha256:" + digest,
		SignatureRef:           "oci://registry.example.com/evidence/signature/image@sha256:" + digest,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	message := "command output\n" + azureEvidenceMarker + base64.StdEncoding.EncodeToString(payload) + "\n"
	got, err := azureEvidenceFromRunCommand([]*armcompute.InstanceViewStatus{{Message: &message}})
	if err != nil {
		t.Fatalf("azureEvidenceFromRunCommand returned error: %v", err)
	}
	if *got != *want {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}

	missing := "command output without evidence"
	if _, err := azureEvidenceFromRunCommand([]*armcompute.InstanceViewStatus{{Message: &missing}}); err == nil {
		t.Fatal("azureEvidenceFromRunCommand should reject a missing marker")
	}
}

func TestValidateAzureEvidence(t *testing.T) {
	digest := strings.Repeat("c", 64)
	repository := "oci://registry.example.com/platform/evidence"
	policy := &v1alpha1.EvidenceSpec{Required: true, RegistryRepository: repository}
	evidence := &platform.RemoteEvidenceResult{
		Status:                 "passed",
		SBOMRef:                repository + "/sbom/image@sha256:" + digest,
		VulnerabilityReportRef: repository + "/vulnerability/image@sha256:" + digest,
		ProvenanceRef:          repository + "/provenance/image@sha256:" + digest,
		SignatureRef:           repository + "/signature/image@sha256:" + digest,
	}
	if err := validateAzureEvidence(policy, evidence); err != nil {
		t.Fatalf("validateAzureEvidence returned error: %v", err)
	}

	evidence.SignatureRef = repository + "/signature/image:latest"
	if err := validateAzureEvidence(policy, evidence); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("validateAzureEvidence error = %v, want immutable reference error", err)
	}
}

func TestAzureRemoteSettings_RequiresNICForSSHGuestAccess(t *testing.T) {
	settings := azureRemoteSettings{VMSize: "Standard_B2s"}
	err := settings.validate(azureRemoteBuildInput{
		BuildID:     "build-123",
		GuestAccess: &v1alpha1.GuestAccessSpec{Protocol: "ssh"},
	})
	if err == nil {
		t.Fatal("validate should require remote.networkInterfaceId for SSH guest access")
	}
	if !strings.Contains(err.Error(), "remote.networkInterfaceId") {
		t.Fatalf("error = %v", err)
	}
}

func TestAzureRemoteSettings_AllowsDirectRegistrationWithoutNIC(t *testing.T) {
	settings := azureRemoteSettings{VMSize: "Standard_B2s"}
	if err := settings.validate(azureRemoteBuildInput{BuildID: "build-123"}); err != nil {
		t.Fatalf("validate should allow direct registration without NIC: %v", err)
	}
}

func TestAzureRemoteSettings_RequiredEvidenceNeedsManagedIdentityAndKMSKey(t *testing.T) {
	input := azureRemoteBuildInput{
		BuildID:  "build-123",
		Evidence: &v1alpha1.EvidenceSpec{Required: true},
	}
	settings := azureRemoteSettings{VMSize: "Standard_B2s"}
	if err := settings.validate(input); err == nil || !strings.Contains(err.Error(), "remote.managedIdentityId") {
		t.Fatalf("validate error = %v, want managed identity requirement", err)
	}

	settings.ManagedIdentityID = "/subscriptions/000/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/imagebuilder"
	if err := settings.validate(input); err == nil || !strings.Contains(err.Error(), "remote.evidence.cosignKeyRef") {
		t.Fatalf("validate error = %v, want Cosign key requirement", err)
	}

	settings.EvidenceCosignKey = "azurekms://imagebuilder-vault/golden-image-signing"
	if err := settings.validate(input); err != nil {
		t.Fatalf("validate returned error for complete evidence settings: %v", err)
	}
}
