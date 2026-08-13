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
	external  map[string]externalEntry
	log       *slog.Logger
}

type externalEntry struct {
	plugin   platform.Plugin
	ownerUID string
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
		external:  make(map[string]externalEntry),
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

// RegisterExternal registers or replaces the adapter owned by one
// PlatformProvider resource. External adapters are keyed by installation name,
// not by their advertised logical provider name, so an external "aws" can
// coexist with the built-in "aws" and with other explicitly selected external
// AWS implementations.
func (r *Registry) RegisterExternal(ownerName, ownerUID string, p platform.Plugin) error {
	if ownerName == "" {
		return fmt.Errorf("external PlatformProvider owner name is required")
	}
	if err := platform.ValidatePluginContract(p); err != nil {
		return err
	}

	r.mu.Lock()
	previous, replaced := r.external[ownerName]
	r.external[ownerName] = externalEntry{plugin: p, ownerUID: ownerUID}
	r.mu.Unlock()

	if replaced {
		closePlugin(previous.plugin)
	}
	r.log.Info("external platform provider registered",
		slog.String("installation", ownerName),
		slog.String("ownerUID", ownerUID),
		slog.String("provider", p.Name()),
		slog.String("version", p.Version()),
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

// DeregisterExternal removes only the adapter owned by the supplied
// PlatformProvider UID. The UID guard prevents deletion of a newly recreated
// resource by a stale reconciliation for its predecessor.
func (r *Registry) DeregisterExternal(ownerName, ownerUID string) bool {
	r.mu.Lock()
	entry, ok := r.external[ownerName]
	if !ok || entry.ownerUID != ownerUID {
		r.mu.Unlock()
		return false
	}
	delete(r.external, ownerName)
	r.mu.Unlock()

	closePlugin(entry.plugin)
	r.log.Info("external platform provider deregistered",
		slog.String("installation", ownerName),
		slog.String("ownerUID", ownerUID),
		slog.String("provider", entry.plugin.Name()),
	)
	return true
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

// External returns the adapter owned by an explicitly selected
// PlatformProvider installation and verifies both resource identity and the
// advertised logical provider name.
func (r *Registry) External(ownerName, ownerUID, providerName string) (platform.Plugin, error) {
	r.mu.RLock()
	entry, ok := r.external[ownerName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("external PlatformProvider %q is not registered or healthy", ownerName)
	}
	if entry.ownerUID != ownerUID {
		return nil, fmt.Errorf("external PlatformProvider %q registration belongs to a different resource generation", ownerName)
	}
	if entry.plugin.Name() != providerName {
		return nil, fmt.Errorf("external PlatformProvider %q advertises provider %q, want %q", ownerName, entry.plugin.Name(), providerName)
	}
	return entry.plugin, nil
}

// SupportsExternal reports whether the exact PlatformProvider resource owns a
// registered adapter for the expected logical provider name.
func (r *Registry) SupportsExternal(ownerName, ownerUID, providerName string) bool {
	_, err := r.External(ownerName, ownerUID, providerName)
	return err == nil
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

func closePlugin(p platform.Plugin) {
	if closer, ok := p.(platform.ClosePlugin); ok {
		_ = closer.Close()
	}
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
