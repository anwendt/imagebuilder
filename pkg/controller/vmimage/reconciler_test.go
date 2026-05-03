// pkg/controller/vmimage/reconciler_test.go
//
// Unit tests for the VMImage reconciler using the controller-runtime fake client.
//
// TDD: each test describes one observable behaviour of the reconciler. The
// fake client provides a full in-memory Kubernetes API surface with no cluster
// required — suitable for the unit-test level (DR-001–DR-005).
//
// Covered behaviours:
//   - Finalizer added on first reconcile
//   - Pending → Building: Job created, status updated
//   - Building → Uploading: Job success detected
//   - Building → Failed: Job failure detected
//   - Building → Failed: timeout exceeded
//   - Uploading → Ready: Job success detected
//   - Deletion: finalizer removed
//   - Unknown phase returns error

package vmimage_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/plugin"
)

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add v1alpha1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatalf("add batchv1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("add coordinationv1 scheme: %v", err)
	}
	return s
}

func stepStatus(img *v1alpha1.VMImage, name string) (v1alpha1.PipelineStepStatus, bool) {
	for _, step := range img.Status.Steps {
		if step.Name == name {
			return step, true
		}
	}
	return v1alpha1.PipelineStepStatus{}, false
}

func requireEvent(t *testing.T, recorder *record.FakeRecorder, reason string) {
	t.Helper()
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, reason) {
			t.Fatalf("event = %q, want reason %q", event, reason)
		}
	default:
		t.Fatalf("expected event reason %q", reason)
	}
}

func hasFinalizer(img *v1alpha1.VMImage, finalizer string) bool {
	for _, item := range img.Finalizers {
		if item == finalizer {
			return true
		}
	}
	return false
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, item := range env {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

// newReconciler creates a VMImageReconciler backed by the fake client.
func newReconciler(t *testing.T, objs ...runtime.Object) (*vmimage.VMImageReconciler, *fake.ClientBuilder) {
	t.Helper()
	s := testScheme(t)
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{})
	for _, o := range objs {
		cb = cb.WithRuntimeObjects(o)
	}
	r := &vmimage.VMImageReconciler{
		Client:   cb.Build(),
		Scheme:   s,
		Registry: plugin.Default(),
	}
	return r, cb
}

func newImg(name, namespace, phase string) *v1alpha1.VMImage {
	img := &v1alpha1.VMImage{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "imagebuilder.io/v1alpha1",
			Kind:       "VMImage",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "test-uid",
		},
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{
				Family:       "linux",
				Distribution: "ubuntu",
				Version:      "24.04",
				Arch:         "amd64",
			},
			Source: v1alpha1.SourceSpec{
				Type: "cloud-image",
				URL:  "https://example.com/ubuntu.img",
			},
			Targets: []v1alpha1.TargetSpec{},
		},
		Status: v1alpha1.VMImageStatus{
			Phase: phase,
		},
	}
	return img
}

func reconcileOnce(t *testing.T, r *vmimage.VMImageReconciler, name, namespace string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	return result
}

func buildResultMessage(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
		Metadata:  map[string]string{"backend": "qemu-img"},
	})
	if err != nil {
		t.Fatalf("marshal build result: %v", err)
	}
	return string(data)
}

func buildResultPod(name, namespace, jobName, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"batch.kubernetes.io/job-name": jobName,
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "build",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Message: message,
						},
					},
				},
			},
		},
	}
}

