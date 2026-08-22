package gcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/googleapi"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

type fakeImageClient struct {
	uploadBucket  string
	uploadObject  string
	uploadPath    string
	createInput   createImageInput
	remoteInput   createImageInput
	deletedImage  string
	deletedBucket string
	deletedObject string
	image         *platform.ImageRef
	err           error
}

func (f *fakeImageClient) UploadObject(_ context.Context, bucket, object, filePath string) (string, error) {
	f.uploadBucket, f.uploadObject, f.uploadPath = bucket, object, filePath
	if f.err != nil {
		return "", f.err
	}
	return gcsImportURL(bucket, object), nil
}
func (f *fakeImageClient) CreateImageFromObject(_ context.Context, input createImageInput) (*platform.ImageRef, error) {
	f.createInput = input
	if f.err != nil {
		return nil, f.err
	}
	return &platform.ImageRef{ID: "https://compute.googleapis.com/compute/v1/projects/test/global/images/ubuntu", Name: input.Name, Location: "global", Tags: input.Labels}, nil
}
func (f *fakeImageClient) CreateImageFromSource(_ context.Context, input createImageInput) (*platform.ImageRef, error) {
	f.remoteInput = input
	if f.err != nil {
		return nil, f.err
	}
	return &platform.ImageRef{ID: "projects/test/global/images/ubuntu-copy", Name: input.Name, Location: "global"}, nil
}
func (f *fakeImageClient) GetImage(_ context.Context, name string) (*platform.ImageRef, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.image != nil {
		return f.image, nil
	}
	return &platform.ImageRef{ID: "projects/test/global/images/" + name, Name: name, Location: "global"}, nil
}
func (f *fakeImageClient) DeleteImage(_ context.Context, name string) error {
	f.deletedImage = name
	return f.err
}
func (f *fakeImageClient) DeleteObject(_ context.Context, bucket, object string) error {
	f.deletedBucket, f.deletedObject = bucket, object
	return f.err
}
func (f *fakeImageClient) HealthCheck(context.Context) error { return f.err }

func initializedPlugin(client imageClient) *Plugin {
	return &Plugin{log: slog.Default(), config: config{providerConfigName: "gcp-prod", project: "test-project", bucket: "images", objectPrefix: "imagebuilder", imageFamily: "ubuntu", storageLocation: "eu"}, client: client}
}

func TestPluginCapabilities(t *testing.T) {
	p := &Plugin{}
	if len(p.SupportedFormats()) != 1 || p.SupportedFormats()[0] != platform.FormatGCETarball {
		t.Fatalf("formats=%v", p.SupportedFormats())
	}
	if len(p.SupportedBuildModes()) != 2 {
		t.Fatalf("modes=%v", p.SupportedBuildModes())
	}
}

func TestInitRejectsUnsafeEndpointOverride(t *testing.T) {
	p := &Plugin{}
	err := p.Init(context.Background(), platform.PluginConfig{ProviderConfigName: "gcp-prod", Extra: map[string]string{"project": "test-project", "gcsBucket": "images", "storageUploadEndpoint": "https://169.254.169.254/upload"}})
	if err == nil || !strings.Contains(err.Error(), "endpoint rejected") {
		t.Fatalf("error = %v, want unsafe endpoint rejection", err)
	}
}

func TestNewSDKClientRejectsUnexpectedCredentialType(t *testing.T) {
	_, err := newSDKClient(context.Background(), platform.PluginConfig{
		SecretData: map[string][]byte{
			"serviceAccountJSON": []byte(`{"type":"authorized_user"}`),
		},
	}, config{})
	if err == nil || !strings.Contains(err.Error(), "expected type") {
		t.Fatalf("newSDKClient error = %v, want unexpected credential type rejection", err)
	}
}

