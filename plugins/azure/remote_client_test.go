package azure

import (
	"strings"
	"testing"

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
	ref := azureRemoteOperationRef{
		BuildID:          "build-123",
		VMName:           "ib-build-123-vm",
		VMID:             "/subscriptions/000/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/ib-build-123-vm",
		DiskName:         "ib-build-123-osdisk",
		ProvisionerIndex: 2,
	}
	parsed, err := parseAzureRemoteOperationRef(ref.String())
	if err != nil {
		t.Fatalf("parseAzureRemoteOperationRef returned error: %v", err)
	}
	if parsed.BuildID != ref.BuildID ||
		parsed.VMName != ref.VMName ||
		parsed.VMID != ref.VMID ||
		parsed.DiskName != ref.DiskName ||
		parsed.ProvisionerIndex != ref.ProvisionerIndex {
		t.Fatalf("parsed ref = %#v, want %#v", parsed, ref)
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
