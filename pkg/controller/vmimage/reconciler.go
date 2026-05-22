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
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/buildpod"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
	"github.com/anwendt/imagebuilder/pkg/observability"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

const (
	finalizerName       = "imagebuilder.io/cleanup"
	defaultBuildTimeout = 2 * time.Hour
	requeueAfter        = 15 * time.Second
)

// VMImageReconciler reconciles VMImage resources.
//
// +kubebuilder:rbac:groups=imagebuilder.io,resources=vmimages,verbs=get;list;watch
// +kubebuilder:rbac:groups=imagebuilder.io,resources=vmimages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=imagebuilder.io,resources=vmimages/finalizers,verbs=update
// +kubebuilder:rbac:groups=imagebuilder.io,resources=providerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get
type VMImageReconciler struct {
	client.Client
	Scheme                     *runtime.Scheme
	Registry                   *plugin.Registry
	Recorder                   record.EventRecorder
	MaxConcurrentBuilds        int
	MaxConcurrentBuildsPerNode int
	SchedulerNamespace         string
	log                        *slog.Logger
}

func (r *VMImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.log = slog.Default().With(slog.String("controller", "vmimage"))
	//lint:ignore SA1019 record.EventRecorder is still used by the controller tests and event helper.
	r.Recorder = mgr.GetEventRecorderFor("vmimage-controller")
	if err := coordinationv1.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("add coordination scheme: %w", err)
	}
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
	log = log.With(slog.String("buildID", string(img.UID)), slog.String("phase", string(img.Status.Phase)))

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
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}

	// 4. Dispatch by current phase.
	switch img.Status.Phase {
	case "", v1alpha1.PhasePending:
		return r.reconcilePending(ctx, img, log)
	case v1alpha1.PhaseBuilding, v1alpha1.PhaseProvisioning:
		if buildMode(img) == v1alpha1.BuildModeRemote {
			return r.reconcileRemoteBuild(ctx, img, log)
		}
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
		providerPlugin, err := r.Registry.Get(providerName)
		if err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("provider %q is not installed or not healthy: %v", providerName, err))
		}
		if err := r.initProviderForTarget(ctx, img.Namespace, target, providerPlugin); err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("provider %q config %q rejected: %v", providerName, target.ProviderConfigRef.Name, err))
		}
		if err := providerPlugin.Validate(ctx, target); err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("provider %q rejected target %q: %v", providerName, target.ProviderConfigRef.Name, err))
		}
	}
	if buildMode(img) == v1alpha1.BuildModeRemote {
		return r.reconcileRemoteBuild(ctx, img, log)
	}

	if err := r.ensureArtifactStorage(ctx, img); err != nil {
		return r.setFailed(ctx, img, fmt.Sprintf("prepare artifact storage: %v", err))
	}

	acquisition, err := r.acquireBuildSlots(ctx, img)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !acquisition.Acquired {
		setStep(img, "Build", "Pending", "BuildQueued", acquisition.Reason, "")
		setCondition(img, "Queued", metav1.ConditionTrue, "BuildConcurrencyLimitReached", acquisition.Reason)
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status to queued: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "BuildQueued", "%s", acquisition.Reason)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	img.Status.BuildLeaseRefs = acquisition.Refs
	img.Status.ScheduledNodeName = acquisition.NodeName

	// Assemble and create the build Job.
	job, err := buildpod.Assemble(img, r.Scheme)
	if err != nil {
		_ = r.releaseBuildSlots(ctx, acquisition.Refs, leaseHolder(img))
		return r.setFailed(ctx, img, fmt.Sprintf("assemble build job: %v", err))
	}
	createdJob := false
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		_ = r.releaseBuildSlots(ctx, acquisition.Refs, leaseHolder(img))
		return ctrl.Result{}, fmt.Errorf("create build job: %w", err)
	} else if err == nil {
		createdJob = true
	}

	now := metav1.Now()
	img.Status.Phase = v1alpha1.PhaseBuilding
	img.Status.StartTime = &now
	jobName := job.Name
	img.Status.BuildJobRef = &jobName
	setStep(img, "Build", "Running", "BuildJobCreated", "Build Job created successfully", "")
	setStep(img, "Boot", "Pending", "WaitingForBuild", "Guest boot has not started yet", "")
	setStep(img, "Readiness", "Pending", "WaitingForBoot", "Guest readiness has not started yet", "")
	setStep(img, "Provisioning", "Pending", "WaitingForReadiness", "Provisioning has not started yet", "")
	setStep(img, "Sanitization", "Pending", "WaitingForProvisioning", "Credential sanitization has not started yet", "")
	setStep(img, "Upload", "Pending", "WaitingForBuild", "Upload has not started yet", "")
	setCondition(img, "BuildStarted", metav1.ConditionTrue, "BuildJobCreated", "Build Job created successfully")
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Building: %w", err)
	}
	if createdJob {
		observability.QueueDurationSeconds.WithLabelValues(metricProvider(img), metricFormat(img)).Observe(time.Since(img.CreationTimestamp.Time).Seconds())
		observability.ActiveBuilds.WithLabelValues(metricProvider(img), metricFormat(img)).Inc()
	}

	r.recordEvent(img, corev1.EventTypeNormal, "BuildStarted", "Build Job %q created", job.Name)
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
		setStep(img, "Build", "Running", "BuildJobRunning", "Build Job is running", "")
		setStep(img, "Boot", "Running", "GuestBooting", "Guest boot/readiness phase is in progress", "")
		setStep(img, "Readiness", "Running", "WaitingForGuest", "Waiting for guest management endpoint", "")
		setStep(img, "Provisioning", "Running", "ProvisionerInitContainersRunning", "Provisioner init containers are executing", "")
		setCondition(img, "Provisioning", metav1.ConditionTrue, "InitContainersRunning", "Provisioner init containers are executing")
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status to Provisioning: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "ProvisioningStarted", "Provisioner init containers are executing in Job %q", job.Name)
	}

	// Check for Job completion.
	if isJobSucceeded(job) {
		log.Info("build job succeeded, moving to Uploading")
		artifact, err := r.buildArtifactFromJob(ctx, img.Namespace, job)
		if err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("read build result: %v", err))
		}
		img.Status.Phase = v1alpha1.PhaseUploading
		img.Status.BuildArtifact = artifact
		setStep(img, "Build", "Succeeded", "JobSucceeded", "Build Job completed successfully", "")
		setStep(img, "Boot", "Succeeded", "JobSucceeded", "Guest boot completed inside the build job", "")
		setStep(img, "Readiness", "Succeeded", "JobSucceeded", "Guest readiness completed inside the build job", "")
		if len(img.Spec.Provisioners) > 0 {
			img.Status.ProvisionerResultRef = "/workspace/provisioners-result.json"
			setStep(img, "Provisioning", "Succeeded", "JobSucceeded", "Provisioners completed inside the build job", "/workspace/provisioners-result.json")
		} else {
			setStep(img, "Provisioning", "Skipped", "NoProvisioners", "No provisioners configured", "")
		}
		if hasGeneratedGuestCredentials(img) {
			setStep(img, "Sanitization", "Succeeded", "JobSucceeded", "Generated guest credentials were sanitized inside the build job", "")
		} else {
			setStep(img, "Sanitization", "Skipped", "NoGeneratedCredentials", "No generated guest credentials configured", "")
		}
		setStep(img, "Upload", "Running", "WaitingForUploadJob", "Waiting for upload/register job", "")
		setCondition(img, "BuildComplete", metav1.ConditionTrue, "JobSucceeded", "Build Job completed successfully")
		observeBuildDuration(img, "Succeeded")
		observability.ActiveBuilds.WithLabelValues(metricProvider(img), metricFormat(img)).Dec()
		if err := r.releaseImageBuildSlots(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("release build slots: %w", err)
		}
		img.Status.BuildLeaseRefs = nil
		img.Status.ScheduledNodeName = ""
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status to Uploading: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "BuildComplete", "Build Job %q completed successfully", job.Name)
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}

	if isJobFailed(job) {
		reason := "BuildFailed"
		message := "build Job failed - check Job logs for details"
		if detail, err := r.buildFailureFromJob(ctx, img.Namespace, job); err == nil {
			if detail.Reason != "" {
				reason = detail.Reason
			}
			if detail.Error != "" {
				message = detail.Error
			}
		}
		setBuildFailureSteps(img, reason, message)
		observeBuildDuration(img, reason)
		observability.ActiveBuilds.WithLabelValues(metricProvider(img), metricFormat(img)).Dec()
		observability.FailuresTotal.WithLabelValues("Build", reason, metricProvider(img)).Inc()
		if err := r.releaseImageBuildSlots(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("release build slots after failure: %w", err)
		}
		img.Status.BuildLeaseRefs = nil
		img.Status.ScheduledNodeName = ""
		return r.setFailedWithReason(ctx, img, reason, message)
	}

	if err := r.renewImageBuildSlots(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("renew build leases: %w", err)
	}

	// Check timeout.
	timeout := defaultBuildTimeout
	if img.Spec.Build.Timeout != nil {
		timeout = img.Spec.Build.Timeout.Duration
	}
	if img.Status.StartTime != nil && time.Since(img.Status.StartTime.Time) > timeout {
		setStep(img, "Build", "Failed", "BuildTimedOut", fmt.Sprintf("Build timed out after %s", timeout), "")
		observability.ActiveBuilds.WithLabelValues(metricProvider(img), metricFormat(img)).Dec()
		if err := r.releaseImageBuildSlots(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("release build slots after timeout: %w", err)
		}
		img.Status.BuildLeaseRefs = nil
		img.Status.ScheduledNodeName = ""
		return r.setFailed(ctx, img, fmt.Sprintf("build timed out after %s", timeout))
	}

	log.Info("build job in progress", slog.String("job", job.Name))
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// ---------------------------------------------------------------------------
// Phase: Remote build — provider-owned build lifecycle
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcileRemoteBuild(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	if len(img.Spec.Targets) != 1 {
		return r.setFailedWithReason(ctx, img, "RemoteBuildInvalid", "remote build currently requires exactly one target")
	}

	target := img.Spec.Targets[0]
	providerName, err := r.providerNameForTarget(ctx, img.Namespace, target)
	if err != nil {
		return r.setFailed(ctx, img, fmt.Sprintf("provider lookup failed: %v", err))
	}
	providerPlugin, err := r.Registry.Get(providerName)
	if err != nil {
		return r.setFailedWithReason(ctx, img, "RemoteBuildUnsupported", fmt.Sprintf("provider %q is not installed or not healthy: %v", providerName, err))
	}
	remotePlugin, ok := providerPlugin.(platform.RemoteBuildPlugin)
	if !ok || !supportsBuildMode(remotePlugin.SupportedBuildModes(), v1alpha1.BuildModeRemote) {
		return r.setFailedWithReason(ctx, img, "RemoteBuildUnsupported", fmt.Sprintf("provider %q does not advertise remote build support", providerName))
	}
	if err := r.initProviderForTarget(ctx, img.Namespace, target, providerPlugin); err != nil {
		return r.setFailedWithReason(ctx, img, "RemoteBuildConfigInvalid", fmt.Sprintf("provider %q config %q rejected: %v", providerName, target.ProviderConfigRef.Name, err))
	}

	if img.Status.StartTime == nil {
		now := metav1.Now()
		img.Status.Phase = v1alpha1.PhaseBuilding
		img.Status.StartTime = &now
		setStep(img, "Build", "Running", "RemoteBuildStarted", "Remote build requested from provider", "")
		setStep(img, "Boot", "Pending", "WaitingForRemoteBuild", "Guest boot is provider-managed", "")
		setStep(img, "Readiness", "Pending", "WaitingForRemoteBuild", "Guest readiness is provider-managed", "")
		setStep(img, "Provisioning", "Pending", "WaitingForRemoteBuild", "Provisioning is provider-managed", "")
		setStep(img, "Sanitization", "Pending", "WaitingForRemoteBuild", "Sanitization is provider-managed", "")
		setStep(img, "Upload", "Pending", "WaitingForRemoteBuild", "Registration is provider-managed", "")
		setCondition(img, "BuildStarted", metav1.ConditionTrue, "RemoteBuildStarted", "Remote build requested from provider")
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status to remote building: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "RemoteBuildStarted", "Remote build requested from provider %q", providerName)
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}

	timeout := defaultBuildTimeout
	if img.Spec.Build.Timeout != nil {
		timeout = img.Spec.Build.Timeout.Duration
	}
	if time.Since(img.Status.StartTime.Time) > timeout {
		setRemoteFailureSteps(img, "RemoteBuildTimedOut", fmt.Sprintf("Remote build timed out after %s", timeout))
		observability.FailuresTotal.WithLabelValues("RemoteBuild", "RemoteBuildTimedOut", providerName).Inc()
		if err := r.cleanupRemoteBuild(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("cleanup remote build after timeout: %w", err)
		}
		return r.setFailedWithReason(ctx, img, "RemoteBuildTimedOut", fmt.Sprintf("remote build timed out after %s", timeout))
	}

	req, err := r.remoteBuildRequest(ctx, img, target)
	if err != nil {
		return r.setFailedWithReason(ctx, img, "RemoteBuildAuthFailed", err.Error())
	}
	result, err := remotePlugin.ReconcileRemoteBuild(ctx, req)
	if err != nil {
		setRemoteFailureSteps(img, "RemoteBuildFailed", err.Error())
		observability.FailuresTotal.WithLabelValues("RemoteBuild", "RemoteBuildFailed", providerName).Inc()
		if cleanupErr := r.cleanupRemoteBuild(ctx, img); cleanupErr != nil {
			return ctrl.Result{}, fmt.Errorf("cleanup remote build after provider failure: %w", cleanupErr)
		}
		return r.setFailedWithReason(ctx, img, "RemoteBuildFailed", fmt.Sprintf("provider %q remote build failed: %v", providerName, err))
	}
	if result == nil {
		return r.setFailedWithReason(ctx, img, "RemoteBuildFailed", fmt.Sprintf("provider %q returned an empty remote build result", providerName))
	}
	if result.OperationRef != "" && (img.Status.RemoteBuildRef == nil || *img.Status.RemoteBuildRef != result.OperationRef) {
		ref := result.OperationRef
		img.Status.RemoteBuildRef = &ref
	}

	updateRemoteSteps(img, result)
	if !result.Done {
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update remote build status: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "RemoteBuildProgress", "%s", remoteBuildMessage(result))
		log.Info("remote build in progress", slog.String("provider", providerName), slog.String("phase", string(result.Phase)))
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	images := remoteImageStatuses(result.Images, target, providerName)
	if len(images) == 0 {
		return r.setFailedWithReason(ctx, img, "RemoteBuildFailed", fmt.Sprintf("provider %q completed remote build without image references", providerName))
	}
	hygiene := remoteHygieneStatus(result.Hygiene)
	if hygiene.Status == "failed" {
		img.Status.HygieneResult = &hygiene
		setStep(img, "Sanitization", "Failed", "RemoteHygieneFailed", hygiene.Message, hygiene.ResultRef)
		observability.FailuresTotal.WithLabelValues("RemoteBuild", "RemoteHygieneFailed", providerName).Inc()
		if err := r.cleanupRemoteBuild(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("cleanup remote build after hygiene failure: %w", err)
		}
		return r.setFailedWithReason(ctx, img, "RemoteHygieneFailed", hygiene.Message)
	}
	now := metav1.Now()
	img.Status.Phase = v1alpha1.PhaseReady
	img.Status.CompletionTime = &now
	img.Status.Images = images
	img.Status.HygieneResult = &hygiene
	if result.Artifact != nil {
		img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
			Path:      result.Artifact.Path,
			Format:    string(result.Artifact.Format),
			Checksum:  result.Artifact.Checksum,
			SizeBytes: result.Artifact.SizeBytes,
			OS:        string(result.Artifact.OS),
			Metadata:  result.Artifact.Metadata,
		}
	}
	setStep(img, "Build", "Succeeded", "RemoteBuildSucceeded", "Remote build completed successfully", "")
	setStep(img, "Boot", "Succeeded", "RemoteBuildSucceeded", "Guest boot completed on provider", "")
	setStep(img, "Readiness", "Succeeded", "RemoteBuildSucceeded", "Guest readiness completed on provider", "")
	if len(img.Spec.Provisioners) > 0 {
		setStep(img, "Provisioning", "Succeeded", "RemoteBuildSucceeded", "Provisioning completed on provider", "")
	} else {
		setStep(img, "Provisioning", "Skipped", "NoProvisioners", "No provisioners configured", "")
	}
	if hygiene.Status == "passed" {
		setStep(img, "Sanitization", "Succeeded", "RemoteHygienePassed", hygiene.Message, hygiene.ResultRef)
	} else {
		setStep(img, "Sanitization", "Succeeded", "RemoteHygieneUnknown", hygiene.Message, hygiene.ResultRef)
	}
	setStep(img, "Upload", "Succeeded", "RemoteImageRegistered", "Remote image registered by provider", "")
	setCondition(img, "Ready", metav1.ConditionTrue, "RemoteBuildSucceeded", "Remote build completed successfully")
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update remote build ready status: %w", err)
	}
	r.recordEvent(img, corev1.EventTypeNormal, "Ready", "Remote image registered on provider %q", providerName)
	log.Info("remote vmimage is ready", slog.String("provider", providerName))
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------------------
// Phase: Uploading — delegate to provider plugins
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcileUploading(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	log.Info("reconciling upload phase")

	if img.Status.BuildArtifact == nil {
		return r.setFailed(ctx, img, "build artifact result missing from status")
	}
	if !usesArtifactPVC(img) {
		return r.setFailed(ctx, img, "artifactStorage.type=pvc is required for the upload job")
	}
	if img.Status.UploadJobRef == nil {
		configs, err := r.providerConfigsForTargets(ctx, img)
		if err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("provider lookup failed: %v", err))
		}
		job, err := uploadpod.Assemble(img, configs, r.Scheme)
		if err != nil {
			return r.setFailed(ctx, img, fmt.Sprintf("assemble upload job: %v", err))
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create upload job: %w", err)
		}
		jobName := job.Name
		img.Status.UploadJobRef = &jobName
		img.Status.UploadOperations = initialUploadOperations(configs, img.Spec.Targets)
		setStep(img, "Upload", "Running", "UploadJobCreated", "Upload Job created successfully", "")
		setCondition(img, "UploadStarted", metav1.ConditionTrue, "UploadJobCreated", "Upload Job created successfully")
		if err := r.Status().Update(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status with upload job ref: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "UploadStarted", "Upload Job %q created", job.Name)
		log.Info("upload job created", slog.String("job", job.Name))
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      *img.Status.UploadJobRef,
		Namespace: img.Namespace,
	}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setFailed(ctx, img, "upload job disappeared unexpectedly")
		}
		return ctrl.Result{}, fmt.Errorf("get upload job: %w", err)
	}
	if isJobFailed(job) {
		reason := "upload Job failed — check Job logs for details"
		if detail, err := r.uploadFailureFromJob(ctx, img.Namespace, job); err == nil && detail != "" {
			reason = fmt.Sprintf("upload Job failed: %s", detail)
		}
		img.Status.UploadOperations = markUploadOperations(img.Status.UploadOperations, "Failed", reason)
		setStep(img, "Upload", "Failed", "UploadJobFailed", reason, "")
		observeUploadDuration(img, "false")
		observability.FailuresTotal.WithLabelValues("Upload", "UploadJobFailed", metricProvider(img)).Inc()
		return r.setFailed(ctx, img, reason)
	}
	if !isJobSucceeded(job) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	images, operations, err := r.imageStatusesFromUploadJob(ctx, img.Namespace, job)
	if err != nil {
		return r.setFailed(ctx, img, fmt.Sprintf("read upload result: %v", err))
	}

	now := metav1.Now()
	img.Status.Phase = v1alpha1.PhaseReady
	img.Status.CompletionTime = &now
	img.Status.Images = images
	img.Status.UploadOperations = mergeUploadOperations(img.Status.UploadOperations, operations, images, "Succeeded", "Image registered")
	setStep(img, "Upload", "Succeeded", "AllTargetsRegistered", "Image registered on all target platforms", "")
	setCondition(img, "Ready", metav1.ConditionTrue, "AllTargetsRegistered", "Image registered on all target platforms")
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Ready: %w", err)
	}
	r.recordEvent(img, corev1.EventTypeNormal, "Ready", "Image registered on %d target platform(s)", len(images))
	if err := r.cleanupArtifactStorage(ctx, img, true); err != nil {
		return ctrl.Result{}, fmt.Errorf("cleanup artifact storage: %w", err)
	}
	log.Info("vmimage is ready")
	return ctrl.Result{}, nil
}

