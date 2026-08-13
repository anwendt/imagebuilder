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
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/provider"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
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

func envMap(env []corev1.EnvVar) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}

func hasSecretVolume(dep *appsv1.Deployment, name, secretName string) bool {
	for _, volume := range dep.Spec.Template.Spec.Volumes {
		if volume.Name == name && volume.Secret != nil && volume.Secret.SecretName == secretName {
			return true
		}
	}
	return false
}

func hasVolumeMount(container corev1.Container, name, mountPath string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == name && mount.MountPath == mountPath && mount.ReadOnly {
			return true
		}
	}
	return false
}

func conditionMessage(pp *v1alpha1.PlatformProvider, conditionType string) string {
	for _, condition := range pp.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Message
		}
	}
	return ""
}

type fakePlatformPlugin struct {
	name         string
	version      string
	formats      []platform.ImageFormat
	os           []platform.OSFamily
	healthErr    error
	healthChecks int
}

func (p *fakePlatformPlugin) Name() string                                        { return p.name }
func (p *fakePlatformPlugin) Version() string                                     { return p.version }
func (p *fakePlatformPlugin) SupportedFormats() []platform.ImageFormat            { return p.formats }
func (p *fakePlatformPlugin) SupportedOS() []platform.OSFamily                    { return p.os }
func (p *fakePlatformPlugin) Init(context.Context, platform.PluginConfig) error   { return nil }
func (p *fakePlatformPlugin) Validate(context.Context, v1alpha1.TargetSpec) error { return nil }
func (p *fakePlatformPlugin) Upload(context.Context, *platform.BuildArtifact) (*platform.UploadResult, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *fakePlatformPlugin) Register(context.Context, *platform.UploadResult) (*platform.ImageRef, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *fakePlatformPlugin) Cleanup(context.Context, *platform.BuildArtifact) error { return nil }
func (p *fakePlatformPlugin) HealthCheck(context.Context) error {
	p.healthChecks++
	return p.healthErr
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
	if result.RequeueAfter != 0 {
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
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter set after adding finalizer")
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

func TestProviderReconcile_ClusterScopedProviderUsesConfiguredProviderNamespace(t *testing.T) {
	pp := newPP("cluster-provider", "", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, ProviderNamespace: "imagebuilder-system"}

	reconcileProvider(t, r, "cluster-provider", "")

	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "provider-cluster-provider",
		Namespace: "imagebuilder-system",
	}, dep); err != nil {
		t.Fatalf("expected Deployment provider-cluster-provider in imagebuilder-system: %v", err)
	}
	svc := &corev1.Service{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "provider-cluster-provider",
		Namespace: "imagebuilder-system",
	}, svc); err != nil {
		t.Fatalf("expected Service provider-cluster-provider in imagebuilder-system: %v", err)
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

func TestProviderReconcile_MutualTLSMountsProviderServerSecrets(t *testing.T) {
	pp := newPP("tls-provider", "default", "ghcr.io/anwendt/provider-tls:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.Transport = &v1alpha1.ProviderTransportSpec{
		TLS: &v1alpha1.ProviderTransportTLSSpec{
			Mode: "Mutual",
			CASecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-ca",
				Namespace: "default",
			},
			ClientCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "operator-client",
				Namespace: "default",
			},
			ServerCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-server",
				Namespace: "default",
			},
		},
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, RequireMTLS: true}

	reconcileProvider(t, r, "tls-provider", "default")

	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "provider-tls-provider", Namespace: "default"}, dep); err != nil {
		t.Fatalf("expected Deployment provider-tls-provider: %v", err)
	}
	container := dep.Spec.Template.Spec.Containers[0]
	env := envMap(container.Env)
	if env["TMPDIR"] != "/var/lib/imagebuilder/uploads" {
		t.Fatalf("provider TMPDIR = %q", env["TMPDIR"])
	}
	if len(dep.Spec.Template.Spec.Volumes) == 0 || dep.Spec.Template.Spec.Volumes[0].Name != "provider-upload-tmp" || dep.Spec.Template.Spec.Volumes[0].EmptyDir == nil {
		t.Fatalf("provider upload temporary volume = %#v", dep.Spec.Template.Spec.Volumes)
	}
	if len(container.VolumeMounts) == 0 || container.VolumeMounts[0].Name != "provider-upload-tmp" || container.VolumeMounts[0].MountPath != "/var/lib/imagebuilder/uploads" || container.VolumeMounts[0].ReadOnly {
		t.Fatalf("provider upload temporary mount = %#v", container.VolumeMounts)
	}
	if env["PROVIDER_GRPC_TLS_MODE"] != "Mutual" ||
		env["PROVIDER_GRPC_TLS_CERT_FILE"] != "/var/run/imagebuilder/provider-tls/tls.crt" ||
		env["PROVIDER_GRPC_TLS_KEY_FILE"] != "/var/run/imagebuilder/provider-tls/tls.key" ||
		env["PROVIDER_GRPC_TLS_CLIENT_CA_FILE"] != "/var/run/imagebuilder/provider-client-ca/ca.crt" {
		t.Fatalf("provider TLS env = %#v", env)
	}
	if !hasSecretVolume(dep, "provider-tls", "provider-server") ||
		!hasSecretVolume(dep, "provider-client-ca", "provider-ca") {
		t.Fatalf("provider TLS volumes = %#v", dep.Spec.Template.Spec.Volumes)
	}
	if !hasVolumeMount(container, "provider-tls", "/var/run/imagebuilder/provider-tls") ||
		!hasVolumeMount(container, "provider-client-ca", "/var/run/imagebuilder/provider-client-ca") {
		t.Fatalf("provider TLS mounts = %#v", container.VolumeMounts)
	}
}

