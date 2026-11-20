package v1alpha1

import (
	"strings"
	"testing"
)

func validControllerEvidenceImage() *VMImage {
	img := validRemoteImageWithEvidence()
	img.Spec.Evidence.Publisher = EvidencePublisherController
	img.Spec.Evidence.SBOMFormat = "cyclonedx-json"
	img.Spec.Evidence.RegistryRepository = "oci://registry.example.com/platform/vmimages"
	img.Spec.Evidence.Subject = &EvidenceSubject{Name: "ubuntu-2604-base", Version: "1.2.0", Platform: "azure"}
	img.Spec.Provisioners = []ProvisionerSpec{
		{Type: "shell", Inline: "echo prepare"},
		{Type: "evidence", Inline: "echo collect", Env: []EnvVar{{Name: "TRIVY_VERSION", Value: "0.70.0"}}},
		{Type: "shell", Inline: "systemd-run --on-active=30 waagent -deprovision+user -force"},
	}
	return img
}

func TestVMImageWebhook_ControllerEvidencePublisher(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*VMImage)
		wantError string
	}{
		{name: "valid controller evidence with a later provisioner"},
		{
			name: "valid controller evidence as final provisioner",
			mutate: func(img *VMImage) {
				img.Spec.Provisioners = img.Spec.Provisioners[:2]
			},
		},
		{
			name: "valid vsphere platform name",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Subject.Platform = "vsphere-fci-c042"
			},
		},
		{
			name: "unknown publisher",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Publisher = "guest"
			},
			wantError: "spec.evidence.publisher must be provider or controller",
		},
		{
			name: "subject missing",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Subject = nil
			},
			wantError: "spec.evidence.subject is required",
		},
		{
			name: "subject name invalid",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Subject.Name = "Ubuntu Base"
			},
			wantError: "spec.evidence.subject.name",
		},
		{
			name: "subject version invalid",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Subject.Version = "1.2.0+build/1"
			},
			wantError: "spec.evidence.subject.version",
		},
		{
			name: "subject platform invalid",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Subject.Platform = ""
			},
			wantError: "spec.evidence.subject.platform",
		},
		{
			name: "spdx sbom with controller publisher",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.SBOMFormat = "spdx-json"
			},
			wantError: "requires spec.evidence.sbomFormat cyclonedx-json",
		},
		{
			name: "controller publisher must be required",
			mutate: func(img *VMImage) {
				img.Spec.Evidence.Required = false
			},
			wantError: "requires spec.evidence.required true",
		},
		{
			name: "two evidence provisioners",
			mutate: func(img *VMImage) {
				img.Spec.Provisioners = append(img.Spec.Provisioners, ProvisionerSpec{Type: "evidence", Inline: "echo again"})
			},
			wantError: "exactly one evidence provisioner",
		},
		{
			name: "evidence provisioner env from secret",
			mutate: func(img *VMImage) {
				img.Spec.Provisioners[1].Env = []EnvVar{{Name: "SCANNER", ValueFrom: &EnvVarSource{SecretKeyRef: &SecretKeyRef{Name: "s", Key: "k"}}}}
			},
			wantError: "must not carry credentials",
		},
		{
			name: "evidence provisioner credential-like env",
			mutate: func(img *VMImage) {
				img.Spec.Provisioners[1].Env = []EnvVar{{Name: "REGISTRY_PASSWORD", Value: "x"}}
			},
			wantError: "must not carry credentials",
		},
		{
			name: "evidence provisioner credential in inline URL",
			mutate: func(img *VMImage) {
				img.Spec.Provisioners[1].Inline = "curl https://user:secret@example.com/trivy.tgz"
			},
			wantError: "must not carry credentials",
		},
		{
			name: "local build mode",
			mutate: func(img *VMImage) {
				img.Spec.Build.Mode = BuildModeLocal
			},
			wantError: "only for spec.build.mode remote",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := validControllerEvidenceImage()
			if tt.mutate != nil {
				tt.mutate(img)
			}
			_, err := img.ValidateCreate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateCreate returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ValidateCreate error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestVMImageWebhook_ProviderPublisherKeepsFinalRule(t *testing.T) {
	img := validRemoteImageWithEvidence()
	img.Spec.Evidence.Publisher = EvidencePublisherProvider
	img.Spec.Provisioners = append(img.Spec.Provisioners, ProvisionerSpec{Type: "shell", Inline: "echo after"})
	if _, err := img.ValidateCreate(); err == nil || !strings.Contains(err.Error(), "must be the final spec.provisioners entry") {
		t.Fatalf("ValidateCreate error = %v, want final provisioner rule for publisher=provider", err)
	}
}

func TestImageVersion(t *testing.T) {
	tests := []struct {
		name string
		img  VMImage
		want string
	}{
		{name: "none"},
		{
			name: "annotation",
			img:  VMImage{},
			want: "2.0.0",
		},
		{
			name: "subject wins",
			img:  VMImage{Spec: VMImageSpec{Evidence: &EvidenceSpec{Subject: &EvidenceSubject{Version: "1.2.0"}}}},
			want: "1.2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want != "" {
				tt.img.Annotations = map[string]string{ImageVersionAnnotation: "2.0.0"}
			}
			if got := tt.img.ImageVersion(); got != tt.want {
				t.Fatalf("ImageVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
