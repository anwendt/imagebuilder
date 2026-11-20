package vsphere

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vapi/library"
	"github.com/vmware/govmomi/vapi/vcenter"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

// Verify boot (ADR-011, opt-in with ProviderConfig extra remote.verifyBoot):
// after publishing, a throwaway VM is deployed from the published Content
// Library template into the build folder, receives its cloud-init exactly as
// VMware Aria Automation delivers it (OVF vApp property user-data) and is
// checked through Guest Operations before the build is reported Done.

const (
	verifyBootCheck   = "verify-boot-passed"
	verifyBootPending = "pending"
	verifyBootPassed  = "passed"
	verifyMarkerPath  = "/var/lib/imagebuilder-verify/marker"
	verifyLoginPoll   = 10 * time.Second
)

// verifyVMName is the guarded name of the verify VM (imagebuilder-verify-...).
func verifyVMName(imageName, buildID string) string {
	return buildVMName("verify-"+firstNonEmpty(imageName, "image"), buildID)
}

func verifyHostname(buildID string) string {
	suffix := sanitizeName(buildID)
	suffix = strings.NewReplacer("_", "-", ".", "-").Replace(suffix)
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return "ib-verify-" + strings.Trim(suffix, "-")
}

func verifyGuestPassword(providerSecret, buildID string) string {
	return deriveGuestPassword(providerSecret, "verify-"+buildID)
}

func newVerifyNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create verify nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// verifyUserData is the cloud-config of the verify VM: hostname, a marker
// file and a temporary root login for Guest Operations.
func verifyUserData(hostname, rootPassword, nonce string) string {
	return strings.Join([]string{
		"#cloud-config",
		"hostname: " + hostname,
		"preserve_hostname: false",
		"disable_root: false",
		"ssh_pwauth: false",
		"chpasswd:",
		"  expire: false",
		"  users:",
		"    - name: root",
		"      password: " + rootPassword,
		"      type: text",
		"write_files:",
		"  - path: " + verifyMarkerPath,
		"    permissions: '0600'",
		"    content: " + nonce,
		"",
	}, "\n")
}

// verifyCheckScript checks the booted verify VM. Exit codes are mapped by
// verifyExitMessage.
func verifyCheckScript(hostname, nonce, agentService string) string {
	return strings.Join([]string{
		"cloud-init status --wait >/dev/null 2>&1 || true",
		"cloud-init status 2>/dev/null | grep -q '^status: done' || exit 10",
		"[ \"$(hostname)\" = " + shellQuote(hostname) + " ] || exit 11",
		"[ \"$(tr -d '\\n' < " + verifyMarkerPath + " 2>/dev/null)\" = " + shellQuote(nonce) + " ] || exit 12",
		agentCheck(agentService),
		"exit 0",
	}, "\n")
}

func agentCheck(services string) string {
	parts := strings.Split(services, "|")
	checks := make([]string, 0, len(parts))
	for _, s := range parts {
		checks = append(checks, "systemctl is-active --quiet "+shellQuote(s))
	}
	return "{ " + strings.Join(checks, " || ") + "; } || exit 13"
}

func verifyExitMessage(code int32) string {
	switch code {
	case 10:
		return "cloud-init did not reach status done"
	case 11:
		return "the hostname from the provided user-data was not applied"
	case 12:
		return "the marker file written by the provided user-data is missing or wrong"
	case 13:
		return "the platform agent (open-vm-tools) is not active"
	default:
		return fmt.Sprintf("the verify check exited with code %d", code)
	}
}

// vappUserDataSpec sets only the value of the existing user-data vApp
// property. A template without the property definition cannot receive
// cloud-init the way VMware Aria Automation delivers it.
func vappUserDataSpec(vapp *types.VmConfigInfo, value string) (*types.VmConfigSpec, error) {
	if vapp != nil {
		for _, p := range vapp.Property {
			if p.Id != userDataPropertyID {
				continue
			}
			info := p
			info.Value = value
			return &types.VmConfigSpec{Property: []types.VAppPropertySpec{{
				ArrayUpdateSpec: types.ArrayUpdateSpec{Operation: types.ArrayUpdateOperationEdit},
				Info:            &info,
			}}}, nil
		}
	}
	return nil, fmt.Errorf("the published template has no OVF vApp property %q definition, so clones cannot receive cloud-init through the OVF environment", userDataPropertyID)
}

// verifyGuest waits until the verify login works and runs the checks.
func verifyGuest(ctx context.Context, g guestSession, hostname, nonce string, loginAttempts int, wait func(context.Context, time.Duration) error) error {
	var lastErr error
	for attempt := 1; ; attempt++ {
		lastErr = g.Run(ctx, "true")
		if lastErr == nil {
			break
		}
		if attempt >= loginAttempts {
			return fmt.Errorf("guest operations did not accept the verify login (cloud-init did not apply the provided user-data): %w", lastErr)
		}
		if err := wait(ctx, verifyLoginPoll); err != nil {
			return err
		}
	}
	err := g.Run(ctx, verifyCheckScript(hostname, nonce, "open-vm-tools|vmtoolsd"))
	var exitErr *guestExitError
	if errors.As(err, &exitErr) {
		return errors.New(verifyExitMessage(exitErr.code))
	}
	if err != nil {
		return fmt.Errorf("run verify checks: %w", err)
	}
	return nil
}

