package vsphere

import (
	"context"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestInitTakesEndpointFromSecretServerKey(t *testing.T) {
	cfg := validPluginConfig()
	cfg.Endpoint = ""
	cfg.SecretData["server"] = []byte("vcenter.example.com")
	p := &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.config.endpoint != "https://vcenter.example.com" {
		t.Fatalf("endpoint = %q", p.config.endpoint)
	}
}

func TestInitPrefersExplicitEndpoint(t *testing.T) {
	cfg := validPluginConfig()
	cfg.SecretData["server"] = []byte("other.example.com")
	p := &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.config.endpoint != "https://vcenter.example.com/sdk" {
		t.Fatalf("endpoint = %q", p.config.endpoint)
	}
}

func TestInitRejectsProtectedNamesInConfig(t *testing.T) {
	cfg := validPluginConfig()
	cfg.Extra["protectedNames"] = "critical-app"
	cfg.Extra["network"] = "dc-01-critical-app-net"
	p := &Plugin{client: &fakeClient{}}
	err := p.Init(context.Background(), cfg)
	assertErrContains(t, err, "protected")
}

func TestInitRequiresLibraryForPublishTo(t *testing.T) {
	cfg := validPluginConfig()
	cfg.Extra["remote.publishTo"] = "content-library"
	p := &Plugin{client: &fakeClient{}}
	assertErrContains(t, p.Init(context.Background(), cfg), "contentLibrary")
	cfg.Extra["contentLibrary"] = "templates"
	cfg.Extra["templateFolder"] = "/dc-01/vm/golden-templates"
	p = &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !p.config.publishToLibrary {
		t.Fatal("publishToLibrary should be enabled")
	}
}

func TestInitParsesBuildSettings(t *testing.T) {
	cfg := validPluginConfig()
	cfg.Extra["buildFolder"] = "/dc01/vm/imagebuilder-builds"
	cfg.Extra["buildDiskGiB"] = "40"
	cfg.Extra["downloadProxy"] = "http://proxy:8080"
	p := &Plugin{client: &fakeClient{}}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.config.buildFolder != "/dc01/vm/imagebuilder-builds" || p.config.buildDiskGiB != 40 || p.config.downloadProxy != "http://proxy:8080" {
		t.Fatalf("config = %+v", p.config)
	}
	_ = platform.PluginConfig{}
}
