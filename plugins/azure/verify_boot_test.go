package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

type fakeCompute struct {
	created     []string
	createdVM   armcompute.VirtualMachine
	createErr   error
	deletedVMs  []string
	deletedDisk []string
	deleteErr   error
}

func (f *fakeCompute) CreateVM(_ context.Context, name string, vm armcompute.VirtualMachine) error {
	f.created = append(f.created, name)
	f.createdVM = vm
	return f.createErr
}

func (f *fakeCompute) DeleteVM(_ context.Context, name string) error {
	f.deletedVMs = append(f.deletedVMs, name)
	return f.deleteErr
}

func (f *fakeCompute) DeleteDisk(_ context.Context, name string) error {
	f.deletedDisk = append(f.deletedDisk, name)
	return nil
}

type fakeOwner struct {
	imageExists, versionExists bool
	existsErr                  error
	deletedImages              []string
	deletedVersions            []string
	deleteErr                  error
}

func (f *fakeOwner) ManagedImageExists(context.Context, string) (bool, error) {
	return f.imageExists, f.existsErr
}

func (f *fakeOwner) GalleryVersionExists(context.Context, string) (bool, error) {
	return f.versionExists, f.existsErr
}

func (f *fakeOwner) DeleteManagedImage(_ context.Context, name string) error {
	f.deletedImages = append(f.deletedImages, name)
	return f.deleteErr
}

func (f *fakeOwner) DeleteGalleryVersion(_ context.Context, version string) error {
	f.deletedVersions = append(f.deletedVersions, version)
	return f.deleteErr
}

func verifyRunner(code string) *fakeRunner {
	return &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) {
		return statuses("Enable succeeded: \n[stdout]\n" + verifyBootResultToken + code + "\n[stderr]\n"), nil
	}}
}

func testVerifySpec() azureVerifySpec {
	return azureVerifySpec{
		BuildID:            "build-0123456789",
		Location:           "westeurope",
		VMSize:             "Standard_B2s",
		NetworkInterfaceID: "/subscriptions/0/nic",
		ImageID:            "/subscriptions/0/galleries/g/images/i/versions/1.2.0",
		OSFamily:           platform.OSFamilyLinux,
		StorageAccountType: armcompute.StorageAccountTypesStandardLRS,
		Tags:               map[string]string{"team": "platform"},
	}
}

func TestRunAzureVerifyBoot(t *testing.T) {
	tests := []struct {
		name    string
		spec    func() azureVerifySpec
		compute *fakeCompute
		runner  *fakeRunner
		wantErr string
	}{
		{name: "passes", runner: verifyRunner("0")},
		{name: "cloud-init not done", runner: verifyRunner("10"), wantErr: "cloud-init did not reach status done"},
		{name: "hostname", runner: verifyRunner("11"), wantErr: "hostname"},
		{name: "marker", runner: verifyRunner("12"), wantErr: "marker file"},
		{name: "agent", runner: verifyRunner("13"), wantErr: "walinuxagent"},
		{name: "unknown code", runner: verifyRunner("99"), wantErr: "code 99"},
		{name: "garbage code", runner: verifyRunner("x"), wantErr: "unreadable"},
		{name: "no result", runner: &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) { return statuses("hi"), nil }}, wantErr: "no result"},
		{name: "run command failed", runner: &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) {
			return []*armcompute.InstanceViewStatus{{Code: strPtr("ProvisioningState/failed"), Message: strPtr("agent down")}}, nil
		}}, wantErr: "did not run"},
		{name: "run command error", runner: &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) { return nil, errors.New("conflict") }}, wantErr: "run verify check"},
		{name: "create fails", compute: &fakeCompute{createErr: errors.New("quota")}, runner: verifyRunner("0"), wantErr: "create verify VM"},
		{name: "windows", spec: func() azureVerifySpec { s := testVerifySpec(); s.OSFamily = platform.OSFamilyWindows; return s }, runner: verifyRunner("0"), wantErr: "Linux images only"},
		{name: "no nic", spec: func() azureVerifySpec { s := testVerifySpec(); s.NetworkInterfaceID = ""; return s }, runner: verifyRunner("0"), wantErr: "networkInterfaceId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compute := tt.compute
			if compute == nil {
				compute = &fakeCompute{}
			}
			spec := testVerifySpec()
			if tt.spec != nil {
				spec = tt.spec()
			}
			err := runAzureVerifyBoot(context.Background(), compute, tt.runner, spec, "nonce123")
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), verifyBootFailedText) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want VerifyBootFailed with %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(compute.created) > 0 && len(compute.deletedVMs) < 2 {
				t.Fatalf("verify VM was not deleted after the check: %v", compute.deletedVMs)
			}
		})
	}
}

