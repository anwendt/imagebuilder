package vsphere

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/guest"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vapi/library"
	"github.com/vmware/govmomi/vapi/vcenter"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

const (
	guestProvisionLog   = "/var/log/imagebuilder-provision.log"
	guestAuthWait       = 15 * time.Minute
	guestLogTailBytes   = 4000
	buildVMHostnamePart = "imagebuilder-build"
)

func (c *govmomiClient) vmFinder(ctx context.Context) (*find.Finder, error) {
	dc, err := c.finder.Datacenter(ctx, c.cfg.datacenter)
	if err != nil {
		return nil, fmt.Errorf("find datacenter %q: %w", c.cfg.datacenter, err)
	}
	finder := find.NewFinder(c.vc.Client, true)
	finder.SetDatacenter(dc)
	return finder, nil
}

// findBuildFolder resolves the dedicated build folder without creating it.
func (c *govmomiClient) findBuildFolder(ctx context.Context, finder *find.Finder) (*object.Folder, error) {
	if err := checkBuildFolderPath(c.cfg.buildFolder, c.cfg.protectedNames); err != nil {
		return nil, err
	}
	folder, err := finder.Folder(ctx, strings.TrimRight(c.cfg.buildFolder, "/"))
	if err != nil {
		return nil, fmt.Errorf("find build folder %q: %w", c.cfg.buildFolder, err)
	}
	return folder, nil
}

// ensureBuildFolder resolves the dedicated build folder and creates it (one
// level below an existing folder) when it does not exist yet.
func (c *govmomiClient) ensureBuildFolder(ctx context.Context, finder *find.Finder) (*object.Folder, error) {
	folder, err := c.findBuildFolder(ctx, finder)
	if err == nil {
		return folder, nil
	}
	var notFound *find.NotFoundError
	if !errors.As(err, &notFound) {
		return nil, err
	}
	trimmed := strings.TrimRight(c.cfg.buildFolder, "/")
	parent, perr := finder.Folder(ctx, path.Dir(trimmed))
	if perr != nil {
		return nil, fmt.Errorf("find parent of build folder %q: %w", c.cfg.buildFolder, perr)
	}
	created, cerr := parent.CreateFolder(ctx, path.Base(trimmed))
	if cerr != nil {
		return nil, fmt.Errorf("create build folder %q: %w", c.cfg.buildFolder, cerr)
	}
	return created, nil
}

// requireOwnedBuildVM refuses any VM that is not a provider-owned build VM.
func (c *govmomiClient) requireOwnedBuildVM(ctx context.Context, vm *object.VirtualMachine) error {
	finder, err := c.vmFinder(ctx)
	if err != nil {
		return err
	}
	folder, err := c.findBuildFolder(ctx, finder)
	if err != nil {
		return err
	}
	var props mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"name", "parent"}, &props); err != nil {
		return fmt.Errorf("read vSphere VM properties: %w", err)
	}
	return checkOwnedBuildVM(props.Name, props.Parent, folder.Reference(), c.cfg.protectedNames)
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	var notFound *find.NotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	return strings.Contains(err.Error(), "ManagedObjectNotFound") || strings.Contains(err.Error(), "has already been deleted")
}

// destroyOwnedBuildVM powers off and destroys a build VM after the guard has
// confirmed it belongs to the provider.
func (c *govmomiClient) destroyOwnedBuildVM(ctx context.Context, vm *object.VirtualMachine) error {
	if err := c.requireOwnedBuildVM(ctx, vm); err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		return err
	}
	if state, err := vmPowerState(ctx, vm); err == nil && state == types.VirtualMachinePowerStatePoweredOn {
		if task, err := vm.PowerOff(ctx); err == nil {
			_ = task.Wait(ctx)
		}
	}
	task, err := vm.Destroy(ctx)
	if err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		return fmt.Errorf("destroy build VM: %w", err)
	}
	if task != nil {
		if err := task.Wait(ctx); err != nil && !isNotFoundErr(err) {
			return fmt.Errorf("wait for build VM destroy: %w", err)
		}
	}
	return nil
}