// verifyBootStep runs the verify boot and turns a failure into VerifyBootFailed.
func (c *govmomiClient) verifyBootStep(ctx context.Context, input vsphereRemoteBuildInput, ref vsphereRemoteOperationRef) (*vsphereRemoteBuildState, error) {
	if err := c.runVerifyBoot(ctx, input, ref.ImageRef); err != nil {
		if derr := c.deleteOwnedLibraryItem(ctx, ref.ImageRef, input.BuildID); derr != nil {
			err = fmt.Errorf("%w; removing the unverified template failed: %v", err, derr)
		}
		return nil, providererrors.Terminal(fmt.Errorf("VerifyBootFailed: %w", err))
	}
	ref.VerifyBoot = verifyBootPassed
	return c.doneState(input, ref)
}

// runVerifyBoot deploys the verify VM from the published template, checks
// it and always destroys it.
func (c *govmomiClient) runVerifyBoot(ctx context.Context, input vsphereRemoteBuildInput, itemID string) (retErr error) {
	name := verifyVMName(input.ImageName, input.BuildID)
	if err := checkBuildVMName(name, c.cfg.protectedNames); err != nil {
		return err
	}
	dc, ds, err := c.resolveDatastore(ctx, c.cfg.datacenter, c.cfg.datastore)
	if err != nil {
		return err
	}
	finder := c.finder
	finder.SetDatacenter(dc)
	rp, err := c.resolveResourcePool(ctx, finder)
	if err != nil {
		return err
	}
	host, err := c.resolveHost(ctx, finder)
	if err != nil {
		return err
	}
	folder, err := c.ensureBuildFolder(ctx, finder)
	if err != nil {
		return err
	}
	if err := c.destroyVerifyVM(ctx, name); err != nil {
		return fmt.Errorf("remove leftover verify VM %q: %w", name, err)
	}
	restClient, err := c.restClient(ctx)
	if err != nil {
		return err
	}
	item, err := library.NewManager(restClient).GetLibraryItem(ctx, itemID)
	if err != nil {
		return fmt.Errorf("read published content library item %q: %w", itemID, err)
	}
	moref, err := c.deployContentLibraryItem(ctx, vcenter.NewManager(restClient), item, name, ds.Reference().Value, rp.Reference().Value, hostReferenceValue(host), folder.Reference().Value)
	if err != nil {
		return fmt.Errorf("deploy verify VM from the published template: %w", err)
	}
	vm := object.NewVirtualMachine(c.vc.Client, *moref)
	defer func() {
		if err := c.destroyOwnedBuildVM(context.WithoutCancel(ctx), vm); err != nil && retErr == nil {
			retErr = fmt.Errorf("destroy verify VM %q: %w", name, err)
		}
	}()
	if err := c.requireOwnedBuildVM(ctx, vm); err != nil {
		return err
	}
	var props mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config.vAppConfig"}, &props); err != nil {
		return fmt.Errorf("read verify VM configuration: %w", err)
	}
	var vapp *types.VmConfigInfo
	if props.Config != nil && props.Config.VAppConfig != nil {
		vapp = props.Config.VAppConfig.GetVmConfigInfo()
	}
	nonce, err := newVerifyNonce()
	if err != nil {
		return err
	}
	hostname := verifyHostname(input.BuildID)
	password := verifyGuestPassword(c.cfg.password, input.BuildID)
	vappSpec, err := vappUserDataSpec(vapp, ovfUserDataProperty(verifyUserData(hostname, password, nonce)))
	if err != nil {
		return err
	}
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{VAppConfig: vappSpec})
	if err == nil {
		err = task.Wait(ctx)
	}
	if err != nil {
		return fmt.Errorf("set the user-data vApp property of the verify VM: %w", err)
	}
	powerOn, err := vm.PowerOn(ctx)
	if err == nil {
		err = powerOn.Wait(ctx)
	}
	if err != nil {
		return fmt.Errorf("power on verify VM: %w", err)
	}
	session := &vmGuestSession{c: c, vm: vm, auth: &types.NamePasswordAuthentication{Username: "root", Password: password}}
	return verifyGuest(ctx, session, hostname, nonce, int(guestAuthWait/verifyLoginPoll), sleepCtx)
}

// destroyVerifyVM removes a verify VM of this build by name from the build
// folder (guarded).
func (c *govmomiClient) destroyVerifyVM(ctx context.Context, name string) error {
	if err := checkBuildVMName(name, c.cfg.protectedNames); err != nil {
		return err
	}
	finder, err := c.vmFinder(ctx)
	if err != nil {
		return err
	}
	vm, err := finder.VirtualMachine(ctx, strings.TrimRight(c.cfg.buildFolder, "/")+"/"+name)
	if err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		return err
	}
	return c.destroyOwnedBuildVM(ctx, vm)
}

// deleteOwnedLibraryItem deletes a content library item only when its
// description carries this build's marker.
func (c *govmomiClient) deleteOwnedLibraryItem(ctx context.Context, itemID, buildID string) error {
	if itemID == "" {
		return nil
	}
	restClient, err := c.restClient(ctx)
	if err != nil {
		return err
	}
	manager := library.NewManager(restClient)
	item, err := manager.GetLibraryItem(ctx, itemID)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil
		}
		return fmt.Errorf("read content library item %q: %w", itemID, err)
	}
	if !ownsLibraryItem(item, buildID) {
		return fmt.Errorf("content library item %q does not carry the marker of build %s; it is left untouched", itemID, buildID)
	}
	if err := manager.DeleteLibraryItem(ctx, item); err != nil {
		return fmt.Errorf("delete content library item %q: %w", itemID, err)
	}
	return nil
}

func ownsLibraryItem(item *library.Item, buildID string) bool {
	if item == nil || item.Description == nil || buildID == "" {
		return false
	}
	for _, line := range strings.Split(*item.Description, "\n") {
		if strings.TrimSpace(line) == "imagebuilder build "+buildID {
			return true
		}
	}
	return false
}