func TestProviderReconcile_RequireMTLSRejectsPlaintextProvider(t *testing.T) {
	tests := []struct {
		name      string
		transport *v1alpha1.ProviderTransportSpec
	}{
		{
			name: "missing transport",
		},
		{
			name: "explicit disabled",
			transport: &v1alpha1.ProviderTransportSpec{
				TLS: &v1alpha1.ProviderTransportTLSSpec{Mode: "Disabled"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pp := newPP("plaintext-provider", "default", "ghcr.io/anwendt/provider-plaintext:v1")
			pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
			pp.Spec.Transport = tt.transport

			s := providerScheme(t)
			c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
			r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, RequireMTLS: true}

			reconcileProvider(t, r, "plaintext-provider", "default")

			updated := &v1alpha1.PlatformProvider{}
			if err := c.Get(context.Background(), types.NamespacedName{Name: "plaintext-provider", Namespace: "default"}, updated); err != nil {
				t.Fatalf("get updated PlatformProvider: %v", err)
			}
			if updated.Status.Phase != "Unhealthy" {
				t.Fatalf("phase = %q, want Unhealthy", updated.Status.Phase)
			}
			if len(updated.Status.Conditions) == 0 || !strings.Contains(updated.Status.Conditions[0].Message, "provider mTLS is required") {
				t.Fatalf("conditions = %#v, want require mTLS message", updated.Status.Conditions)
			}

			dep := &appsv1.Deployment{}
			err := c.Get(context.Background(), types.NamespacedName{Name: "provider-plaintext-provider", Namespace: "default"}, dep)
			if !apierrors.IsNotFound(err) {
				t.Fatalf("deployment should not be created when mTLS is required, get err = %v", err)
			}
		})
	}
}

func TestProviderReconcile_GlobalPackagePolicyRejectsUnsignedMutableProvider(t *testing.T) {
	pp := newPP("mutable-provider", "default", "docker.io/library/provider:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{
		Client:            c,
		Scheme:            s,
		RequireDigest:     true,
		RequireSignature:  true,
		AllowedRegistries: []string{"ghcr.io/anwendt"},
	}

	reconcileProvider(t, r, "mutable-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mutable-provider", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated PlatformProvider: %v", err)
	}
	if updated.Status.Phase != "Unhealthy" {
		t.Fatalf("phase = %q, want Unhealthy", updated.Status.Phase)
	}
	if len(updated.Status.Conditions) == 0 || !strings.Contains(updated.Status.Conditions[0].Message, "pinned by digest") {
		t.Fatalf("conditions = %#v, want digest policy message", updated.Status.Conditions)
	}
}

func TestProviderReconcile_MutualTLSUpdatesExistingDeployment(t *testing.T) {
	pp := newPP("tls-update-provider", "default", "ghcr.io/anwendt/provider-tls:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "tls-update-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "tls-update-provider", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get PlatformProvider: %v", err)
	}
	updated.Spec.Transport = &v1alpha1.ProviderTransportSpec{
		TLS: &v1alpha1.ProviderTransportTLSSpec{
			Mode: "Mutual",
			CASecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-ca",
				Namespace: "default",
			},
			ClientCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "operator-client",
				Namespace: "default",
			},
			ServerCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-server",
				Namespace: "default",
			},
		},
	}
	if err := c.Update(context.Background(), updated); err != nil {
		t.Fatalf("update PlatformProvider: %v", err)
	}

	reconcileProvider(t, r, "tls-update-provider", "default")

	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "provider-tls-update-provider", Namespace: "default"}, dep); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	container := dep.Spec.Template.Spec.Containers[0]
	if envMap(container.Env)["PROVIDER_GRPC_TLS_MODE"] != "Mutual" {
		t.Fatalf("provider TLS env was not rolled out: %#v", container.Env)
	}
	if !hasSecretVolume(dep, "provider-tls", "provider-server") {
		t.Fatalf("provider TLS volume was not rolled out: %#v", dep.Spec.Template.Spec.Volumes)
	}
}