func TestRunAzureVerifyBoot_DeleteFailureIsReported(t *testing.T) {
	compute := &fakeCompute{deleteErr: errors.New("locked")}
	if err := runAzureVerifyBoot(context.Background(), compute, verifyRunner("0"), testVerifySpec(), "n"); err == nil || !strings.Contains(err.Error(), "leftover") {
		t.Fatalf("error = %v, want leftover cleanup error", err)
	}
}

func TestAzureVerifyVirtualMachine(t *testing.T) {
	spec := testVerifySpec()
	hostname := azureVerifyHostname(spec.BuildID)
	customData := base64.StdEncoding.EncodeToString([]byte(azureVerifyUserData(hostname, "n0nce")))
	vm := azureVerifyVirtualMachine(spec, hostname, customData)
	props := vm.Properties
	if *props.StorageProfile.ImageReference.ID != spec.ImageID {
		t.Fatalf("image reference = %v", *props.StorageProfile.ImageReference.ID)
	}
	if *props.StorageProfile.OSDisk.DeleteOption != armcompute.DiskDeleteOptionTypesDelete || *props.StorageProfile.OSDisk.Name != azureVerifyDiskName(spec.BuildID) {
		t.Fatal("verify OS disk must be deleted with the VM")
	}
	if *props.NetworkProfile.NetworkInterfaces[0].ID != spec.NetworkInterfaceID || *props.NetworkProfile.NetworkInterfaces[0].Properties.DeleteOption != armcompute.DeleteOptionsDetach {
		t.Fatal("verify VM must reuse and keep the configured NIC")
	}
	if *props.OSProfile.ComputerName != hostname || props.OSProfile.AdminPassword != nil || !*props.OSProfile.LinuxConfiguration.DisablePasswordAuthentication {
		t.Fatal("verify VM must use key-only login and the expected hostname")
	}
	if string(*props.HardwareProfile.VMSize) != spec.VMSize || *vm.Tags["imagebuilder.io/purpose"] != "verify-boot" || *vm.Tags["team"] != "platform" {
		t.Fatalf("size/tags = %v %v", *props.HardwareProfile.VMSize, vm.Tags)
	}
	decoded, _ := base64.StdEncoding.DecodeString(*props.OSProfile.CustomData)
	for _, want := range []string{"#cloud-config", "hostname: " + hostname, verifyBootMarkerFile, "content: n0nce"} {
		if !strings.Contains(string(decoded), want) {
			t.Fatalf("customData missing %q:\n%s", want, decoded)
		}
	}
	if len(hostname) > 64 || !strings.HasPrefix(hostname, "ib-verify-") {
		t.Fatalf("hostname = %q", hostname)
	}
	script := azureVerifyCheckScript(hostname, "n0nce")
	for _, want := range []string{"cloud-init status --wait", "'^status: done'", "walinuxagent", verifyBootResultToken} {
		if !strings.Contains(script, want) {
			t.Fatalf("check script missing %q", want)
		}
	}
	if nonce, err := newVerifyNonce(); err != nil || len(nonce) != 32 {
		t.Fatalf("nonce = %q, %v", nonce, err)
	}
}

