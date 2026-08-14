// api/v1alpha1/webhook_test.go
//
// Unit tests for the admission webhook validation logic (AS-026, AS-047, AS-049).
// These tests call the validate() helpers directly without a live API server.

package v1alpha1

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// VMImage webhook tests
// ---------------------------------------------------------------------------

func validMarketplaceSource() SourceSpec {
	return SourceSpec{
		Type: "marketplace",
		MarketplaceRef: &MarketplaceRef{
			Publisher: "Canonical",
			Offer:     "ubuntu-24_04-lts",
			SKU:       "server",
			Version:   "latest",
		},
	}
}

func TestVMImageWebhook_ValidCreate_NoURL(t *testing.T) {
	img := &VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err != nil {
		t.Errorf("ValidateCreate with marketplace source (no URL) returned error: %v", err)
	}
}

func TestVMImageWebhook_LocalBuild_DefaultsArtifactStorageToPVC(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	if got := effectiveArtifactStorageType(img.Spec.Build); got != "pvc" {
		t.Fatalf("effective artifact storage type = %q, want pvc", got)
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("local build without explicit artifactStorage should be admitted with PVC default: %v", err)
	}
}

func TestVMImageWebhook_LocalBuild_RejectsEmptyDirArtifactStorage(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Build:  BuildSpec{ArtifactStorage: &ArtifactStorageSpec{Type: "emptyDir"}},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), "spec.build.artifactStorage.type must be pvc for local builds") {
		t.Fatalf("ValidateCreate error = %v, want local-build PVC requirement", err)
	}
}

func TestVMImageWebhook_RemoteBuild_AllowsEmptyDirArtifactStorage(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", ProviderRef: "ami-0123456789abcdef0"},
			Build: BuildSpec{
				Mode:            BuildModeRemote,
				ArtifactStorage: &ArtifactStorageSpec{Type: "emptyDir"},
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("remote build should not require PVC artifact storage: %v", err)
	}
}

func TestVMImageWebhook_MarketplaceSource_RequiresReference(t *testing.T) {
	img := &VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "marketplace"},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), "marketplaceRef or spec.source.providerRef") {
		t.Fatalf("ValidateCreate error = %v, want missing marketplace reference", err)
	}
}

func TestVMImageWebhook_MarketplaceSource_AllowsProviderRef(t *testing.T) {
	img := &VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "marketplace", ProviderRef: "ami-0123456789abcdef0"},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("ValidateCreate with providerRef marketplace source returned error: %v", err)
	}
}

func TestVMImageWebhook_CustomOCIProvisionerRequiresImage(t *testing.T) {
	img := &VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Provisioners: []ProvisionerSpec{
				{Type: "inspec"},
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), "image is required for custom OCI provisioner type") {
		t.Fatalf("ValidateCreate error = %v, want missing custom OCI provisioner image", err)
	}
}

func TestVMImageWebhook_CustomOCIProvisionerAllowsImage(t *testing.T) {
	img := &VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Provisioners: []ProvisionerSpec{
				{Type: "inspec", Image: "ghcr.io/example/imagebuilder-provisioner-inspec@sha256:abc123"},
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("ValidateCreate with custom OCI provisioner image returned error: %v", err)
	}
}

func TestVMImageWebhook_MissingChecksum_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type: "iso",
				URL:  "https://example.com/ubuntu.iso",
				// Checksum intentionally omitted
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("expected error when URL set but checksum missing")
	}
}

func TestVMImageWebhook_RemoteProviderRefSource_DoesNotRequireChecksum(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", ProviderRef: "ami-0123456789abcdef0"},
			Build:  BuildSpec{Mode: BuildModeRemote},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err != nil {
		t.Errorf("remote provider source ref should be valid without checksum: %v", err)
	}
}

func TestVMImageWebhook_SnapshotSource_AllowsRemoteProviderRef(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "snapshot", ProviderRef: "snap-0123456789abcdef0"},
			Build:  BuildSpec{Mode: BuildModeRemote},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("snapshot source should be valid for remote provider registration: %v", err)
	}
}

func TestVMImageWebhook_SnapshotSource_RejectsLocalBuild(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "snapshot", ProviderRef: "snap-0123456789abcdef0"},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("snapshot source should require remote build mode")
	}
}

