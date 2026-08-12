package plugin_test

import (
	"testing"

	"github.com/anwendt/imagebuilder/pkg/plugin"

	_ "github.com/anwendt/imagebuilder/plugins/aws"
	_ "github.com/anwendt/imagebuilder/plugins/azure"
	_ "github.com/anwendt/imagebuilder/plugins/gcp"
	_ "github.com/anwendt/imagebuilder/plugins/openstack"
	_ "github.com/anwendt/imagebuilder/plugins/vsphere"
)

func TestBuiltInProvidersReturnIsolatedInstances(t *testing.T) {
	for _, name := range []string{"aws", "azure", "gcp", "openstack", "vsphere"} {
		t.Run(name, func(t *testing.T) {
			first, err := plugin.Default().New(name)
			if err != nil {
				t.Fatalf("New first: %v", err)
			}
			second, err := plugin.Default().New(name)
			if err != nil {
				t.Fatalf("New second: %v", err)
			}
			if first == second {
				t.Fatalf("built-in provider %q reused its global prototype", name)
			}
		})
	}
}
