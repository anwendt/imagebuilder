package vmimage_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestCoreE2E_RemoteBuildSuccess(t *testing.T) {
	img := remoteE2EImage("core-e2e-remote-success")
	provider := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		result:             successfulRemoteBuildResult(),
	}
	r, c, recorder := remoteE2EReconciler(t, img, provider)

	reconcileNoError(t, r, img.Name)
	reconcileNoError(t, r, img.Name)
	reconcileNoError(t, r, img.Name)

	updated := getE2EImage(t, c, img.Name)
	if updated.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("phase = %q, want Ready", updated.Status.Phase)
	}
	if updated.Status.RemoteBuildRef == nil || *updated.Status.RemoteBuildRef != "provider://remote/success" {
		t.Fatalf("remoteBuildRef = %#v, want provider://remote/success", updated.Status.RemoteBuildRef)
	}
	if updated.Status.HygieneResult == nil || updated.Status.HygieneResult.Status != "passed" {
		t.Fatalf("hygieneResult = %#v, want passed", updated.Status.HygieneResult)
	}
	if len(updated.Status.Images) != 1 || updated.Status.Images[0].ImageRef != "ami-core-e2e" {
		t.Fatalf("images = %#v, want ami-core-e2e", updated.Status.Images)
	}
	requireEventReason(t, recorder, "RemoteBuildStarted")
	requireEventReason(t, recorder, "Ready")
}

func TestCoreE2E_RestartDuringRemoteBuildUsesDurableOperationRef(t *testing.T) {
	img := remoteE2EImage("core-e2e-remote-restart")
	inProgressProvider := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		result: &platform.RemoteBuildResult{
			OperationRef: "provider://remote/restart",
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      "provisioning",
			Done:         false,
		},
	}
	first, c, _ := remoteE2EReconciler(t, img, inProgressProvider)

	reconcileNoError(t, first, img.Name)
	reconcileNoError(t, first, img.Name)
	reconcileNoError(t, first, img.Name)

	building := getE2EImage(t, c, img.Name)
	if building.Status.RemoteBuildRef == nil || *building.Status.RemoteBuildRef != "provider://remote/restart" {
		t.Fatalf("remoteBuildRef = %#v, want durable provider operation ref", building.Status.RemoteBuildRef)
	}
	if building.Status.Phase != v1alpha1.PhaseProvisioning {
		t.Fatalf("phase = %q, want Provisioning while remote provider reports provisioning", building.Status.Phase)
	}

	resumedProvider := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		result:             successfulRemoteBuildResult(),
	}
	restarted := remoteE2ERestartedReconciler(t, c, resumedProvider)
	reconcileNoError(t, restarted, img.Name)

	if resumedProvider.lastRequest == nil || resumedProvider.lastRequest.OperationRef != "provider://remote/restart" {
		t.Fatalf("resumed request = %#v, want persisted OperationRef", resumedProvider.lastRequest)
	}
	ready := getE2EImage(t, c, img.Name)
	if ready.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("phase = %q, want Ready after resumed remote build", ready.Status.Phase)
	}
}

func TestCoreE2E_RemoteBuildTimeout(t *testing.T) {
	provCfg := remoteE2EProviderConfig()
	start := metav1.NewTime(metav1.Now().Add(-2 * time.Hour))
	remoteRef := "provider://remote/timeout"
	img := remoteE2EImage("core-e2e-remote-timeout")
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.Phase = v1alpha1.PhaseBuilding
	img.Status.StartTime = &start
	img.Status.RemoteBuildRef = &remoteRef
	img.Spec.Build.Timeout = &metav1.Duration{Duration: time.Hour}

	provider := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}}
	r, c, recorder := remoteE2EReconcilerWithObjects(t, provider, img, provCfg)

	reconcileNoError(t, r, img.Name)

	updated := getE2EImage(t, c, img.Name)
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if got := conditionReason(updated, "Failed"); got != "RemoteBuildTimedOut" {
		t.Fatalf("Failed reason = %q, want RemoteBuildTimedOut", got)
	}
	if provider.cleanupCalls != 1 || provider.cleanupRequest == nil || provider.cleanupRequest.OperationRef != remoteRef {
		t.Fatalf("cleanup calls/request = %d/%#v, want timeout cleanup for %q", provider.cleanupCalls, provider.cleanupRequest, remoteRef)
	}
	requireEventReason(t, recorder, "RemoteBuildTimedOut")
}

