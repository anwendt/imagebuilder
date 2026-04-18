// pkg/plugin/registry.go
//
// Plugin registry — analog to database/sql driver registration.
// Built-in plugins register themselves via init() with a blank import in main.go:
//
//	import _ "github.com/yourorg/imagebuilder/plugins/aws"
//
// External (gRPC) plugins are registered by the PlatformProvider controller
// once the provider pod is healthy and has completed the capability handshake.

package plugin

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/yourorg/imagebuilder/pkg/plugin/platform"
)

// Registry holds all active platform plugins.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]platform.Plugin
	log     *slog.Logger
}

// NewRegistry creates an empty Registry.
func NewRegistry(log *slog.Logger) *Registry {
	return &Registry{
		plugins: make(map[string]platform.Plugin),
		log:     log,
	}
}

// Register adds a plugin to the registry.
// Returns an error if a plugin with the same name is already registered.
// This is called from plugin init() functions and from the gRPC provider controller.
func (r *Registry) Register(p platform.Plugin) error {
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
	r.log.Info("platform plugin deregistered", slog.String("plugin", name))
}

// Get returns the plugin for the given provider name.
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

// Default returns the global default registry.
func Default() *Registry {
	return defaultRegistry
}
