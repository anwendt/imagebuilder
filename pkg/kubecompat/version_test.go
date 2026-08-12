package kubecompat_test

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/version"

	"github.com/anwendt/imagebuilder/pkg/kubecompat"
)

type fakeDiscovery struct {
	info *version.Info
	err  error
}

func (f fakeDiscovery) ServerVersion() (*version.Info, error) { return f.info, f.err }

func TestCheckVersionCompatibilityBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		info    *version.Info
		wantErr bool
	}{
		{name: "below boundary", info: &version.Info{Major: "1", Minor: "28", GitVersion: "v1.28.15"}, wantErr: true},
		{name: "minimum", info: &version.Info{Major: "1", Minor: "29", GitVersion: "v1.29.0"}},
		{name: "vendor suffix", info: &version.Info{Major: "1", Minor: "29+", GitVersion: "v1.29.3-eks-123"}},
		{name: "stable sidecars", info: &version.Info{Major: "1", Minor: "33", GitVersion: "v1.33.1"}},
		{name: "future major", info: &version.Info{Major: "2", Minor: "0", GitVersion: "v2.0.0"}},
		{name: "missing", info: nil, wantErr: true},
		{name: "malformed", info: &version.Info{Major: "one", Minor: "29"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := kubecompat.CheckVersion(test.info)
			if (err != nil) != test.wantErr {
				t.Fatalf("CheckVersion error = %v, wantErr %t", err, test.wantErr)
			}
			if test.name == "below boundary" && !strings.Contains(err.Error(), kubecompat.MinimumKubernetesVersion) {
				t.Fatalf("boundary error = %q", err)
			}
		})
	}
}

func TestCheckServerFailsClosedWhenDiscoveryFails(t *testing.T) {
	err := kubecompat.CheckServer(fakeDiscovery{err: errors.New("forbidden")})
	if err == nil || !strings.Contains(err.Error(), "discover Kubernetes server version") {
		t.Fatalf("CheckServer error = %v", err)
	}
}

func TestCheckServerAcceptsSupportedCluster(t *testing.T) {
	err := kubecompat.CheckServer(fakeDiscovery{info: &version.Info{Major: "1", Minor: "29+", GitVersion: "v1.29.2+k3s1"}})
	if err != nil {
		t.Fatalf("CheckServer returned error: %v", err)
	}
}