func uploadResultMessage(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal([]v1alpha1.ImageStatus{
		{
			Provider:       "aws",
			ProviderConfig: "aws-cfg",
			ImageRef:       "ami-123",
			Location:       "eu-west-1",
			Format:         "vmdk",
			Checksum:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	})
	if err != nil {
		t.Fatalf("marshal upload result: %v", err)
	}
	return string(data)
}

func uploadResultWithOperationsMessage(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(struct {
		Images     []v1alpha1.ImageStatus           `json:"images"`
		Operations []v1alpha1.UploadOperationStatus `json:"operations"`
	}{
		Images: []v1alpha1.ImageStatus{
			{
				Provider:       "aws",
				ProviderConfig: "aws-cfg",
				ImageRef:       "ami-123",
				Location:       "eu-west-1",
				Format:         "vmdk",
				Checksum:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
		Operations: []v1alpha1.UploadOperationStatus{
			{
				Provider:             "aws",
				ProviderConfig:       "aws-cfg",
				Format:               "vmdk",
				Phase:                "Succeeded",
				OperationRef:         "s3://imagebuilder/build-123/disk.vmdk",
				ImageRef:             "ami-123",
				UploadMilliseconds:   1250,
				UploadBytes:          42,
				RegisterMilliseconds: 2500,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal upload result: %v", err)
	}
	return string(data)
}

func uploadResultPod(name, namespace, jobName, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"batch.kubernetes.io/job-name": jobName,
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "upload",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Message: message,
						},
					},
				},
			},
		},
	}
}

func failedUploadPod(name, namespace, jobName, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"batch.kubernetes.io/job-name": jobName},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "upload",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Message: message,
						},
					},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Finalizer
// ---------------------------------------------------------------------------

func TestReconcile_AddsFinalizer_OnFirstReconcile(t *testing.T) {
	img := newImg("test-img", "default", "")
	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-img", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Requeue {
		t.Error("expected Requeue=true after adding finalizer")
	}

	updated := &v1alpha1.VMImage{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-img", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated img: %v", err)
	}
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == "imagebuilder.io/cleanup" {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		t.Error("finalizer imagebuilder.io/cleanup not found after first reconcile")
	}
}

// ---------------------------------------------------------------------------
// Pending → Building
// ---------------------------------------------------------------------------

func TestReconcile_Pending_CreatesJob(t *testing.T) {
	img := newImg("build-me", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "build-me", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Job should exist.
	job := &batchv1.Job{}
	jobKey := types.NamespacedName{Name: "build-me-build", Namespace: "default"}
	if err := c.Get(context.Background(), jobKey, job); err != nil {
		t.Fatalf("expected Job build-me-build to be created: %v", err)
	}
}

func TestReconcile_Pending_SetsPhaseToBuilding(t *testing.T) {
	img := newImg("build-me", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	r.Reconcile(context.Background(), ctrl.Request{ //nolint:errcheck
		NamespacedName: types.NamespacedName{Name: "build-me", Namespace: "default"},
	})

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "build-me", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseBuilding {
		t.Errorf("status.phase = %q after pending reconcile, want Building", updated.Status.Phase)
	}
	if step, ok := stepStatus(updated, "Build"); !ok || step.Status != "Running" {
		t.Fatalf("Build step = %#v, ok=%v, want Running", step, ok)
	}
	if step, ok := stepStatus(updated, "Upload"); !ok || step.Status != "Pending" {
		t.Fatalf("Upload step = %#v, ok=%v, want Pending", step, ok)
	}
}

func TestReconcile_Pending_RecordsBuildStartedEvent(t *testing.T) {
	img := newImg("build-event", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: recorder}

	r.Reconcile(context.Background(), ctrl.Request{ //nolint:errcheck
		NamespacedName: types.NamespacedName{Name: "build-event", Namespace: "default"},
	})

	requireEvent(t, recorder, "BuildStarted")
}

func TestReconcile_Pending_QueuesWhenGlobalBuildLimitReached(t *testing.T) {
	img := newImg("queued-global", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	holder := "default/other/test-uid"
	duration := int32(3600)
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-build-global-0", Namespace: "default"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, lease).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{
		Client:                     c,
		Scheme:                     s,
		Registry:                   plugin.Default(),
		Recorder:                   recorder,
		MaxConcurrentBuilds:        1,
		MaxConcurrentBuildsPerNode: -1,
	}

	result := reconcileOnce(t, r, "queued-global", "default")
	if result.RequeueAfter == 0 {
		t.Fatal("queued build should requeue")
	}
	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "queued-global-build", Namespace: "default"}, job); err == nil {
		t.Fatal("build job should not be created while queued")
	}
	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "queued-global", Namespace: "default"}, updated) //nolint:errcheck
	if step, ok := stepStatus(updated, "Build"); !ok || step.Reason != "BuildQueued" {
		t.Fatalf("Build step = %#v, ok=%v, want BuildQueued", step, ok)
	}
	requireEvent(t, recorder, "BuildQueued")
}

