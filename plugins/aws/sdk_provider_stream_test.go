package aws

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

func TestSDKProviderStreamsDirectlyToS3(t *testing.T) {
	payload := []byte("aws-direct-stream")
	client := &fakeAWSLocalImageClient{}
	plugin := &AWSPlugin{config: awsConfig{providerConfigName: "aws-prod", region: "eu-central-1", extraConfig: map[string]string{"s3Bucket": "images"}}, localClient: client}
	provider := NewSDKProvider()
	provider.plugins["aws-prod"] = plugin

	result, err := provider.UploadArtifact(context.Background(), sdk.ArtifactInfo{
		Format: "vmdk", Checksum: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), TotalSizeBytes: int64(len(payload)),
		OSFamily: "linux", ProviderConfigName: "aws-prod", Metadata: map[string]string{"buildID": "build-123"},
	}, bytes.NewReader(payload), nil)
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	if !bytes.Equal(client.uploadedBody, payload) || client.uploadedSize != int64(len(payload)) {
		t.Fatalf("uploaded=%q size=%d", client.uploadedBody, client.uploadedSize)
	}
	if result.ProviderRef == "" {
		t.Fatal("provider ref is empty")
	}
}
