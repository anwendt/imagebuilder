package builder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

func TestPrepareGuestCredentials_GeneratesSSHKeyInCredentialDirAndInjectsCloudInit(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := t.TempDir()
	img := guestCredentialTestImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "ssh",
		HostPort: 2222,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{SSHKey: true},
		},
	}

	creds, err := prepareGuestCredentials(context.Background(), img, workspace, credentialDir)
	if err != nil {
		t.Fatalf("prepareGuestCredentials returned error: %v", err)
	}
	if !strings.HasPrefix(creds.PrivateKeyPath, credentialDir) {
		t.Fatalf("private key path = %q, want under %q", creds.PrivateKeyPath, credentialDir)
	}
	if _, err := os.Stat(creds.PrivateKeyPath); err != nil {
		t.Fatalf("generated private key missing: %v", err)
	}
	info, err := os.Stat(creds.PrivateKeyPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, want 0600", info.Mode().Perm())
	}
	if len(img.Spec.Provisioners) != 1 || img.Spec.Provisioners[0].Type != "cloud-init" {
		t.Fatalf("cloud-init provisioner not injected: %#v", img.Spec.Provisioners)
	}
	if !strings.Contains(img.Spec.Provisioners[0].Inline, "ssh_authorized_keys:") ||
		!strings.Contains(img.Spec.Provisioners[0].Inline, "name: imagebuilder") {
		t.Fatalf("cloud-init credentials missing:\n%s", img.Spec.Provisioners[0].Inline)
	}
}

func TestPrepareGuestCredentials_GeneratesPasswordInCredentialDirAndAutounattend(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := t.TempDir()
	img := guestCredentialTestImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.OS.Family = "windows"
	img.Spec.Source.Installer = &v1alpha1.InstallerMediaSpec{
		Type: "autounattend",
		Windows: &v1alpha1.WindowsInstallerMediaSpec{
			CloudbaseInitMSI: `E:\CloudbaseInitSetup.msi`,
		},
	}
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "winrm",
		HostPort: 55986,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{Password: true, PasswordLength: 24},
		},
	}

	creds, err := prepareGuestCredentials(context.Background(), img, workspace, credentialDir)
	if err != nil {
		t.Fatalf("prepareGuestCredentials returned error: %v", err)
	}
	if !strings.HasPrefix(creds.PasswordPath, credentialDir) {
		t.Fatalf("password path = %q, want under %q", creds.PasswordPath, credentialDir)
	}
	password, err := os.ReadFile(creds.PasswordPath)
	if err != nil {
		t.Fatalf("read password: %v", err)
	}
	if len(strings.TrimSpace(string(password))) != 24 {
		t.Fatalf("password length = %d, want 24", len(strings.TrimSpace(string(password))))
	}
	autounattend, err := os.ReadFile(filepath.Join(workspace, "autounattend", "Autounattend.xml"))
	if err != nil {
		t.Fatalf("read autounattend: %v", err)
	}
	if !strings.Contains(string(autounattend), "Enable WinRM HTTPS for imagebuilder") ||
		!strings.Contains(string(autounattend), "Configure Cloudbase-Init") ||
		!strings.Contains(string(autounattend), "cloudbase-init-unattend.conf") ||
		!strings.Contains(string(autounattend), strings.TrimSpace(string(password))) {
		t.Fatalf("autounattend credential material missing:\n%s", autounattend)
	}
}

func TestPrepareGuestCredentials_RejectsGeneratedPasswordWithCustomAutounattend(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := t.TempDir()
	img := &v1alpha1.VMImage{}
	img.Spec.OS.Family = "windows"
	img.Spec.Source.Type = "iso"
	img.Spec.Source.Installer = &v1alpha1.InstallerMediaSpec{
		Type:         "autounattend",
		Autounattend: "<unattend></unattend>",
	}
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "winrm",
		HostPort: 55986,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{Password: true},
		},
	}
	if err := prepareInstallerMedia(context.Background(), img, workspace); err != nil {
		t.Fatalf("prepareInstallerMedia returned error: %v", err)
	}

	_, err := prepareGuestCredentials(context.Background(), img, workspace, credentialDir)
	if err == nil {
		t.Fatal("prepareGuestCredentials should reject custom autounattend with generated credentials")
	}
}

func TestCleanupGeneratedGuestCredentials_RemovesSensitiveFiles(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := filepath.Join(t.TempDir(), "guest-credentials")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatalf("mkdir credentials: %v", err)
	}
	seedISO := filepath.Join(workspace, "cloud-init.iso")
	if err := os.WriteFile(seedISO, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write seed iso: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "cloud-init"), 0o700); err != nil {
		t.Fatalf("mkdir cloud-init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "autounattend"), 0o700); err != nil {
		t.Fatalf("mkdir autounattend: %v", err)
	}

	cleanupGeneratedGuestCredentials(workspace, GeneratedGuestCredentials{Dir: credentialDir}, []string{seedISO})

	for _, path := range []string{
		credentialDir,
		seedISO,
		filepath.Join(workspace, "cloud-init"),
		filepath.Join(workspace, "autounattend"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed, stat err = %v", path, err)
		}
	}
}

func guestCredentialTestImage(source v1alpha1.SourceSpec, format string) *v1alpha1.VMImage {
	return &v1alpha1.VMImage{
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{
				Family:       "linux",
				Distribution: "ubuntu",
				Version:      "24.04",
				Arch:         "amd64",
			},
			Source: source,
			Targets: []v1alpha1.TargetSpec{{
				ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "target"},
				Format:            format,
			}},
		},
	}
}
