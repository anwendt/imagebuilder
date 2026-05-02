package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/ansible"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/chef"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/cloudinit"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/custom"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/file"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/powershell"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/puppet"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/saltstack"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/shell"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/sysprep"
)

const (
	defaultWorkspace   = "/workspace"
	resultFileName     = "result.json"
	terminationLogPath = "/dev/termination-log"
)

type resultFile struct {
	Path         string                          `json:"path"`
	Format       string                          `json:"format"`
	Checksum     string                          `json:"checksum"`
	SizeBytes    int64                           `json:"sizeBytes"`
	OS           string                          `json:"os"`
	Metadata     map[string]string               `json:"metadata,omitempty"`
	Provisioners []builder.ProvisionerStepStatus `json:"provisioners,omitempty"`
	Reason       string                          `json:"reason,omitempty"`
	Error        string                          `json:"error,omitempty"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	ctx := context.Background()
	req, workspace, err := requestFromEnv()
	if err != nil {
		slog.Error("invalid builder configuration", slog.Any("error", err))
		os.Exit(1)
	}
	log := slog.Default().With(
		slog.String("buildID", envOrDefault("BUILD_ID", req.Image.Namespace+"/"+req.Image.Name)),
		slog.String("vmimage", req.Image.Name),
		slog.String("namespace", req.Image.Namespace),
	)
	log.Info("build started", slog.String("phase", "Build"))

	engine := builder.NewEngine(builder.EngineOptions{
		Backends: []builder.Backend{
			builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
				QEMUImgPath:         envOrDefault("QEMU_IMG_PATH", "/usr/bin/qemu-img"),
				QEMUSystemPath:      envOrDefault("QEMU_SYSTEM_PATH", "/usr/bin/qemu-system-x86_64"),
				QEMUSystemPathARM64: envOrDefault("QEMU_SYSTEM_PATH_ARM64", "/usr/bin/qemu-system-aarch64"),
				ARM64EFICodePath:    os.Getenv("QEMU_EFI_CODE_PATH_ARM64"),
				GenISOImagePath:     envOrDefault("GENISOIMAGE_PATH", "/usr/bin/genisoimage"),
				DiskSize:            os.Getenv("ISO_DISK_SIZE"),
			}),
			builder.NewQEMUImageBackend(builder.QEMUImageBackendOptions{
				QEMUImgPath: envOrDefault("QEMU_IMG_PATH", "/usr/bin/qemu-img"),
			}),
			builder.NewCloudImageBackend(),
		},
	})
	artifact, err := engine.Build(ctx, req)
	if err != nil {
		log.Error("build failed", slog.String("phase", builder.ErrorReason(err)), slog.Any("error", err))
		if writeErr := writeFailure(filepath.Join(workspace, resultFileName), err); writeErr != nil {
			log.Warn("write failure result", slog.String("phase", "Build"), slog.Any("error", writeErr))
		}
		if writeErr := writeFailure(terminationLogPath, err); writeErr != nil {
			log.Warn("write failure termination message", slog.String("phase", "Build"), slog.Any("error", writeErr))
		}
		os.Exit(1)
	}

	result := resultFile{
		Path:         artifact.Path,
		Format:       string(artifact.Format),
		Checksum:     artifact.Checksum,
		SizeBytes:    artifact.SizeBytes,
		OS:           string(artifact.OS),
		Metadata:     artifact.Metadata,
		Provisioners: readProvisionerStatuses(filepath.Join(workspace, "provisioners-result.json")),
	}
	if err := writeResult(filepath.Join(workspace, resultFileName), result); err != nil {
		log.Error("write build result", slog.String("phase", "Build"), slog.Any("error", err))
		os.Exit(1)
	}
	if err := writeResult(terminationLogPath, result); err != nil {
		log.Warn("write termination message", slog.String("phase", "Build"), slog.Any("error", err))
	}
	log.Info("build completed", slog.String("phase", "Build"), slog.String("artifact", artifact.Path), slog.String("format", string(artifact.Format)))
}

func readProvisionerStatuses(path string) []builder.ProvisionerStepStatus {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var statuses []builder.ProvisionerStepStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		return nil
	}
	return statuses
}

func requestFromEnv() (builder.BuildRequest, string, error) {
	workspace := envOrDefault("WORKSPACE_DIR", defaultWorkspace)
	timeout := 2 * time.Hour
	if raw := os.Getenv("BUILD_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return builder.BuildRequest{}, "", fmt.Errorf("parse BUILD_TIMEOUT: %w", err)
		}
		timeout = parsed
	}

	guestAccess, err := guestAccessFromEnv()
	if err != nil {
		return builder.BuildRequest{}, "", err
	}
	provisioners, err := provisionersFromEnv()
	if err != nil {
		return builder.BuildRequest{}, "", err
	}
	security, err := buildSecurityFromEnv()
	if err != nil {
		return builder.BuildRequest{}, "", err
	}
	cacheTTL, err := cacheTTLFromEnv()
	if err != nil {
		return builder.BuildRequest{}, "", err
	}

	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envOrDefault("VMIMAGE_NAME", "image-build"),
			Namespace: envOrDefault("VMIMAGE_NAMESPACE", "default"),
		},
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{
				Family:       os.Getenv("OS_FAMILY"),
				Distribution: os.Getenv("OS_DISTRIBUTION"),
				Version:      os.Getenv("OS_VERSION"),
				Arch:         envOrDefault("OS_ARCH", "amd64"),
			},
			Source: v1alpha1.SourceSpec{
				Type:        os.Getenv("SOURCE_TYPE"),
				URL:         os.Getenv("SOURCE_URL"),
				Checksum:    os.Getenv("SOURCE_CHECKSUM"),
				BootCommand: bootCommandFromEnv(),
			},
			Provisioners: provisioners,
			Targets: []v1alpha1.TargetSpec{
				{
					ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: envOrDefault("TARGET_PROVIDER_CONFIG", "default")},
					Format:            envOrDefault("TARGET_FORMAT", "raw"),
				},
			},
			Build: v1alpha1.BuildSpec{
				Timeout:     &metav1.Duration{Duration: timeout},
				GuestAccess: guestAccess,
				Security:    security,
			},
		},
	}
	if img.Spec.OS.Family == "" {
		return builder.BuildRequest{}, "", fmt.Errorf("OS_FAMILY is required")
	}
	if img.Spec.Source.Type == "" {
		return builder.BuildRequest{}, "", fmt.Errorf("SOURCE_TYPE is required")
	}
	if img.Spec.Source.URL == "" {
		return builder.BuildRequest{}, "", fmt.Errorf("SOURCE_URL is required")
	}
	if img.Spec.Source.Checksum == "" {
		return builder.BuildRequest{}, "", fmt.Errorf("SOURCE_CHECKSUM is required")
	}

	return builder.BuildRequest{
		Image:         img,
		WorkspaceDir:  workspace,
		CacheDir:      os.Getenv("CACHE_DIR"),
		CacheTTL:      cacheTTL,
		CacheRetain:   os.Getenv("CACHE_RETAIN_POLICY"),
		CredentialDir: os.Getenv("GUEST_CREDENTIALS_DIR"),
	}, workspace, nil
}

func cacheTTLFromEnv() (time.Duration, error) {
	raw := os.Getenv("CACHE_TTL")
	if raw == "" {
		return 0, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse CACHE_TTL: %w", err)
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("CACHE_TTL must be greater than zero")
	}
	return ttl, nil
}

func buildSecurityFromEnv() (*v1alpha1.BuildSecuritySpec, error) {
	raw := os.Getenv("QEMU_ENABLE_KVM")
	if raw == "" {
		return nil, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("parse QEMU_ENABLE_KVM: %w", err)
	}
	if !enabled {
		return nil, nil
	}
	return &v1alpha1.BuildSecuritySpec{EnableKVM: true}, nil
}

func writeResult(path string, result resultFile) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeFailure(path string, err error) error {
	return writeResult(path, resultFile{
		Reason: builder.ErrorReason(err),
		Error:  sanitizeErrorDetail(builder.ErrorDetail(err)),
	})
}

func sanitizeErrorDetail(detail string) string {
	if detail == "" {
		return ""
	}
	replacements := []string{
		guestCredsEnvPath(), "[guest-credentials]",
		generatedCredsEnvPath(), "[generated-credentials]",
	}
	detail = strings.NewReplacer(replacements...).Replace(detail)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(password|passwd|token|secret|private[_-]?key)(\s*[:=]\s*)\S+`),
		regexp.MustCompile(`(?i)(Authorization:\s*(Basic|Bearer)\s+)\S+`),
	}
	for _, pattern := range patterns {
		detail = pattern.ReplaceAllString(detail, `$1$2[redacted]`)
	}
	return detail
}