func TestCoreE2E_RemoteBuildCancellationDelete(t *testing.T) {
	provCfg := remoteE2EProviderConfig()
	now := metav1.Now()
	remoteRef := "provider://remote/delete"
	img := remoteE2EImage("core-e2e-remote-delete")
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.Phase = v1alpha1.PhaseBuilding
	img.Status.RemoteBuildRef = &remoteRef

	provider := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}}
	r, c, _ := remoteE2EReconcilerWithObjects(t, provider, img, provCfg)

	reconcileNoError(t, r, img.Name)

	updated := &v1alpha1.VMImage{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: "default"}, updated); err == nil {
		if hasFinalizer(updated, "imagebuilder.io/cleanup") {
			t.Fatal("finalizer should be removed after remote cleanup succeeds")
		}
	}
	if provider.cleanupCalls != 1 || provider.cleanupRequest == nil || provider.cleanupRequest.OperationRef != remoteRef {
		t.Fatalf("cleanup calls/request = %d/%#v, want delete cleanup for %q", provider.cleanupCalls, provider.cleanupRequest, remoteRef)
	}
}

func TestCoreE2E_RemoteBuildCleanupFailure(t *testing.T) {
	provCfg := remoteE2EProviderConfig()
	now := metav1.Now()
	img := remoteE2EImage("core-e2e-cleanup-failure")
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.Phase = v1alpha1.PhaseBuilding
	ref := "provider://remote/cleanup-failure"
	img.Status.RemoteBuildRef = &ref

	provider := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		cleanupErr:         errors.New("delete temporary instance: access denied"),
	}
	r, c, recorder := remoteE2EReconcilerWithObjects(t, provider, img, provCfg)

	if err := reconcileAllowError(r, img.Name); err == nil {
		t.Fatal("Reconcile should return cleanup error")
	}

	updated := getE2EImage(t, c, img.Name)
	if step, ok := stepStatus(updated, "Cleanup"); !ok || step.Status != "Failed" || step.Reason != "RemoteBuildCleanupFailed" {
		t.Fatalf("Cleanup step = %#v, want failed RemoteBuildCleanupFailed", step)
	}
	if got := conditionReason(updated, "CleanupFailed"); got != "RemoteBuildCleanupFailed" {
		t.Fatalf("CleanupFailed reason = %q, want RemoteBuildCleanupFailed", got)
	}
	if !hasFinalizer(updated, "imagebuilder.io/cleanup") {
		t.Fatal("finalizer should remain after cleanup failure")
	}
	requireEventReason(t, recorder, "RemoteBuildCleanupFailed")
}

func TestCoreE2E_RemoteBuildHygieneFailure(t *testing.T) {
	img := remoteE2EImage("core-e2e-hygiene-failure")
	provider := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		result: &platform.RemoteBuildResult{
			OperationRef: "provider://remote/hygiene-failure",
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "registered",
			Done:         true,
			Images: []platform.RemoteImageRef{
				{
					Provider:       "aws",
					ProviderConfig: "aws-cfg",
					Format:         platform.FormatAMI,
					ImageRef: platform.ImageRef{
						ID:       "ami-core-e2e",
						Location: "eu-west-1",
					},
				},
			},
			Hygiene: &platform.RemoteHygieneResult{
				Status:    "failed",
				Message:   "temporary bootstrap user remains",
				Checks:    []string{"temporary-user-removed"},
				ResultRef: "provider://hygiene/failed",
			},
		},
	}
	r, c, recorder := remoteE2EReconciler(t, img, provider)

	reconcileNoError(t, r, img.Name)
	reconcileNoError(t, r, img.Name)
	reconcileNoError(t, r, img.Name)

	updated := getE2EImage(t, c, img.Name)
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if got := conditionReason(updated, "Failed"); got != "RemoteHygieneFailed" {
		t.Fatalf("Failed reason = %q, want RemoteHygieneFailed", got)
	}
	if updated.Status.HygieneResult == nil || updated.Status.HygieneResult.Status != "failed" {
		t.Fatalf("hygieneResult = %#v, want failed", updated.Status.HygieneResult)
	}
	if provider.cleanupCalls != 1 {
		t.Fatalf("cleanupCalls = %d, want 1 after hygiene failure", provider.cleanupCalls)
	}
	requireEventReason(t, recorder, "RemoteHygieneFailed")
}