func TestValidateRequiresGCEArchive(t *testing.T) {
	p := initializedPlugin(&fakeImageClient{})
	if err := p.Validate(context.Background(), v1alpha1.TargetSpec{Format: "raw"}); err == nil {
		t.Fatal("raw should be rejected")
	}
	if err := p.Validate(context.Background(), v1alpha1.TargetSpec{Format: "gcetarball"}); err != nil {
		t.Fatalf("gcetarball rejected: %v", err)
	}
}
func TestLocalUploadAndRegister(t *testing.T) {
	client := &fakeImageClient{}
	p := initializedPlugin(client)
	result, err := p.Upload(context.Background(), &platform.BuildArtifact{Path: "/workspace/disk.tar.gz", Format: platform.FormatGCETarball, Checksum: "sha256:abc", OS: platform.OSFamilyLinux, Metadata: map[string]string{"buildID": "build-123", "imageName": "Ubuntu 24.04", "arch": "amd64", "target.tag.team": "platform"}})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if result.ProviderRef != "https://storage.googleapis.com/images/imagebuilder/build-123-abc.tar.gz" {
		t.Fatalf("providerRef=%q", result.ProviderRef)
	}
	ref, err := p.Register(context.Background(), result)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if ref.Name != "ubuntu-24-04" || client.createInput.GCSURI != result.ProviderRef || client.createInput.Architecture != "X86_64" {
		t.Fatalf("image=%#v input=%#v", ref, client.createInput)
	}
	if client.deletedObject != "imagebuilder/build-123-abc.tar.gz" {
		t.Fatalf("deleted object=%q", client.deletedObject)
	}
}
func TestCleanupDeletesImageAndObject(t *testing.T) {
	client := &fakeImageClient{}
	p := initializedPlugin(client)
	err := p.Cleanup(context.Background(), &platform.BuildArtifact{Metadata: map[string]string{"imageRef": "projects/test/global/images/old", "bucket": "images", "object": "old.tar.gz"}})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if client.deletedImage != "old" || client.deletedObject != "old.tar.gz" {
		t.Fatalf("image=%q object=%q", client.deletedImage, client.deletedObject)
	}
}
func TestRemoteImageCopy(t *testing.T) {
	client := &fakeImageClient{}
	p := initializedPlugin(client)
	result, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{BuildID: "build-123", ImageName: "ubuntu-copy", SourceType: "cloud-image", SourceProviderRef: "projects/debian-cloud/global/images/debian-12", OSArch: "amd64", Target: v1alpha1.TargetSpec{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "gcp-prod"}, Format: "gcetarball"}})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild: %v", err)
	}
	if !result.Done || result.Images[0].ImageRef.Name != "ubuntu-copy" || client.remoteInput.SourceImage == "" {
		t.Fatalf("result=%#v input=%#v", result, client.remoteInput)
	}
}
func TestRemoteMarketplaceReferenceIsPreserved(t *testing.T) {
	client := &fakeImageClient{}
	p := initializedPlugin(client)
	_, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{BuildID: "build-123", ImageName: "ubuntu-copy", SourceType: "marketplace", SourceMarketplace: &v1alpha1.MarketplaceRef{Publisher: "ubuntu-os-cloud", Version: "ubuntu-2404-noble-amd64-v20260101"}, Target: v1alpha1.TargetSpec{Format: "gcetarball"}})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild: %v", err)
	}
	if client.remoteInput.SourceImage != "projects/ubuntu-os-cloud/global/images/ubuntu-2404-noble-amd64-v20260101" {
		t.Fatalf("source image=%q", client.remoteInput.SourceImage)
	}
}
func TestMarketplaceReferenceRejectsInvalidSegments(t *testing.T) {
	_, err := marketplaceSource(&v1alpha1.MarketplaceRef{Publisher: "Ubuntu OS", Version: "noble"})
	if err == nil {
		t.Fatal("invalid publisher should be rejected")
	}
}
func TestRemoteProvisionersRejected(t *testing.T) {
	p := initializedPlugin(&fakeImageClient{})
	_, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{BuildID: "b", SourceType: "cloud-image", SourceProviderRef: "image", Target: v1alpha1.TargetSpec{Format: "gcetarball"}, Provisioners: []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}}})
	if err == nil {
		t.Fatal("remote provisioner should be rejected")
	}
}
func TestClassifyErrorMarksServiceErrorsTransient(t *testing.T) {
	err := classifyError(&googleapi.Error{Code: 429, Message: "rate limited", Header: http.Header{"Retry-After": []string{"17"}}})
	if !providererrors.IsTransient(err) {
		t.Fatalf("429 not transient: %v", err)
	}
	if delay := providererrors.RetryAfter(err); delay != 17*time.Second {
		t.Fatalf("retry delay=%s", delay)
	}
	if providererrors.IsTransient(classifyError(errors.New("invalid input"))) {
		t.Fatal("unknown input error should remain terminal")
	}
	operationErr := &operationError{operation: "op", codes: []string{"QUOTA_EXCEEDED"}, message: "quota exhausted"}
	if !providererrors.IsTransient(classifyError(operationErr)) {
		t.Fatalf("quota operation error not transient: %v", operationErr)
	}
}

func TestIdentityLabelsRequireSameArtifact(t *testing.T) {
	expected := map[string]string{"managed-by": "imagebuilder", "build-id": identityLabel("build-1"), "source-id": identityLabel("source-1")}
	if !matchesIdentityLabels(expected, expected) {
		t.Fatal("matching labels rejected")
	}
	if matchesIdentityLabels(map[string]string{"managed-by": "imagebuilder", "build-id": identityLabel("other")}, expected) {
		t.Fatal("unrelated image accepted")
	}
}

func TestTargetLabelsReserveIdentityKeys(t *testing.T) {
	if err := validateTargetLabels(map[string]string{"managed_by": "other"}); err == nil {
		t.Fatal("reserved managed-by label should be rejected")
	}
	labels := labelsFromMetadata(map[string]string{"target.tag.123-team": "platform"})
	if labels["label-123-team"] != "platform" {
		t.Fatalf("labels=%v", labels)
	}
}