// deployCloudImage downloads the checksum-pinned cloud image OVA through the
// configured download proxy and deploys it as a build VM in the dedicated
// build folder. Cloud-init gets a temporary per-build root password through
// the OVF user-data property so VMware Guest Operations can reach the guest.
func (c *govmomiClient) deployCloudImage(ctx context.Context, input vsphereRemoteBuildInput) (*vsphereRemoteBuildState, error) {
	if c.cfg.network == "" {
		return nil, fmt.Errorf("vSphere cloud-image build requires ProviderConfig extra network")
	}
	name := buildVMName(input.ImageName, input.BuildID)
	if err := checkBuildVMName(name, c.cfg.protectedNames); err != nil {
		return nil, err
	}
	dc, ds, err := c.resolveDatastore(ctx, c.cfg.datacenter, c.cfg.datastore)
	if err != nil {
		return nil, err
	}
	finder := find.NewFinder(c.vc.Client, true)
	finder.SetDatacenter(dc)
	rp, err := c.resolveResourcePool(ctx, finder)
	if err != nil {
		return nil, err
	}
	host, err := c.resolveHost(ctx, finder)
	if err != nil {
		return nil, err
	}
	folder, err := c.ensureBuildFolder(ctx, finder)
	if err != nil {
		return nil, err
	}
	ref := vsphereRemoteOperationRef{BuildID: input.BuildID, VMName: name}
	// A leftover VM of an interrupted earlier attempt of this very build is
	// removed first; the guard only lets provider-owned build VMs through.
	if leftover, err := finder.VirtualMachine(ctx, strings.TrimRight(c.cfg.buildFolder, "/")+"/"+name); err == nil {
		if err := c.destroyOwnedBuildVM(ctx, leftover); err != nil {
			return nil, fmt.Errorf("remove leftover build VM %q: %w", name, err)
		}
	}

	httpClient, err := platform.HTTPClient(map[string]string{"httpProxy": c.cfg.downloadProxy, "httpsProxy": c.cfg.downloadProxy})
	if err != nil {
		return nil, fmt.Errorf("configure source download proxy: %w", err)
	}
	downloadDir := filepath.Join(c.cfg.downloadDir, sanitizeName(input.BuildID))
	defer func() { _ = os.RemoveAll(downloadDir) }()
	ovaPath, err := downloadVerifiedSource(ctx, httpClient, input.SourceRef, input.SourceChecksum, downloadDir)
	if err != nil {
		return nil, err
	}

	archive, fpath := artifactArchive(ovaPath, platform.FormatOVA)
	networks, err := importer.Spec(fpath, archive, false, false)
	if err != nil {
		return nil, fmt.Errorf("read OVF spec of source image: %w", err)
	}
	mapping := networks.NetworkMapping
	if len(mapping) == 0 {
		mapping = []importer.Network{{Name: firstNonEmpty(c.cfg.ovfNetworkName, "VM Network")}}
	}
	for i := range mapping {
		mapping[i].Network = c.cfg.network
	}
	password := deriveGuestPassword(c.cfg.password, input.BuildID)
	opts := importer.Options{
		DiskProvisioning: c.cfg.diskProvisioning,
		Annotation:       strings.TrimSpace(c.cfg.annotation + "\nimagebuilder build " + input.BuildID),
		Name:             &name,
		NetworkMapping:   mapping,
		PropertyMapping: ovfProperties(map[string]string{
			"user-data":   ovfUserDataProperty(cloudInitUserData(password)),
			"hostname":    buildVMHostnamePart,
			"instance-id": name,
		}),
	}
	imp := importer.Importer{
		Log:          func(msg string) (int, error) { c.logImportWarning(msg); return len(msg), nil },
		Name:         name,
		Client:       c.vc.Client,
		Finder:       finder,
		Datacenter:   dc,
		Datastore:    ds,
		ResourcePool: rp,
		Host:         host,
		Folder:       folder,
		Archive:      archive,
	}
	moref, err := imp.Import(ctx, fpath, opts)
	if err != nil {
		return nil, fmt.Errorf("import source image OVA as build VM %q: %w", name, err)
	}
	ref.VMRef = moref.Value
	vm := object.NewVirtualMachine(c.vc.Client, *moref)
	if c.cfg.buildCPUs > 0 || c.cfg.buildMemoryMiB > 0 {
		spec := types.VirtualMachineConfigSpec{}
		if c.cfg.buildCPUs > 0 {
			spec.NumCPUs = c.cfg.buildCPUs
		}
		if c.cfg.buildMemoryMiB > 0 {
			spec.MemoryMB = int64(c.cfg.buildMemoryMiB)
		}
		task, err := vm.Reconfigure(ctx, spec)
		if err == nil {
			err = task.Wait(ctx)
		}
		if err != nil {
			_ = c.destroyOwnedBuildVM(ctx, vm)
			return nil, fmt.Errorf("size build VM: %w", err)
		}
	}
	if c.cfg.buildDiskGiB > 0 {
		if err := growFirstDisk(ctx, vm, dc, c.cfg.buildDiskGiB); err != nil {
			_ = c.destroyOwnedBuildVM(ctx, vm)
			return nil, err
		}
	}
	return &vsphereRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseBooting,
		Message:      "vSphere cloud image deployed as build VM",
	}, nil
}