func guestCredsEnvPath() string {
	if path := os.Getenv("GUEST_ACCESS_PASSWORD_PATH"); path != "" {
		return path
	}
	if path := os.Getenv("GUEST_ACCESS_SSH_KEY_PATH"); path != "" {
		return path
	}
	return "/credentials/guest"
}

func generatedCredsEnvPath() string {
	if path := os.Getenv("GUEST_CREDENTIALS_DIR"); path != "" {
		return path
	}
	return "/credentials/generated"
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func bootCommandFromEnv() []string {
	raw := os.Getenv("BOOT_COMMAND")
	if raw == "" {
		return nil
	}
	var commands []string
	if err := json.Unmarshal([]byte(raw), &commands); err == nil {
		return commands
	}
	return []string{raw}
}

func provisionersFromEnv() ([]v1alpha1.ProvisionerSpec, error) {
	raw := os.Getenv("PROVISIONERS")
	if raw == "" {
		return nil, nil
	}
	var provisioners []v1alpha1.ProvisionerSpec
	if err := json.Unmarshal([]byte(raw), &provisioners); err != nil {
		return nil, fmt.Errorf("parse PROVISIONERS: %w", err)
	}
	return provisioners, nil
}

func guestAccessFromEnv() (*v1alpha1.GuestAccessSpec, error) {
	protocol := os.Getenv("GUEST_ACCESS_PROTOCOL")
	hostPort, err := int32FromEnv("GUEST_ACCESS_HOST_PORT")
	if err != nil {
		return nil, err
	}
	if protocol == "" && hostPort == 0 {
		return nil, nil
	}
	guestPort, err := int32FromEnv("GUEST_ACCESS_GUEST_PORT")
	if err != nil {
		return nil, err
	}
	access := &v1alpha1.GuestAccessSpec{
		Protocol:     protocol,
		Host:         os.Getenv("GUEST_ACCESS_HOST"),
		HostPort:     hostPort,
		User:         os.Getenv("GUEST_ACCESS_USER"),
		SSHKeyPath:   os.Getenv("GUEST_ACCESS_SSH_KEY_PATH"),
		PasswordPath: os.Getenv("GUEST_ACCESS_PASSWORD_PATH"),
		GuestPort:    guestPort,
	}
	if raw := os.Getenv("GUEST_ACCESS_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("parse GUEST_ACCESS_TIMEOUT: %w", err)
		}
		access.Timeout = &metav1.Duration{Duration: parsed}
	}
	if protocol == "winrm" {
		insecure, err := boolFromEnv("GUEST_ACCESS_WINRM_INSECURE_SKIP_VERIFY")
		if err != nil {
			return nil, err
		}
		winrm := &v1alpha1.WinRMAccessSpec{
			InsecureSkipVerify: insecure,
		}
		if raw := os.Getenv("GUEST_ACCESS_WINRM_HTTPS"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("parse GUEST_ACCESS_WINRM_HTTPS: %w", err)
			}
			winrm.HTTPS = &parsed
		}
		access.WinRM = winrm
	}
	credentials, err := guestCredentialsFromEnv()
	if err != nil {
		return nil, err
	}
	access.Credentials = credentials
	return access, nil
}