func (r *VMImageReconciler) ensureArtifactStorage(ctx context.Context, img *v1alpha1.VMImage) error {
	if !usesArtifactPVC(img) || usesExistingArtifactPVC(img) {
		return nil
	}
	pvc := artifactPVC(img)
	existing := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get workspace pvc: %w", err)
		}
		if shouldOwnerReferenceArtifactPVC(img) {
			if err := controllerutil.SetControllerReference(img, pvc, r.Scheme); err != nil {
				return fmt.Errorf("set workspace pvc owner reference: %w", err)
			}
		}
		if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create workspace pvc: %w", err)
		}
		return nil
	}
	return nil
}

func (r *VMImageReconciler) cleanupArtifactStorage(ctx context.Context, img *v1alpha1.VMImage, success bool) error {
	if !usesArtifactPVC(img) || usesExistingArtifactPVC(img) || shouldRetainArtifactPVC(img, success) {
		return nil
	}
	pvc := &corev1.PersistentVolumeClaim{}
	key := types.NamespacedName{Name: buildpod.WorkspaceClaimName(img), Namespace: img.Namespace}
	if err := r.Get(ctx, key, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		cleanupErr := fmt.Errorf("get workspace pvc for cleanup: %w", err)
		r.markCleanupFailure(ctx, img, "artifact-storage", "ArtifactStorageCleanupFailed", cleanupErr)
		return cleanupErr
	}
	if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		cleanupErr := fmt.Errorf("delete workspace pvc: %w", err)
		r.markCleanupFailure(ctx, img, "artifact-storage", "ArtifactStorageCleanupFailed", cleanupErr)
		return cleanupErr
	}
	return nil
}

