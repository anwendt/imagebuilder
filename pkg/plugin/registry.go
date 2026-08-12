// pkg/plugin/registry.go
//
// Plugin registry — analog to database/sql driver registration.
// Built-in plugins register themselves via init() with a blank import in main.go:
//
//	import _ "github.com/anwendt/imagebuilder/plugins/aws"
//
// External (gRPC) plugins are registered by the PlatformProvider controller
// once the provider pod is healthy and has completed the capability handshake.

package plugin

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// Registry holds immutable capability prototypes and optional factories.
type Registry struct {
	mu        sync.RWMutex
	plugins   map[string]platform.Plugin
	factories map[string]Factory
	log       *slog.Logger
}

// Factory constructs an unconfigured provider instance. The caller owns the
// returned instance and must call Init with exactly one ProviderConfig before
// using it. Factories are used by built-in providers to prevent credentials,
// clients, and configuration from being shared across concurrent reconciles.
type Factory func() platform.Plugin

// NewRegistry creates an empty Registry.
func NewRegistry(log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		plugins:   make(map[string]platform.Plugin),
		factories: make(map[string]Factory),
		log:       log,
	}
}

// RegisterFactory registers provider capabilities together with a constructor
// for isolated operation instances.
func (r *Registry) RegisterFactory(prototype platform.Plugin, factory Factory) error {
	if factory == nil {
		return fmt.Errorf("platform plugin factory is required")
	}
	if err := r.Register(prototype); err != nil {
		return err
	}

	instance := factory()
	if err := platform.ValidatePluginContract(instance); err != nil {
		r.Deregister(prototype.Name())
		return fmt.Errorf("validate platform plugin %q factory: %w", prototype.Name(), err)
	}
	if instance.Name() != prototype.Name() {
		r.Deregister(prototype.Name())
		return fmt.Errorf("platform plugin factory returned %q, want %q", instance.Name(), prototype.Name())
	}

	r.mu.Lock()
	r.factories[prototype.Name()] = factory
	r.mu.Unlock()
	return nil
}

// Register adds a plugin to the registry.
// Returns an error if a plugin with the same name is already registered.
// This is called from plugin init() functions and from the gRPC provider controller.
func (r *Registry) Register(p platform.Plugin) error {
	if err := platform.ValidatePluginContract(p); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Name()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("platform plugin %q is already registered", name)
	}

	r.plugins[name] = p
	r.log.Info("platform plugin registered",
		slog.String("plugin", name),
		slog.String("version", p.Version()),
		slog.Any("formats", p.SupportedFormats()),
		slog.Any("os", p.SupportedOS()),
	)
	return nil
}

// Deregister removes a plugin — called when an external provider pod is deleted.
func (r *Registry) Deregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.plugins, name)
	delete(r.factories, name)
	r.log.Info("platform plugin deregistered", slog.String("plugin", name))
}

// New returns an isolated provider instance when the provider registered a
// factory. Legacy plugins and external gRPC adapters are safe shared clients
// and are returned directly.
func (r *Registry) New(name string) (platform.Plugin, error) {
	r.mu.RLock()
	p, ok := r.plugins[name]
	factory := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf(
			"platform plugin %q not found — is the PlatformProvider installed?", name,
		)
	}
	if factory == nil {
		return p, nil
	}
	instance := factory()
	if err := platform.ValidatePluginContract(instance); err != nil {
		return nil, fmt.Errorf("construct platform plugin %q: %w", name, err)
	}
	if instance.Name() != name {
		return nil, fmt.Errorf("platform plugin factory returned %q, want %q", instance.Name(), name)
	}
	return instance, nil
}

// Get returns the registered capability prototype or shared external adapter.
// ProviderConfig-bound operations must use New instead.
func (r *Registry) Get(name string) (platform.Plugin, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.plugins[name]
	if !ok {
		return nil, fmt.Errorf(
			"platform plugin %q not found — is the PlatformProvider installed?", name,
		)
	}
	return p, nil
}

// List returns the names of all registered plugins.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		names = append(names, name)
	}
	return names
}

// Supports returns true if a plugin for the given provider name is registered.
func (r *Registry) Supports(provider string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.plugins[provider]
	return ok
}

// ---------------------------------------------------------------------------
// Global default registry — used by built-in plugin init() functions.
// The operator main.go passes this registry to all controllers.
// ---------------------------------------------------------------------------

var defaultRegistry = NewRegistry(slog.Default())

// Register adds a plugin to the default registry.
// Called from plugin package init() functions via blank import.
func Register(p platform.Plugin) error {
	return defaultRegistry.Register(p)
}

// RegisterFactory adds a built-in provider factory to the default registry.
func RegisterFactory(prototype platform.Plugin, factory Factory) error {
	return defaultRegistry.RegisterFactory(prototype, factory)
}

// Default returns the global default registry.
func Default() *Registry {
	return defaultRegistry
}
