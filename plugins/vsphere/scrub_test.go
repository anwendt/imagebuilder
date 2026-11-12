package vsphere

import (
	"strings"
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

func TestScrubSpecClearsGuestinfoAndRemovesVApp(t *testing.T) {
	extra := []types.BaseOptionValue{
		&types.OptionValue{Key: "guestinfo.userdata", Value: "x"},
		&types.OptionValue{Key: "guestinfo.metadata.encoding", Value: "base64"},
		&types.OptionValue{Key: "disk.enableUUID", Value: "TRUE"},
		&types.OptionValue{Key: "nvram", Value: "n"},
	}
	spec := scrubSpec(extra)
	if spec.VAppConfigRemoved == nil || !*spec.VAppConfigRemoved {
		t.Fatal("vApp configuration must be removed")
	}
	keys := map[string]string{}
	for _, o := range spec.ExtraConfig {
		ov := o.GetOptionValue()
		keys[ov.Key] = ov.Value.(string)
	}
	if len(keys) != 2 || keys["guestinfo.userdata"] != "" || keys["guestinfo.metadata.encoding"] != "" {
		t.Fatalf("only guestinfo keys are cleared (empty value): %v", keys)
	}
}

func TestTemplateConfigFindings(t *testing.T) {
	clean := templateConfigFindings(nil, []types.BaseOptionValue{&types.OptionValue{Key: "disk.enableUUID", Value: "TRUE"}})
	if len(clean) != 0 {
		t.Fatalf("clean template reported findings: %v", clean)
	}
	vapp := &types.VmConfigInfo{Property: []types.VAppPropertyInfo{{Id: "user-data"}}}
	got := templateConfigFindings(vapp, []types.BaseOptionValue{
		&types.OptionValue{Key: "guestinfo.userdata", Value: "abc"},
		&types.OptionValue{Key: "guestinfo.vmtools.buildNumber", Value: "123"},
	})
	joined := strings.Join(got, ";")
	for _, want := range []string{"vApp property user-data", "guestinfo.userdata"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("findings %v missing %q", got, want)
		}
	}
	if strings.Contains(joined, "vmtools") {
		t.Fatalf("tools-maintained guestinfo keys are not build data: %v", got)
	}
	// An empty vApp config without properties is clean.
	if f := templateConfigFindings(&types.VmConfigInfo{}, nil); len(f) != 0 {
		t.Fatalf("empty vApp config reported: %v", f)
	}
}
