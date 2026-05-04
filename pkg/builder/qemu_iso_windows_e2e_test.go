package builder_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/powershell"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/sysprep"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestQEMUISOBackend_WindowsCloudbaseInitSysprep_E2E(t *testing.T) {
	if os.Getenv("IMAGEBUILDER_WINDOWS_E2E") != "1" {
		t.Skip("set IMAGEBUILDER_WINDOWS_E2E=1 to run the live Windows Cloudbase-Init/Sysprep ISO E2E test")
	}
	isoPath := os.Getenv("IMAGEBUILDER_WINDOWS_E2E_ISO_PATH")
	if strings.TrimSpace(isoPath) == "" {
		t.Fatal("IMAGEBUILDER_WINDOWS_E2E_ISO_PATH is required")
	}
	if _, err := os.Stat(isoPath); err != nil {
		t.Fatalf("stat IMAGEBUILDER_WINDOWS_E2E_ISO_PATH: %v", err)
	}
	if strings.TrimSpace(os.Getenv("IMAGEBUILDER_WINDOWS_E2E_CLOUDBASE_INIT_MSI")) == "" {
		t.Fatal("IMAGEBUILDER_WINDOWS_E2E_CLOUDBASE_INIT_MSI is required and must be a guest-visible MSI path")
	}

	timeout := windowsE2EDuration(t, "IMAGEBUILDER_WINDOWS_E2E_TIMEOUT", 3*time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	workspace := windowsE2EWorkspace(t)
	credentialDir := filepath.Join(workspace, "credentials")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatalf("create credential dir: %v", err)
	}
	img := windowsE2EImage(t)
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		QEMUImgPath:     windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_QEMU_IMG", ""),
		QEMUSystemPath:  windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_QEMU_SYSTEM", ""),
		GenISOImagePath: windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_GENISOIMAGE", ""),
		DiskSize:        windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_DISK_SIZE", "60G"),
	})
	artifact, err := backend.Build(ctx, builder.BackendRequest{
		BuildRequest: builder.BuildRequest{
			Image:         img,
			WorkspaceDir:  workspace,
			CredentialDir: credentialDir,
		},
		Source: &builder.SourceArtifact{Path: isoPath},
		Format: platform.FormatQCOW2,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if artifact.Path == "" {
		t.Fatal("Windows E2E completed without artifact path")
	}
	if artifact.Format != platform.FormatQCOW2 {
		t.Fatalf("artifact format = %q, want qcow2", artifact.Format)
	}
	if artifact.SizeBytes == 0 {
		t.Fatal("Windows E2E produced an empty artifact")
	}
	if artifact.OS != platform.OSFamilyWindows {
		t.Fatalf("artifact OS = %q, want windows", artifact.OS)
	}
	t.Logf("Windows Cloudbase-Init/Sysprep E2E artifact: %s", artifact.Path)
}

func windowsE2EImage(t *testing.T) *v1alpha1.VMImage {
	t.Helper()
	img := testImage(v1alpha1.SourceSpec{
		Type:        "iso",
		BootCommand: windowsE2EBootCommand(),
		Installer: &v1alpha1.InstallerMediaSpec{
			Type: "autounattend",
			Windows: &v1alpha1.WindowsInstallerMediaSpec{
				VirtioDriverPath: os.Getenv("IMAGEBUILDER_WINDOWS_E2E_VIRTIO_DRIVER_PATH"),
				CloudbaseInitMSI: os.Getenv("IMAGEBUILDER_WINDOWS_E2E_CLOUDBASE_INIT_MSI"),
				CloudbaseInit:    windowsE2ECloudbaseInit(),
			},
		},
	}, string(platform.FormatQCOW2))
	img.Spec.OS = v1alpha1.OSSpec{
		Family:       "windows",
		Distribution: windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_DISTRIBUTION", "windows-server"),
		Version:      windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_VERSION", "2022"),
		Arch:         windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_ARCH", "amd64"),
	}
	buildTimeout := windowsE2EDuration(t, "IMAGEBUILDER_WINDOWS_E2E_TIMEOUT", 3*time.Hour)
	img.Spec.Build.Timeout = &metav1.Duration{Duration: buildTimeout}
	winRMHTTPS := true
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol:  "winrm",
		Host:      windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_HOST", "127.0.0.1"),
		HostPort:  int32(windowsE2EInt(t, "IMAGEBUILDER_WINDOWS_E2E_WINRM_PORT", 55986)),
		GuestPort: int32(windowsE2EInt(t, "IMAGEBUILDER_WINDOWS_E2E_WINRM_GUEST_PORT", 5986)),
		User:      windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_USER", "Administrator"),
		WinRM:     &v1alpha1.WinRMAccessSpec{HTTPS: &winRMHTTPS, InsecureSkipVerify: true},
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{
				Password:       true,
				PasswordLength: int32(windowsE2EInt(t, "IMAGEBUILDER_WINDOWS_E2E_PASSWORD_LENGTH", 24)),
			},
		},
	}
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{
			Type:   "powershell",
			Inline: windowsE2EDefault("IMAGEBUILDER_WINDOWS_E2E_POWERSHELL", "$ErrorActionPreference = 'Stop'; Set-Content -Path C:\\imagebuilder-windows-e2e.txt -Value 'imagebuilder-windows-e2e'"),
		},
		{
			Type: "sysprep",
			WindowsConfig: &v1alpha1.WindowsProvisionerConfig{
				Sysprep: &v1alpha1.SysprepConfig{Generalize: true, Shutdown: true},
			},
		},
	}
	return img
}

func windowsE2ECloudbaseInit() *v1alpha1.CloudbaseInitSpec {
	if strings.TrimSpace(os.Getenv("IMAGEBUILDER_WINDOWS_E2E_CLOUDBASE_INIT_MSI")) == "" {
		return nil
	}
	return &v1alpha1.CloudbaseInitSpec{
		MetadataServices: []string{
			"cloudbaseinit.metadata.services.configdrive.ConfigDriveService",
			"cloudbaseinit.metadata.services.nocloudservice.NoCloudConfigDriveService",
		},
		Plugins: []string{
			"cloudbaseinit.plugins.common.mtu.MTUPlugin",
			"cloudbaseinit.plugins.windows.createuser.CreateUserPlugin",
			"cloudbaseinit.plugins.common.sshpublickeys.SetUserSSHPublicKeysPlugin",
			"cloudbaseinit.plugins.windows.extendvolumes.ExtendVolumesPlugin",
			"cloudbaseinit.plugins.common.localscripts.LocalScriptsPlugin",
		},
		AddUserToLocalGroups: []string{"Administrators"},
	}
}

func windowsE2EBootCommand() []string {
	value := strings.TrimSpace(os.Getenv("IMAGEBUILDER_WINDOWS_E2E_BOOT_COMMAND"))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func windowsE2EWorkspace(t *testing.T) string {
	t.Helper()
	if value := strings.TrimSpace(os.Getenv("IMAGEBUILDER_WINDOWS_E2E_WORKSPACE")); value != "" {
		if err := os.MkdirAll(value, 0o700); err != nil {
			t.Fatalf("create IMAGEBUILDER_WINDOWS_E2E_WORKSPACE: %v", err)
		}
		return value
	}
	return t.TempDir()
}

func windowsE2EDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func windowsE2EDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
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

func windowsE2EInt(t interface{ Fatalf(string, ...any) }, key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || parsed > 65535 {
		t.Fatalf("%s must be a TCP port or positive integer, got %q", key, value)
	}
	return parsed
}
