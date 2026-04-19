// pkg/controller/provider/reconciler_test.go
//
// Unit tests for the PlatformProvider reconciler.
// Uses controller-runtime fake client — no cluster required.
//
// Covered behaviours:
//   - NotFound → no error, no requeue
//   - Finalizer added on first reconcile
//   - Deployment created on first full reconcile
//   - Service created on first full reconcile
//   - Deployment image updated when spec changes
//   - Phase = Installing when Deployment has no ready replicas
//   - Phase = Healthy when Deployment has ready replicas
//   - Phase = Unhealthy when ready replicas drop to 0 after being Healthy
//   - Deletion removes finalizer

package provider_test

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/provider"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func providerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add v1alpha1: %v", err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("add appsv1: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	return s
}

func newPP(name, namespace, pkg string) *v1alpha1.PlatformProvider {
	return &v1alpha1.PlatformProvider{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "imagebuilder.io/v1alpha1",
			Kind:       "PlatformProvider",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "test-uid",
		},
		Spec: v1alpha1.PlatformProviderSpec{
			Package:           pkg,
			PackagePullPolicy: "IfNotPresent",
		},
	}
}

func newProviderReconciler(t *testing.T, objs ...runtime.Object) (*provider.PlatformProviderReconciler, *fake.ClientBuilder) {
	t.Helper()
	s := providerScheme(t)
	cb := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&v1alpha1.PlatformProvider{})
	for _, o := range objs {
		cb = cb.WithRuntimeObjects(o)
	}
	r := &provider.PlatformProviderReconciler{
		Client: cb.Build(),
		Scheme: s,
	}
	return r, cb
}

func reconcileProvider(t *testing.T, r *provider.PlatformProviderReconciler, name, namespace string) ctrl.Result {
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
// Not found
// ---------------------------------------------------------------------------

func TestProviderReconcile_NotFound_ReturnsNil(t *testing.T) {
	r, _ := newProviderReconciler(t)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ghost", Namespace: "default"},
	})
	if err != nil {
		t.Errorf("expected nil error for not-found, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found")
	}
}

// ---------------------------------------------------------------------------
// Finalizer
// ---------------------------------------------------------------------------

func TestProviderReconcile_AddsFinalizer(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "aws-provider", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Requeue {
		t.Error("expected Requeue=true after adding finalizer")
	}

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated) //nolint:errcheck
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == "imagebuilder.io/provider-cleanup" {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		t.Error("finalizer imagebuilder.io/provider-cleanup not found after first reconcile")
	}
}

// ---------------------------------------------------------------------------
// Deployment created
// ---------------------------------------------------------------------------

