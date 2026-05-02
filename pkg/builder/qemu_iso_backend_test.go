package builder_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/cloudinit"
)

func TestQEMUISOBackend_Build_CreatesDiskBootsVMAndConvertsArtifact(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "installer.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	qmpClient := &recordingQMPClient{}
	readiness := &recordingReadinessProbe{}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:         runner,
		QMPClient:      qmpClient,
		ReadinessProbe: readiness,
		QEMUImgPath:    "/usr/bin/qemu-img",
		QEMUSystemPath: "/usr/bin/qemu-system-x86_64",
	})

	artifact, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{
			Image: testImage(v1alpha1.SourceSpec{
				Type:        "iso",
				BootCommand: []string{"<tab>", " inst.ks=http://example.test/ks.cfg", "<enter>"},
			}, "qcow2"),
			WorkspaceDir: workspace,
		},
		Source: &builder.SourceArtifact{Path: sourcePath},
		Format: platform.FormatQCOW2,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(runner.commands) != 3 {
		t.Fatalf("commands = %#v, want create, system, convert", runner.commands)
	}
	create := runner.commands[0]
	if create.Name != "/usr/bin/qemu-img" || !equalStrings(create.Args[:3], []string{"create", "-f", "qcow2"}) {
		t.Fatalf("create command = %#v", create)
	}
	system := runner.commands[1]
	if system.Name != "/usr/bin/qemu-system-x86_64" {
		t.Fatalf("system command = %#v", system)
	}
	if !containsString(system.Args, "-cdrom") || !containsString(system.Args, sourcePath) {
		t.Fatalf("system args should include iso cdrom: %#v", system.Args)
	}
	if !containsString(system.Args, "-qmp") || !containsArgWithPrefix(system.Args, "unix:") {
		t.Fatalf("system args should include qmp socket: %#v", system.Args)
	}
	if !containsString(system.Args, "none") {
		t.Fatalf("system args should disable hmp monitor: %#v", system.Args)
	}
	if len(readiness.accesses) != 0 {
		t.Fatalf("readiness should not run without guestAccess, got %#v", readiness.accesses)
	}
	if !equalStrings(qmpClient.commands, []string{
		"sendkey tab",
		"sendkey spc",
		"sendkey i",
		"sendkey n",
		"sendkey s",
		"sendkey t",
		"sendkey dot",
		"sendkey k",
		"sendkey s",
		"sendkey equal",
		"sendkey h",
		"sendkey t",
		"sendkey t",
		"sendkey p",
		"sendkey shift-semicolon",
		"sendkey slash",
		"sendkey slash",
		"sendkey e",
		"sendkey x",
		"sendkey a",
		"sendkey m",
		"sendkey p",
		"sendkey l",
		"sendkey e",
		"sendkey dot",
		"sendkey t",
		"sendkey e",
		"sendkey s",
		"sendkey t",
		"sendkey slash",
		"sendkey k",
		"sendkey s",
		"sendkey dot",
		"sendkey c",
		"sendkey f",
		"sendkey g",
		"sendkey ret",
	}) {
		t.Fatalf("qmp commands = %#v", qmpClient.commands)
	}
	convert := runner.commands[2]
	if convert.Name != "/usr/bin/qemu-img" || !containsString(convert.Args, "convert") || !containsString(convert.Args, "qcow2") {
		t.Fatalf("convert command = %#v", convert)
	}
	if artifact.Path != filepath.Join(workspace, "artifact.qcow2") {
		t.Fatalf("artifact path = %q", artifact.Path)
	}
	if artifact.Format != platform.FormatQCOW2 {
		t.Fatalf("artifact format = %q, want qcow2", artifact.Format)
	}
	if _, err := os.Stat(filepath.Join(workspace, "install.qcow2")); !os.IsNotExist(err) {
		t.Fatalf("transient install disk should be cleaned up, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "qmp.sock")); !os.IsNotExist(err) {
		t.Fatalf("qmp socket should be cleaned up, stat err = %v", err)
	}
}