func TestReconcile_Pending_QueuesWhenNodeBuildLimitReached(t *testing.T) {
	img := newImg("queued-node", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.NodeSelector = map[string]string{"kubernetes.io/hostname": "node-a"}
	holder := "default/other/test-uid"
	duration := int32(3600)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-a",
			Labels: map[string]string{"kubernetes.io/hostname": "node-a"},
		},
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-build-node-66570ff05a207404-0", Namespace: "default"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, node, lease).Build()
	r := &vmimage.VMImageReconciler{
		Client:                     c,
		Scheme:                     s,
		Registry:                   plugin.Default(),
		MaxConcurrentBuilds:        -1,
		MaxConcurrentBuildsPerNode: 1,
	}

	reconcileOnce(t, r, "queued-node", "default")

	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "queued-node-build", Namespace: "default"}, job); err == nil {
		t.Fatal("build job should not be created while node slot is occupied")
	}
	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "queued-node", Namespace: "default"}, updated) //nolint:errcheck
	if step, ok := stepStatus(updated, "Build"); !ok || step.Reason != "BuildQueued" {
		t.Fatalf("Build step = %#v, ok=%v, want BuildQueued", step, ok)
	}
}

func TestReconcile_Pending_SchedulesBuildOntoConcreteNode(t *testing.T) {
	img := newImg("scheduled-node", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.NodeSelector = map[string]string{"imagebuilder.io/build-node": "true"}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-a",
			Labels: map[string]string{
				"imagebuilder.io/build-node": "true",
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, node).Build()
	r := &vmimage.VMImageReconciler{
		Client:                     c,
		Scheme:                     s,
		Registry:                   plugin.Default(),
		MaxConcurrentBuilds:        -1,
		MaxConcurrentBuildsPerNode: 1,
	}

	reconcileOnce(t, r, "scheduled-node", "default")

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "scheduled-node", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.ScheduledNodeName != "node-a" {
		t.Fatalf("scheduledNodeName = %q, want node-a", updated.Status.ScheduledNodeName)
	}
	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "scheduled-node-build", Namespace: "default"}, job); err != nil {
		t.Fatalf("expected build job: %v", err)
	}
	if job.Spec.Template.Spec.NodeName != "node-a" {
		t.Fatalf("job nodeName = %q, want node-a", job.Spec.Template.Spec.NodeName)
	}
}

func TestReconcile_Pending_SetsStartTime(t *testing.T) {
	img := newImg("build-me", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	r.Reconcile(context.Background(), ctrl.Request{ //nolint:errcheck
		NamespacedName: types.NamespacedName{Name: "build-me", Namespace: "default"},
	})

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "build-me", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.StartTime == nil {
		t.Error("status.startTime should be set after transitioning to Building")
	}
}

// ---------------------------------------------------------------------------
// Building → Uploading (job succeeded)
// ---------------------------------------------------------------------------

func TestReconcile_Building_JobSucceeded_MovesToUploading(t *testing.T) {
	jobName := "upload-test-build"
	img := newImg("upload-test", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.BuildLeaseRefs = []string{"default/imagebuilder-build-global-0", "default/imagebuilder-build-node-6b86b273ff34fce1-0"}
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}}
	now := metav1.Now()
	img.Status.StartTime = &now
	holder := "default/upload-test/test-uid"
	globalLease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-build-global-0", Namespace: "default"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}
	nodeLease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-build-node-6b86b273ff34fce1-0", Namespace: "default"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}

	pod := buildResultPod("upload-test-pod", "default", jobName, buildResultMessage(t))

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod, globalLease, nodeLease).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upload-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Requeue {
		t.Error("expected Requeue=true to immediately enter Uploading phase")
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "upload-test", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseUploading {
		t.Errorf("phase = %q, want Uploading", updated.Status.Phase)
	}
	if updated.Status.BuildArtifact == nil {
		t.Fatal("buildArtifact should be set after successful build")
	}
	if updated.Status.BuildArtifact.Format != "vmdk" {
		t.Errorf("artifact format = %q, want vmdk", updated.Status.BuildArtifact.Format)
	}
	if len(updated.Status.BuildLeaseRefs) != 0 {
		t.Fatalf("buildLeaseRefs should be cleared after build success, got %#v", updated.Status.BuildLeaseRefs)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "imagebuilder-build-global-0", Namespace: "default"}, &coordinationv1.Lease{}); err == nil {
		t.Fatal("global build lease should be released after build success")
	}
	if updated.Status.ProvisionerResultRef != "/workspace/provisioners-result.json" {
		t.Fatalf("provisionerResultRef = %q", updated.Status.ProvisionerResultRef)
	}
	if step, ok := stepStatus(updated, "Build"); !ok || step.Status != "Succeeded" {
		t.Fatalf("Build step = %#v, ok=%v, want Succeeded", step, ok)
	}
	if step, ok := stepStatus(updated, "Sanitization"); !ok || step.Status != "Skipped" {
		t.Fatalf("Sanitization step = %#v, ok=%v, want Skipped", step, ok)
	}
	if step, ok := stepStatus(updated, "Upload"); !ok || step.Status != "Running" {
		t.Fatalf("Upload step = %#v, ok=%v, want Running", step, ok)
	}
}

