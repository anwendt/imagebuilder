package azure

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

func runWrapped(t *testing.T, script string) string {
	t.Helper()
	wrapped, err := wrapAzureLinuxScript(script)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wrapper.sh")
	if err := os.WriteFile(path, []byte(wrapped), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", path).CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper itself must exit 0: %v: %s", err, out)
	}
	return string(out)
}

func TestWrapAzureLinuxScriptReportsExitCode(t *testing.T) {
	tests := []struct {
		name, script, want string
	}{
		{"success", "#!/bin/sh\necho hello\n", azureExitCodeMarker + "0"},
		{"failure with set -e", "#!/bin/sh\nset -e\nfalse\necho unreachable\n", azureExitCodeMarker + "1"},
		{"explicit exit code", "exit 3", azureExitCodeMarker + "3"},
		{"no shebang uses sh", "echo plain; exit 0\n", azureExitCodeMarker + "0"},
		{"heredoc inside script", "#!/bin/sh\ncat <<'EOF'\nx\nEOF\nexit 4\n", azureExitCodeMarker + "4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runWrapped(t, tt.script)
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if got := lines[len(lines)-1]; got != tt.want {
				t.Fatalf("last line = %q, want %q (output %q)", got, tt.want, out)
			}
			if strings.Contains(out, "unreachable") {
				t.Fatal("set -e must stop the script")
			}
		})
	}
}

func TestAzureRunCommandExitError(t *testing.T) {
	status := func(msg string) []*armcompute.InstanceViewStatus {
		return []*armcompute.InstanceViewStatus{{Code: to.Ptr("ProvisioningState/succeeded"), Message: to.Ptr(msg)}}
	}
	tests := []struct {
		name    string
		msg     string
		wantErr string
	}{
		{"zero", "Enable succeeded: \n[stdout]\nok\n" + azureExitCodeMarker + "0\n[stderr]\n", ""},
		{"non zero", "Enable succeeded: \n[stdout]\n" + azureExitCodeMarker + "100\n[stderr]\nE: Failed to fetch\n", "exited with code 100"},
		{"missing marker", "Enable succeeded: \n[stdout]\n\n[stderr]\n", "marker missing"},
		{"last marker wins", azureExitCodeMarker + "0\n" + azureExitCodeMarker + "2\n", "exited with code 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := azureRunCommandExitError(status(tt.msg))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
