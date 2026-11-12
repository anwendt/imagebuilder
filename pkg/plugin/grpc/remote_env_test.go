package grpc

import (
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestRemoteBuildProtoRequestCarriesLiteralProvisionerEnv(t *testing.T) {
	req := &platform.RemoteBuildRequest{
		Provisioners: []v1alpha1.ProvisionerSpec{{
			Type:   "shell",
			Inline: "true",
			Env: []v1alpha1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://proxy.example.com:3128"},
				{Name: "FROM_SECRET", ValueFrom: &v1alpha1.EnvVarSource{SecretKeyRef: &v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
			},
		}},
	}
	got := remoteBuildProtoRequest(req).GetProvisioners()[0].GetEnv()
	if len(got) != 1 || got["HTTP_PROXY"] != "http://proxy.example.com:3128" {
		t.Fatalf("env = %v, want only the literal variable", got)
	}
}
