package vsphere

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

const templateConfigCleanCheck = "vsphere-template-config-clean"

// errBuildDataInTemplate marks a published template that still carries data
// injected for the build (vApp properties or guestinfo user-data).
var errBuildDataInTemplate = errors.New("BuildDataInTemplate: the published template still carries build configuration")

// buildGuestinfoKeys are guestinfo keys that carry configuration. Keys
// maintained by VMware Tools (guestinfo.vmtools.*, guestinfo.appInfo, ...)
// are not build data.
func isBuildGuestinfoKey(key string) bool {
	if !strings.HasPrefix(key, "guestinfo.") {
		return false
	}
	switch {
	case strings.HasPrefix(key, "guestinfo.vmtools."), key == "guestinfo.appInfo":
		return false
	}
	return true
}

// scrubSpec removes the configuration the provider injected into the build VM:
// the vApp (OVF environment) properties, which carry the build user-data with
// the temporary root password, and guestinfo configuration keys. An empty
// extraConfig value deletes the key.
func scrubSpec(extra []types.BaseOptionValue) types.VirtualMachineConfigSpec {
	spec := types.VirtualMachineConfigSpec{VAppConfigRemoved: types.NewBool(true)}
	for _, o := range extra {
		ov := o.GetOptionValue()
		if isBuildGuestinfoKey(ov.Key) {
			spec.ExtraConfig = append(spec.ExtraConfig, &types.OptionValue{Key: ov.Key, Value: ""})
		}
	}
	return spec
}

// templateConfigFindings lists build data still present in a VM configuration.
func templateConfigFindings(vapp *types.VmConfigInfo, extra []types.BaseOptionValue) []string {
	var out []string
	if vapp != nil {
		for _, p := range vapp.Property {
			out = append(out, "vApp property "+p.Id)
		}
	}
	for _, o := range extra {
		ov := o.GetOptionValue()
		if isBuildGuestinfoKey(ov.Key) {
			out = append(out, ov.Key)
		}
	}
	return out
}

// vappPropertyRemovals returns the specs that delete every vApp property.
func vappPropertyRemovals(vapp *types.VmConfigInfo) []types.VAppPropertySpec {
	if vapp == nil {
		return nil
	}
	out := make([]types.VAppPropertySpec, 0, len(vapp.Property))
	for _, p := range vapp.Property {
		out = append(out, types.VAppPropertySpec{
			ArrayUpdateSpec: types.ArrayUpdateSpec{Operation: types.ArrayUpdateOperationRemove, RemoveKey: p.Key},
			Info:            &types.VAppPropertyInfo{Key: p.Key},
		})
	}
	return out
}

// scrubBuildConfig removes the injected build configuration from the powered
// off build VM before it is published: first every vApp property and the
// guestinfo keys explicitly, then the vApp configuration as a whole.
func (c *govmomiClient) scrubBuildConfig(ctx context.Context, vm *object.VirtualMachine) error {
	var props mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config.extraConfig", "config.vAppConfig"}, &props); err != nil {
		return fmt.Errorf("read build VM configuration: %w", err)
	}
	var extra []types.BaseOptionValue
	var vapp *types.VmConfigInfo
	if props.Config != nil {
		extra = props.Config.ExtraConfig
		if props.Config.VAppConfig != nil {
			vapp = props.Config.VAppConfig.GetVmConfigInfo()
		}
	}
	first := scrubSpec(extra)
	first.VAppConfigRemoved = nil
	if removals := vappPropertyRemovals(vapp); len(removals) > 0 {
		first.VAppConfig = &types.VmConfigSpec{Property: removals}
	}
	steps := []types.VirtualMachineConfigSpec{first}
	if vapp != nil {
		steps = append(steps, types.VirtualMachineConfigSpec{VAppConfigRemoved: types.NewBool(true)})
	}
	for _, spec := range steps {
		if len(spec.ExtraConfig) == 0 && spec.VAppConfig == nil && spec.VAppConfigRemoved == nil {
			continue
		}
		task, err := vm.Reconfigure(ctx, spec)
		if err != nil {
			return fmt.Errorf("remove build configuration from the build VM: %w", err)
		}
		if err := task.Wait(ctx); err != nil {
			return fmt.Errorf("remove build configuration from the build VM: %w", err)
		}
	}
	return nil
}

// verifyPublishedTemplate inspects the configuration of the template VM that
// the Content Library created and fails when it still carries build data. It
// proves the template configuration (no vApp properties, no guestinfo
// user-data/metadata); it does not boot a clone.
func (c *govmomiClient) verifyPublishedTemplate(ctx context.Context, itemName string) error {
	finder, err := c.vmFinder(ctx)
	if err != nil {
		return err
	}
	folderPath := strings.TrimRight(c.cfg.templateFolder, "/")
	tmpl, err := finder.VirtualMachine(ctx, folderPath+"/"+itemName)
	if err != nil {
		var nf *find.NotFoundError
		if errors.As(err, &nf) {
			return fmt.Errorf("verify published template: no template VM %q in %q: %w", itemName, c.cfg.templateFolder, err)
		}
		return fmt.Errorf("verify published template: %w", err)
	}
	var props mo.VirtualMachine
	if err := tmpl.Properties(ctx, tmpl.Reference(), []string{"config.vAppConfig", "config.extraConfig"}, &props); err != nil {
		return fmt.Errorf("verify published template: read configuration: %w", err)
	}
	if props.Config == nil {
		return fmt.Errorf("verify published template: no configuration returned")
	}
	var vapp *types.VmConfigInfo
	if props.Config.VAppConfig != nil {
		vapp = props.Config.VAppConfig.GetVmConfigInfo()
	}
	if findings := templateConfigFindings(vapp, props.Config.ExtraConfig); len(findings) > 0 {
		return fmt.Errorf("%w: %s", errBuildDataInTemplate, strings.Join(findings, ", "))
	}
	return nil
}
