// pkg/controller/provider/reconciler.go
//
// PlatformProvider Reconciler — manages the lifecycle of external provider pods.
//
// Each PlatformProvider CR causes the operator to:
//   1. Pull the specified OCI image and run it as a Kubernetes Deployment.
//   2. Expose the Deployment via a ClusterIP Service (gRPC Unix socket or TCP).
//   3. Perform a gRPC HealthCheck handshake and populate status.capabilities.
//   4. Set phase = Healthy / Unhealthy based on continuous health checks.
//
// ADR-002: providers run as separate gRPC containers (not .so plugins).
// SR-007: providers run with minimal RBAC; they cannot access Secrets.

package provider

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/observability"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	plugingrpc "github.com/anwendt/imagebuilder/pkg/plugin/grpc"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/security/signaturepolicy"
)

const (
	providerFinalizerName     = "imagebuilder.io/provider-cleanup"
	providerGRPCPort          = 50051
	requeueAfter              = 30
	defaultProviderNamespace  = "imagebuilder-system"
	providerTLSMountPath      = "/var/run/imagebuilder/provider-tls"
	providerClientCAMountPath = "/var/run/imagebuilder/provider-client-ca"
	providerUploadTempPath    = "/var/lib/imagebuilder/uploads"
	providerUploadTempVolume  = "provider-upload-tmp"
)

// PlatformProviderReconciler reconciles PlatformProvider resources.
//
// +kubebuilder:rbac:groups=imagebuilder.io,resources=platformproviders,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=imagebuilder.io,resources=platformproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=imagebuilder.io,resources=platformproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get
// +kubebuilder:rbac:groups=kyverno.io,resources=clusterpolicies,verbs=get
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get
type PlatformProviderReconciler struct {
	client.Client
	Scheme                    *runtime.Scheme
	Registry                  *plugin.Registry
	ProviderNamespace         string
	RequireMTLS               bool
	RequireDigest             bool
	RequireSignature          bool
	AllowedRegistries         []string
	RestrictServiceAccounts   bool
	AllowedServiceAccounts    []string
	ForbidServiceAccountToken bool
	SignatureVerifier         *signaturepolicy.Verifier
	ConnectProvider           func(ctx context.Context, address string) (platform.Plugin, error)
	log                       *slog.Logger
}

func (r *PlatformProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.log = slog.Default().With(slog.String("controller", "platformprovider"))
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PlatformProvider{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

func (r *PlatformProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.log == nil {
		r.log = slog.Default().With(slog.String("controller", "platformprovider"))
	}
	log := r.log.With(slog.String("name", req.Name), slog.String("namespace", req.Namespace))

	pp := &v1alpha1.PlatformProvider{}
	if err := r.Get(ctx, req.NamespacedName, pp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get platformprovider: %w", err)
	}

	// Handle deletion.
	if !pp.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, pp, log)
	}

	// Ensure finalizer.
	if !controllerutil.ContainsFinalizer(pp, providerFinalizerName) {
		controllerutil.AddFinalizer(pp, providerFinalizerName)
		if err := r.Update(ctx, pp); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}

	// Reconcile Deployment and Service.
	if err := r.validateProviderPackagePolicy(pp); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider package policy rejected image: %v", err))
	}
	if pp.Spec.Security != nil && pp.Spec.Security.VerifySignature {
		if r.SignatureVerifier == nil {
			return r.failSignatureVerification(ctx, pp, log, "provider signature verification requested, but no cryptographic verifier is configured")
		}
		if err := r.SignatureVerifier.Verify(ctx, pp.Spec.Package); err != nil {
			return r.failSignatureVerification(ctx, pp, log, fmt.Sprintf("provider signature verification policy is unavailable: %v", err))
		}
	}
	if err := r.validateProviderTransportPolicy(pp); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider transport policy rejected: %v", err))
	}
	if err := r.validateProviderServiceAccountPolicy(pp); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider service account policy rejected: %v", err))
	}
	if err := r.reconcileDeployment(ctx, pp, log); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("deployment reconcile failed: %v", err))
	}
	if err := r.reconcileService(ctx, pp, log); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("service reconcile failed: %v", err))
	}

	// Check Deployment readiness and update phase.
	return r.reconcileHealth(ctx, pp, log)
}