func growFirstDisk(ctx context.Context, vm *object.VirtualMachine, datacenter *object.Datacenter, gib int) error {
	devices, err := vm.Device(ctx)
	if err != nil {
		return fmt.Errorf("read build VM devices: %w", err)
	}
	disks := devices.SelectByType((*types.VirtualDisk)(nil))
	if len(disks) == 0 {
		return fmt.Errorf("build VM has no disk to grow")
	}
	disk := disks[0].(*types.VirtualDisk)
	want := int64(gib) * 1024 * 1024
	if disk.CapacityInKB >= want {
		return nil
	}
	backing, ok := disk.Backing.(types.BaseVirtualDeviceFileBackingInfo)
	if !ok {
		return fmt.Errorf("build VM disk has no file backing to grow")
	}
	// VirtualDiskManager extends the powered-off disk file directly; editing the
	// device capacity is rejected for disks imported from stream-optimized OVAs.
	task, err := object.NewVirtualDiskManager(vm.Client()).ExtendVirtualDisk(ctx, backing.GetVirtualDeviceFileBackingInfo().FileName, datacenter, want, nil)
	if err != nil {
		return fmt.Errorf("grow build VM disk to %d GiB: %w", gib, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("grow build VM disk to %d GiB: %w", gib, err)
	}
	return nil
}

// remoteGuestAuthFor returns the Guest Operations credentials: the ones from
// the ProviderConfig Secret when present, otherwise the temporary per-build
// root login of a cloud-image build.
func (c *govmomiClient) remoteGuestAuthFor(input vsphereRemoteBuildInput) (*types.NamePasswordAuthentication, error) {
	username := firstNonEmpty(c.cfg.guestUsername, c.cfg.extraConfig["remote.guestUsername"], c.cfg.extraConfig["guestUsername"])
	password := firstNonEmpty(c.cfg.guestPassword, c.cfg.extraConfig["remote.guestPassword"], c.cfg.extraConfig["guestPassword"])
	if username == "" && password == "" && input.SourceType == "cloud-image" {
		return &types.NamePasswordAuthentication{Username: "root", Password: deriveGuestPassword(c.cfg.password, input.BuildID)}, nil
	}
	if username == "" || password == "" {
		return nil, fmt.Errorf("vSphere remote build with provisioners requires secret keys guestUsername and guestPassword")
	}
	return &types.NamePasswordAuthentication{Username: username, Password: password}, nil
}

// waitGuestOperations waits until Guest Operations accept the credentials,
// configures the optional static build network and waits for cloud-init, so
// provisioners start on a settled guest.
func (c *govmomiClient) waitGuestOperations(ctx context.Context, vm *object.VirtualMachine, input vsphereRemoteBuildInput) error {
	if input.OSFamily != platform.OSFamilyLinux || len(input.Provisioners) == 0 {
		return nil
	}
	auth, err := c.remoteGuestAuthFor(input)
	if err != nil {
		return err
	}
	pm, err := guest.NewOperationsManager(c.vc.Client, vm.Reference()).ProcessManager(ctx)
	if err != nil {
		return fmt.Errorf("create vSphere guest process manager: %w", err)
	}
	deadline := time.Now().Add(guestAuthWait)
	for {
		err := c.runGuestScript(ctx, vm, pm, auth, "cloud-init status --wait >/dev/null 2>&1 || true", false)
		if err == nil {
			break
		}
		var exitErr *guestExitError
		if errors.As(err, &exitErr) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vSphere Guest Operations did not accept the build credentials within %s: %w", guestAuthWait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	if c.cfg.buildNet != nil {
		if err := c.runGuestScript(ctx, vm, pm, auth, c.cfg.buildNet.guestScript(), true); err != nil {
			return fmt.Errorf("configure build network %s: %w", c.cfg.buildNet.Address, err)
		}
	}
	return nil
}

// guestExitError is a guest program that ran and returned a non-zero code.
type guestExitError struct{ code int32 }

func (e *guestExitError) Error() string {
	return fmt.Sprintf("guest program exited with code %d", e.code)
}

// runGuestScript runs a shell script as the build user. With logged set the
// output goes to the provisioning log and its tail is added to a failure.
func (c *govmomiClient) runGuestScript(ctx context.Context, vm *object.VirtualMachine, pm *guest.ProcessManager, auth *types.NamePasswordAuthentication, script string, logged bool) error {
	if logged {
		script = "exec >>" + guestProvisionLog + " 2>&1\n" + script
	}
	pid, err := pm.StartProgram(ctx, auth, &types.GuestProgramSpec{
		ProgramPath:  "/bin/sh",
		Arguments:    "-c " + shellQuote(script),
		EnvVariables: []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"},
	})
	if err != nil {
		return err
	}
	if err := waitGuestProcess(ctx, pm, auth, pid, -1); err != nil {
		if logged {
			if tail := c.guestLogTail(ctx, vm, auth); tail != "" {
				return fmt.Errorf("%w; log tail:\n%s", err, tail)
			}
		}
		return err
	}
	return nil
}

// guestLogTail returns the end of the guest provisioning log for error messages.
func (c *govmomiClient) guestLogTail(ctx context.Context, vm *object.VirtualMachine, auth types.BaseGuestAuthentication) string {
	fm, err := guest.NewOperationsManager(c.vc.Client, vm.Reference()).FileManager(ctx)
	if err != nil {
		return ""
	}
	info, err := fm.InitiateFileTransferFromGuest(ctx, auth, guestProvisionLog)
	if err != nil {
		return ""
	}
	u, err := url.Parse(info.Url)
	if err != nil {
		return ""
	}
	if parsed, err := c.vc.Client.ParseURL(info.Url); err == nil {
		u = parsed
	}
	body, _, err := c.vc.Client.Download(ctx, u, &soap.DefaultDownload)
	if err != nil {
		return ""
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, 4<<20))
	if err != nil {
		return ""
	}
	if len(data) > guestLogTailBytes {
		data = data[len(data)-guestLogTailBytes:]
	}
	return strings.TrimSpace(string(data))
}

