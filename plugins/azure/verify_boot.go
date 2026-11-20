package azure

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"

	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// Verify boot (ADR-011, opt-in with ProviderConfig extra remote.verifyBoot):
// after registration the provider boots a throwaway VM from the published
// image with customData, smoke-tests it through Run Command and deletes it.

const (
	verifyBootPending     = "pending"
	verifyBootPassed      = "passed"
	verifyBootCheck       = "verify-boot-passed"
	verifyBootFailedText  = "VerifyBootFailed: "
	verifyBootMarkerFile  = "/var/lib/imagebuilder-verify/marker"
	verifyBootResultToken = "IMAGEBUILDER_VERIFY_BOOT_RESULT="
)

// azureVerifyCompute creates and deletes the throwaway verify VM.
type azureVerifyCompute interface {
	CreateVM(ctx context.Context, name string, vm armcompute.VirtualMachine) error
	// DeleteVM and DeleteDisk are idempotent (404 is success).
	DeleteVM(ctx context.Context, name string) error
	DeleteDisk(ctx context.Context, name string) error
}

// azureImageOwner deletes images this build created when the verify fails.
type azureImageOwner interface {
	ManagedImageExists(ctx context.Context, name string) (bool, error)
	GalleryVersionExists(ctx context.Context, version string) (bool, error)
	DeleteManagedImage(ctx context.Context, name string) error
	DeleteGalleryVersion(ctx context.Context, version string) error
}

func azureVerifyVMName(buildID string) string {
	return "ib-" + sanitizeName(buildID) + "-verify"
}

func azureVerifyDiskName(buildID string) string {
	return "ib-" + sanitizeName(buildID) + "-verify-osdisk"
}

func azureVerifyHostname(buildID string) string {
	suffix := sanitizeName(buildID)
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return strings.Trim("ib-verify-"+suffix, "-")
}

func newVerifyNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate verify nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// azureVerifyUserData is the cloud-config handed to the verify VM as customData.
func azureVerifyUserData(hostname, nonce string) string {
	return strings.Join([]string{
		"#cloud-config",
		"hostname: " + hostname,
		"preserve_hostname: false",
		"write_files:",
		"  - path: " + verifyBootMarkerFile,
		"    permissions: '0600'",
		"    content: " + nonce,
		"",
	}, "\n")
}

// azureVerifyCheckScript prints one result token; Run Command output carries
// it independent of the extension's exit-code handling.
func azureVerifyCheckScript(hostname, nonce string) string {
	return strings.Join([]string{
		"rc=0",
		"cloud-init status --wait >/dev/null 2>&1 || true",
		"if ! cloud-init status 2>/dev/null | grep -q '^status: done'; then rc=10",
		"elif [ \"$(hostname)\" != " + evidence.ShellQuote(hostname) + " ]; then rc=11",
		"elif [ \"$(tr -d '[:space:]' < " + verifyBootMarkerFile + " 2>/dev/null)\" != " + evidence.ShellQuote(nonce) + " ]; then rc=12",
		"elif ! systemctl is-active --quiet walinuxagent && ! systemctl is-active --quiet waagent; then rc=13",
		"fi",
		"echo " + verifyBootResultToken + "$rc",
		"exit 0",
	}, "\n")
}

var verifyBootMessages = map[int]string{
	10: "cloud-init did not reach status done",
	11: "the hostname from the provided user-data was not applied",
	12: "the marker file written by the provided user-data is missing or wrong",
	13: "the Azure agent (walinuxagent) is not active",
}

func parseAzureVerifyResult(statuses []*armcompute.InstanceViewStatus) error {
	output := runCommandOutput(statuses)
	index := strings.LastIndex(output, verifyBootResultToken)
	if index < 0 {
		if err := azureRunCommandError(statuses); err != nil {
			return fmt.Errorf("verify check did not run: %w", err)
		}
		return fmt.Errorf("verify check returned no result")
	}
	raw := strings.TrimSpace(output[index+len(verifyBootResultToken):])
	if end := strings.IndexAny(raw, "\r\n \t"); end >= 0 {
		raw = raw[:end]
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("verify check returned an unreadable result %q", raw)
	}
	if code == 0 {
		return nil
	}
	if message, ok := verifyBootMessages[code]; ok {
		return errors.New(message)
	}
	return fmt.Errorf("verify check failed with code %d", code)
}