func TestCoreE2E_UploadRegisterRestartRecovery(t *testing.T) {
	jobName := "core-e2e-upload-recovery-upload"
	img := newImg("core-e2e-upload-recovery", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.UploadJobRef = &jobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Status.UploadOperations = []v1alpha1.UploadOperationStatus{
		{
			Provider:       "aws",
			ProviderConfig: "aws-cfg",
			Format:         "vmdk",
			Phase:          "Uploading",
			OperationRef:   "s3://imagebuilder/core-e2e-upload-recovery/disk.vmdk",
		},
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	pod := uploadResultPod("core-e2e-upload-recovery-pod", "default", jobName, uploadResultWithOperationsMessage(t))

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod).Build()
	restarted := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: record.NewFakeRecorder(10)}

	reconcileNoError(t, restarted, img.Name)

	updated := getE2EImage(t, c, img.Name)
	if updated.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("phase = %q, want Ready after restarted reconciler reads completed upload job", updated.Status.Phase)
	}
	if len(updated.Status.UploadOperations) != 1 {
		t.Fatalf("uploadOperations len = %d, want 1", len(updated.Status.UploadOperations))
	}
	op := updated.Status.UploadOperations[0]
	if op.Phase != "Succeeded" || op.OperationRef != "s3://imagebuilder/build-123/disk.vmdk" || op.ImageRef != "ami-123" {
		t.Fatalf("upload operation = %#v, want recovered successful operation with refs", op)
	}
	if len(updated.Status.Images) != 1 || updated.Status.Images[0].ImageRef != "ami-123" {
		t.Fatalf("images = %#v, want recovered ami-123", updated.Status.Images)
	}
}

func TestCoreE2E_RestartDuringUploadRegisterKeepsOperationRef(t *testing.T) {
	jobName := "core-e2e-upload-running-upload"
	img := newImg("core-e2e-upload-running", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.UploadJobRef = &jobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Status.UploadOperations = []v1alpha1.UploadOperationStatus{
		{
			Provider:       "aws",
			ProviderConfig: "aws-cfg",
			Format:         "vmdk",
			Phase:          "Uploading",
			OperationRef:   "s3://imagebuilder/core-e2e-upload-running/disk.vmdk",
		},
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status:     batchv1.JobStatus{Active: 1},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job).Build()
	restarted := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: record.NewFakeRecorder(10)}

	result := reconcileNoError(t, restarted, img.Name)
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while upload job is still running, got %+v", result)
	}
	updated := getE2EImage(t, c, img.Name)
	if updated.Status.Phase != v1alpha1.PhaseUploading {
		t.Fatalf("phase = %q, want Uploading", updated.Status.Phase)
	}
	if len(updated.Status.UploadOperations) != 1 || updated.Status.UploadOperations[0].OperationRef != "s3://imagebuilder/core-e2e-upload-running/disk.vmdk" {
		t.Fatalf("uploadOperations = %#v, want persisted operation ref", updated.Status.UploadOperations)
	}
}

func TestCoreE2E_RestartDuringCleanupKeepsFinalizerUntilCleanupCompletes(t *testing.T) {
	now := metav1.Now()
	uploadJobName := "core-e2e-cleanup-running-upload"
	img := newImg("core-e2e-cleanup-running", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Status.UploadJobRef = &uploadJobName
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	img.Spec.Targets = []v1alpha1.TargetSpec{{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"}}
	provCfg := remoteE2EProviderConfig()
	cleanupJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "core-e2e-cleanup-running-upload-cleanup", Namespace: "default"},
		Status:     batchv1.JobStatus{Active: 1},
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg, cleanupJob).Build()
	restarted := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: record.NewFakeRecorder(10)}

	result := reconcileNoError(t, restarted, img.Name)
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while cleanup job is running, got %+v", result)
	}
	updated := getE2EImage(t, c, img.Name)
	if !hasFinalizer(updated, "imagebuilder.io/cleanup") {
		t.Fatal("finalizer should remain while cleanup job is still running")
	}
	if step, ok := stepStatus(updated, "Cleanup"); ok && step.Status == "Failed" {
		t.Fatalf("Cleanup step = %#v, want no failed cleanup while job is still running", step)
	}
}

