package vmimage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/evidencejob"
	"github.com/anwendt/imagebuilder/pkg/controller/revision"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/observability"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ADR-011: with spec.evidence.publisher=controller the provider returns the
// guest evidence outbox with the Done response. The controller stores it in a
// Secret in its own namespace, renders the VM image descriptor and SLSA
// provenance and runs a publication Job there; the VMImage becomes Ready only
// after the Job reported the immutable references.

// The evidence publisher works in the operator namespace only (Helm template
// evidence-publisher.yaml renders the matching Role).
//
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;create;update;delete,namespace=imagebuilder-system
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete,namespace=imagebuilder-system
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch,namespace=imagebuilder-system

const evidencePublicationPoll = requeueAfter

func evidenceOwner(img *v1alpha1.VMImage) evidencejob.Owner {
	return evidencejob.Owner{
		Namespace: img.Namespace,
		Name:      img.Name,
		UID:       string(img.UID),
		BuildID:   revision.BuildID(string(img.UID), img.Spec.Build.Revision),
	}
}

func evidenceTarget(img *v1alpha1.VMImage, providerName string) (evidencejob.Target, error) {
	if img.Spec.Evidence == nil || img.Spec.Evidence.Subject == nil {
		return evidencejob.Target{}, fmt.Errorf("spec.evidence.subject is required for publisher controller")
	}
	subject := img.Spec.Evidence.Subject
	return evidencejob.NewTarget(img.Spec.Evidence.RegistryRepository, subject.Name, subject.Version, subject.Platform, providerName)
}

// remoteFailureReason maps provider error prefixes to condition reasons.
func remoteFailureReason(err error) string {
	if err == nil {
		return "RemoteBuildFailed"
	}
	message := err.Error()
	for _, reason := range []string{"VerifyBootFailed", "EvidenceBundleLost", "EvidenceBundleMissing", "EvidenceBundleTooLarge", "EvidenceBundleInvalid", "EvidenceCollectionFailed"} {
		if strings.Contains(message, reason+":") {
			if reason == "VerifyBootFailed" {
				return reason
			}
			return "RemoteEvidenceRejected"
		}
	}
	return "RemoteBuildFailed"
}

// startEvidencePublication handles a Done remote build in controller mode.
func (r *VMImageReconciler) startEvidencePublication(ctx context.Context, img *v1alpha1.VMImage, req *platform.RemoteBuildRequest, result *platform.RemoteBuildResult, remotePlugin platform.RemoteBuildPlugin, images []v1alpha1.ImageStatus, hygiene v1alpha1.HygieneResultStatus, providerName string, log *slog.Logger) (ctrl.Result, error) {
	fail := func(reason, message string) (ctrl.Result, error) {
		img.Status.Evidence = &v1alpha1.EvidenceStatus{Status: "failed", Publisher: v1alpha1.EvidencePublisherController, Message: message}
		setStep(img, "Evidence", "Failed", reason, message, "")
		observability.FailuresTotal.WithLabelValues("RemoteBuild", reason, providerName).Inc()
		if cleanupErr := r.cleanupRemoteBuild(ctx, img); cleanupErr != nil {
			return ctrl.Result{}, fmt.Errorf("cleanup remote build after evidence failure: %w", cleanupErr)
		}
		return r.setFailedWithReason(ctx, img, reason, message)
	}
	if err := r.Evidence.Validate(); err != nil {
		return fail("EvidencePublisherNotConfigured", err.Error())
	}
	if result.EvidenceBundle == nil || len(result.EvidenceBundle.Content) == 0 {
		return fail("RemoteEvidenceMissing", fmt.Sprintf("provider %q completed the build without the evidence bundle required by publisher controller", providerName))
	}
	bundle, err := evidence.VerifyBundle(result.EvidenceBundle.Content, result.EvidenceBundle.SHA256)
	if err != nil {
		return fail("RemoteEvidenceRejected", err.Error())
	}
	if err := evidence.CheckSeverities(bundle.TrivyReport, img.Spec.Evidence.FailOnSeverity); err != nil {
		return fail("EvidenceVulnerabilityPolicyFailed", err.Error())
	}
	target, err := evidenceTarget(img, providerName)
	if err != nil {
		return fail("RemoteEvidenceRejected", err.Error())
	}
	owner := evidenceOwner(img)
	started := time.Now()
	if img.Status.StartTime != nil {
		started = img.Status.StartTime.Time
	}
	input := evidencejob.DescriptorInput{
		Image:          img,
		Provider:       providerName,
		Images:         result.Images,
		Bundle:         bundle,
		BuildID:        owner.BuildID,
		BuilderVersion: r.Evidence.BuilderVersion,
		StartedOn:      started,
		FinishedOn:     time.Now(),
	}
	descriptor, err := evidencejob.RenderDescriptor(input)
	if err != nil {
		return fail("RemoteEvidenceRejected", err.Error())
	}
	provenance, err := evidencejob.RenderProvenance(input, descriptor)
	if err != nil {
		return fail("RemoteEvidenceRejected", err.Error())
	}
	secret := evidencejob.BuildSecret(r.Evidence, owner, bundle.Content, descriptor, provenance)
	if err := r.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("store evidence bundle Secret: %w", err)
		}
		existing := &corev1.Secret{}
		if err := r.directReader().Get(ctx, client.ObjectKeyFromObject(secret), existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("read evidence bundle Secret: %w", err)
		}
		existing.Data = secret.Data
		existing.Labels = secret.Labels
		if err := r.Update(ctx, existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("update evidence bundle Secret: %w", err)
		}
	}
	// The bundle is durable now; let the provider drop its own copy.
	ack := *req
	ack.EvidenceBundleAcknowledged = true
	if _, err := remotePlugin.ReconcileRemoteBuild(ctx, &ack); err != nil {
		log.Warn("provider did not accept the evidence bundle acknowledgement; its copy is removed on cleanup", slog.Any("error", err))
	}

	img.Status.Phase = v1alpha1.PhaseUploading
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
	img.Status.Evidence = &v1alpha1.EvidenceStatus{
		Status:        "unknown",
		Publisher:     v1alpha1.EvidencePublisherController,
		Message:       fmt.Sprintf("Publishing %s:%s to %s", target.Repository, target.DescriptorTag(), r.Evidence.Namespace),
		BundleDigest:  bundle.Digest(),
		PublishJobRef: secret.Name,
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
	setStep(img, "Evidence", "Running", "EvidencePublishing", "Controller publishes and signs the VM image descriptor", bundle.Digest())
	setCondition(img, "EvidencePublished", metav1.ConditionFalse, "EvidencePublishing", "Evidence publication Job pending")
	img.Status.NextRemoteRetryTime = nil
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status for evidence publication: %w", err)
	}
	r.recordEvent(img, corev1.EventTypeNormal, "EvidencePublishing", "Evidence bundle %s stored; publishing %s:%s", bundle.Digest(), target.Repository, target.DescriptorTag())
	return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
}

