package vsphere

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/test/e2e/workloads"
)

func TestVSphereProviderLive_E2E(t *testing.T) {
	if os.Getenv("VSPHERE_E2E") != "1" {
		t.Skip("set VSPHERE_E2E=1 to run live vSphere provider E2E")
	}
	artifactPath := os.Getenv("VSPHERE_E2E_ARTIFACT_PATH")
	if artifactPath == "" {
		t.Fatal("VSPHERE_E2E_ARTIFACT_PATH is required and must point to a VMDK, OVA, or OVF artifact")
	}
	format := platform.ImageFormat(firstNonEmpty(os.Getenv("VSPHERE_E2E_FORMAT"), formatFromPath(artifactPath)))
	if !isSupportedFormat(format) {
		t.Fatalf("VSPHERE_E2E_FORMAT=%q is unsupported; use vmdk, ova, or ovf", format)
	}
	timeout := durationFromEnv(t, "VSPHERE_E2E_TIMEOUT", 55*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cfg := platform.PluginConfig{
		ProviderConfigName: "vsphere-e2e",
		Endpoint:           os.Getenv("VSPHERE_E2E_ENDPOINT"),
		Insecure:           boolFromEnv("VSPHERE_E2E_INSECURE", true),
		SecretData: map[string][]byte{
			"username": []byte(os.Getenv("VSPHERE_E2E_USERNAME")),
			"password": []byte(os.Getenv("VSPHERE_E2E_PASSWORD")),
		},
		Extra: liveExtraConfig(artifactPath),
	}
	requireVSphereE2EEnv(t, cfg)
	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := plugin.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	buildID := "vsphere-e2e-" + strconv.FormatInt(time.Now().Unix(), 10)
	artifact := &platform.BuildArtifact{
		Path:      artifactPath,
		Format:    format,
		Checksum:  os.Getenv("VSPHERE_E2E_CHECKSUM"),
		SizeBytes: info.Size(),
		OS:        platform.OSFamily(firstNonEmpty(os.Getenv("VSPHERE_E2E_OS_FAMILY"), string(platform.OSFamilyLinux))),
		Metadata: map[string]string{
			"buildID":   buildID,
			"imageName": firstNonEmpty(os.Getenv("VSPHERE_E2E_IMAGE_NAME"), buildID),
		},
	}
	upload, err := plugin.Upload(ctx, artifact)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	defer func() {
		_ = plugin.Cleanup(context.Background(), &platform.BuildArtifact{Path: artifactPath, Format: format, Metadata: upload.Metadata})
	}()
	ref, err := plugin.Register(ctx, upload)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if ref.ID == "" {
		t.Fatal("Register returned empty image ID")
	}
}

func TestVSphereRemoteBuildTomcat_E2E(t *testing.T) {
	if os.Getenv("VSPHERE_E2E") != "1" || !strings.EqualFold(os.Getenv("VSPHERE_E2E_WORKLOAD"), "tomcat") {
		t.Skip("set VSPHERE_E2E=1 and VSPHERE_E2E_WORKLOAD=tomcat to run live vSphere Tomcat remote build E2E")
	}

	timeout := durationFromEnv(t, "VSPHERE_E2E_TIMEOUT", 75*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	buildID := "vsphere-tomcat-e2e-" + strconv.FormatInt(time.Now().Unix(), 10)
	cfg := platform.PluginConfig{
		ProviderConfigName: "vsphere-e2e",
		Endpoint:           os.Getenv("VSPHERE_E2E_ENDPOINT"),
		Insecure:           boolFromEnv("VSPHERE_E2E_INSECURE", true),
		SecretData: map[string][]byte{
			"username":      []byte(os.Getenv("VSPHERE_E2E_USERNAME")),
			"password":      []byte(os.Getenv("VSPHERE_E2E_PASSWORD")),
			"guestUsername": []byte(os.Getenv("VSPHERE_E2E_GUEST_USERNAME")),
			"guestPassword": []byte(os.Getenv("VSPHERE_E2E_GUEST_PASSWORD")),
		},
		Extra: liveExtraConfig(firstNonEmpty(os.Getenv("VSPHERE_E2E_SOURCE_VM"), buildID)),
	}
	cfg.Extra["imageName"] = firstNonEmpty(os.Getenv("VSPHERE_E2E_IMAGE_NAME"), buildID)
	requireVSphereE2EEnv(t, cfg)
	for key, value := range map[string]string{
		"VSPHERE_E2E_SOURCE_VM":      os.Getenv("VSPHERE_E2E_SOURCE_VM"),
		"VSPHERE_E2E_GUEST_USERNAME": string(cfg.SecretData["guestUsername"]),
		"VSPHERE_E2E_GUEST_PASSWORD": string(cfg.SecretData["guestPassword"]),
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required", key)
		}
	}

	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := plugin.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	req := &platform.RemoteBuildRequest{
		BuildID:           buildID,
		ImageName:         cfg.Extra["imageName"],
		Namespace:         firstNonEmpty(os.Getenv("VSPHERE_E2E_NAMESPACE"), "imagebuilder-e2e"),
		OSFamily:          platform.OSFamily(firstNonEmpty(os.Getenv("VSPHERE_E2E_OS_FAMILY"), string(platform.OSFamilyLinux))),
		OSDistribution:    firstNonEmpty(os.Getenv("VSPHERE_E2E_OS_DISTRIBUTION"), "ubuntu"),
		OSVersion:         firstNonEmpty(os.Getenv("VSPHERE_E2E_OS_VERSION"), "24.04"),
		OSArch:            firstNonEmpty(os.Getenv("VSPHERE_E2E_OS_ARCH"), "amd64"),
		SourceType:        firstNonEmpty(os.Getenv("VSPHERE_E2E_SOURCE_TYPE"), "template"),
		SourceProviderRef: os.Getenv("VSPHERE_E2E_SOURCE_VM"),
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "vsphere-e2e"},
			Format:            firstNonEmpty(os.Getenv("VSPHERE_E2E_FORMAT"), string(platform.FormatVMDK)),
			Tags:              map[string]string{"imagebuilder.io/e2e": "true", "imagebuilder.io/workload": "tomcat"},
		},
		Provisioners: []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: workloads.TomcatTarShellProvisioner()}},
		Timeout:      durationFromEnv(t, "VSPHERE_E2E_BUILD_TIMEOUT", 65*time.Minute),
	}

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if err := plugin.CleanupRemoteBuild(cleanupCtx, req); err != nil {
			t.Logf("remote cleanup failed: %v", err)
		}
	}()

	pollInterval := durationFromEnv(t, "VSPHERE_E2E_POLL_INTERVAL", 20*time.Second)
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
				t.Fatalf("remote build completed without vSphere image ID: %#v", result.Images)
			}
			if result.Hygiene == nil || result.Hygiene.Status != "passed" {
				t.Fatalf("remote build completed without passed hygiene result: %#v", result.Hygiene)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for vSphere Tomcat remote build: %v", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func TestVSphereRemoteBuildUbuntuLatest_E2E(t *testing.T) {
	if os.Getenv("VSPHERE_E2E") != "1" || !strings.EqualFold(os.Getenv("VSPHERE_E2E_WORKLOAD"), "ubuntu24") {
		t.Skip("set VSPHERE_E2E=1 and VSPHERE_E2E_WORKLOAD=ubuntu24 to run live vSphere Ubuntu 24.04 latest marketplace E2E")
	}

	timeout := durationFromEnv(t, "VSPHERE_E2E_TIMEOUT", 75*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	buildID := "vsphere-ubuntu24-e2e-" + strconv.FormatInt(time.Now().Unix(), 10)
	cfg := platform.PluginConfig{
		ProviderConfigName: "vsphere-e2e",
		Endpoint:           os.Getenv("VSPHERE_E2E_ENDPOINT"),
		Insecure:           boolFromEnv("VSPHERE_E2E_INSECURE", true),
		SecretData: map[string][]byte{
			"username": []byte(os.Getenv("VSPHERE_E2E_USERNAME")),
			"password": []byte(os.Getenv("VSPHERE_E2E_PASSWORD")),
		},
		Extra: liveExtraConfig(buildID),
	}
	cfg.Extra["imageName"] = firstNonEmpty(os.Getenv("VSPHERE_E2E_UBUNTU24_IMAGE_NAME"), buildID)
	if source := strings.TrimSpace(os.Getenv("VSPHERE_E2E_MARKETPLACE_SOURCE")); source != "" {
		cfg.Extra["marketplace.canonical.ubuntu.24.04.latest"] = source
	}
	requireVSphereE2EEnv(t, cfg)
	if strings.TrimSpace(cfg.Extra["marketplace.canonical.ubuntu.24.04.latest"]) == "" {
		t.Fatal("VSPHERE_E2E_MARKETPLACE_SOURCE is required and must reference a template, VM, content-library:/Library/Item, or library-item:<id>")
	}

	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := plugin.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	req := &platform.RemoteBuildRequest{
		BuildID:        buildID,
		ImageName:      cfg.Extra["imageName"],
		Namespace:      firstNonEmpty(os.Getenv("VSPHERE_E2E_NAMESPACE"), "imagebuilder-e2e"),
		OSFamily:       platform.OSFamilyLinux,
		OSDistribution: "ubuntu",
		OSVersion:      "24.04",
		OSArch:         firstNonEmpty(os.Getenv("VSPHERE_E2E_OS_ARCH"), "amd64"),
		SourceType:     "marketplace",
		SourceMarketplace: &v1alpha1.MarketplaceRef{
			Publisher: firstNonEmpty(os.Getenv("VSPHERE_E2E_MARKETPLACE_PUBLISHER"), "Canonical"),
			Offer:     firstNonEmpty(os.Getenv("VSPHERE_E2E_MARKETPLACE_OFFER"), "ubuntu"),
			SKU:       firstNonEmpty(os.Getenv("VSPHERE_E2E_MARKETPLACE_SKU"), "24.04"),
			Version:   firstNonEmpty(os.Getenv("VSPHERE_E2E_MARKETPLACE_VERSION"), "latest"),
		},
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "vsphere-e2e"},
			Format:            firstNonEmpty(os.Getenv("VSPHERE_E2E_FORMAT"), string(platform.FormatVMDK)),
			Tags:              map[string]string{"imagebuilder.io/e2e": "true", "imagebuilder.io/workload": "ubuntu24"},
		},
		Timeout: durationFromEnv(t, "VSPHERE_E2E_BUILD_TIMEOUT", 65*time.Minute),
	}

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if err := plugin.CleanupRemoteBuild(cleanupCtx, req); err != nil {
			t.Logf("remote cleanup failed: %v", err)
		}
	}()

	pollInterval := durationFromEnv(t, "VSPHERE_E2E_POLL_INTERVAL", 20*time.Second)
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
				t.Fatalf("remote build completed without vSphere image ID: %#v", result.Images)
			}
			if result.Hygiene == nil || result.Hygiene.Status != "passed" {
				t.Fatalf("remote build completed without passed hygiene result: %#v", result.Hygiene)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for vSphere Ubuntu 24.04 latest remote build: %v", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func liveExtraConfig(artifactPath string) map[string]string {
	extra := map[string]string{
		"datacenter":       os.Getenv("VSPHERE_E2E_DATACENTER"),
		"datastore":        os.Getenv("VSPHERE_E2E_DATASTORE"),
		"uploadPathPrefix": firstNonEmpty(os.Getenv("VSPHERE_E2E_UPLOAD_PATH_PREFIX"), "imagebuilder-e2e"),
		"imageName":        firstNonEmpty(os.Getenv("VSPHERE_E2E_IMAGE_NAME"), "vsphere-e2e-"+strconv.FormatInt(time.Now().Unix(), 10)),
	}
	for key, env := range map[string]string{
		"folder":             "VSPHERE_E2E_FOLDER",
		"cluster":            "VSPHERE_E2E_CLUSTER",
		"resourcePool":       "VSPHERE_E2E_RESOURCE_POOL",
		"host":               "VSPHERE_E2E_HOST",
		"network":            "VSPHERE_E2E_NETWORK",
		"ovfNetworkName":     "VSPHERE_E2E_OVF_NETWORK_NAME",
		"diskProvisioning":   "VSPHERE_E2E_DISK_PROVISIONING",
		"contentLibrary":     "VSPHERE_E2E_CONTENT_LIBRARY",
		"contentLibraryID":   "VSPHERE_E2E_CONTENT_LIBRARY_ID",
		"deployment":         "VSPHERE_E2E_DEPLOYMENT",
		"ipAllocationPolicy": "VSPHERE_E2E_IP_ALLOCATION_POLICY",
		"ipProtocol":         "VSPHERE_E2E_IP_PROTOCOL",
		"annotation":         "VSPHERE_E2E_ANNOTATION",
	} {
		if value := os.Getenv(env); value != "" {
			extra[key] = value
		}
	}
	if value := os.Getenv("VSPHERE_E2E_MARK_AS_TEMPLATE"); value != "" {
		extra["markAsTemplate"] = value
	}
	if value := os.Getenv("VSPHERE_E2E_REQUIRE_MANIFEST"); value != "" {
		extra["requireManifest"] = value
	}
	if extra["imageName"] == "" {
		extra["imageName"] = strings.TrimSuffix(filepath.Base(artifactPath), filepath.Ext(artifactPath))
	}
	return extra
}

func formatFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ova":
		return string(platform.FormatOVA)
	case ".ovf":
		return string(platform.FormatOVF)
	default:
		return string(platform.FormatVMDK)
	}
}

func boolFromEnv(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationFromEnv(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		t.Fatalf("%s must be a Go duration, for example 55m: %v", key, err)
	}
	if parsed <= 0 {
		t.Fatalf("%s must be positive", key)
	}
	if parsed > 4*time.Hour {
		t.Fatalf("%s=%s is too large for this live test", key, parsed)
	}
	return parsed
}

func requireVSphereE2EEnv(t *testing.T, cfg platform.PluginConfig) {
	t.Helper()
	required := map[string]string{
		"VSPHERE_E2E_ENDPOINT":   cfg.Endpoint,
		"VSPHERE_E2E_USERNAME":   string(cfg.SecretData["username"]),
		"VSPHERE_E2E_PASSWORD":   string(cfg.SecretData["password"]),
		"VSPHERE_E2E_DATACENTER": cfg.Extra["datacenter"],
		"VSPHERE_E2E_DATASTORE":  cfg.Extra["datastore"],
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required", key)
		}
	}
}
