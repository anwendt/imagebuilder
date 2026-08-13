package vsphere

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func validPluginConfig() platform.PluginConfig {
	return platform.PluginConfig{
		ProviderConfigName: "vsphere-prod",
		Endpoint:           "https://vcenter.example.com/sdk",
		SecretData: map[string][]byte{
			"username": []byte("administrator@vsphere.local"),
			"password": []byte("secret"),
		},
		Extra: map[string]string{
			"datacenter":       "dc01",
			"datastore":        "vsanDatastore",
			"uploadPathPrefix": "imagebuilder",
		},
	}
}

type fakeClient struct {
	uploadInput     uploadInput
	uploadedBody    []byte
	registerInput   registerInput
	remoteInput     vsphereRemoteBuildInput
	remoteState     *vsphereRemoteBuildState
	remoteCleanup   vsphereRemoteBuildInput
	cleanupMetadata map[string]string
	healthErr       error
}

func (f *fakeClient) UploadArtifact(_ context.Context, input uploadInput, body io.Reader) (*platform.UploadResult, error) {
	f.uploadInput = input
	f.uploadedBody, _ = io.ReadAll(body)
	return &platform.UploadResult{
		ProviderRef: datastoreReference(input.Datastore, input.DatastorePath),
		Metadata: map[string]string{
			"datacenter":    input.Datacenter,
			"datastore":     input.Datastore,
			"datastorePath": input.DatastorePath,
			"format":        string(input.Format),
			"checksum":      input.Checksum,
			"imageName":     input.ImageName,
		},
	}, nil
}

func (f *fakeClient) RegisterImage(_ context.Context, input registerInput) (*platform.ImageRef, error) {
	f.registerInput = input
	return &platform.ImageRef{
		ID:       input.ProviderRef,
		Name:     input.ImageName,
		Location: input.Datacenter,
		Tags:     input.Tags,
	}, nil
}

func (f *fakeClient) ReconcileRemoteBuild(_ context.Context, input vsphereRemoteBuildInput) (*vsphereRemoteBuildState, error) {
	f.remoteInput = input
	if f.remoteState != nil {
		return f.remoteState, nil
	}
	return &vsphereRemoteBuildState{
		OperationRef: "vsphere://remote-build/build-123?provisionerIndex=1&vmName=imagebuilder-build-123&vmRef=vm-123",
		Phase:        platform.RemoteBuildPhaseProvisioning,
		Message:      "provisioner completed",
	}, nil
}

func (f *fakeClient) CleanupRemoteBuild(_ context.Context, input vsphereRemoteBuildInput) error {
	f.remoteCleanup = input
	return nil
}

func (f *fakeClient) Cleanup(_ context.Context, metadata map[string]string) error {
	f.cleanupMetadata = metadata
	return nil
}

func (f *fakeClient) HealthCheck(_ context.Context) error {
	return f.healthErr
}

func newInitializedPlugin(t *testing.T) (*Plugin, *fakeClient) {
	t.Helper()
	client := &fakeClient{}
	p := &Plugin{client: client}
	if err := p.Init(context.Background(), validPluginConfig()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p, client
}

func TestPluginCapabilities(t *testing.T) {
	p := &Plugin{}
	if p.Name() != "vsphere" {
		t.Fatalf("Name() = %q, want vsphere", p.Name())
	}
	formats := map[platform.ImageFormat]bool{}
	for _, format := range p.SupportedFormats() {
		formats[format] = true
	}
	for _, want := range []platform.ImageFormat{platform.FormatOVA, platform.FormatOVF, platform.FormatVMDK} {
		if !formats[want] {
			t.Errorf("SupportedFormats() missing %q", want)
		}
	}
	families := map[platform.OSFamily]bool{}
	for _, family := range p.SupportedOS() {
		families[family] = true
	}
	for _, want := range []platform.OSFamily{platform.OSFamilyLinux, platform.OSFamilyWindows} {
		if !families[want] {
			t.Errorf("SupportedOS() missing %q", want)
		}
	}
	modes := p.SupportedBuildModes()
	if len(modes) != 2 || modes[0] != v1alpha1.BuildModeLocal || modes[1] != v1alpha1.BuildModeRemote {
		t.Fatalf("SupportedBuildModes() = %v, want local and remote", modes)
	}
}

func TestPluginInitValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*platform.PluginConfig)
	}{
		{name: "endpoint", mutate: func(cfg *platform.PluginConfig) { cfg.Endpoint = "" }},
		{name: "username", mutate: func(cfg *platform.PluginConfig) { delete(cfg.SecretData, "username") }},
		{name: "password", mutate: func(cfg *platform.PluginConfig) { delete(cfg.SecretData, "password") }},
		{name: "datacenter", mutate: func(cfg *platform.PluginConfig) { delete(cfg.Extra, "datacenter") }},
		{name: "datastore", mutate: func(cfg *platform.PluginConfig) { delete(cfg.Extra, "datastore") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validPluginConfig()
			tt.mutate(&cfg)
			p := &Plugin{client: &fakeClient{}}
			if err := p.Init(context.Background(), cfg); err == nil {
				t.Fatalf("Init should reject missing %s", tt.name)
			}
		})
	}
}

func TestPluginValidateFormats(t *testing.T) {
	p, _ := newInitializedPlugin(t)
	for _, format := range []string{"ova", "ovf", "vmdk"} {
		if err := p.Validate(context.Background(), v1alpha1.TargetSpec{Format: format}); err != nil {
			t.Errorf("Validate(%q) returned error: %v", format, err)
		}
	}
	if err := p.Validate(context.Background(), v1alpha1.TargetSpec{Format: "ami"}); err == nil {
		t.Fatal("Validate should reject unsupported format")
	}
}

