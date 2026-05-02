package builder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

func TestPrepareInstallerMedia_WritesCloudInitNoCloudFiles(t *testing.T) {
	workspace := t.TempDir()
	img := installerMediaImage("linux", &v1alpha1.InstallerMediaSpec{
		Type:          "nocloud",
		UserData:      "#cloud-config\npackage_update: true\n",
		MetaData:      "instance-id: test\n",
		NetworkConfig: "version: 2\n",
	})

	if err := prepareInstallerMedia(context.Background(), img, workspace); err != nil {
		t.Fatalf("prepareInstallerMedia returned error: %v", err)
	}
	assertFileContains(t, filepath.Join(workspace, "cloud-init", "user-data"), "package_update: true")
	assertFileContains(t, filepath.Join(workspace, "cloud-init", "meta-data"), "instance-id: test")
	assertFileContains(t, filepath.Join(workspace, "cloud-init", "network-config"), "version: 2")
}

func TestPrepareInstallerMedia_WritesKickstartAndPreseedFiles(t *testing.T) {
	tests := []struct {
		name      string
		installer *v1alpha1.InstallerMediaSpec
		path      string
		want      string
	}{
		{
			name:      "kickstart",
			installer: &v1alpha1.InstallerMediaSpec{Type: "kickstart", Kickstart: "text\nreboot\n"},
			path:      filepath.Join("kickstart", "ks.cfg"),
			want:      "reboot",
		},
		{
			name:      "preseed",
			installer: &v1alpha1.InstallerMediaSpec{Type: "preseed", Preseed: "d-i passwd/root-login boolean false\n"},
			path:      filepath.Join("preseed", "preseed.cfg"),
			want:      "root-login",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			img := installerMediaImage("linux", tt.installer)
			if err := prepareInstallerMedia(context.Background(), img, workspace); err != nil {
				t.Fatalf("prepareInstallerMedia returned error: %v", err)
			}
			assertFileContains(t, filepath.Join(workspace, tt.path), tt.want)
		})
	}
}

func TestPrepareInstallerMedia_WritesAutounattendWithWindowsHooks(t *testing.T) {
	workspace := t.TempDir()
	img := installerMediaImage("windows", &v1alpha1.InstallerMediaSpec{
		Type: "autounattend",
		Windows: &v1alpha1.WindowsInstallerMediaSpec{
			VirtioDriverPath: `E:\viostor\2k22\amd64`,
			CloudbaseInitMSI: `E:\CloudbaseInitSetup.msi`,
		},
	})

	if err := prepareInstallerMedia(context.Background(), img, workspace); err != nil {
		t.Fatalf("prepareInstallerMedia returned error: %v", err)
	}
	path := filepath.Join(workspace, "autounattend", "Autounattend.xml")
	assertFileContains(t, path, `E:\viostor\2k22\amd64`)
	assertFileContains(t, path, `CloudbaseInitSetup.msi`)
	assertFileContains(t, path, `cloudbase-init.conf`)
	assertFileContains(t, path, `metadata_services=cloudbaseinit.metadata.services.configdrive.ConfigDriveService,cloudbaseinit.metadata.services.nocloudservice.NoCloudConfigDriveService`)
	assertFileContains(t, path, `Set-Service -Name cloudbase-init -StartupType Automatic`)
	assertFileContains(t, path, "Enable WinRM HTTPS")
}

func TestPrepareInstallerMedia_WritesCustomCloudbaseInitConfig(t *testing.T) {
	workspace := t.TempDir()
	img := installerMediaImage("windows", &v1alpha1.InstallerMediaSpec{
		Type: "autounattend",
		Windows: &v1alpha1.WindowsInstallerMediaSpec{
			CloudbaseInitMSI: `E:\CloudbaseInitSetup.msi`,
			CloudbaseInit: &v1alpha1.CloudbaseInitSpec{
				MetadataServices:     []string{"custom.MetadataService"},
				Plugins:              []string{"custom.Plugin"},
				LocalScriptsPath:     `C:\Imagebuilder\Scripts`,
				AddUserToLocalGroups: []string{"Administrators", "Remote Management Users"},
			},
		},
	})

	if err := prepareInstallerMedia(context.Background(), img, workspace); err != nil {
		t.Fatalf("prepareInstallerMedia returned error: %v", err)
	}
	path := filepath.Join(workspace, "autounattend", "Autounattend.xml")
	assertFileContains(t, path, "metadata_services=custom.MetadataService")
	assertFileContains(t, path, "plugins=custom.Plugin")
	assertFileContains(t, path, `local_scripts_path=C:\Imagebuilder\Scripts`)
	assertFileContains(t, path, "groups=Administrators,Remote Management Users")
}

func TestPrepareInstallerMedia_WritesARM64AutounattendComponentArchitecture(t *testing.T) {
	workspace := t.TempDir()
	img := installerMediaImage("windows", &v1alpha1.InstallerMediaSpec{
		Type: "autounattend",
		Windows: &v1alpha1.WindowsInstallerMediaSpec{
			VirtioDriverPath: `E:\viostor\2k22\arm64`,
		},
	})
	img.Spec.OS.Arch = "arm64"

	if err := prepareInstallerMedia(context.Background(), img, workspace); err != nil {
		t.Fatalf("prepareInstallerMedia returned error: %v", err)
	}
	path := filepath.Join(workspace, "autounattend", "Autounattend.xml")
	assertFileContains(t, path, `processorArchitecture="arm64"`)
	assertFileContains(t, path, `E:\viostor\2k22\arm64`)
}

func installerMediaImage(osFamily string, installer *v1alpha1.InstallerMediaSpec) *v1alpha1.VMImage {
	return &v1alpha1.VMImage{
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{Family: osFamily, Distribution: "test", Version: "1", Arch: "amd64"},
			Source: v1alpha1.SourceSpec{
				Type:      "iso",
				Installer: installer,
			},
		},
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q:\n%s", path, want, data)
	}
}
