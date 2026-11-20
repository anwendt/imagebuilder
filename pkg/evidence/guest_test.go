package evidence

import (
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

func TestIsController(t *testing.T) {
	tests := []struct {
		name string
		spec *v1alpha1.EvidenceSpec
		want bool
	}{
		{name: "nil"},
		{name: "default", spec: &v1alpha1.EvidenceSpec{}},
		{name: "provider", spec: &v1alpha1.EvidenceSpec{Publisher: "provider"}},
		{name: "controller", spec: &v1alpha1.EvidenceSpec{Publisher: "controller"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsController(tt.spec); got != tt.want {
				t.Fatalf("IsController() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGuestEnvExports(t *testing.T) {
	out := GuestEnvExports(GuestInput{
		BuildID:   "uid-1",
		ImageName: "img",
		SourceRef: "azure-marketplace://Canonical/o/s/v",
		Policy: &v1alpha1.EvidenceSpec{
			SBOMFormat:           "cyclonedx-json",
			VulnerabilityScanner: "trivy",
			FailOnSeverity:       []string{"HIGH", "CRITICAL"},
			Subject:              &v1alpha1.EvidenceSubject{Name: "ubuntu-2604-base", Version: "1.2.0", Platform: "it's"},
		},
	})
	for _, want := range []string{
		"export IMAGEBUILDER_EVIDENCE_OUTBOX='/var/lib/imagebuilder/evidence'",
		"export IMAGEBUILDER_EVIDENCE_SUBJECT_NAME='ubuntu-2604-base'",
		"export IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY='HIGH,CRITICAL'",
		`export IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM='it'"'"'s'`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exports missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(strings.ToUpper(out), "COSIGN") || strings.Contains(strings.ToUpper(out), "PASSWORD") {
		t.Fatalf("exports must not reference credentials:\n%s", out)
	}
	if got := GuestEnv(GuestInput{})["IMAGEBUILDER_EVIDENCE_SUBJECT_NAME"]; got != "" {
		t.Fatalf("empty input subject = %q", got)
	}
}

func TestPackAndRemoveScripts(t *testing.T) {
	pack, err := PackScript("/var/tmp/imagebuilder-evidence-abc.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pack, "tar czf /var/tmp/imagebuilder-evidence-abc.tar.gz -C /var/lib/imagebuilder evidence") {
		t.Fatalf("pack script = %s", pack)
	}
	remove, err := RemoveScript("/var/tmp/imagebuilder-evidence-abc.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remove, "rm -rf /var/lib/imagebuilder/evidence") || !strings.Contains(remove, "test ! -e /var/lib/imagebuilder/evidence") {
		t.Fatalf("remove script = %s", remove)
	}
	if only, err := RemoveScript(""); err != nil || strings.Contains(only, "rm -f") {
		t.Fatalf("RemoveScript(\"\") = %q, %v", only, err)
	}
	for _, bad := range []string{"relative", "/tmp/x;rm -rf /", "/tmp/../etc/x", "/tmp/a b"} {
		if _, err := PackScript(bad); err == nil {
			t.Fatalf("PackScript(%q) accepted an unsafe path", bad)
		}
		if _, err := RemoveScript(bad); err == nil {
			t.Fatalf("RemoveScript(%q) accepted an unsafe path", bad)
		}
	}
}

func TestSpecFromSDK(t *testing.T) {
	if SpecFromSDK(nil) != nil {
		t.Fatal("nil policy must map to nil")
	}
	spec := SpecFromSDK(&sdk.RemoteEvidencePolicy{
		Required:           true,
		Publisher:          "controller",
		SBOMFormat:         "cyclonedx-json",
		FailOnSeverity:     []string{"HIGH"},
		RegistryRepository: "oci://r/x",
		Subject:            &sdk.RemoteEvidenceSubject{Name: "n", Version: "1", Platform: "azure"},
	})
	if !IsController(spec) || spec.Subject == nil || spec.Subject.Platform != "azure" || spec.FailOnSeverity[0] != "HIGH" || !spec.Required {
		t.Fatalf("SpecFromSDK() = %+v", spec)
	}
}

func TestSDKBundle(t *testing.T) {
	if SDKBundle(nil) != nil || (*Bundle)(nil).PlatformBundle() != nil {
		t.Fatal("nil bundles must map to nil")
	}
	b := &Bundle{Content: []byte{1, 2}, SHA256: "ab"}
	out := SDKBundle(b.PlatformBundle())
	if out.SHA256 != "ab" || out.MediaType != MediaType || len(out.Content) != 2 {
		t.Fatalf("SDKBundle() = %+v", out)
	}
	if b.Digest() != "sha256:ab" {
		t.Fatalf("Digest() = %q", b.Digest())
	}
}