func TestPluginUploadBuildsDatastoreReference(t *testing.T) {
	p, client := newInitializedPlugin(t)
	artifactPath := t.TempDir() + "/ubuntu.vmdk"
	if err := os.WriteFile(artifactPath, []byte("vmdk-data"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	result, err := p.Upload(context.Background(), &platform.BuildArtifact{
		Path:      artifactPath,
		Format:    platform.FormatVMDK,
		Checksum:  "sha256:abc123",
		SizeBytes: 42,
		OS:        platform.OSFamilyLinux,
		Metadata: map[string]string{
			"buildID":   "build/01",
			"imageName": "ubuntu-template",
		},
	})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	wantPath := "imagebuilder/build-01/ubuntu.vmdk"
	if client.uploadInput.DatastorePath != wantPath {
		t.Fatalf("DatastorePath = %q, want %q", client.uploadInput.DatastorePath, wantPath)
	}
	if result.ProviderRef != "[vsanDatastore] "+wantPath {
		t.Fatalf("ProviderRef = %q", result.ProviderRef)
	}
}

func TestPluginUploadRequiresBuildID(t *testing.T) {
	p, _ := newInitializedPlugin(t)
	_, err := p.Upload(context.Background(), &platform.BuildArtifact{
		Path:   "/workspace/out/ubuntu.vmdk",
		Format: platform.FormatVMDK,
	})
	if err == nil {
		t.Fatal("Upload should require buildID metadata")
	}
}

func TestPluginRegisterReturnsProviderRef(t *testing.T) {
	p, client := newInitializedPlugin(t)
	ref, err := p.Register(context.Background(), &platform.UploadResult{
		ProviderRef: "[vsanDatastore] imagebuilder/build-01/ubuntu.vmdk",
		Metadata: map[string]string{
			"imageName":  "ubuntu-template",
			"datacenter": "dc01",
			"datastore":  "vsanDatastore",
			"format":     "vmdk",
			"checksum":   "sha256:abc123",
		},
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if ref.ID != "[vsanDatastore] imagebuilder/build-01/ubuntu.vmdk" {
		t.Fatalf("ImageRef.ID = %q", ref.ID)
	}
	if client.registerInput.Format != platform.FormatVMDK {
		t.Fatalf("register format = %q", client.registerInput.Format)
	}
}

func TestPluginCleanupIsIdempotent(t *testing.T) {
	p, client := newInitializedPlugin(t)
	if err := p.Cleanup(context.Background(), &platform.BuildArtifact{Metadata: map[string]string{
		"vsphere.providerRef": "[vsanDatastore] imagebuilder/build-01/ubuntu.vmdk",
	}}); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	if client.cleanupMetadata["providerRef"] == "" {
		t.Fatal("cleanup providerRef was not passed")
	}
}

func TestPluginHealthCheckDelegates(t *testing.T) {
	p, client := newInitializedPlugin(t)
	client.healthErr = errors.New("not healthy")
	if err := p.HealthCheck(context.Background()); err == nil {
		t.Fatal("HealthCheck should return client error")
	}
}

func TestPluginReconcileRemoteBuildDelegates(t *testing.T) {
	p, client := newInitializedPlugin(t)
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-template",
		OSFamily:          platform.OSFamilyLinux,
		SourceType:        "snapshot",
		SourceProviderRef: "vm-100",
		Provisioners: []v1alpha1.ProvisionerSpec{{
			Type:   "shell",
			Inline: "echo ok",
		}},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "vsphere-prod"},
			Format:            "ova",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if result.Done || result.Phase != platform.RemoteBuildPhaseProvisioning {
		t.Fatalf("remote result = %#v, want provisioning", result)
	}
	if client.remoteInput.SourceRef != "vm-100" || len(client.remoteInput.Provisioners) != 1 {
		t.Fatalf("remote input = %#v", client.remoteInput)
	}
}

func TestPluginReconcileRemoteBuildAcceptsMarketplaceRef(t *testing.T) {
	p, client := newInitializedPlugin(t)
	_, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:    "build-123",
		ImageName:  "ubuntu-template",
		OSFamily:   platform.OSFamilyLinux,
		OSArch:     "amd64",
		SourceType: "marketplace",
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: "Canonical",
			Offer:     "ubuntu",
			SKU:       "24.04",
			Version:   "latest",
		},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "vsphere-prod"},
			Format:            "ova",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if client.remoteInput.SourceMarketplace == nil || client.remoteInput.SourceMarketplace.Offer != "ubuntu" {
		t.Fatalf("remote marketplace = %#v, want Ubuntu marketplace ref", client.remoteInput.SourceMarketplace)
	}
	if client.remoteInput.SourceRef != "" {
		t.Fatalf("remote source ref = %q, want resolver to fill it later", client.remoteInput.SourceRef)
	}
}

func TestPluginReconcileRemoteBuildReturnsImage(t *testing.T) {
	p, client := newInitializedPlugin(t)
	client.remoteState = &vsphereRemoteBuildState{
		OperationRef: "vsphere://remote-build/build-123?imageRef=vm-123",
		Phase:        platform.RemoteBuildPhaseReady,
		Done:         true,
		Image: &platform.ImageRef{
			ID:       "vm-123",
			Name:     "ubuntu-template",
			Location: "dc01",
		},
	}
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-template",
		OSFamily:          platform.OSFamilyLinux,
		SourceType:        "snapshot",
		SourceProviderRef: "vm-100",
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "vsphere-prod"},
			Format:            "ova",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if !result.Done || len(result.Images) != 1 || result.Images[0].ImageRef.ID != "vm-123" {
		t.Fatalf("remote result = %#v, want ready image", result)
	}
}
