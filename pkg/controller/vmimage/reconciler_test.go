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
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	return s
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
	now := metav1.Now()
	img.Status.StartTime = &now

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
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
	jobName := "ready-test-build"
	img := newImg("ready-test", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
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
