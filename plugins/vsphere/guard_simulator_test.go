package vsphere

import (
	"context"
	"os"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

func newGuardSimulatorClient(t *testing.T, extra map[string]string) (*govmomiClient, *simulator.Model, func()) {
	t.Helper()
	if os.Getenv("IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS") != "1" {
		t.Skip("set IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS=1 to run the govmomi simulator guard tests")
	}
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	server := model.Service.NewServer()
	cfg := config{
		endpoint:       server.URL.String(),
		insecure:       true,
		username:       "user",
		password:       "pass",
		datacenter:     "DC0",
		datastore:      "LocalDS_0",
		buildFolder:    "/DC0/vm/imagebuilder-builds",
		protectedNames: []string{"mif"},
		extraConfig:    extra,
	}
	c, err := newGovmomiClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c.(*govmomiClient), model, func() { server.Close(); model.Remove() }
}

func TestGuardCleanupRefusesForeignVMs(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	finder, err := c.vmFinder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensureBuildFolder(ctx, finder); err != nil {
		t.Fatalf("ensureBuildFolder: %v", err)
	}
	foreign, err := finder.VirtualMachine(ctx, "DC0_H0_VM0")
	if err != nil {
		t.Fatalf("find foreign VM: %v", err)
	}
	err = c.cleanupVSphereRemoteVM(ctx, vsphereRemoteOperationRef{BuildID: "b1", VMRef: foreign.Reference().Value})
	assertErrContains(t, err, "guard")
	if _, err := finder.VirtualMachine(ctx, "DC0_H0_VM0"); err != nil {
		t.Fatalf("foreign VM must still exist: %v", err)
	}
	// A foreign name is refused even when only the name is known.
	err = c.cleanupVSphereRemoteVM(ctx, vsphereRemoteOperationRef{BuildID: "b1", VMName: "DC0_H0_VM0"})
	assertErrContains(t, err, "prefix")
}

func TestGuardCleanupDestroysOwnedBuildVM(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	finder, err := c.vmFinder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := c.ensureBuildFolder(ctx, finder)
	if err != nil {
		t.Fatalf("ensureBuildFolder: %v", err)
	}
	// Ensure is idempotent.
	if again, err := c.ensureBuildFolder(ctx, finder); err != nil || again.Reference() != folder.Reference() {
		t.Fatalf("second ensureBuildFolder: %v", err)
	}
	vm := createSimulatorBuildVM(t, c, folder, "imagebuilder-demo-12345678")
	err = c.cleanupVSphereRemoteVM(ctx, vsphereRemoteOperationRef{BuildID: "12345678", VMRef: vm.Reference().Value})
	if err != nil {
		t.Fatalf("cleanup owned VM: %v", err)
	}
	var notFound *find.NotFoundError
	if _, err := finder.VirtualMachine(ctx, "/DC0/vm/imagebuilder-builds/imagebuilder-demo-12345678"); err == nil || !asNotFound(err, &notFound) {
		t.Fatalf("owned VM should be gone, err=%v", err)
	}
	// A second cleanup of the vanished VM is a no-op.
	if err := c.cleanupVSphereRemoteVM(ctx, vsphereRemoteOperationRef{BuildID: "12345678", VMRef: vm.Reference().Value}); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestGuardRefusesVMWithPrefixOutsideBuildFolder(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	finder, err := c.vmFinder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensureBuildFolder(ctx, finder); err != nil {
		t.Fatal(err)
	}
	vmFolder, err := finder.Folder(ctx, "/DC0/vm")
	if err != nil {
		t.Fatal(err)
	}
	vm := createSimulatorBuildVM(t, c, vmFolder, "imagebuilder-lookalike-1")
	err = c.cleanupVSphereRemoteVM(ctx, vsphereRemoteOperationRef{BuildID: "x", VMRef: vm.Reference().Value})
	assertErrContains(t, err, "build folder")
}

func asNotFound(err error, target **find.NotFoundError) bool {
	nf, ok := err.(*find.NotFoundError)
	if ok {
		*target = nf
	}
	return ok
}

func createSimulatorBuildVM(t *testing.T, c *govmomiClient, folder *object.Folder, name string) *object.VirtualMachine {
	t.Helper()
	ctx := context.Background()
	finder, _ := c.vmFinder(ctx)
	pool, err := finder.ResourcePool(ctx, "/DC0/host/DC0_C0/Resources")
	if err != nil {
		t.Fatal(err)
	}
	spec := types.VirtualMachineConfigSpec{
		Name:    name,
		GuestId: string(types.VirtualMachineGuestOsIdentifierOtherGuest64),
		Files:   &types.VirtualMachineFileInfo{VmPathName: "[LocalDS_0]"},
	}
	task, err := folder.CreateVM(ctx, spec, pool, nil)
	if err != nil {
		t.Fatalf("create VM: %v", err)
	}
	info, err := task.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait create VM: %v", err)
	}
	return object.NewVirtualMachine(c.vc.Client, info.Result.(types.ManagedObjectReference))
}