func artifactPVC(img *v1alpha1.VMImage) *corev1.PersistentVolumeClaim {
	quantity := resource.MustParse(artifactPVCSize(img))
	accessMode := corev1.PersistentVolumeAccessMode(artifactPVCAccessMode(img))
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildpod.WorkspaceClaimName(img),
			Namespace: img.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "imagebuilder",
				"imagebuilder.io/vmimage":      img.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: quantity,
				},
			},
		},
	}
	if img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.PVC != nil &&
		img.Spec.Build.ArtifactStorage.PVC.StorageClassName != nil {
		pvc.Spec.StorageClassName = img.Spec.Build.ArtifactStorage.PVC.StorageClassName
	}
	return pvc
}

func artifactPVCSize(img *v1alpha1.VMImage) string {
	if img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.PVC != nil &&
		img.Spec.Build.ArtifactStorage.PVC.Size != "" {
		return img.Spec.Build.ArtifactStorage.PVC.Size
	}
	if img.Spec.Build.Resources != nil && img.Spec.Build.Resources.Storage != "" {
		return img.Spec.Build.Resources.Storage
	}
	return "20Gi"
}

func artifactPVCAccessMode(img *v1alpha1.VMImage) string {
	if img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.PVC != nil &&
		img.Spec.Build.ArtifactStorage.PVC.AccessMode != "" {
		return img.Spec.Build.ArtifactStorage.PVC.AccessMode
	}
	return string(corev1.ReadWriteOnce)
}

