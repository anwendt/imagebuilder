package evidencejob

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

const (
	// ArtifactType is the OCI artifactType of a VM image descriptor manifest.
	ArtifactType = "application/vnd.tsigitops.vmimage.v1+json"
	// DescriptorMediaType is the media type of the descriptor layer.
	DescriptorMediaType = "application/vnd.tsigitops.vmimage.descriptor.v1+json"
	// IndexArtifactType marks OCI indexes written by the controller.
	IndexArtifactType = "application/vnd.tsigitops.vmimage.index.v1+json"
	// PlatformAnnotation identifies the platform entry inside the index.
	PlatformAnnotation = "io.tsigitops.vmimage.platform"

	// BuildType is the SLSA v1 buildType of controller-published VM images.
	BuildType = "https://github.com/anwendt/imagebuilder/buildtypes/vmimage-remote/v1"
	// BuilderID identifies the core controller as SLSA builder.
	BuilderID = "https://github.com/anwendt/imagebuilder/controller"

	// Predicate types attached by the publication Job (identical to the
	// ImageFactory, artifactbuild ADR-0010).
	PredicateSignature  = "https://sigstore.dev/cosign/sign/v1"
	PredicateProvenance = "https://slsa.dev/provenance/v1"
	PredicateCycloneDX  = "https://cyclonedx.org/bom"
	PredicateTrivy      = "https://tsigitops.t-systems.net/attestations/trivy/v1"
)

// DescriptorInput is everything the controller knows about a finished build.
type DescriptorInput struct {
	Image          *v1alpha1.VMImage
	Provider       string
	Images         []platform.RemoteImageRef
	Bundle         *evidence.Bundle
	BuildID        string
	BuilderVersion string
	StartedOn      time.Time
	FinishedOn     time.Time
}

// Descriptor is the VM image descriptor pushed as OCI artifact.
type Descriptor struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	Kind            string                   `json:"kind"`
	Subject         v1alpha1.EvidenceSubject `json:"subject"`
	Provider        string                   `json:"provider"`
	ProviderConfig  string                   `json:"providerConfig"`
	Images          []DescriptorImage        `json:"images"`
	Source          DescriptorSource         `json:"source"`
	OS              v1alpha1.OSSpec          `json:"os"`
	Provisioners    []DescriptorProvisioner  `json:"provisioners"`
	Evidence        DescriptorEvidence       `json:"evidence"`
	VMImage         DescriptorVMImage        `json:"vmImage"`
	Builder         DescriptorBuilder        `json:"builder"`
	BuildStartedOn  string                   `json:"buildStartedOn"`
	BuildFinishedOn string                   `json:"buildFinishedOn"`
}

type DescriptorImage struct {
	ImageRef string            `json:"imageRef"`
	Name     string            `json:"name,omitempty"`
	Location string            `json:"location,omitempty"`
	Format   string            `json:"format,omitempty"`
	Checksum string            `json:"checksum,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

type DescriptorSource struct {
	Type        string                   `json:"type"`
	URN         string                   `json:"urn,omitempty"`
	Marketplace *v1alpha1.MarketplaceRef `json:"marketplace,omitempty"`
	URL         string                   `json:"url,omitempty"`
	SHA256      string                   `json:"sha256,omitempty"`
	Checksum    string                   `json:"checksum,omitempty"`
	ProviderRef string                   `json:"providerRef,omitempty"`
}

type DescriptorProvisioner struct {
	Index        int            `json:"index"`
	Type         string         `json:"type"`
	Git          *DescriptorGit `json:"git,omitempty"`
	InlineSHA256 string         `json:"inlineSha256,omitempty"`
}

type DescriptorGit struct {
	URL  string `json:"url"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

type DescriptorEvidence struct {
	BundleDigest      string          `json:"bundleDigest"`
	BundleMediaType   string          `json:"bundleMediaType"`
	SBOMFormat        string          `json:"sbomFormat"`
	SBOMDigest        string          `json:"sbomDigest"`
	TrivyReportDigest string          `json:"trivyReportDigest"`
	Collector         json.RawMessage `json:"collector,omitempty"`
}

type DescriptorVMImage struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Generation int64  `json:"generation"`
	Revision   string `json:"revision,omitempty"`
	BuildID    string `json:"buildId"`
}