func TestProviderReconcile_MutualTLSMissingServerSecretRefIsUnhealthy(t *testing.T) {
	pp := newPP("bad-tls-provider", "default", "ghcr.io/anwendt/provider-tls:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.Transport = &v1alpha1.ProviderTransportSpec{
		TLS: &v1alpha1.ProviderTransportTLSSpec{
			Mode: "Mutual",
			CASecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-ca",
				Namespace: "default",
			},
			ClientCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "operator-client",
				Namespace: "default",
			},
		},
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "bad-tls-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "bad-tls-provider", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated PlatformProvider: %v", err)
	}
	if updated.Status.Phase != "Unhealthy" {
		t.Fatalf("phase = %q, want Unhealthy", updated.Status.Phase)
	}
}

func TestProviderReconcile_MutualTLSSecretNamespaceMustMatchProviderNamespace(t *testing.T) {
	pp := newPP("bad-tls-namespace", "", "ghcr.io/anwendt/provider-tls:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.Transport = &v1alpha1.ProviderTransportSpec{
		TLS: &v1alpha1.ProviderTransportTLSSpec{
			Mode: "Mutual",
			CASecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-ca",
				Namespace: "other",
			},
			ClientCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "operator-client",
				Namespace: "imagebuilder-system",
			},
			ServerCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
				Name:      "provider-server",
				Namespace: "imagebuilder-system",
			},
		},
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, ProviderNamespace: "imagebuilder-system"}

	reconcileProvider(t, r, "bad-tls-namespace", "")

	updated := &v1alpha1.PlatformProvider{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "bad-tls-namespace"}, updated); err != nil {
		t.Fatalf("get updated PlatformProvider: %v", err)
	}
	if updated.Status.Phase != "Unhealthy" {
		t.Fatalf("phase = %q, want Unhealthy", updated.Status.Phase)
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

func TestProviderReconcile_ReadyProvider_RegistersCapabilities(t *testing.T) {
	pp := newPP("aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "provider", Image: "ghcr.io/anwendt/provider-aws:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}

	reg := plugin.NewRegistry(nil)
	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{
		Client:   c,
		Scheme:   s,
		Registry: reg,
		ConnectProvider: func(ctx context.Context, address string) (platform.Plugin, error) {
			if address != "provider-aws.default.svc:50051" {
				t.Fatalf("provider address = %q, want provider-aws.default.svc:50051", address)
			}
			return &fakePlatformPlugin{
				name:    "aws",
				version: "v1.2.3",
				formats: []platform.ImageFormat{platform.FormatVMDK, platform.FormatRaw},
				os:      []platform.OSFamily{platform.OSFamilyLinux, platform.OSFamilyWindows},
			}, nil
		},
	}

	reconcileProvider(t, r, "aws", "default")

	if !reg.SupportsExternal("aws", string(pp.UID), "aws") {
		t.Fatal("expected external provider installation to be registered after readiness handshake")
	}

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Healthy" {
		t.Errorf("phase = %q, want Healthy", updated.Status.Phase)
	}
	if updated.Status.Capabilities == nil {
		t.Fatal("expected status.capabilities to be populated")
	}
	if updated.Status.Capabilities.ProviderName != "aws" {
		t.Errorf("providerName = %q, want aws", updated.Status.Capabilities.ProviderName)
	}
	if updated.Status.Capabilities.ProtocolVersion != "v1" {
		t.Errorf("protocolVersion = %q, want v1", updated.Status.Capabilities.ProtocolVersion)
	}
}

