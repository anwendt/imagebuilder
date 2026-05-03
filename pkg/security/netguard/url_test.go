package netguard_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/security/netguard"
)

type fakeResolver map[string][]string

func (r fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	addrs, ok := r[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return addrs, nil
}

func TestValidatePublicHTTPSURL(t *testing.T) {
	tests := []struct {
		name          string
		rawURL        string
		opts          netguard.Options
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:          "rejects plain http",
			rawURL:        "http://images.example.test/source.img",
			opts:          netguard.Options{Resolver: fakeResolver{"images.example.test": {"93.184.216.34"}}},
			wantErr:       true,
			wantErrSubstr: "https",
		},
		{
			name:          "rejects raw IP hosts",
			rawURL:        "https://10.0.0.1/source.img",
			wantErr:       true,
			wantErrSubstr: "raw IP",
		},
		{
			name:          "rejects DNS names resolving to private ranges",
			rawURL:        "https://metadata.example.test/source.img",
			opts:          netguard.Options{Resolver: fakeResolver{"metadata.example.test": {"169.254.169.254"}}},
			wantErr:       true,
			wantErrSubstr: "blocked range",
		},
		{
			name:          "rejects unresolved hosts by default",
			rawURL:        "https://missing.example.test/source.img",
			opts:          netguard.Options{Resolver: fakeResolver{}},
			wantErr:       true,
			wantErrSubstr: "could not be resolved",
		},
		{
			name:   "allows unresolved hosts when configured",
			rawURL: "https://missing.example.test/source.img",
			opts: netguard.Options{
				AllowUnresolved: true,
				Resolver:        fakeResolver{},
			},
		},
		{
			name:   "allows public resolved DNS names",
			rawURL: "https://images.example.test/source.img",
			opts:   netguard.Options{Resolver: fakeResolver{"images.example.test": {"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := netguard.ValidatePublicHTTPSURL(context.Background(), "field", tt.rawURL, tt.opts)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ValidatePublicHTTPSURL returned nil, want error")
				}
				if tt.wantErrSubstr != "" && !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidatePublicHTTPSURL returned error: %v", err)
			}
		})
	}
}