func TestReconcile_Building_RenewsBuildLeasesWhileJobRuns(t *testing.T) {
	jobName := "renew-test-build"
	img := newImg("renew-test", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.BuildLeaseRefs = []string{"default/imagebuilder-build-global-0"}
	now := metav1.Now()
	img.Status.StartTime = &now

	holder := "default/renew-test/test-uid"
	duration := int32(60)
	oldRenew := metav1.NewMicroTime(time.Now().Add(-30 * time.Second))
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-build-global-0", Namespace: "default"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			RenewTime:            &oldRenew,
		},
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"}}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, lease).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result := reconcileOnce(t, r, "renew-test", "default")
	if result.RequeueAfter == 0 {
		t.Fatalf("expected running build to requeue, got %+v", result)
	}

	updatedLease := &coordinationv1.Lease{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "imagebuilder-build-global-0", Namespace: "default"}, updatedLease); err != nil {
		t.Fatalf("get renewed lease: %v", err)
	}
	if updatedLease.Spec.RenewTime == nil || !updatedLease.Spec.RenewTime.After(oldRenew.Time) {
		t.Fatalf("renewTime = %#v, want after %s", updatedLease.Spec.RenewTime, oldRenew.Time)
	}
	if updatedLease.Spec.HolderIdentity == nil || *updatedLease.Spec.HolderIdentity != holder {
		t.Fatalf("holder = %#v, want %q", updatedLease.Spec.HolderIdentity, holder)
	}
}

func TestReconcile_Building_RecreatesMissingBuildLeaseWhileJobRuns(t *testing.T) {
	jobName := "recover-lease-build"
	img := newImg("recover-lease", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.BuildLeaseRefs = []string{"default/imagebuilder-build-global-0"}
	now := metav1.Now()
	img.Status.StartTime = &now
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"}}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result := reconcileOnce(t, r, "recover-lease", "default")
	if result.RequeueAfter == 0 {
		t.Fatalf("expected running build to requeue, got %+v", result)
	}

	lease := &coordinationv1.Lease{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "imagebuilder-build-global-0", Namespace: "default"}, lease); err != nil {
		t.Fatalf("expected missing lease to be recreated: %v", err)
	}
	holder := "default/recover-lease/test-uid"
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holder {
		t.Fatalf("holder = %#v, want %q", lease.Spec.HolderIdentity, holder)
	}
	if lease.Spec.RenewTime == nil {
		t.Fatal("renewTime should be set on recreated lease")
	}
}

// ---------------------------------------------------------------------------
// Building → Failed (job failed)
// ---------------------------------------------------------------------------

func TestReconcile_Building_JobFailed_MovesToFailed(t *testing.T) {
	jobName := "fail-test-build"
	img := newImg("fail-test", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	now := metav1.Now()
	img.Status.StartTime = &now

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "fail-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "fail-test", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed", updated.Status.Phase)
	}
}