func TestQEMUISOBackend_Build_UsesARM64QEMUParameters(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "installer-arm64.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:              runner,
		QMPClient:           &recordingQMPClient{},
		ReadinessProbe:      &recordingReadinessProbe{},
		QEMUImgPath:         "/usr/bin/qemu-img",
		QEMUSystemPath:      "/usr/bin/qemu-system-x86_64",
		QEMUSystemPathARM64: "/usr/bin/qemu-system-aarch64",
		ARM64EFICodePath:    "/usr/share/AAVMF/AAVMF_CODE.fd",
		HygieneChecker:      &recordingHygieneChecker{},
	})
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.OS.Arch = "arm64"
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol:  "ssh",
		Host:      "127.0.0.1",
		HostPort:  2222,
		GuestPort: 22,
		User:      "imagebuilder",
	}

	if _, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: img, WorkspaceDir: workspace},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatQCOW2,
	}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	system := runner.commands[1]
	if system.Name != "/usr/bin/qemu-system-aarch64" {
		t.Fatalf("system command = %#v", system)
	}
	if !containsString(system.Args, "virt,accel=tcg") ||
		!containsString(system.Args, "-cpu") ||
		!containsString(system.Args, "max") ||
		!containsString(system.Args, "-bios") ||
		!containsString(system.Args, "/usr/share/AAVMF/AAVMF_CODE.fd") ||
		!containsString(system.Args, "virtio-net-device,netdev=net0") {
		t.Fatalf("arm64 system args = %#v", system.Args)
	}
}

func TestQEMUISOBackend_Build_UsesKVMAccelerationWhenEnabled(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "installer.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:         runner,
		QMPClient:      &recordingQMPClient{},
		ReadinessProbe: &recordingReadinessProbe{},
		QEMUImgPath:    "/usr/bin/qemu-img",
		QEMUSystemPath: "/usr/bin/qemu-system-x86_64",
	})
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Build.Security = &v1alpha1.BuildSecuritySpec{EnableKVM: true}

	if _, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: img, WorkspaceDir: workspace},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatQCOW2,
	}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	system := runner.commands[1]
	if !containsString(system.Args, "accel=kvm:tcg") {
		t.Fatalf("system args should enable kvm fallback acceleration: %#v", system.Args)
	}
}

func TestQEMUISOBackend_Build_WaitsForGuestReadinessWhenConfigured(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "installer.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	readiness := &recordingReadinessProbe{}
	provisioners := &recordingProvisionerRunner{}
	finalizer := &recordingFinalizer{}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:         runner,
		QMPClient:      &recordingQMPClient{},
		ReadinessProbe: readiness,
		Provisioners:   provisioners,
		HygieneChecker: &recordingHygieneChecker{},
		Finalizer:      finalizer,
		QEMUImgPath:    "/usr/bin/qemu-img",
		QEMUSystemPath: "/usr/bin/qemu-system-x86_64",
	})
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}}
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol:   "ssh",
		Host:       "127.0.0.1",
		HostPort:   2222,
		User:       "imagebuilder",
		SSHKeyPath: "/workspace/id_ed25519",
		GuestPort:  22,
	}

	if _, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: img, WorkspaceDir: workspace},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatQCOW2,
	}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(readiness.accesses) != 1 {
		t.Fatalf("readiness calls = %d, want 1", len(readiness.accesses))
	}
	got := readiness.accesses[0]
	if got.Protocol != "ssh" || got.Host != "127.0.0.1" || got.HostPort != 2222 || got.GuestPort != 22 ||
		got.User != "imagebuilder" || got.SSHKeyPath != "/workspace/id_ed25519" {
		t.Fatalf("guest access = %#v", got)
	}
	if len(provisioners.requests) != 1 {
		t.Fatalf("provisioner requests = %d, want 1", len(provisioners.requests))
	}
	if len(finalizer.requests) != 1 || finalizer.requests[0].GuestAccess.Protocol != "ssh" {
		t.Fatalf("finalizer requests = %#v", finalizer.requests)
	}
	if provisioners.requests[0].GuestAccess.HostPort != 2222 || len(provisioners.requests[0].Image.Spec.Provisioners) != 1 {
		t.Fatalf("provisioner request = %#v", provisioners.requests[0])
	}
	system := runner.commands[1]
	if !containsString(system.Args, "-netdev") ||
		!containsString(system.Args, "user,id=net0,hostfwd=tcp:127.0.0.1:2222-:22") ||
		!containsString(system.Args, "-device") ||
		!containsString(system.Args, "virtio-net-pci,netdev=net0") {
		t.Fatalf("system args should include qemu host forwarding: %#v", system.Args)
	}
}

