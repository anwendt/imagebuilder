package openstack

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/test/e2e/workloads"
)

func TestOpenStackRemoteBuild_E2E(t *testing.T) {
	if os.Getenv("OPENSTACK_E2E") != "1" {
		t.Skip("set OPENSTACK_E2E=1 to run the real OpenStack remote build E2E test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), openStackE2EDuration(t, "OPENSTACK_E2E_TIMEOUT", 55*time.Minute))
	defer cancel()

	cfg := openStackE2EConfig()
	requireOpenStackE2EEnv(t, cfg)
	plugin := &Plugin{}
	if err := plugin.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := plugin.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	buildID := "openstack-e2e-" + time.Now().UTC().Format("20060102-150405")
	req := &platform.RemoteBuildRequest{
		BuildID:           buildID,
		ImageName:         openStackE2EDefault("OPENSTACK_E2E_IMAGE_NAME", buildID),
		Namespace:         openStackE2EDefault("OPENSTACK_E2E_NAMESPACE", "imagebuilder-e2e"),
		OSFamily:          openStackE2EOSFamily(),
		OSDistribution:    openStackE2EDefault("OPENSTACK_E2E_OS_DISTRIBUTION", "ubuntu"),
		OSVersion:         openStackE2EDefault("OPENSTACK_E2E_OS_VERSION", "24.04"),
		OSArch:            openStackE2EDefault("OPENSTACK_E2E_OS_ARCH", "amd64"),
		SourceType:        openStackE2EDefault("OPENSTACK_E2E_SOURCE_TYPE", "cloud-image"),
		SourceProviderRef: os.Getenv("OPENSTACK_E2E_SOURCE_IMAGE_ID"),
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "openstack-e2e"},
			Format:            openStackE2EDefault("OPENSTACK_E2E_FORMAT", string(platform.FormatQCOW2)),
			Tags:              map[string]string{"imagebuilder.io/e2e": "true"},
		},
		Provisioners: openStackE2EProvisioners(),
		Timeout:      openStackE2EDuration(t, "OPENSTACK_E2E_BUILD_TIMEOUT", 45*time.Minute),
	}

	var completedImageID string
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		if err := plugin.CleanupRemoteBuild(cleanupCtx, req); err != nil {
			t.Logf("remote cleanup failed: %v", err)
		}
		if completedImageID != "" {
			if err := plugin.Cleanup(cleanupCtx, &platform.BuildArtifact{Metadata: map[string]string{"imageID": completedImageID}}); err != nil {
				t.Logf("image cleanup failed: %v", err)
			}
		}
	}()

	pollInterval := openStackE2EDuration(t, "OPENSTACK_E2E_POLL_INTERVAL", 20*time.Second)
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
				t.Fatalf("remote build completed without OpenStack image ID: %#v", result.Images)
			}
			completedImageID = result.Images[0].ImageRef.ID
			if result.Hygiene == nil || result.Hygiene.Status != "passed" {
				t.Fatalf("remote build completed without passed hygiene result: %#v", result.Hygiene)
			}
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for OpenStack remote build: %v", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func openStackE2EConfig() platform.PluginConfig {
	return platform.PluginConfig{
		ProviderConfigName: "openstack-e2e",
		Endpoint:           os.Getenv("OPENSTACK_E2E_AUTH_URL"),
		Region:             os.Getenv("OPENSTACK_E2E_REGION"),
		SecretData: map[string][]byte{
			"username":         []byte(os.Getenv("OPENSTACK_E2E_USERNAME")),
			"userID":           []byte(os.Getenv("OPENSTACK_E2E_USER_ID")),
			"password":         []byte(os.Getenv("OPENSTACK_E2E_PASSWORD")),
			"token":            []byte(os.Getenv("OPENSTACK_E2E_TOKEN")),
			"projectID":        []byte(os.Getenv("OPENSTACK_E2E_PROJECT_ID")),
			"projectName":      []byte(os.Getenv("OPENSTACK_E2E_PROJECT_NAME")),
			"domainID":         []byte(os.Getenv("OPENSTACK_E2E_DOMAIN_ID")),
			"domainName":       []byte(openStackE2EDefault("OPENSTACK_E2E_DOMAIN_NAME", "Default")),
			"remotePrivateKey": []byte(os.Getenv("OPENSTACK_E2E_REMOTE_PRIVATE_KEY")),
		},
		Extra: map[string]string{
			"remote.flavorRef":       os.Getenv("OPENSTACK_E2E_FLAVOR_REF"),
			"remote.networkID":       os.Getenv("OPENSTACK_E2E_NETWORK_ID"),
			"remote.networkName":     os.Getenv("OPENSTACK_E2E_NETWORK_NAME"),
			"remote.securityGroups":  os.Getenv("OPENSTACK_E2E_SECURITY_GROUPS"),
			"remote.keyName":         os.Getenv("OPENSTACK_E2E_KEY_NAME"),
			"remote.sshUser":         openStackE2EDefault("OPENSTACK_E2E_SSH_USER", "ubuntu"),
			"remote.sshPort":         openStackE2EDefault("OPENSTACK_E2E_SSH_PORT", "22"),
			"remote.configDrive":     os.Getenv("OPENSTACK_E2E_CONFIG_DRIVE"),
			"image.containerFormat":  openStackE2EDefault("OPENSTACK_E2E_CONTAINER_FORMAT", "bare"),
			"image.visibility":       openStackE2EDefault("OPENSTACK_E2E_VISIBILITY", "private"),
			"image.property.os_type": string(openStackE2EOSFamily()),
		},
	}
}

