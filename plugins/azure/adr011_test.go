package azure

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

func controllerEvidence() *v1alpha1.EvidenceSpec {
	return &v1alpha1.EvidenceSpec{
		Required:           true,
		Publisher:          v1alpha1.EvidencePublisherController,
		SBOMFormat:         "cyclonedx-json",
		RegistryRepository: "oci://registry.example.com/vmimages",
		Subject:            &v1alpha1.EvidenceSubject{Name: "ubuntu-2604-base", Version: "1.2.0", Platform: "azure"},
	}
}

func TestValidateAzureRemoteProvisioners_ControllerMode(t *testing.T) {
	later := []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "a"}, {Type: "evidence", Inline: "e"}, {Type: "shell", Inline: "deprovision"}}
	tests := []struct {
		name    string
		input   azureRemoteBuildInput
		wantErr string
	}{
		{name: "controller allows later provisioners", input: azureRemoteBuildInput{OSFamily: platform.OSFamilyLinux, Evidence: controllerEvidence(), Provisioners: later}},
		{name: "provider keeps final rule", input: azureRemoteBuildInput{OSFamily: platform.OSFamilyLinux, Evidence: &v1alpha1.EvidenceSpec{Required: true}, Provisioners: later}, wantErr: "must be the final"},
		{name: "controller rejects two", input: azureRemoteBuildInput{OSFamily: platform.OSFamilyLinux, Evidence: controllerEvidence(), Provisioners: append(later, v1alpha1.ProvisionerSpec{Type: "evidence", Inline: "e"})}, wantErr: "exactly one"},
		{name: "controller rejects windows", input: azureRemoteBuildInput{OSFamily: platform.OSFamilyWindows, Evidence: controllerEvidence(), Provisioners: []v1alpha1.ProvisionerSpec{{Type: "evidence", Inline: "e"}}}, wantErr: "Linux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAzureRemoteProvisioners(tt.input)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestAzureRemoteSettings_ControllerAndVerifyBoot(t *testing.T) {
	tests := []struct {
		name     string
		extra    map[string]string
		input    azureRemoteBuildInput
		wantErr  string
		wantBoot bool
	}{
		{name: "controller needs no identity or key", extra: map[string]string{"remote.networkInterfaceId": "nic"}, input: azureRemoteBuildInput{BuildID: "b", OSFamily: platform.OSFamilyLinux, Evidence: controllerEvidence()}},
		{name: "controller windows", extra: map[string]string{"remote.networkInterfaceId": "nic"}, input: azureRemoteBuildInput{BuildID: "b", OSFamily: platform.OSFamilyWindows, Evidence: controllerEvidence()}, wantErr: "Linux"},
		{name: "verify boot", extra: map[string]string{"remote.networkInterfaceId": "nic", "remote.verifyBoot": "true"}, input: azureRemoteBuildInput{BuildID: "b", OSFamily: platform.OSFamilyLinux}, wantBoot: true},
		{name: "verify boot without nic", extra: map[string]string{"remote.verifyBoot": "TRUE"}, input: azureRemoteBuildInput{BuildID: "b", OSFamily: platform.OSFamilyLinux}, wantErr: "networkInterfaceId", wantBoot: true},
		{name: "verify boot windows", extra: map[string]string{"remote.networkInterfaceId": "nic", "remote.verifyBoot": "true"}, input: azureRemoteBuildInput{BuildID: "b", OSFamily: platform.OSFamilyWindows}, wantErr: "Linux", wantBoot: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := azureRemoteSettingsFromExtra(tt.extra)
			if settings.VerifyBoot != tt.wantBoot {
				t.Fatalf("VerifyBoot = %v", settings.VerifyBoot)
			}
			err := settings.validate(tt.input)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
	if azureRemoteSettingsFromExtra(map[string]string{"httpsProxy": " http://p:3128 "}).HTTPSProxy != "http://p:3128" {
		t.Fatal("httpsProxy not read")
	}
}

func TestAzureRunCommand_ControllerEvidence(t *testing.T) {
	input := azureRemoteBuildInput{
		BuildID:           "build-1",
		ImageName:         "ubuntu",
		OSFamily:          platform.OSFamilyLinux,
		Evidence:          controllerEvidence(),
		EvidenceCosignKey: "azurekms://must-not-appear",
		SourceMarketplace: &v1alpha1.MarketplaceRef{Publisher: "Canonical", Offer: "o", SKU: "s", Version: "1"},
	}
	id, script, err := azureRunCommand(input, v1alpha1.ProvisionerSpec{Type: "evidence", Inline: "collect"})
	if err != nil || id != "RunShellScript" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	body := script[0]
	for _, want := range []string{"export IMAGEBUILDER_EVIDENCE_OUTBOX='/var/lib/imagebuilder/evidence'", "export IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM='azure'", "azure-marketplace://Canonical/o/s/1", "\ncollect"} {
		if !strings.Contains(body, want) {
			t.Fatalf("script missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "COSIGN") || strings.Contains(body, "must-not-appear") {
		t.Fatalf("controller evidence script carries signing config:\n%s", body)
	}
	input.OSFamily = platform.OSFamilyWindows
	if _, _, err := azureRunCommand(input, v1alpha1.ProvisionerSpec{Type: "evidence", Inline: "collect"}); err == nil {
		t.Fatal("controller evidence on Windows must fail")
	}
}

func TestResolveAzureGalleryVersion(t *testing.T) {
	tests := []struct {
		name, gallery, configured, image, want string
		wantErr                                bool
	}{
		{name: "configured wins", gallery: "g", configured: "9.9.9", image: "1.2.0", want: "9.9.9"},
		{name: "derived", gallery: "g", image: "1.2.0", want: "1.2.0"},
		{name: "no gallery", image: "1.2.0", want: ""},
		{name: "no version", gallery: "g", want: ""},
		{name: "invalid", gallery: "g", image: "1.2.0-rc1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAzureGalleryVersion(tt.gallery, tt.configured, tt.image)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestAzureRemoteOperationRef_ADR011Fields(t *testing.T) {
	ref := azureRemoteOperationRef{BuildID: "b", ImageID: "img", EvidenceBlob: "p/evidence/b.tar.gz", EvidenceSHA256: strings.Repeat("a", 64), VerifyBoot: verifyBootPending, OwnedImage: "img", OwnedGalleryVersion: "1.2.0"}
	parsed, err := parseAzureRemoteOperationRef(ref.String())
	if err != nil || parsed != ref {
		t.Fatalf("parsed = %+v, err = %v", parsed, err)
	}
}

func TestPlugin_ReconcileRemoteBuild_PassesADR011FieldsAndBundle(t *testing.T) {
	p, client := newInitializedPlugin(t)
	bundle := &platform.RemoteEvidenceBundle{Content: []byte{1}, SHA256: "ab"}
	client.remoteState = &azureRemoteBuildState{Done: true, Image: &platform.ImageRef{ID: "img"}, EvidenceBundle: bundle}
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:                    "b",
		SourceType:                 "marketplace",
		SourceMarketplace:          &v1alpha1.MarketplaceRef{Publisher: "p", Offer: "o", SKU: "s", Version: "1"},
		Target:                     v1alpha1.TargetSpec{Format: "vhd"},
		Evidence:                   controllerEvidence(),
		ImageVersion:               "1.2.0",
		EvidenceBundleAcknowledged: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EvidenceBundle != bundle || client.remoteInput.ImageVersion != "1.2.0" || !client.remoteInput.EvidenceBundleAcknowledged {
		t.Fatalf("result = %+v, input = %+v", result, client.remoteInput)
	}
}

func TestPlugin_ReconcileRemoteBuild_DerivesGalleryVersionForExistingSource(t *testing.T) {
	client := &fakeClient{}
	p := &Plugin{client: client}
	cfg := validConfig()
	cfg.Extra["galleryName"] = "gal"
	cfg.Extra["galleryImageName"] = "ubuntu"
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	req := &platform.RemoteBuildRequest{BuildID: "b", SourceType: "snapshot", SourceProviderRef: "/snap", Target: v1alpha1.TargetSpec{Format: "vhd"}, ImageVersion: "1.2.0"}
	if _, err := p.ReconcileRemoteBuild(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if client.registerInput.GalleryVersion != "1.2.0" {
		t.Fatalf("gallery version = %q", client.registerInput.GalleryVersion)
	}
	if err := p.CleanupRemoteBuild(context.Background(), req); err != nil || client.cleanupMetadata["galleryVersion"] != "1.2.0" {
		t.Fatalf("cleanup metadata = %v, err = %v", client.cleanupMetadata, err)
	}
	req.ImageVersion = "latest"
	if _, err := p.ReconcileRemoteBuild(context.Background(), req); err == nil {
		t.Fatal("invalid derived gallery version must fail")
	}
}

func TestPlugin_Init_VerifyBootRequiresGeneralized(t *testing.T) {
	p := &Plugin{client: &fakeClient{}}
	cfg := validConfig()
	cfg.Extra["remote.verifyBoot"] = "true"
	cfg.Extra["osState"] = "specialized"
	if err := p.Init(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "generalized") {
		t.Fatalf("Init error = %v, want generalized requirement", err)
	}
}

func TestClassifyAzureRemoteError_KeepsExplicitClassification(t *testing.T) {
	err := providererrors.Terminal(errors.New(verifyBootFailedText + "x"))
	if providererrors.IsTransient(classifyAzureRemoteError(err)) {
		t.Fatal("terminal error became transient")
	}
}
