package vsphere

import (
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestVSphereGuestProgramProvisioners(t *testing.T) {
	tests := []struct {
		name      string
		input     vsphereRemoteBuildInput
		spec      v1alpha1.ProvisionerSpec
		wantProg  string
		wantPart  string
		wantError bool
	}{
		{
			name:     "linux shell",
			input:    vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux},
			spec:     v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"},
			wantProg: "/bin/sh",
			wantPart: "echo ok",
		},
		{
			name:     "windows powershell",
			input:    vsphereRemoteBuildInput{OSFamily: platform.OSFamilyWindows},
			spec:     v1alpha1.ProvisionerSpec{Type: "powershell", Inline: "Write-Host ok"},
			wantProg: "powershell.exe",
			wantPart: "Write-Host ok",
		},
		{
			name:     "linux file",
			input:    vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux},
			spec:     v1alpha1.ProvisionerSpec{Type: "file", Inline: "hello", Args: []string{"/etc/imagebuilder"}},
			wantProg: "/bin/sh",
			wantPart: "base64 -d >",
		},
		{
			name:     "windows file",
			input:    vsphereRemoteBuildInput{OSFamily: platform.OSFamilyWindows},
			spec:     v1alpha1.ProvisionerSpec{Type: "file", Inline: "hello", Args: []string{`C:\imagebuilder.txt`}},
			wantProg: "powershell.exe",
			wantPart: "[IO.File]::WriteAllBytes",
		},
		{
			name:      "cloud-init unsupported",
			input:     vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux},
			spec:      v1alpha1.ProvisionerSpec{Type: "cloud-init", Inline: "#cloud-config"},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			program, args, err := vsphereGuestProgram(tt.input, tt.spec)
			if tt.wantError {
				if err == nil {
					t.Fatal("vsphereGuestProgram should fail")
				}
				return
			}
			if err != nil {
				t.Fatalf("vsphereGuestProgram returned error: %v", err)
			}
			if !strings.Contains(program, tt.wantProg) {
				t.Fatalf("program = %q, want part %q", program, tt.wantProg)
			}
			if !strings.Contains(args, tt.wantPart) {
				t.Fatalf("args = %q, want part %q", args, tt.wantPart)
			}
		})
	}
}

func TestVSphereRemoteOperationRefRoundTrip(t *testing.T) {
	ref := vsphereRemoteOperationRef{
		BuildID:          "build-123",
		VMName:           "imagebuilder-build-123",
		VMRef:            "vm-123",
		ProvisionerIndex: 2,
	}
	parsed, err := parseVSphereRemoteOperationRef(ref.String())
	if err != nil {
		t.Fatalf("parseVSphereRemoteOperationRef returned error: %v", err)
	}
	if parsed.BuildID != ref.BuildID ||
		parsed.VMName != ref.VMName ||
		parsed.VMRef != ref.VMRef ||
		parsed.ProvisionerIndex != ref.ProvisionerIndex {
		t.Fatalf("parsed ref = %#v, want %#v", parsed, ref)
	}
}

func TestVSphereRemoteNetwork_RequiresNetworkForSSHGuestAccess(t *testing.T) {
	client := &govmomiClient{}
	err := client.validateVSphereRemoteNetwork(vsphereRemoteBuildInput{
		GuestAccess: &v1alpha1.GuestAccessSpec{Protocol: "ssh"},
	})
	if err == nil {
		t.Fatal("validateVSphereRemoteNetwork should require ProviderConfig extra network for SSH guest access")
	}
	if !strings.Contains(err.Error(), "extra network") {
		t.Fatalf("error = %v", err)
	}
}

func TestVSphereRemoteNetwork_AllowsGuestOperationsWithoutNetwork(t *testing.T) {
	client := &govmomiClient{}
	if err := client.validateVSphereRemoteNetwork(vsphereRemoteBuildInput{
		Provisioners: []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}},
	}); err != nil {
		t.Fatalf("validateVSphereRemoteNetwork should allow Guest Operations without SSH network: %v", err)
	}
}

func TestVSphereMarketplaceSourceCandidatesPreferExplicitMapping(t *testing.T) {
	input := vsphereRemoteBuildInput{
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: "Canonical",
			Offer:     "ubuntu",
			SKU:       "24.04",
			Version:   "latest",
		},
	}
	candidates := vsphereMarketplaceSourceCandidates(map[string]string{
		"marketplace.canonical.ubuntu.24.04.latest": "content-library:/Golden Images/ubuntu-24-template",
	}, input)
	if len(candidates) == 0 || candidates[0] != "content-library:/Golden Images/ubuntu-24-template" {
		t.Fatalf("candidates = %v, want explicit mapping first", candidates)
	}
	if !containsString(candidates, "ubuntu-24-template") {
		t.Fatalf("candidates = %v, want Ubuntu fallback", candidates)
	}
}

func TestVSphereMarketplaceConfigKeys(t *testing.T) {
	keys := vsphereMarketplaceConfigKeys(&v1alpha1.MarketplaceRef{
		Publisher: "Open Telekom Cloud",
		Offer:     "ubuntu",
		SKU:       "24.04",
		Version:   "latest",
	})
	want := "marketplace.open.telekom.cloud.ubuntu.24.04.latest"
	if len(keys) == 0 || keys[0] != want {
		t.Fatalf("keys = %v, want first %q", keys, want)
	}
}

func TestVSphereContentLibraryRefParsing(t *testing.T) {
	path, ok := contentLibraryPath("content-library:Golden Images/ubuntu-24-template")
	if !ok || path != "/Golden Images/ubuntu-24-template" {
		t.Fatalf("content library path = %q, %v", path, ok)
	}
	path, ok = contentLibraryPath("library:/Golden Images/ubuntu-24-template")
	if !ok || path != "/Golden Images/ubuntu-24-template" {
		t.Fatalf("library path = %q, %v", path, ok)
	}
	id, ok := contentLibraryItemID("library-item:1234")
	if !ok || id != "1234" {
		t.Fatalf("library item ID = %q, %v", id, ok)
	}
	if !isVSphereContentLibraryRef("content-library:/Golden Images/ubuntu-24-template") {
		t.Fatal("content-library ref should be recognised")
	}
	if isVSphereContentLibraryRef("ubuntu-24-template") {
		t.Fatal("plain template name should not be recognised as content library ref")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