func TestQEMUISOBackend_Build_GeneratesEphemeralSSHKeyAndCloudInitSeed(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := t.TempDir()
	sourcePath := filepath.Join(workspace, "installer.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	readiness := &recordingReadinessProbe{}
	provisioners := &recordingProvisionerRunner{}
	sanitizer := &recordingSanitizer{}
	finalizer := &recordingFinalizer{}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:          runner,
		QMPClient:       &recordingQMPClient{},
		ReadinessProbe:  readiness,
		Provisioners:    provisioners,
		Sanitizer:       sanitizer,
		HygieneChecker:  &recordingHygieneChecker{},
		Finalizer:       finalizer,
		QEMUImgPath:     "/usr/bin/qemu-img",
		QEMUSystemPath:  "/usr/bin/qemu-system-x86_64",
		GenISOImagePath: "/usr/bin/genisoimage",
	})
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}}
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "ssh",
		Host:     "127.0.0.1",
		HostPort: 2222,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{SSHKey: true},
		},
	}

	if _, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: img, WorkspaceDir: workspace, CredentialDir: credentialDir},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatQCOW2,
	}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	keyPath := filepath.Join(credentialDir, "guest-credentials", "id_ed25519")
	if len(readiness.accesses) != 1 || readiness.accesses[0].SSHKeyPath != keyPath ||
		readiness.accesses[0].User != "imagebuilder" {
		t.Fatalf("readiness access = %#v", readiness.accesses)
	}
	if len(provisioners.requests) != 1 || len(provisioners.requests[0].Image.Spec.Provisioners) != 1 ||
		provisioners.requests[0].Image.Spec.Provisioners[0].Type != "shell" {
		t.Fatalf("post-boot provisioners = %#v", provisioners.requests)
	}
	if len(sanitizer.calls) != 1 || sanitizer.calls[0].access.SSHKeyPath != keyPath {
		t.Fatalf("sanitizer calls = %#v", sanitizer.calls)
	}
	if len(finalizer.requests) != 1 || finalizer.requests[0].GuestAccess.SSHKeyPath != keyPath {
		t.Fatalf("finalizer requests = %#v", finalizer.requests)
	}
	system := runner.commands[2]
	if !containsString(system.Args, "-drive") ||
		!containsString(system.Args, fmt.Sprintf("file=%s,media=cdrom,readonly=on", filepath.Join(workspace, "cloud-init.iso"))) {
		t.Fatalf("system args should include cloud-init seed iso: %#v", system.Args)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("generated ssh key should be cleaned up, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "cloud-init")); !os.IsNotExist(err) {
		t.Fatalf("cloud-init credential seed should be cleaned up, stat err = %v", err)
	}
}

func TestQEMUISOBackend_Build_GeneratesWinRMPasswordAndAutounattendISO(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := t.TempDir()
	sourcePath := filepath.Join(workspace, "windows.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	readiness := &recordingReadinessProbe{}
	sanitizer := &recordingSanitizer{}
	finalizer := &recordingFinalizer{}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:          runner,
		QMPClient:       &recordingQMPClient{},
		ReadinessProbe:  readiness,
		Sanitizer:       sanitizer,
		HygieneChecker:  &recordingHygieneChecker{},
		Finalizer:       finalizer,
		QEMUImgPath:     "/usr/bin/qemu-img",
		QEMUSystemPath:  "/usr/bin/qemu-system-x86_64",
		GenISOImagePath: "/usr/bin/genisoimage",
	})
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.OS.Family = "windows"
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "winrm",
		Host:     "127.0.0.1",
		HostPort: 55986,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{Password: true, PasswordLength: 24},
		},
	}

	if _, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: img, WorkspaceDir: workspace, CredentialDir: credentialDir},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatQCOW2,
	}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	passwordPath := filepath.Join(credentialDir, "guest-credentials", "password")
	if len(readiness.accesses) != 1 || readiness.accesses[0].PasswordPath != passwordPath ||
		readiness.accesses[0].User != "Administrator" || !readiness.accesses[0].WinRMHTTPS {
		t.Fatalf("readiness access = %#v", readiness.accesses)
	}
	if len(sanitizer.calls) != 1 || sanitizer.calls[0].access.PasswordPath != passwordPath {
		t.Fatalf("sanitizer calls = %#v", sanitizer.calls)
	}
	if len(finalizer.requests) != 1 || finalizer.requests[0].GuestAccess.PasswordPath != passwordPath {
		t.Fatalf("finalizer requests = %#v", finalizer.requests)
	}
	system := runner.commands[2]
	if !containsString(system.Args, fmt.Sprintf("file=%s,media=cdrom,readonly=on", filepath.Join(workspace, "autounattend.iso"))) {
		t.Fatalf("system args should include autounattend iso: %#v", system.Args)
	}
	if _, err := os.Stat(passwordPath); !os.IsNotExist(err) {
		t.Fatalf("generated password should be cleaned up, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "autounattend")); !os.IsNotExist(err) {
		t.Fatalf("autounattend credential material should be cleaned up, stat err = %v", err)
	}
}