func TestOwnedAzureImages(t *testing.T) {
	tests := []struct {
		name          string
		owner         *fakeOwner
		gallery       string
		version       string
		wantImage     string
		wantVersion   string
		wantRecordErr bool
	}{
		{name: "new image and version", owner: &fakeOwner{}, gallery: "g", version: "1.2.0", wantImage: "img", wantVersion: "1.2.0"},
		{name: "existing image and version", owner: &fakeOwner{imageExists: true, versionExists: true}, gallery: "g", version: "1.2.0"},
		{name: "no gallery", owner: &fakeOwner{}, wantImage: "img"},
		{name: "lookup error", owner: &fakeOwner{existsErr: errors.New("403")}, gallery: "g", version: "1.2.0", wantRecordErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := azureRemoteOperationRef{}
			err := recordOwnedAzureImages(context.Background(), tt.owner, &ref, "img", tt.gallery, tt.version)
			if (err != nil) != tt.wantRecordErr {
				t.Fatalf("record error = %v", err)
			}
			if tt.wantRecordErr {
				return
			}
			if ref.OwnedImage != tt.wantImage || ref.OwnedGalleryVersion != tt.wantVersion {
				t.Fatalf("owned = %q/%q", ref.OwnedImage, ref.OwnedGalleryVersion)
			}
			if err := deleteOwnedAzureImages(context.Background(), tt.owner, ref); err != nil {
				t.Fatal(err)
			}
			if (tt.wantVersion != "") != (len(tt.owner.deletedVersions) == 1) || (tt.wantImage != "") != (len(tt.owner.deletedImages) == 1) {
				t.Fatalf("deleted = %v %v", tt.owner.deletedImages, tt.owner.deletedVersions)
			}
		})
	}
	failing := &fakeOwner{deleteErr: errors.New("busy")}
	if err := deleteOwnedAzureImages(context.Background(), failing, azureRemoteOperationRef{OwnedImage: "i", OwnedGalleryVersion: "1.0.0"}); err == nil {
		t.Fatal("delete errors must be reported")
	}
}

func TestFinishAzureVerifyBoot(t *testing.T) {
	bundle := testBundle(t)
	ref := azureRemoteOperationRef{BuildID: "b", ImageID: "img", VerifyBoot: verifyBootPending, OwnedGalleryVersion: "1.2.0", OwnedImage: "x", EvidenceBlob: "blob", EvidenceSHA256: evidence.SHA256Hex(bundle)}
	input := azureRemoteBuildInput{ImageName: "x", Evidence: &v1alpha1.EvidenceSpec{Publisher: v1alpha1.EvidencePublisherController}}

	store := &fakeStore{blobs: map[string][]byte{"blob": bundle}}
	state, err := finishAzureVerifyBoot(context.Background(), &fakeCompute{}, verifyRunner("0"), &fakeOwner{}, store, testVerifySpec(), "n", input, ref, "westeurope")
	if err != nil || !state.Done || state.EvidenceBundle == nil || !containsString(state.Hygiene.Checks, verifyBootCheck) {
		t.Fatalf("state = %+v, err = %v", state, err)
	}
	parsed, _ := parseAzureRemoteOperationRef(state.OperationRef)
	if parsed.VerifyBoot != verifyBootPassed {
		t.Fatalf("op ref verifyBoot = %q", parsed.VerifyBoot)
	}

	owner := &fakeOwner{}
	_, err = finishAzureVerifyBoot(context.Background(), &fakeCompute{}, verifyRunner("12"), owner, store, testVerifySpec(), "n", input, ref, "westeurope")
	if err == nil || !strings.HasPrefix(err.Error(), verifyBootFailedText) || providererrors.IsTransient(err) {
		t.Fatalf("error = %v, want terminal VerifyBootFailed", err)
	}
	if len(owner.deletedVersions) != 1 || len(owner.deletedImages) != 1 {
		t.Fatalf("owned images not deleted: %v %v", owner.deletedImages, owner.deletedVersions)
	}

	owner = &fakeOwner{deleteErr: errors.New("busy")}
	if _, err = finishAzureVerifyBoot(context.Background(), &fakeCompute{}, verifyRunner("12"), owner, store, testVerifySpec(), "n", input, ref, "westeurope"); err == nil || !strings.Contains(err.Error(), "remove images") {
		t.Fatalf("error = %v, want cleanup error joined", err)
	}

	owner = &fakeOwner{}
	if _, err = finishAzureVerifyBoot(context.Background(), &fakeCompute{deleteErr: errors.New("locked")}, verifyRunner("0"), owner, store, testVerifySpec(), "n", input, ref, "westeurope"); err == nil || len(owner.deletedVersions) != 0 {
		t.Fatalf("infrastructure error = %v must not delete images (%v)", err, owner.deletedVersions)
	}
}

func strPtr(s string) *string { return &s }