func TestVMImageWebhook_SnapshotSource_AllowsProviderManagedProvisioners(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:           OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source:       SourceSpec{Type: "snapshot", ProviderRef: "vm-123"},
			Build:        BuildSpec{Mode: BuildModeRemote},
			Provisioners: []ProvisionerSpec{{Type: "shell", Inline: "echo ok"}},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "vsphere-cfg"}, Format: "ova"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("snapshot source with provider-managed provisioners should be admitted: %v", err)
	}
}

func TestVMImageWebhook_ProviderNativeSourceInURL_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "ami-0123456789abcdef0"},
			Build:  BuildSpec{Mode: BuildModeRemote},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
			},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Fatal("expected provider-native source ref in source.url to be rejected")
	}
}

func TestVMImageWebhook_OSArch_AllowsARM64(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04", Arch: "arm64"},
			Source: validMarketplaceSource(),
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("arm64 os arch should be valid: %v", err)
	}
}

func TestVMImageWebhook_OSArch_RejectsUnsupportedArch(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04", Arch: "s390x"},
			Source: validMarketplaceSource(),
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("unsupported os arch should be rejected")
	}
}

func TestVMImageWebhook_EmptyTargets_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source:  validMarketplaceSource(),
			Targets: []TargetSpec{}, // empty
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("expected error when targets is empty")
	}
}

func TestVMImageWebhook_ValidUpdate_AcceptsValidSpec(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://releases.ubuntu.com/24.04/ubuntu.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"},
			},
		},
	}
	_, err := img.ValidateUpdate(nil)
	if err != nil {
		t.Errorf("ValidateUpdate with valid spec returned error: %v", err)
	}
}

func TestVMImageWebhook_TerminalUpdateRequiresRevisionChange(t *testing.T) {
	old := &VMImage{
		Spec: VMImageSpec{
			OS:      OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source:  validMarketplaceSource(),
			Build:   BuildSpec{Revision: "v1"},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"}},
		},
		Status: VMImageStatus{Phase: PhaseReady},
	}
	updated := old.DeepCopy()
	updated.Spec.OS.Version = "24.10"
	_, err := updated.ValidateUpdate(old)
	if err == nil || !strings.Contains(err.Error(), "must change spec.build.revision") {
		t.Fatalf("ValidateUpdate error = %v, want revision requirement", err)
	}
}

func TestVMImageWebhook_TerminalRevisionChangeRequestsRebuild(t *testing.T) {
	old := &VMImage{
		Spec: VMImageSpec{
			OS:      OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source:  validMarketplaceSource(),
			Build:   BuildSpec{Revision: "v1"},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"}},
		},
		Status: VMImageStatus{Phase: PhaseFailed},
	}
	updated := old.DeepCopy()
	updated.Spec.Build.Revision = "v2"
	updated.Spec.OS.Version = "24.10"
	if _, err := updated.ValidateUpdate(old); err != nil {
		t.Fatalf("terminal update with new revision should be admitted: %v", err)
	}
}

func TestVMImageWebhook_ActiveBuildSpecIsImmutable(t *testing.T) {
	old := &VMImage{
		Spec: VMImageSpec{
			OS:      OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source:  validMarketplaceSource(),
			Build:   BuildSpec{Revision: "v1"},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"}},
		},
		Status: VMImageStatus{Phase: PhaseBuilding},
	}
	updated := old.DeepCopy()
	updated.Spec.Build.Revision = "v2"
	_, err := updated.ValidateUpdate(old)
	if err == nil || !strings.Contains(err.Error(), "spec cannot be changed while VMImage phase is") {
		t.Fatalf("ValidateUpdate error = %v, want active-build immutability", err)
	}
}

func TestVMImageWebhook_SourceCacheSpec_Valid(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Build: BuildSpec{
				Cache: &SourceCacheSpec{
					Ref:          "source-cache",
					TTL:          &metav1.Duration{Duration: 24 * time.Hour},
					RetainPolicy: "Always",
				},
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("valid cache spec should be accepted: %v", err)
	}
}

func TestVMImageWebhook_SourceCacheSpec_RequiresRef(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Build:  BuildSpec{Cache: &SourceCacheSpec{TTL: &metav1.Duration{Duration: time.Hour}}},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("cache config without ref should be rejected")
	}
}

