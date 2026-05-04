package azure

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestAzureProviderLive_E2E(t *testing.T) {
	if os.Getenv("AZURE_E2E") != "1" {
		t.Skip("set AZURE_E2E=1 to run live Azure provider E2E")
	}
	vhdPath := os.Getenv("AZURE_E2E_VHD_PATH")
	if vhdPath == "" {
		t.Fatal("AZURE_E2E_VHD_PATH is required and must point to a fixed VHD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	cfg := platform.PluginConfig{
		ProviderConfigName: "azure-e2e",
		Region:             os.Getenv("AZURE_E2E_LOCATION"),
		SecretData: map[string][]byte{
			"subscriptionId":    []byte(os.Getenv("AZURE_E2E_SUBSCRIPTION_ID")),
			"tenantId":          []byte(os.Getenv("AZURE_E2E_TENANT_ID")),
			"clientId":          []byte(os.Getenv("AZURE_E2E_CLIENT_ID")),
			"clientSecret":      []byte(os.Getenv("AZURE_E2E_CLIENT_SECRET")),
			"storageAccountKey": []byte(os.Getenv("AZURE_E2E_STORAGE_ACCOUNT_KEY")),
		},
		Extra: map[string]string{
			"resourceGroup":    os.Getenv("AZURE_E2E_RESOURCE_GROUP"),
			"storageAccount":   os.Getenv("AZURE_E2E_STORAGE_ACCOUNT"),
			"storageContainer": firstNonEmpty(os.Getenv("AZURE_E2E_STORAGE_CONTAINER"), "imagebuilder-e2e"),
			"blobPrefix":       "e2e",
			"imageName":        firstNonEmpty(os.Getenv("AZURE_E2E_IMAGE_NAME"), "imagebuilder-e2e-"+strconvTimeSuffix()),
		},
	}
	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	artifact := &platform.BuildArtifact{
		Path:     vhdPath,
		Format:   platform.FormatVHD,
		OS:       platform.OSFamilyLinux,
		Metadata: map[string]string{"buildID": "azure-e2e-" + strconvTimeSuffix()},
	}
	upload, err := plugin.Upload(ctx, artifact)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	defer func() {
		_ = plugin.Cleanup(context.Background(), &platform.BuildArtifact{Metadata: upload.Metadata})
	}()
	ref, err := plugin.Register(ctx, upload)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if ref.ID == "" {
		t.Fatal("Register returned empty image ID")
	}
}

func strconvTimeSuffix() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}
