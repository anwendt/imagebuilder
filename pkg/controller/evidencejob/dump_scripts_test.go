package evidencejob

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpScripts writes the step scripts to EVIDENCEJOB_DUMP_DIR for the
// local registry harness (hack/evidence-publication-e2e.sh). It is a no-op in
// normal test runs.
func TestDumpScripts(t *testing.T) {
	dir := os.Getenv("EVIDENCEJOB_DUMP_DIR")
	if dir == "" {
		t.Skip("EVIDENCEJOB_DUMP_DIR not set")
	}
	for name, script := range map[string]string{
		"prepare.sh":         PrepareScript,
		"push-descriptor.sh": PushDescriptorScript,
		"sign-descriptor.sh": SignDescriptorScript,
		"publish-index.sh":   PublishIndexScript,
		"sign-index.sh":      SignIndexScript,
		"report.sh":          ReportScript,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
