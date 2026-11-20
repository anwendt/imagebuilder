package evidence

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

// IsController reports whether the evidence policy selects the ADR-011
// controller publisher.
func IsController(spec *v1alpha1.EvidenceSpec) bool {
	return spec != nil && strings.TrimSpace(spec.Publisher) == v1alpha1.EvidencePublisherController
}

// GuestInput describes the build values handed to the evidence provisioner.
type GuestInput struct {
	BuildID   string
	ImageName string
	SourceRef string
	Policy    *v1alpha1.EvidenceSpec
}

// GuestEnvKeys is the stable order of the evidence provisioner environment.
var GuestEnvKeys = []string{
	"IMAGEBUILDER_EVIDENCE_OUTBOX",
	"IMAGEBUILDER_EVIDENCE_PUBLISHER",
	"IMAGEBUILDER_BUILD_ID",
	"IMAGEBUILDER_IMAGE_NAME",
	"IMAGEBUILDER_SOURCE_REF",
	"IMAGEBUILDER_EVIDENCE_SUBJECT_NAME",
	"IMAGEBUILDER_EVIDENCE_SUBJECT_VERSION",
	"IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM",
	"IMAGEBUILDER_EVIDENCE_SBOM_FORMAT",
	"IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER",
	"IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY",
}

// GuestEnv returns the non-secret environment of a controller-mode evidence
// provisioner. It never contains registry or signing credentials.
func GuestEnv(input GuestInput) map[string]string {
	env := map[string]string{
		"IMAGEBUILDER_EVIDENCE_OUTBOX":    OutboxDir,
		"IMAGEBUILDER_EVIDENCE_PUBLISHER": v1alpha1.EvidencePublisherController,
		"IMAGEBUILDER_BUILD_ID":           input.BuildID,
		"IMAGEBUILDER_IMAGE_NAME":         input.ImageName,
		"IMAGEBUILDER_SOURCE_REF":         input.SourceRef,
	}
	if policy := input.Policy; policy != nil {
		env["IMAGEBUILDER_EVIDENCE_SBOM_FORMAT"] = policy.SBOMFormat
		env["IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER"] = policy.VulnerabilityScanner
		env["IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY"] = strings.Join(policy.FailOnSeverity, ",")
		if subject := policy.Subject; subject != nil {
			env["IMAGEBUILDER_EVIDENCE_SUBJECT_NAME"] = subject.Name
			env["IMAGEBUILDER_EVIDENCE_SUBJECT_VERSION"] = subject.Version
			env["IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM"] = subject.Platform
		}
	}
	return env
}

// GuestEnvExports renders GuestEnv as POSIX shell export lines.
func GuestEnvExports(input GuestInput) string {
	env := GuestEnv(input)
	lines := make([]string, 0, len(GuestEnvKeys))
	for _, key := range GuestEnvKeys {
		lines = append(lines, "export "+key+"="+ShellQuote(env[key]))
	}
	return strings.Join(lines, "\n")
}

// ShellQuote quotes a value for POSIX sh.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

var safeGuestPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// PackScript returns a POSIX shell script that packs the outbox into file.
// The outbox must exist and contain both required files.
func PackScript(file string) (string, error) {
	if !safeGuestPath.MatchString(file) || strings.Contains(file, "..") {
		return "", fmt.Errorf("unsafe guest bundle path %q", file)
	}
	return strings.Join([]string{
		"set -eu",
		"umask 077",
		"test -s " + OutboxDir + "/" + SBOMFile + " || { echo 'evidence outbox: " + SBOMFile + " missing' >&2; exit 3; }",
		"test -s " + OutboxDir + "/" + TrivyFile + " || { echo 'evidence outbox: " + TrivyFile + " missing' >&2; exit 3; }",
		"rm -f " + file,
		"tar czf " + file + " -C " + OutboxParent + " " + OutboxName,
	}, "\n"), nil
}

// RemoveScript returns a POSIX shell script that deletes the outbox and the
// optional packed file and proves that both are gone.
func RemoveScript(file string) (string, error) {
	lines := []string{"set -eu", "rm -rf " + OutboxDir}
	if file != "" {
		if !safeGuestPath.MatchString(file) || strings.Contains(file, "..") {
			return "", fmt.Errorf("unsafe guest bundle path %q", file)
		}
		lines = append(lines, "rm -f "+file, "test ! -e "+file)
	}
	lines = append(lines, "test ! -e "+OutboxDir)
	return strings.Join(lines, "\n"), nil
}

// SpecFromSDK maps the provider SDK evidence policy to the API type.
func SpecFromSDK(policy *sdk.RemoteEvidencePolicy) *v1alpha1.EvidenceSpec {
	if policy == nil {
		return nil
	}
	spec := &v1alpha1.EvidenceSpec{
		Required:             policy.Required,
		Publisher:            policy.Publisher,
		SBOMFormat:           policy.SBOMFormat,
		VulnerabilityScanner: policy.VulnerabilityScanner,
		FailOnSeverity:       append([]string(nil), policy.FailOnSeverity...),
		RegistryRepository:   policy.RegistryRepository,
	}
	if subject := policy.Subject; subject != nil {
		spec.Subject = &v1alpha1.EvidenceSubject{Name: subject.Name, Version: subject.Version, Platform: subject.Platform}
	}
	return spec
}

// SDKBundle maps a provider-neutral bundle to the provider SDK type.
func SDKBundle(bundle *platform.RemoteEvidenceBundle) *sdk.RemoteEvidenceBundle {
	if bundle == nil {
		return nil
	}
	return &sdk.RemoteEvidenceBundle{
		Content:   append([]byte(nil), bundle.Content...),
		SHA256:    bundle.SHA256,
		MediaType: bundle.MediaType,
	}
}

// PlatformBundle returns the provider-neutral form of a validated bundle.
func (b *Bundle) PlatformBundle() *platform.RemoteEvidenceBundle {
	if b == nil {
		return nil
	}
	return &platform.RemoteEvidenceBundle{Content: b.Content, SHA256: b.SHA256, MediaType: MediaType}
}