func guestCredentialsFromEnv() (*v1alpha1.GuestCredentialsSpec, error) {
	sshKey, err := boolFromEnv("GUEST_CREDENTIALS_GENERATE_SSH_KEY")
	if err != nil {
		return nil, err
	}
	password, err := boolFromEnv("GUEST_CREDENTIALS_GENERATE_PASSWORD")
	if err != nil {
		return nil, err
	}
	method := os.Getenv("GUEST_CREDENTIALS_INJECTION_METHOD")
	passwordLength, err := int32FromEnv("GUEST_CREDENTIALS_GENERATE_PASSWORD_LENGTH")
	if err != nil {
		return nil, err
	}
	if !sshKey && !password && method == "" {
		return nil, nil
	}
	creds := &v1alpha1.GuestCredentialsSpec{}
	if sshKey || password || passwordLength != 0 {
		creds.Generate = &v1alpha1.GuestGeneratedCredentialsSpec{
			SSHKey:         sshKey,
			Password:       password,
			PasswordLength: passwordLength,
		}
	}
	if method != "" {
		creds.Injection = &v1alpha1.GuestCredentialInjectionSpec{Method: method}
	}
	return creds, nil
}

func int32FromEnv(name string) (int32, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return int32(value), nil
}

func boolFromEnv(name string) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}