type azureVerifySpec struct {
	BuildID            string
	Location           string
	VMSize             string
	NetworkInterfaceID string
	ImageID            string
	OSFamily           platform.OSFamily
	StorageAccountType armcompute.StorageAccountTypes
	Tags               map[string]string
}

func azureVerifyVirtualMachine(spec azureVerifySpec, hostname, customData string) armcompute.VirtualMachine {
	const adminUser = "imagebuilder"
	tags := cloneStringMap(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags["imagebuilder.io/purpose"] = "verify-boot"
	osDisk := &armcompute.OSDisk{
		Name:         to.Ptr(azureVerifyDiskName(spec.BuildID)),
		CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
		OSType:       to.Ptr(azureOSType(spec.OSFamily)),
		Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
		DeleteOption: to.Ptr(armcompute.DiskDeleteOptionTypesDelete),
	}
	if spec.StorageAccountType != "" {
		osDisk.ManagedDisk = &armcompute.ManagedDiskParameters{StorageAccountType: to.Ptr(spec.StorageAccountType)}
	}
	return armcompute.VirtualMachine{
		Location: to.Ptr(spec.Location),
		Tags:     azureTags(tags),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(spec.VMSize))},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{
					ID: to.Ptr(spec.NetworkInterfaceID),
					Properties: &armcompute.NetworkInterfaceReferenceProperties{
						Primary:      to.Ptr(true),
						DeleteOption: to.Ptr(armcompute.DeleteOptionsDetach),
					},
				}},
			},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{ID: to.Ptr(spec.ImageID)},
				OSDisk:         osDisk,
			},
			OSProfile: &armcompute.OSProfile{
				AdminUsername: to.Ptr(adminUser),
				ComputerName:  to.Ptr(hostname),
				CustomData:    to.Ptr(customData),
				LinuxConfiguration: &armcompute.LinuxConfiguration{
					DisablePasswordAuthentication: to.Ptr(true),
					ProvisionVMAgent:              to.Ptr(true),
					SSH: &armcompute.SSHConfiguration{
						PublicKeys: []*armcompute.SSHPublicKey{{
							Path:    to.Ptr("/home/" + adminUser + "/.ssh/authorized_keys"),
							KeyData: to.Ptr(azureRemoteBuildPublicKey),
						}},
					},
				},
			},
		},
	}
}

// runAzureVerifyBoot boots the published image once and smoke-tests it. The
// verify VM and its OS disk are always deleted; the NIC stays.
func runAzureVerifyBoot(ctx context.Context, compute azureVerifyCompute, runner azureShellRunner, spec azureVerifySpec, nonce string) (retErr error) {
	if spec.OSFamily != platform.OSFamilyLinux {
		return errors.New(verifyBootFailedText + "verify boot supports Linux images only")
	}
	if spec.NetworkInterfaceID == "" {
		return errors.New(verifyBootFailedText + "verify boot requires ProviderConfig extra remote.networkInterfaceId")
	}
	vmName := azureVerifyVMName(spec.BuildID)
	diskName := azureVerifyDiskName(spec.BuildID)
	if err := cleanupAzureVerifyVM(ctx, compute, vmName, diskName); err != nil {
		return fmt.Errorf("remove leftover verify VM: %w", err)
	}
	defer func() {
		if err := cleanupAzureVerifyVM(ctx, compute, vmName, diskName); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("delete verify VM %q: %w", vmName, err))
		}
	}()
	hostname := azureVerifyHostname(spec.BuildID)
	customData := base64.StdEncoding.EncodeToString([]byte(azureVerifyUserData(hostname, nonce)))
	if err := compute.CreateVM(ctx, vmName, azureVerifyVirtualMachine(spec, hostname, customData)); err != nil {
		return fmt.Errorf("%screate verify VM from %s: %w", verifyBootFailedText, spec.ImageID, err)
	}
	statuses, err := runner.RunShellScript(ctx, vmName, azureVerifyCheckScript(hostname, nonce))
	if err != nil {
		return fmt.Errorf("%srun verify check: %w", verifyBootFailedText, err)
	}
	if err := parseAzureVerifyResult(statuses); err != nil {
		return errors.New(verifyBootFailedText + err.Error())
	}
	return nil
}

func cleanupAzureVerifyVM(ctx context.Context, compute azureVerifyCompute, vmName, diskName string) error {
	return errors.Join(compute.DeleteVM(ctx, vmName), compute.DeleteDisk(ctx, diskName))
}

