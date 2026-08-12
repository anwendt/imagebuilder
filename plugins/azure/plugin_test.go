package azure

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

func TestClassifyAzureRemoteError_RateLimitIsTransient(t *testing.T) {
	err := classifyAzureRemoteError(&azcore.ResponseError{StatusCode: 429, ErrorCode: "TooManyRequests"})
	if !providererrors.IsTransient(err) {
		t.Fatalf("Azure 429 was not classified as transient: %v", err)
	}
}

func validConfig() platform.PluginConfig {
	return platform.PluginConfig{
		ProviderConfigName: "azure-prod",
		Region:             "westeurope",
		SecretData: map[string][]byte{
			"subscriptionId":    []byte("00000000-0000-0000-0000-000000000000"),
			"tenantId":          []byte("11111111-1111-1111-1111-111111111111"),
			"clientId":          []byte("22222222-2222-2222-2222-222222222222"),
			"clientSecret":      []byte("secret"),
			"storageAccountKey": []byte("storage-key"),
		},
		Extra: map[string]string{
			"resourceGroup":    "rg-imagebuilder-prod",
			"storageAccount":   "imagebuilderprod",
			"storageContainer": "vhds",
		},
	}
}

type fakeClient struct {
	uploadedContainer string
	uploadedBlob      string
	uploadedPath      string
	registerInput     registerInput
	remoteInput       azureRemoteBuildInput
	remoteState       *azureRemoteBuildState
	remoteCleanup     azureRemoteBuildInput
	cleanupMetadata   map[string]string
	healthErr         error
}

func (f *fakeClient) UploadBlob(_ context.Context, container, blobName, filePath string) (string, error) {
	f.uploadedContainer = container
	f.uploadedBlob = blobName
	f.uploadedPath = filePath
	return "https://imagebuilderprod.blob.core.windows.net/" + container + "/" + blobName, nil
}

func (f *fakeClient) RegisterImage(_ context.Context, input registerInput) (*platform.ImageRef, error) {
	f.registerInput = input
	return &platform.ImageRef{
		ID:       "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/" + input.ResourceGroup + "/providers/Microsoft.Compute/images/" + input.ImageName,
		Name:     input.ImageName,
		Location: input.Location,
		Tags:     input.Tags,
	}, nil
}

func (f *fakeClient) ReconcileRemoteBuild(_ context.Context, input azureRemoteBuildInput) (*azureRemoteBuildState, error) {
	f.remoteInput = input
	if f.remoteState != nil {
		return f.remoteState, nil
	}
	return &azureRemoteBuildState{
		OperationRef: "azure://remote-build/" + input.BuildID + "?provisionerIndex=1&vmName=ib-build-123-vm",
		Phase:        platform.RemoteBuildPhaseProvisioning,
		Message:      "provisioner completed",
	}, nil
}

func (f *fakeClient) CleanupRemoteBuild(_ context.Context, input azureRemoteBuildInput) error {
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
	if err := p.Init(context.Background(), validConfig()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p, client
}

func TestPlugin_Capabilities(t *testing.T) {
	p := &Plugin{}
	if p.Name() != "azure" {
		t.Fatalf("Name() = %q, want azure", p.Name())
	}
	if got := p.SupportedBuildModes(); len(got) != 2 || got[0] != v1alpha1.BuildModeLocal || got[1] != v1alpha1.BuildModeRemote {
		t.Fatalf("SupportedBuildModes() = %v, want local and remote", got)
	}
	formats := p.SupportedFormats()
	if len(formats) != 1 || formats[0] != platform.FormatVHD {
		t.Fatalf("SupportedFormats() = %v, want [vhd]", formats)
	}
}

func TestPlugin_Init_RequiresProductionConfig(t *testing.T) {
	cfg := validConfig()
	delete(cfg.SecretData, "clientSecret")
	p := &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Fatal("Init without clientSecret should fail")
	}
}

