package aws

import (
	"context"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

type fakeAWSRemoteBuildClient struct {
	got awsRemoteBuildRequest
	out *awsRemoteBuildState
	err error
}

func (f *fakeAWSRemoteBuildClient) ReconcileRemoteBuild(ctx context.Context, req awsRemoteBuildRequest) (*awsRemoteBuildState, error) {
	f.got = req
	return f.out, f.err
}

func (f *fakeAWSRemoteBuildClient) CleanupRemoteBuild(ctx context.Context, req awsRemoteBuildRequest) error {
	f.got = req
	return f.err
}

func initializedRemoteAWSPlugin(t *testing.T, client awsRemoteBuildClient) *AWSPlugin {
	t.Helper()

	p := &AWSPlugin{}
	err := p.Init(context.Background(), platform.PluginConfig{
		ProviderConfigName: "aws-prod",
		Region:             "eu-central-1",
		SecretData: map[string][]byte{
			"accessKeyId":     []byte("AKIAIOSFODNN7EXAMPLE"),
			"secretAccessKey": []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
		},
		Extra: map[string]string{"s3Bucket": "imagebuilder-artifacts"},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	p.remoteClient = client
	return p
}

func TestAWSPlugin_ReconcileRemoteBuild_RequiresAMIFormat(t *testing.T) {
	p := initializedRemoteAWSPlugin(t, &fakeAWSRemoteBuildClient{})

	_, err := p.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:    "build-123",
		ImageName:  "ubuntu-remote",
		SourceType: "cloud-image",
		SourceURL:  "https://example.invalid/ubuntu.img",
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-prod"},
			Format:            string(platform.FormatRaw),
		},
	})
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject non-AMI remote targets")
	}
}

func TestAWSPlugin_ReconcileRemoteBuild_ReturnsInProgressState(t *testing.T) {
	client := &fakeAWSRemoteBuildClient{
		out: &awsRemoteBuildState{
			OperationRef: "aws:imagebuilder/build-123",
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      "provisioning instance",
		},
	}
	p := initializedRemoteAWSPlugin(t, client)

	result, err := p.ReconcileRemoteBuild(context.Background(), remoteBuildRequest())
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if result.OperationRef != "aws:imagebuilder/build-123" {
		t.Errorf("OperationRef = %q", result.OperationRef)
	}
	if result.Phase != platform.RemoteBuildPhaseProvisioning {
		t.Errorf("Phase = %q", result.Phase)
	}
	if result.Done {
		t.Error("Done should be false for in-progress state")
	}
	if client.got.Region != "eu-central-1" {
		t.Errorf("client Region = %q", client.got.Region)
	}
	if client.got.Timeout != 30*time.Minute {
		t.Errorf("client Timeout = %s", client.got.Timeout)
	}
}

func TestAWSPlugin_ReconcileRemoteBuild_ReturnsAMIWhenDone(t *testing.T) {
	client := &fakeAWSRemoteBuildClient{
		out: &awsRemoteBuildState{
			OperationRef: "aws:imagebuilder/build-123",
			Phase:        platform.RemoteBuildPhaseRegistering,
			Message:      "ami available",
			Done:         true,
			AMIID:        "ami-0123456789abcdef0",
			ImageName:    "ubuntu-remote-final",
			Checksum:     "sha256:0123456789abcdef",
			Hygiene: &platform.RemoteHygieneResult{
				Status:  "passed",
				Message: "AWS provider attested remote build controls and final AMI availability",
				Checks:  []string{"aws-ami-available"},
			},
		},
	}
	p := initializedRemoteAWSPlugin(t, client)

	result, err := p.ReconcileRemoteBuild(context.Background(), remoteBuildRequest())
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if !result.Done {
		t.Fatal("Done should be true")
	}
	if result.Phase != platform.RemoteBuildPhaseReady {
		t.Errorf("Phase = %q", result.Phase)
	}
	if len(result.Images) != 1 {
		t.Fatalf("Images len = %d, want 1", len(result.Images))
	}
	image := result.Images[0]
	if image.Provider != "aws" {
		t.Errorf("Provider = %q", image.Provider)
	}
	if image.ProviderConfig != "aws-prod" {
		t.Errorf("ProviderConfig = %q", image.ProviderConfig)
	}
	if image.Format != platform.FormatAMI {
		t.Errorf("Format = %q", image.Format)
	}
	if image.ImageRef.ID != "ami-0123456789abcdef0" {
		t.Errorf("ImageRef.ID = %q", image.ImageRef.ID)
	}
	if image.ImageRef.Location != "eu-central-1" {
		t.Errorf("ImageRef.Location = %q", image.ImageRef.Location)
	}
	if image.ImageRef.Tags["env"] != "test" {
		t.Errorf("ImageRef.Tags[env] = %q", image.ImageRef.Tags["env"])
	}
	if result.Hygiene == nil || result.Hygiene.Status != "passed" {
		t.Fatalf("Hygiene = %#v, want passed attestation", result.Hygiene)
	}
	if len(result.Hygiene.Checks) != 1 || result.Hygiene.Checks[0] != "aws-ami-available" {
		t.Fatalf("Hygiene checks = %#v, want aws-ami-available", result.Hygiene.Checks)
	}
}

func TestAWSPlugin_ReconcileRemoteBuild_DoneWithoutHygieneFallsBackToUnknown(t *testing.T) {
	client := &fakeAWSRemoteBuildClient{
		out: &awsRemoteBuildState{
			OperationRef: "aws:imagebuilder/build-123",
			Phase:        platform.RemoteBuildPhaseReady,
			Done:         true,
			AMIID:        "ami-0123456789abcdef0",
			ImageName:    "ubuntu-remote-final",
		},
	}
	p := initializedRemoteAWSPlugin(t, client)

	result, err := p.ReconcileRemoteBuild(context.Background(), remoteBuildRequest())
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if result.Hygiene == nil || result.Hygiene.Status != "unknown" {
		t.Fatalf("Hygiene = %#v, want unknown fallback", result.Hygiene)
	}
	if len(result.Hygiene.Checks) != 1 || result.Hygiene.Checks[0] != "provider-attestation-missing" {
		t.Fatalf("Hygiene checks = %#v, want provider-attestation-missing", result.Hygiene.Checks)
	}
}

func TestAWSPlugin_ReconcileRemoteBuild_DoneRequiresAMIID(t *testing.T) {
	client := &fakeAWSRemoteBuildClient{
		out: &awsRemoteBuildState{
			OperationRef: "aws:imagebuilder/build-123",
			Phase:        platform.RemoteBuildPhaseReady,
			Done:         true,
		},
	}
	p := initializedRemoteAWSPlugin(t, client)

	_, err := p.ReconcileRemoteBuild(context.Background(), remoteBuildRequest())
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject completed states without AMI ID")
	}
}

func remoteBuildRequest() *platform.RemoteBuildRequest {
	return &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-remote",
		Namespace:         "imagebuilder-system",
		OSFamily:          platform.OSFamilyLinux,
		OSDistribution:    "ubuntu",
		OSVersion:         "24.04",
		OSArch:            "amd64",
		SourceType:        "cloud-image",
		SourceProviderRef: "ami-0123456789abcdef0",
		SourceChecksum:    "sha256:0123456789abcdef",
		Timeout:           30 * time.Minute,
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-prod"},
			Format:            string(platform.FormatAMI),
			Tags:              map[string]string{"env": "test"},
		},
	}
}
