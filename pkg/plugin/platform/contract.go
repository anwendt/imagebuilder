package platform

import "fmt"

const ProtocolVersionV1 = "v1"

func ValidatePluginContract(p Plugin) error {
	if p == nil {
		return fmt.Errorf("platform plugin is nil")
	}
	if p.Name() == "" {
		return fmt.Errorf("platform plugin name is required")
	}
	if p.Version() == "" {
		return fmt.Errorf("platform plugin %q version is required", p.Name())
	}
	return nil
}