func TestPlugin_Validate_AcceptsOnlyVHD(t *testing.T) {
	p, _ := newInitializedPlugin(t)
	if err := p.Validate(context.Background(), v1alpha1.TargetSpec{Format: "vhd"}); err != nil {
		t.Fatalf("Validate(vhd) returned error: %v", err)
	}
	for _, format := range []string{"raw", "ami", "ova", "qcow2"} {
		if err := p.Validate(context.Background(), v1alpha1.TargetSpec{Format: format}); err == nil {
			t.Fatalf("Validate(%s) should fail", format)
		}
	}
}

func TestPlugin_UploadAndRegisterManagedImage(t *testing.T) {
	p, client := newInitializedPlugin(t)
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "disk.vhd")
	if err := writeFixedVHD(artifactPath, 512); err != nil {
		t.Fatal(err)
	}

	upload, err := p.Upload(context.Background(), &platform.BuildArtifact{
		Path:      artifactPath,
		Format:    platform.FormatVHD,
		Checksum:  "sha256:abc",
		SizeBytes: 3,
		OS:        platform.OSFamilyLinux,
		Metadata: map[string]string{
			"buildID":   "build-123",
			"imageName": "ubuntu-24-04",
		},
	})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if client.uploadedContainer != "vhds" {
		t.Fatalf("uploaded container = %q, want vhds", client.uploadedContainer)
	}
	if upload.ProviderRef == "" || upload.Metadata["blobURL"] == "" {
		t.Fatalf("upload = %#v, want blob provider ref", upload)
	}

	ref, err := p.Register(context.Background(), upload)
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if ref.Name != "ubuntu-24-04" {
		t.Fatalf("image name = %q, want ubuntu-24-04", ref.Name)
	}
	if client.registerInput.ResourceGroup != "rg-imagebuilder-prod" ||
		client.registerInput.Location != "westeurope" ||
		client.registerInput.BlobURL != upload.ProviderRef ||
		client.registerInput.OS != platform.OSFamilyLinux {
		t.Fatalf("register input = %#v", client.registerInput)
	}
}

func TestPlugin_UploadRejectsDynamicVHD(t *testing.T) {
	p, _ := newInitializedPlugin(t)
	artifactPath := filepath.Join(t.TempDir(), "dynamic.vhd")
	if err := writeVHD(artifactPath, 512, 3); err != nil {
		t.Fatal(err)
	}
	_, err := p.Upload(context.Background(), &platform.BuildArtifact{
		Path:     artifactPath,
		Format:   platform.FormatVHD,
		OS:       platform.OSFamilyLinux,
		Metadata: map[string]string{"buildID": "build-123"},
	})
	if err == nil {
		t.Fatal("Upload should reject non-fixed VHDs")
	}
}

func TestPlugin_ReconcileRemoteBuild_RegistersSnapshot(t *testing.T) {
	p, client := newInitializedPlugin(t)
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-snapshot",
		OSFamily:          platform.OSFamilyLinux,
		SourceType:        "snapshot",
		SourceProviderRef: "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Compute/snapshots/source",
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-prod"},
			Format:            "vhd",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if !result.Done || result.Phase != platform.RemoteBuildPhaseReady {
		t.Fatalf("remote result = %#v, want ready", result)
	}
	if client.registerInput.SnapshotID == "" {
		t.Fatalf("SnapshotID was not passed to register input: %#v", client.registerInput)
	}
}

func TestPlugin_ReconcileRemoteBuild_WithProvisionersDelegatesRemoteBuild(t *testing.T) {
	p, client := newInitializedPlugin(t)
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-provisioned",
		OSFamily:          platform.OSFamilyLinux,
		SourceType:        "managed-disk",
		SourceProviderRef: "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Compute/disks/source",
		Provisioners: []v1alpha1.ProvisionerSpec{{
			Type:   "shell",
			Inline: "echo ok",
		}},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-prod"},
			Format:            "vhd",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if result.Done || result.Phase != platform.RemoteBuildPhaseProvisioning {
		t.Fatalf("remote result = %#v, want provisioning", result)
	}
	if len(client.remoteInput.Provisioners) != 1 || client.remoteInput.SourceType != "managed-disk" {
		t.Fatalf("remote input = %#v", client.remoteInput)
	}
}

