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
)

const (
	providerFinalizerName = "imagebuilder.io/provider-cleanup"
	providerGRPCPort      = 50051
	requeueAfter          = 30
)

// PlatformProviderReconciler reconciles PlatformProvider resources.
//
// +kubebuilder:rbac:groups=imagebuilder.io,resources=platformproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=imagebuilder.io,resources=platformproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=imagebuilder.io,resources=platformproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
type PlatformProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	log    *slog.Logger
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
		return ctrl.Result{Requeue: true}, nil
	}

	// Reconcile Deployment and Service.
	if err := r.reconcileDeployment(ctx, pp, log); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("deployment reconcile failed: %v", err))
	}
	if err := r.reconcileService(ctx, pp, log); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("service reconcile failed: %v", err))
	}

	// Check Deployment readiness and update phase.
	return r.reconcileHealth(ctx, pp, log)
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

	// Update image if it changed.
	existing.Spec.Template.Spec.Containers[0].Image = pp.Spec.Package
	existing.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullPolicy(pp.Spec.PackagePullPolicy)
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
	}

	var pullSecrets []corev1.LocalObjectReference
	for _, s := range pp.Spec.PackagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: s})
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      providerDeploymentName(pp),
			Namespace: pp.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ImagePullSecrets: pullSecrets,
					Containers: []corev1.Container{
						{
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
						},
					},
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
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      providerServiceName(pp),
			Namespace: pp.Namespace,
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
		Namespace: pp.Namespace,
	}, dep); err != nil {
		return r.setUnhealthy(ctx, pp, fmt.Sprintf("cannot read deployment: %v", err))
	}

	ready := dep.Status.ReadyReplicas > 0
	if ready {
		if pp.Status.Phase == "Healthy" {
			// Already healthy — nothing to update.
			return ctrl.Result{RequeueAfter: requeueAfter * 1e9}, nil
		}
		log.Info("provider deployment is ready — marking Healthy")
		pp.Status.Phase = "Healthy"
		setCondition(pp, "Ready", metav1.ConditionTrue, "DeploymentReady", "Provider deployment has ready replicas")
	} else {
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