func TestVMImageWebhook_SourceCacheSpec_RejectsCacheRefMismatch(t *testing.T) {
	legacy := "legacy-cache"
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Build: BuildSpec{
				CacheRef: &legacy,
				Cache:    &SourceCacheSpec{Ref: "structured-cache"},
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("mismatched cacheRef and cache.ref should be rejected")
	}
}

func TestVMImageWebhook_SourceCacheSpec_RejectsNonPositiveTTL(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Build: BuildSpec{
				Cache: &SourceCacheSpec{
					Ref: "source-cache",
					TTL: &metav1.Duration{Duration: 0},
				},
			},
			Targets: []TargetSpec{
				{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"},
			},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("non-positive cache TTL should be rejected")
	}
}

func TestVMImageWebhook_BootCommand_ISOSource_Valid(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type:        "iso",
				URL:         "https://releases.ubuntu.com/24.04/ubuntu.iso",
				Checksum:    "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
				BootCommand: []string{"<tab>", " ks=http://192.0.2.1/ks.cfg", "<enter>"},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err != nil {
		t.Errorf("bootCommand on iso source should be valid: %v", err)
	}
}

func TestVMImageWebhook_BootCommand_NonISOSource_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type:        "cloud-image",
				BootCommand: []string{"<enter>"},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("bootCommand on cloud-image source should return error")
	}
}

func TestVMImageWebhook_BootCommand_MarketplaceSource_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type:        "marketplace",
				BootCommand: []string{"<up>", "<enter>"},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("bootCommand on marketplace source should return error")
	}
}

func TestVMImageWebhook_InstallerMedia_ISOSource_Valid(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{
				Type: "iso",
				Installer: &InstallerMediaSpec{
					Type:     "autoinstall",
					UserData: "#cloud-config\nautoinstall:\n  version: 1\n",
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err != nil {
		t.Errorf("installer media on iso source should be valid: %v", err)
	}
}

func TestVMImageWebhook_InstallerMedia_NonISOSource_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{
				Type:      "cloud-image",
				Installer: &InstallerMediaSpec{Type: "kickstart", Kickstart: "text\n"},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("installer media on cloud-image source should return error")
	}
}

func TestVMImageWebhook_InstallerMedia_RejectsOSMismatch(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "windows", Distribution: "windows-server", Version: "2022"},
			Source: SourceSpec{
				Type:      "iso",
				Installer: &InstallerMediaSpec{Type: "kickstart", Kickstart: "text\n"},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("linux installer media on windows source should return error")
	}
}

func TestVMImageWebhook_InstallerMedia_CloudbaseConfigRequiresMSI(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "windows", Distribution: "windows-server", Version: "2022"},
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://example.com/windows.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
				Installer: &InstallerMediaSpec{
					Type: "autounattend",
					Windows: &WindowsInstallerMediaSpec{
						CloudbaseInit: &CloudbaseInitSpec{
							MetadataServices: []string{"custom.MetadataService"},
						},
					},
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("cloudbaseInit config without cloudbaseInitMsi should be rejected")
	}
}

