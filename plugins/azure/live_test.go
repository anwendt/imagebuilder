package azure

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/test/e2e/workloads"
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

func TestAzureRemoteBuildTomcat_E2E(t *testing.T) {
	if os.Getenv("AZURE_E2E") != "1" || !strings.EqualFold(os.Getenv("AZURE_E2E_WORKLOAD"), "tomcat") {
		t.Skip("set AZURE_E2E=1 and AZURE_E2E_WORKLOAD=tomcat to run live Azure Tomcat remote build E2E")
	}

	ctx, cancel := context.WithTimeout(context.Background(), azureE2EDuration(t, "AZURE_E2E_TIMEOUT", 75*time.Minute))
	defer cancel()

	cfg := azureE2EConfig("azure-tomcat-e2e-" + strconvTimeSuffix())
	for key, value := range map[string]string{
		"AZURE_E2E_LOCATION":             cfg.Region,
		"AZURE_E2E_RESOURCE_GROUP":       cfg.Extra["resourceGroup"],
		"AZURE_E2E_STORAGE_ACCOUNT":      cfg.Extra["storageAccount"],
		"AZURE_E2E_SOURCE_ID":            os.Getenv("AZURE_E2E_SOURCE_ID"),
		"AZURE_E2E_NETWORK_INTERFACE_ID": cfg.Extra["remote.networkInterfaceId"],
		"AZURE_E2E_SUBSCRIPTION_ID":      string(cfg.SecretData["subscriptionId"]),
		"AZURE_E2E_TENANT_ID":            string(cfg.SecretData["tenantId"]),
		"AZURE_E2E_CLIENT_ID":            string(cfg.SecretData["clientId"]),
		"AZURE_E2E_CLIENT_SECRET":        string(cfg.SecretData["clientSecret"]),
		"AZURE_E2E_STORAGE_ACCOUNT_KEY":  string(cfg.SecretData["storageAccountKey"]),
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required", key)
		}
	}

	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}

	buildID := "azure-tomcat-e2e-" + strconvTimeSuffix()
	req := &platform.RemoteBuildRequest{
		BuildID:           buildID,
		ImageName:         firstNonEmpty(os.Getenv("AZURE_E2E_IMAGE_NAME"), buildID),
		Namespace:         firstNonEmpty(os.Getenv("AZURE_E2E_NAMESPACE"), "imagebuilder-e2e"),
		OSFamily:          platform.OSFamilyLinux,
		OSDistribution:    firstNonEmpty(os.Getenv("AZURE_E2E_OS_DISTRIBUTION"), "ubuntu"),
		OSVersion:         firstNonEmpty(os.Getenv("AZURE_E2E_OS_VERSION"), "24.04"),
		OSArch:            firstNonEmpty(os.Getenv("AZURE_E2E_OS_ARCH"), "amd64"),
		SourceType:        firstNonEmpty(os.Getenv("AZURE_E2E_SOURCE_TYPE"), "managed-disk"),
		SourceProviderRef: os.Getenv("AZURE_E2E_SOURCE_ID"),
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-e2e"},
			Format:            string(platform.FormatVHD),
			Tags:              map[string]string{"imagebuilder-e2e": "true", "imagebuilder-workload": "tomcat"},
		},
		Provisioners: []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: workloads.TomcatTarShellProvisioner()}},
		Timeout:      azureE2EDuration(t, "AZURE_E2E_BUILD_TIMEOUT", 65*time.Minute),
	}

	var completedImageName string
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if err := plugin.CleanupRemoteBuild(cleanupCtx, req); err != nil {
			t.Logf("remote cleanup failed: %v", err)
		}
		if completedImageName != "" {
			if err := plugin.Cleanup(cleanupCtx, &platform.BuildArtifact{Metadata: map[string]string{"imageName": completedImageName}}); err != nil {
				t.Logf("image cleanup failed: %v", err)
			}
		}
	}()

	pollInterval := azureE2EDuration(t, "AZURE_E2E_POLL_INTERVAL", 20*time.Second)
	for {
		result, err := plugin.ReconcileRemoteBuild(ctx, req)
		if err != nil {
			t.Fatalf("ReconcileRemoteBuild: %v", err)
		}
		if result.OperationRef != "" {
			req.OperationRef = result.OperationRef
		}
		t.Logf("phase=%s done=%t ref=%s message=%s", result.Phase, result.Done, result.OperationRef, result.Message)
		if result.Done {
			if len(result.Images) != 1 || result.Images[0].ImageRef.ID == "" {
				t.Fatalf("remote build completed without Azure image ID: %#v", result.Images)
			}
			completedImageName = result.Images[0].ImageRef.Name
			if result.Hygiene == nil || result.Hygiene.Status != "passed" {
				t.Fatalf("remote build completed without passed hygiene result: %#v", result.Hygiene)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for Azure Tomcat remote build: %v", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func TestAzureRemoteBuildUbuntuLatest_E2E(t *testing.T) {
	if os.Getenv("AZURE_E2E") != "1" || !strings.EqualFold(os.Getenv("AZURE_E2E_WORKLOAD"), "ubuntu24") {
		t.Skip("set AZURE_E2E=1 and AZURE_E2E_WORKLOAD=ubuntu24 to run live Azure Ubuntu 24.04 latest marketplace E2E")
	}

	ctx, cancel := context.WithTimeout(context.Background(), azureE2EDuration(t, "AZURE_E2E_TIMEOUT", 75*time.Minute))
	defer cancel()

	cfg := azureE2EConfig("azure-ubuntu24-e2e-" + strconvTimeSuffix())
	for key, value := range map[string]string{
		"AZURE_E2E_LOCATION":             cfg.Region,
		"AZURE_E2E_RESOURCE_GROUP":       cfg.Extra["resourceGroup"],
		"AZURE_E2E_STORAGE_ACCOUNT":      cfg.Extra["storageAccount"],
		"AZURE_E2E_NETWORK_INTERFACE_ID": cfg.Extra["remote.networkInterfaceId"],
		"AZURE_E2E_SUBSCRIPTION_ID":      string(cfg.SecretData["subscriptionId"]),
		"AZURE_E2E_TENANT_ID":            string(cfg.SecretData["tenantId"]),
		"AZURE_E2E_CLIENT_ID":            string(cfg.SecretData["clientId"]),
		"AZURE_E2E_CLIENT_SECRET":        string(cfg.SecretData["clientSecret"]),
		"AZURE_E2E_STORAGE_ACCOUNT_KEY":  string(cfg.SecretData["storageAccountKey"]),
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required", key)
		}
	}

	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}

	buildID := "azure-ubuntu24-e2e-" + strconvTimeSuffix()
	req := &platform.RemoteBuildRequest{
		BuildID:        buildID,
		ImageName:      firstNonEmpty(os.Getenv("AZURE_E2E_UBUNTU24_IMAGE_NAME"), buildID),
		Namespace:      firstNonEmpty(os.Getenv("AZURE_E2E_NAMESPACE"), "imagebuilder-e2e"),
		OSFamily:       platform.OSFamilyLinux,
		OSDistribution: "ubuntu",
		OSVersion:      "24.04",
		OSArch:         firstNonEmpty(os.Getenv("AZURE_E2E_OS_ARCH"), "amd64"),
		SourceType:     "marketplace",
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: firstNonEmpty(os.Getenv("AZURE_E2E_MARKETPLACE_PUBLISHER"), "Canonical"),
			Offer:     firstNonEmpty(os.Getenv("AZURE_E2E_MARKETPLACE_OFFER"), "ubuntu-24_04-lts"),
			SKU:       firstNonEmpty(os.Getenv("AZURE_E2E_MARKETPLACE_SKU"), "server"),
			Version:   firstNonEmpty(os.Getenv("AZURE_E2E_MARKETPLACE_VERSION"), "latest"),
		},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-e2e"},
			Format:            string(platform.FormatVHD),
			Tags:              map[string]string{"imagebuilder-e2e": "true", "imagebuilder-workload": "ubuntu24"},
		},
		Timeout: azureE2EDuration(t, "AZURE_E2E_BUILD_TIMEOUT", 65*time.Minute),
	}

	var completedImageName string
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if err := plugin.CleanupRemoteBuild(cleanupCtx, req); err != nil {
			t.Logf("remote cleanup failed: %v", err)
		}
		if completedImageName != "" {
			if err := plugin.Cleanup(cleanupCtx, &platform.BuildArtifact{Metadata: map[string]string{"imageName": completedImageName}}); err != nil {
				t.Logf("image cleanup failed: %v", err)
			}
		}
	}()

	pollInterval := azureE2EDuration(t, "AZURE_E2E_POLL_INTERVAL", 20*time.Second)
	for {
		result, err := plugin.ReconcileRemoteBuild(ctx, req)
		if err != nil {
			t.Fatalf("ReconcileRemoteBuild: %v", err)
		}
		if result.OperationRef != "" {
			req.OperationRef = result.OperationRef
		}
		t.Logf("phase=%s done=%t ref=%s message=%s", result.Phase, result.Done, result.OperationRef, result.Message)
		if result.Done {
			if len(result.Images) != 1 || result.Images[0].ImageRef.ID == "" {
				t.Fatalf("remote build completed without Azure image ID: %#v", result.Images)
			}
			completedImageName = result.Images[0].ImageRef.Name
			if result.Hygiene == nil || result.Hygiene.Status != "passed" {
				t.Fatalf("remote build completed without passed hygiene result: %#v", result.Hygiene)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for Azure Ubuntu 24.04 latest remote build: %v", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func azureE2EConfig(imageName string) platform.PluginConfig {
	return platform.PluginConfig{
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
			"resourceGroup":             os.Getenv("AZURE_E2E_RESOURCE_GROUP"),
			"storageAccount":            os.Getenv("AZURE_E2E_STORAGE_ACCOUNT"),
			"storageContainer":          firstNonEmpty(os.Getenv("AZURE_E2E_STORAGE_CONTAINER"), "imagebuilder-e2e"),
			"blobPrefix":                "e2e",
			"imageName":                 firstNonEmpty(os.Getenv("AZURE_E2E_IMAGE_NAME"), imageName),
			"remote.vmSize":             firstNonEmpty(os.Getenv("AZURE_E2E_VM_SIZE"), "Standard_B2s"),
			"remote.networkInterfaceId": os.Getenv("AZURE_E2E_NETWORK_INTERFACE_ID"),
			"diskSizeGiB":               os.Getenv("AZURE_E2E_DISK_SIZE_GIB"),
			"hyperVGeneration":          os.Getenv("AZURE_E2E_HYPERV_GENERATION"),
			"osState":                   os.Getenv("AZURE_E2E_OS_STATE"),
			"storageAccountType":        os.Getenv("AZURE_E2E_STORAGE_ACCOUNT_TYPE"),
		},
	}
}

func azureE2EDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		t.Fatalf("%s must be a Go duration, got %q: %v", key, value, err)
	}
	if duration <= 0 {
		t.Fatalf("%s must be positive", key)
	}
	return duration
}

func strconvTimeSuffix() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}