// reconcileEvidencePublication drives the publication Job (phase Uploading of
// a remote build in controller mode).
func (r *VMImageReconciler) reconcileEvidencePublication(ctx context.Context, img *v1alpha1.VMImage, log *slog.Logger) (ctrl.Result, error) {
	if img.Status.Evidence == nil || img.Status.Evidence.PublishJobRef == "" {
		return r.setFailedWithReason(ctx, img, "EvidencePublicationFailed", "evidence publication state is missing")
	}
	providerName := ""
	if len(img.Status.Images) > 0 {
		providerName = img.Status.Images[0].Provider
	}
	target, err := evidenceTarget(img, providerName)
	if err != nil {
		return r.failEvidencePublication(ctx, img, err.Error())
	}
	name := img.Status.Evidence.PublishJobRef
	key := types.NamespacedName{Namespace: r.Evidence.Namespace, Name: name}
	job := &batchv1.Job{}
	if err := r.directReader().Get(ctx, key, job); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get evidence publication Job: %w", err)
		}
		secret := &corev1.Secret{}
		if err := r.directReader().Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return r.failEvidencePublication(ctx, img, "evidence bundle Secret "+key.String()+" is missing; rebuild required")
			}
			return ctrl.Result{}, fmt.Errorf("get evidence bundle Secret: %w", err)
		}
		bundleSHA := strings.TrimPrefix(img.Status.Evidence.BundleDigest, "sha256:")
		if evidence.SHA256Hex(secret.Data[evidencejob.BundleKey]) != bundleSHA {
			return r.failEvidencePublication(ctx, img, "evidence bundle Secret does not match the recorded bundle digest")
		}
		job, err = evidencejob.BuildJob(r.Evidence, evidenceOwner(img), target, bundleSHA)
		if err != nil {
			return r.failEvidencePublication(ctx, img, err.Error())
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create evidence publication Job: %w", err)
		}
		r.recordEvent(img, corev1.EventTypeNormal, "EvidencePublicationStarted", "Evidence publication Job %s/%s created", key.Namespace, key.Name)
		log.Info("evidence publication job created", slog.String("job", key.String()))
		return ctrl.Result{RequeueAfter: evidencePublicationPoll}, nil
	}
	switch {
	case isJobSucceeded(job):
		message, failed, err := r.evidencePodResult(ctx, key)
		if err != nil {
			return ctrl.Result{}, err
		}
		if failed {
			return r.failEvidencePublication(ctx, img, message)
		}
		result, err := evidencejob.ParseResult(message)
		if err != nil {
			return r.failEvidencePublication(ctx, img, err.Error())
		}
		return r.completeEvidencePublication(ctx, img, target, result, key)
	case isJobFailed(job):
		message, _, err := r.evidencePodResult(ctx, key)
		if err != nil {
			return ctrl.Result{}, err
		}
		if message == "" {
			message = "evidence publication Job failed"
		}
		return r.failEvidencePublication(ctx, img, message)
	default:
		return ctrl.Result{RequeueAfter: evidencePublicationPoll}, nil
	}
}