func (r *PlatformProviderReconciler) failSignatureVerification(ctx context.Context, pp *v1alpha1.PlatformProvider, log *slog.Logger, reason string) (ctrl.Result, error) {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Name: providerDeploymentName(pp), Namespace: r.providerNamespace(pp)}
	if err := r.Get(ctx, key, deployment); err == nil {
		log.Warn("deleting provider deployment after signature verification failure", slog.String("deployment", key.Name))
		if err := r.Delete(ctx, deployment); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete unverified provider deployment: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("get provider deployment after signature verification failure: %w", err)
	}
	return r.setUnhealthy(ctx, pp, reason)
}

// ---------------------------------------------------------------------------
// Deployment
// ---------------------------------------------------------------------------

func (r *PlatformProviderReconciler) reconcileDeployment(ctx context.Context, pp *v1alpha1.PlatformProvider, log *slog.Logger) error {
	desired := r.buildDeployment(pp)
	if err := controllerutil.SetControllerReference(pp, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on deployment: %w", err)
	}

	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		log.Info("creating provider deployment", slog.String("deployment", desired.Name))
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}

	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template.Labels = desired.Spec.Template.Labels
	existing.Spec.Template.Annotations = desired.Spec.Template.Annotations
	existing.Spec.Template.Spec = desired.Spec.Template.Spec
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	return nil
}

func (r *PlatformProviderReconciler) buildDeployment(pp *v1alpha1.PlatformProvider) *appsv1.Deployment {
	replicas := int32(1)
	pullPolicy := corev1.PullIfNotPresent
	if pp.Spec.PackagePullPolicy != "" {
		pullPolicy = corev1.PullPolicy(pp.Spec.PackagePullPolicy)
	}

	labels := map[string]string{
		"app.kubernetes.io/managed-by":  "imagebuilder",
		"imagebuilder.io/provider-name": pp.Name,
		"imagebuilder.io/provider-pod":  "true",
	}
	for key, value := range pp.Spec.PodLabels {
		labels[key] = value
	}
	annotations := map[string]string{}
	for key, value := range pp.Spec.PodAnnotations {
		annotations[key] = value
	}
	var pullSecrets []corev1.LocalObjectReference
	for _, s := range pp.Spec.PackagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: s})
	}

	container := corev1.Container{
		Name:            "provider",
		Image:           pp.Spec.Package,
		ImagePullPolicy: pullPolicy,
		Ports: []corev1.ContainerPort{
			{Name: "grpc", ContainerPort: providerGRPCPort, Protocol: corev1.ProtocolTCP},
		},
		// SR-011: restricted security context.
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: boolPtr(false),
			ReadOnlyRootFilesystem:   boolPtr(true),
			RunAsNonRoot:             boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: "TMPDIR", Value: providerUploadTempPath})
	container.Env = append(container.Env, pp.Spec.Env...)
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      providerUploadTempVolume,
		MountPath: providerUploadTempPath,
	})
	volumes := []corev1.Volume{
		{
			Name: providerUploadTempVolume,
			VolumeSource: corev1.VolumeSource{
				// Direct-streaming providers do not consume this volume. It remains
				// available for bounded random-access fallbacks such as vSphere OVA/OVF.
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
	if tlsSpec := providerMutualTLS(pp); tlsSpec != nil {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "PROVIDER_GRPC_TLS_MODE", Value: "Mutual"},
			corev1.EnvVar{Name: "PROVIDER_GRPC_TLS_CERT_FILE", Value: providerTLSMountPath + "/" + providerTLSCertKey(tlsSpec.ServerCertificateSecretRef)},
			corev1.EnvVar{Name: "PROVIDER_GRPC_TLS_KEY_FILE", Value: providerTLSMountPath + "/" + providerTLSKeyKey(tlsSpec.ServerCertificateSecretRef)},
			corev1.EnvVar{Name: "PROVIDER_GRPC_TLS_CLIENT_CA_FILE", Value: providerClientCAMountPath + "/" + providerTLSCAKey(tlsSpec.CASecretRef)},
		)
		container.VolumeMounts = append(container.VolumeMounts,
			corev1.VolumeMount{Name: "provider-tls", MountPath: providerTLSMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: "provider-client-ca", MountPath: providerClientCAMountPath, ReadOnly: true},
		)
		volumes = append(volumes,
			corev1.Volume{
				Name: "provider-tls",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: tlsSpec.ServerCertificateSecretRef.Name},
				},
			},
			corev1.Volume{
				Name: "provider-client-ca",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: tlsSpec.CASecretRef.Name},
				},
			},
		)
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      providerDeploymentName(pp),
			Namespace: r.providerNamespace(pp),
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					ImagePullSecrets:   pullSecrets,
					ServiceAccountName: pp.Spec.ServiceAccountName,
					// AS-028: provider pods do not need API server access by default.
					AutomountServiceAccountToken: providerAutomountServiceAccountToken(pp),
					// AS-053: explicitly forbid host namespace sharing.
					HostNetwork: false,
					HostPID:     false,
					HostIPC:     false,
					Containers:  []corev1.Container{container},
					Volumes:     volumes,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
		},
	}
}

