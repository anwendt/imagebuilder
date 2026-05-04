package vsphere

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/guest"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	provisionersource "github.com/anwendt/imagebuilder/pkg/provisioner/source"
)

type vsphereRemoteBuildInput struct {
	BuildID            string
	OperationRef       string
	ImageName          string
	SourceType         string
	SourceRef          string
	SourceChecksum     string
	OSFamily           platform.OSFamily
	Format             platform.ImageFormat
	Tags               map[string]string
	ProviderConfigName string
	Provisioners       []v1alpha1.ProvisionerSpec
	GuestAccess        *v1alpha1.GuestAccessSpec
}

type vsphereRemoteBuildState struct {
	OperationRef string
	Phase        platform.RemoteBuildPhase
	Message      string
	Done         bool
	Image        *platform.ImageRef
	Hygiene      *platform.RemoteHygieneResult
}

func (c *govmomiClient) ReconcileRemoteBuild(ctx context.Context, input vsphereRemoteBuildInput) (*vsphereRemoteBuildState, error) {
	expandedInput, cleanup, err := expandVSphereRemoteProvisioners(ctx, input)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	input = expandedInput
	if input.SourceRef == "" {
		return nil, fmt.Errorf("vsphere remote build source providerRef is required")
	}
	if err := validateVSphereRemoteProvisioners(input); err != nil {
		return nil, err
	}
	if err := c.validateVSphereRemoteNetwork(input); err != nil {
		return nil, err
	}
	ref, err := parseVSphereRemoteOperationRef(input.OperationRef)
	if err != nil {
		return nil, err
	}
	if ref.ImageRef != "" {
		return &vsphereRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "vSphere remote image is ready",
			Done:         true,
			Image:        vsphereRemoteImage(input, c.cfg.datacenter, ref.ImageRef),
			Hygiene:      vsphereRemoteHygiene(input, ref.ImageRef),
		}, nil
	}
	if ref.VMRef == "" {
		return c.cloneRemoteSource(ctx, input)
	}
	vm := object.NewVirtualMachine(c.vc.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: ref.VMRef})
	if ref.ProvisionerIndex < len(input.Provisioners) {
		if err := c.ensureRemoteVMReady(ctx, vm); err != nil {
			return nil, err
		}
		if err := c.runVSphereProvisioner(ctx, vm, input, ref, input.Provisioners[ref.ProvisionerIndex]); err != nil {
			_ = c.cleanupVSphereRemoteVM(ctx, ref)
			return nil, err
		}
		ref.ProvisionerIndex++
		return &vsphereRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      fmt.Sprintf("vSphere guest provisioner %d completed", ref.ProvisionerIndex-1),
		}, nil
	}
	return c.finishRemoteVM(ctx, vm, input, ref)
}