func (r *VMImageReconciler) evidencePodResult(ctx context.Context, key types.NamespacedName) (string, bool, error) {
	pods := &corev1.PodList{}
	if err := r.directReader().List(ctx, pods, client.InNamespace(key.Namespace), client.MatchingLabels{"job-name": key.Name}); err != nil {
		return "", false, fmt.Errorf("list evidence publication pods: %w", err)
	}
	var lastFailure string
	for i := range pods.Items {
		message, failed := evidencejob.PodResult(&pods.Items[i])
		if failed {
			lastFailure = message
			continue
		}
		if message != "" {
			return message, false, nil
		}
	}
	if lastFailure != "" {
		return lastFailure, true, nil
	}
	return "", false, nil
}

func (r *VMImageReconciler) completeEvidencePublication(ctx context.Context, img *v1alpha1.VMImage, target evidencejob.Target, result evidencejob.Result, key types.NamespacedName) (ctrl.Result, error) {
	now := metav1.Now()
	status := img.Status.Evidence.DeepCopy()
	status.Status = "passed"
	status.Message = fmt.Sprintf("Published %s:%s and merged into %s:%s", target.Repository, target.DescriptorTag(), target.Repository, target.Version)
	status.DescriptorRef = target.RefFor(result.DescriptorDigest)
	status.IndexRef = target.RefFor(result.IndexDigest)
	status.SignatureRef = target.RefFor(result.SignatureDigest)
	status.ProvenanceRef = target.RefFor(result.ProvenanceDigest)
	status.SBOMRef = target.RefFor(result.SBOMDigest)
	status.VulnerabilityReportRef = target.RefFor(result.VulnerabilityReportDigest)
	status.GeneratedAt = &now
	if err := validateRemoteEvidence(img.Spec.Evidence, *status); err != nil {
		return r.failEvidencePublication(ctx, img, err.Error())
	}
	img.Status.Evidence = status
	img.Status.Phase = v1alpha1.PhaseReady
	img.Status.CompletionTime = &now
	setStep(img, "Evidence", "Succeeded", "EvidencePublished", status.Message, status.DescriptorRef)
	setCondition(img, "EvidencePublished", metav1.ConditionTrue, "EvidencePublished", status.Message)
	setCondition(img, "Ready", metav1.ConditionTrue, "RemoteBuildSucceeded", "Remote build completed and evidence published")
	if err := r.Status().Update(ctx, img); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after evidence publication: %w", err)
	}
	r.deleteEvidenceObjects(ctx, key, true)
	r.recordEvent(img, corev1.EventTypeNormal, "Ready", "Evidence published as %s", status.DescriptorRef)
	return ctrl.Result{}, nil
}

func (r *VMImageReconciler) failEvidencePublication(ctx context.Context, img *v1alpha1.VMImage, message string) (ctrl.Result, error) {
	message = evidencejob.Truncate(message)
	if img.Status.Evidence == nil {
		img.Status.Evidence = &v1alpha1.EvidenceStatus{Publisher: v1alpha1.EvidencePublisherController}
	}
	img.Status.Evidence.Status = "failed"
	img.Status.Evidence.Message = message
	setStep(img, "Evidence", "Failed", "EvidencePublicationFailed", message, "")
	setCondition(img, "EvidencePublished", metav1.ConditionFalse, "EvidencePublicationFailed", message)
	provider := ""
	if len(img.Status.Images) > 0 {
		provider = img.Status.Images[0].Provider
	}
	observability.FailuresTotal.WithLabelValues("EvidencePublication", "EvidencePublicationFailed", provider).Inc()
	if img.Status.Evidence.PublishJobRef != "" {
		// Keep the failed Job (TTL) for its logs; the bundle is not needed anymore.
		r.deleteEvidenceObjects(ctx, types.NamespacedName{Namespace: r.Evidence.Namespace, Name: img.Status.Evidence.PublishJobRef}, false)
	}
	return r.setFailedWithReason(ctx, img, "EvidencePublicationFailed", message)
}

// deleteEvidenceObjects removes the bundle Secret and optionally the Job.
func (r *VMImageReconciler) deleteEvidenceObjects(ctx context.Context, key types.NamespacedName, withJob bool) {
	if key.Namespace == "" || key.Name == "" {
		return
	}
	if withJob {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			r.log.Warn("delete evidence publication Job", slog.String("job", key.String()), slog.Any("error", err))
		}
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		r.log.Warn("delete evidence bundle Secret", slog.String("secret", key.String()), slog.Any("error", err))
	}
}

// cleanupEvidencePublication removes publication objects when the VMImage is
// deleted.
func (r *VMImageReconciler) cleanupEvidencePublication(ctx context.Context, img *v1alpha1.VMImage) {
	if img.Status.Evidence == nil || img.Status.Evidence.PublishJobRef == "" {
		return
	}
	r.deleteEvidenceObjects(ctx, types.NamespacedName{Namespace: r.Evidence.Namespace, Name: img.Status.Evidence.PublishJobRef}, true)
}