type DescriptorBuilder struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// RenderDescriptor returns the canonical JSON of the VM image descriptor.
func RenderDescriptor(in DescriptorInput) ([]byte, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	img := in.Image
	target := img.Spec.Targets[0]
	d := Descriptor{
		SchemaVersion:   1,
		Kind:            "VMImageDescriptor",
		Subject:         *img.Spec.Evidence.Subject,
		Provider:        in.Provider,
		ProviderConfig:  target.ProviderConfigRef.Name,
		Source:          descriptorSource(img.Spec.Source),
		OS:              img.Spec.OS,
		Provisioners:    descriptorProvisioners(img.Spec.Provisioners),
		VMImage:         vmImageRef(in),
		Builder:         DescriptorBuilder{ID: BuilderID, Version: builderVersion(in.BuilderVersion)},
		BuildStartedOn:  formatTime(in.StartedOn),
		BuildFinishedOn: formatTime(in.FinishedOn),
		Evidence: DescriptorEvidence{
			BundleDigest:      in.Bundle.Digest(),
			BundleMediaType:   evidence.MediaType,
			SBOMFormat:        img.Spec.Evidence.SBOMFormat,
			SBOMDigest:        "sha256:" + evidence.SHA256Hex(in.Bundle.SBOM),
			TrivyReportDigest: "sha256:" + evidence.SHA256Hex(in.Bundle.TrivyReport),
		},
	}
	if len(in.Bundle.Collector) > 0 {
		d.Evidence.Collector = json.RawMessage(in.Bundle.Collector)
	}
	for _, image := range in.Images {
		d.Images = append(d.Images, DescriptorImage{
			ImageRef: image.ImageRef.ID,
			Name:     image.ImageRef.Name,
			Location: image.ImageRef.Location,
			Format:   string(image.Format),
			Checksum: image.Checksum,
			Tags:     image.ImageRef.Tags,
		})
	}
	return json.MarshalIndent(d, "", "  ")
}

func (in DescriptorInput) validate() error {
	switch {
	case in.Image == nil:
		return fmt.Errorf("descriptor: VMImage is required")
	case in.Image.Spec.Evidence == nil || in.Image.Spec.Evidence.Subject == nil:
		return fmt.Errorf("descriptor: spec.evidence.subject is required")
	case len(in.Image.Spec.Targets) != 1:
		return fmt.Errorf("descriptor: exactly one target is required")
	case in.Bundle == nil:
		return fmt.Errorf("descriptor: evidence bundle is required")
	case len(in.Images) == 0:
		return fmt.Errorf("descriptor: at least one registered image is required")
	}
	return nil
}

func vmImageRef(in DescriptorInput) DescriptorVMImage {
	return DescriptorVMImage{
		Namespace:  in.Image.Namespace,
		Name:       in.Image.Name,
		UID:        string(in.Image.UID),
		Generation: in.Image.Generation,
		Revision:   in.Image.Spec.Build.Revision,
		BuildID:    in.BuildID,
	}
}

