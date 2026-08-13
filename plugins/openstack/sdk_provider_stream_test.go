package openstack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

func TestSDKProviderStreamsDirectlyToGlance(t *testing.T) {
	payload := []byte("glance-direct-stream")
	client := &fakeOpenStackClient{}
	plugin := &Plugin{config: openStackConfig{providerConfigName: "openstack-prod", region: "RegionOne", extraConfig: map[string]string{}}, client: client}
	provider := NewSDKProvider()
	provider.plugins["openstack-prod"] = plugin

	result, err := provider.UploadArtifact(context.Background(), sdk.ArtifactInfo{
		Format: "qcow2", Checksum: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), TotalSizeBytes: int64(len(payload)),
		OSFamily: "linux", ProviderConfigName: "openstack-prod", Metadata: map[string]string{"buildID": "build-123"},
	}, bytes.NewReader(payload), nil)
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	if !bytes.Equal(client.uploadedBody, payload) {
		t.Fatalf("uploaded=%q", client.uploadedBody)
	}
	if result.ProviderRef != "img-uploaded" {
		t.Fatalf("provider ref=%q", result.ProviderRef)
	}
}