// publishVMTemplate publishes the (powered off) build VM as a VM template item
// of the configured Content Library. An existing item is never modified: the
// same build finding its own item again is a successful retry, anything else
// is an error.
func (c *govmomiClient) publishVMTemplate(ctx context.Context, vm *object.VirtualMachine, input vsphereRemoteBuildInput) (string, error) {
	restClient, err := c.restClient(ctx)
	if err != nil {
		return "", err
	}
	manager := library.NewManager(restClient)
	libraryID := c.cfg.contentLibraryID
	if libraryID == "" {
		lib, err := manager.GetLibraryByName(ctx, c.cfg.contentLibrary)
		if err != nil {
			return "", fmt.Errorf("find content library %q: %w", c.cfg.contentLibrary, err)
		}
		libraryID = lib.ID
	}
	itemName := input.ImageName
	if itemName == "" {
		return "", fmt.Errorf("content library item name (image name) is required")
	}
	marker := "imagebuilder build " + input.BuildID
	ids, err := manager.FindLibraryItems(ctx, library.FindItem{LibraryID: libraryID, Name: itemName})
	if err != nil {
		return "", fmt.Errorf("look up content library item %q: %w", itemName, err)
	}
	for _, id := range ids {
		item, err := manager.GetLibraryItem(ctx, id)
		if err != nil {
			return "", fmt.Errorf("read content library item %q: %w", itemName, err)
		}
		if item.Description != nil && strings.Contains(*item.Description, marker) {
			return id, nil
		}
	}
	if len(ids) > 0 {
		return "", fmt.Errorf("content library item %q already exists; the provider never modifies existing items, use a new image name", itemName)
	}
	spec := vcenter.Template{
		Name:        itemName,
		Library:     libraryID,
		SourceVM:    vm.Reference().Value,
		Description: strings.TrimSpace(c.cfg.annotation + "\n" + marker),
	}
	if c.cfg.datastore != "" {
		_, ds, err := c.resolveDatastore(ctx, c.cfg.datacenter, c.cfg.datastore)
		if err != nil {
			return "", err
		}
		storage := &vcenter.DiskStorage{Datastore: ds.Reference().Value}
		spec.DiskStorage = storage
		spec.VMHomeStorage = storage
	}
	id, err := vcenter.NewManager(restClient).CreateTemplate(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("publish build VM as content library template %q: %w", itemName, err)
	}
	return id, nil
}

func ovfProperties(values map[string]string) []importer.Property {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]importer.Property, 0, len(keys))
	for _, key := range keys {
		var p importer.Property
		p.Key = key
		p.Value = values[key]
		out = append(out, p)
	}
	return out
}