func builderVersion(version string) string {
	if strings.TrimSpace(version) == "" {
		return "unknown"
	}
	return strings.TrimSpace(version)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func descriptorSource(source v1alpha1.SourceSpec) DescriptorSource {
	out := DescriptorSource{Type: source.Type, URL: source.URL, Checksum: source.Checksum, ProviderRef: source.ProviderRef}
	if strings.HasPrefix(source.Checksum, "sha256:") {
		out.SHA256 = strings.TrimPrefix(source.Checksum, "sha256:")
	}
	if ref := source.MarketplaceRef; ref != nil {
		copied := *ref
		out.Marketplace = &copied
		out.URN = strings.Join([]string{ref.Publisher, ref.Offer, ref.SKU, ref.Version}, ":")
	}
	return out
}

func descriptorProvisioners(specs []v1alpha1.ProvisionerSpec) []DescriptorProvisioner {
	out := make([]DescriptorProvisioner, 0, len(specs))
	for i, spec := range specs {
		p := DescriptorProvisioner{Index: i, Type: spec.Type}
		if spec.Source != nil && spec.Source.Git != nil {
			p.Git = &DescriptorGit{URL: spec.Source.Git.URL, Ref: spec.Source.Git.Ref, Path: spec.Source.Git.Path}
		} else if spec.Inline != "" {
			p.InlineSHA256 = evidence.SHA256Hex([]byte(spec.Inline))
		}
		out = append(out, p)
	}
	return out
}

var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// RenderProvenance returns the SLSA v1 provenance predicate for the
// descriptor (the in-toto statement is created by cosign attest).
func RenderProvenance(in DescriptorInput, descriptor []byte) ([]byte, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	img := in.Image
	target := img.Spec.Targets[0]
	type dependency struct {
		URI    string            `json:"uri"`
		Digest map[string]string `json:"digest,omitempty"`
		Name   string            `json:"name,omitempty"`
	}
	var deps []dependency
	source := descriptorSource(img.Spec.Source)
	switch {
	case source.Marketplace != nil:
		deps = append(deps, dependency{URI: "marketplace:" + source.URN, Name: "source"})
	case source.URL != "":
		dep := dependency{URI: source.URL, Name: "source"}
		if source.SHA256 != "" {
			dep.Digest = map[string]string{"sha256": source.SHA256}
		}
		deps = append(deps, dep)
	case source.ProviderRef != "":
		deps = append(deps, dependency{URI: in.Provider + ":" + source.ProviderRef, Name: "source"})
	}
	seen := map[string]bool{}
	for _, p := range descriptorProvisioners(img.Spec.Provisioners) {
		if p.Git == nil {
			continue
		}
		uri := "git+" + p.Git.URL + "@" + p.Git.Ref
		if seen[uri] {
			continue
		}
		seen[uri] = true
		dep := dependency{URI: uri}
		if gitCommitPattern.MatchString(p.Git.Ref) {
			dep.Digest = map[string]string{"gitCommit": p.Git.Ref}
		}
		deps = append(deps, dep)
	}
	sort.SliceStable(deps, func(i, j int) bool { return deps[i].Name > deps[j].Name })
	predicate := map[string]any{
		"buildDefinition": map[string]any{
			"buildType": BuildType,
			"externalParameters": map[string]any{
				"vmImage":        map[string]string{"namespace": img.Namespace, "name": img.Name},
				"subject":        img.Spec.Evidence.Subject,
				"source":         source,
				"os":             img.Spec.OS,
				"provisioners":   descriptorProvisioners(img.Spec.Provisioners),
				"providerConfig": target.ProviderConfigRef.Name,
				"format":         target.Format,
			},
			"internalParameters": map[string]any{
				"provider":   in.Provider,
				"buildId":    in.BuildID,
				"generation": img.Generation,
				"revision":   img.Spec.Build.Revision,
			},
			"resolvedDependencies": deps,
		},
		"runDetails": map[string]any{
			"builder": map[string]any{
				"id":      BuilderID,
				"version": map[string]string{"imagebuilder": builderVersion(in.BuilderVersion)},
			},
			"metadata": map[string]string{
				"invocationId": string(img.UID) + "/" + in.BuildID,
				"startedOn":    formatTime(in.StartedOn),
				"finishedOn":   formatTime(in.FinishedOn),
			},
			"byproducts": []map[string]any{
				{"name": "evidence-bundle", "digest": map[string]string{"sha256": in.Bundle.SHA256}, "mediaType": evidence.MediaType},
				{"name": "vm-image-descriptor", "digest": map[string]string{"sha256": evidence.SHA256Hex(descriptor)}, "mediaType": DescriptorMediaType},
			},
		},
	}
	return json.MarshalIndent(predicate, "", "  ")
}