func TestQEMUISOBackend_Build_AttachesInstallerMediaISOs(t *testing.T) {
	tests := []struct {
		name      string
		source    v1alpha1.SourceSpec
		wantISO   string
		wantVolID string
		wantFile  string
	}{
		{
			name: "nocloud",
			source: v1alpha1.SourceSpec{
				Type: "iso",
				Installer: &v1alpha1.InstallerMediaSpec{
					Type:          "nocloud",
					UserData:      "#cloud-config\npackage_update: true\n",
					NetworkConfig: "version: 2\n",
				},
			},
			wantISO:   "cloud-init.iso",
			wantVolID: "cidata",
			wantFile:  filepath.Join("cloud-init", "network-config"),
		},
		{
			name: "kickstart",
			source: v1alpha1.SourceSpec{
				Type:      "iso",
				Installer: &v1alpha1.InstallerMediaSpec{Type: "kickstart", Kickstart: "text\nreboot\n"},
			},
			wantISO:   "kickstart.iso",
			wantVolID: "OEMDRV",
			wantFile:  filepath.Join("kickstart", "ks.cfg"),
		},
		{
			name: "preseed",
			source: v1alpha1.SourceSpec{
				Type:      "iso",
				Installer: &v1alpha1.InstallerMediaSpec{Type: "preseed", Preseed: "d-i passwd/root-login boolean false\n"},
			},
			wantISO:   "preseed.iso",
			wantVolID: "PRESEED",
			wantFile:  filepath.Join("preseed", "preseed.cfg"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			sourcePath := filepath.Join(workspace, "installer.iso")
			if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
				t.Fatalf("write iso: %v", err)
			}
			runner := &recordingRunner{writeOutput: []byte("artifact")}
			backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
				Runner:          runner,
				QMPClient:       &recordingQMPClient{},
				ReadinessProbe:  &recordingReadinessProbe{},
				QEMUImgPath:     "/usr/bin/qemu-img",
				QEMUSystemPath:  "/usr/bin/qemu-system-x86_64",
				GenISOImagePath: "/usr/bin/genisoimage",
			})

			if _, err := backend.Build(context.Background(), builder.BackendRequest{
				BuildRequest: builder.BuildRequest{Image: testImage(tt.source, "qcow2"), WorkspaceDir: workspace},
				Source:       &builder.SourceArtifact{Path: sourcePath},
				Format:       platform.FormatQCOW2,
			}); err != nil {
				t.Fatalf("Build returned error: %v", err)
			}

			if !commandWithArgs(runner.commands, "/usr/bin/genisoimage", "-volid", tt.wantVolID, filepath.Join(workspace, tt.wantFile)) {
				t.Fatalf("genisoimage command for %s not found in %#v", tt.wantVolID, runner.commands)
			}
			system := runner.commands[len(runner.commands)-2]
			if !containsString(system.Args, fmt.Sprintf("file=%s,media=cdrom,readonly=on", filepath.Join(workspace, tt.wantISO))) {
				t.Fatalf("system args should include %s: %#v", tt.wantISO, system.Args)
			}
		})
	}
}

func TestQEMUISOBackend_Build_SanitizesGeneratedCredentialsBeforeSysprep(t *testing.T) {
	workspace := t.TempDir()
	credentialDir := t.TempDir()
	sourcePath := filepath.Join(workspace, "windows.iso")
	if err := os.WriteFile(sourcePath, []byte("iso"), 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("artifact")}
	provisioners := &recordingProvisionerRunner{}
	sanitizer := &recordingSanitizer{}
	finalizer := &recordingFinalizer{}
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{
		Runner:          runner,
		QMPClient:       &recordingQMPClient{},
		ReadinessProbe:  &recordingReadinessProbe{},
		Provisioners:    provisioners,
		Sanitizer:       sanitizer,
		HygieneChecker:  &recordingHygieneChecker{},
		Finalizer:       finalizer,
		QEMUImgPath:     "/usr/bin/qemu-img",
		QEMUSystemPath:  "/usr/bin/qemu-system-x86_64",
		GenISOImagePath: "/usr/bin/genisoimage",
	})
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.OS.Family = "windows"
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "powershell", Inline: "Write-Host ok"},
		{Type: "sysprep"},
	}
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "winrm",
		Host:     "127.0.0.1",
		HostPort: 55986,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{Password: true},
		},
	}

	if _, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: img, WorkspaceDir: workspace, CredentialDir: credentialDir},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatQCOW2,
	}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(provisioners.requests) != 2 {
		t.Fatalf("provisioner requests = %d, want pre-sysprep and sysprep", len(provisioners.requests))
	}
	if got := provisioners.requests[0].Image.Spec.Provisioners; len(got) != 1 || got[0].Type != "powershell" {
		t.Fatalf("first provisioner batch = %#v", got)
	}
	if got := provisioners.requests[1].Image.Spec.Provisioners; len(got) != 1 || got[0].Type != "sysprep" {
		t.Fatalf("second provisioner batch = %#v", got)
	}
	if len(sanitizer.calls) != 1 {
		t.Fatalf("sanitizer calls = %#v", sanitizer.calls)
	}
	if len(finalizer.requests) != 1 || !finalizer.requests[0].SysprepShutdown {
		t.Fatalf("finalizer requests = %#v", finalizer.requests)
	}
}