func expandVSphereRemoteProvisioners(ctx context.Context, input vsphereRemoteBuildInput) (vsphereRemoteBuildInput, func(), error) {
	if !provisionersource.HasSources(input.Provisioners) {
		return input, func() {}, nil
	}
	workspace, err := os.MkdirTemp("", "imagebuilder-vsphere-provisioners-*")
	if err != nil {
		return input, func() {}, fmt.Errorf("create vSphere remote provisioner source workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	provisioners, err := provisionersource.ExpandProvisioners(ctx, workspace, input.Provisioners)
	if err != nil {
		cleanup()
		return input, func() {}, err
	}
	input.Provisioners = provisioners
	return input, cleanup, nil
}

func (c *govmomiClient) CleanupRemoteBuild(ctx context.Context, input vsphereRemoteBuildInput) error {
	ref, err := parseVSphereRemoteOperationRef(input.OperationRef)
	if err != nil {
		return err
	}
	if ref.BuildID == "" {
		ref.BuildID = input.BuildID
	}
	if ref.VMName == "" && input.BuildID != "" {
		ref.VMName = vsphereRemoteVMName(input.BuildID)
	}
	return c.cleanupVSphereRemoteVM(ctx, ref)
}

func (c *govmomiClient) cloneRemoteSource(ctx context.Context, input vsphereRemoteBuildInput) (*vsphereRemoteBuildState, error) {
	source, err := c.resolveRemoteSourceVM(ctx, input.SourceRef)
	if err != nil {
		return nil, err
	}
	dc, ds, err := c.resolveDatastore(ctx, c.cfg.datacenter, c.cfg.datastore)
	if err != nil {
		return nil, err
	}
	vmFinder := c.finder
	vmFinder.SetDatacenter(dc)
	rp, err := c.resolveResourcePool(ctx, vmFinder)
	if err != nil {
		return nil, err
	}
	host, err := c.resolveHost(ctx, vmFinder)
	if err != nil {
		return nil, err
	}
	folder, err := vmFinder.FolderOrDefault(ctx, firstNonEmpty(c.cfg.folder, "vm"))
	if err != nil {
		return nil, fmt.Errorf("find VM folder %q: %w", firstNonEmpty(c.cfg.folder, "vm"), err)
	}
	dsRef := ds.Reference()
	rpRef := rp.Reference()
	relocate := types.VirtualMachineRelocateSpec{
		Datastore: &dsRef,
		Pool:      &rpRef,
	}
	if host != nil {
		hostRef := host.Reference()
		relocate.Host = &hostRef
	}
	name := firstNonEmpty(input.ImageName, vsphereRemoteVMName(input.BuildID))
	ref := vsphereRemoteOperationRef{BuildID: input.BuildID, VMName: name}
	task, err := source.Clone(ctx, folder, name, types.VirtualMachineCloneSpec{
		Location: relocate,
		Template: false,
		PowerOn:  false,
	})
	if err != nil {
		return nil, fmt.Errorf("clone vSphere remote source %q: %w", input.SourceRef, err)
	}
	info, err := task.WaitForResult(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for vSphere clone %q: %w", name, err)
	}
	if vmRef, ok := info.Result.(types.ManagedObjectReference); ok {
		ref.VMRef = vmRef.Value
	}
	var cloned *object.VirtualMachine
	if ref.VMRef == "" {
		cloned, err = vmFinder.VirtualMachine(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("resolve cloned vSphere VM %q: %w", name, err)
		}
		ref.VMRef = cloned.Reference().Value
	} else {
		cloned = object.NewVirtualMachine(c.vc.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: ref.VMRef})
	}
	if vsphereRemoteRequiresSSH(input) {
		if err := c.ensureVSphereRemoteNIC(ctx, vmFinder, cloned); err != nil {
			_ = c.cleanupVSphereRemoteVM(ctx, ref)
			return nil, err
		}
	}
	return &vsphereRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseBooting,
		Message:      "vSphere remote clone created",
	}, nil
}

func (c *govmomiClient) resolveRemoteSourceVM(ctx context.Context, ref string) (*object.VirtualMachine, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("vSphere source providerRef is required")
	}
	if strings.HasPrefix(ref, "vm-") {
		return object.NewVirtualMachine(c.vc.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: ref}), nil
	}
	dc, err := c.finder.Datacenter(ctx, c.cfg.datacenter)
	if err != nil {
		return nil, fmt.Errorf("find datacenter %q: %w", c.cfg.datacenter, err)
	}
	finder := c.finder
	finder.SetDatacenter(dc)
	vm, err := finder.VirtualMachine(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("find vSphere source VM/template %q: %w", ref, err)
	}
	return vm, nil
}