func TestVMImageWebhook_GuestAccess_ISOSource_Valid(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://releases.ubuntu.com/24.04/ubuntu.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
			Build: BuildSpec{
				GuestAccess: &GuestAccessSpec{
					Protocol: "ssh",
					Host:     "127.0.0.1",
					HostPort: 2222,
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err != nil {
		t.Errorf("guestAccess on iso source should be valid: %v", err)
	}
}

func TestVMImageWebhook_GuestAccess_NonISOSource_ReturnsError(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{Type: "cloud-image"},
			Build: BuildSpec{
				GuestAccess: &GuestAccessSpec{
					Protocol: "ssh",
					HostPort: 2222,
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("guestAccess on cloud-image source should return error")
	}
}

func TestVMImageWebhook_GuestAccess_RejectsNonLoopbackHost(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://releases.ubuntu.com/24.04/ubuntu.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
			Build: BuildSpec{
				GuestAccess: &GuestAccessSpec{
					Protocol: "ssh",
					Host:     "0.0.0.0",
					HostPort: 2222,
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("guestAccess with non-loopback host should return error")
	}
}

func TestVMImageWebhook_GuestAccess_RejectsLinuxWinRM(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://releases.ubuntu.com/24.04/ubuntu.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
			Build: BuildSpec{
				GuestAccess: &GuestAccessSpec{Protocol: "winrm", HostPort: 5986},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("linux guestAccess with winrm should return error")
	}
}

func TestVMImageWebhook_GuestAccess_RejectsWindowsCloudInitInjection(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "windows", Distribution: "windows-server", Version: "2022"},
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://example.com/windows.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
			Build: BuildSpec{
				GuestAccess: &GuestAccessSpec{
					Protocol: "winrm",
					HostPort: 5986,
					Credentials: &GuestCredentialsSpec{
						Generate:  &GuestGeneratedCredentialsSpec{Password: true},
						Injection: &GuestCredentialInjectionSpec{Method: "cloud-init"},
					},
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("windows guestAccess with cloud-init injection should return error")
	}
}

func TestVMImageWebhook_GuestAccess_InsecureSkipVerifyRequiresGeneratedPassword(t *testing.T) {
	https := true
	img := &VMImage{
		Spec: VMImageSpec{
			OS: OSSpec{Family: "windows", Distribution: "windows-server", Version: "2022"},
			Source: SourceSpec{
				Type:     "iso",
				URL:      "https://example.com/windows.iso",
				Checksum: "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
			Build: BuildSpec{
				GuestAccess: &GuestAccessSpec{
					Protocol: "winrm",
					HostPort: 5986,
					WinRM:    &WinRMAccessSpec{HTTPS: &https, InsecureSkipVerify: true},
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("insecureSkipVerify without generated password should return error")
	}
}

func TestVMImageWebhook_ProvisionerImagePolicy_RejectsMutableTag(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Provisioners: []ProvisionerSpec{{
				Type:  "custom",
				Image: "ghcr.io/yourorg/provisioner-custom:v1",
			}},
			Build: BuildSpec{
				Security: &BuildSecuritySpec{
					ProvisionerImages: &ImagePolicySpec{RequireDigest: true},
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil {
		t.Error("mutable provisioner image should return error when requireDigest is enabled")
	}
}

func TestVMImageWebhook_KVMRequiresDedicatedBuildNodeSelector(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Build: BuildSpec{
				Security: &BuildSecuritySpec{EnableKVM: true},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), `imagebuilder.io/kvm`) {
		t.Fatalf("error = %v, want KVM node selector rejection", err)
	}

	img.Spec.Build.NodeSelector = map[string]string{
		"imagebuilder.io/kvm":       "true",
		"imagebuilder.io/dedicated": "imagebuilder",
	}
	_, err = img.ValidateCreate()
	if err != nil {
		t.Fatalf("KVM with dedicated build node selector should be valid: %v", err)
	}
}

func TestVMImageWebhook_ProvisionerImagePolicy_AllowsPinnedAllowedImage(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: validMarketplaceSource(),
			Provisioners: []ProvisionerSpec{{
				Type:  "custom",
				Image: "ghcr.io/yourorg/provisioner-custom@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			}},
			Build: BuildSpec{
				Security: &BuildSecuritySpec{
					ProvisionerImages: &ImagePolicySpec{
						AllowedRegistries: []string{"ghcr.io/yourorg"},
						RequireDigest:     true,
						VerifySignature:   true,
					},
				},
			},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	_, err := img.ValidateCreate()
	if err != nil {
		t.Errorf("pinned allowed provisioner image should be valid: %v", err)
	}
}

func TestVMImageWebhook_Delete_AlwaysAllowed(t *testing.T) {
	img := &VMImage{}
	_, err := img.ValidateDelete()
	if err != nil {
		t.Errorf("ValidateDelete returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PlatformProvider webhook tests
// ---------------------------------------------------------------------------

func TestPlatformProviderWebhook_GlobalProductionPolicyRejectsMutablePlaintextProvider(t *testing.T) {
	SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{
		RequireMTLS:       true,
		RequireDigest:     true,
		RequireSignature:  true,
		AllowedRegistries: []string{"ghcr.io/anwendt"},
		ProviderNamespace: "imagebuilder-system",
	})
	t.Cleanup(func() { SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{}) })

	pp := &PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws"},
		Spec: PlatformProviderSpec{
			Package: "ghcr.io/anwendt/imagebuilder-provider-aws:v1",
		},
	}
	_, err := pp.ValidateCreate()
	if err == nil {
		t.Fatal("PlatformProvider should be rejected by global production policy")
	}
	for _, want := range []string{
		"pinned by digest",
		"provider mTLS is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestPlatformProviderWebhook_GlobalProductionPolicyAllowsPinnedSignedMTLSProvider(t *testing.T) {
	SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{
		RequireMTLS:       true,
		RequireDigest:     true,
		RequireSignature:  true,
		AllowedRegistries: []string{"ghcr.io/anwendt"},
		ProviderNamespace: "imagebuilder-system",
	})
	t.Cleanup(func() { SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{}) })

	pp := &PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws"},
		Spec: PlatformProviderSpec{
			Package: "ghcr.io/anwendt/imagebuilder-provider-aws@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Security: &ProviderPackageSecuritySpec{
				VerifySignature: true,
			},
			Transport: &ProviderTransportSpec{
				TLS: &ProviderTransportTLSSpec{
					Mode: "Mutual",
					CASecretRef: &ProviderTLSSecretRef{
						Name:      "provider-ca",
						Namespace: "imagebuilder-system",
					},
					ClientCertificateSecretRef: &ProviderTLSSecretRef{
						Name:      "operator-client",
						Namespace: "imagebuilder-system",
					},
					ServerCertificateSecretRef: &ProviderTLSSecretRef{
						Name:      "provider-server",
						Namespace: "imagebuilder-system",
					},
				},
			},
		},
	}
	_, err := pp.ValidateCreate()
	if err != nil {
		t.Fatalf("PlatformProvider should be accepted: %v", err)
	}
}

func TestPlatformProviderWebhook_RejectsForbiddenRegistry(t *testing.T) {
	SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{
		AllowedRegistries: []string{"ghcr.io/anwendt"},
	})
	t.Cleanup(func() { SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{}) })

	pp := &PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws"},
		Spec: PlatformProviderSpec{
			Package: "docker.io/library/provider:v1",
		},
	}
	_, err := pp.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), "registry is not allowed") {
		t.Fatalf("error = %v, want registry rejection", err)
	}
}

func TestPlatformProviderWebhook_RejectsUnapprovedServiceAccountAndToken(t *testing.T) {
	SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{
		RestrictServiceAccounts: true, AllowedServiceAccounts: []string{"provider-approved"}, ForbidServiceAccountToken: true,
	})
	t.Cleanup(func() { SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{}) })
	automount := true
	pp := &PlatformProvider{Spec: PlatformProviderSpec{
		Package: "ghcr.io/example/provider:v1", ServiceAccountName: "cluster-admin", AutomountServiceAccountToken: &automount,
	}}
	_, err := pp.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), "serviceAccountName") {
		t.Fatalf("error = %v, want ServiceAccount policy rejection", err)
	}
}