func requireOpenStackE2EEnv(t *testing.T, cfg platform.PluginConfig) {
	t.Helper()
	required := map[string]string{
		"OPENSTACK_E2E_AUTH_URL":        cfg.Endpoint,
		"OPENSTACK_E2E_REGION":          cfg.Region,
		"OPENSTACK_E2E_SOURCE_IMAGE_ID": os.Getenv("OPENSTACK_E2E_SOURCE_IMAGE_ID"),
		"OPENSTACK_E2E_FLAVOR_REF":      cfg.Extra["remote.flavorRef"],
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required when OPENSTACK_E2E=1", key)
		}
	}
	if strings.TrimSpace(string(cfg.SecretData["token"])) == "" &&
		(strings.TrimSpace(string(cfg.SecretData["username"])) == "" || strings.TrimSpace(string(cfg.SecretData["password"])) == "") {
		t.Fatal("OPENSTACK_E2E_TOKEN or OPENSTACK_E2E_USERNAME/OPENSTACK_E2E_PASSWORD is required")
	}
	if len(openStackE2EProvisioners()) > 0 {
		for key, value := range map[string]string{
			"OPENSTACK_E2E_KEY_NAME":           cfg.Extra["remote.keyName"],
			"OPENSTACK_E2E_REMOTE_PRIVATE_KEY": string(cfg.SecretData["remotePrivateKey"]),
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s is required when OpenStack E2E provisioners are enabled", key)
			}
		}
	}
}

func openStackE2EOSFamily() platform.OSFamily {
	if strings.EqualFold(openStackE2EDefault("OPENSTACK_E2E_OS_FAMILY", "linux"), string(platform.OSFamilyWindows)) {
		return platform.OSFamilyWindows
	}
	return platform.OSFamilyLinux
}

func openStackE2EProvisioners() []v1alpha1.ProvisionerSpec {
	if strings.EqualFold(os.Getenv("OPENSTACK_E2E_DISABLE_PROVISIONER"), "true") {
		return nil
	}
	if openStackE2EOSFamily() == platform.OSFamilyWindows {
		return nil
	}
	return []v1alpha1.ProvisionerSpec{{
		Type:   "shell",
		Inline: openStackE2ELinuxProvisionerScript(),
	}}
}

func openStackE2ELinuxProvisionerScript() string {
	if script := strings.TrimSpace(os.Getenv("OPENSTACK_E2E_SHELL")); script != "" {
		return script
	}
	switch strings.ToLower(openStackE2EDefault("OPENSTACK_E2E_WORKLOAD", "marker")) {
	case "tomcat":
		return workloads.TomcatTarShellProvisioner()
	default:
		return "set -eu\necho imagebuilder-openstack-e2e >/tmp/imagebuilder-e2e.txt"
	}
}

func openStackE2EDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func openStackE2EDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
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
