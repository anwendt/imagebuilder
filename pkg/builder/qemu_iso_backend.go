package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	provisionersource "github.com/anwendt/imagebuilder/pkg/provisioner/source"
)

const (
	defaultQEMUSystemPathAMD64 = "/usr/bin/qemu-system-x86_64"
	defaultQEMUSystemPathARM64 = "/usr/bin/qemu-system-aarch64"
	defaultISODiskSize         = "20G"
	defaultGenISOImage         = "/usr/bin/genisoimage"
)

type QEMUISOBackendOptions struct {
	Runner              CommandRunner
	QMPClient           QMPClient
	ReadinessProbe      GuestReadinessProbe
	Provisioners        ProvisionerRunner
	Sanitizer           GuestCredentialSanitizer
	HygieneChecker      GuestHygieneChecker
	Finalizer           GuestFinalizer
	QEMUImgPath         string
	QEMUSystemPath      string
	QEMUSystemPathARM64 string
	ARM64EFICodePath    string
	GenISOImagePath     string
	DiskSize            string
}

type QEMUISOBackend struct {
	runner              ManagedCommandRunner
	qmpClient           QMPClient
	readinessProbe      GuestReadinessProbe
	provisioners        ProvisionerRunner
	sanitizer           GuestCredentialSanitizer
	hygieneChecker      GuestHygieneChecker
	finalizer           GuestFinalizer
	qemuImgPath         string
	qemuSystemPath      string
	qemuSystemPathARM64 string
	arm64EFICodePath    string
	genISOImagePath     string
	diskSize            string
}

func NewQEMUISOBackend(opts QEMUISOBackendOptions) *QEMUISOBackend {
	runner := opts.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	managedRunner, ok := runner.(ManagedCommandRunner)
	if !ok {
		managedRunner = startUnsupportedRunner{CommandRunner: runner}
	}
	qmpClient := opts.QMPClient
	if qmpClient == nil {
		qmpClient = QMPUnixClient{}
	}
	readinessProbe := opts.ReadinessProbe
	if readinessProbe == nil {
		readinessProbe = NetworkGuestReadinessProbe{}
	}
	provisioners := opts.Provisioners
	if provisioners == nil {
		runner := NewSequentialProvisionerRunner()
		provisioners = runner
	}
	sanitizer := opts.Sanitizer
	if sanitizer == nil {
		sanitizer = RemoteGuestCredentialSanitizer{}
	}
	hygieneChecker := opts.HygieneChecker
	if hygieneChecker == nil {
		hygieneChecker = RemoteGuestHygieneChecker{}
	}
	finalizer := opts.Finalizer
	if finalizer == nil {
		finalizer = RemoteGuestFinalizer{}
	}
	qemuImgPath := opts.QEMUImgPath
	if qemuImgPath == "" {
		qemuImgPath = defaultQEMUImgPath
	}
	qemuSystemPath := opts.QEMUSystemPath
	if qemuSystemPath == "" {
		qemuSystemPath = defaultQEMUSystemPathAMD64
	}
	qemuSystemPathARM64 := opts.QEMUSystemPathARM64
	if qemuSystemPathARM64 == "" {
		qemuSystemPathARM64 = defaultQEMUSystemPathARM64
	}
	genISOImagePath := opts.GenISOImagePath
	if genISOImagePath == "" {
		genISOImagePath = defaultGenISOImage
	}
	diskSize := opts.DiskSize
	if diskSize == "" {
		diskSize = defaultISODiskSize
	}
	return &QEMUISOBackend{
		runner:              managedRunner,
		qmpClient:           qmpClient,
		readinessProbe:      readinessProbe,
		provisioners:        provisioners,
		sanitizer:           sanitizer,
		hygieneChecker:      hygieneChecker,
		finalizer:           finalizer,
		qemuImgPath:         qemuImgPath,
		qemuSystemPath:      qemuSystemPath,
		qemuSystemPathARM64: qemuSystemPathARM64,
		arm64EFICodePath:    opts.ARM64EFICodePath,
		genISOImagePath:     genISOImagePath,
		diskSize:            diskSize,
	}
}

func (b *QEMUISOBackend) Name() string { return "qemu-iso" }

