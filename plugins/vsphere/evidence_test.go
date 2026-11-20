package vsphere

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

// fakeGuestSession records scripts and serves downloads.
type fakeGuestSession struct {
	scripts     []string
	runErr      map[int]error // per call index
	download    []byte
	downloadErr error
	gotLimit    int64
	gotPath     string
}

func (f *fakeGuestSession) Run(_ context.Context, script string) error {
	f.scripts = append(f.scripts, script)
	return f.runErr[len(f.scripts)-1]
}

func (f *fakeGuestSession) Download(_ context.Context, guestPath string, limit int64) ([]byte, error) {
	f.gotPath, f.gotLimit = guestPath, limit
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	if int64(len(f.download)) > limit {
		return f.download[:limit], nil
	}
	return f.download, nil
}

func validBundleBytes(t *testing.T) []byte {
	t.Helper()
	data, err := evidence.Pack(map[string][]byte{
		evidence.SBOMFile:  []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`),
		evidence.TrivyFile: []byte(`{"SchemaVersion":2,"Results":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func controllerPolicy() *v1alpha1.EvidenceSpec {
	return &v1alpha1.EvidenceSpec{
		Required:   true,
		Publisher:  v1alpha1.EvidencePublisherController,
		SBOMFormat: "cyclonedx-json",
		Subject:    &v1alpha1.EvidenceSubject{Name: "ubuntu-2604-base", Version: "1.2.0", Platform: "vsphere-fci-c042"},
	}
}

func TestCollectGuestEvidence(t *testing.T) {
	good := validBundleBytes(t)
	tests := []struct {
		name        string
		guest       *fakeGuestSession
		wantErr     string
		wantScripts int
	}{
		{name: "packs downloads validates and removes", guest: &fakeGuestSession{download: good}, wantScripts: 2},
		{name: "pack fails", guest: &fakeGuestSession{runErr: map[int]error{0: &guestExitError{code: 3}}}, wantErr: "EvidenceCollectionFailed: pack", wantScripts: 1},
		{name: "download fails", guest: &fakeGuestSession{downloadErr: errors.New("boom")}, wantErr: "EvidenceCollectionFailed: download", wantScripts: 1},
		{name: "too large", guest: &fakeGuestSession{download: make([]byte, evidence.MaxBundleBytes+10)}, wantErr: "EvidenceBundleTooLarge", wantScripts: 1},
		{name: "invalid bundle", guest: &fakeGuestSession{download: []byte("nope")}, wantErr: "EvidenceBundleInvalid", wantScripts: 1},
		{name: "remove fails", guest: &fakeGuestSession{download: good, runErr: map[int]error{1: &guestExitError{code: 1}}}, wantErr: "remove the guest evidence outbox", wantScripts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := collectGuestEvidence(context.Background(), tt.guest, "Build/ID 1")
			if len(tt.guest.scripts) != tt.wantScripts {
				t.Fatalf("scripts run = %d, want %d", len(tt.guest.scripts), tt.wantScripts)
			}
			if tt.wantErr != "" {
				assertErrContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bundle.SHA256 != evidence.SHA256Hex(good) {
				t.Fatalf("bundle sha = %s", bundle.SHA256)
			}
			if tt.guest.gotLimit != evidence.MaxBundleBytes+1 || tt.guest.gotPath != "/var/tmp/imagebuilder-evidence-build-id-1.tar.gz" {
				t.Fatalf("download path/limit = %s/%d", tt.guest.gotPath, tt.guest.gotLimit)
			}
			if !strings.Contains(tt.guest.scripts[0], "tar czf /var/tmp/imagebuilder-evidence-build-id-1.tar.gz") ||
				!strings.Contains(tt.guest.scripts[1], "rm -rf /var/lib/imagebuilder/evidence") {
				t.Fatalf("scripts = %q", tt.guest.scripts)
			}
		})
	}
}

func TestEvidenceBundleStore(t *testing.T) {
	dir := t.TempDir()
	data := validBundleBytes(t)
	if err := persistEvidenceBundle(dir, "b1", data); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(evidenceStorePath(dir, "b1"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored bundle = %v, %v", info, err)
	}
	if _, err := loadEvidenceBundle(dir, "b1", evidence.SHA256Hex(data)); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := loadEvidenceBundle(dir, "b1", strings.Repeat("0", 64)); err == nil {
		t.Fatal("load with wrong digest must fail")
	}
	if err := removeEvidenceBundle(dir, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := removeEvidenceBundle(dir, "b1"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if err := persistEvidenceBundle(filepath.Join(dir, "file"), "b1", data); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistEvidenceBundle(blocker, "b1", data); err == nil {
		t.Fatal("persist below a file must fail")
	}
}

func TestDoneEvidenceBundle(t *testing.T) {
	data := validBundleBytes(t)
	sha := evidence.SHA256Hex(data)
	tests := []struct {
		name      string
		input     vsphereRemoteBuildInput
		ref       vsphereRemoteOperationRef
		stored    []byte
		wantErr   string
		wantNil   bool
		wantGone  bool
		terminal  bool
		wantStore bool
	}{
		{name: "provider publisher returns nothing", input: vsphereRemoteBuildInput{BuildID: "b"}, wantNil: true},
		{name: "returns bundle", input: vsphereRemoteBuildInput{BuildID: "b", Evidence: controllerPolicy()}, ref: vsphereRemoteOperationRef{EvidenceSHA256: sha}, stored: data, wantStore: true},
		{name: "ack deletes", input: vsphereRemoteBuildInput{BuildID: "b", Evidence: controllerPolicy(), EvidenceBundleAcknowledged: true}, ref: vsphereRemoteOperationRef{EvidenceSHA256: sha}, stored: data, wantNil: true, wantGone: true},
		{name: "missing sha", input: vsphereRemoteBuildInput{BuildID: "b", Evidence: controllerPolicy()}, wantErr: "EvidenceBundleMissing", terminal: true},
		{name: "lost", input: vsphereRemoteBuildInput{BuildID: "b", Evidence: controllerPolicy()}, ref: vsphereRemoteOperationRef{EvidenceSHA256: sha}, wantErr: "EvidenceBundleLost", terminal: true},
		{name: "tampered", input: vsphereRemoteBuildInput{BuildID: "b", Evidence: controllerPolicy()}, ref: vsphereRemoteOperationRef{EvidenceSHA256: strings.Repeat("1", 64)}, stored: data, wantErr: "EvidenceBundleInvalid", terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.stored != nil {
				if err := persistEvidenceBundle(dir, "b", tt.stored); err != nil {
					t.Fatal(err)
				}
			}
			bundle, err := doneEvidenceBundle(dir, tt.input, tt.ref)
			if tt.wantErr != "" {
				assertErrContains(t, err, tt.wantErr)
				if tt.terminal && providererrors.IsTransient(err) {
					t.Fatalf("error must be terminal: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNil != (bundle == nil) {
				t.Fatalf("bundle = %v, wantNil %v", bundle, tt.wantNil)
			}
			if bundle != nil && (bundle.SHA256 != sha || bundle.MediaType != evidence.MediaType) {
				t.Fatalf("bundle = %+v", bundle)
			}
			_, statErr := os.Stat(evidenceStorePath(dir, "b"))
			if tt.wantGone && statErr == nil {
				t.Fatal("acknowledged bundle must be deleted")
			}
			if tt.wantStore && statErr != nil {
				t.Fatalf("bundle must stay until acknowledged: %v", statErr)
			}
		})
	}
}

func TestDoneStateReturnsEvidenceAndHygiene(t *testing.T) {
	dir := t.TempDir()
	data := validBundleBytes(t)
	if err := persistEvidenceBundle(dir, "b", data); err != nil {
		t.Fatal(err)
	}
	c := &govmomiClient{cfg: config{downloadDir: dir, publishToLibrary: true, contentLibrary: "lib", datacenter: "dc"}}
	input := vsphereRemoteBuildInput{BuildID: "b", ImageName: "img", Format: platform.FormatOVA, Evidence: controllerPolicy(), Provisioners: []v1alpha1.ProvisionerSpec{{Type: "evidence"}}}
	ref := vsphereRemoteOperationRef{BuildID: "b", ImageRef: "item-1", EvidenceSHA256: evidence.SHA256Hex(data), VerifyBoot: verifyBootPassed, CredentialRevoked: true, TemplateVerified: true}
	state, err := c.doneState(input, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Done || state.EvidenceBundle == nil || state.Image.Location != "lib" || state.Image.Tags["imagebuilder.io/registration-mode"] != "content-library-template" {
		t.Fatalf("state = %+v", state)
	}
	checks := strings.Join(state.Hygiene.Checks, ",")
	for _, want := range []string{verifyBootCheck, revokeCheckName, templateConfigCleanCheck, "vsphere-evidence-outbox-retrieved"} {
		if !strings.Contains(checks, want) {
			t.Fatalf("checks %s missing %s", checks, want)
		}
	}
	c.cfg.publishToLibrary = false
	state, err = c.doneState(vsphereRemoteBuildInput{BuildID: "b", Format: platform.FormatOVA}, vsphereRemoteOperationRef{ImageRef: "vm-1"})
	if err != nil || state.Image.Location != "dc" || state.EvidenceBundle != nil || strings.Contains(strings.Join(state.Hygiene.Checks, ","), verifyBootCheck) {
		t.Fatalf("clone state = %+v, %v", state, err)
	}
	if _, err := c.doneState(vsphereRemoteBuildInput{BuildID: "x", Evidence: controllerPolicy()}, vsphereRemoteOperationRef{ImageRef: "vm-1"}); err == nil {
		t.Fatal("controller mode without a bundle must fail")
	}
}

func TestEvidenceProvisionerGuestProgram(t *testing.T) {
	linux := vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux, BuildID: "b1", ImageName: "img", SourceRef: "https://x/y.ova", Evidence: controllerPolicy()}
	spec := v1alpha1.ProvisionerSpec{Type: "evidence", Inline: "collect-evidence"}
	prog, args, err := vsphereGuestProgram(linux, spec)
	if err != nil || prog != "/bin/sh" {
		t.Fatalf("program = %q, %v", prog, err)
	}
	for _, want := range []string{"IMAGEBUILDER_EVIDENCE_OUTBOX=", "IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM=", "vsphere-fci-c042", "collect-evidence", guestProvisionLog} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %s", want, args)
		}
	}
	tests := []struct {
		name  string
		input vsphereRemoteBuildInput
		spec  v1alpha1.ProvisionerSpec
		want  string
	}{
		{name: "provider publisher", input: vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux, Evidence: &v1alpha1.EvidenceSpec{Publisher: "provider"}}, spec: spec, want: "publisher controller"},
		{name: "no policy", input: vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux}, spec: spec, want: "publisher controller"},
		{name: "windows", input: vsphereRemoteBuildInput{OSFamily: platform.OSFamilyWindows, Evidence: controllerPolicy()}, spec: spec, want: "linux"},
		{name: "empty inline", input: linux, spec: v1alpha1.ProvisionerSpec{Type: "evidence"}, want: "inline content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := vsphereGuestProgram(tt.input, tt.spec)
			assertErrContains(t, err, tt.want)
		})
	}
}

func TestValidateVSphereRemoteProvisionersEvidence(t *testing.T) {
	ev := []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "x"}, {Type: "evidence", Inline: "y"}, {Type: "shell", Inline: "z"}}
	tests := []struct {
		name  string
		input vsphereRemoteBuildInput
		want  string
	}{
		{name: "controller linux", input: vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux, Evidence: controllerPolicy(), Provisioners: ev}},
		{name: "provider publisher", input: vsphereRemoteBuildInput{OSFamily: platform.OSFamilyLinux, Evidence: &v1alpha1.EvidenceSpec{}, Provisioners: ev}, want: "only with spec.evidence.publisher controller"},
		{name: "windows", input: vsphereRemoteBuildInput{OSFamily: platform.OSFamilyWindows, Evidence: controllerPolicy(), Provisioners: ev[1:2]}, want: "only linux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVSphereRemoteProvisioners(tt.input)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertErrContains(t, err, tt.want)
		})
	}
}

func TestVSphereRemoteOperationRefEvidenceRoundTrip(t *testing.T) {
	ref := vsphereRemoteOperationRef{BuildID: "b", ImageRef: "item", EvidenceSHA256: "abc", VerifyBoot: verifyBootPending, CredentialRevoked: true, TemplateVerified: true}
	parsed, err := parseVSphereRemoteOperationRef(ref.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != ref {
		t.Fatalf("parsed = %+v, want %+v", parsed, ref)
	}
}
