package evidencejob

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

var testSHA = strings.Repeat("a", 64)

func testConfig() Config {
	return Config{Enabled: true, Namespace: "imagebuilder-system", RegistrySecret: "acr-push", CosignSecret: "imagefactory-cosign", HTTPSProxy: "http://proxy:3128", NoProxy: ".svc"}
}

func testOwner() Owner {
	return Owner{Namespace: "tenant", Name: "ubuntu-2604-base-azure", UID: "11111111-2222-3333-4444-555555555555", BuildID: "build-1"}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid"},
		{name: "disabled", mutate: func(c *Config) { c.Enabled = false }, wantErr: "disabled"},
		{name: "no namespace", mutate: func(c *Config) { c.Namespace = "" }, wantErr: "namespace"},
		{name: "no cosign", mutate: func(c *Config) { c.CosignSecret = "" }, wantErr: "cosign key Secret"},
		{name: "no registry", mutate: func(c *Config) { c.RegistrySecret = " " }, wantErr: "registry credential Secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate() = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewTarget(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		subject  [3]string
		wantRepo string
		wantErr  bool
	}{
		{name: "acr", repo: "oci://tsigitopsacr.azurecr.io/tsigitops/vmimages", subject: [3]string{"ubuntu-2604-base", "1.2.0", "azure"}, wantRepo: "tsigitopsacr.azurecr.io/tsigitops/vmimages/ubuntu-2604-base"},
		{name: "port and trailing slash", repo: "oci://registry:5000/vm/", subject: [3]string{"n", "1", "p"}, wantRepo: "registry:5000/vm/n"},
		{name: "no path", repo: "oci://registry.example.com", subject: [3]string{"n", "1", "p"}, wantErr: true},
		{name: "digest", repo: "oci://r.example.com/x@sha256:abc", subject: [3]string{"n", "1", "p"}, wantErr: true},
		{name: "uppercase path", repo: "oci://r.example.com/X", subject: [3]string{"n", "1", "p"}, wantErr: true},
		{name: "missing subject", repo: "oci://r.example.com/x", subject: [3]string{"n", "", "p"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := NewTarget(tt.repo, tt.subject[0], tt.subject[1], tt.subject[2], "azure")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewTarget() = %+v, want error", target)
				}
				return
			}
			if err != nil || target.Repository != tt.wantRepo {
				t.Fatalf("NewTarget() = %+v, %v; want repo %q", target, err, tt.wantRepo)
			}
			if target.DescriptorTag() != tt.subject[1]+"-"+tt.subject[2] {
				t.Fatalf("DescriptorTag() = %q", target.DescriptorTag())
			}
			if got := target.RefFor("sha256:" + testSHA); got != "oci://"+tt.wantRepo+"@sha256:"+testSHA {
				t.Fatalf("RefFor() = %q", got)
			}
		})
	}
}

func TestObjectNameAndLabels(t *testing.T) {
	owner := testOwner()
	name := ObjectName(owner)
	if !strings.HasPrefix(name, "ib-evidence-") || len(name) != len("ib-evidence-")+16 {
		t.Fatalf("ObjectName() = %q", name)
	}
	other := owner
	other.BuildID = "build-2"
	if ObjectName(other) == name {
		t.Fatal("ObjectName must differ per build")
	}
	owner.Name = strings.Repeat("x", 80) + "-"
	labels := Labels(owner)
	if len(labels[LabelVMImage]) > 63 || labels[LabelComponent] != Component {
		t.Fatalf("Labels() = %v", labels)
	}
}

func TestBuildSecret(t *testing.T) {
	secret := BuildSecret(testConfig(), testOwner(), []byte("b"), []byte("d"), []byte("p"))
	if secret.Namespace != "imagebuilder-system" || string(secret.Data[BundleKey]) != "b" || string(secret.Data[DescriptorKey]) != "d" || string(secret.Data[ProvenanceKey]) != "p" {
		t.Fatalf("BuildSecret() = %+v", secret)
	}
	if secret.Name != ObjectName(testOwner()) || secret.Labels[LabelVMImageUID] == "" {
		t.Fatalf("BuildSecret() metadata = %+v", secret.ObjectMeta)
	}
}

