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

const (
	userDataPropertyID = "user-data"
	ovfTransportGuest  = "com.vmware.guestInfo"
)

// scrubSpec clears the guestinfo configuration keys the provider injected into
// the build VM. An empty extraConfig value deletes the key. The vApp
// properties are handled by vappValueClearing: their definitions stay.
func scrubSpec(extra []types.BaseOptionValue) types.VirtualMachineConfigSpec {
	var spec types.VirtualMachineConfigSpec
	for _, o := range extra {
		ov := o.GetOptionValue()
		if isBuildGuestinfoKey(ov.Key) {
			spec.ExtraConfig = append(spec.ExtraConfig, &types.OptionValue{Key: ov.Key, Value: ""})
		}
	}
	return spec
}

// vappValueClearing keeps the vApp product and property definitions and the
// OVF environment transport, but empties every property value and default
// (the build user-data with the temporary root password lives there). Without
// the definitions a platform that delivers cloud-init through the OVF
// environment (for example vRA) cannot inject user-data into clones.
func vappValueClearing(vapp *types.VmConfigInfo) *types.VmConfigSpec {
	if vapp == nil || len(vapp.Property) == 0 {
		return nil
	}
	spec := &types.VmConfigSpec{OvfEnvironmentTransport: vapp.OvfEnvironmentTransport}
	if len(spec.OvfEnvironmentTransport) == 0 {
		spec.OvfEnvironmentTransport = []string{ovfTransportGuest}
	}
	for _, p := range vapp.Property {
		info := p
		info.Value = ""
		info.DefaultValue = ""
		spec.Property = append(spec.Property, types.VAppPropertySpec{
			ArrayUpdateSpec: types.ArrayUpdateSpec{Operation: types.ArrayUpdateOperationEdit},
			Info:            &info,
		})
	}
	return spec
}

// templateConfigFindings lists build data still present in a VM configuration:
// non-empty vApp property values or defaults and data-carrying guestinfo keys.
// Property definitions alone are not a finding. With requireUserData a missing
// user-data property definition is a finding too, because clones could not
// receive cloud-init through the OVF environment.
func templateConfigFindings(vapp *types.VmConfigInfo, extra []types.BaseOptionValue, requireUserData bool) []string {
	var out []string
	hasUserData := false
	if vapp != nil {
		for _, p := range vapp.Property {
			if p.Id == userDataPropertyID {
				hasUserData = true
			}
			if p.Value != "" {
				out = append(out, "vApp property "+p.Id+" has a value")
			}
			if p.DefaultValue != "" {
				out = append(out, "vApp property "+p.Id+" has a default value")
			}
		}
	}
	if requireUserData && !hasUserData {
		out = append(out, "vApp property "+userDataPropertyID+" definition missing")
	}
	for _, o := range extra {
		ov := o.GetOptionValue()
		if isBuildGuestinfoKey(ov.Key) {
			out = append(out, ov.Key)
		}
	}
	return out
}

// scrubBuildConfig clears the injected build configuration from the powered
// off build VM before it is published: guestinfo keys are removed and vApp
// property values and defaults are emptied, the definitions stay.
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
	spec := scrubSpec(extra)
	spec.VAppConfig = vappValueClearing(vapp)
	if len(spec.ExtraConfig) == 0 && spec.VAppConfig == nil {
		return nil
	}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return fmt.Errorf("remove build configuration from the build VM: %w", err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("remove build configuration from the build VM: %w", err)
	}
	return nil
}

// verifyPublishedTemplate inspects the configuration of the template VM that
// the Content Library created and fails when it still carries build data. It
// proves the template configuration (empty vApp property values, user-data
// definition present, no guestinfo user-data/metadata); it does not boot a clone.
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
	if findings := templateConfigFindings(vapp, props.Config.ExtraConfig, c.cfg.requireUserDataProperty); len(findings) > 0 {
		return fmt.Errorf("%w: %s", errBuildDataInTemplate, strings.Join(findings, ", "))
	}
	return nil
}