func TestQEMUISOBackend_SupportsISOConvertibleFormats(t *testing.T) {
	backend := builder.NewQEMUISOBackend(builder.QEMUISOBackendOptions{})
	if !backend.Supports(builder.BuildRequest{Image: testImage(v1alpha1.SourceSpec{Type: "iso"}, "raw")}) {
		t.Fatal("backend should support iso to raw")
	}
	arm := testImage(v1alpha1.SourceSpec{Type: "iso"}, "raw")
	arm.Spec.OS.Arch = "arm64"
	if !backend.Supports(builder.BuildRequest{Image: arm}) {
		t.Fatal("backend should support arm64 iso to raw")
	}
	if backend.Supports(builder.BuildRequest{Image: testImage(v1alpha1.SourceSpec{Type: "cloud-image"}, "raw")}) {
		t.Fatal("backend should not support cloud-image")
	}
	if backend.Supports(builder.BuildRequest{Image: testImage(v1alpha1.SourceSpec{Type: "iso"}, "ami")}) {
		t.Fatal("backend should not support ami output")
	}
	unsupportedArch := testImage(v1alpha1.SourceSpec{Type: "iso"}, "raw")
	unsupportedArch.Spec.OS.Arch = "s390x"
	if backend.Supports(builder.BuildRequest{Image: unsupportedArch}) {
		t.Fatal("backend should not support unknown arch")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsArgWithPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func commandWithArgs(commands []builder.Command, name string, args ...string) bool {
	for _, cmd := range commands {
		if cmd.Name != name {
			continue
		}
		matched := true
		for _, arg := range args {
			if !containsString(cmd.Args, arg) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

type recordingQMPClient struct {
	commands []string
	err      error
}

func (c *recordingQMPClient) ExecuteHumanMonitorCommand(_ context.Context, _ string, command string) error {
	c.commands = append(c.commands, command)
	return c.err
}

type recordingReadinessProbe struct {
	accesses []builder.GuestAccess
	err      error
}

func (p *recordingReadinessProbe) Wait(_ context.Context, access builder.GuestAccess) error {
	p.accesses = append(p.accesses, access)
	return p.err
}

type recordingProvisionerRunner struct {
	requests []builder.ProvisioningRequest
	err      error
}

func (r *recordingProvisionerRunner) Run(_ context.Context, req builder.ProvisioningRequest) error {
	r.requests = append(r.requests, req)
	return r.err
}

type recordingSanitizer struct {
	calls []sanitizeCall
	err   error
}

type sanitizeCall struct {
	access       builder.GuestAccess
	workspaceDir string
}

func (s *recordingSanitizer) Sanitize(_ context.Context, access builder.GuestAccess, _ builder.GeneratedGuestCredentials, workspaceDir string) error {
	s.calls = append(s.calls, sanitizeCall{access: access, workspaceDir: workspaceDir})
	return s.err
}

type recordingHygieneChecker struct {
	requests []builder.GuestHygieneRequest
	err      error
}

func (c *recordingHygieneChecker) Check(_ context.Context, req builder.GuestHygieneRequest) error {
	c.requests = append(c.requests, req)
	return c.err
}

type recordingFinalizer struct {
	requests []builder.GuestFinalizationRequest
	err      error
}

func (f *recordingFinalizer) Finalize(_ context.Context, req builder.GuestFinalizationRequest) error {
	f.requests = append(f.requests, req)
	return f.err
}