func TestPlatformProviderWebhook_RejectsServiceAccountToken(t *testing.T) {
	SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{ForbidServiceAccountToken: true})
	t.Cleanup(func() { SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{}) })
	automount := true
	pp := &PlatformProvider{Spec: PlatformProviderSpec{Package: "ghcr.io/example/provider:v1", AutomountServiceAccountToken: &automount}}
	_, err := pp.ValidateCreate()
	if err == nil || !strings.Contains(err.Error(), "automountServiceAccountToken") {
		t.Fatalf("error = %v, want token policy rejection", err)
	}
}

func TestPlatformProviderWebhook_AllowsApprovedServiceAccountWithoutToken(t *testing.T) {
	SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{
		RestrictServiceAccounts: true, AllowedServiceAccounts: []string{"provider-approved"}, ForbidServiceAccountToken: true,
	})
	t.Cleanup(func() { SetPlatformProviderAdmissionPolicy(PlatformProviderAdmissionPolicy{}) })
	automount := false
	pp := &PlatformProvider{Spec: PlatformProviderSpec{
		Package: "ghcr.io/example/provider:v1", ServiceAccountName: "provider-approved", AutomountServiceAccountToken: &automount,
	}}
	if _, err := pp.ValidateCreate(); err != nil {
		t.Fatalf("approved ServiceAccount rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ProviderConfig webhook tests
// ---------------------------------------------------------------------------

func TestProviderConfigWebhook_NoEndpoint_Valid(t *testing.T) {
	pc := &ProviderConfig{
		Spec: ProviderConfigSpec{
			Provider:    "aws",
			Credentials: CredentialsSpec{SecretRef: SecretRef{Name: "aws-creds"}},
		},
	}
	_, err := pc.ValidateCreate()
	if err != nil {
		t.Errorf("ValidateCreate without endpoint returned error: %v", err)
	}
}

func TestProviderConfigWebhook_ValidHTTPSEndpoint(t *testing.T) {
	pc := &ProviderConfig{
		Spec: ProviderConfigSpec{
			Provider:    "vsphere",
			Credentials: CredentialsSpec{SecretRef: SecretRef{Name: "vsphere-creds"}},
			Endpoint:    "https://vcenter.example.com/sdk",
		},
	}
	_, err := pc.ValidateCreate()
	if err != nil {
		t.Errorf("ValidateCreate with HTTPS endpoint returned error: %v", err)
	}
}

func TestProviderConfigWebhook_HTTPEndpoint_ReturnsError(t *testing.T) {
	pc := &ProviderConfig{
		Spec: ProviderConfigSpec{
			Provider:    "vsphere",
			Credentials: CredentialsSpec{SecretRef: SecretRef{Name: "vsphere-creds"}},
			Endpoint:    "http://vcenter.example.com/sdk",
		},
	}
	_, err := pc.ValidateCreate()
	if err == nil {
		t.Error("expected error for HTTP (non-HTTPS) endpoint")
	}
}

func TestProviderConfigWebhook_RawIPEndpoint_ReturnsError(t *testing.T) {
	pc := &ProviderConfig{
		Spec: ProviderConfigSpec{
			Provider:    "vsphere",
			Credentials: CredentialsSpec{SecretRef: SecretRef{Name: "vsphere-creds"}},
			Endpoint:    "https://192.168.1.100/sdk",
		},
	}
	_, err := pc.ValidateCreate()
	if err == nil {
		t.Error("expected error for raw IP address in endpoint (SSRF protection)")
	}
}

func TestProviderConfigWebhook_Insecure_ReturnsWarning(t *testing.T) {
	pc := &ProviderConfig{
		Spec: ProviderConfigSpec{
			Provider:    "vsphere",
			Credentials: CredentialsSpec{SecretRef: SecretRef{Name: "vsphere-creds"}},
			Endpoint:    "https://vcenter.example.com/sdk",
			Insecure:    true,
		},
	}
	warnings, err := pc.ValidateCreate()
	if err != nil {
		t.Errorf("ValidateCreate returned unexpected error: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected warning when spec.insecure=true")
	}
}

func TestProviderConfigWebhook_Delete_AlwaysAllowed(t *testing.T) {
	pc := &ProviderConfig{}
	_, err := pc.ValidateDelete()
	if err != nil {
		t.Errorf("ValidateDelete returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SSRF validation helper tests
// ---------------------------------------------------------------------------

func TestValidateNoSSRF_EmptyURL_OK(t *testing.T) {
	if err := validateNoSSRF("field", ""); err != nil {
		t.Errorf("empty URL should be accepted: %v", err)
	}
}

func TestValidateNoSSRF_HTTPScheme_Rejected(t *testing.T) {
	if err := validateNoSSRF("field", "http://example.com/file.iso"); err == nil {
		t.Error("http:// URL should be rejected")
	}
}

func TestValidateNoSSRF_RawIP_Rejected(t *testing.T) {
	if err := validateNoSSRF("field", "https://10.0.0.1/file"); err == nil {
		t.Error("raw private IP URL should be rejected")
	}
}

func TestValidateNoSSRF_LoopbackIP_Rejected(t *testing.T) {
	if err := validateNoSSRF("field", "https://127.0.0.1/file"); err == nil {
		t.Error("loopback IP URL should be rejected")
	}
}

func TestValidateNoSSRF_LinkLocalIP_Rejected(t *testing.T) {
	if err := validateNoSSRF("field", "https://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("link-local (metadata) IP should be rejected")
	}
}

func TestVMImageWebhook_ProvisionerGitSource_Valid(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "https://example.com/ubuntu.qcow2", Checksum: "sha256:abc"},
			Provisioners: []ProvisionerSpec{{
				Type: "shell",
				Source: &ProvisionerSourceSpec{Git: &GitProvisionerSourceSpec{
					URL:  "https://example.com/hardening.git",
					Ref:  "7f6e5d4c3b2a190817263544536271809abcdef0",
					Path: "scripts",
				}},
			}},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("provisioner git source should be valid: %v", err)
	}
}

func TestVMImageWebhook_ProvisionerGitSourceAuthSecret_Valid(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "https://example.com/ubuntu.qcow2", Checksum: "sha256:abc"},
			Provisioners: []ProvisionerSpec{{
				Type: "shell",
				Source: &ProvisionerSourceSpec{Git: &GitProvisionerSourceSpec{
					URL:  "https://example.com/private.git",
					Ref:  "7f6e5d4c3b2a190817263544536271809abcdef0",
					Path: "scripts",
					Auth: &GitProvisionerAuthSpec{SecretRef: &GitProvisionerAuthSecretRef{
						Name:     "private-git",
						TokenKey: "pat",
					}},
				}},
			}},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err != nil {
		t.Fatalf("provisioner git source auth secret should be valid: %v", err)
	}
}

func TestVMImageWebhook_ProvisionerGitSourceAuthSecret_RejectsEmptyName(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "https://example.com/ubuntu.qcow2", Checksum: "sha256:abc"},
			Provisioners: []ProvisionerSpec{{
				Type: "shell",
				Source: &ProvisionerSourceSpec{Git: &GitProvisionerSourceSpec{
					URL:  "https://example.com/private.git",
					Ref:  "main",
					Path: "scripts",
					Auth: &GitProvisionerAuthSpec{SecretRef: &GitProvisionerAuthSecretRef{}},
				}},
			}},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("provisioner git source auth secret without name should be rejected")
	}
}

