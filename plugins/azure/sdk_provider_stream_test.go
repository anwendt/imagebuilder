package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

func TestSDKProviderStreamsVHDDirectlyToPageBlob(t *testing.T) {
	payload := fixedVHDStreamPayload()
	client := &fakeClient{}
	plugin := &Plugin{config: config{providerConfigName: "azure-prod", resourceGroup: "rg", location: "westeurope", storageContainer: "vhds", pageUploadChunk: 4 * 1024 * 1024}, client: client}
	provider := NewSDKProvider()
	provider.plugins["azure-prod"] = plugin

	result, err := provider.UploadArtifact(context.Background(), sdk.ArtifactInfo{
		Format: "vhd", Checksum: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), TotalSizeBytes: int64(len(payload)),
		OSFamily: "linux", ProviderConfigName: "azure-prod", Metadata: map[string]string{"buildID": "build-123", "imageName": "ubuntu"},
	}, bytes.NewReader(payload), nil)
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	if !bytes.Equal(client.uploadedBody, payload) || client.uploadedSize != int64(len(payload)) {
		t.Fatalf("uploaded size=%d", client.uploadedSize)
	}
	if result.ProviderRef == "" {
		t.Fatal("provider ref is empty")
	}
}

func fixedVHDStreamPayload() []byte {
	payload := make([]byte, azurePageSize*2)
	footer := payload[len(payload)-vhdFooterSize:]
	copy(footer[0:8], vhdCookie)
	binary.BigEndian.PutUint64(footer[48:56], uint64(len(payload)-vhdFooterSize))
	binary.BigEndian.PutUint32(footer[60:64], vhdDiskTypeFixed)
	var sum uint32
	for index, value := range footer {
		if index < 64 || index >= 68 {
			sum += uint32(value)
		}
	}
	binary.BigEndian.PutUint32(footer[64:68], ^sum)
	return payload
}