func TestCoreE2E_RestartDuringLeaseRenewalRenewsHeldLease(t *testing.T) {
	jobName := "core-e2e-lease-renewal-build"
	img := newImg("core-e2e-lease-renewal", "default", v1alpha1.PhaseBuilding)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.BuildJobRef = &jobName
	img.Status.BuildLeaseRefs = []string{"default/imagebuilder-build-global-0"}
	now := metav1.Now()
	img.Status.StartTime = &now

	holder := "default/core-e2e-lease-renewal/test-uid"
	duration := int32(60)
	oldRenew := metav1.NewMicroTime(time.Now().Add(-30 * time.Second))
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-build-global-0", Namespace: "default"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			RenewTime:            &oldRenew,
		},
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"}}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, lease).Build()
	restarted := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), Recorder: record.NewFakeRecorder(10)}

	result := reconcileNoError(t, restarted, img.Name)
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while build job is running, got %+v", result)
	}
	updatedLease := &coordinationv1.Lease{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "imagebuilder-build-global-0", Namespace: "default"}, updatedLease); err != nil {
		t.Fatalf("get renewed lease: %v", err)
	}
	if updatedLease.Spec.RenewTime == nil || !updatedLease.Spec.RenewTime.After(oldRenew.Time) {
		t.Fatalf("renewTime = %#v, want after %s", updatedLease.Spec.RenewTime, oldRenew.Time)
	}
	if updatedLease.Spec.HolderIdentity == nil || *updatedLease.Spec.HolderIdentity != holder {
		t.Fatalf("holder = %#v, want %q", updatedLease.Spec.HolderIdentity, holder)
	}
}

func remoteE2EImage(name string) *v1alpha1.VMImage {
	img := newImg(name, "default", v1alpha1.PhasePending)
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Source.URL = ""
	img.Spec.Source.ProviderRef = "ami-0123456789abcdef0"
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}
	return img
}

func remoteE2EProviderConfig() *v1alpha1.ProviderConfig {
	return &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws", Region: "eu-west-1"},
	}
}

func successfulRemoteBuildResult() *platform.RemoteBuildResult {
	return &platform.RemoteBuildResult{
		OperationRef: "provider://remote/success",
		Phase:        platform.RemoteBuildPhaseReady,
		Message:      "registered",
		Done:         true,
		Images: []platform.RemoteImageRef{
			{
				Provider:       "aws",
				ProviderConfig: "aws-cfg",
				Format:         platform.FormatAMI,
				Checksum:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ImageRef: platform.ImageRef{
					ID:       "ami-core-e2e",
					Location: "eu-west-1",
				},
			},
		},
		Hygiene: &platform.RemoteHygieneResult{
			Status:    "passed",
			Message:   "bootstrap residue absent",
			Checks:    []string{"temporary-user-removed", "bootstrap-files-removed"},
			ResultRef: "provider://hygiene/success",
		},
	}
}

func remoteE2EReconciler(t *testing.T, img *v1alpha1.VMImage, provider *fakeRemoteBuildPlugin) (*vmimage.VMImageReconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	return remoteE2EReconcilerWithObjects(t, provider, img, remoteE2EProviderConfig())
}

func remoteE2EReconcilerWithObjects(t *testing.T, provider *fakeRemoteBuildPlugin, objs ...client.Object) (*vmimage.VMImageReconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	reg := plugin.NewRegistry(slog.Default())
	if err := reg.Register(provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	s := testScheme(t)
	builder := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{})
	for _, obj := range objs {
		builder = builder.WithObjects(obj)
	}
	c := builder.Build()
	recorder := record.NewFakeRecorder(20)
	return &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg, Recorder: recorder}, c, recorder
}

func remoteE2ERestartedReconciler(t *testing.T, c client.Client, provider *fakeRemoteBuildPlugin) *vmimage.VMImageReconciler {
	t.Helper()
	reg := plugin.NewRegistry(slog.Default())
	if err := reg.Register(provider); err != nil {
		t.Fatalf("register restarted provider: %v", err)
	}
	return &vmimage.VMImageReconciler{Client: c, Scheme: testScheme(t), Registry: reg, Recorder: record.NewFakeRecorder(20)}
}

func reconcileNoError(t *testing.T, r *vmimage.VMImageReconciler, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	return result
}

func reconcileAllowError(r *vmimage.VMImageReconciler, name string) error {
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})
	return err
}

func getE2EImage(t *testing.T, c client.Client, name string) *v1alpha1.VMImage {
	t.Helper()
	img := &v1alpha1.VMImage{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, img); err != nil {
		t.Fatalf("get VMImage %q: %v", name, err)
	}
	return img
}

func requireEventReason(t *testing.T, recorder *record.FakeRecorder, reason string) {
	t.Helper()
	for {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, reason) {
				return
			}
		default:
			t.Fatalf("expected event reason %q", reason)
		}
	}
}