func TestReconcile_Building_JobFailed_UsesBuilderFailureClassification(t *testing.T) {
	jobName := "classified-fail-build"
	img := newImg("classified-fail", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	now := metav1.Now()
	img.Status.StartTime = &now

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}
	pod := buildResultPod("classified-fail-pod", "default", jobName, `{"reason":"GuestReadinessTimeout","error":"guest readiness: timeout waiting for 127.0.0.1:2222"}`)
	recorder := record.NewFakeRecorder(10)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: recorder}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "classified-fail", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "classified-fail", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if step, ok := stepStatus(updated, "Readiness"); !ok || step.Status != "Failed" || step.Reason != "GuestReadinessTimeout" {
		t.Fatalf("Readiness step = %#v, ok=%v", step, ok)
	}
	foundCondition := false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Failed" && cond.Reason == "GuestReadinessTimeout" && strings.Contains(cond.Message, "timeout") {
			foundCondition = true
		}
	}
	if !foundCondition {
		t.Fatalf("Failed condition did not include classified reason: %#v", updated.Status.Conditions)
	}
	requireEvent(t, recorder, "GuestReadinessTimeout")
}

// ---------------------------------------------------------------------------
// Building → Failed (timeout)
// ---------------------------------------------------------------------------

func TestReconcile_Building_Timeout_MovesToFailed(t *testing.T) {
	jobName := "timeout-test-build"
	past := metav1.NewTime(time.Now().Add(-3 * time.Hour)) // 3h ago, exceeds 2h default
	img := newImg("timeout-test", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.StartTime = &past

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status:     batchv1.JobStatus{Active: 1}, // still running
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "timeout-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "timeout-test", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q after timeout, want Failed", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Building → Provisioning (init containers active)
// ---------------------------------------------------------------------------

func TestReconcile_Building_InitContainersActive_MovesToProvisioning(t *testing.T) {
	jobName := "provisioning-test-build"
	now := metav1.Now()
	img := newImg("provisioning-test", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.StartTime = &now

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status:     batchv1.JobStatus{Active: 1}, // Active pods = init containers running
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	r.Reconcile(context.Background(), ctrl.Request{ //nolint:errcheck
		NamespacedName: types.NamespacedName{Name: "provisioning-test", Namespace: "default"},
	})

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "provisioning-test", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning when init containers are active", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Uploading → Ready
// ---------------------------------------------------------------------------

func TestReconcile_Uploading_JobSucceeded_MovesToReady(t *testing.T) {
	jobName := "ready-test-upload"
	img := newImg("ready-test", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.UploadJobRef = &jobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
	pod := uploadResultPod("ready-test-upload-pod", "default", jobName, uploadResultMessage(t))

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ready-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected empty result for terminal Ready state, got %+v", result)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "ready-test", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseReady {
		t.Errorf("phase = %q, want Ready", updated.Status.Phase)
	}
	if updated.Status.CompletionTime == nil {
		t.Error("completionTime should be set when image is Ready")
	}
	if len(updated.Status.Images) != 1 {
		t.Fatalf("status.images length = %d, want 1", len(updated.Status.Images))
	}
	if len(updated.Status.UploadOperations) != 1 {
		t.Fatalf("status.uploadOperations length = %d, want 1", len(updated.Status.UploadOperations))
	}
	op := updated.Status.UploadOperations[0]
	if op.Provider != "aws" || op.ProviderConfig != "aws-cfg" || op.Format != "vmdk" || op.Phase != "Succeeded" {
		t.Fatalf("upload operation = %#v, want aws/aws-cfg/vmdk Succeeded", op)
	}
	if op.ImageRef != "ami-123" {
		t.Fatalf("upload operation imageRef = %q, want ami-123", op.ImageRef)
	}
}

func TestReconcile_Uploading_JobSucceeded_StoresReportedUploadOperationRefs(t *testing.T) {
	jobName := "ready-ops-upload"
	img := newImg("ready-ops", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.UploadJobRef = &jobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Status.UploadOperations = []v1alpha1.UploadOperationStatus{
		{Provider: "aws", ProviderConfig: "aws-cfg", Format: "vmdk", Phase: "Uploading"},
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
	pod := uploadResultPod("ready-ops-upload-pod", "default", jobName, uploadResultWithOperationsMessage(t))

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ready-ops", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected empty result for terminal Ready state, got %+v", result)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "ready-ops", Namespace: "default"}, updated) //nolint:errcheck
	if len(updated.Status.UploadOperations) != 1 {
		t.Fatalf("status.uploadOperations length = %d, want 1", len(updated.Status.UploadOperations))
	}
	op := updated.Status.UploadOperations[0]
	if op.OperationRef != "s3://imagebuilder/build-123/disk.vmdk" {
		t.Fatalf("operationRef = %q, want provider operation ref", op.OperationRef)
	}
	if op.ImageRef != "ami-123" {
		t.Fatalf("imageRef = %q, want ami-123", op.ImageRef)
	}
	if op.UploadMilliseconds != 1250 || op.RegisterMilliseconds != 2500 {
		t.Fatalf("durations = %d/%d, want 1250/2500", op.UploadMilliseconds, op.RegisterMilliseconds)
	}
	if op.UploadBytes != 42 {
		t.Fatalf("uploadBytes = %d, want 42", op.UploadBytes)
	}
}

// ---------------------------------------------------------------------------
// Terminal states — no requeue
// ---------------------------------------------------------------------------

func TestReconcile_Ready_IsTerminal_NoRequeue(t *testing.T) {
	img := newImg("done", "default", v1alpha1.PhaseReady)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "done", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("Ready is terminal: expected empty result, got %+v", result)
	}
}

func TestReconcile_Failed_IsTerminal_NoRequeue(t *testing.T) {
	img := newImg("failed", "default", v1alpha1.PhaseFailed)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "failed", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("Failed is terminal: expected empty result, got %+v", result)
	}
}

// ---------------------------------------------------------------------------
// Unknown phase
// ---------------------------------------------------------------------------

func TestReconcile_UnknownPhase_ReturnsError(t *testing.T) {
	img := newImg("unknown", "default", "Bogus")
	img.Finalizers = []string{"imagebuilder.io/cleanup"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "unknown", Namespace: "default"},
	})
	if err == nil {
		t.Error("expected error for unknown phase, got nil")
	}
}

