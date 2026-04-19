// pkg/controller/vmimage/reconciler.go
//
// VMImage Reconciler — the core control loop.
//
// Reconciliation phases (FR-006, FR-007, FR-008):
//
//	Pending     → validate spec, check provider health
//	Building    → create Kubernetes Job (QEMU / diskimage-builder)
//	Provisioning → init-containers are running (managed by Kubernetes)
//	Uploading   → Job succeeded, upload artifact to each target platform
//	Ready       → all targets registered, write image refs to status
//	Failed      → any phase failed, cleanup partial uploads

package vmimage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/buildpod"
	"github.com/anwendt/imagebuilder/pkg/plugin"
)

const (
	finalizerName       = "imagebuilder.io/cleanup"
	defaultBuildTimeout = 2 * time.Hour
	requeueAfter        = 15 * time.Second
)

// VMImageReconciler reconciles VMImage resources.
//
// +kubebuilder:rbac:groups=imagebuilder.io,resources=vmimages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=imagebuilder.io,resources=vmimages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=imagebuilder.io,resources=vmimages/finalizers,verbs=update
// +kubebuilder:rbac:groups=imagebuilder.io,resources=providerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
type VMImageReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry *plugin.Registry
	log      *slog.Logger
}

func (r *VMImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.log = slog.Default().With(slog.String("controller", "vmimage"))
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.VMImage{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

// Reconcile is the main reconciliation loop. It is called whenever a VMImage
// resource changes or a Job owned by a VMImage changes.
func (r *VMImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.log == nil {
		r.log = slog.Default().With(slog.String("controller", "vmimage"))
	}
	log := r.log.With(slog.String("name", req.Name), slog.String("namespace", req.Namespace))

	// 1. Fetch the VMImage resource.
	img := &v1alpha1.VMImage{}
	if err := r.Get(ctx, req.NamespacedName, img); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get vmimage: %w", err)
	}

	// 2. Handle deletion — clean up the finalizer.
	if !img.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, img, log)
	}

	// 3. Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(img, finalizerName) {
		controllerutil.AddFinalizer(img, finalizerName)
		if err := r.Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Dispatch by current phase.
	switch img.Status.Phase {
	case "", v1alpha1.PhasePending:
		return r.reconcilePending(ctx, img, log)
	case v1alpha1.PhaseBuilding, v1alpha1.PhaseProvisioning:
		return r.reconcileBuilding(ctx, img, log)
	case v1alpha1.PhaseUploading:
		return r.reconcileUploading(ctx, img, log)
	case v1alpha1.PhaseReady, v1alpha1.PhaseFailed:
		// Terminal states — no further action unless spec changes (handled by re-creation).
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unknown phase %q", img.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Phase: Pending — validate and create the build Job
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcilePending(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	log.Info("reconciling pending vmimage")

	// Validate all referenced providers are healthy.
	for _, target := range img.Spec.Targets {
		providerName, err := r.providerNameForTarget(ctx, img.Namespace, target)
		if err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("provider lookup failed: %v", err))
		}
		if !r.Registry.Supports(providerName) {
			return r.setFailed(ctx, img, fmt.Sprintf("provider %q is not installed or not healthy", providerName))
		}
	}

	// Assemble and create the build Job.
	job, err := buildpod.Assemble(img, r.Scheme)
	if err != nil {
		return r.setFailed(ctx, img, fmt.Sprintf("assemble build job: %v", err))
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create build job: %w", err)
	}

	now := metav1.Now()
	img.Status.Phase = v1alpha1.PhaseBuilding
	img.Status.StartTime = &now
	jobName := job.Name
	img.Status.BuildJobRef = &jobName
	setCondition(img, "BuildStarted", metav1.ConditionTrue, "BuildJobCreated", "Build Job created successfully")
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Building: %w", err)
	}

	log.Info("build job created", slog.String("job", job.Name))
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// ---------------------------------------------------------------------------
// Phase: Building / Provisioning — monitor Job progress
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcileBuilding(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	if img.Status.BuildJobRef == nil {
		return r.setFailed(ctx, img, "build job reference missing from status")
	}

	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      *img.Status.BuildJobRef,
		Namespace: img.Namespace,
	}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setFailed(ctx, img, "build job disappeared unexpectedly")
		}
		return ctrl.Result{}, fmt.Errorf("get build job: %w", err)
	}

	// Update phase to Provisioning if init containers are running.
	if img.Status.Phase == v1alpha1.PhaseBuilding && jobHasActiveInitContainers(job) {
		img.Status.Phase = v1alpha1.PhaseProvisioning
		setCondition(img, "Provisioning", metav1.ConditionTrue, "InitContainersRunning", "Provisioner init containers are executing")
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status to Provisioning: %w", err)
		}
	}

	// Check for Job completion.
	if isJobSucceeded(job) {
		log.Info("build job succeeded, moving to Uploading")
		img.Status.Phase = v1alpha1.PhaseUploading
		setCondition(img, "BuildComplete", metav1.ConditionTrue, "JobSucceeded", "Build Job completed successfully")
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status to Uploading: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if isJobFailed(job) {
		return r.setFailed(ctx, img, "build Job failed — check Job logs for details")
	}

	// Check timeout.
	timeout := defaultBuildTimeout
	if img.Spec.Build.Timeout != nil {
		timeout = img.Spec.Build.Timeout.Duration
	}
	if img.Status.StartTime != nil && time.Since(img.Status.StartTime.Time) > timeout {
		return r.setFailed(ctx, img, fmt.Sprintf("build timed out after %s", timeout))
	}

	log.Info("build job in progress", slog.String("job", job.Name))
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// ---------------------------------------------------------------------------
// Phase: Uploading — delegate to provider plugins
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcileUploading(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	log.Info("reconciling upload phase")

	// Upload is performed by the build Job's main container.
	// Here the reconciler reads the job's output annotation / status to get image refs.
	// For now: transition to Ready if job succeeded (upload was part of the job).
	if img.Status.BuildJobRef != nil {
		job := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      *img.Status.BuildJobRef,
			Namespace: img.Namespace,
		}, job); err == nil && isJobSucceeded(job) {
			now := metav1.Now()
			img.Status.Phase = v1alpha1.PhaseReady
			img.Status.CompletionTime = &now
			setCondition(img, "Ready", metav1.ConditionTrue, "AllTargetsRegistered", "Image registered on all target platforms")
			if err := r.Status().Update(ctx, img); err != nil {
				return ctrl.Result{}, fmt.Errorf("update status to Ready: %w", err)
			}
			log.Info("vmimage is ready")
			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// ---------------------------------------------------------------------------
// Deletion / cleanup
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcileDelete(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	log.Info("reconciling deletion")

	// Owned Jobs are garbage-collected automatically by Kubernetes via ownerReferences.
	// Nothing else to clean up at the operator level (cloud artifacts are cleaned by the Job).

	controllerutil.RemoveFinalizer(img, finalizerName)
	if err := r.Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	log.Info("finalizer removed, vmimage deleted")
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) setFailed(ctx context.Context, img *v1alpha1.VMImage, reason string) (ctrl.Result, error) {
	r.log.Error("vmimage failed", slog.String("name", img.Name), slog.String("reason", reason))
	now := metav1.Now()
	img.Status.Phase = v1alpha1.PhaseFailed
	img.Status.CompletionTime = &now
	setCondition(img, "Failed", metav1.ConditionTrue, "Error", reason)
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Failed: %w", err)
	}
	return ctrl.Result{}, nil
}

// providerNameForTarget resolves the provider name for a target by reading the ProviderConfig.
func (r *VMImageReconciler) providerNameForTarget(ctx context.Context, namespace string, target v1alpha1.TargetSpec) (string, error) {
	cfg := &v1alpha1.ProviderConfig{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      target.ProviderConfigRef.Name,
		Namespace: namespace, // AS-005: same namespace only
	}, cfg); err != nil {
		return "", fmt.Errorf("get ProviderConfig %q: %w", target.ProviderConfigRef.Name, err)
	}
	return cfg.Spec.Provider, nil
}

func setCondition(img *v1alpha1.VMImage, condType string, status metav1.ConditionStatus, reason, msg string) {
	now := metav1.Now()
	for i, c := range img.Status.Conditions {
		if c.Type == condType {
			img.Status.Conditions[i].Status = status
			img.Status.Conditions[i].Reason = reason
			img.Status.Conditions[i].Message = msg
			img.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	img.Status.Conditions = append(img.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
	})
}

func isJobSucceeded(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func isJobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func jobHasActiveInitContainers(job *batchv1.Job) bool {
	return job.Status.Active > 0
}
