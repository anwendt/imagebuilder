package vsphere

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

type fakeGuest struct {
	probes    []error // returned by successive Probe calls
	probeIdx  int
	schedule  error
	scheduled int
}

func (f *fakeGuest) Probe(context.Context) error {
	if f.probeIdx >= len(f.probes) {
		return errAuthRejected
	}
	err := f.probes[f.probeIdx]
	f.probeIdx++
	return err
}
func (f *fakeGuest) Schedule(context.Context) error { f.scheduled++; return f.schedule }

var errAuthRejected = soap.WrapVimFault(&types.InvalidGuestLogin{})

func noWait(context.Context, time.Duration) error { return nil }

func TestRevokeBuildCredential(t *testing.T) {
	other := errors.New("tools unavailable")
	tests := []struct {
		name          string
		g             *fakeGuest
		wantErr       string
		wantRevoked   bool
		wantScheduled int
	}{
		{"locked after the delay", &fakeGuest{probes: []error{nil, errAuthRejected}}, "", true, 1},
		{"already revoked", &fakeGuest{probes: []error{errAuthRejected}}, "", true, 0},
		{"still accepted", &fakeGuest{probes: []error{nil, nil}}, "BuildCredentialNotRevoked", false, 1},
		{"scheduling fails", &fakeGuest{probes: []error{nil}, schedule: errors.New("exit 1")}, "schedule", false, 1},
		{"first probe inconclusive", &fakeGuest{probes: []error{other}}, "inconclusive", false, 0},
		{"second probe inconclusive", &fakeGuest{probes: []error{nil, other}}, "inconclusive", false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revoked, err := revokeBuildCredential(context.Background(), tt.g, noWait, 30*time.Second)
			assertErrContains(t, err, tt.wantErr)
			if revoked != tt.wantRevoked {
				t.Fatalf("revoked = %v, want %v", revoked, tt.wantRevoked)
			}
			if tt.g.scheduled != tt.wantScheduled {
				t.Fatalf("scheduled = %d, want %d", tt.g.scheduled, tt.wantScheduled)
			}
		})
	}
}

func TestIsGuestAuthError(t *testing.T) {
	if !isGuestAuthError(errAuthRejected) {
		t.Fatal("InvalidGuestLogin must be an auth error")
	}
	if !isGuestAuthError(errors.New("ServerFaultCode: Failed to authenticate with the guest operating system using the supplied credentials.")) {
		t.Fatal("message form must be recognised")
	}
	if isGuestAuthError(errors.New("GuestOperationsUnavailable")) || isGuestAuthError(nil) {
		t.Fatal("other errors are not auth errors")
	}
}

func TestRevokeScript(t *testing.T) {
	script, err := revokeScript("root", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"systemd-run", "--on-active=30s", "passwd -l root"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}
	if _, err := revokeScript("root; rm -rf /", time.Second); err == nil {
		t.Fatal("unsafe user names must be rejected")
	}
}

func TestCheckTemplateFolderPath(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		build   string
		prot    []string
		wantErr string
	}{
		{"ok", "/dc-01/vm/golden-templates", "/dc-01/vm/imagebuilder-builds", nil, ""},
		{"empty", "", "/dc-01/vm/imagebuilder-builds", nil, "templateFolder"},
		{"relative", "golden", "/dc-01/vm/imagebuilder-builds", nil, "absolute"},
		{"same as build folder", "/dc-01/vm/imagebuilder-builds", "/dc-01/vm/imagebuilder-builds", nil, "build folder"},
		{"inside build folder", "/dc-01/vm/imagebuilder-builds/tpl", "/dc-01/vm/imagebuilder-builds", nil, "build folder"},
		{"protected", "/dc-01/vm/critical-app/tpl", "/dc-01/vm/imagebuilder-builds", []string{"critical-app"}, "protected"},
		{"unclean", "/dc-01/vm/../tpl", "/dc-01/vm/imagebuilder-builds", nil, "clean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrContains(t, checkTemplateFolderPath(tt.tmpl, tt.build, tt.prot), tt.wantErr)
		})
	}
}

func TestInitRequiresTemplateFolderForLibraryPublishing(t *testing.T) {
	cfg := validPluginConfig()
	cfg.Extra["remote.publishTo"] = "content-library"
	cfg.Extra["contentLibrary"] = "templates"
	cfg.Extra["buildFolder"] = "/dc01/vm/imagebuilder-builds"
	p := &Plugin{client: &fakeClient{}}
	assertErrContains(t, p.Init(context.Background(), cfg), "templateFolder")
	cfg.Extra["templateFolder"] = "/dc01/vm/golden-templates"
	p = &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !p.config.revokeBuildCredential {
		t.Fatal("credential revocation must default to on")
	}
	cfg.Extra["revokeBuildCredential"] = "false"
	p = &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if p.config.revokeBuildCredential {
		t.Fatal("revokeBuildCredential=false must disable the step")
	}
}
