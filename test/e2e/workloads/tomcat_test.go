package workloads

import (
	"strings"
	"testing"
)

func TestTomcatTarShellProvisioner(t *testing.T) {
	script := TomcatTarShellProvisioner()
	for _, want := range []string{
		"apache-tomcat-${TOMCAT_VERSION}.tar.gz",
		"sha512sum -c",
		"openjdk-17-jre-headless",
		"/etc/systemd/system/tomcat.service",
		"curl -fsS http://127.0.0.1:8080/health",
		"install.mode=tar",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Tomcat provisioner script does not contain %q", want)
		}
	}
}
