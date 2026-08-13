package vsphere

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

func TestSDKProviderStreamsVMDKDirectlyToDatastore(t *testing.T) {
	payload := []byte("vmdk-direct-stream")
	client := &fakeClient{}
	plugin := &Plugin{config: config{providerConfigName: "vsphere-prod", datacenter: "dc01", datastore: "datastore1", uploadPathPrefix: "imagebuilder", extraConfig: map[string]string{}}, client: client}
	provider := NewSDKProvider()
	provider.plugins["vsphere-prod"] = plugin

	result, err := provider.UploadArtifact(context.Background(), sdk.ArtifactInfo{
		Format: "vmdk", Checksum: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), TotalSizeBytes: int64(len(payload)),
		OSFamily: "linux", ProviderConfigName: "vsphere-prod", Metadata: map[string]string{"buildID": "build-123", "imageName": "ubuntu"},
	}, bytes.NewReader(payload), nil)
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	if !bytes.Equal(client.uploadedBody, payload) {
		t.Fatalf("uploaded=%q", client.uploadedBody)
	}
	if result.ProviderRef == "" || result.Metadata["artifactPath"] != "" {
		t.Fatalf("result=%#v", result)
	}
}