func TestBuildJob(t *testing.T) {
	target, err := NewTarget("oci://tsigitopsacr.azurecr.io/tsigitops/vmimages", "ubuntu-2604-base", "1.2.0", "azure", "azure")
	if err != nil {
		t.Fatal(err)
	}
	job, err := BuildJob(testConfig(), testOwner(), target, testSHA)
	if err != nil {
		t.Fatal(err)
	}
	spec := job.Spec.Template.Spec
	if job.Namespace != "imagebuilder-system" || *spec.AutomountServiceAccountToken || spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("job basics wrong: %+v", job.ObjectMeta)
	}
	if *job.Spec.BackoffLimit != 2 || *job.Spec.ActiveDeadlineSeconds != int64(defaultActiveDeadline.Seconds()) || job.Spec.TTLSecondsAfterFinished == nil {
		t.Fatalf("job limits wrong: %+v", job.Spec)
	}
	wantSteps := []string{"prepare", "push-descriptor", "sign-descriptor", "publish-index", "sign-index", "report"}
	all := append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...)
	if len(all) != len(wantSteps) {
		t.Fatalf("steps = %d, want %d", len(all), len(wantSteps))
	}
	for i, c := range all {
		if c.Name != wantSteps[i] {
			t.Fatalf("step %d = %q, want %q", i, c.Name, wantSteps[i])
		}
		sc := c.SecurityContext
		if sc == nil || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || !*sc.RunAsNonRoot || len(sc.Capabilities.Drop) != 1 {
			t.Fatalf("step %s security context = %+v", c.Name, sc)
		}
		if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
			t.Fatalf("step %s termination policy = %q", c.Name, c.TerminationMessagePolicy)
		}
		hasPassword := false
		for _, env := range c.Env {
			if env.Name == "COSIGN_PASSWORD" {
				hasPassword = true
				if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef.Name != "imagefactory-cosign" || env.ValueFrom.SecretKeyRef.Key != "cosign.password" {
					t.Fatalf("cosign password env = %+v", env)
				}
			}
			if env.Value != "" && strings.Contains(strings.ToLower(env.Name), "password") {
				t.Fatalf("step %s carries a literal password", c.Name)
			}
		}
		if hasPassword != strings.HasPrefix(c.Name, "sign-") {
			t.Fatalf("step %s cosign password presence = %v", c.Name, hasPassword)
		}
		for _, m := range c.VolumeMounts {
			if m.Name == "cosign" && !strings.HasPrefix(c.Name, "sign-") {
				t.Fatalf("step %s mounts the cosign key", c.Name)
			}
		}
	}
	if all[0].Image != DefaultCosignImage || all[1].Image != DefaultOrasImage {
		t.Fatalf("default images not applied: %q %q", all[0].Image, all[1].Image)
	}
	envOf := func(c corev1.Container, name string) string {
		for _, env := range c.Env {
			if env.Name == name {
				return env.Value
			}
		}
		return ""
	}
	if envOf(all[1], "REPO") != "tsigitopsacr.azurecr.io/tsigitops/vmimages/ubuntu-2604-base" || envOf(all[1], "DESCRIPTOR_TAG") != "1.2.0-azure" || envOf(all[1], "INDEX_TAG") != "1.2.0" || envOf(all[1], "HTTPS_PROXY") != "http://proxy:3128" || envOf(all[0], "BUNDLE_SHA256") != testSHA {
		t.Fatalf("step env wrong: %+v", all[1].Env)
	}
	vols := map[string]corev1.Volume{}
	for _, v := range spec.Volumes {
		vols[v.Name] = v
	}
	if vols["input"].Secret.SecretName != job.Name || vols["registry"].Secret.SecretName != "acr-push" || vols["registry"].Secret.Items[0].Key != corev1.DockerConfigJsonKey || vols["cosign"].Secret.Items[0].Key != "cosign.key" {
		t.Fatalf("volumes wrong: %+v", spec.Volumes)
	}

	if _, err := BuildJob(Config{}, testOwner(), target, testSHA); err == nil {
		t.Fatal("BuildJob accepted a disabled config")
	}
	if _, err := BuildJob(testConfig(), testOwner(), target, "nope"); err == nil {
		t.Fatal("BuildJob accepted an invalid bundle digest")
	}
	custom := testConfig()
	custom.OrasImage, custom.CosignImage, custom.ActiveDeadline = "oras:x", "cosign:y", time.Minute
	custom.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resourceMust("1Gi")}}
	job, err = BuildJob(custom, testOwner(), target, testSHA)
	if err != nil || job.Spec.Template.Spec.Containers[0].Image != "oras:x" || *job.Spec.ActiveDeadlineSeconds != 60 {
		t.Fatalf("custom config not applied: %v", err)
	}
}

func TestParseResult(t *testing.T) {
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	valid := Result{DescriptorDigest: d("1"), IndexDigest: d("2"), SignatureDigest: d("3"), ProvenanceDigest: d("4"), SBOMDigest: d("5"), VulnerabilityReportDigest: d("6"), IndexSignatureDigest: d("7")}
	raw, _ := json.Marshal(valid)
	got, err := ParseResult(" " + string(raw) + "\n")
	if err != nil || got != valid {
		t.Fatalf("ParseResult() = %+v, %v", got, err)
	}
	broken := valid
	broken.SBOMDigest = "sha256:short"
	raw, _ = json.Marshal(broken)
	if _, err := ParseResult(string(raw)); err == nil || !strings.Contains(err.Error(), "sbomDigest") {
		t.Fatalf("ParseResult() error = %v, want sbomDigest", err)
	}
	if _, err := ParseResult("not json"); err == nil {
		t.Fatal("ParseResult accepted garbage")
	}
}