// ---------------------------------------------------------------------------
// Deletion
// ---------------------------------------------------------------------------

func TestReconcile_Deletion_RemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	img := newImg("delete-me", "default", v1alpha1.PhaseReady)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "delete-me", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "delete-me", Namespace: "default"}, updated) //nolint:errcheck
	for _, f := range updated.Finalizers {
		if f == "imagebuilder.io/cleanup" {
			t.Error("finalizer should have been removed during deletion")
		}
	}
}

func TestReconcile_DeletionDuringUpload_StopsUploadBeforeCleanup(t *testing.T) {
	now := metav1.Now()
	uploadJobName := "delete-upload-upload"
	img := newImg("delete-upload", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.UploadJobRef = &uploadJobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
	}

	providerConfig := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider: "aws",
			Credentials: v1alpha1.CredentialsSpec{
				SecretRef: v1alpha1.SecretRef{Name: "aws-secret", Key: "credentials"},
			},
		},
	}
	uploadJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: uploadJobName, Namespace: "default"}}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, providerConfig, uploadJob).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: recorder}

	result := reconcileOnce(t, r, "delete-upload", "default")
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while upload job is stopping, got %+v", result)
	}

	deletedUploadJob := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: uploadJobName, Namespace: "default"}, deletedUploadJob); err == nil {
		t.Fatalf("upload job still exists after deletion request")
	}
	cleanupJob := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "delete-upload-upload-cleanup", Namespace: "default"}, cleanupJob); err == nil {
		t.Fatalf("cleanup job should not be created until active upload job is gone")
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "delete-upload", Namespace: "default"}, updated) //nolint:errcheck
	if !hasFinalizer(updated, "imagebuilder.io/cleanup") {
		t.Fatal("finalizer should remain while upload cleanup is pending")
	}
	requireEvent(t, recorder, "UploadCleanupPending")
}

