package vsphere

import (
	"strings"
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

func TestScrubSpecClearsGuestinfoOnly(t *testing.T) {
	extra := []types.BaseOptionValue{
		&types.OptionValue{Key: "guestinfo.userdata", Value: "x"},
		&types.OptionValue{Key: "guestinfo.metadata.encoding", Value: "base64"},
		&types.OptionValue{Key: "disk.enableUUID", Value: "TRUE"},
		&types.OptionValue{Key: "guestinfo.vmtools.buildNumber", Value: "1"},
	}
	spec := scrubSpec(extra)
	if spec.VAppConfigRemoved != nil {
		t.Fatal("the vApp configuration must not be removed")
	}
	keys := map[string]string{}
	for _, o := range spec.ExtraConfig {
		ov := o.GetOptionValue()
		keys[ov.Key] = ov.Value.(string)
	}
	if len(keys) != 2 || keys["guestinfo.userdata"] != "" || keys["guestinfo.metadata.encoding"] != "" {
		t.Fatalf("only data guestinfo keys are cleared (empty value): %v", keys)
	}
}

func TestVappValueClearingKeepsDefinitions(t *testing.T) {
	vapp := &types.VmConfigInfo{
		OvfEnvironmentTransport: []string{"com.vmware.guestInfo"},
		Property: []types.VAppPropertyInfo{
			{Key: 1, Id: "user-data", Label: "Encoded user-data", Type: "string", Value: "secret", DefaultValue: "secret", UserConfigurable: types.NewBool(true)},
			{Key: 2, Id: "hostname", Type: "string", Value: "build", DefaultValue: "ubuntu"},
		},
	}
	spec := vappValueClearing(vapp)
	if spec == nil || len(spec.Property) != 2 {
		t.Fatalf("both property definitions must be edited, got %+v", spec)
	}
	if len(spec.OvfEnvironmentTransport) != 1 || spec.OvfEnvironmentTransport[0] != "com.vmware.guestInfo" {
		t.Fatalf("transport lost: %v", spec.OvfEnvironmentTransport)
	}
	for i, p := range spec.Property {
		if p.Operation != types.ArrayUpdateOperationEdit {
			t.Fatalf("property %d must be edited, not removed: %s", i, p.Operation)
		}
		if p.Info.Value != "" || p.Info.DefaultValue != "" {
			t.Fatalf("property %s keeps a value: %+v", p.Info.Id, p.Info)
		}
		if p.Info.Key != vapp.Property[i].Key || p.Info.Id != vapp.Property[i].Id || p.Info.Type != vapp.Property[i].Type {
			t.Fatalf("definition changed: %+v", p.Info)
		}
	}
	if vapp.Property[0].Value != "secret" {
		t.Fatal("input must not be mutated")
	}
	if vappValueClearing(nil) != nil || vappValueClearing(&types.VmConfigInfo{}) != nil {
		t.Fatal("nothing to clear without properties")
	}
	if got := vappValueClearing(&types.VmConfigInfo{Property: vapp.Property}); got.OvfEnvironmentTransport[0] != "com.vmware.guestInfo" {
		t.Fatalf("missing transport defaults to guestInfo: %v", got.OvfEnvironmentTransport)
	}
}

func TestTemplateConfigFindings(t *testing.T) {
	disk := []types.BaseOptionValue{&types.OptionValue{Key: "disk.enableUUID", Value: "TRUE"}}
	defs := &types.VmConfigInfo{Property: []types.VAppPropertyInfo{{Id: "user-data"}, {Id: "hostname"}}}
	if f := templateConfigFindings(defs, disk, true); len(f) != 0 {
		t.Fatalf("empty property definitions must be clean: %v", f)
	}
	for name, vapp := range map[string]*types.VmConfigInfo{
		"value":   {Property: []types.VAppPropertyInfo{{Id: "user-data", Value: "abc"}}},
		"default": {Property: []types.VAppPropertyInfo{{Id: "user-data", DefaultValue: "abc"}}},
	} {
		f := templateConfigFindings(vapp, nil, true)
		if len(f) != 1 || !strings.Contains(f[0], "vApp property user-data") {
			t.Fatalf("%s: non-empty property must fail: %v", name, f)
		}
	}
	got := templateConfigFindings(defs, []types.BaseOptionValue{
		&types.OptionValue{Key: "guestinfo.userdata", Value: "abc"},
		&types.OptionValue{Key: "guestinfo.vmtools.buildNumber", Value: "123"},
	}, true)
	if len(got) != 1 || got[0] != "guestinfo.userdata" {
		t.Fatalf("only data guestinfo keys are findings: %v", got)
	}
	// Missing user-data definition: finding when required, ignored otherwise.
	if f := templateConfigFindings(nil, nil, true); len(f) != 1 || !strings.Contains(f[0], "definition missing") {
		t.Fatalf("missing user-data definition must fail: %v", f)
	}
	if f := templateConfigFindings(&types.VmConfigInfo{Property: []types.VAppPropertyInfo{{Id: "hostname"}}}, nil, true); len(f) != 1 {
		t.Fatalf("user-data definition required: %v", f)
	}
	if f := templateConfigFindings(nil, nil, false); len(f) != 0 {
		t.Fatalf("assertion disabled: %v", f)
	}
}
