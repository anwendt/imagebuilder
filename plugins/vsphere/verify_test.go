package vsphere

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/vapi/library"
	"github.com/vmware/govmomi/vim25/types"
)

func TestVerifyNames(t *testing.T) {
	name := verifyVMName("ubuntu-2604-base", "0f1e2d3c-aaaa-bbbb-cccc-1234567890ab")
	if err := checkBuildVMName(name, nil); err != nil || !strings.HasPrefix(name, "imagebuilder-verify-ubuntu-2604-base-") {
		t.Fatalf("verify VM name %q: %v", name, err)
	}
	if got := verifyHostname("0f1e2d3c-aaaa-bbbb-cccc-1234567890ab"); got != "ib-verify-567890ab" {
		t.Fatalf("hostname = %q", got)
	}
	if got := verifyHostname("ab"); got != "ib-verify-ab" {
		t.Fatalf("short hostname = %q", got)
	}
	if verifyGuestPassword("s", "b") == deriveGuestPassword("s", "b") {
		t.Fatal("verify password must differ from the build password")
	}
	n1, err := newVerifyNonce()
	n2, _ := newVerifyNonce()
	if err != nil || len(n1) != 32 || n1 == n2 {
		t.Fatalf("nonce = %q %q %v", n1, n2, err)
	}
}

func TestVerifyUserDataAndScript(t *testing.T) {
	ud := verifyUserData("ib-verify-1", "pw", "nonce1")
	for _, want := range []string{"#cloud-config", "hostname: ib-verify-1", "password: pw", "ssh_pwauth: false", verifyMarkerPath, "content: nonce1"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing %q:\n%s", want, ud)
		}
	}
	script := verifyCheckScript("ib-verify-1", "nonce1", "open-vm-tools|vmtoolsd")
	for _, want := range []string{"cloud-init status --wait", "^status: done", "exit 10", "'ib-verify-1'", "'nonce1'", "systemctl is-active --quiet 'open-vm-tools' || systemctl is-active --quiet 'vmtoolsd'", "exit 13"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	for code, want := range map[int32]string{10: "cloud-init", 11: "hostname", 12: "marker", 13: "open-vm-tools", 99: "code 99"} {
		if !strings.Contains(verifyExitMessage(code), want) {
			t.Fatalf("exit %d message = %q", code, verifyExitMessage(code))
		}
	}
}

func TestVAppUserDataSpec(t *testing.T) {
	value := base64.StdEncoding.EncodeToString([]byte("#cloud-config"))
	vapp := &types.VmConfigInfo{Property: []types.VAppPropertyInfo{
		{Key: 1, Id: "hostname", Type: "string"},
		{Key: 2, Id: userDataPropertyID, Type: "string"},
	}}
	spec, err := vappUserDataSpec(vapp, value)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Property) != 1 || spec.Property[0].Operation != types.ArrayUpdateOperationEdit || spec.Property[0].Info.Key != 2 || spec.Property[0].Info.Value != value || spec.Property[0].Info.Type != "string" {
		t.Fatalf("spec = %+v", spec.Property)
	}
	if vapp.Property[1].Value != "" {
		t.Fatal("input must not be modified")
	}
	for _, missing := range []*types.VmConfigInfo{nil, {Property: []types.VAppPropertyInfo{{Key: 1, Id: "hostname"}}}} {
		if _, err := vappUserDataSpec(missing, value); err == nil || !strings.Contains(err.Error(), "user-data") {
			t.Fatalf("missing user-data property: %v", err)
		}
	}
}

func TestVerifyGuest(t *testing.T) {
	authErr := errors.New("Failed to authenticate with the guest operating system")
	noWait := func(context.Context, time.Duration) error { return nil }
	tests := []struct {
		name     string
		runErr   map[int]error
		attempts int
		wait     func(context.Context, time.Duration) error
		wantErr  string
		wantRuns int
	}{
		{name: "passes after login retries", runErr: map[int]error{0: authErr, 1: authErr}, attempts: 5, wantRuns: 4},
		{name: "login never works", runErr: map[int]error{0: authErr, 1: authErr, 2: authErr}, attempts: 3, wantErr: "did not accept the verify login", wantRuns: 3},
		{name: "wait cancelled", runErr: map[int]error{0: authErr}, attempts: 3, wait: func(context.Context, time.Duration) error { return context.Canceled }, wantErr: "canceled", wantRuns: 1},
		{name: "hostname check fails", runErr: map[int]error{1: &guestExitError{code: 11}}, attempts: 3, wantErr: "hostname", wantRuns: 2},
		{name: "check cannot start", runErr: map[int]error{1: errors.New("tools gone")}, attempts: 3, wantErr: "run verify checks", wantRuns: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &fakeGuestSession{runErr: tt.runErr}
			wait := tt.wait
			if wait == nil {
				wait = noWait
			}
			err := verifyGuest(context.Background(), g, "h", "n", tt.attempts, wait)
			if len(g.scripts) != tt.wantRuns {
				t.Fatalf("runs = %d, want %d", len(g.scripts), tt.wantRuns)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertErrContains(t, err, tt.wantErr)
		})
	}
}

func TestOwnsLibraryItem(t *testing.T) {
	desc := "Golden image\nimagebuilder build b-1"
	other := "imagebuilder build b-12"
	tests := []struct {
		name string
		item *library.Item
		id   string
		want bool
	}{
		{name: "owned", item: &library.Item{Description: &desc}, id: "b-1", want: true},
		{name: "prefix of another build", item: &library.Item{Description: &other}, id: "b-1"},
		{name: "no description", item: &library.Item{}, id: "b-1"},
		{name: "nil", id: "b-1"},
		{name: "empty build id", item: &library.Item{Description: &desc}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ownsLibraryItem(tt.item, tt.id); got != tt.want {
				t.Fatalf("ownsLibraryItem() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyBootRequiresContentLibrary(t *testing.T) {
	cfg := validPluginConfig()
	cfg.Extra["remote.verifyBoot"] = "true"
	p := &Plugin{client: &fakeClient{}}
	err := p.Init(context.Background(), cfg)
	assertErrContains(t, err, "remote.verifyBoot requires remote.publishTo content-library")
}