func (c *govmomiClient) ensureRemoteVMReady(ctx context.Context, vm *object.VirtualMachine) error {
	powered, err := vmPowerState(ctx, vm)
	if err != nil {
		return err
	}
	if powered != types.VirtualMachinePowerStatePoweredOn {
		task, err := vm.PowerOn(ctx)
		if err != nil {
			return fmt.Errorf("power on vSphere remote VM: %w", err)
		}
		if err := task.Wait(ctx); err != nil {
			return fmt.Errorf("wait for vSphere remote VM power on: %w", err)
		}
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		ready, err := vm.IsToolsRunning(ctx)
		if err != nil {
			return fmt.Errorf("check VMware Tools status: %w", err)
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for VMware Tools")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func (c *govmomiClient) validateVSphereRemoteNetwork(input vsphereRemoteBuildInput) error {
	if vsphereRemoteRequiresSSH(input) && strings.TrimSpace(c.cfg.network) == "" {
		return fmt.Errorf("vSphere remote build with SSH guest access requires ProviderConfig extra network")
	}
	return nil
}

func (c *govmomiClient) ensureVSphereRemoteNIC(ctx context.Context, finder *find.Finder, vm *object.VirtualMachine) error {
	network, err := finder.Network(ctx, c.cfg.network)
	if err != nil {
		return fmt.Errorf("find vSphere remote network %q: %w", c.cfg.network, err)
	}
	backing, err := network.EthernetCardBackingInfo(ctx)
	if err != nil {
		return fmt.Errorf("resolve vSphere remote network backing %q: %w", c.cfg.network, err)
	}
	devices, err := vm.Device(ctx)
	if err != nil {
		return fmt.Errorf("read vSphere remote VM devices: %w", err)
	}
	nics := devices.SelectByType((*types.VirtualEthernetCard)(nil))
	if len(nics) == 0 {
		nic := &types.VirtualVmxnet3{
			VirtualVmxnet: types.VirtualVmxnet{
				VirtualEthernetCard: types.VirtualEthernetCard{
					VirtualDevice: types.VirtualDevice{
						Key:     -100,
						Backing: backing,
						Connectable: &types.VirtualDeviceConnectInfo{
							StartConnected:    true,
							Connected:         true,
							AllowGuestControl: true,
						},
					},
					AddressType: string(types.VirtualEthernetCardMacTypeGenerated),
				},
			},
		}
		specs, err := object.VirtualDeviceList{nic}.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
		if err != nil {
			return fmt.Errorf("create vSphere remote NIC config: %w", err)
		}
		task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: specs})
		if err != nil {
			return fmt.Errorf("add vSphere remote NIC: %w", err)
		}
		if err := task.Wait(ctx); err != nil {
			return fmt.Errorf("wait for vSphere remote NIC add: %w", err)
		}
		return nil
	}
	for _, device := range nics {
		card := device.(types.BaseVirtualEthernetCard).GetVirtualEthernetCard()
		card.Backing = backing
		if card.Connectable == nil {
			card.Connectable = &types.VirtualDeviceConnectInfo{}
		}
		card.Connectable.StartConnected = true
		card.Connectable.Connected = true
	}
	specs, err := nics.ConfigSpec(types.VirtualDeviceConfigSpecOperationEdit)
	if err != nil {
		return fmt.Errorf("create vSphere remote NIC edit config: %w", err)
	}
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: specs})
	if err != nil {
		return fmt.Errorf("configure vSphere remote NIC: %w", err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("wait for vSphere remote NIC configure: %w", err)
	}
	return nil
}

