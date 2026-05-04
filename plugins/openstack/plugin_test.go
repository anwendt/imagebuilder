package openstack

import (
	"context"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

type fakeOpenStackClient struct {
	uploadInput openStackUploadInput
	remoteInput openStackRemoteBuildInput
	deleted     []string
	image       *platform.ImageRef
	remoteState *openStackRemoteBuildState
}

func (f *fakeOpenStackClient) UploadImage(_ context.Context, input openStackUploadInput) (*platform.ImageRef, error) {
	f.uploadInput = input
	return &platform.ImageRef{ID: "img-uploaded", Name: input.ImageName, Location: "RegionOne"}, nil
}

func (f *fakeOpenStackClient) GetImage(_ context.Context, id string) (*platform.ImageRef, error) {
	if f.image != nil {
		return f.image, nil
	}
	return &platform.ImageRef{ID: id, Name: "registered", Location: "RegionOne"}, nil
}

func (f *fakeOpenStackClient) DeleteImage(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeOpenStackClient) ReconcileRemoteBuild(_ context.Context, input openStackRemoteBuildInput) (*openStackRemoteBuildState, error) {
	f.remoteInput = input
	if f.remoteState != nil {
		return f.remoteState, nil
	}
	return &openStackRemoteBuildState{
		OperationRef: "openstack://remote-build/build-123?serverId=srv-1",
		Phase:        platform.RemoteBuildPhaseBooting,
		Message:      "started",
	}, nil
}

func (f *fakeOpenStackClient) CleanupRemoteBuild(context.Context, openStackRemoteBuildInput) error {
	return nil
}

func (f *fakeOpenStackClient) HealthCheck(context.Context) error {
	return nil
}

func TestPluginSupportedBuildModesIncludesRemote(t *testing.T) {
	modes := (&Plugin{}).SupportedBuildModes()
	if len(modes) != 2 || modes[0] != v1alpha1.BuildModeLocal || modes[1] != v1alpha1.BuildModeRemote {
		t.Fatalf("SupportedBuildModes() = %v, want local and remote", modes)
	}
}

func TestPluginUploadCreatesGlanceImage(t *testing.T) {
	client := &fakeOpenStackClient{}
	plugin := &Plugin{
		config: openStackConfig{
			providerConfigName: "openstack-prod",
			region:             "RegionOne",
			extraConfig: map[string]string{
				"image.visibility": "shared",
				"image.protected":  "true",
			},
		},
		client: client,
	}

	result, err := plugin.Upload(context.Background(), &platform.BuildArtifact{
		Path:      "/tmp/image.qcow2",
		Format:    platform.FormatQCOW2,
		Checksum:  "sha256:abc",
		SizeBytes: 1024,
		OS:        platform.OSFamilyLinux,
		Metadata: map[string]string{
			"buildID":   "build-123",
			"imageName": "ubuntu-openstack",
			"osArch":    "amd64",
		},
	})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.ProviderRef != "img-uploaded" {
		t.Fatalf("ProviderRef = %q, want img-uploaded", result.ProviderRef)
	}
	if client.uploadInput.ImageName != "ubuntu-openstack" ||
		client.uploadInput.DiskFormat != "qcow2" ||
		client.uploadInput.Visibility != "shared" ||
		!client.uploadInput.Protected {
		t.Fatalf("upload input = %#v", client.uploadInput)
	}
}

func TestPluginReconcileRemoteBuildReturnsImage(t *testing.T) {
	client := &fakeOpenStackClient{
		remoteState: &openStackRemoteBuildState{
			OperationRef: "openstack://remote-build/build-123?imageId=img-final",
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "ready",
			Done:         true,
			Image:        &platform.ImageRef{ID: "img-final", Name: "ubuntu-final", Location: "RegionOne"},
			Hygiene:      &platform.RemoteHygieneResult{Status: "passed", Checks: []string{"openstack-server-snapshot"}},
		},
	}
	plugin := &Plugin{
		config: openStackConfig{providerConfigName: "openstack-prod"},
		client: client,
	}

	result, err := plugin.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		ImageName:         "ubuntu-final",
		SourceType:        "cloud-image",
		SourceProviderRef: "img-source",
		OSFamily:          platform.OSFamilyLinux,
		Target:            v1alpha1.TargetSpec{Format: string(platform.FormatQCOW2)},
		Provisioners:      []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if !result.Done || len(result.Images) != 1 || result.Images[0].ImageRef.ID != "img-final" {
		t.Fatalf("remote result = %#v", result)
	}
	if client.remoteInput.SourceRef != "img-source" || client.remoteInput.ProviderConfigName != "openstack-prod" {
		t.Fatalf("remote input = %#v", client.remoteInput)
	}
}

func TestPluginReconcileRemoteBuildRequiresProviderRef(t *testing.T) {
	plugin := &Plugin{client: &fakeOpenStackClient{}}
	_, err := plugin.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID: "build-123",
		Target:  v1alpha1.TargetSpec{Format: string(platform.FormatQCOW2)},
	})
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should require source providerRef")
	}
}