func TestPodResult(t *testing.T) {
	ok := corev1.ContainerStatus{Name: "prepare", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}
	tests := []struct {
		name       string
		pod        corev1.Pod
		wantMsg    string
		wantFailed bool
	}{
		{name: "running"},
		{
			name: "succeeded",
			pod: corev1.Pod{Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{ok},
				ContainerStatuses:     []corev1.ContainerStatus{{Name: "report", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "{}"}}}},
			}},
			wantMsg: "{}",
		},
		{
			name: "init failed",
			pod: corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
				ok,
				{Name: "push-descriptor", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "push failed: 401"}}},
			}}},
			wantMsg:    "step push-descriptor failed: push failed: 401",
			wantFailed: true,
		},
		{
			name: "last state failure with reason",
			pod: corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "sign-index", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}}},
			}}},
			wantMsg:    "step sign-index failed: OOMKilled",
			wantFailed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, failed := PodResult(&tt.pod)
			if msg != tt.wantMsg || failed != tt.wantFailed {
				t.Fatalf("PodResult() = %q, %v; want %q, %v", msg, failed, tt.wantMsg, tt.wantFailed)
			}
		})
	}
	long := strings.Repeat("x", 2000)
	if got := Truncate(long); len(got) != terminationMessageMax+3 || !strings.HasPrefix(got, "...") {
		t.Fatalf("Truncate() length = %d", len(got))
	}
}

func testDescriptorInput(t *testing.T) DescriptorInput {
	t.Helper()
	bundleData, err := evidence.Pack(map[string][]byte{
		evidence.SBOMFile:      []byte(`{"bomFormat":"CycloneDX"}`),
		evidence.TrivyFile:     []byte(`{"SchemaVersion":2}`),
		evidence.CollectorFile: []byte(`{"trivy":"0.70.0"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := evidence.ValidateBundle(bundleData)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-2604-base-azure", Namespace: "tenant", UID: types.UID("uid-1"), Generation: 3},
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{Family: "linux", Distribution: "ubuntu", Version: "26.04", Arch: "amd64"},
			Source: v1alpha1.SourceSpec{Type: "marketplace", MarketplaceRef: &v1alpha1.MarketplaceRef{
				Publisher: "Canonical", Offer: "ubuntu-26_04-lts", SKU: "server", Version: "26.04.202611010",
			}},
			Provisioners: []v1alpha1.ProvisionerSpec{
				{Type: "shell", Source: &v1alpha1.ProvisionerSourceSpec{Git: &v1alpha1.GitProvisionerSourceSpec{URL: "https://git.example.com/images.git", Ref: commit, Path: "scripts/base"}}},
				{Type: "evidence", Source: &v1alpha1.ProvisionerSourceSpec{Git: &v1alpha1.GitProvisionerSourceSpec{URL: "https://git.example.com/images.git", Ref: commit, Path: "scripts/evidence.sh"}}},
				{Type: "shell", Inline: "systemd-run --on-active=30 waagent -deprovision+user -force"},
			},
			Targets:  []v1alpha1.TargetSpec{{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "azure-gallery"}, Format: "vhd"}},
			Evidence: &v1alpha1.EvidenceSpec{Publisher: "controller", SBOMFormat: "cyclonedx-json", Subject: &v1alpha1.EvidenceSubject{Name: "ubuntu-2604-base", Version: "1.2.0", Platform: "azure"}},
		},
	}
	return DescriptorInput{
		Image:    img,
		Provider: "azure",
		Images: []platform.RemoteImageRef{{
			ImageRef: platform.ImageRef{ID: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/galleries/g/images/i/versions/1.2.0", Location: "westeurope"},
			Format:   "vhd",
		}},
		Bundle:         bundle,
		BuildID:        "build-1",
		BuilderVersion: "v0.6.0",
		StartedOn:      time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		FinishedOn:     time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC),
	}
}

func TestRenderDescriptor(t *testing.T) {
	in := testDescriptorInput(t)
	raw, err := RenderDescriptor(in)
	if err != nil {
		t.Fatal(err)
	}
	var d Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Subject.Platform != "azure" || d.Provider != "azure" || d.ProviderConfig != "azure-gallery" || d.Images[0].Location != "westeurope" {
		t.Fatalf("descriptor basics wrong: %+v", d)
	}
	if d.Source.URN != "Canonical:ubuntu-26_04-lts:server:26.04.202611010" {
		t.Fatalf("source URN = %q", d.Source.URN)
	}
	if d.Provisioners[0].Git == nil || d.Provisioners[0].Git.Path != "scripts/base" || d.Provisioners[2].InlineSHA256 == "" || d.Provisioners[2].Git != nil {
		t.Fatalf("provisioners = %+v", d.Provisioners)
	}
	if d.VMImage.UID != "uid-1" || d.VMImage.Generation != 3 || d.Builder.Version != "v0.6.0" || d.BuildStartedOn != "2026-11-20T10:00:00Z" {
		t.Fatalf("vmImage/builder = %+v %+v", d.VMImage, d.Builder)
	}
	if d.Evidence.BundleDigest != in.Bundle.Digest() || !strings.HasPrefix(d.Evidence.SBOMDigest, "sha256:") || string(d.Evidence.Collector) == "" {
		t.Fatalf("evidence = %+v", d.Evidence)
	}
	if strings.Contains(string(raw), "waagent") {
		t.Fatal("descriptor must not contain inline provisioner content")
	}

	in.Image.Spec.Source = v1alpha1.SourceSpec{Type: "cloud-image", URL: "https://cloud-images.ubuntu.com/x.ova", Checksum: "sha256:" + testSHA}
	in.BuilderVersion = ""
	raw, err = RenderDescriptor(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sha256": "`+testSHA+`"`) || !strings.Contains(string(raw), `"version": "unknown"`) {
		t.Fatalf("cloud-image descriptor = %s", raw)
	}
}

func TestRenderDescriptorValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DescriptorInput)
	}{
		{name: "no image", mutate: func(in *DescriptorInput) { in.Image = nil }},
		{name: "no subject", mutate: func(in *DescriptorInput) { in.Image.Spec.Evidence.Subject = nil }},
		{name: "two targets", mutate: func(in *DescriptorInput) {
			in.Image.Spec.Targets = append(in.Image.Spec.Targets, in.Image.Spec.Targets[0])
		}},
		{name: "no bundle", mutate: func(in *DescriptorInput) { in.Bundle = nil }},
		{name: "no images", mutate: func(in *DescriptorInput) { in.Images = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := testDescriptorInput(t)
			tt.mutate(&in)
			if _, err := RenderDescriptor(in); err == nil {
				t.Fatal("RenderDescriptor accepted invalid input")
			}
			if _, err := RenderProvenance(in, nil); err == nil {
				t.Fatal("RenderProvenance accepted invalid input")
			}
		})
	}
}