func usesArtifactPVC(img *v1alpha1.VMImage) bool {
	return img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.Type == "pvc"
}

func usesExistingArtifactPVC(img *v1alpha1.VMImage) bool {
	return usesArtifactPVC(img) &&
		img.Spec.Build.ArtifactStorage.PVC != nil &&
		img.Spec.Build.ArtifactStorage.PVC.ClaimName != ""
}

func shouldOwnerReferenceArtifactPVC(img *v1alpha1.VMImage) bool {
	return artifactPVCRetainPolicy(img) == "Never"
}

func shouldRetainArtifactPVC(img *v1alpha1.VMImage, success bool) bool {
	switch artifactPVCRetainPolicy(img) {
	case "Always":
		return true
	case "OnFailure":
		return !success
	default:
		return false
	}
}

func artifactPVCRetainPolicy(img *v1alpha1.VMImage) string {
	if img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.PVC != nil &&
		img.Spec.Build.ArtifactStorage.PVC.RetainPolicy != "" {
		return img.Spec.Build.ArtifactStorage.PVC.RetainPolicy
	}
	return "Never"
}

func (r *VMImageReconciler) providerConfigsForTargets(ctx context.Context, img *v1alpha1.VMImage) ([]v1alpha1.ProviderConfig, error) {
	configs := make([]v1alpha1.ProviderConfig, 0, len(img.Spec.Targets))
	seen := map[string]bool{}
	for _, target := range img.Spec.Targets {
		name := target.ProviderConfigRef.Name
		if seen[name] {
			continue
		}
		seen[name] = true
		cfg := &v1alpha1.ProviderConfig{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: img.Namespace}, cfg); err != nil {
			return nil, fmt.Errorf("get ProviderConfig %q: %w", name, err)
		}
		configs = append(configs, *cfg)
	}
	return configs, nil
}

func (r *VMImageReconciler) initProviderForTarget(ctx context.Context, namespace string, target v1alpha1.TargetSpec, providerPlugin platform.Plugin) error {
	cfg := &v1alpha1.ProviderConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: target.ProviderConfigRef.Name, Namespace: namespace}, cfg); err != nil {
		return fmt.Errorf("get ProviderConfig %q: %w", target.ProviderConfigRef.Name, err)
	}
	secretData, err := r.providerSecretData(ctx, cfg)
	if err != nil {
		return err
	}
	return providerPlugin.Init(ctx, platform.PluginConfig{
		ProviderConfigName: cfg.Name,
		SecretData:         secretData,
		Region:             cfg.Spec.Region,
		Endpoint:           cfg.Spec.Endpoint,
		Insecure:           cfg.Spec.Insecure,
		Extra:              cfg.Spec.Extra,
	})
}

func (r *VMImageReconciler) providerSecretData(ctx context.Context, cfg *v1alpha1.ProviderConfig) (map[string][]byte, error) {
	ref := cfg.Spec.Credentials.SecretRef
	if ref.Name == "" {
		return nil, nil
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: cfg.Namespace}, secret); err != nil {
		return nil, fmt.Errorf("get credentials Secret %q for ProviderConfig %q: %w", ref.Name, cfg.Name, err)
	}
	if ref.Key == "" {
		out := make(map[string][]byte, len(secret.Data))
		for key, value := range secret.Data {
			out[key] = append([]byte(nil), value...)
		}
		return out, nil
	}
	value, ok := secret.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("credentials Secret %q missing key %q", ref.Name, ref.Key)
	}
	if expanded, ok := expandProviderCredentialJSON(value); ok {
		return expanded, nil
	}
	return map[string][]byte{ref.Key: append([]byte(nil), value...)}, nil
}

func expandProviderCredentialJSON(raw []byte) (map[string][]byte, bool) {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return nil, false
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) == 0 {
		return nil, false
	}
	out := make(map[string][]byte, len(values))
	for key, value := range values {
		out[key] = []byte(value)
	}
	return out, true
}