func (c *govmomiClient) runVSphereProvisioner(ctx context.Context, vm *object.VirtualMachine, input vsphereRemoteBuildInput, ref vsphereRemoteOperationRef, spec v1alpha1.ProvisionerSpec) error {
	program, args, err := vsphereGuestProgram(input, spec)
	if err != nil {
		return err
	}
	auth, err := c.remoteGuestAuth()
	if err != nil {
		return err
	}
	ops := guest.NewOperationsManager(c.vc.Client, vm.Reference())
	pm, err := ops.ProcessManager(ctx)
	if err != nil {
		return fmt.Errorf("create vSphere guest process manager: %w", err)
	}
	pid, err := pm.StartProgram(ctx, auth, &types.GuestProgramSpec{
		ProgramPath: program,
		Arguments:   args,
	})
	if err != nil {
		return fmt.Errorf("start vSphere guest provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	return waitGuestProcess(ctx, pm, auth, pid, ref.ProvisionerIndex)
}

func (c *govmomiClient) remoteGuestAuth() (*types.NamePasswordAuthentication, error) {
	username := firstNonEmpty(c.cfg.guestUsername, c.cfg.extraConfig["remote.guestUsername"], c.cfg.extraConfig["guestUsername"])
	password := firstNonEmpty(c.cfg.guestPassword, c.cfg.extraConfig["remote.guestPassword"], c.cfg.extraConfig["guestPassword"])
	if username == "" || password == "" {
		return nil, fmt.Errorf("vSphere remote build with provisioners requires secret keys guestUsername and guestPassword")
	}
	return &types.NamePasswordAuthentication{Username: username, Password: password}, nil
}

func waitGuestProcess(ctx context.Context, pm *guest.ProcessManager, auth types.BaseGuestAuthentication, pid int64, index int) error {
	for {
		procs, err := pm.ListProcesses(ctx, auth, []int64{pid})
		if err != nil {
			return fmt.Errorf("list vSphere guest process for provisioner %d: %w", index, err)
		}
		if len(procs) == 0 {
			return fmt.Errorf("vSphere guest process %d for provisioner %d disappeared", pid, index)
		}
		if procs[0].EndTime != nil {
			if procs[0].ExitCode != 0 {
				return fmt.Errorf("vSphere guest provisioner %d exited with code %d", index, procs[0].ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *govmomiClient) finishRemoteVM(ctx context.Context, vm *object.VirtualMachine, input vsphereRemoteBuildInput, ref vsphereRemoteOperationRef) (*vsphereRemoteBuildState, error) {
	if state, err := vmPowerState(ctx, vm); err == nil && state == types.VirtualMachinePowerStatePoweredOn {
		if err := vm.ShutdownGuest(ctx); err != nil {
			_ = c.cleanupVSphereRemoteVM(ctx, ref)
			return nil, fmt.Errorf("shutdown vSphere remote VM guest: %w", err)
		}
		if err := vm.WaitForPowerState(ctx, types.VirtualMachinePowerStatePoweredOff); err != nil {
			_ = c.cleanupVSphereRemoteVM(ctx, ref)
			return nil, fmt.Errorf("wait for vSphere remote VM shutdown: %w", err)
		}
	}
	if c.cfg.markAsTemplate {
		if err := vm.MarkAsTemplate(ctx); err != nil {
			_ = c.cleanupVSphereRemoteVM(ctx, ref)
			return nil, fmt.Errorf("mark vSphere remote VM as template: %w", err)
		}
	}
	ref.ImageRef = firstNonEmpty(ref.VMRef, vm.Reference().Value)
	image := vsphereRemoteImage(input, c.cfg.datacenter, ref.ImageRef)
	return &vsphereRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseReady,
		Message:      "vSphere remote image registered from cloned VM",
		Done:         true,
		Image:        image,
		Hygiene:      vsphereRemoteHygiene(input, image.ID),
	}, nil
}

func (c *govmomiClient) cleanupVSphereRemoteVM(ctx context.Context, ref vsphereRemoteOperationRef) error {
	if ref.VMRef == "" && ref.VMName == "" {
		return nil
	}
	vm := object.NewVirtualMachine(c.vc.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: ref.VMRef})
	if ref.VMRef == "" {
		dc, err := c.finder.Datacenter(ctx, c.cfg.datacenter)
		if err != nil {
			return err
		}
		finder := c.finder
		finder.SetDatacenter(dc)
		resolved, err := finder.VirtualMachine(ctx, ref.VMName)
		if err != nil {
			return nil
		}
		vm = resolved
	}
	if state, err := vmPowerState(ctx, vm); err == nil && state == types.VirtualMachinePowerStatePoweredOn {
		task, err := vm.PowerOff(ctx)
		if err == nil {
			_ = task.Wait(ctx)
		}
	}
	task, err := vm.Destroy(ctx)
	if err != nil {
		return nil
	}
	if task != nil {
		return task.Wait(ctx)
	}
	return nil
}

func vmPowerState(ctx context.Context, vm *object.VirtualMachine) (types.VirtualMachinePowerState, error) {
	var obj mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"runtime.powerState"}, &obj); err != nil {
		return "", fmt.Errorf("read vSphere VM power state: %w", err)
	}
	return obj.Runtime.PowerState, nil
}

func validateVSphereRemoteProvisioners(input vsphereRemoteBuildInput) error {
	for _, provisioner := range input.Provisioners {
		switch provisioner.Type {
		case "shell":
			if input.OSFamily == platform.OSFamilyWindows {
				return fmt.Errorf("shell provisioner is not supported for vSphere Windows remote builds; use powershell or file")
			}
		case "powershell":
			if input.OSFamily == platform.OSFamilyLinux {
				return fmt.Errorf("powershell provisioner is not supported for vSphere Linux remote builds; use shell or file")
			}
		case "file":
		default:
			return fmt.Errorf("provisioner type %q is not supported by vSphere remote build", provisioner.Type)
		}
	}
	return nil
}

func vsphereRemoteRequiresSSH(input vsphereRemoteBuildInput) bool {
	return input.GuestAccess != nil && strings.EqualFold(strings.TrimSpace(input.GuestAccess.Protocol), "ssh")
}

func vsphereGuestProgram(input vsphereRemoteBuildInput, spec v1alpha1.ProvisionerSpec) (string, string, error) {
	switch spec.Type {
	case "shell":
		if input.OSFamily != platform.OSFamilyLinux {
			return "", "", fmt.Errorf("shell provisioner requires linux OS family for vSphere remote build")
		}
		if strings.TrimSpace(spec.Inline) == "" {
			return "", "", fmt.Errorf("shell provisioner requires inline content for vSphere remote build")
		}
		return "/bin/sh", "-c " + shellQuote(spec.Inline), nil
	case "powershell":
		if input.OSFamily != platform.OSFamilyWindows {
			return "", "", fmt.Errorf("powershell provisioner requires windows OS family for vSphere remote build")
		}
		if strings.TrimSpace(spec.Inline) == "" {
			return "", "", fmt.Errorf("powershell provisioner requires inline content for vSphere remote build")
		}
		return "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", "-NoProfile -ExecutionPolicy Bypass -Command " + powershellQuote(spec.Inline), nil
	case "file":
		if strings.TrimSpace(spec.Inline) == "" {
			return "", "", fmt.Errorf("file provisioner requires inline content for vSphere remote build")
		}
		if len(spec.Args) != 1 || strings.TrimSpace(spec.Args[0]) == "" {
			return "", "", fmt.Errorf("file provisioner requires destination path in args[0] for vSphere remote build")
		}
		return vsphereFileProvisionerProgram(input, spec.Inline, spec.Args[0])
	default:
		return "", "", fmt.Errorf("provisioner type %q is not supported by vSphere remote build", spec.Type)
	}
}

func vsphereFileProvisionerProgram(input vsphereRemoteBuildInput, content, destination string) (string, string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	switch input.OSFamily {
	case platform.OSFamilyLinux:
		dir := path.Dir(destination)
		script := strings.Join([]string{
			"set -eu",
			"install -d -m 0755 " + shellQuote(dir),
			"base64 -d > " + shellQuote(destination) + " <<'__IMAGEBUILDER_FILE__'",
			encoded,
			"__IMAGEBUILDER_FILE__",
			"chmod 0600 " + shellQuote(destination),
		}, "\n")
		return "/bin/sh", "-c " + shellQuote(script), nil
	case platform.OSFamilyWindows:
		script := strings.Join([]string{
			"$destination = " + powershellQuote(destination),
			"$parent = Split-Path -Parent $destination",
			"if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }",
			"$bytes = [Convert]::FromBase64String(" + powershellQuote(encoded) + ")",
			"[IO.File]::WriteAllBytes($destination, $bytes)",
		}, "\n")
		return "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", "-NoProfile -ExecutionPolicy Bypass -Command " + powershellQuote(script), nil
	default:
		return "", "", fmt.Errorf("file provisioner requires linux or windows OS family for vSphere remote build")
	}
}

type vsphereRemoteOperationRef struct {
	BuildID          string
	VMName           string
	VMRef            string
	ImageRef         string
	ProvisionerIndex int
}

func (r vsphereRemoteOperationRef) String() string {
	values := url.Values{}
	if r.VMName != "" {
		values.Set("vmName", r.VMName)
	}
	if r.VMRef != "" {
		values.Set("vmRef", r.VMRef)
	}
	if r.ImageRef != "" {
		values.Set("imageRef", r.ImageRef)
	}
	if r.ProvisionerIndex > 0 {
		values.Set("provisionerIndex", strconv.Itoa(r.ProvisionerIndex))
	}
	u := url.URL{Scheme: "vsphere", Host: "remote-build", Path: "/" + r.BuildID, RawQuery: values.Encode()}
	return u.String()
}

func parseVSphereRemoteOperationRef(value string) (vsphereRemoteOperationRef, error) {
	if value == "" {
		return vsphereRemoteOperationRef{}, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return vsphereRemoteOperationRef{}, fmt.Errorf("parse vSphere remote operation ref: %w", err)
	}
	if u.Scheme != "vsphere" || u.Host != "remote-build" {
		return vsphereRemoteOperationRef{}, fmt.Errorf("invalid vSphere remote operation ref %q", value)
	}
	index := 0
	if rawIndex := u.Query().Get("provisionerIndex"); rawIndex != "" {
		parsed, err := strconv.Atoi(rawIndex)
		if err != nil || parsed < 0 {
			return vsphereRemoteOperationRef{}, fmt.Errorf("invalid vSphere remote operation ref provisionerIndex %q", rawIndex)
		}
		index = parsed
	}
	return vsphereRemoteOperationRef{
		BuildID:          strings.TrimPrefix(u.Path, "/"),
		VMName:           u.Query().Get("vmName"),
		VMRef:            u.Query().Get("vmRef"),
		ImageRef:         u.Query().Get("imageRef"),
		ProvisionerIndex: index,
	}, nil
}

func vsphereRemoteVMName(buildID string) string {
	return "imagebuilder-" + sanitizeName(buildID)
}

func vsphereRemoteImage(input vsphereRemoteBuildInput, location, id string) *platform.ImageRef {
	tags := cloneStringMap(input.Tags)
	tags["imagebuilder.io/provider"] = "vsphere"
	tags["imagebuilder.io/registration-mode"] = "remote-clone"
	tags["imagebuilder.io/format"] = string(input.Format)
	return &platform.ImageRef{
		ID:       id,
		Name:     input.ImageName,
		Location: location,
		Tags:     tags,
	}
}

func vsphereRemoteHygiene(input vsphereRemoteBuildInput, resultRef string) *platform.RemoteHygieneResult {
	checks := []string{"vsphere-clone-completed"}
	if len(input.Provisioners) > 0 {
		checks = append(checks, "vsphere-guest-operations-provisioners-completed", "vsphere-guest-shutdown-completed")
	}
	return &platform.RemoteHygieneResult{
		Status:    "passed",
		Message:   "vSphere remote build completed",
		Checks:    checks,
		ResultRef: resultRef,
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