func TestProviderReconcile_AdvertisedNameMustMatchResourceName(t *testing.T) {
	pp := newPP("external-aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-external-aws", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"imagebuilder.io/provider-name": "external-aws"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: pp.Spec.Package}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	reg := plugin.NewRegistry(nil)
	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{
		Client: c, Scheme: s, Registry: reg,
		ConnectProvider: func(context.Context, string) (platform.Plugin, error) {
			return &fakePlatformPlugin{name: "aws", version: "external"}, nil
		},
	}
	reconcileProvider(t, r, pp.Name, pp.Namespace)

	updated := &v1alpha1.PlatformProvider{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: pp.Name, Namespace: pp.Namespace}, updated); err != nil {
		t.Fatalf("get PlatformProvider: %v", err)
	}
	if updated.Status.Phase != "Unhealthy" || !strings.Contains(conditionMessage(updated, "Ready"), "must match") {
		t.Fatalf("status=%#v", updated.Status)
	}
	if reg.SupportsExternal(pp.Name, string(pp.UID), "aws") {
		t.Fatal("mismatched provider was registered")
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

func TestProviderReconcile_LostReadinessDeregistersOnlyExternalAdapter(t *testing.T) {
	pp := newPP("aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Phase = "Healthy"
	pp.Status.Capabilities = &v1alpha1.ProviderCapabilities{ProviderName: "aws", ProtocolVersion: "v1"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: pp.Spec.Package}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	reg := plugin.NewRegistry(nil)
	if err := reg.Register(&fakePlatformPlugin{name: "aws", version: "builtin"}); err != nil {
		t.Fatalf("register builtin: %v", err)
	}
	if err := reg.RegisterExternal(pp.Name, string(pp.UID), &fakePlatformPlugin{name: "aws", version: "external"}); err != nil {
		t.Fatalf("register external: %v", err)
	}
	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, Registry: reg}
	reconcileProvider(t, r, pp.Name, pp.Namespace)

	if reg.SupportsExternal(pp.Name, string(pp.UID), "aws") {
		t.Fatal("unready provider remained externally registered")
	}
	if !reg.Supports("aws") {
		t.Fatal("unready external provider removed colliding builtin")
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

func TestProviderReconcile_AlreadyHealthy_PerformsProviderHealthCheck(t *testing.T) {
	pp := newPP("aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Phase = "Healthy"
	pp.Status.Capabilities = &v1alpha1.ProviderCapabilities{ProviderName: "aws", ProtocolVersion: "v1"}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "provider", Image: "ghcr.io/anwendt/provider-aws:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}

	registered := &fakePlatformPlugin{name: "aws", version: "v1.2.3"}
	reg := plugin.NewRegistry(nil)
	if err := reg.RegisterExternal(pp.Name, string(pp.UID), registered); err != nil {
		t.Fatalf("register fake plugin: %v", err)
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileProvider(t, r, "aws", "default")

	if registered.healthChecks != 1 {
		t.Errorf("healthChecks = %d, want 1", registered.healthChecks)
	}
}

func TestProviderReconcile_AlreadyHealthy_UnhealthyWhenProviderHealthCheckFails(t *testing.T) {
	pp := newPP("aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Status.Phase = "Healthy"
	pp.Status.Capabilities = &v1alpha1.ProviderCapabilities{ProviderName: "aws", ProtocolVersion: "v1"}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "provider", Image: "ghcr.io/anwendt/provider-aws:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}

	reg := plugin.NewRegistry(nil)
	if err := reg.RegisterExternal(pp.Name, string(pp.UID), &fakePlatformPlugin{name: "aws", version: "v1.2.3", healthErr: fmt.Errorf("grpc unavailable")}); err != nil {
		t.Fatalf("register fake plugin: %v", err)
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileProvider(t, r, "aws", "default")

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Unhealthy" {
		t.Errorf("phase = %q, want Unhealthy", updated.Status.Phase)
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

func TestProviderReconcile_PackagePolicy_RequireDigestRejectsMutableTag(t *testing.T) {
	pp := newPP("aws-provider", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.Security = &v1alpha1.ProviderPackageSecuritySpec{RequireDigest: true}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "aws-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != "Unhealthy" {
		t.Fatalf("phase = %q, want Unhealthy", updated.Status.Phase)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "provider-aws-provider", Namespace: "default"}, dep); err == nil {
		t.Fatal("deployment should not be created when provider package policy rejects the image")
	}
}

func TestProviderReconcile_PackagePolicy_VerifySignatureFailsClosedWithoutVerifier(t *testing.T) {
	image := "ghcr.io/anwendt/provider-aws@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pp := newPP("aws-provider", "default", image)
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.Security = &v1alpha1.ProviderPackageSecuritySpec{
		AllowedRegistries: []string{"ghcr.io/anwendt"},
		RequireDigest:     true,
		VerifySignature:   true,
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}

	reconcileProvider(t, r, "aws-provider", "default")

	updated := &v1alpha1.PlatformProvider{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "aws-provider", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get PlatformProvider: %v", err)
	}
	if updated.Status.Phase != "Unhealthy" || !strings.Contains(conditionMessage(updated, "Ready"), "no cryptographic verifier") {
		t.Fatalf("status=%#v", updated.Status)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "provider-aws-provider", Namespace: "default"}, dep); err == nil {
		t.Fatal("provider Deployment must not be created without a cryptographic verifier")
	}
}

func TestProviderReconcile_SignatureVerificationFailureDeletesExistingDeployment(t *testing.T) {
	image := "ghcr.io/anwendt/provider-aws@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pp := newPP("aws-provider", "default", image)
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.Spec.Security = &v1alpha1.ProviderPackageSecuritySpec{RequireDigest: true, VerifySignature: true}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws-provider", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws-provider"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: image}}}},
		},
	}
	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s}
	reconcileProvider(t, r, pp.Name, pp.Namespace)

	if err := c.Get(context.Background(), types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deployment still exists or lookup failed unexpectedly: %v", err)
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

func TestProviderReconcile_Deletion_DeregistersProvider(t *testing.T) {
	now := metav1.Now()
	pp := newPP("external-aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.DeletionTimestamp = &now
	pp.Status.Capabilities = &v1alpha1.ProviderCapabilities{ProviderName: "aws", ProtocolVersion: "v1"}

	reg := plugin.NewRegistry(nil)
	if err := reg.RegisterExternal(pp.Name, string(pp.UID), &fakePlatformPlugin{name: "aws", version: "v1.2.3"}); err != nil {
		t.Fatalf("register fake plugin: %v", err)
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, Registry: reg}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "external-aws", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.SupportsExternal(pp.Name, string(pp.UID), "aws") {
		t.Error("expected provider to be deregistered during deletion")
	}
}

func TestProviderReconcile_ExternalSameNameDoesNotReplaceBuiltin(t *testing.T) {
	pp := newPP("aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-aws", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"imagebuilder.io/provider-name": "aws"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: pp.Spec.Package}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	builtin := &fakePlatformPlugin{name: "aws", version: "builtin"}
	reg := plugin.NewRegistry(nil)
	if err := reg.Register(builtin); err != nil {
		t.Fatalf("register builtin: %v", err)
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp, dep).Build()
	r := &provider.PlatformProviderReconciler{
		Client: c, Scheme: s, Registry: reg,
		ConnectProvider: func(context.Context, string) (platform.Plugin, error) {
			return &fakePlatformPlugin{name: "aws", version: "external"}, nil
		},
	}
	reconcileProvider(t, r, pp.Name, pp.Namespace)

	gotBuiltin, err := reg.New("aws")
	if err != nil || gotBuiltin.Version() != "builtin" {
		t.Fatalf("builtin = %#v error=%v", gotBuiltin, err)
	}
	gotExternal, err := reg.External(pp.Name, string(pp.UID), "aws")
	if err != nil || gotExternal.Version() != "external" {
		t.Fatalf("external = %#v error=%v", gotExternal, err)
	}
}

func TestProviderReconcile_DeletionNeverRemovesCollidingBuiltin(t *testing.T) {
	now := metav1.Now()
	pp := newPP("aws", "default", "ghcr.io/anwendt/provider-aws:v1")
	pp.Finalizers = []string{"imagebuilder.io/provider-cleanup"}
	pp.DeletionTimestamp = &now
	pp.Status.Capabilities = &v1alpha1.ProviderCapabilities{ProviderName: "aws", ProtocolVersion: "v1"}
	reg := plugin.NewRegistry(nil)
	if err := reg.Register(&fakePlatformPlugin{name: "aws", version: "builtin"}); err != nil {
		t.Fatalf("register builtin: %v", err)
	}
	if err := reg.RegisterExternal(pp.Name, string(pp.UID), &fakePlatformPlugin{name: "aws", version: "external"}); err != nil {
		t.Fatalf("register external: %v", err)
	}

	s := providerScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.PlatformProvider{}).WithObjects(pp).Build()
	r := &provider.PlatformProviderReconciler{Client: c, Scheme: s, Registry: reg}
	reconcileProvider(t, r, pp.Name, pp.Namespace)

	if !reg.Supports("aws") {
		t.Fatal("external deletion removed the built-in provider")
	}
	if reg.SupportsExternal(pp.Name, string(pp.UID), "aws") {
		t.Fatal("external provider remained registered after deletion")
	}
}
