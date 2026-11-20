package main

import (
	"flag"
	"strings"
	"testing"
)

func TestEvidenceFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, f *evidenceFlags)
	}{
		{name: "disabled by default", args: nil},
		{
			name: "enabled",
			args: []string{"--evidence-publisher-enabled", "--evidence-namespace=imagebuilder-system", "--evidence-registry-secret=acr-push", "--evidence-cosign-secret=imagefactory-cosign", "--evidence-memory-limit=512Mi", "--evidence-https-proxy=http://proxy:3128", "--imagebuilder-version=v0.6.0"},
			check: func(t *testing.T, f *evidenceFlags) {
				cfg, err := f.config()
				if err != nil {
					t.Fatal(err)
				}
				if !cfg.Enabled || cfg.Resources.Limits.Memory().String() != "512Mi" || cfg.HTTPSProxy != "http://proxy:3128" || cfg.BuilderVersion != "v0.6.0" || cfg.CosignKeyKey != "cosign.key" {
					t.Fatalf("config = %+v", cfg)
				}
			},
		},
		{name: "enabled without secrets", args: []string{"--evidence-publisher-enabled", "--evidence-namespace=x"}, wantErr: "not configured"},
		{name: "bad quantity", args: []string{"--evidence-cpu-limit=lots"}, wantErr: "--evidence-cpu-limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			f := registerEvidenceFlags(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			_, err := f.config()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("config() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, f)
			}
		})
	}
}
