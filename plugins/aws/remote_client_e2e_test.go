package aws

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestAWSRemoteBuild_E2E(t *testing.T) {
	if os.Getenv("AWS_E2E") != "1" {
		t.Skip("set AWS_E2E=1 to run the real AWS remote build E2E test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("AWS_E2E_TIMEOUT", 45*time.Minute))
	defer cancel()

	buildID := "e2e-" + time.Now().UTC().Format("20060102-150405")
	cfg := awsConfig{
		region: os.Getenv("AWS_E2E_REGION"),
		extraConfig: map[string]string{
			"remote.instanceType":       envDefault("AWS_E2E_INSTANCE_TYPE", "t3.micro"),
			"remote.subnetId":           os.Getenv("AWS_E2E_SUBNET_ID"),
			"remote.securityGroupIds":   os.Getenv("AWS_E2E_SECURITY_GROUP_IDS"),
			"remote.iamInstanceProfile": os.Getenv("AWS_E2E_INSTANCE_PROFILE"),
			"remote.kmsKeyId":           os.Getenv("AWS_E2E_KMS_KEY_ID"),
			"remote.rootVolumeSizeGiB":  envDefault("AWS_E2E_ROOT_VOLUME_SIZE_GIB", "16"),
		},
	}
	if roleARN := os.Getenv("AWS_E2E_ROLE_ARN"); roleARN != "" {
		cfg.roleARN = roleARN
	}
	for key, value := range map[string]string{
		"AWS_E2E_REGION":             cfg.region,
		"AWS_E2E_SOURCE_AMI":         os.Getenv("AWS_E2E_SOURCE_AMI"),
		"AWS_E2E_SUBNET_ID":          cfg.extraConfig["remote.subnetId"],
		"AWS_E2E_SECURITY_GROUP_IDS": cfg.extraConfig["remote.securityGroupIds"],
		"AWS_E2E_INSTANCE_PROFILE":   cfg.extraConfig["remote.iamInstanceProfile"],
		"AWS_E2E_KMS_KEY_ID":         cfg.extraConfig["remote.kmsKeyId"],
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required when AWS_E2E=1", key)
		}
	}

	client, err := newAWSRemoteBuildClient(ctx, cfg, slog.Default())
	if err != nil {
		t.Fatalf("newAWSRemoteBuildClient: %v", err)
	}
	awsClient, ok := client.(*awsSDKRemoteBuildClient)
	if !ok {
		t.Fatalf("client type = %T, want *awsSDKRemoteBuildClient", client)
	}

	req := awsRemoteBuildRequest{
		BuildID:           buildID,
		ImageName:         "imagebuilder-aws-e2e",
		Namespace:         "imagebuilder-e2e",
		Region:            cfg.region,
		SourceType:        "cloud-image",
		SourceProviderRef: os.Getenv("AWS_E2E_SOURCE_AMI"),
		OSFamily:          e2eOSFamily(),
		OSDistribution:    e2eOSDistribution(),
		OSVersion:         e2eOSVersion(),
		OSArch:            envDefault("AWS_E2E_OS_ARCH", "amd64"),
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-e2e"},
			Format:            string(platform.FormatAMI),
			Tags:              map[string]string{"imagebuilder.io/e2e": "true"},
		},
		Provisioners: e2eProvisioners(),
		Timeout:      envDuration("AWS_E2E_BUILD_TIMEOUT", 40*time.Minute),
	}

	var lastRef awsRemoteOperationRef
	defer func() {
		if lastRef.InstanceID != "" || lastRef.ImageID != "" {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cleanupCancel()
			if err := awsClient.cleanupRemoteBuild(cleanupCtx, lastRef); err != nil {
				t.Logf("cleanup failed: %v", err)
			}
		}
	}()

	for {
		state, err := awsClient.ReconcileRemoteBuild(ctx, req)
		if err != nil {
			t.Fatalf("ReconcileRemoteBuild: %v", err)
		}
		if state.OperationRef != "" {
			req.OperationRef = state.OperationRef
			parsed, err := parseAWSRemoteOperationRef(state.OperationRef)
			if err != nil {
				t.Fatalf("parse operation ref: %v", err)
			}
			lastRef = parsed
		}
		t.Logf("phase=%s done=%t ref=%s message=%s", state.Phase, state.Done, state.OperationRef, state.Message)
		if state.Done {
			if state.AMIID == "" {
				t.Fatal("remote build completed without AMI ID")
			}
			lastRef.ImageID = state.AMIID
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for AWS remote build: %v", ctx.Err())
		case <-time.After(envDuration("AWS_E2E_POLL_INTERVAL", 20*time.Second)):
		}
	}
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func e2eOSFamily() platform.OSFamily {
	if strings.EqualFold(envDefault("AWS_E2E_OS_FAMILY", "linux"), string(platform.OSFamilyWindows)) {
		return platform.OSFamilyWindows
	}
	return platform.OSFamilyLinux
}

func e2eOSDistribution() string {
	if e2eOSFamily() == platform.OSFamilyWindows {
		return envDefault("AWS_E2E_OS_DISTRIBUTION", "windows-server")
	}
	return envDefault("AWS_E2E_OS_DISTRIBUTION", "ubuntu")
}

func e2eOSVersion() string {
	if e2eOSFamily() == platform.OSFamilyWindows {
		return envDefault("AWS_E2E_OS_VERSION", "2022")
	}
	return envDefault("AWS_E2E_OS_VERSION", "24.04")
}

func e2eProvisioners() []v1alpha1.ProvisionerSpec {
	if e2eOSFamily() == platform.OSFamilyWindows {
		return []v1alpha1.ProvisionerSpec{
			{Type: "powershell", Inline: "$ErrorActionPreference = 'Stop'; Set-Content -Path C:\\imagebuilder-aws-e2e.txt -Value 'imagebuilder-aws-e2e'"},
		}
	}
	return []v1alpha1.ProvisionerSpec{
		{Type: "shell", Inline: "set -eu\necho imagebuilder-aws-e2e >/tmp/imagebuilder-e2e.txt"},
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		panic(fmt.Sprintf("%s must be a duration, got %q", key, value))
	}
	return duration
}
