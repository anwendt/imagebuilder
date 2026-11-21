package azure

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

// Azure Run Command (RunShellScript) reports "Enable succeeded" whatever the
// script's exit code is, so a failing provisioner used to pass silently (found
// in the first ubuntu-2604-base 1.2.0 builds: apt could not reach its proxy,
// the hardening step never ran and the build still succeeded). Every Linux
// script therefore runs inside a wrapper that prints its exit code as the last
// stdout line; the provider fails the step unless that marker says 0.

const azureExitCodeMarker = "IMAGEBUILDER_EXIT_CODE="

var azureExitCodePattern = regexp.MustCompile(azureExitCodeMarker + `(\d+)`)

// wrapAzureLinuxScript writes the script to a private temporary file, runs it
// with its own shebang (sh without one) and prints the exit code marker.
func wrapAzureLinuxScript(script string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate script delimiter: %w", err)
	}
	delimiter := "IMAGEBUILDER_SCRIPT_" + strings.ToUpper(hex.EncodeToString(suffix))
	if strings.Contains(script, delimiter) {
		return "", errors.New("script contains the generated delimiter")
	}
	if !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	var b strings.Builder
	b.WriteString("set +e\n")
	b.WriteString("imagebuilder_script=$(mktemp /tmp/imagebuilder-provisioner.XXXXXX) || { echo \"" + azureExitCodeMarker + "125\"; exit 0; }\n")
	b.WriteString("cat > \"$imagebuilder_script\" <<'" + delimiter + "'\n")
	b.WriteString(script)
	b.WriteString(delimiter + "\n")
	b.WriteString("chmod 0700 \"$imagebuilder_script\"\n")
	b.WriteString("if [ \"$(head -c 2 \"$imagebuilder_script\")\" = '#!' ]; then \"$imagebuilder_script\"; else /bin/sh \"$imagebuilder_script\"; fi\n")
	b.WriteString("imagebuilder_rc=$?\n")
	b.WriteString("rm -f \"$imagebuilder_script\"\n")
	b.WriteString("echo \"" + azureExitCodeMarker + "${imagebuilder_rc}\"\n")
	return b.String(), nil
}

// azureRunCommandExitError returns an error unless the Run Command output
// carries the exit code marker with value 0. The error contains the tail of
// the output (Run Command keeps only the last 4 KiB per stream anyway).
func azureRunCommandExitError(statuses []*armcompute.InstanceViewStatus) error {
	var output strings.Builder
	code := -1
	for _, status := range statuses {
		message := value(status.Message)
		output.WriteString(message)
		if matches := azureExitCodePattern.FindAllStringSubmatch(message, -1); len(matches) > 0 {
			parsed, err := strconv.Atoi(matches[len(matches)-1][1])
			if err == nil {
				code = parsed
			}
		}
	}
	if code == 0 {
		return nil
	}
	tail := output.String()
	if len(tail) > 1500 {
		tail = tail[len(tail)-1500:]
	}
	if code < 0 {
		return fmt.Errorf("script exit code marker missing from Run Command output: %s", strings.TrimSpace(tail))
	}
	return fmt.Errorf("script exited with code %d: %s", code, strings.TrimSpace(tail))
}