func (r *VMImageReconciler) uploadFailureFromJob(ctx context.Context, namespace string, job *batchv1.Job) (string, error) {
	message, err := r.terminatedContainerMessage(ctx, namespace, job, "upload")
	if err != nil {
		return "", err
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err == nil && payload.Error != "" {
		return payload.Error, nil
	}
	return message, nil
}

func (r *VMImageReconciler) imageStatusesFromUploadJob(ctx context.Context, namespace string, job *batchv1.Job) ([]v1alpha1.ImageStatus, []v1alpha1.UploadOperationStatus, error) {
	message, err := r.terminatedContainerMessage(ctx, namespace, job, "upload")
	if err != nil {
		return nil, nil, err
	}
	var images []v1alpha1.ImageStatus
	var operations []v1alpha1.UploadOperationStatus
	var payload struct {
		Images     []v1alpha1.ImageStatus           `json:"images"`
		Operations []v1alpha1.UploadOperationStatus `json:"operations,omitempty"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err == nil && len(payload.Images) > 0 {
		images = payload.Images
		operations = payload.Operations
		for _, op := range payload.Operations {
			if op.UploadMilliseconds > 0 {
				observability.UploadDurationSeconds.WithLabelValues(op.Provider, op.Format, "true").Observe(float64(op.UploadMilliseconds) / 1000)
			}
			if op.UploadBytes > 0 {
				observability.UploadBytesTotal.WithLabelValues(op.Provider, op.Format).Add(float64(op.UploadBytes))
				if op.UploadMilliseconds > 0 {
					observability.UploadThroughputBytesPerSecond.WithLabelValues(op.Provider, op.Format).Observe(float64(op.UploadBytes) / (float64(op.UploadMilliseconds) / 1000))
				}
			}
			if op.RegisterMilliseconds > 0 {
				observability.RegisterDurationSeconds.WithLabelValues(op.Provider, op.Format, "true").Observe(float64(op.RegisterMilliseconds) / 1000)
			}
		}
	} else if err := json.Unmarshal([]byte(message), &images); err != nil {
		return nil, nil, fmt.Errorf("parse upload result: %w", err)
	}
	if len(images) == 0 {
		return nil, nil, fmt.Errorf("upload result contains no images")
	}
	for _, image := range images {
		if image.Provider == "" || image.ProviderConfig == "" || image.ImageRef == "" || image.Format == "" {
			return nil, nil, fmt.Errorf("upload result is incomplete")
		}
	}
	return images, operations, nil
}

func initialUploadOperations(configs []v1alpha1.ProviderConfig, targets []v1alpha1.TargetSpec) []v1alpha1.UploadOperationStatus {
	providers := make(map[string]string, len(configs))
	for _, cfg := range configs {
		providers[cfg.Name] = cfg.Spec.Provider
	}
	now := metav1.Now()
	operations := make([]v1alpha1.UploadOperationStatus, 0, len(targets))
	for _, target := range targets {
		providerConfig := target.ProviderConfigRef.Name
		operations = append(operations, v1alpha1.UploadOperationStatus{
			Provider:           providers[providerConfig],
			ProviderConfig:     providerConfig,
			Format:             target.Format,
			Phase:              "Uploading",
			Message:            "Upload Job created",
			LastTransitionTime: now,
		})
	}
	return operations
}

func markUploadOperations(operations []v1alpha1.UploadOperationStatus, phase, message string) []v1alpha1.UploadOperationStatus {
	now := metav1.Now()
	out := make([]v1alpha1.UploadOperationStatus, len(operations))
	copy(out, operations)
	for i := range out {
		if out[i].Phase != phase {
			out[i].LastTransitionTime = now
		}
		out[i].Phase = phase
		out[i].Message = message
	}
	return out
}

func mergeUploadOperations(existing, reported []v1alpha1.UploadOperationStatus, images []v1alpha1.ImageStatus, phase, message string) []v1alpha1.UploadOperationStatus {
	now := metav1.Now()
	byKey := make(map[string]int, len(existing)+len(reported)+len(images))
	out := make([]v1alpha1.UploadOperationStatus, 0, len(existing)+len(reported))
	for _, op := range existing {
		byKey[uploadOperationKey(op.ProviderConfig, op.Format)] = len(out)
		out = append(out, op)
	}
	upsert := func(op v1alpha1.UploadOperationStatus) {
		key := uploadOperationKey(op.ProviderConfig, op.Format)
		if idx, ok := byKey[key]; ok {
			if op.Provider != "" {
				out[idx].Provider = op.Provider
			}
			if op.Phase != "" {
				out[idx].Phase = op.Phase
			}
			if op.OperationRef != "" {
				out[idx].OperationRef = op.OperationRef
			}
			if op.ImageRef != "" {
				out[idx].ImageRef = op.ImageRef
			}
			if op.Message != "" {
				out[idx].Message = op.Message
			}
			if !op.LastTransitionTime.IsZero() {
				out[idx].LastTransitionTime = op.LastTransitionTime
			}
			if op.UploadMilliseconds > 0 {
				out[idx].UploadMilliseconds = op.UploadMilliseconds
			}
			if op.UploadBytes > 0 {
				out[idx].UploadBytes = op.UploadBytes
			}
			if op.RegisterMilliseconds > 0 {
				out[idx].RegisterMilliseconds = op.RegisterMilliseconds
			}
			return
		}
		if op.LastTransitionTime.IsZero() {
			op.LastTransitionTime = now
		}
		byKey[key] = len(out)
		out = append(out, op)
	}
	for _, op := range reported {
		if op.Phase == "" {
			op.Phase = phase
		}
		if op.Message == "" {
			op.Message = message
		}
		upsert(op)
	}
	for _, image := range images {
		op := v1alpha1.UploadOperationStatus{
			Provider:           image.Provider,
			ProviderConfig:     image.ProviderConfig,
			Format:             image.Format,
			Phase:              phase,
			ImageRef:           image.ImageRef,
			Message:            message,
			LastTransitionTime: now,
		}
		upsert(op)
	}
	return out
}

func uploadOperationKey(providerConfig, format string) string {
	return providerConfig + "\x00" + format
}

func (r *VMImageReconciler) buildArtifactFromJob(ctx context.Context, namespace string, job *batchv1.Job) (*v1alpha1.ArtifactStatus, error) {
	message, err := r.terminatedContainerMessage(ctx, namespace, job, "build")
	if err != nil {
		return nil, err
	}
	var payload struct {
		v1alpha1.ArtifactStatus
		Provisioners []struct {
			Type     string  `json:"type"`
			Success  bool    `json:"success"`
			Duration float64 `json:"durationSeconds"`
		} `json:"provisioners,omitempty"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return nil, fmt.Errorf("parse build result: %w", err)
	}
	artifact := &payload.ArtifactStatus
	if artifact.Path == "" || artifact.Format == "" || artifact.Checksum == "" || artifact.SizeBytes <= 0 || artifact.OS == "" {
		return nil, fmt.Errorf("build result is incomplete")
	}
	for _, step := range payload.Provisioners {
		if step.Duration > 0 {
			success := "false"
			if step.Success {
				success = "true"
			}
			observability.ProvisionerDurationSeconds.WithLabelValues(step.Type, success).Observe(step.Duration)
		}
	}
	return artifact, nil
}

type buildFailureDetail struct {
	Reason string `json:"reason"`
	Error  string `json:"error"`
}

func (r *VMImageReconciler) buildFailureFromJob(ctx context.Context, namespace string, job *batchv1.Job) (buildFailureDetail, error) {
	message, err := r.terminatedContainerMessage(ctx, namespace, job, "build")
	if err != nil {
		return buildFailureDetail{}, err
	}
	var detail buildFailureDetail
	if err := json.Unmarshal([]byte(message), &detail); err == nil && (detail.Reason != "" || detail.Error != "") {
		if detail.Reason == "" {
			detail.Reason = "BuildFailed"
		}
		return detail, nil
	}
	return buildFailureDetail{Reason: "BuildFailed", Error: message}, nil
}

func (r *VMImageReconciler) terminatedContainerMessage(ctx context.Context, namespace string, job *batchv1.Job, containerName string) (string, error) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name},
	); err != nil {
		return "", fmt.Errorf("list build pods: %w", err)
	}
	if len(pods.Items) == 0 {
		if err := r.List(ctx, pods,
			client.InNamespace(namespace),
			client.MatchingLabels{"job-name": job.Name},
		); err != nil {
			return "", fmt.Errorf("list legacy build pods: %w", err)
		}
	}

	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != containerName {
				continue
			}
			if status.State.Terminated == nil {
				continue
			}
			message := status.State.Terminated.Message
			if message == "" {
				return "", fmt.Errorf("%s container termination message is empty", containerName)
			}
			return message, nil
		}
	}
	return "", fmt.Errorf("%s pod result not found", containerName)
}