func TestVMImageWebhook_ProvisionerGitSourceAuth_RejectsUserManagedRuntimePaths(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "https://example.com/ubuntu.qcow2", Checksum: "sha256:abc"},
			Provisioners: []ProvisionerSpec{{
				Type: "shell",
				Source: &ProvisionerSourceSpec{Git: &GitProvisionerSourceSpec{
					URL:  "https://example.com/private.git",
					Ref:  "main",
					Path: "scripts",
					Auth: &GitProvisionerAuthSpec{TokenPath: "/var/run/secrets/token"},
				}},
			}},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("user-managed git auth runtime paths should be rejected")
	}
}

func TestVMImageWebhook_ProvisionerGitSource_RejectsCredentialsInURL(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "https://example.com/ubuntu.qcow2", Checksum: "sha256:abc"},
			Provisioners: []ProvisionerSpec{{
				Type: "shell",
				Source: &ProvisionerSourceSpec{Git: &GitProvisionerSourceSpec{
					URL:  "https://token@example.com/private.git",
					Ref:  "main",
					Path: "scripts",
				}},
			}},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("git source credentials embedded in URL should be rejected")
	}
}

func TestVMImageWebhook_ProvisionerGitSource_RejectsInline(t *testing.T) {
	img := &VMImage{
		Spec: VMImageSpec{
			OS:     OSSpec{Family: "linux", Distribution: "ubuntu", Version: "24.04"},
			Source: SourceSpec{Type: "cloud-image", URL: "https://example.com/ubuntu.qcow2", Checksum: "sha256:abc"},
			Provisioners: []ProvisionerSpec{{
				Type:   "shell",
				Inline: "echo inline",
				Source: &ProvisionerSourceSpec{Git: &GitProvisionerSourceSpec{
					URL:  "https://example.com/hardening.git",
					Ref:  "main",
					Path: "scripts",
				}},
			}},
			Targets: []TargetSpec{{ProviderConfigRef: ProviderConfigRef{Name: "cfg"}, Format: "vmdk"}},
		},
	}
	if _, err := img.ValidateCreate(); err == nil {
		t.Fatal("provisioner git source with inline should be rejected")
	}
}