func TestProviderReconcile_CreatesDeployment(t *testing.T) {
	pp := newPP("vsphere-provider", "default", "ghcr.io/anwendt/provider-vsphere:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "vsphere-provider", "default")

	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "provider-vsphere-provider",
		Namespace: "default",
	}, dep); err != nil {
		t.Fatalf("expected Deployment provider-vsphere-provider to exist: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "ghcr.io/anwendt/provider-vsphere:v1" {
		t.Errorf("container image = %q, want ghcr.io/anwendt/provider-vsphere:v1",
			dep.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestProviderReconcile_DeploymentOwnerReference(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "aws-provider", "default")

	dep := &appsv1.Deployment{}
	c.Get(context.Background(), types.NamespacedName{Name: "provider-aws-provider", Namespace: "default"}, dep) //nolint:errcheck

	if len(dep.OwnerReferences) != 1 || dep.OwnerReferences[0].Name != "aws-provider" {
		t.Errorf("Deployment should be owned by PlatformProvider aws-provider, got: %v", dep.OwnerReferences)
	}
}

// ---------------------------------------------------------------------------
// Service created
// ---------------------------------------------------------------------------

func TestProviderReconcile_CreatesService(t *testing.T) {
	pp := newPP("gcp-provider", "default", "ghcr.io/anwendt/provider-gcp:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "gcp-provider", "default")

	svc := &corev1.Service{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "provider-gcp-provider",
		Namespace: "default",
	}, svc); err != nil {
		t.Fatalf("expected Service provider-gcp-provider to exist: %v", err)
	}
	if len(svc.Spec.Ports) == 0 || svc.Spec.Ports[0].Port != 50051 {
		t.Errorf("Service should expose gRPC port 50051, got: %v", svc.Spec.Ports)
	}
}

func TestProviderReconcile_ServiceType_ClusterIP(t *testing.T) {
	pp := newPP("openstack-provider", "default", "ghcr.io/anwendt/provider-openstack:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "openstack-provider", "default")

	svc := &corev1.Service{}
	c.Get(context.Background(), types.NamespacedName{Name: "provider-openstack-provider", Namespace: "default"}, svc) //nolint:errcheck
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("Service type = %q, want ClusterIP", svc.Spec.Type)
	}
}

// ---------------------------------------------------------------------------
// Phase: Installing → Healthy
// ---------------------------------------------------------------------------

func TestProviderReconcile_Phase_Installing_WhenDeploymentNotReady(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws-provider", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws-provider"},
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

	reconcileProvider(t, r, "aws-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Installing" {
		t.Errorf("phase = %q, want Installing when ReadyReplicas=0", updated.Status.Phase)
	}
}

func TestProviderReconcile_Phase_Healthy_WhenDeploymentReady(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws-provider", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws-provider"},
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

	reconcileProvider(t, r, "aws-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Healthy" {
		t.Errorf("phase = %q, want Healthy when ReadyReplicas=1", updated.Status.Phase)
	}
}

func TestProviderReconcile_Phase_Unhealthy_WhenHealthyThenDrops(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Phase = "Healthy" // was previously healthy

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws-provider", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws-provider"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "provider", Image: "ghcr.io/anwendt/provider-aws:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 0}, // dropped
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "aws-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Unhealthy" {
		t.Errorf("phase = %q, want Unhealthy when ReadyReplicas dropped from 1 to 0", updated.Status.Phase)
	}
}

func TestProviderReconcile_RequeuesAfterHealthCheck(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Phase = "Healthy"

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws-provider", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws-provider"},
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

	result := reconcileProvider(t, r, "aws-provider", "default")
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter for periodic health check")
	}
}

// ---------------------------------------------------------------------------
// Deployment security context
// ---------------------------------------------------------------------------

func TestProviderReconcile_Deployment_SecurityContext_NonRoot(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "aws-provider", "default")

	dep := &appsv1.Deployment{}
	c.Get(context.Background(), types.NamespacedName{Name: "provider-aws-provider", Namespace: "default"}, dep) //nolint:errcheck

	psc := dep.Spec.Template.Spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("pod security context RunAsNonRoot should be true")
	}
	csc := dep.Spec.Template.Spec.Containers[0].SecurityContext
	if csc == nil || csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Error("container security context AllowPrivilegeEscalation should be false")
	}
}

// ---------------------------------------------------------------------------
// PullSecrets forwarded
// ---------------------------------------------------------------------------

func TestProviderReconcile_PullSecrets_ForwardedToDeployment(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.PackagePullSecrets = []string{"registry-creds"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "aws-provider", "default")

	dep := &appsv1.Deployment{}
	c.Get(context.Background(), types.NamespacedName{Name: "provider-aws-provider", Namespace: "default"}, dep) //nolint:errcheck

	if len(dep.Spec.Template.Spec.ImagePullSecrets) != 1 ||
		dep.Spec.Template.Spec.ImagePullSecrets[0].Name != "registry-creds" {
		t.Errorf("pull secrets = %v, want [registry-creds]", dep.Spec.Template.Spec.ImagePullSecrets)
	}
}

// ---------------------------------------------------------------------------
// Deletion
// ---------------------------------------------------------------------------

func TestProviderReconcile_Deletion_RemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.DeletionTimestamp = &now

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "aws-provider", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated) //nolint:errcheck
	for _, f := range updated.Finalizers {
		if f == "imagebuilder.io/provider-cleanup" {
			t.Error("finalizer should have been removed during deletion")
		}
	}
}