// ---------------------------------------------------------------------------
// Deletion / cleanup
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) reconcileDelete(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	log.Info("reconciling deletion")

	if buildMode(img) == v1alpha1.BuildModeRemote {
		if err := r.cleanupRemoteBuild(ctx, img); err != nil {
			return ctrl.Result{}, fmt.Errorf("cleanup remote build during deletion: %w", err)
		}
	} else if img.Status.Phase == v1alpha1.PhaseUploading {
		done, err := r.cleanupLocalUpload(ctx, img)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("cleanup local upload during deletion: %w", err)
		}
		if !done {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}
	if err := r.releaseImageBuildSlots(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("release build slots during deletion: %w", err)
	}

	controllerutil.RemoveFinalizer(img, finalizerName)
	if err := r.Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	log.Info("finalizer removed, vmimage deleted")
	return ctrl.Result{}, nil
}

func (r *VMImageReconciler) cleanupLocalUpload(ctx context.Context, img *v1alpha1.VMImage) (bool, error) {
	if img.Status.BuildArtifact == nil || !usesArtifactPVC(img) {
		return true, nil
	}
	if img.Status.UploadJobRef != nil {
		uploadJob := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{Name: *img.Status.UploadJobRef, Namespace: img.Namespace}, uploadJob)
		if err != nil && !apierrors.IsNotFound(err) {
			cleanupErr := fmt.Errorf("get upload job: %w", err)
			r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupFailed", cleanupErr)
			return false, cleanupErr
		}
		if err == nil && uploadJob.DeletionTimestamp.IsZero() && !isJobSucceeded(uploadJob) && !isJobFailed(uploadJob) {
			if err := r.Delete(ctx, uploadJob); err != nil && !apierrors.IsNotFound(err) {
				cleanupErr := fmt.Errorf("delete active upload job: %w", err)
				r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupFailed", cleanupErr)
				return false, cleanupErr
			}
			r.recordEvent(img, corev1.EventTypeNormal, "UploadCleanupPending", "Upload Job %q is being stopped before cleanup", uploadJob.Name)
			return false, nil
		}
		if err == nil && !uploadJob.DeletionTimestamp.IsZero() {
			return false, nil
		}
	}

	configs, err := r.providerConfigsForTargets(ctx, img)
	if err != nil {
		cleanupErr := fmt.Errorf("load provider configs for upload cleanup: %w", err)
		r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupFailed", cleanupErr)
		return false, cleanupErr
	}
	cleanupJobName := uploadpod.CleanupJobName(img)
	cleanupJob := &batchv1.Job{}
	err = r.Get(ctx, types.NamespacedName{Name: cleanupJobName, Namespace: img.Namespace}, cleanupJob)
	if apierrors.IsNotFound(err) {
		job, err := uploadpod.AssembleCleanup(img, configs, r.Scheme)
		if err != nil {
			cleanupErr := fmt.Errorf("assemble upload cleanup job: %w", err)
			r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupFailed", cleanupErr)
			return false, cleanupErr
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			cleanupErr := fmt.Errorf("create upload cleanup job: %w", err)
			r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupFailed", cleanupErr)
			return false, cleanupErr
		}
		r.recordEvent(img, corev1.EventTypeNormal, "UploadCleanupStarted", "Upload cleanup Job %q created", job.Name)
		return false, nil
	}
	if err != nil {
		cleanupErr := fmt.Errorf("get upload cleanup job: %w", err)
		r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupFailed", cleanupErr)
		return false, cleanupErr
	}
	if isJobSucceeded(cleanupJob) {
		r.recordEvent(img, corev1.EventTypeNormal, "UploadCleanupComplete", "Upload cleanup Job %q completed", cleanupJob.Name)
		return true, nil
	}
	if isJobFailed(cleanupJob) {
		observability.FailuresTotal.WithLabelValues("Cleanup", "UploadCleanupJobFailed", metricProvider(img)).Inc()
		cleanupErr := fmt.Errorf("upload cleanup Job %q failed", cleanupJob.Name)
		r.markCleanupFailure(ctx, img, "upload-register", "UploadCleanupJobFailed", cleanupErr)
		return false, cleanupErr
	}
	return false, nil
}