// recordOwnedAzureImages notes which images this build is about to create, so
// a failed verify only deletes what did not exist before.
func recordOwnedAzureImages(ctx context.Context, owner azureImageOwner, ref *azureRemoteOperationRef, imageName, galleryName, galleryVersion string) error {
	exists, err := owner.ManagedImageExists(ctx, imageName)
	if err != nil {
		return fmt.Errorf("check managed image %q: %w", imageName, err)
	}
	if !exists {
		ref.OwnedImage = imageName
	}
	if galleryName == "" || galleryVersion == "" {
		return nil
	}
	exists, err = owner.GalleryVersionExists(ctx, galleryVersion)
	if err != nil {
		return fmt.Errorf("check gallery image version %q: %w", galleryVersion, err)
	}
	if !exists {
		ref.OwnedGalleryVersion = galleryVersion
	}
	return nil
}

// deleteOwnedAzureImages removes the images this build created.
func deleteOwnedAzureImages(ctx context.Context, owner azureImageOwner, ref azureRemoteOperationRef) error {
	var errs []error
	if ref.OwnedGalleryVersion != "" {
		if err := owner.DeleteGalleryVersion(ctx, ref.OwnedGalleryVersion); err != nil {
			errs = append(errs, fmt.Errorf("delete gallery image version %q: %w", ref.OwnedGalleryVersion, err))
		}
	}
	if ref.OwnedImage != "" {
		if err := owner.DeleteManagedImage(ctx, ref.OwnedImage); err != nil {
			errs = append(errs, fmt.Errorf("delete managed image %q: %w", ref.OwnedImage, err))
		}
	}
	return errors.Join(errs...)
}

// sdkVerifyOps implements azureVerifyCompute and azureImageOwner on sdkClient.
type sdkVerifyOps struct{ c *sdkClient }

func (o sdkVerifyOps) CreateVM(ctx context.Context, name string, vm armcompute.VirtualMachine) error {
	poller, err := o.c.vms.BeginCreateOrUpdate(ctx, o.c.cfg.resourceGroup, name, vm, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func (o sdkVerifyOps) DeleteVM(ctx context.Context, name string) error {
	poller, err := o.c.vms.BeginDelete(ctx, o.c.cfg.resourceGroup, name, nil)
	if err != nil {
		return ignoreNotFound(err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return ignoreNotFound(err)
}

func (o sdkVerifyOps) DeleteDisk(ctx context.Context, name string) error {
	poller, err := o.c.disks.BeginDelete(ctx, o.c.cfg.resourceGroup, name, nil)
	if err != nil {
		return ignoreNotFound(err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return ignoreNotFound(err)
}

func (o sdkVerifyOps) ManagedImageExists(ctx context.Context, name string) (bool, error) {
	_, err := o.c.images.Get(ctx, o.c.cfg.resourceGroup, name, nil)
	return existsFromErr(err)
}

func (o sdkVerifyOps) GalleryVersionExists(ctx context.Context, version string) (bool, error) {
	_, err := o.c.galleryVersions.Get(ctx, o.c.cfg.resourceGroup, o.c.cfg.galleryName, o.c.cfg.galleryImageName, version, nil)
	return existsFromErr(err)
}

func (o sdkVerifyOps) DeleteManagedImage(ctx context.Context, name string) error {
	poller, err := o.c.images.BeginDelete(ctx, o.c.cfg.resourceGroup, name, nil)
	if err != nil {
		return ignoreNotFound(err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return ignoreNotFound(err)
}

func (o sdkVerifyOps) DeleteGalleryVersion(ctx context.Context, version string) error {
	poller, err := o.c.galleryVersions.BeginDelete(ctx, o.c.cfg.resourceGroup, o.c.cfg.galleryName, o.c.cfg.galleryImageName, version, nil)
	if err != nil {
		return ignoreNotFound(err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return ignoreNotFound(err)
}

func ignoreNotFound(err error) error {
	if err == nil || isHTTPStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func existsFromErr(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if isHTTPStatus(err, http.StatusNotFound) {
		return false, nil
	}
	return false, err
}

func (c *sdkClient) verifyCompute() azureVerifyCompute {
	if c.verify != nil {
		return c.verify
	}
	return sdkVerifyOps{c: c}
}

func (c *sdkClient) imageOwner() azureImageOwner {
	if c.owner != nil {
		return c.owner
	}
	return sdkVerifyOps{c: c}
}
