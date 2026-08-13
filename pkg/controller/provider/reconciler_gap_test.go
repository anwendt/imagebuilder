// pkg/controller/provider/reconciler_gap_test.go
//
// Additional tests targeting coverage gaps in the PlatformProvider reconciler:
//   - setCondition: update-existing branch
//   - reconcileHealth: Healthy→Healthy short-circuit (no status update)
//   - buildDeployment: custom PackagePullPolicy forwarded

package provider_test

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/provider"
)

// ---------------------------------------------------------------------------
// setCondition update branch:
// pre-populate a condition, then trigger a reconcile that sets the same type
// ---------------------------------------------------------------------------

func TestProviderReconcile_SetCondition_UpdatesExisting(t *testing.T) {
	pp := newPP("cond-pp", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "OldReason",
			Message:            "old",
			LastTransitionTime: metav1.Now(),
		},
	}

	// Deployment with ReadyReplicas=0 → setCondition("Ready", False, ...) → UPDATE existing
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-cond-pp", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "cond-pp"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "provider", Image: "ghcr.io/anwendt/provider-aws:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 0},
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "cond-pp", "default")

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "cond-pp", Namespace: "default"}, updated) //nolint:errcheck

	readyCount := 0
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Ready" {
			readyCount++
			if cond.Status != metav1.ConditionFalse {
				t.Errorf("Ready condition status = %q, want False after update", cond.Status)
			}
		}
	}
	if readyCount != 1 {
		t.Errorf("expected exactly 1 Ready condition, got %d", readyCount)
	}
}

// ---------------------------------------------------------------------------
// reconcileHealth: already Healthy + still ReadyReplicas>0 → no status update, just requeue
// ---------------------------------------------------------------------------

func TestProviderReconcile_AlreadyHealthy_NoStatusUpdate(t *testing.T) {
	pp := newPP("already-healthy", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Phase = "Healthy" // already set

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-already-healthy", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "already-healthy"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "provider", Image: "ghcr.io/anwendt/provider-aws:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	result := reconcileProvider(t, r, "already-healthy", "default")

	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter when already Healthy")
	}

	// Phase should remain Healthy
	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "already-healthy", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Healthy" {
		t.Errorf("phase = %q, want Healthy (no change when already healthy)", updated.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// buildDeployment: custom PackagePullPolicy forwarded to container
// ---------------------------------------------------------------------------

func TestProviderReconcile_CustomPullPolicy_ForwardedToDeployment(t *testing.T) {
	pp := newPP("pull-policy-test", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.PackagePullPolicy = "Always"

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "pull-policy-test", "default")

	dep := &appsv1.Deployment{}
	c.Get(context.Background(), types.NamespacedName{Name: "provider-pull-policy-test", Namespace: "default"}, dep) //nolint:errcheck

	if dep.Spec.Template.Spec.Containers[0].ImagePullPolicy != corev1.PullAlways {
		t.Errorf("ImagePullPolicy = %q, want Always", dep.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	}
}

// ---------------------------------------------------------------------------
// Deployment labels
// ---------------------------------------------------------------------------

func TestProviderReconcile_DeploymentLabels(t *testing.T) {
	pp := newPP("label-test", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "label-test", "default")

	dep := &appsv1.Deployment{}
	c.Get(context.Background(), types.NamespacedName{Name: "provider-label-test", Namespace: "default"}, dep) //nolint:errcheck

	if dep.Labels["imagebuilder.io/provider-name"] != "label-test" {
		t.Errorf("label imagebuilder.io/provider-name = %q, want label-test", dep.Labels["imagebuilder.io/provider-name"])
	}
	if dep.Labels["app.kubernetes.io/managed-by"] != "imagebuilder" {
		t.Error("label app.kubernetes.io/managed-by missing")
	}
	if dep.Labels["imagebuilder.io/provider-pod"] != "true" || dep.Spec.Template.Labels["imagebuilder.io/provider-pod"] != "true" {
		t.Error("provider pod signature-policy selector label missing")
	}
}
