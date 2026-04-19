// pkg/plugin/registry_test.go
//
// Unit tests for the plugin Registry.

package plugin_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ---------------------------------------------------------------------------
// Fake plugin for testing
// ---------------------------------------------------------------------------

type fakePlugin struct {
	name    string
	version string
}

func (p *fakePlugin) Name() string                              { return p.name }
func (p *fakePlugin) Version() string                          { return p.version }
func (p *fakePlugin) SupportedFormats() []platform.ImageFormat { return []platform.ImageFormat{platform.FormatVMDK} }
func (p *fakePlugin) SupportedOS() []platform.OSFamily         { return []platform.OSFamily{platform.OSFamilyLinux} }
func (p *fakePlugin) Init(_ context.Context, _ platform.PluginConfig) error          { return nil }
func (p *fakePlugin) Validate(_ context.Context, _ v1alpha1.TargetSpec) error        { return nil }
func (p *fakePlugin) Upload(_ context.Context, _ *platform.BuildArtifact) (*platform.UploadResult, error) {
	return &platform.UploadResult{}, nil
}
func (p *fakePlugin) Register(_ context.Context, _ *platform.UploadResult) (*platform.ImageRef, error) {
	return &platform.ImageRef{ID: "fake-id"}, nil
}
func (p *fakePlugin) Cleanup(_ context.Context, _ *platform.BuildArtifact) error { return nil }
func (p *fakePlugin) HealthCheck(_ context.Context) error                         { return nil }

func newRegistry() *plugin.Registry {
	return plugin.NewRegistry(slog.Default())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRegistry_Register_And_Get(t *testing.T) {
	r := newRegistry()
	p := &fakePlugin{name: "aws", version: "v1.0.0"}

	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("aws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "aws" {
		t.Errorf("Get returned plugin with name %q, want aws", got.Name())
	}
}

func TestRegistry_Register_Duplicate_ReturnsError(t *testing.T) {
	r := newRegistry()
	p := &fakePlugin{name: "aws", version: "v1.0.0"}

	if err := r.Register(p); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(p); err == nil {
		t.Error("second Register with same name should return error, got nil")
	}
}

func TestRegistry_Get_NotFound_ReturnsError(t *testing.T) {
	r := newRegistry()
	if _, err := r.Get("nonexistent"); err == nil {
		t.Error("Get for unregistered plugin should return error, got nil")
	}
}

func TestRegistry_Supports_True_When_Registered(t *testing.T) {
	r := newRegistry()
	r.Register(&fakePlugin{name: "vsphere", version: "v1"}) //nolint:errcheck

	if !r.Supports("vsphere") {
		t.Error("Supports should return true for registered provider")
	}
}

func TestRegistry_Supports_False_When_NotRegistered(t *testing.T) {
	r := newRegistry()
	if r.Supports("gcp") {
		t.Error("Supports should return false for unregistered provider")
	}
}

func TestRegistry_Deregister_RemovesPlugin(t *testing.T) {
	r := newRegistry()
	r.Register(&fakePlugin{name: "openstack", version: "v1"}) //nolint:errcheck

	r.Deregister("openstack")
	if r.Supports("openstack") {
		t.Error("Supports should return false after Deregister")
	}
}

func TestRegistry_List_ReturnsAllNames(t *testing.T) {
	r := newRegistry()
	r.Register(&fakePlugin{name: "aws", version: "v1"})   //nolint:errcheck
	r.Register(&fakePlugin{name: "azure", version: "v1"}) //nolint:errcheck

	list := r.List()
	if len(list) != 2 {
		t.Errorf("List() returned %d entries, want 2: %v", len(list), list)
	}
}

func TestRegistry_List_EmptyRegistry(t *testing.T) {
	r := newRegistry()
	list := r.List()
	if len(list) != 0 {
		t.Errorf("List() on empty registry returned %v, want []", list)
	}
}
