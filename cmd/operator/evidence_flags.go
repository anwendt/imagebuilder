package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/anwendt/imagebuilder/pkg/controller/evidencejob"
)

// evidenceFlags configures the ADR-011 controller evidence publisher.
type evidenceFlags struct {
	enabled           bool
	namespace         string
	orasImage         string
	cosignImage       string
	registrySecret    string
	cosignSecret      string
	cosignKeyKey      string
	cosignPasswordKey string
	cpuRequest        string
	memoryRequest     string
	cpuLimit          string
	memoryLimit       string
	httpsProxy        string
	noProxy           string
	activeDeadline    time.Duration
	builderVersion    string
}

func registerEvidenceFlags(fs *flag.FlagSet) *evidenceFlags {
	f := &evidenceFlags{}
	fs.BoolVar(&f.enabled, "evidence-publisher-enabled", false, "Enable the controller evidence publisher for spec.evidence.publisher=controller (ADR-011)")
	fs.StringVar(&f.namespace, "evidence-namespace", os.Getenv("POD_NAMESPACE"), "Namespace for evidence publication Jobs and bundle Secrets (the controller namespace)")
	fs.StringVar(&f.orasImage, "evidence-oras-image", evidencejob.DefaultOrasImage, "ORAS image (with a POSIX shell) used by the publication Job")
	fs.StringVar(&f.cosignImage, "evidence-cosign-image", evidencejob.DefaultCosignImage, "Cosign v3 image (with a POSIX shell) used by the publication Job")
	fs.StringVar(&f.registrySecret, "evidence-registry-secret", "", "kubernetes.io/dockerconfigjson Secret with push credentials for the evidence registry")
	fs.StringVar(&f.cosignSecret, "evidence-cosign-secret", "", "Secret holding the cosign private key and password")
	fs.StringVar(&f.cosignKeyKey, "evidence-cosign-key-key", "cosign.key", "Key of the cosign private key in --evidence-cosign-secret")
	fs.StringVar(&f.cosignPasswordKey, "evidence-cosign-password-key", "cosign.password", "Key of the cosign key password in --evidence-cosign-secret")
	fs.StringVar(&f.cpuRequest, "evidence-cpu-request", "50m", "CPU request of each publication step")
	fs.StringVar(&f.memoryRequest, "evidence-memory-request", "64Mi", "Memory request of each publication step")
	fs.StringVar(&f.cpuLimit, "evidence-cpu-limit", "500m", "CPU limit of each publication step")
	fs.StringVar(&f.memoryLimit, "evidence-memory-limit", "256Mi", "Memory limit of each publication step")
	fs.StringVar(&f.httpsProxy, "evidence-https-proxy", "", "HTTPS proxy for registry access of the publication Job")
	fs.StringVar(&f.noProxy, "evidence-no-proxy", "", "NO_PROXY for the publication Job")
	fs.DurationVar(&f.activeDeadline, "evidence-job-timeout", 30*time.Minute, "Active deadline of a publication Job")
	fs.StringVar(&f.builderVersion, "imagebuilder-version", os.Getenv("IMAGEBUILDER_VERSION"), "Imagebuilder release recorded in VM image descriptors and provenance")
	return f
}

func (f *evidenceFlags) config() (evidencejob.Config, error) {
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	for _, item := range []struct {
		name  string
		value string
		list  corev1.ResourceList
		key   corev1.ResourceName
	}{
		{"--evidence-cpu-request", f.cpuRequest, resources.Requests, corev1.ResourceCPU},
		{"--evidence-memory-request", f.memoryRequest, resources.Requests, corev1.ResourceMemory},
		{"--evidence-cpu-limit", f.cpuLimit, resources.Limits, corev1.ResourceCPU},
		{"--evidence-memory-limit", f.memoryLimit, resources.Limits, corev1.ResourceMemory},
	} {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(item.value)
		if err != nil {
			return evidencejob.Config{}, fmt.Errorf("%s: %w", item.name, err)
		}
		item.list[item.key] = quantity
	}
	cfg := evidencejob.Config{
		Enabled:           f.enabled,
		Namespace:         strings.TrimSpace(f.namespace),
		OrasImage:         strings.TrimSpace(f.orasImage),
		CosignImage:       strings.TrimSpace(f.cosignImage),
		RegistrySecret:    strings.TrimSpace(f.registrySecret),
		CosignSecret:      strings.TrimSpace(f.cosignSecret),
		CosignKeyKey:      strings.TrimSpace(f.cosignKeyKey),
		CosignPasswordKey: strings.TrimSpace(f.cosignPasswordKey),
		Resources:         resources,
		HTTPSProxy:        strings.TrimSpace(f.httpsProxy),
		NoProxy:           strings.TrimSpace(f.noProxy),
		ActiveDeadline:    f.activeDeadline,
		BuilderVersion:    strings.TrimSpace(f.builderVersion),
	}
	if cfg.Enabled {
		if err := cfg.Validate(); err != nil {
			return evidencejob.Config{}, err
		}
	}
	return cfg, nil
}
