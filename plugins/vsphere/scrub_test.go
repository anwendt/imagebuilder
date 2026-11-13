package vsphere

import (
	"github.com/vmware/govmomi/vim25/xml"
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
			{Key: 1, Id: "user-data", Label: "Encoded user-data", Type: "string", Value: "secret-ud", DefaultValue: "secret-ud", UserConfigurable: types.NewBool(true)},
			{Key: 2, Id: "hostname", Type: "string", Value: "build-host", DefaultValue: "ubuntu"},
		},
	}
	steps := vappValueClearing(vapp)
	if len(steps) != 2 {
		t.Fatalf("want remove and add steps, got %d", len(steps))
	}
	remove, add := steps[0], steps[1]
	for i, p := range vapp.Property {
		if remove.Property[i].Operation != types.ArrayUpdateOperationRemove || remove.Property[i].RemoveKey != p.Key {
			t.Fatalf("step 1 must remove property key %d: %+v", p.Key, remove.Property[i])
		}
		a := add.Property[i]
		if a.Operation != types.ArrayUpdateOperationAdd || a.Info == nil {
			t.Fatalf("step 2 must add the definition: %+v", a)
		}
		if a.Info.Key != p.Key || a.Info.Id != p.Id || a.Info.Type != p.Type || a.Info.Label != p.Label {
			t.Fatalf("definition changed: %+v", a.Info)
		}
		if a.Info.Value != "" || a.Info.DefaultValue != "" {
			t.Fatalf("added property %s keeps a value: %+v", p.Id, a.Info)
		}
	}
	if len(add.OvfEnvironmentTransport) != 1 || add.OvfEnvironmentTransport[0] != "com.vmware.guestInfo" {
		t.Fatalf("transport lost: %v", add.OvfEnvironmentTransport)
	}
	if vapp.Property[0].Value != "secret-ud" {
		t.Fatal("input must not be mutated")
	}
	if vappValueClearing(nil) != nil || vappValueClearing(&types.VmConfigInfo{}) != nil {
		t.Fatal("nothing to clear without properties")
	}
	if got := vappValueClearing(&types.VmConfigInfo{Property: vapp.Property}); got[1].OvfEnvironmentTransport[0] != "com.vmware.guestInfo" {
		t.Fatalf("missing transport defaults to guestInfo: %v", got[1].OvfEnvironmentTransport)
	}
}

// TestVappClearingWireFormat guards the real-vCenter bug: govmomi omits empty
// Value/DefaultValue, so an edit to "" never reaches vCenter. The serialised
// requests must not contain a secret value at all, must remove the old
// properties and must add the definitions.
func TestVappClearingWireFormat(t *testing.T) {
	vapp := &types.VmConfigInfo{Property: []types.VAppPropertyInfo{
		{Key: 1, Id: "user-data", Type: "string", Value: "secret-ud", DefaultValue: "secret-dv"},
	}}
	var wire []string
	for _, v := range vappValueClearing(vapp) {
		b, err := xml.Marshal(types.VirtualMachineConfigSpec{VAppConfig: v})
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, string(b))
	}
	all := strings.Join(wire, "\n")
	if strings.Contains(all, "secret") || strings.Contains(all, "<value") || strings.Contains(all, "<defaultValue") {
		t.Fatalf("a value reaches the wire: %s", all)
	}
	if !strings.Contains(wire[0], "remove") || !strings.Contains(wire[1], "add") || !strings.Contains(wire[1], "<id>user-data</id>") {
		t.Fatalf("expected remove then add of the definition: %v", wire)
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