func TestParseGCSImportURL(t *testing.T) {
	value := gcsImportURL("images", "imagebuilder/build + one.tar.gz")
	bucket, object, ok := parseGCSImportURL(value)
	if !ok || bucket != "images" || object != "imagebuilder/build + one.tar.gz" {
		t.Fatalf("parsed bucket=%q object=%q ok=%v from %q", bucket, object, ok, value)
	}
}

func TestSDKConfigFingerprintIsStableAndSensitive(t *testing.T) {
	a := sdkConfigFingerprint(sdk.Config{ProviderConfigName: "gcp", Region: "eu", Extra: map[string]string{"project": "one"}})
	b := sdkConfigFingerprint(sdk.Config{ProviderConfigName: "gcp", Region: "eu", Extra: map[string]string{"project": "one"}})
	c := sdkConfigFingerprint(sdk.Config{ProviderConfigName: "gcp", Region: "eu", Extra: map[string]string{"project": "two"}})
	if a != b || a == c {
		t.Fatalf("fingerprints a=%x b=%x c=%x", a, b, c)
	}
}

func TestSDKProviderUploadAndRegister(t *testing.T) {
	client := &fakeImageClient{}
	provider := NewSDKProvider()
	provider.plugins["gcp-prod"] = initializedPlugin(client)
	upload, err := provider.UploadArtifact(context.Background(), sdk.ArtifactInfo{
		Format:             "gcetarball",
		Checksum:           "sha256:abc",
		TotalSizeBytes:     8,
		OSFamily:           "linux",
		ProviderConfigName: "gcp-prod",
		Metadata:           map[string]string{"buildID": "build-123", "imageName": "Ubuntu"},
	}, io.NopCloser(strings.NewReader("artifact")), nil)
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	ref, err := provider.RegisterImage(context.Background(), sdk.RegisterInput{ProviderRef: upload.ProviderRef, ProviderConfigName: "gcp-prod", Format: "gcetarball", ImageName: "Ubuntu"})
	if err != nil {
		t.Fatalf("RegisterImage: %v", err)
	}
	if ref.Name != "ubuntu" || client.deletedObject != "imagebuilder/build-123-abc.tar.gz" {
		t.Fatalf("ref=%#v deleted object=%q", ref, client.deletedObject)
	}
}

func TestUploadResultRecoversStagingMetadataAfterRestart(t *testing.T) {
	provider := NewSDKProvider()
	result := provider.uploadResult(sdk.RegisterInput{ProviderRef: "https://storage.googleapis.com/images/imagebuilder/build.tar.gz", ProviderConfigName: "gcp-prod"})
	if result.Metadata["bucket"] != "images" || result.Metadata["object"] != "imagebuilder/build.tar.gz" {
		t.Fatalf("metadata=%v", result.Metadata)
	}
}

func TestRegisterRecoversBuildIdentityAfterRestart(t *testing.T) {
	client := &fakeImageClient{}
	p := initializedPlugin(client)
	_, err := p.Register(context.Background(), &platform.UploadResult{ProviderRef: "https://storage.googleapis.com/images/imagebuilder/build-123-abc.tar.gz", Metadata: map[string]string{"bucket": "images", "object": "imagebuilder/build-123-abc.tar.gz", "gcsURI": "https://storage.googleapis.com/images/imagebuilder/build-123-abc.tar.gz", "imageName": "ubuntu"}})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if client.createInput.Labels["build-id"] != identityLabel("build-123") {
		t.Fatalf("labels=%v", client.createInput.Labels)
	}
}
func TestNameSanitizationAndRequestID(t *testing.T) {
	if got := sanitizeName("Ubuntu 24.04_AMD64"); got != "ubuntu-24-04-amd64" {
		t.Fatalf("sanitize=%q", got)
	}
	if a, b := requestID("build"), requestID("build"); a != b || len(a) != 36 {
		t.Fatalf("request IDs %q %q", a, b)
	}
}

func TestCredentialsJSONReconstructsExpandedServiceAccount(t *testing.T) {
	raw := credentialsJSON(map[string][]byte{
		"type":         []byte("service_account"),
		"project_id":   []byte("test-project"),
		"private_key":  []byte("private"),
		"client_email": []byte("provider@test-project.iam.gserviceaccount.com"),
	})
	if len(raw) == 0 || !strings.Contains(string(raw), `"project_id":"test-project"`) {
		t.Fatalf("credentials JSON = %s", raw)
	}
}

func TestCredentialProjectReadsRawServiceAccountJSON(t *testing.T) {
	project := credentialProject(map[string][]byte{"credentials": []byte(`{"type":"service_account","project_id":"raw-project","private_key":"private","client_email":"provider@example.com"}`)})
	if project != "raw-project" {
		t.Fatalf("project=%q", project)
	}
}
