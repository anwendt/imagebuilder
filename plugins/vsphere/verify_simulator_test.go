package vsphere

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/vapi/library"
	_ "github.com/vmware/govmomi/vapi/simulator"
)

func TestDestroyVerifyVMWithSimulator(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	finder, err := c.vmFinder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := c.ensureBuildFolder(ctx, finder)
	if err != nil {
		t.Fatal(err)
	}
	name := verifyVMName("ubuntu", "build-12345678")
	// Absent: nothing to do.
	if err := c.destroyVerifyVM(ctx, name); err != nil {
		t.Fatalf("destroy absent verify VM: %v", err)
	}
	createSimulatorBuildVM(t, c, folder, name)
	if err := c.destroyVerifyVM(ctx, name); err != nil {
		t.Fatalf("destroy verify VM: %v", err)
	}
	if _, err := finder.VirtualMachine(ctx, "/DC0/vm/imagebuilder-builds/"+name); err == nil {
		t.Fatal("verify VM must be gone")
	}
	assertErrContains(t, c.destroyVerifyVM(ctx, "DC0_H0_VM0"), "prefix")
}

func TestCollectEvidenceRefusesForeignVMWithSimulator(t *testing.T) {
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
	foreign, err := finder.VirtualMachine(ctx, "DC0_H0_VM0")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.collectAndPersistEvidence(ctx, foreign, vsphereRemoteBuildInput{BuildID: "b1", Evidence: controllerPolicy()})
	assertErrContains(t, err, "guard")
}

func TestDeleteOwnedLibraryItemWithSimulator(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	rc, err := c.restClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := library.NewManager(rc)
	_, ds, err := c.resolveDatastore(ctx, "DC0", "LocalDS_0")
	if err != nil {
		t.Fatal(err)
	}
	libID, err := m.CreateLibrary(ctx, library.Library{Name: "golden", Type: "LOCAL", Storage: []library.StorageBacking{{DatastoreID: ds.Reference().Value, Type: "DATASTORE"}}})
	if err != nil {
		t.Fatal(err)
	}
	mine := "Golden\nimagebuilder build b-1"
	foreign := "something else"
	mineID, err := m.CreateLibraryItem(ctx, library.Item{Name: "mine", Type: "ovf", LibraryID: libID, Description: &mine})
	if err != nil {
		t.Fatal(err)
	}
	foreignID, err := m.CreateLibraryItem(ctx, library.Item{Name: "foreign", Type: "ovf", LibraryID: libID, Description: &foreign})
	if err != nil {
		t.Fatal(err)
	}
	assertErrContains(t, c.deleteOwnedLibraryItem(ctx, foreignID, "b-1"), "left untouched")
	if _, err := m.GetLibraryItem(ctx, foreignID); err != nil {
		t.Fatalf("foreign item must stay: %v", err)
	}
	if err := c.deleteOwnedLibraryItem(ctx, mineID, "b-1"); err != nil {
		t.Fatalf("delete owned item: %v", err)
	}
	if _, err := m.GetLibraryItem(ctx, mineID); err == nil {
		t.Fatal("owned item must be deleted")
	}
	if err := c.deleteOwnedLibraryItem(ctx, mineID, "b-1"); err != nil {
		t.Fatalf("deleting a missing item must be a no-op: %v", err)
	}
	if err := c.deleteOwnedLibraryItem(ctx, "", "b-1"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBootStepFailsWithoutUserDataPropertyWithSimulator(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	rc, err := c.restClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, ds, err := c.resolveDatastore(ctx, "DC0", "LocalDS_0")
	if err != nil {
		t.Fatal(err)
	}
	m := library.NewManager(rc)
	if _, err := m.CreateLibrary(ctx, library.Library{Name: "golden", Type: "LOCAL", Storage: []library.StorageBacking{{DatastoreID: ds.Reference().Value, Type: "DATASTORE"}}}); err != nil {
		t.Fatal(err)
	}
	c.cfg.contentLibrary = "golden"
	c.cfg.templateFolder = "/DC0/vm/imagebuilder-templates"
	c.cfg.publishToLibrary = true
	c.cfg.verifyBoot = true
	c.cfg.cluster = "DC0_C0"
	finder, err := c.vmFinder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := c.ensureBuildFolder(ctx, finder)
	if err != nil {
		t.Fatal(err)
	}
	input := vsphereRemoteBuildInput{BuildID: "build-12345678", ImageName: "ubuntu-2604-base"}
	buildVM := createSimulatorBuildVM(t, c, folder, buildVMName(input.ImageName, input.BuildID))
	itemID, err := c.publishVMTemplate(ctx, buildVM, input)
	if err != nil {
		t.Skipf("simulator cannot publish a VM template: %v", err)
	}
	// The simulator drops the template description; restore the build marker
	// vCenter keeps.
	published, err := m.GetLibraryItem(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	marker := "imagebuilder build " + input.BuildID
	published.Description = &marker
	if err := m.UpdateLibraryItem(ctx, published); err != nil {
		t.Fatal(err)
	}
	_, err = c.verifyBootStep(ctx, input, vsphereRemoteOperationRef{BuildID: input.BuildID, ImageRef: itemID, VerifyBoot: verifyBootPending})
	assertErrContains(t, err, "VerifyBootFailed")
	t.Logf("verify error: %v", err)
	if _, gerr := m.GetLibraryItem(ctx, itemID); gerr == nil {
		t.Fatal("the unverified, own library item must be deleted")
	}
	if _, ferr := finder.VirtualMachine(ctx, "/DC0/vm/imagebuilder-builds/"+verifyVMName(input.ImageName, input.BuildID)); ferr == nil {
		t.Fatal("the verify VM must be destroyed")
	}
}

func TestCleanupRemoteBuildRemovesUnverifiedImageAndBundleWithSimulator(t *testing.T) {
	c, _, done := newGuardSimulatorClient(t, nil)
	defer done()
	ctx := context.Background()
	c.cfg.downloadDir = t.TempDir()
	c.cfg.verifyBoot = true
	rc, err := c.restClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, ds, err := c.resolveDatastore(ctx, "DC0", "LocalDS_0")
	if err != nil {
		t.Fatal(err)
	}
	m := library.NewManager(rc)
	libID, err := m.CreateLibrary(ctx, library.Library{Name: "golden", Type: "LOCAL", Storage: []library.StorageBacking{{DatastoreID: ds.Reference().Value, Type: "DATASTORE"}}})
	if err != nil {
		t.Fatal(err)
	}
	marker := "imagebuilder build b-9"
	itemID, err := m.CreateLibraryItem(ctx, library.Item{Name: "img", Type: "ovf", LibraryID: libID, Description: &marker})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistEvidenceBundle(c.cfg.downloadDir, "b-9", validBundleBytes(t)); err != nil {
		t.Fatal(err)
	}
	finder, _ := c.vmFinder(ctx)
	if _, err := c.ensureBuildFolder(ctx, finder); err != nil {
		t.Fatal(err)
	}
	ref := vsphereRemoteOperationRef{BuildID: "b-9", ImageRef: itemID, VerifyBoot: verifyBootPending}
	if err := c.CleanupRemoteBuild(ctx, vsphereRemoteBuildInput{BuildID: "b-9", ImageName: "img", OperationRef: ref.String()}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := m.GetLibraryItem(ctx, itemID); err == nil {
		t.Fatal("unverified item must be removed")
	}
	if _, err := loadEvidenceBundle(c.cfg.downloadDir, "b-9", "x"); err == nil {
		t.Fatal("persisted bundle must be removed")
	}
}