func (b *QEMUISOBackend) Supports(req BuildRequest) bool {
	if req.Image == nil || strings.ToLower(req.Image.Spec.Source.Type) != "iso" {
		return false
	}
	if !localBackendSupportsArch(req.Image.Spec.OS.Arch) {
		return false
	}
	if len(req.Image.Spec.Targets) == 0 {
		return false
	}
	_, ok := qemuOutputFormat(platform.ImageFormat(req.Image.Spec.Targets[0].Format))
	return ok
}

func (b *QEMUISOBackend) Build(ctx context.Context, req BackendRequest) (*platform.BuildArtifact, error) {
	if req.Source == nil {
		return nil, fmt.Errorf("source artifact is required")
	}
	qemuArch, err := qemuArchConfig(req.Image.Spec.OS.Arch, b.qemuSystemPath, b.qemuSystemPathARM64, b.arm64EFICodePath)
	if err != nil {
		return nil, err
	}
	qemuFormat, ok := qemuOutputFormat(req.Format)
	if !ok {
		return nil, fmt.Errorf("qemu iso backend does not support output format %q", req.Format)
	}
	workspaceDir, err := cleanWorkspace(req.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	diskPath := filepath.Join(workspaceDir, "install.qcow2")
	artifactPath := filepath.Join(workspaceDir, fmt.Sprintf("%s.%s", defaultArtifactFileName, req.Format))
	conversionPath := artifactPath
	if req.Format == platform.FormatGCETarball {
		conversionPath = filepath.Join(workspaceDir, defaultArtifactFileName+".raw")
	}
	qmpPath := filepath.Join(workspaceDir, "qmp.sock")

	if err := prepareInstallerMedia(ctx, req.Image, workspaceDir); err != nil {
		return nil, err
	}
	generatedCreds, err := prepareGuestCredentials(ctx, req.Image, workspaceDir, req.CredentialDir)
	if err != nil {
		return nil, err
	}
	postBootProvisioners, err := b.runPrebootProvisioners(ctx, req.Image, workspaceDir, guestAccessPlaceholder(req.Image))
	if err != nil {
		return nil, err
	}
	installDataISOs, err := b.createInstallerDataISOs(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}
	defer cleanupQEMUISOTransientWorkspace(workspaceDir, req.Source, diskPath, qmpPath, installDataISOs)
	if generatedCreds.Dir != "" {
		defer cleanupGeneratedGuestCredentials(workspaceDir, generatedCreds, installDataISOs)
	}

	events, err := ParseBootCommand(req.Image.Spec.Source.BootCommand)
	if err != nil {
		return nil, err
	}
	guestAccess, waitForGuest, err := GuestAccessFromSpec(req.Image.Spec.Build.GuestAccess)
	if err != nil {
		return nil, err
	}
	if !waitForGuest && provisionersRequireGuestAccess(req.Image.Spec.Provisioners) {
		return nil, fmt.Errorf("spec.build.guestAccess is required when running provisioners during an iso build")
	}
	if err := b.runner.Run(ctx, Command{
		Name: b.qemuImgPath,
		Args: []string{"create", "-f", "qcow2", diskPath, b.diskSize},
		Dir:  workspaceDir,
	}); err != nil {
		_ = os.Remove(diskPath)
		return nil, fmt.Errorf("qemu-img create: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, buildTimeout(req.BuildRequest))
	defer cancel()
	process, err := b.runner.Start(runCtx, Command{
		Name: qemuArch.SystemPath,
		Args: qemuSystemArgs(qemuArch, req.Source.Path, diskPath, qmpPath, installDataISOs, guestAccess, waitForGuest, qemuKVMEnabled(req.Image)),
		Dir:  workspaceDir,
	})
	if err != nil {
		_ = os.Remove(diskPath)
		return nil, Classify(ReasonBootFailed, fmt.Errorf("start qemu-system boot iso: %w", err))
	}
	if err := b.sendBootEvents(runCtx, qmpPath, events); err != nil {
		stopProcess(process)
		_ = os.Remove(diskPath)
		return nil, Classify(ReasonBootFailed, fmt.Errorf("send iso boot commands: %w", err))
	}
	if waitForGuest {
		if err := b.readinessProbe.Wait(runCtx, guestAccess); err != nil {
			stopProcess(process)
			_ = os.Remove(diskPath)
			return nil, Classify(ReasonGuestReadinessTimeout, fmt.Errorf("guest readiness: %w", err))
		}
	}
	if len(postBootProvisioners) > 0 {
		if preSysprep := provisionersBeforeSysprep(postBootProvisioners); len(preSysprep) > 0 {
			postBootImage := req.Image.DeepCopy()
			postBootImage.Spec.Provisioners = preSysprep
			if err := b.provisioners.Run(runCtx, ProvisioningRequest{
				Image:        postBootImage,
				WorkspaceDir: workspaceDir,
				GuestAccess:  guestAccess,
			}); err != nil {
				stopProcess(process)
				_ = os.Remove(diskPath)
				return nil, Classify(ReasonProvisionerFailed, fmt.Errorf("provision guest: %w", err))
			}
		}
	}
	if err := b.sanitizeGeneratedGuestCredentials(runCtx, guestAccess, generatedCreds, workspaceDir); err != nil {
		stopProcess(process)
		_ = os.Remove(diskPath)
		return nil, err
	}
	if waitForGuest {
		if err := b.checkFinalImageHygiene(runCtx, req.Image, guestAccess, workspaceDir, generatedCreds); err != nil {
			stopProcess(process)
			_ = os.Remove(diskPath)
			return nil, err
		}
	}
	sysprepShutdown := false
	if sysprep, ok := sysprepProvisioner(postBootProvisioners); ok {
		sysprepShutdown = sysprepRequestsShutdown(sysprep)
		sysprepImage := req.Image.DeepCopy()
		sysprepImage.Spec.Provisioners = []v1alpha1.ProvisionerSpec{sysprep}
		if err := b.provisioners.Run(runCtx, ProvisioningRequest{
			Image:        sysprepImage,
			WorkspaceDir: workspaceDir,
			GuestAccess:  guestAccess,
		}); err != nil {
			stopProcess(process)
			_ = os.Remove(diskPath)
			return nil, Classify(ReasonProvisionerFailed, fmt.Errorf("provision sysprep: %w", err))
		}
	}
	needsFinalization := len(postBootProvisioners) > 0 || generatedCreds.Dir != ""
	if needsFinalization {
		if err := b.finalizeGuest(runCtx, req.Image, guestAccess, workspaceDir, qmpPath, waitForGuest, sysprepShutdown); err != nil {
			stopProcess(process)
			_ = os.Remove(diskPath)
			return nil, err
		}
	}
	if err := process.Wait(); err != nil {
		_ = os.Remove(diskPath)
		return nil, Classify(ReasonBootFailed, fmt.Errorf("qemu-system boot iso: %w", err))
	}
	if err := b.runner.Run(ctx, Command{
		Name: b.qemuImgPath,
		Args: []string{"convert", "-p", "-O", qemuFormat, diskPath, conversionPath},
		Dir:  workspaceDir,
	}); err != nil {
		_ = os.Remove(artifactPath)
		return nil, Classify(ReasonArtifactConvertFailed, fmt.Errorf("qemu-img convert installed disk: %w", err))
	}
	if req.Format == platform.FormatGCETarball {
		defer os.Remove(conversionPath)
		if err := createGCEArchive(ctx, b.runner, conversionPath, artifactPath); err != nil {
			return nil, Classify(ReasonArtifactConvertFailed, err)
		}
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("stat iso artifact: %w", err)
	}
	checksum, err := fileChecksum(artifactPath, "sha256")
	if err != nil {
		return nil, fmt.Errorf("checksum iso artifact: %w", err)
	}
	return &platform.BuildArtifact{
		Path:      artifactPath,
		Format:    req.Format,
		Checksum:  checksum,
		SizeBytes: info.Size(),
		OS:        platform.OSFamily(req.Image.Spec.OS.Family),
		Metadata:  buildMetadata(req.BuildRequest, b.Name()),
	}, nil
}

func (b *QEMUISOBackend) sendBootEvents(ctx context.Context, qmpPath string, events []BootEvent) error {
	for _, event := range events {
		switch event.Type {
		case BootEventWait:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(event.Wait):
			}
		case BootEventKey, BootEventText:
			for _, command := range BootEventsToQEMUMonitorScript([]BootEvent{event}) {
				if err := b.qmpClient.ExecuteHumanMonitorCommand(ctx, qmpPath, command); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported boot event type %q", event.Type)
		}
	}
	return nil
}

func (b *QEMUISOBackend) sanitizeGeneratedGuestCredentials(ctx context.Context, access GuestAccess, creds GeneratedGuestCredentials, workspaceDir string) error {
	if creds.Dir == "" {
		return nil
	}
	if err := b.sanitizer.Sanitize(ctx, access, creds, workspaceDir); err != nil {
		return err
	}
	return nil
}

func (b *QEMUISOBackend) checkFinalImageHygiene(ctx context.Context, img *v1alpha1.VMImage, access GuestAccess, workspaceDir string, creds GeneratedGuestCredentials) error {
	if b.hygieneChecker == nil {
		return nil
	}
	if err := b.hygieneChecker.Check(ctx, GuestHygieneRequest{
		OSFamily:        img.Spec.OS.Family,
		GuestAccess:     access,
		WorkspaceDir:    workspaceDir,
		GeneratedUser:   access.User,
		GeneratedSSHKey: creds.PublicKey != "",
		GeneratedPass:   creds.Password != "",
	}); err != nil {
		return Classify(ReasonFinalHygieneFailed, fmt.Errorf("final image hygiene: %w", err))
	}
	return nil
}

func (b *QEMUISOBackend) finalizeGuest(ctx context.Context, img *v1alpha1.VMImage, access GuestAccess, workspaceDir, qmpPath string, hasGuestAccess, sysprepShutdown bool) error {
	if hasGuestAccess {
		if err := b.finalizer.Finalize(ctx, GuestFinalizationRequest{
			OSFamily:        img.Spec.OS.Family,
			GuestAccess:     access,
			WorkspaceDir:    workspaceDir,
			SysprepShutdown: sysprepShutdown,
		}); err != nil {
			return fmt.Errorf("finalize guest shutdown: %w", err)
		}
		return nil
	}
	if err := b.qmpClient.ExecuteHumanMonitorCommand(ctx, qmpPath, "system_powerdown"); err != nil {
		return fmt.Errorf("finalize guest qmp shutdown: %w", err)
	}
	return nil
}

func (b *QEMUISOBackend) runPrebootProvisioners(ctx context.Context, img *v1alpha1.VMImage, workspaceDir string, access GuestAccess) ([]v1alpha1.ProvisionerSpec, error) {
	var postBoot []v1alpha1.ProvisionerSpec
	provisioners, err := provisionersource.ExpandProvisioners(ctx, workspaceDir, img.Spec.Provisioners)
	if err != nil {
		return nil, err
	}
	for _, spec := range provisioners {
		if spec.Type != "cloud-init" {
			postBoot = append(postBoot, spec)
			continue
		}
		p, ok := provisioner.GetInProcess(spec.Type)
		if !ok {
			return nil, fmt.Errorf("preboot provisioner %q is not available in the builder runtime", spec.Type)
		}
		if err := p.Validate(ctx, spec); err != nil {
			return nil, fmt.Errorf("validate preboot provisioner %q: %w", spec.Type, err)
		}
		if _, err := p.Run(ctx, &provisioner.RunRequest{
			WorkspaceDir:   workspaceDir,
			VMAddress:      access.Host,
			VMUser:         access.User,
			Protocol:       access.Protocol,
			SSHPort:        int(access.HostPort),
			SSHKeyPath:     access.SSHKeyPath,
			VMPasswordPath: access.PasswordPath,
			OS:             img.Spec.OS.Family,
			Spec:           spec,
		}); err != nil {
			return nil, fmt.Errorf("run preboot provisioner %q: %w", spec.Type, err)
		}
	}
	return postBoot, nil
}

func (b *QEMUISOBackend) createInstallerDataISOs(ctx context.Context, workspaceDir string) ([]string, error) {
	var isos []string
	cloudInitISO, err := b.createCloudInitSeedISO(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}
	if cloudInitISO != "" {
		isos = append(isos, cloudInitISO)
	}
	autounattendISO, err := b.createAutounattendISO(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}
	if autounattendISO != "" {
		isos = append(isos, autounattendISO)
	}
	kickstartISO, err := b.createKickstartISO(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}
	if kickstartISO != "" {
		isos = append(isos, kickstartISO)
	}
	preseedISO, err := b.createPreseedISO(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}
	if preseedISO != "" {
		isos = append(isos, preseedISO)
	}
	return isos, nil
}

func (b *QEMUISOBackend) createCloudInitSeedISO(ctx context.Context, workspaceDir string) (string, error) {
	seedDir := filepath.Join(workspaceDir, "cloud-init")
	userData := filepath.Join(seedDir, "user-data")
	metaData := filepath.Join(seedDir, "meta-data")
	if _, err := os.Stat(userData); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat cloud-init user-data: %w", err)
	}
	if _, err := os.Stat(metaData); err != nil {
		return "", fmt.Errorf("stat cloud-init meta-data: %w", err)
	}
	files := []string{userData, metaData}
	networkConfig := filepath.Join(seedDir, cloudInitNetworkName)
	if _, err := os.Stat(networkConfig); err == nil {
		files = append(files, networkConfig)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat cloud-init network-config: %w", err)
	}
	seedISO := filepath.Join(workspaceDir, "cloud-init.iso")
	if err := b.createISO(ctx, workspaceDir, seedISO, "cidata", files...); err != nil {
		return "", fmt.Errorf("create cloud-init seed iso: %w", err)
	}
	return seedISO, nil
}

func (b *QEMUISOBackend) createAutounattendISO(ctx context.Context, workspaceDir string) (string, error) {
	path := filepath.Join(workspaceDir, "autounattend", "Autounattend.xml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat autounattend file: %w", err)
	}
	isoPath := filepath.Join(workspaceDir, "autounattend.iso")
	if err := b.createISO(ctx, workspaceDir, isoPath, "AUTOUNATTEND", path); err != nil {
		return "", fmt.Errorf("create autounattend iso: %w", err)
	}
	return isoPath, nil
}

func (b *QEMUISOBackend) createKickstartISO(ctx context.Context, workspaceDir string) (string, error) {
	path := filepath.Join(workspaceDir, kickstartSeedDirName, kickstartConfigName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat kickstart file: %w", err)
	}
	isoPath := filepath.Join(workspaceDir, "kickstart.iso")
	if err := b.createISO(ctx, workspaceDir, isoPath, "OEMDRV", path); err != nil {
		return "", fmt.Errorf("create kickstart iso: %w", err)
	}
	return isoPath, nil
}

func (b *QEMUISOBackend) createPreseedISO(ctx context.Context, workspaceDir string) (string, error) {
	path := filepath.Join(workspaceDir, preseedSeedDirName, preseedConfigName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat preseed file: %w", err)
	}
	isoPath := filepath.Join(workspaceDir, "preseed.iso")
	if err := b.createISO(ctx, workspaceDir, isoPath, "PRESEED", path); err != nil {
		return "", fmt.Errorf("create preseed iso: %w", err)
	}
	return isoPath, nil
}

func (b *QEMUISOBackend) createISO(ctx context.Context, workspaceDir, outputPath, volumeID string, files ...string) error {
	args := []string{
		"-quiet",
		"-output", outputPath,
		"-volid", volumeID,
		"-joliet",
		"-rock",
	}
	args = append(args, files...)
	return b.runner.Run(ctx, Command{Name: b.genISOImagePath, Args: args, Dir: workspaceDir})
}

func cleanupQEMUISOTransientWorkspace(workspaceDir string, source *SourceArtifact, diskPath, qmpPath string, installDataISOs []string) {
	for _, path := range append([]string{diskPath, qmpPath}, installDataISOs...) {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	if source != nil && source.Path != "" && filepath.Dir(source.Path) == workspaceDir && filepath.Base(source.Path) == defaultSourceFileName {
		_ = os.Remove(source.Path)
	}
	for _, dir := range []string{
		cloudInitSeedDirName,
		autounattendSeedDirName,
		kickstartSeedDirName,
		preseedSeedDirName,
	} {
		_ = os.RemoveAll(filepath.Join(workspaceDir, dir))
	}
}

func guestAccessPlaceholder(img *v1alpha1.VMImage) GuestAccess {
	if img == nil || img.Spec.Build.GuestAccess == nil {
		return GuestAccess{}
	}
	access, _, err := GuestAccessFromSpec(img.Spec.Build.GuestAccess)
	if err != nil {
		return GuestAccess{}
	}
	return access
}

func provisionersBeforeSysprep(specs []v1alpha1.ProvisionerSpec) []v1alpha1.ProvisionerSpec {
	filtered := make([]v1alpha1.ProvisionerSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Type != "sysprep" {
			filtered = append(filtered, spec)
		}
	}
	return filtered
}

func sysprepProvisioner(specs []v1alpha1.ProvisionerSpec) (v1alpha1.ProvisionerSpec, bool) {
	for _, spec := range specs {
		if spec.Type == "sysprep" {
			return spec, true
		}
	}
	return v1alpha1.ProvisionerSpec{}, false
}

func sysprepRequestsShutdown(spec v1alpha1.ProvisionerSpec) bool {
	if spec.WindowsConfig != nil && spec.WindowsConfig.Sysprep != nil {
		return spec.WindowsConfig.Sysprep.Shutdown
	}
	return true
}

type qemuArchSpec struct {
	Arch       string
	SystemPath string
	Machine    string
	CPUArgs    []string
	DiskArgs   []string
	NetDevice  string
	BIOSPath   string
}

func qemuArchConfig(arch, amd64SystemPath, arm64SystemPath, arm64EFICodePath string) (qemuArchSpec, error) {
	switch strings.ToLower(arch) {
	case "", "amd64":
		return qemuArchSpec{
			Arch:       "amd64",
			SystemPath: amd64SystemPath,
			Machine:    "pc",
			DiskArgs:   []string{"-drive"},
			NetDevice:  "virtio-net-pci",
		}, nil
	case "arm64":
		return qemuArchSpec{
			Arch:       "arm64",
			SystemPath: arm64SystemPath,
			Machine:    "virt",
			CPUArgs:    []string{"-cpu", "max"},
			DiskArgs:   []string{"-drive"},
			NetDevice:  "virtio-net-device",
			BIOSPath:   arm64EFICodePath,
		}, nil
	default:
		return qemuArchSpec{}, fmt.Errorf("qemu iso backend does not support os arch %q", arch)
	}
}

func qemuSystemArgs(arch qemuArchSpec, isoPath, diskPath, qmpPath string, installDataISOs []string, guestAccess GuestAccess, exposeGuest, enableKVM bool) []string {
	args := []string{
		"-display", "none",
		"-machine", qemuMachineArg(arch, enableKVM),
		"-m", "2048",
	}
	args = append(args, arch.CPUArgs...)
	if arch.BIOSPath != "" {
		args = append(args, "-bios", arch.BIOSPath)
	}
	args = append(args, arch.DiskArgs...)
	args = append(args,
		fmt.Sprintf("file=%s,if=virtio,format=qcow2", diskPath),
		"-cdrom", isoPath,
		"-boot", "d",
		"-qmp", "unix:"+qmpPath+",server,nowait",
		"-monitor", "none",
		"-no-reboot",
	)
	for _, isoPath := range installDataISOs {
		args = append(args, "-drive", fmt.Sprintf("file=%s,media=cdrom,readonly=on", isoPath))
	}
	if exposeGuest {
		args = append(args,
			"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp:%s:%d-:%d", guestAccess.Host, guestAccess.HostPort, guestAccess.GuestPort),
			"-device", arch.NetDevice+",netdev=net0",
		)
	}
	return args
}

func qemuKVMEnabled(img *v1alpha1.VMImage) bool {
	return img != nil && img.Spec.Build.Security != nil && img.Spec.Build.Security.EnableKVM
}

func qemuMachineAcceleration(enableKVM bool) string {
	if enableKVM {
		return "accel=kvm:tcg"
	}
	return "accel=tcg"
}

func qemuMachineArg(arch qemuArchSpec, enableKVM bool) string {
	if arch.Machine == "" || arch.Machine == "pc" {
		return qemuMachineAcceleration(enableKVM)
	}
	return arch.Machine + "," + qemuMachineAcceleration(enableKVM)
}

func buildTimeout(req BuildRequest) time.Duration {
	if req.Image != nil && req.Image.Spec.Build.Timeout != nil {
		return req.Image.Spec.Build.Timeout.Duration
	}
	return 2 * time.Hour
}

func stopProcess(process CommandProcess) {
	if process == nil {
		return
	}
	_ = process.Kill()
	_ = process.Wait()
}

type startUnsupportedRunner struct {
	CommandRunner
}

func (r startUnsupportedRunner) Start(context.Context, Command) (CommandProcess, error) {
	return nil, fmt.Errorf("command runner does not support starting long-running processes")
}