func (r *VMImageReconciler) cleanupRemoteBuild(ctx context.Context, img *v1alpha1.VMImage) error {
	if buildMode(img) != v1alpha1.BuildModeRemote || len(img.Spec.Targets) != 1 {
		return nil
	}
	target := img.Spec.Targets[0]
	providerName, err := r.providerNameForTarget(ctx, img.Namespace, target)
	if err != nil {
		cleanupErr := fmt.Errorf("provider lookup failed: %w", err)
		r.markCleanupFailure(ctx, img, "remote-build", "RemoteBuildCleanupFailed", cleanupErr)
		return cleanupErr
	}
	providerPlugin, err := r.Registry.Get(providerName)
	if err != nil {
		cleanupErr := fmt.Errorf("get provider %q: %w", providerName, err)
		r.markCleanupFailure(ctx, img, "remote-build", "RemoteBuildCleanupFailed", cleanupErr)
		return cleanupErr
	}
	cleanupPlugin, ok := providerPlugin.(platform.RemoteBuildCleanupPlugin)
	if !ok {
		return nil
	}
	if err := r.initProviderForTarget(ctx, img.Namespace, target, providerPlugin); err != nil {
		cleanupErr := fmt.Errorf("initialise provider %q for remote cleanup: %w", providerName, err)
		r.markCleanupFailure(ctx, img, "remote-build", "RemoteBuildCleanupFailed", cleanupErr)
		return cleanupErr
	}
	req := remoteBuildRequest(img, target)
	if err := cleanupPlugin.CleanupRemoteBuild(ctx, req); err != nil {
		cleanupErr := fmt.Errorf("provider remote cleanup: %w", err)
		r.markCleanupFailure(ctx, img, "remote-build", "RemoteBuildCleanupFailed", cleanupErr)
		return cleanupErr
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (r *VMImageReconciler) setFailed(ctx context.Context, img *v1alpha1.VMImage, reason string) (ctrl.Result, error) {
	return r.setFailedWithReason(ctx, img, "Error", reason)
}

func (r *VMImageReconciler) setFailedWithReason(ctx context.Context, img *v1alpha1.VMImage, conditionReason, message string) (ctrl.Result, error) {
	r.log.Error("vmimage failed", slog.String("name", img.Name), slog.String("reason", conditionReason), slog.String("message", message))
	now := metav1.Now()
	img.Status.Phase = v1alpha1.PhaseFailed
	img.Status.CompletionTime = &now
	if err := r.releaseImageBuildSlots(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("release build slots after failure: %w", err)
	}
	img.Status.BuildLeaseRefs = nil
	img.Status.ScheduledNodeName = ""
	setCondition(img, "Failed", metav1.ConditionTrue, conditionReason, message)
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Failed: %w", err)
	}
	r.recordEvent(img, corev1.EventTypeWarning, conditionReason, "%s", message)
	if err := r.cleanupArtifactStorage(ctx, img, false); err != nil {
		return ctrl.Result{}, fmt.Errorf("cleanup artifact storage after failure: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *VMImageReconciler) markCleanupFailure(ctx context.Context, img *v1alpha1.VMImage, scope, reason string, cleanupErr error) {
	message := cleanupErr.Error()
	provider := metricProvider(img)
	observability.CleanupFailuresTotal.WithLabelValues(scope, reason, provider).Inc()
	setStep(img, "Cleanup", "Failed", reason, message, "")
	setCondition(img, "CleanupFailed", metav1.ConditionTrue, reason, message)
	r.recordEvent(img, corev1.EventTypeWarning, reason, "%s", message)
	if err := r.Status().Update(ctx, img); err != nil {
		log := r.log
		if log == nil {
			log = slog.Default().With(slog.String("controller", "vmimage"))
		}
		log.Error("failed to update cleanup failure status",
			slog.String("name", img.Name),
			slog.String("namespace", img.Namespace),
			slog.String("cleanupScope", scope),
			slog.String("reason", reason),
			slog.String("error", err.Error()))
	}
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

func buildMode(img *v1alpha1.VMImage) string {
	if img.Spec.Build.Mode == "" {
		return v1alpha1.BuildModeLocal
	}
	return img.Spec.Build.Mode
}

func supportsBuildMode(modes []string, want string) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

func remoteBuildRequest(img *v1alpha1.VMImage, target v1alpha1.TargetSpec) *platform.RemoteBuildRequest {
	timeout := defaultBuildTimeout
	if img.Spec.Build.Timeout != nil {
		timeout = img.Spec.Build.Timeout.Duration
	}
	operationRef := ""
	if img.Status.RemoteBuildRef != nil {
		operationRef = *img.Status.RemoteBuildRef
	}
	return &platform.RemoteBuildRequest{
		BuildID:           string(img.UID),
		OperationRef:      operationRef,
		ImageName:         img.Name,
		Namespace:         img.Namespace,
		OSFamily:          platform.OSFamily(img.Spec.OS.Family),
		OSDistribution:    img.Spec.OS.Distribution,
		OSVersion:         img.Spec.OS.Version,
		OSArch:            img.Spec.OS.Arch,
		SourceType:        img.Spec.Source.Type,
		SourceURL:         img.Spec.Source.URL,
		SourceProviderRef: img.Spec.Source.ProviderRef,
		SourceChecksum:    img.Spec.Source.Checksum,
		Target:            target,
		Provisioners:      img.Spec.Provisioners,
		GuestAccess:       img.Spec.Build.GuestAccess,
		Timeout:           timeout,
	}
}

func (r *VMImageReconciler) remoteBuildRequest(ctx context.Context, img *v1alpha1.VMImage, target v1alpha1.TargetSpec) (*platform.RemoteBuildRequest, error) {
	req := remoteBuildRequest(img, target)
	provisioners, err := r.withRemoteGitAuth(ctx, img.Namespace, req.Provisioners)
	if err != nil {
		return nil, err
	}
	req.Provisioners = provisioners
	return req, nil
}

func (r *VMImageReconciler) withRemoteGitAuth(ctx context.Context, namespace string, specs []v1alpha1.ProvisionerSpec) ([]v1alpha1.ProvisionerSpec, error) {
	out := make([]v1alpha1.ProvisionerSpec, 0, len(specs))
	cache := map[string]*corev1.Secret{}
	for _, spec := range specs {
		spec = *spec.DeepCopy()
		if spec.Source == nil || spec.Source.Git == nil || spec.Source.Git.Auth == nil ||
			spec.Source.Git.Auth.SecretRef == nil || spec.Source.Git.Auth.SecretRef.Name == "" {
			out = append(out, spec)
			continue
		}
		ref := spec.Source.Git.Auth.SecretRef
		secret, ok := cache[ref.Name]
		if !ok {
			secret = &corev1.Secret{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, secret); err != nil {
				return nil, fmt.Errorf("read git provisioner auth secret %q: %w", ref.Name, err)
			}
			cache[ref.Name] = secret
		}
		spec.Source.Git.Auth.RuntimeToken = string(secret.Data[gitAuthTokenKey(ref)])
		spec.Source.Git.Auth.RuntimeUsername = string(secret.Data[gitAuthUsernameKey(ref)])
		spec.Source.Git.Auth.RuntimePassword = string(secret.Data[gitAuthPasswordKey(ref)])
		out = append(out, spec)
	}
	return out, nil
}

func gitAuthTokenKey(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	if ref.TokenKey != "" {
		return ref.TokenKey
	}
	return "token"
}

func gitAuthUsernameKey(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	if ref.UsernameKey != "" {
		return ref.UsernameKey
	}
	return "username"
}

func gitAuthPasswordKey(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	if ref.PasswordKey != "" {
		return ref.PasswordKey
	}
	return "password"
}

func updateRemoteSteps(img *v1alpha1.VMImage, result *platform.RemoteBuildResult) {
	reason := "RemoteBuildProgress"
	message := remoteBuildMessage(result)
	switch result.Phase {
	case platform.RemoteBuildPhaseBooting:
		setStep(img, "Build", "Running", reason, message, "")
		setStep(img, "Boot", "Running", "RemoteGuestBooting", message, "")
	case platform.RemoteBuildPhaseReadiness:
		setStep(img, "Boot", "Succeeded", "RemoteGuestBooted", "Guest boot completed on provider", "")
		setStep(img, "Readiness", "Running", "RemoteGuestReadiness", message, "")
	case platform.RemoteBuildPhaseProvisioning:
		img.Status.Phase = v1alpha1.PhaseProvisioning
		setStep(img, "Readiness", "Succeeded", "RemoteGuestReady", "Guest readiness completed on provider", "")
		setStep(img, "Provisioning", "Running", "RemoteProvisioning", message, "")
	case platform.RemoteBuildPhaseSanitizing:
		setStep(img, "Provisioning", "Succeeded", "RemoteProvisioningSucceeded", "Provisioning completed on provider", "")
		setStep(img, "Sanitization", "Running", "RemoteSanitizing", message, "")
	case platform.RemoteBuildPhaseRegistering:
		setStep(img, "Sanitization", "Succeeded", "RemoteSanitizationSucceeded", "Sanitization completed on provider", "")
		setStep(img, "Upload", "Running", "RemoteRegistering", message, "")
	case platform.RemoteBuildPhaseReady:
		setStep(img, "Upload", "Succeeded", "RemoteImageRegistered", message, "")
	default:
		setStep(img, "Build", "Running", reason, message, "")
	}
}

func remoteBuildMessage(result *platform.RemoteBuildResult) string {
	if result.Message != "" {
		return result.Message
	}
	if result.Phase != "" {
		return fmt.Sprintf("Remote build phase %s", result.Phase)
	}
	return "Remote build is in progress"
}

func remoteHygieneStatus(result *platform.RemoteHygieneResult) v1alpha1.HygieneResultStatus {
	if result == nil {
		return v1alpha1.HygieneResultStatus{
			Status:  "unknown",
			Message: "Provider did not return a final image hygiene attestation",
		}
	}

	status := result.Status
	switch status {
	case "passed", "failed", "unknown":
	default:
		status = "unknown"
	}

	message := result.Message
	if message == "" {
		switch status {
		case "passed":
			message = "Provider attested final image hygiene checks passed"
		case "failed":
			message = "Provider reported final image hygiene checks failed"
		default:
			message = "Provider did not return a conclusive final image hygiene attestation"
		}
	}

	return v1alpha1.HygieneResultStatus{
		Status:    status,
		Message:   message,
		Checks:    append([]string(nil), result.Checks...),
		ResultRef: result.ResultRef,
	}
}

func setRemoteFailureSteps(img *v1alpha1.VMImage, reason, message string) {
	setStep(img, "Build", "Failed", reason, message, "")
	setStep(img, "Boot", "Failed", reason, message, "")
	setStep(img, "Readiness", "Failed", reason, message, "")
	setStep(img, "Provisioning", "Failed", reason, message, "")
	setStep(img, "Sanitization", "Failed", reason, message, "")
	setStep(img, "Upload", "Failed", reason, message, "")
}

func remoteImageStatuses(images []platform.RemoteImageRef, target v1alpha1.TargetSpec, defaultProvider string) []v1alpha1.ImageStatus {
	statuses := make([]v1alpha1.ImageStatus, 0, len(images))
	for _, image := range images {
		provider := image.Provider
		if provider == "" {
			provider = defaultProvider
		}
		providerConfig := image.ProviderConfig
		if providerConfig == "" {
			providerConfig = target.ProviderConfigRef.Name
		}
		format := image.Format
		if format == "" {
			format = platform.ImageFormat(target.Format)
		}
		if provider == "" || providerConfig == "" || image.ImageRef.ID == "" || format == "" {
			continue
		}
		statuses = append(statuses, v1alpha1.ImageStatus{
			Provider:       provider,
			ProviderConfig: providerConfig,
			ImageRef:       image.ImageRef.ID,
			Location:       image.ImageRef.Location,
			Format:         string(format),
			Checksum:       image.Checksum,
		})
	}
	return statuses
}

func (r *VMImageReconciler) recordEvent(img *v1alpha1.VMImage, eventType, reason, messageFmt string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(img, eventType, reason, messageFmt, args...)
}

func hasGeneratedGuestCredentials(img *v1alpha1.VMImage) bool {
	return img.Spec.Build.GuestAccess != nil &&
		img.Spec.Build.GuestAccess.Credentials != nil &&
		img.Spec.Build.GuestAccess.Credentials.Generate != nil
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

func setStep(img *v1alpha1.VMImage, name, status, reason, msg, resultRef string) {
	now := metav1.Now()
	for i, step := range img.Status.Steps {
		if step.Name == name {
			if step.Status != status {
				img.Status.Steps[i].LastTransitionTime = now
			}
			img.Status.Steps[i].Status = status
			img.Status.Steps[i].Reason = reason
			img.Status.Steps[i].Message = msg
			img.Status.Steps[i].ResultRef = resultRef
			return
		}
	}
	img.Status.Steps = append(img.Status.Steps, v1alpha1.PipelineStepStatus{
		Name:               name,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
		ResultRef:          resultRef,
	})
}

func setBuildFailureSteps(img *v1alpha1.VMImage, reason, message string) {
	switch reason {
	case "SourceFetchFailed":
		setStep(img, "Build", "Failed", reason, message, "")
		setStep(img, "Boot", "Skipped", "BuildFailed", "Source fetch failed before guest boot", "")
		setStep(img, "Readiness", "Skipped", "BuildFailed", "Source fetch failed before guest readiness", "")
		setStep(img, "Provisioning", "Skipped", "BuildFailed", "Source fetch failed before provisioning", "")
		setStep(img, "Sanitization", "Skipped", "BuildFailed", "Source fetch failed before sanitization", "")
	case "BootFailed":
		setStep(img, "Build", "Running", "BuildJobRunning", "Build Job reached guest boot", "")
		setStep(img, "Boot", "Failed", reason, message, "")
		setStep(img, "Readiness", "Skipped", "BootFailed", "Guest boot failed before readiness", "")
		setStep(img, "Provisioning", "Skipped", "BootFailed", "Guest boot failed before provisioning", "")
		setStep(img, "Sanitization", "Skipped", "BootFailed", "Guest boot failed before sanitization", "")
	case "GuestReadinessTimeout":
		setStep(img, "Build", "Running", "BuildJobRunning", "Build Job reached guest readiness", "")
		setStep(img, "Boot", "Succeeded", "GuestBooted", "Guest boot completed before readiness wait", "")
		setStep(img, "Readiness", "Failed", reason, message, "")
		setStep(img, "Provisioning", "Skipped", "ReadinessFailed", "Guest readiness failed before provisioning", "")
		setStep(img, "Sanitization", "Skipped", "ReadinessFailed", "Guest readiness failed before sanitization", "")
	case "ProvisionerFailed":
		setStep(img, "Build", "Running", "BuildJobRunning", "Build Job reached provisioning", "")
		setStep(img, "Boot", "Succeeded", "GuestBooted", "Guest boot completed before provisioning", "")
		setStep(img, "Readiness", "Succeeded", "GuestReady", "Guest readiness completed before provisioning", "")
		setStep(img, "Provisioning", "Failed", reason, message, "/workspace/provisioners-result.json")
		setStep(img, "Sanitization", "Skipped", "ProvisionerFailed", "Provisioning failed before sanitization", "")
	case "ArtifactConvertFailed":
		setStep(img, "Build", "Failed", reason, message, "")
		setStep(img, "Boot", "Succeeded", "GuestBooted", "Guest boot completed before artifact conversion", "")
		setStep(img, "Readiness", "Succeeded", "GuestReady", "Guest readiness completed before artifact conversion", "")
		if len(img.Spec.Provisioners) > 0 {
			setStep(img, "Provisioning", "Succeeded", "ProvisioningComplete", "Provisioners completed before artifact conversion", "/workspace/provisioners-result.json")
		} else {
			setStep(img, "Provisioning", "Skipped", "NoProvisioners", "No provisioners configured", "")
		}
	default:
		setStep(img, "Build", "Failed", reason, message, "")
	}
	setStep(img, "Upload", "Skipped", "BuildFailed", "Upload skipped because build failed", "")
}

func observeBuildDuration(img *v1alpha1.VMImage, phase string) {
	if img.Status.StartTime == nil {
		return
	}
	observability.BuildDurationSeconds.WithLabelValues(phase, metricProvider(img), metricFormat(img)).Observe(time.Since(img.Status.StartTime.Time).Seconds())
}

func observeUploadDuration(img *v1alpha1.VMImage, success string) {
	step, ok := stepStatus(img, "Upload")
	if !ok || step.LastTransitionTime.IsZero() {
		return
	}
	observability.UploadDurationSeconds.WithLabelValues(metricProvider(img), metricFormat(img), success).Observe(time.Since(step.LastTransitionTime.Time).Seconds())
}

func stepStatus(img *v1alpha1.VMImage, name string) (v1alpha1.PipelineStepStatus, bool) {
	for _, step := range img.Status.Steps {
		if step.Name == name {
			return step, true
		}
	}
	return v1alpha1.PipelineStepStatus{}, false
}

func metricProvider(img *v1alpha1.VMImage) string {
	if len(img.Spec.Targets) == 0 {
		return "unknown"
	}
	return img.Spec.Targets[0].ProviderConfigRef.Name
}

func metricFormat(img *v1alpha1.VMImage) string {
	if len(img.Spec.Targets) == 0 {
		return "unknown"
	}
	return img.Spec.Targets[0].Format
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
