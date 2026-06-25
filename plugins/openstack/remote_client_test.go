package openstack

import (
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestOpenStackRemoteSettingsRequireSSHMaterialForProvisioners(t *testing.T) {
	settings := openStackRemoteSettings{FlavorRef: "m1.small", KeyName: "builder", SSHUser: "ubuntu"}
	err := settings.validate(openStackRemoteBuildInput{
		BuildID:      "build-123",
		Provisioners: []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}},
	}, openStackConfig{})
	if err == nil {
		t.Fatal("settings should require remotePrivateKey for provisioners")
	}
}

func TestValidateOpenStackRemoteProvisionersRejectsWindowsShell(t *testing.T) {
	err := validateOpenStackRemoteProvisioners(openStackRemoteBuildInput{
		OSFamily:     platform.OSFamilyWindows,
		Provisioners: []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}},
	})
	if err == nil {
		t.Fatal("Windows shell provisioners should be rejected")
	}
}

func TestOpenStackRemoteOperationRefRoundTrip(t *testing.T) {
	ref := openStackRemoteOperationRef{
		BuildID:          "build-123",
		ServerID:         "srv-1",
		ServerName:       "ib-build-123-remote",
		ImageID:          "img-1",
		ProvisionerIndex: 2,
	}
	parsed, err := parseOpenStackRemoteOperationRef(ref.String())
	if err != nil {
		t.Fatalf("parse returned error: %v", err)
	}
	if parsed != ref {
		t.Fatalf("parsed ref = %#v, want %#v", parsed, ref)
	}
}

func TestOpenStackServerAddressPrefersNamedNetworkIPv4(t *testing.T) {
	server := &openStackServer{
		Addresses: map[string]any{
			"private": []any{
				map[string]any{"addr": "2001:db8::1", "version": float64(6)},
				map[string]any{"addr": "10.0.0.5", "version": float64(4)},
			},
			"public": []any{
				map[string]any{"addr": "203.0.113.10", "version": float64(4)},
			},
		},
	}
	if got := openStackServerAddress(server, "public"); got != "203.0.113.10" {
		t.Fatalf("address = %q, want public IPv4", got)
	}
	if got := openStackServerAddress(server, "private"); got != "10.0.0.5" {
		t.Fatalf("address = %q, want private IPv4", got)
	}
}

func TestSelectOpenStackMarketplaceImageChoosesNewestOTCUbuntu24(t *testing.T) {
	input := openStackRemoteBuildInput{
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: "Open Telekom Cloud",
			Offer:     "ubuntu",
			SKU:       "24.04",
			Version:   "latest",
		},
		OSArch: "amd64",
	}
	list := []images.Image{
		{
			ID:        "img-old",
			Name:      "Standard_Ubuntu_24.04_latest",
			Status:    images.ImageStatusActive,
			UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			ID:        "img-new",
			Name:      "Standard_Ubuntu_24.04_latest",
			Status:    images.ImageStatusActive,
			UpdatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			ID:        "img-arm",
			Name:      "Standard_Ubuntu_24.04_ARM64_latest",
			Status:    images.ImageStatusActive,
			UpdatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	got := selectOpenStackMarketplaceImage(input, list)
	if got == nil || got.ID != "img-new" {
		t.Fatalf("selected image = %#v, want img-new", got)
	}
}

func TestOpenStackMarketplaceImageMatchesPinnedVersion(t *testing.T) {
	input := openStackRemoteBuildInput{
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: "Open Telekom Cloud",
			Offer:     "ubuntu",
			SKU:       "24.04",
			Version:   "20260601",
		},
		OSArch: "amd64",
	}
	image := images.Image{
		ID:     "img-pinned",
		Name:   "Standard_Ubuntu_24.04_20260601",
		Status: images.ImageStatusActive,
	}
	if !openStackMarketplaceImageMatches(input, image) {
		t.Fatal("expected pinned OTC Ubuntu image to match")
	}
}
