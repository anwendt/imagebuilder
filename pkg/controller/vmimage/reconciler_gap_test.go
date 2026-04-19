// pkg/controller/vmimage/reconciler_gap_test.go
//
// Additional tests targeting coverage gaps in the VMImage reconciler:
//   - reconcilePending: provider validation paths (ProviderConfig missing, not supported)
//   - providerNameForTarget: happy path and error path
//   - setCondition: update-existing branch (when condition type already present)
//   - reconcileBuilding: missing BuildJobRef → Failed

package vmimage_test

import (
	"context"
	"log/slog"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ---------------------------------------------------------------------------
// reconcilePending: provider config not found → Failed
// ---------------------------------------------------------------------------

func TestReconcile_Pending_ProviderConfigNotFound_SetsFailed(t *testing.T) {
	img := newImg("prov-not-found", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "missing-config"},
			Format:            "vmdk",
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	// No ProviderConfig object created → Get will return NotFound
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "prov-not-found", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "prov-not-found", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed when ProviderConfig is missing", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// reconcilePending: provider not in registry → Failed
// ---------------------------------------------------------------------------

func TestReconcile_Pending_ProviderNotSupported_SetsFailed(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "unknown-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "nonexistent-provider-xyz"},
	}
	img := newImg("prov-unsupported", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "unknown-cfg"},
			Format:            "vmdk",
		},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	// Default registry has no "nonexistent-provider-xyz" registered
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.NewRegistry(slog.Default())}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "prov-unsupported", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "prov-unsupported", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed when provider not in registry", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// reconcilePending: valid ProviderConfig + registered provider → Building
// ---------------------------------------------------------------------------

func TestReconcile_Pending_ValidProvider_MovesToBuilding(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("prov-valid", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"},
			Format:            "vmdk",
		},
	}

	// Use a fresh registry with "aws" pre-registered
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(&fakeRegistryPlugin{name: "aws"})

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "prov-valid", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "prov-valid", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseBuilding {
		t.Errorf("phase = %q, want Building when provider is valid and registered", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// setCondition: update-existing branch
// (trigger two reconcile cycles so the same condition is set twice)
// ---------------------------------------------------------------------------

func TestReconcile_SetCondition_UpdatesExistingCondition(t *testing.T) {
	jobName := "cond-test-build"
	now := metav1.Now()

	// Start in Building with a pre-existing "BuildStarted" condition
	img := newImg("cond-test", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.StartTime = &now
	img.Status.Conditions = []metav1.Condition{
		{
			Type:               "BuildComplete",
			Status:             metav1.ConditionFalse,
			Reason:             "Pending",
			Message:            "initial",
			LastTransitionTime: now,
		},
	}

	// Job is succeeded → will call setCondition("BuildComplete", True, ...) which UPDATES
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

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cond-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "cond-test", Namespace: "default"}, updated) //nolint:errcheck

	// Condition should have been updated (not duplicated)
	buildCompleteCount := 0
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "BuildComplete" {
			buildCompleteCount++
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("BuildComplete condition status = %q, want True after update", cond.Status)
			}
		}
	}
	if buildCompleteCount != 1 {
		t.Errorf("expected exactly 1 BuildComplete condition, got %d", buildCompleteCount)
	}
}

// ---------------------------------------------------------------------------
// reconcileBuilding: missing BuildJobRef → Failed
// ---------------------------------------------------------------------------

func TestReconcile_Building_NilBuildJobRef_SetsFailed(t *testing.T) {
	img := newImg("no-ref", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	// BuildJobRef intentionally nil

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-ref", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "no-ref", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed when BuildJobRef is nil", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// reconcileBuilding: job disappeared (IsNotFound) → Failed
// ---------------------------------------------------------------------------

func TestReconcile_Building_JobDisappeared_SetsFailed(t *testing.T) {
	jobName := "vanished-build"
	now := metav1.Now()
	img := newImg("vanished", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.StartTime = &now
	// No Job object in fake client → Get returns NotFound

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "vanished", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "vanished", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed when build job disappeared", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Fake plugin for registry (minimal — only Name() matters)
// ---------------------------------------------------------------------------

type fakeRegistryPlugin struct {
	name string
}

func (p *fakeRegistryPlugin) Name() string                                                    { return p.name }
func (p *fakeRegistryPlugin) Version() string                                                 { return "v0.0.1" }
func (p *fakeRegistryPlugin) SupportedFormats() []platform.ImageFormat                        { return nil }
func (p *fakeRegistryPlugin) SupportedOS() []platform.OSFamily                                { return nil }
func (p *fakeRegistryPlugin) Init(_ context.Context, _ platform.PluginConfig) error           { return nil }
func (p *fakeRegistryPlugin) Validate(_ context.Context, _ v1alpha1.TargetSpec) error         { return nil }
func (p *fakeRegistryPlugin) Upload(_ context.Context, _ *platform.BuildArtifact) (*platform.UploadResult, error) {
	return &platform.UploadResult{}, nil
}
func (p *fakeRegistryPlugin) Register(_ context.Context, _ *platform.UploadResult) (*platform.ImageRef, error) {
	return &platform.ImageRef{}, nil
}
func (p *fakeRegistryPlugin) Cleanup(_ context.Context, _ *platform.BuildArtifact) error { return nil }
func (p *fakeRegistryPlugin) HealthCheck(_ context.Context) error                          { return nil }