func TestPlugin_ReconcileRemoteBuild_MarketplaceDelegatesRemoteBuild(t *testing.T) {
	p, client := newInitializedPlugin(t)
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:    "build-123",
		ImageName:  "ubuntu-marketplace",
		OSFamily:   platform.OSFamilyLinux,
		SourceType: "marketplace",
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: "Canonical",
			Offer:     "ubuntu-24_04-lts",
			SKU:       "server",
			Version:   "latest",
		},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-prod"},
			Format:            "vhd",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if result.Done || result.Phase != platform.RemoteBuildPhaseProvisioning {
		t.Fatalf("remote result = %#v, want provisioning", result)
	}
	if client.remoteInput.SourceType != "marketplace" || client.remoteInput.SourceMarketplace == nil {
		t.Fatalf("remote input = %#v, want marketplace source", client.remoteInput)
	}
	if client.remoteInput.SourceMarketplace.Publisher != "Canonical" ||
		client.remoteInput.SourceMarketplace.Offer != "ubuntu-24_04-lts" ||
		client.remoteInput.SourceMarketplace.SKU != "server" ||
		client.remoteInput.SourceMarketplace.Version != "latest" {
		t.Fatalf("remote marketplace = %#v, want Ubuntu marketplace ref", client.remoteInput.SourceMarketplace)
	}
}

func TestPlugin_ReconcileRemoteBuild_WithProvisionersReturnsImage(t *testing.T) {
	p, client := newInitializedPlugin(t)
	client.remoteState = &azureRemoteBuildState{
		OperationRef: "azure://remote-build/build-123?imageId=/subscriptions/000/resourceGroups/rg/providers/Microsoft.Compute/images/img",
		Phase:        platform.RemoteBuildPhaseReady,
		Done:         true,
		Image: &platform.ImageRef{
			ID:       "/subscriptions/000/resourceGroups/rg/providers/Microsoft.Compute/images/img",
			Name:     "ubuntu-provisioned",
			Location: "westeurope",
		},
	}
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-provisioned",
		OSFamily:          platform.OSFamilyLinux,
		SourceType:        "snapshot",
		SourceProviderRef: "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Compute/snapshots/source",
		Provisioners: []v1alpha1.ProvisionerSpec{{
			Type:   "shell",
			Inline: "echo ok",
		}},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-prod"},
			Format:            "vhd",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if !result.Done || len(result.Images) != 1 || result.Images[0].ImageRef.ID == "" {
		t.Fatalf("remote result = %#v, want ready image", result)
	}
}

func TestPlugin_Init_AllowsWorkloadIdentityWithoutClientSecret(t *testing.T) {
	cfg := validConfig()
	cfg.Extra["authMode"] = "workloadIdentity"
	delete(cfg.SecretData, "clientSecret")
	delete(cfg.SecretData, "storageAccountKey")
	p := &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init with workload identity returned error: %v", err)
	}
}

func TestPlugin_CleanupDelegatesToClient(t *testing.T) {
	p, client := newInitializedPlugin(t)
	err := p.Cleanup(context.Background(), &platform.BuildArtifact{Metadata: map[string]string{
		"blobName":  "build-123/disk.vhd",
		"imageName": "ubuntu-24-04",
	}})
	if err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	if client.cleanupMetadata["blobName"] == "" || client.cleanupMetadata["imageName"] == "" {
		t.Fatalf("cleanup metadata = %#v", client.cleanupMetadata)
	}
}

func writeFixedVHD(path string, currentSize int64) error {
	return writeVHD(path, currentSize, vhdDiskTypeFixed)
}

func writeVHD(path string, currentSize int64, diskType uint32) error {
	footer := make([]byte, vhdFooterSize)
	copy(footer[0:8], []byte(vhdCookie))
	binary.BigEndian.PutUint64(footer[48:56], uint64(currentSize))
	binary.BigEndian.PutUint32(footer[60:64], diskType)
	var sum uint32
	for _, b := range footer {
		sum += uint32(b)
	}
	binary.BigEndian.PutUint32(footer[64:68], ^sum)
	data := make([]byte, currentSize)
	data = append(data, footer...)
	return os.WriteFile(path, data, 0o600)
}