func TestReconcile_DeletionDuringUpload_CreatesCleanupJobAfterUploadStopped(t *testing.T) {
	now := metav1.Now()
	uploadJobName := "cleanup-create-upload"
	img := newImg("cleanup-create", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.UploadJobRef = &uploadJobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
	}

	providerConfig := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider: "aws",
			Credentials: v1alpha1.CredentialsSpec{
				SecretRef: v1alpha1.SecretRef{Name: "aws-secret", Key: "credentials"},
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, providerConfig).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: recorder}

	result := reconcileOnce(t, r, "cleanup-create", "default")
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while cleanup job is running, got %+v", result)
	}

	cleanupJob := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cleanup-create-upload-cleanup", Namespace: "default"}, cleanupJob); err != nil {
		t.Fatalf("expected cleanup job to be created: %v", err)
	}
	env := cleanupJob.Spec.Template.Spec.Containers[0].Env
	if got := envValue(env, "UPLOAD_CLEANUP_ONLY"); got != "true" {
		t.Fatalf("UPLOAD_CLEANUP_ONLY = %q, want true", got)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "cleanup-create", Namespace: "default"}, updated) //nolint:errcheck
	if !hasFinalizer(updated, "imagebuilder.io/cleanup") {
		t.Fatal("finalizer should remain until cleanup job completes")
	}
	requireEvent(t, recorder, "UploadCleanupStarted")
}

func TestReconcile_DeletionDuringUpload_RemovesFinalizerAfterCleanupJobSucceeded(t *testing.T) {
	now := metav1.Now()
	uploadJobName := "cleanup-done-upload"
	img := newImg("cleanup-done", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.UploadJobRef = &uploadJobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
	}

	providerConfig := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider: "aws",
			Credentials: v1alpha1.CredentialsSpec{
				SecretRef: v1alpha1.SecretRef{Name: "aws-secret", Key: "credentials"},
			},
		},
	}
	cleanupJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "cleanup-done-upload-cleanup", Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, providerConfig, cleanupJob).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: recorder}

	result := reconcileOnce(t, r, "cleanup-done", "default")
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("expected deletion cleanup to finish without requeue, got %+v", result)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "cleanup-done", Namespace: "default"}, updated) //nolint:errcheck
	if hasFinalizer(updated, "imagebuilder.io/cleanup") {
		t.Fatal("finalizer should be removed after upload cleanup job succeeds")
	}
	requireEvent(t, recorder, "UploadCleanupComplete")
}

func TestReconcile_DeletionDuringUpload_CleanupJobFailureUpdatesStatus(t *testing.T) {
	now := metav1.Now()
	uploadJobName := "cleanup-failed-upload"
	img := newImg("cleanup-failed", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.UploadJobRef = &uploadJobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
	}

	providerConfig := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider: "aws",
			Credentials: v1alpha1.CredentialsSpec{
				SecretRef: v1alpha1.SecretRef{Name: "aws-secret", Key: "credentials"},
			},
		},
	}
	cleanupJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "cleanup-failed-upload-cleanup", Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, providerConfig, cleanupJob).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: recorder}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cleanup-failed", Namespace: "default"}})
	if err == nil {
		t.Fatal("Reconcile should return cleanup job failure")
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "cleanup-failed", Namespace: "default"}, updated) //nolint:errcheck
	if step, ok := stepStatus(updated, "Cleanup"); !ok || step.Reason != "UploadCleanupJobFailed" {
		t.Fatalf("Cleanup step = %#v, want UploadCleanupJobFailed", step)
	}
	if got := conditionReason(updated, "CleanupFailed"); got != "UploadCleanupJobFailed" {
		t.Fatalf("CleanupFailed reason = %q, want UploadCleanupJobFailed", got)
	}
	requireEvent(t, recorder, "UploadCleanupJobFailed")
}

// ---------------------------------------------------------------------------
// Not found — no error
// ---------------------------------------------------------------------------

func TestReconcile_NotFound_ReturnsNil(t *testing.T) {
	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ghost", Namespace: "default"},
	})
	if err != nil {
		t.Errorf("expected nil error for not-found resource, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found resource")
	}
}
