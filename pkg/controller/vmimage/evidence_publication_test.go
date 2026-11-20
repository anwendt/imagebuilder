package vmimage_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/evidencejob"
	"github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

const evidenceNS = "imagebuilder-system"

func controllerEvidenceImage(name string) (*v1alpha1.VMImage, *v1alpha1.ProviderConfig) {
	img, cfg := remoteTestImage(name)
	img.Spec.Evidence = &v1alpha1.EvidenceSpec{
		Required:             true,
		Publisher:            v1alpha1.EvidencePublisherController,
		SBOMFormat:           "cyclonedx-json",
		VulnerabilityScanner: "trivy",
		FailOnSeverity:       []string{"CRITICAL"},
		RegistryRepository:   "oci://registry.example.com/platform/vmimages",
		Subject:              &v1alpha1.EvidenceSubject{Name: "ubuntu-2604-base", Version: "1.2.0", Platform: "aws"},
	}
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "evidence", Inline: "collect"}}
	return img, cfg
}

func evidenceConfig() evidencejob.Config {
	return evidencejob.Config{Enabled: true, Namespace: evidenceNS, RegistrySecret: "push", CosignSecret: "cosign", BuilderVersion: "v0.6.0"}
}

func testBundle(t *testing.T, trivy string) *platform.RemoteEvidenceBundle {
	t.Helper()
	data, err := evidence.Pack(map[string][]byte{
		evidence.SBOMFile:  []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`),
		evidence.TrivyFile: []byte(trivy),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &platform.RemoteEvidenceBundle{Content: data, SHA256: evidence.SHA256Hex(data), MediaType: evidence.MediaType}
}

func doneResult(bundle *platform.RemoteEvidenceBundle) *platform.RemoteBuildResult {
	return &platform.RemoteBuildResult{
		OperationRef: "op-1",
		Phase:        platform.RemoteBuildPhaseReady,
		Done:         true,
		Images: []platform.RemoteImageRef{{
			Provider: "aws",
			ImageRef: platform.ImageRef{ID: "ami-123", Location: "eu-west-1"},
			Format:   platform.FormatAMI,
		}},
		Hygiene:        &platform.RemoteHygieneResult{Status: "passed", Checks: []string{"verify-boot-passed"}},
		EvidenceBundle: bundle,
	}
}

func evidenceReconciler(t *testing.T, img *v1alpha1.VMImage, cfg *v1alpha1.ProviderConfig, plugin *fakeRemoteBuildPlugin) (*vmimage.VMImageReconciler, client.Client) {
	t.Helper()
	r, c := remoteTestReconciler(t, img, cfg, plugin)
	r.Evidence = evidenceConfig()
	return r, c
}

func getImage(t *testing.T, c client.Client, img *v1alpha1.VMImage) *v1alpha1.VMImage {
	t.Helper()
	updated := &v1alpha1.VMImage{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func publicationResult() evidencejob.Result {
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	return evidencejob.Result{DescriptorDigest: d("1"), IndexDigest: d("2"), SignatureDigest: d("3"), ProvenanceDigest: d("4"), SBOMDigest: d("5"), VulnerabilityReportDigest: d("6"), IndexSignatureDigest: d("7")}
}

func finishJob(t *testing.T, c client.Client, name string, condition batchv1.JobConditionType, pod *corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, job); err != nil {
		t.Fatalf("publication job not created: %v", err)
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: condition, Status: corev1.ConditionTrue})
	if err := c.Status().Update(ctx, job); err != nil {
		if err := c.Update(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if pod != nil {
		pod.Name = name + "-pod"
		pod.Namespace = evidenceNS
		pod.Labels = map[string]string{"job-name": name}
		if err := c.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
}

func reportPod(message string) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "report",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}},
	}}}}
}

func TestEvidencePublication_SucceedsAndSetsReferences(t *testing.T) {
	img, cfg := controllerEvidenceImage("evidence-ok")
	img.Annotations = map[string]string{v1alpha1.ImageVersionAnnotation: "9.9.9"}
	plugin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}, result: doneResult(testBundle(t, `{"SchemaVersion":2}`))}
	r, c := evidenceReconciler(t, img, cfg, plugin)
	ctx := context.Background()

	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)
	updated := getImage(t, c, img)
	if updated.Status.Phase != v1alpha1.PhaseUploading || updated.Status.Evidence == nil || updated.Status.Evidence.PublishJobRef == "" {
		t.Fatalf("after Done: phase=%q evidence=%+v", updated.Status.Phase, updated.Status.Evidence)
	}
	if plugin.remoteCalls != 2 || !plugin.lastRequest.EvidenceBundleAcknowledged || plugin.lastRequest.ImageVersion != "1.2.0" {
		t.Fatalf("provider not acknowledged: calls=%d last=%+v", plugin.remoteCalls, plugin.lastRequest)
	}
	name := updated.Status.Evidence.PublishJobRef
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, secret); err != nil {
		t.Fatalf("bundle secret: %v", err)
	}
	var descriptor evidencejob.Descriptor
	if err := json.Unmarshal(secret.Data[evidencejob.DescriptorKey], &descriptor); err != nil || descriptor.Images[0].ImageRef != "ami-123" || descriptor.Builder.Version != "v0.6.0" {
		t.Fatalf("descriptor = %+v, %v", descriptor, err)
	}
	if step, _ := stepStatus(updated, "Evidence"); step.Status != "Running" {
		t.Fatalf("Evidence step = %+v", step)
	}

	reconcileOnce(t, r, img.Name, img.Namespace)
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, job); err != nil {
		t.Fatalf("job not created: %v", err)
	}
	if res := reconcileOnce(t, r, img.Name, img.Namespace); res.RequeueAfter == 0 {
		t.Fatal("running publication must requeue")
	}
	raw, _ := json.Marshal(publicationResult())
	finishJob(t, c, name, batchv1.JobComplete, reportPod(string(raw)))
	reconcileOnce(t, r, img.Name, img.Namespace)

	updated = getImage(t, c, img)
	ev := updated.Status.Evidence
	repo := "oci://registry.example.com/platform/vmimages/ubuntu-2604-base@"
	if updated.Status.Phase != v1alpha1.PhaseReady || ev.Status != "passed" || ev.DescriptorRef != repo+"sha256:"+strings.Repeat("1", 64) || ev.IndexRef != repo+"sha256:"+strings.Repeat("2", 64) || ev.SBOMRef != repo+"sha256:"+strings.Repeat("5", 64) {
		t.Fatalf("ready status = %q %+v", updated.Status.Phase, ev)
	}
	if len(updated.Status.Images) != 1 || updated.Status.HygieneResult == nil {
		t.Fatalf("images/hygiene not recorded: %+v", updated.Status)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("bundle secret not deleted: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("job not deleted: %v", err)
	}
}

func TestEvidencePublication_FailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		config     func(*evidencejob.Config)
		result     func(t *testing.T) *platform.RemoteBuildResult
		remoteErr  error
		wantReason string
		reconciles int
	}{
		{
			name:       "publisher not configured",
			config:     func(c *evidencejob.Config) { c.CosignSecret = "" },
			wantReason: "EvidencePublisherNotConfigured",
			reconciles: 1,
		},
		{
			name:       "bundle missing",
			result:     func(*testing.T) *platform.RemoteBuildResult { return doneResult(nil) },
			wantReason: "RemoteEvidenceMissing",
			reconciles: 2,
		},
		{
			name: "bundle digest mismatch",
			result: func(t *testing.T) *platform.RemoteBuildResult {
				bundle := testBundle(t, `{"SchemaVersion":2}`)
				bundle.SHA256 = strings.Repeat("0", 64)
				return doneResult(bundle)
			},
			wantReason: "RemoteEvidenceRejected",
			reconciles: 2,
		},
		{
			name: "critical vulnerability",
			result: func(t *testing.T) *platform.RemoteBuildResult {
				return doneResult(testBundle(t, `{"SchemaVersion":2,"Results":[{"Vulnerabilities":[{"Severity":"CRITICAL","FixedVersion":"1.0"},{"Severity":"CRITICAL"}]}]}`))
			},
			wantReason: "EvidenceVulnerabilityPolicyFailed",
			reconciles: 2,
		},
		{
			name:       "verify boot failed",
			remoteErr:  errors.New("rpc error: code = FailedPrecondition desc = VerifyBootFailed: marker missing"),
			wantReason: "VerifyBootFailed",
			reconciles: 2,
		},
		{
			name:       "bundle lost",
			remoteErr:  errors.New("EvidenceBundleLost: provider restarted"),
			wantReason: "RemoteEvidenceRejected",
			reconciles: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, cfg := controllerEvidenceImage("evidence-fail")
			plugin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}, remoteErr: tt.remoteErr}
			if tt.result != nil {
				plugin.result = tt.result(t)
			}
			r, c := evidenceReconciler(t, img, cfg, plugin)
			if tt.config != nil {
				tt.config(&r.Evidence)
			}
			for i := 0; i < tt.reconciles; i++ {
				reconcileOnce(t, r, img.Name, img.Namespace)
			}
			updated := getImage(t, c, img)
			if updated.Status.Phase != v1alpha1.PhaseFailed || conditionReason(updated, "Failed") != tt.wantReason {
				t.Fatalf("phase=%q reason=%q, want Failed/%s (conditions %+v)", updated.Status.Phase, conditionReason(updated, "Failed"), tt.wantReason, updated.Status.Conditions)
			}
			secrets := &corev1.SecretList{}
			_ = c.List(context.Background(), secrets, client.InNamespace(evidenceNS))
			if len(secrets.Items) != 0 {
				t.Fatalf("no bundle secret may remain: %d", len(secrets.Items))
			}
		})
	}
}

func startPublication(t *testing.T, name string) (*vmimage.VMImageReconciler, client.Client, *v1alpha1.VMImage, string) {
	t.Helper()
	img, cfg := controllerEvidenceImage(name)
	plugin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}, result: doneResult(testBundle(t, `{"SchemaVersion":2}`))}
	r, c := evidenceReconciler(t, img, cfg, plugin)
	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)
	updated := getImage(t, c, img)
	return r, c, img, updated.Status.Evidence.PublishJobRef
}

func TestEvidencePublication_JobFailures(t *testing.T) {
	tests := []struct {
		name        string
		condition   batchv1.JobConditionType
		pod         *corev1.Pod
		wantMessage string
	}{
		{
			name:      "step failed",
			condition: batchv1.JobFailed,
			pod: &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "push-descriptor",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "push failed: 401 unauthorized"}},
			}}}},
			wantMessage: "step push-descriptor failed: push failed: 401 unauthorized",
		},
		{name: "failed without pods", condition: batchv1.JobFailed, wantMessage: "evidence publication Job failed"},
		{name: "unparsable result", condition: batchv1.JobComplete, pod: reportPod("garbage"), wantMessage: "parse evidence publication result"},
		{name: "succeeded without report", condition: batchv1.JobComplete, pod: &corev1.Pod{}, wantMessage: "parse evidence publication result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, c, img, name := startPublication(t, "evidence-job")
			reconcileOnce(t, r, img.Name, img.Namespace)
			finishJob(t, c, name, tt.condition, tt.pod)
			reconcileOnce(t, r, img.Name, img.Namespace)
			updated := getImage(t, c, img)
			if updated.Status.Phase != v1alpha1.PhaseFailed || conditionReason(updated, "Failed") != "EvidencePublicationFailed" || !strings.Contains(updated.Status.Evidence.Message, tt.wantMessage) {
				t.Fatalf("phase=%q evidence=%+v, want message %q", updated.Status.Phase, updated.Status.Evidence, tt.wantMessage)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: evidenceNS, Name: name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Fatalf("bundle secret not deleted after failure: %v", err)
			}
		})
	}
}

func TestEvidencePublication_MissingSecretFailsClosed(t *testing.T) {
	r, c, img, name := startPublication(t, "evidence-nosecret")
	if err := c.Delete(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: evidenceNS, Name: name}}); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, img.Name, img.Namespace)
	updated := getImage(t, c, img)
	if updated.Status.Phase != v1alpha1.PhaseFailed || !strings.Contains(updated.Status.Evidence.Message, "is missing") {
		t.Fatalf("phase=%q evidence=%+v", updated.Status.Phase, updated.Status.Evidence)
	}
}

func TestEvidencePublication_TamperedSecretFailsClosed(t *testing.T) {
	r, c, img, name := startPublication(t, "evidence-tampered")
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: evidenceNS, Name: name}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[evidencejob.BundleKey] = []byte("other")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, img.Name, img.Namespace)
	if updated := getImage(t, c, img); updated.Status.Phase != v1alpha1.PhaseFailed || !strings.Contains(updated.Status.Evidence.Message, "does not match") {
		t.Fatalf("phase=%q evidence=%+v", updated.Status.Phase, updated.Status.Evidence)
	}
}

func TestEvidencePublication_DeleteRemovesObjects(t *testing.T) {
	r, c, img, name := startPublication(t, "evidence-delete")
	reconcileOnce(t, r, img.Name, img.Namespace)
	ctx := context.Background()
	current := getImage(t, c, img)
	if err := c.Delete(ctx, current); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, img.Name, img.Namespace)
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("job not deleted: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: evidenceNS, Name: name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("secret not deleted: %v", err)
	}
}

func TestEvidencePublication_MissingStateFails(t *testing.T) {
	img, cfg := controllerEvidenceImage("evidence-nostate")
	img.Status.Phase = v1alpha1.PhaseUploading
	plugin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}}
	r, c := evidenceReconciler(t, img, cfg, plugin)
	reconcileOnce(t, r, img.Name, img.Namespace)
	if updated := getImage(t, c, img); updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
}

type ackFailingPlugin struct {
	*fakeRemoteBuildPlugin
}

func (p ackFailingPlugin) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	if req.EvidenceBundleAcknowledged {
		p.remoteCalls++
		return nil, errors.New("provider restarted")
	}
	return p.fakeRemoteBuildPlugin.ReconcileRemoteBuild(ctx, req)
}

func TestEvidencePublication_ExistingSecretArtifactAndAckFailure(t *testing.T) {
	img, cfg := controllerEvidenceImage("evidence-variants")
	img.Spec.Provisioners = nil
	result := doneResult(testBundle(t, `{"SchemaVersion":2}`))
	result.Hygiene = &platform.RemoteHygieneResult{Status: "unknown"}
	result.Artifact = &platform.BuildArtifact{Path: "provider://x", Format: platform.FormatAMI, Checksum: "sha256:" + strings.Repeat("e", 64), OS: platform.OSFamilyLinux}
	inner := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}, result: result}
	registry := plugin.NewRegistry(slog.Default())
	if err := registry.Register(ackFailingPlugin{inner}); err != nil {
		t.Fatal(err)
	}
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, cfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: scheme, Registry: registry, Evidence: evidenceConfig()}
	owner := evidencejob.Owner{Namespace: img.Namespace, Name: img.Name, UID: string(img.UID), BuildID: string(img.UID)}
	stale := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: evidenceNS, Name: evidencejob.ObjectName(owner)}, Data: map[string][]byte{"stale": []byte("x")}}
	if err := c.Create(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)
	updated := getImage(t, c, img)
	if updated.Status.Phase != v1alpha1.PhaseUploading || updated.Status.BuildArtifact == nil {
		t.Fatalf("phase=%q artifact=%+v", updated.Status.Phase, updated.Status.BuildArtifact)
	}
	if step, _ := stepStatus(updated, "Sanitization"); step.Reason != "RemoteHygieneUnknown" {
		t.Fatalf("Sanitization step = %+v", step)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: evidenceNS, Name: updated.Status.Evidence.PublishJobRef}, secret); err != nil {
		t.Fatal(err)
	}
	if _, stale := secret.Data["stale"]; stale || len(secret.Data[evidencejob.BundleKey]) == 0 {
		t.Fatalf("existing secret not replaced: %v", secret.Data)
	}
}