func providerAutomountServiceAccountToken(pp *v1alpha1.PlatformProvider) *bool {
	if pp.Spec.AutomountServiceAccountToken != nil {
		return pp.Spec.AutomountServiceAccountToken
	}
	return boolPtr(false)
}

// ---------------------------------------------------------------------------
// Service (ClusterIP for gRPC)
// ---------------------------------------------------------------------------

func (r *PlatformProviderReconciler) reconcileService(ctx context.Context, pp *v1alpha1.PlatformProvider, log *slog.Logger) error {
	desired := r.buildService(pp)
	if err := controllerutil.SetControllerReference(pp, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on service: %w", err)
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		log.Info("creating provider service", slog.String("service", desired.Name))
		return r.Create(ctx, desired)
	}
	return err
}

func (r *PlatformProviderReconciler) buildService(pp *v1alpha1.PlatformProvider) *corev1.Service {
	labels := map[string]string{
		"app.kubernetes.io/managed-by":  "imagebuilder",
		"imagebuilder.io/provider-name": pp.Name,
		"imagebuilder.io/provider-pod":  "true",
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      providerServiceName(pp),
			Namespace: r.providerNamespace(pp),
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       providerGRPCPort,
					TargetPort: intstr.FromInt(providerGRPCPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

// ---------------------------------------------------------------------------
// Health check — based on Deployment readiness
// ---------------------------------------------------------------------------

func (r *PlatformProviderReconciler) reconcileHealth(ctx context.Context, pp *v1alpha1.PlatformProvider, log *slog.Logger) (ctrl.Result, error) {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      providerDeploymentName(pp),
		Namespace: r.providerNamespace(pp),
	}, dep); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("cannot read deployment: %v", err))
	}

	ready := dep.Status.ReadyReplicas > 0
	if ready {
		if pp.Status.Phase == "Healthy" {
			if err := r.healthCheckRegisteredProvider(ctx, pp); err != nil {
				return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider health check failed: %v", err))
			}
			observability.ProviderHealthy.WithLabelValues(pp.Name, r.providerNamespace(pp)).Set(1)
			// Already healthy — nothing to update.
			return ctrl.Result{RequeueAfter: requeueAfter * 1e9}, nil
		}
		if r.Registry != nil {
			providerPlugin, err := r.connectProvider(ctx, pp)
			if err != nil {
				return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider handshake failed: %v", err))
			}
			pp.Status.Capabilities = capabilitiesFromPlugin(providerPlugin)
			if providerPlugin.Name() != pp.Name {
				closeProviderPlugin(providerPlugin)
				return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider advertises name %q, which must match PlatformProvider metadata.name %q", providerPlugin.Name(), pp.Name))
			}
			if err := r.Registry.RegisterExternal(pp.Name, string(pp.UID), providerPlugin); err != nil {
				closeProviderPlugin(providerPlugin)
				return r.setUnhealthy(ctx, pp, fmt.Sprintf("provider registry update failed: %v", err))
			}
		}
		log.Info("provider deployment is ready — marking Healthy")
		pp.Status.Phase = "Healthy"
		observability.ProviderHealthy.WithLabelValues(pp.Name, r.providerNamespace(pp)).Set(1)
		setCondition(pp, "Ready", metav1.ConditionTrue, "DeploymentReady", "Provider deployment has ready replicas")
	} else {
		if r.Registry != nil {
			r.Registry.DeregisterExternal(pp.Name, string(pp.UID))
		}
		observability.ProviderHealthy.WithLabelValues(pp.Name, r.providerNamespace(pp)).Set(0)
		installing := pp.Status.Phase == "" || pp.Status.Phase == "Installing"
		if installing {
			log.Info("provider deployment not yet ready — Installing")
			pp.Status.Phase = "Installing"
			setCondition(pp, "Ready", metav1.ConditionFalse, "DeploymentNotReady", "Waiting for provider pod to become ready")
		} else {
			log.Info("provider deployment lost readiness — marking Unhealthy")
			pp.Status.Phase = "Unhealthy"
			setCondition(pp, "Ready", metav1.ConditionFalse, "DeploymentUnhealthy", "Provider deployment has no ready replicas")
		}
	}

	if err := r.Status().Update(ctx, pp); err != nil {
		return ctrl.Result{}, fmt.Errorf("update platformprovider status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueAfter * 1e9}, nil
}

// ---------------------------------------------------------------------------
// Deletion
// ---------------------------------------------------------------------------

func (r *PlatformProviderReconciler) reconcileDelete(ctx context.Context, pp *v1alpha1.PlatformProvider, log *slog.Logger) (ctrl.Result, error) {
	log.Info("reconciling deletion of platform provider")
	if r.Registry != nil {
		r.Registry.DeregisterExternal(pp.Name, string(pp.UID))
	}
	observability.ProviderHealthy.DeleteLabelValues(pp.Name, r.providerNamespace(pp))
	// Owned Deployment and Service are garbage-collected via ownerReferences.
	controllerutil.RemoveFinalizer(pp, providerFinalizerName)
	if err := r.Update(ctx, pp); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	log.Info("finalizer removed, platform provider deleted")
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (r *PlatformProviderReconciler) setUnhealthy(ctx context.Context, pp *v1alpha1.PlatformProvider, reason string) (ctrl.Result, error) {
	r.log.Error("platform provider unhealthy", slog.String("name", pp.Name), slog.String("reason", reason))
	if r.Registry != nil {
		r.Registry.DeregisterExternal(pp.Name, string(pp.UID))
	}
	observability.ProviderHealthy.WithLabelValues(pp.Name, r.providerNamespace(pp)).Set(0)
	pp.Status.Phase = "Unhealthy"
	setCondition(pp, "Ready", metav1.ConditionFalse, "Error", reason)
	if err := r.Status().Update(ctx, pp); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Unhealthy: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueAfter * 1e9}, nil
}

func providerDeploymentName(pp *v1alpha1.PlatformProvider) string {
	return fmt.Sprintf("provider-%s", pp.Name)
}

func providerServiceName(pp *v1alpha1.PlatformProvider) string {
	return fmt.Sprintf("provider-%s", pp.Name)
}

func (r *PlatformProviderReconciler) providerNamespace(pp *v1alpha1.PlatformProvider) string {
	if r.ProviderNamespace != "" {
		return r.ProviderNamespace
	}
	if pp.Namespace != "" {
		return pp.Namespace
	}
	return defaultProviderNamespace
}

func (r *PlatformProviderReconciler) connectProvider(ctx context.Context, pp *v1alpha1.PlatformProvider) (platform.Plugin, error) {
	address := fmt.Sprintf("%s.%s.svc:%d", providerServiceName(pp), r.providerNamespace(pp), providerGRPCPort)
	if r.ConnectProvider != nil {
		return r.ConnectProvider(ctx, address)
	}

	tlsConfig, err := r.providerTLSConfig(ctx, pp)
	if err != nil {
		return nil, err
	}
	adapter := plugingrpc.NewAdapterWithTLS(address, tlsConfig)
	if err := adapter.Connect(ctx); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (r *PlatformProviderReconciler) providerTLSConfig(ctx context.Context, pp *v1alpha1.PlatformProvider) (*plugingrpc.ProviderTLSConfig, error) {
	if pp.Spec.Transport == nil || pp.Spec.Transport.TLS == nil ||
		pp.Spec.Transport.TLS.Mode == "" || pp.Spec.Transport.TLS.Mode == "Disabled" {
		return nil, nil
	}
	tlsSpec := pp.Spec.Transport.TLS
	if tlsSpec.Mode != "Mutual" {
		return nil, fmt.Errorf("unsupported provider transport tls mode %q", tlsSpec.Mode)
	}
	if tlsSpec.CASecretRef == nil {
		return nil, fmt.Errorf("provider mTLS requires spec.transport.tls.caSecretRef")
	}
	if tlsSpec.ClientCertificateSecretRef == nil {
		return nil, fmt.Errorf("provider mTLS requires spec.transport.tls.clientCertificateSecretRef")
	}
	ca, err := r.secretData(ctx, tlsSpec.CASecretRef, providerTLSCAKey(tlsSpec.CASecretRef))
	if err != nil {
		return nil, fmt.Errorf("load provider mTLS CA bundle: %w", err)
	}
	cert, err := r.secretData(ctx, tlsSpec.ClientCertificateSecretRef, providerTLSCertKey(tlsSpec.ClientCertificateSecretRef))
	if err != nil {
		return nil, fmt.Errorf("load provider mTLS client certificate: %w", err)
	}
	key, err := r.secretData(ctx, tlsSpec.ClientCertificateSecretRef, providerTLSKeyKey(tlsSpec.ClientCertificateSecretRef))
	if err != nil {
		return nil, fmt.Errorf("load provider mTLS client key: %w", err)
	}
	serverName := tlsSpec.ServerName
	if serverName == "" {
		serverName = fmt.Sprintf("%s.%s.svc", providerServiceName(pp), r.providerNamespace(pp))
	}
	return &plugingrpc.ProviderTLSConfig{
		ServerName: serverName,
		CABundle:   ca,
		ClientCert: cert,
		ClientKey:  key,
	}, nil
}

func (r *PlatformProviderReconciler) secretData(ctx context.Context, ref *v1alpha1.ProviderTLSSecretRef, key string) ([]byte, error) {
	if ref == nil {
		return nil, fmt.Errorf("secret reference is required")
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, secret); err != nil {
		return nil, err
	}
	value := secret.Data[key]
	if len(value) == 0 {
		return nil, fmt.Errorf("secret %s/%s key %q is empty or missing", ref.Namespace, ref.Name, key)
	}
	return value, nil
}

func providerTLSCAKey(ref *v1alpha1.ProviderTLSSecretRef) string {
	if ref.CAKey != "" {
		return ref.CAKey
	}
	return "ca.crt"
}

func providerTLSCertKey(ref *v1alpha1.ProviderTLSSecretRef) string {
	if ref.CertKey != "" {
		return ref.CertKey
	}
	return "tls.crt"
}

func providerTLSKeyKey(ref *v1alpha1.ProviderTLSSecretRef) string {
	if ref.KeyKey != "" {
		return ref.KeyKey
	}
	return "tls.key"
}

func (r *PlatformProviderReconciler) healthCheckRegisteredProvider(ctx context.Context, pp *v1alpha1.PlatformProvider) error {
	if r.Registry == nil || pp.Status.Capabilities == nil || pp.Status.Capabilities.ProviderName == "" {
		return nil
	}
	if pp.Status.Capabilities.ProviderName != pp.Name {
		return fmt.Errorf("provider advertises name %q, which must match PlatformProvider metadata.name %q", pp.Status.Capabilities.ProviderName, pp.Name)
	}

	providerPlugin, err := r.Registry.External(pp.Name, string(pp.UID), pp.Status.Capabilities.ProviderName)
	if err != nil {
		providerPlugin, err = r.connectProvider(ctx, pp)
		if err != nil {
			return err
		}
		if providerPlugin.Name() != pp.Status.Capabilities.ProviderName {
			closeProviderPlugin(providerPlugin)
			return fmt.Errorf("provider capability name changed from %q to %q", pp.Status.Capabilities.ProviderName, providerPlugin.Name())
		}
		if err := r.Registry.RegisterExternal(pp.Name, string(pp.UID), providerPlugin); err != nil {
			closeProviderPlugin(providerPlugin)
			return err
		}
	}
	return providerPlugin.HealthCheck(ctx)
}

func closeProviderPlugin(providerPlugin platform.Plugin) {
	if closer, ok := providerPlugin.(platform.ClosePlugin); ok {
		_ = closer.Close()
	}
}

func capabilitiesFromPlugin(p platform.Plugin) *v1alpha1.ProviderCapabilities {
	formats := p.SupportedFormats()
	formatStrings := make([]string, 0, len(formats))
	for _, format := range formats {
		formatStrings = append(formatStrings, string(format))
	}

	families := p.SupportedOS()
	familyStrings := make([]string, 0, len(families))
	for _, family := range families {
		familyStrings = append(familyStrings, string(family))
	}

	return &v1alpha1.ProviderCapabilities{
		ProviderName:    p.Name(),
		ProviderVersion: p.Version(),
		Formats:         formatStrings,
		OSFamilies:      familyStrings,
		BuildModes:      buildModesFromPlugin(p),
		ProtocolVersion: platform.ProtocolVersionV1,
	}
}

func buildModesFromPlugin(p platform.Plugin) []string {
	modes := []string{v1alpha1.BuildModeLocal}
	if remote, ok := p.(platform.RemoteBuildPlugin); ok {
		seen := map[string]bool{v1alpha1.BuildModeLocal: true}
		for _, mode := range remote.SupportedBuildModes() {
			if mode == "" || seen[mode] {
				continue
			}
			seen[mode] = true
			modes = append(modes, mode)
		}
	}
	return modes
}

func (r *PlatformProviderReconciler) validateProviderPackagePolicy(pp *v1alpha1.PlatformProvider) error {
	if pp.Spec.Package == "" {
		return fmt.Errorf("spec.package is required")
	}
	if r.RequireDigest && !strings.Contains(pp.Spec.Package, "@sha256:") {
		return fmt.Errorf("spec.package must be pinned by digest by operator policy")
	}
	if r.RequireSignature && (pp.Spec.Security == nil || !pp.Spec.Security.VerifySignature) {
		return fmt.Errorf("spec.security.verifySignature is required by operator policy")
	}
	if len(r.AllowedRegistries) > 0 && !providerPackageAllowed(pp.Spec.Package, r.AllowedRegistries) {
		return fmt.Errorf("spec.package registry is not allowed by operator policy")
	}
	security := pp.Spec.Security
	if security == nil {
		return nil
	}
	if security.RequireDigest || security.VerifySignature {
		if !strings.Contains(pp.Spec.Package, "@sha256:") {
			return fmt.Errorf("spec.package must be pinned by digest when requireDigest or verifySignature is enabled")
		}
	}
	if len(security.AllowedRegistries) > 0 {
		if !providerPackageAllowed(pp.Spec.Package, security.AllowedRegistries) {
			return fmt.Errorf("spec.package registry is not in spec.security.allowedRegistries")
		}
	}
	return nil
}

func providerPackageAllowed(image string, allowedRegistries []string) bool {
	for _, prefix := range allowedRegistries {
		prefix = strings.TrimSuffix(prefix, "/")
		if image == prefix || strings.HasPrefix(image, prefix+"/") {
			return true
		}
	}
	return false
}

func (r *PlatformProviderReconciler) validateProviderTransportPolicy(pp *v1alpha1.PlatformProvider) error {
	tlsSpec := providerMutualTLS(pp)
	if tlsSpec == nil {
		if pp.Spec.Transport != nil && pp.Spec.Transport.TLS != nil &&
			pp.Spec.Transport.TLS.Mode != "" && pp.Spec.Transport.TLS.Mode != "Disabled" {
			return fmt.Errorf("spec.transport.tls.mode must be Disabled or Mutual")
		}
		if r.RequireMTLS {
			return fmt.Errorf("provider mTLS is required by operator policy; set spec.transport.tls.mode=Mutual")
		}
		return nil
	}
	if tlsSpec.CASecretRef == nil {
		return fmt.Errorf("spec.transport.tls.caSecretRef is required when mode=Mutual")
	}
	if tlsSpec.ClientCertificateSecretRef == nil {
		return fmt.Errorf("spec.transport.tls.clientCertificateSecretRef is required when mode=Mutual")
	}
	if tlsSpec.ServerCertificateSecretRef == nil {
		return fmt.Errorf("spec.transport.tls.serverCertificateSecretRef is required when mode=Mutual")
	}
	for field, ref := range map[string]*v1alpha1.ProviderTLSSecretRef{
		"caSecretRef":                tlsSpec.CASecretRef,
		"clientCertificateSecretRef": tlsSpec.ClientCertificateSecretRef,
		"serverCertificateSecretRef": tlsSpec.ServerCertificateSecretRef,
	} {
		if ref.Name == "" {
			return fmt.Errorf("spec.transport.tls.%s.name is required", field)
		}
		if ref.Namespace == "" {
			return fmt.Errorf("spec.transport.tls.%s.namespace is required", field)
		}
		if ref.Namespace != r.providerNamespace(pp) {
			return fmt.Errorf("spec.transport.tls.%s.namespace must match provider namespace %q", field, r.providerNamespace(pp))
		}
	}
	return nil
}

func (r *PlatformProviderReconciler) validateProviderServiceAccountPolicy(pp *v1alpha1.PlatformProvider) error {
	serviceAccount := strings.TrimSpace(pp.Spec.ServiceAccountName)
	if r.RestrictServiceAccounts && serviceAccount != "" {
		allowed := false
		for _, candidate := range r.AllowedServiceAccounts {
			if strings.TrimSpace(candidate) == serviceAccount {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("spec.serviceAccountName %q is not allowed", serviceAccount)
		}
	}
	if r.ForbidServiceAccountToken && pp.Spec.AutomountServiceAccountToken != nil && *pp.Spec.AutomountServiceAccountToken {
		return fmt.Errorf("spec.automountServiceAccountToken=true is forbidden")
	}
	return nil
}

func providerMutualTLS(pp *v1alpha1.PlatformProvider) *v1alpha1.ProviderTransportTLSSpec {
	if pp.Spec.Transport == nil || pp.Spec.Transport.TLS == nil {
		return nil
	}
	tlsSpec := pp.Spec.Transport.TLS
	if tlsSpec.Mode != "Mutual" {
		return nil
	}
	return tlsSpec
}

func setCondition(pp *v1alpha1.PlatformProvider, condType string, status metav1.ConditionStatus, reason, msg string) {
	now := metav1.Now()
	for i, c := range pp.Status.Conditions {
		if c.Type == condType {
			pp.Status.Conditions[i].Status = status
			pp.Status.Conditions[i].Reason = reason
			pp.Status.Conditions[i].Message = msg
			pp.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	pp.Status.Conditions = append(pp.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
	})
}

func boolPtr(b bool) *bool { return &b }
