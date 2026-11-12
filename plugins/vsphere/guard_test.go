package vsphere

import (
	"strings"
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

func TestBuildVMNameAlwaysCarriesPrefix(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		buildID   string
	}{
		{"image name and build id", "ubuntu-2604-vsphere-v1-0-0", "0123456789abcdef"},
		{"only build id", "", "build-123"},
		{"image name that already has prefix", "imagebuilder-foo", "abcdef123456"},
		{"unsafe characters", "Ubuntu 26.04/critical-app Template!", "abcdef123456"},
		{"very long name", strings.Repeat("a", 200), "abcdef123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildVMName(tt.imageName, tt.buildID)
			if !strings.HasPrefix(got, buildVMNamePrefix) {
				t.Fatalf("buildVMName() = %q, want prefix %q", got, buildVMNamePrefix)
			}
			if len(got) > 80 {
				t.Fatalf("buildVMName() length = %d, want <= 80", len(got))
			}
			if strings.ContainsAny(got, " /!") {
				t.Fatalf("buildVMName() = %q contains unsafe characters", got)
			}
		})
	}
}

func TestBuildVMNameIsStableAndDistinguishesBuilds(t *testing.T) {
	a := buildVMName("img", "build-aaaaaaaa")
	if a != buildVMName("img", "build-aaaaaaaa") {
		t.Fatal("buildVMName must be deterministic")
	}
	if a == buildVMName("img", "build-bbbbbbbb") {
		t.Fatal("buildVMName must differ for different builds of the same image")
	}
}

func TestCheckBuildVMName(t *testing.T) {
	tests := []struct {
		name      string
		vm        string
		protected []string
		wantErr   string
	}{
		{"ok", "imagebuilder-foo-1234", nil, ""},
		{"missing prefix", "ubuntu-jammy2204-v1", nil, "prefix"},
		{"prefix only", "imagebuilder-", nil, "prefix"},
		{"protected substring", "imagebuilder-critical-app-prod", []string{"critical-app"}, "protected"},
		{"protected substring case-insensitive", "imagebuilder-CRITICAL-APP-prod", []string{"critical-app"}, "protected"},
		{"empty", "", nil, "prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBuildVMName(tt.vm, tt.protected)
			assertErrContains(t, err, tt.wantErr)
		})
	}
}

func TestCheckBuildFolderPath(t *testing.T) {
	tests := []struct {
		name      string
		folder    string
		protected []string
		wantErr   string
	}{
		{"ok", "/dc-01/vm/imagebuilder-builds", nil, ""},
		{"ok trailing slash", "/dc-01/vm/imagebuilder-builds/", nil, ""},
		{"empty", "", nil, "buildFolder"},
		{"existing foreign folder", "/dc-01/vm/templates", nil, "imagebuilder"},
		{"vm root", "/dc-01/vm", nil, "imagebuilder"},
		{"relative", "imagebuilder-builds", nil, "absolute"},
		{"protected", "/dc-01/vm/critical-app/imagebuilder-builds", []string{"critical-app"}, "protected"},
		{"parent traversal", "/dc-01/vm/../imagebuilder-builds", nil, "clean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBuildFolderPath(tt.folder, tt.protected)
			assertErrContains(t, err, tt.wantErr)
		})
	}
}

func TestCheckOwnedBuildVM(t *testing.T) {
	folder := types.ManagedObjectReference{Type: "Folder", Value: "group-v100"}
	other := types.ManagedObjectReference{Type: "Folder", Value: "group-v200"}
	tests := []struct {
		name    string
		vmName  string
		parent  *types.ManagedObjectReference
		wantErr string
	}{
		{"owned", "imagebuilder-foo-1234", &folder, ""},
		{"wrong folder", "imagebuilder-foo-1234", &other, "folder"},
		{"no parent", "imagebuilder-foo-1234", nil, "folder"},
		{"foreign name in build folder", "ubuntu-jammy2204-v1", &folder, "prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkOwnedBuildVM(tt.vmName, tt.parent, folder, nil)
			assertErrContains(t, err, tt.wantErr)
		})
	}
}

func TestCheckProtectedConfigValues(t *testing.T) {
	err := checkProtectedConfig(map[string]string{
		"datacenter": "dc-01",
		"network":    "dc-01-critical-app-net",
	}, []string{"critical-app"})
	assertErrContains(t, err, "network")
	if err := checkProtectedConfig(map[string]string{"network": "build-net"}, []string{"critical-app"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := checkProtectedConfig(map[string]string{"network": "anything-critical-app"}, nil); err != nil {
		t.Fatalf("no protected names means no check, got %v", err)
	}
}

func TestParseProtectedNames(t *testing.T) {
	got := parseProtectedNames(" CRITICAL-APP, foo ,,")
	if len(got) != 2 || got[0] != "critical-app" || got[1] != "foo" {
		t.Fatalf("parseProtectedNames = %v", got)
	}
}

func assertErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}