func TestRenderProvenance(t *testing.T) {
	in := testDescriptorInput(t)
	descriptor, err := RenderDescriptor(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RenderProvenance(in, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		BuildDefinition struct {
			BuildType            string `json:"buildType"`
			ResolvedDependencies []struct {
				URI    string            `json:"uri"`
				Digest map[string]string `json:"digest"`
				Name   string            `json:"name"`
			} `json:"resolvedDependencies"`
		} `json:"buildDefinition"`
		RunDetails struct {
			Builder struct {
				ID string `json:"id"`
			} `json:"builder"`
			Byproducts []struct {
				Name   string            `json:"name"`
				Digest map[string]string `json:"digest"`
			} `json:"byproducts"`
		} `json:"runDetails"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	deps := p.BuildDefinition.ResolvedDependencies
	if p.BuildDefinition.BuildType != BuildType || p.RunDetails.Builder.ID != BuilderID {
		t.Fatalf("provenance header = %s", raw)
	}
	if len(deps) != 2 || deps[0].Name != "source" || deps[0].URI != "marketplace:Canonical:ubuntu-26_04-lts:server:26.04.202611010" || deps[1].Digest["gitCommit"] != strings.Repeat("c", 40) {
		t.Fatalf("resolvedDependencies = %+v", deps)
	}
	if len(p.RunDetails.Byproducts) != 2 || p.RunDetails.Byproducts[0].Digest["sha256"] != in.Bundle.SHA256 || p.RunDetails.Byproducts[1].Digest["sha256"] != evidence.SHA256Hex(descriptor) {
		t.Fatalf("byproducts = %+v", p.RunDetails.Byproducts)
	}

	for _, source := range []v1alpha1.SourceSpec{
		{Type: "cloud-image", URL: "https://x/y.ova", Checksum: "sha256:" + testSHA},
		{Type: "snapshot", ProviderRef: "/subscriptions/s/snapshots/x"},
	} {
		in.Image.Spec.Source = source
		for i := range in.Image.Spec.Provisioners[:2] {
			in.Image.Spec.Provisioners[i].Source.Git.Ref = "main"
		}
		raw, err := RenderProvenance(in, descriptor)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"gitCommit"`) {
			t.Fatalf("branch ref must not be recorded as gitCommit: %s", raw)
		}
	}
}

func resourceMust(value string) resource.Quantity { return resource.MustParse(value) }
