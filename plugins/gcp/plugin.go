// plugins/gcp/plugin.go — TODO: implement
package gcp

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func init() {
	if err := plugin.Register(&Plugin{}); err != nil {
		panic(fmt.Sprintf("gcp plugin: %v", err))
	}
}

type Plugin struct{}

func (p *Plugin) Name() string                              { return "gcp" }
func (p *Plugin) Version() string                          { return "v0.1.0" }
func (p *Plugin) SupportedFormats() []platform.ImageFormat { return nil }
func (p *Plugin) SupportedOS() []platform.OSFamily         { return nil }
func (p *Plugin) Init(_ context.Context, _ platform.PluginConfig) error          { return nil }
func (p *Plugin) Validate(_ context.Context, _ v1alpha1.TargetSpec) error        { return nil }
func (p *Plugin) Cleanup(_ context.Context, _ *platform.BuildArtifact) error     { return nil }
func (p *Plugin) HealthCheck(_ context.Context) error                             { return nil }
func (p *Plugin) Upload(_ context.Context, _ *platform.BuildArtifact) (*platform.UploadResult, error) {
	return nil, fmt.Errorf("gcp: not yet implemented")
}
func (p *Plugin) Register(_ context.Context, _ *platform.UploadResult) (*platform.ImageRef, error) {
	return nil, fmt.Errorf("gcp: not yet implemented")
}
