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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
	"github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
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

func TestReconcile_Pending_ArtifactPVC_CreatesWorkspaceClaim(t *testing.T) {
	storageClass := "fast-block"
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("pvc-build", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{
		Type: "pvc",
		PVC: &v1alpha1.ArtifactPVCSpec{
			StorageClassName: &storageClass,
			Size:             "50Gi",
			AccessMode:       "ReadWriteOnce",
			RetainPolicy:     "Never",
		},
	}

	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(&fakeRegistryPlugin{name: "aws"})

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "pvc-build", "default")

	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "pvc-build-workspace", Namespace: "default"}, pvc); err != nil {
		t.Fatalf("workspace PVC was not created: %v", err)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast-block" {
		t.Fatalf("storageClassName = %#v, want fast-block", pvc.Spec.StorageClassName)
	}
	if got := pvc.Spec.AccessModes[0]; got != corev1.ReadWriteOnce {
		t.Fatalf("accessMode = %q, want ReadWriteOnce", got)
	}
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "50Gi" {
		t.Fatalf("storage request = %q, want 50Gi", got)
	}
}

func TestReconcile_Pending_ArtifactPVC_ExistingClaimDoesNotCreatePVC(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("existing-pvc-build", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{
		Type: "pvc",
		PVC: &v1alpha1.ArtifactPVCSpec{
			ClaimName:  "shared-workspace",
			AccessMode: "ReadWriteMany",
		},
	}

	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(&fakeRegistryPlugin{name: "aws"})

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "existing-pvc-build", "default")

	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(context.Background(), types.NamespacedName{Name: "existing-pvc-build-workspace", Namespace: "default"}, pvc)
	if err == nil {
		t.Fatal("operator should not create a per-build PVC when claimName is provided")
	}
}

func TestReconcile_Pending_CallsProviderValidate(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("validate-called", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"},
			Format:            "vmdk",
		},
	}

	providerPlugin := &fakeRegistryPlugin{name: "aws"}
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(providerPlugin)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "validate-called", "default")

	if providerPlugin.validateCalls != 1 {
		t.Errorf("validateCalls = %d, want 1", providerPlugin.validateCalls)
	}
}

func TestReconcile_Pending_ProviderValidateFailure_SetsFailed(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("validate-fails", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"},
			Format:            "ami",
		},
	}

	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(&fakeRegistryPlugin{name: "aws", validateErr: fmt.Errorf("unsupported target format")})

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "validate-fails", "default")

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "validate-fails", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed when provider validation fails", updated.Status.Phase)
	}
}

func TestReconcile_RemoteBuild_ProviderUnsupported_SetsFailed(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("remote-unsupported", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}

	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(&fakeRegistryPlugin{name: "aws"})

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "remote-unsupported", "default")

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "remote-unsupported", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if got := conditionReason(updated, "Failed"); got != "RemoteBuildUnsupported" {
		t.Fatalf("Failed reason = %q, want RemoteBuildUnsupported", got)
	}
}

func TestReconcile_RemoteBuild_CollidingExternalTakesPrecedenceOverBuiltin(t *testing.T) {
	img, provCfg := remoteTestImage("remote-external-precedence")
	pp := &v1alpha1.PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "aws", UID: "external-aws-uid"},
		Status: v1alpha1.PlatformProviderStatus{
			Phase: "Healthy",
			Capabilities: &v1alpha1.ProviderCapabilities{
				ProviderName: "aws", ProviderVersion: "external", ProtocolVersion: "v1", BuildModes: []string{v1alpha1.BuildModeLocal, v1alpha1.BuildModeRemote},
			},
		},
	}
	builtin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}, result: &platform.RemoteBuildResult{Phase: platform.RemoteBuildPhaseReady, Done: true}}
	external := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}, result: &platform.RemoteBuildResult{Phase: platform.RemoteBuildPhaseBooting, Message: "external booting"}}
	reg := plugin.NewRegistry(slog.Default())
	if err := reg.Register(builtin); err != nil {
		t.Fatalf("register builtin: %v", err)
	}
	if err := reg.RegisterExternal(pp.Name, string(pp.UID), external); err != nil {
		t.Fatalf("register external: %v", err)
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg, pp).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}
	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)

	if external.remoteCalls != 1 {
		t.Fatalf("external remote calls=%d, want 1", external.remoteCalls)
	}
	if builtin.remoteCalls != 0 {
		t.Fatalf("builtin remote calls=%d, want 0", builtin.remoteCalls)
	}
}

func TestReconcile_RemoteBuild_UnhealthyExternalDoesNotFallbackToBuiltin(t *testing.T) {
	img, provCfg := remoteTestImage("remote-no-fallback")
	pp := &v1alpha1.PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "aws", UID: "external-aws-uid"},
		Status: v1alpha1.PlatformProviderStatus{
			Phase:        "Unhealthy",
			Capabilities: &v1alpha1.ProviderCapabilities{ProviderName: "aws", ProtocolVersion: "v1"},
		},
	}
	builtin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}}
	reg := plugin.NewRegistry(slog.Default())
	if err := reg.Register(builtin); err != nil {
		t.Fatalf("register builtin: %v", err)
	}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg, pp).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}
	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)

	if builtin.remoteCalls != 0 {
		t.Fatalf("builtin remote calls=%d, want 0", builtin.remoteCalls)
	}
	updated := &v1alpha1.VMImage{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated); err != nil {
		t.Fatalf("get VMImage: %v", err)
	}
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase=%q, want Failed", updated.Status.Phase)
	}
}

func TestReconcile_RemoteBuild_TransientErrorRetriesWithoutCleanup(t *testing.T) {
	img, provCfg := remoteTestImage("remote-transient")
	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		remoteErr:          providererrors.Transient(errors.New("provider throttled"), 0),
	}
	r, c := remoteTestReconciler(t, img, provCfg, providerPlugin)

	reconcileOnce(t, r, img.Name, img.Namespace) // initialize remote build status
	result := reconcileOnce(t, r, img.Name, img.Namespace)
	if result.RequeueAfter != 15*time.Second {
		t.Fatalf("retry delay = %s, want 15s", result.RequeueAfter)
	}
	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated) //nolint:errcheck
	if updated.Status.Phase == v1alpha1.PhaseFailed {
		t.Fatalf("transient error made VMImage terminal: %#v", updated.Status)
	}
	if updated.Status.RemoteRetryCount != 1 || updated.Status.NextRemoteRetryTime == nil {
		t.Fatalf("retry status = count %d next %#v", updated.Status.RemoteRetryCount, updated.Status.NextRemoteRetryTime)
	}
	if got := conditionReason(updated, "RemoteBuildRetrying"); got != "TransientProviderError" {
		t.Fatalf("RemoteBuildRetrying reason = %q", got)
	}
	if providerPlugin.cleanupCalls != 0 {
		t.Fatalf("cleanup calls = %d, want 0 for transient error", providerPlugin.cleanupCalls)
	}

	result = reconcileOnce(t, r, img.Name, img.Namespace)
	if result.RequeueAfter <= 0 || providerPlugin.remoteCalls != 1 {
		t.Fatalf("backoff gate result=%+v remoteCalls=%d", result, providerPlugin.remoteCalls)
	}
}

func TestReconcile_RemoteBuild_TransientInitErrorRetries(t *testing.T) {
	img, provCfg := remoteTestImage("remote-init-transient")
	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{
			name:    "aws",
			initErr: providererrors.Transient(errors.New("validation endpoint unavailable"), 0),
		},
	}
	r, c := remoteTestReconciler(t, img, provCfg, providerPlugin)
	reconcileOnce(t, r, img.Name, img.Namespace)
	result := reconcileOnce(t, r, img.Name, img.Namespace)
	if result.RequeueAfter != 15*time.Second {
		t.Fatalf("retry delay = %s, want 15s", result.RequeueAfter)
	}
	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated) //nolint:errcheck
	if updated.Status.Phase == v1alpha1.PhaseFailed || updated.Status.RemoteRetryCount != 1 {
		t.Fatalf("transient init status = %#v", updated.Status)
	}
	if providerPlugin.remoteCalls != 0 || providerPlugin.cleanupCalls != 0 {
		t.Fatalf("remoteCalls=%d cleanupCalls=%d", providerPlugin.remoteCalls, providerPlugin.cleanupCalls)
	}
}

func TestReconcile_RemoteBuild_TransientBackoffIncreasesAndRecovers(t *testing.T) {
	img, provCfg := remoteTestImage("remote-recovery")
	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		remoteErr:          providererrors.Transient(errors.New("temporary outage"), 0),
	}
	r, c := remoteTestReconciler(t, img, provCfg, providerPlugin)
	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated) //nolint:errcheck
	past := metav1.NewTime(time.Now().Add(-time.Second))
	updated.Status.NextRemoteRetryTime = &past
	if err := c.Status().Update(context.Background(), updated); err != nil {
		t.Fatalf("make retry due: %v", err)
	}
	result := reconcileOnce(t, r, img.Name, img.Namespace)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("second retry delay = %s, want 30s", result.RequeueAfter)
	}

	c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated) //nolint:errcheck
	past = metav1.NewTime(time.Now().Add(-time.Second))
	updated.Status.NextRemoteRetryTime = &past
	if err := c.Status().Update(context.Background(), updated); err != nil {
		t.Fatalf("make recovery retry due: %v", err)
	}
	providerPlugin.remoteErr = nil
	providerPlugin.result = &platform.RemoteBuildResult{Phase: platform.RemoteBuildPhaseBooting, Message: "recovered"}
	result = reconcileOnce(t, r, img.Name, img.Namespace)
	if result.RequeueAfter != 15*time.Second {
		t.Fatalf("progress requeue = %s, want 15s", result.RequeueAfter)
	}
	c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated) //nolint:errcheck
	if updated.Status.RemoteRetryCount != 0 || updated.Status.NextRemoteRetryTime != nil {
		t.Fatalf("retry state was not reset: count %d next %#v", updated.Status.RemoteRetryCount, updated.Status.NextRemoteRetryTime)
	}
	if got := conditionReason(updated, "RemoteBuildRetrying"); got != "RemoteBuildRecovered" {
		t.Fatalf("recovery reason = %q", got)
	}
}

func TestReconcile_RemoteBuild_TerminalErrorFailsAndCleansUp(t *testing.T) {
	img, provCfg := remoteTestImage("remote-terminal")
	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		remoteErr:          errors.New("invalid source image"),
	}
	r, c := remoteTestReconciler(t, img, provCfg, providerPlugin)
	reconcileOnce(t, r, img.Name, img.Namespace)
	reconcileOnce(t, r, img.Name, img.Namespace)

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: img.Name, Namespace: img.Namespace}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed || providerPlugin.cleanupCalls != 1 {
		t.Fatalf("terminal result phase=%q cleanupCalls=%d", updated.Status.Phase, providerPlugin.cleanupCalls)
	}
}

func TestReconcile_RemoteBuild_CompletesWithProviderImage(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("remote-ready", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Source.URL = ""
	img.Spec.Source.ProviderRef = "ami-0123456789abcdef0"
	img.Spec.Source.MarketplaceRef = &v1alpha1.MarketplaceRef{
		Publisher: "Canonical",
		Offer:     "ubuntu-24_04-lts",
		SKU:       "server",
		Version:   "latest",
	}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}

	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		result: &platform.RemoteBuildResult{
			OperationRef: "remote-op-1",
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "registered",
			Done:         true,
			Images: []platform.RemoteImageRef{
				{
					ImageRef: platform.ImageRef{
						ID:       "ami-123",
						Location: "eu-west-1",
					},
					Format:   platform.FormatAMI,
					Checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				},
			},
			Hygiene: &platform.RemoteHygieneResult{
				Status:    "passed",
				Message:   "bootstrap residue absent",
				Checks:    []string{"temporary-user-removed", "bootstrap-files-removed"},
				ResultRef: "provider://hygiene/report-1",
			},
		},
	}
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(providerPlugin)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "remote-ready", "default")
	result := reconcileOnce(t, r, "remote-ready", "default")
	if result.RequeueAfter != 0 {
		t.Fatalf("result.RequeueAfter = %s, want 0 for completed remote build", result.RequeueAfter)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "remote-ready", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("phase = %q, want Ready", updated.Status.Phase)
	}
	if updated.Status.RemoteBuildRef == nil || *updated.Status.RemoteBuildRef != "remote-op-1" {
		t.Fatalf("remoteBuildRef = %#v, want remote-op-1", updated.Status.RemoteBuildRef)
	}
	if len(updated.Status.Images) != 1 || updated.Status.Images[0].ImageRef != "ami-123" {
		t.Fatalf("images = %#v, want one ami-123 image", updated.Status.Images)
	}
	if providerPlugin.remoteCalls != 1 {
		t.Fatalf("remoteCalls = %d, want 1", providerPlugin.remoteCalls)
	}
	if providerPlugin.lastRequest == nil || providerPlugin.lastRequest.SourceProviderRef != "ami-0123456789abcdef0" {
		t.Fatalf("remote source providerRef = %#v, want ami-0123456789abcdef0", providerPlugin.lastRequest)
	}
	if providerPlugin.lastRequest.SourceMarketplace == nil ||
		providerPlugin.lastRequest.SourceMarketplace.Publisher != "Canonical" ||
		providerPlugin.lastRequest.SourceMarketplace.Offer != "ubuntu-24_04-lts" ||
		providerPlugin.lastRequest.SourceMarketplace.SKU != "server" ||
		providerPlugin.lastRequest.SourceMarketplace.Version != "latest" {
		t.Fatalf("remote marketplace = %#v, want Ubuntu marketplace ref", providerPlugin.lastRequest.SourceMarketplace)
	}
	if updated.Status.HygieneResult == nil || updated.Status.HygieneResult.Status != "passed" {
		t.Fatalf("hygieneResult = %#v, want passed", updated.Status.HygieneResult)
	}
	if updated.Status.HygieneResult.ResultRef != "provider://hygiene/report-1" {
		t.Fatalf("hygiene resultRef = %q, want provider://hygiene/report-1", updated.Status.HygieneResult.ResultRef)
	}
	if step, ok := stepStatus(updated, "Sanitization"); !ok || step.Reason != "RemoteHygienePassed" {
		t.Fatalf("Sanitization step = %#v, want RemoteHygienePassed", step)
	}
}

func TestReconcile_RemoteBuild_FailedHygieneCleansProviderResources(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg("remote-hygiene-failed", "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Source.URL = ""
	img.Spec.Source.ProviderRef = "ami-0123456789abcdef0"
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}

	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		result: &platform.RemoteBuildResult{
			OperationRef: "remote-op-hygiene",
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "registered",
			Done:         true,
			Images: []platform.RemoteImageRef{
				{
					ImageRef: platform.ImageRef{
						ID:       "ami-123",
						Location: "eu-west-1",
					},
					Format: platform.FormatAMI,
				},
			},
			Hygiene: &platform.RemoteHygieneResult{
				Status:    "failed",
				Message:   "temporary bootstrap user remains",
				Checks:    []string{"temporary-user-removed"},
				ResultRef: "provider://hygiene/report-failed",
			},
		},
	}
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(providerPlugin)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "remote-hygiene-failed", "default")
	reconcileOnce(t, r, "remote-hygiene-failed", "default")

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "remote-hygiene-failed", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if got := conditionReason(updated, "Failed"); got != "RemoteHygieneFailed" {
		t.Fatalf("Failed reason = %q, want RemoteHygieneFailed", got)
	}
	if updated.Status.HygieneResult == nil || updated.Status.HygieneResult.Status != "failed" {
		t.Fatalf("hygieneResult = %#v, want failed", updated.Status.HygieneResult)
	}
	if providerPlugin.cleanupCalls != 1 {
		t.Fatalf("cleanupCalls = %d, want 1", providerPlugin.cleanupCalls)
	}
	if step, ok := stepStatus(updated, "Sanitization"); !ok || step.Reason != "RemoteHygieneFailed" {
		t.Fatalf("Sanitization step = %#v, want RemoteHygieneFailed", step)
	}
}

func TestReconcile_RemoteBuild_TimeoutCleansProviderResources(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	start := metav1.NewTime(metav1.Now().Add(-2 * time.Hour))
	remoteRef := "aws://remote-build/build-123?instanceId=i-123"
	img := newImg("remote-timeout", "default", v1alpha1.PhaseBuilding)
	img.UID = "build-123"
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Build.Timeout = &metav1.Duration{Duration: time.Hour}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}
	img.Status.StartTime = &start
	img.Status.RemoteBuildRef = &remoteRef

	providerPlugin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}}
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(providerPlugin)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "remote-timeout", "default")

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "remote-timeout", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	if providerPlugin.cleanupCalls != 1 {
		t.Fatalf("cleanupCalls = %d, want 1", providerPlugin.cleanupCalls)
	}
	if providerPlugin.cleanupRequest == nil || providerPlugin.cleanupRequest.OperationRef != remoteRef {
		t.Fatalf("cleanup request = %#v, want operation ref %q", providerPlugin.cleanupRequest, remoteRef)
	}
}

func TestReconcile_Delete_RemoteBuildCleansProviderResources(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	now := metav1.Now()
	remoteRef := "aws://remote-build/build-123?instanceId=i-123"
	img := newImg("remote-delete", "default", v1alpha1.PhaseBuilding)
	img.UID = "build-123"
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}
	img.Status.RemoteBuildRef = &remoteRef

	providerPlugin := &fakeRemoteBuildPlugin{fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"}}
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(providerPlugin)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg}

	reconcileOnce(t, r, "remote-delete", "default")

	if providerPlugin.cleanupCalls != 1 {
		t.Fatalf("cleanupCalls = %d, want 1", providerPlugin.cleanupCalls)
	}
	if providerPlugin.cleanupRequest == nil || providerPlugin.cleanupRequest.OperationRef != remoteRef {
		t.Fatalf("cleanup request = %#v, want operation ref %q", providerPlugin.cleanupRequest, remoteRef)
	}
}

func TestReconcile_Delete_RemoteBuildCleanupFailureUpdatesStatus(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	now := metav1.Now()
	remoteRef := "aws://remote-build/build-123?instanceId=i-123"
	img := newImg("remote-delete-cleanup-failed", "default", v1alpha1.PhaseBuilding)
	img.UID = "build-123"
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.DeletionTimestamp = &now
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "ami"},
	}
	img.Status.RemoteBuildRef = &remoteRef

	providerPlugin := &fakeRemoteBuildPlugin{
		fakeRegistryPlugin: fakeRegistryPlugin{name: "aws"},
		cleanupErr:         errors.New("delete temporary instance: access denied"),
	}
	reg := plugin.NewRegistry(slog.Default())
	_ = reg.Register(providerPlugin)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	recorder := record.NewFakeRecorder(10)
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: reg, Recorder: recorder}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "remote-delete-cleanup-failed", Namespace: "default"}})
	if err == nil {
		t.Fatal("Reconcile should return cleanup error")
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "remote-delete-cleanup-failed", Namespace: "default"}, updated) //nolint:errcheck
	if step, ok := stepStatus(updated, "Cleanup"); !ok || step.Reason != "RemoteBuildCleanupFailed" {
		t.Fatalf("Cleanup step = %#v, want RemoteBuildCleanupFailed", step)
	}
	if got := conditionReason(updated, "CleanupFailed"); got != "RemoteBuildCleanupFailed" {
		t.Fatalf("CleanupFailed reason = %q, want RemoteBuildCleanupFailed", got)
	}
	requireEvent(t, recorder, "RemoteBuildCleanupFailed")
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
	pod := buildResultPod("cond-test-pod", "default", jobName, buildResultMessage(t))

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod).Build()
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
// reconcileUploading: upload job pipeline
// ---------------------------------------------------------------------------

func TestReconcile_Uploading_CreatesUploadJob(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "aws",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "aws-secret"}},
			Region:      "eu-west-1",
		},
	}
	img := newImg("upload-pipeline", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{
		{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"},
			Format:            "vmdk",
		},
	}
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
		Metadata:  map[string]string{"backend": "qemu-img"},
	}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	result := reconcileOnce(t, r, "upload-pipeline", "default")
	if result.RequeueAfter == 0 {
		t.Fatalf("result = %+v, want requeue after upload job creation", result)
	}

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "upload-pipeline", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.UploadJobRef == nil || *updated.Status.UploadJobRef != "upload-pipeline-upload" {
		t.Fatalf("uploadJobRef = %#v, want upload-pipeline-upload", updated.Status.UploadJobRef)
	}
	if len(updated.Status.UploadOperations) != 1 {
		t.Fatalf("uploadOperations len = %d, want 1", len(updated.Status.UploadOperations))
	}
	op := updated.Status.UploadOperations[0]
	if op.Provider != "aws" || op.ProviderConfig != "aws-cfg" || op.Format != "vmdk" || op.Phase != "Uploading" {
		t.Fatalf("upload operation = %#v, want aws/aws-cfg/vmdk Uploading", op)
	}
	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "upload-pipeline-upload", Namespace: "default"}, job); err != nil {
		t.Fatalf("upload job was not created: %v", err)
	}
}

func TestReconcile_Uploading_UsesHealthyPlatformProviderService(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "custom",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "custom-secret"}},
		},
	}
	platformProvider := &v1alpha1.PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
		Status: v1alpha1.PlatformProviderStatus{
			Phase: "Healthy",
			Capabilities: &v1alpha1.ProviderCapabilities{
				ProviderName:    "custom",
				ProviderVersion: "v1.0.0",
				Formats:         []string{"raw"},
				OSFamilies:      []string{"linux"},
				ProtocolVersion: "v1",
			},
		},
	}
	img := newImg("upload-grpc", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{{
		ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "custom-cfg"},
		Format:            "raw",
	}}
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{Path: "/workspace/artifact.raw", Format: "raw", SizeBytes: 42, OS: "linux"}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, provCfg, platformProvider).Build()
	r := &vmimage.VMImageReconciler{
		Client:            c,
		Scheme:            s,
		Registry:          plugin.Default(),
		ProviderNamespace: "imagebuilder-system",
	}
	reconcileOnce(t, r, "upload-grpc", "default")

	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "upload-grpc-upload", Namespace: "default"}, job); err != nil {
		t.Fatalf("upload job was not created: %v", err)
	}
	var targets []uploadpod.TargetConfig
	if err := json.Unmarshal([]byte(envValue(job.Spec.Template.Spec.Containers[0].Env, "UPLOAD_TARGETS_JSON")), &targets); err != nil {
		t.Fatalf("parse UPLOAD_TARGETS_JSON: %v", err)
	}
	if len(targets) != 1 || targets[0].GRPC == nil || targets[0].GRPC.Address != "provider-custom.imagebuilder-system.svc:50051" {
		t.Fatalf("targets = %#v, want direct PlatformProvider route", targets)
	}
}

func TestReconcile_Uploading_MaterializesProviderMTLSSecret(t *testing.T) {
	provCfg := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-cfg", Namespace: "workloads"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "custom",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "custom-secret"}},
		},
	}
	platformProvider := &v1alpha1.PlatformProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
		Spec: v1alpha1.PlatformProviderSpec{
			Transport: &v1alpha1.ProviderTransportSpec{TLS: &v1alpha1.ProviderTransportTLSSpec{
				Mode:       "Mutual",
				ServerName: "custom.internal.test",
				CASecretRef: &v1alpha1.ProviderTLSSecretRef{
					Name: "provider-ca", Namespace: "imagebuilder-system", CAKey: "root.pem",
				},
				ClientCertificateSecretRef: &v1alpha1.ProviderTLSSecretRef{
					Name: "provider-client", Namespace: "imagebuilder-system", CertKey: "client.pem", KeyKey: "client-key.pem",
				},
			}},
		},
		Status: v1alpha1.PlatformProviderStatus{
			Phase: "Healthy",
			Capabilities: &v1alpha1.ProviderCapabilities{
				ProviderName: "custom", ProviderVersion: "v1.0.0", ProtocolVersion: "v1",
			},
		},
	}
	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-ca", Namespace: "imagebuilder-system"},
		Data:       map[string][]byte{"root.pem": []byte("ca-data")},
	}
	clientSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-client", Namespace: "imagebuilder-system"},
		Data: map[string][]byte{
			"client.pem":     []byte("cert-data"),
			"client-key.pem": []byte("key-data"),
		},
	}
	img := newImg("upload-mtls", "workloads", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Targets = []v1alpha1.TargetSpec{{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "custom-cfg"}, Format: "raw"}}
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{Path: "/workspace/artifact.raw", Format: "raw", SizeBytes: 42, OS: "linux"}
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(
		img, provCfg, platformProvider, caSecret, clientSecret,
	).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default(), ProviderNamespace: "imagebuilder-system"}
	reconcileOnce(t, r, "upload-mtls", "workloads")

	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "upload-mtls-upload", Namespace: "workloads"}, job); err != nil {
		t.Fatalf("upload job was not created: %v", err)
	}
	var targets []uploadpod.TargetConfig
	if err := json.Unmarshal([]byte(envValue(job.Spec.Template.Spec.Containers[0].Env, "UPLOAD_TARGETS_JSON")), &targets); err != nil {
		t.Fatalf("parse UPLOAD_TARGETS_JSON: %v", err)
	}
	if len(targets) != 1 || targets[0].GRPC == nil || targets[0].GRPC.TLS == nil || targets[0].GRPC.TLS.ServerName != "custom.internal.test" {
		t.Fatalf("targets = %#v, want mTLS PlatformProvider route", targets)
	}
	if len(job.Spec.Template.Spec.Volumes) != 3 || job.Spec.Template.Spec.Volumes[2].Secret == nil {
		t.Fatalf("upload Job volumes = %#v", job.Spec.Template.Spec.Volumes)
	}
	materialized := &corev1.Secret{}
	secretName := job.Spec.Template.Spec.Volumes[2].Secret.SecretName
	if err := c.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: "workloads"}, materialized); err != nil {
		t.Fatalf("materialized mTLS Secret not found: %v", err)
	}
	if string(materialized.Data["ca.crt"]) != "ca-data" || string(materialized.Data["tls.crt"]) != "cert-data" || string(materialized.Data["tls.key"]) != "key-data" {
		t.Fatalf("materialized Secret data = %#v", materialized.Data)
	}
	if len(materialized.OwnerReferences) != 1 || materialized.OwnerReferences[0].UID != img.UID {
		t.Fatalf("materialized Secret owner references = %#v", materialized.OwnerReferences)
	}
}

func TestReconcile_Uploading_UploadJobFailed_SetsFailed(t *testing.T) {
	jobName := "upload-fails-upload"
	img := newImg("upload-fails", "default", v1alpha1.PhaseUploading)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Status.UploadJobRef = &jobName
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "pvc"}
	img.Status.BuildArtifact = &v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 42,
		OS:        "linux",
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
		},
	}
	pod := failedUploadPod("upload-fails-pod", "default", jobName, `{"error":"provider aws upload failed"}`)

	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, job, pod).Build()
	r := &vmimage.VMImageReconciler{Client: c, Scheme: s, Registry: plugin.Default()}

	reconcileOnce(t, r, "upload-fails", "default")

	updated := &v1alpha1.VMImage{}
	c.Get(context.Background(), types.NamespacedName{Name: "upload-fails", Namespace: "default"}, updated) //nolint:errcheck
	if updated.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
	var found bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Failed" && cond.Message == "upload Job failed: provider aws upload failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed condition did not include upload termination detail: %#v", updated.Status.Conditions)
	}
}

// ---------------------------------------------------------------------------
// Fake plugin for registry (minimal — only Name() matters)
// ---------------------------------------------------------------------------

func remoteTestImage(name string) (*v1alpha1.VMImage, *v1alpha1.ProviderConfig) {
	providerConfig := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws"},
	}
	img := newImg(name, "default", v1alpha1.PhasePending)
	img.Finalizers = []string{"imagebuilder.io/cleanup"}
	img.Spec.Build.Mode = v1alpha1.BuildModeRemote
	img.Spec.Source.URL = ""
	img.Spec.Source.ProviderRef = "ami-0123456789abcdef0"
	img.Spec.Targets = []v1alpha1.TargetSpec{{
		ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: providerConfig.Name},
		Format:            "ami",
	}}
	return img, providerConfig
}

func remoteTestReconciler(t *testing.T, img *v1alpha1.VMImage, providerConfig *v1alpha1.ProviderConfig, providerPlugin *fakeRemoteBuildPlugin) (*vmimage.VMImageReconciler, client.Client) {
	t.Helper()
	registry := plugin.NewRegistry(slog.Default())
	if err := registry.Register(providerPlugin); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	scheme := testScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.VMImage{}).WithObjects(img, providerConfig).Build()
	return &vmimage.VMImageReconciler{Client: kubeClient, Scheme: scheme, Registry: registry}, kubeClient
}

type fakeRegistryPlugin struct {
	name           string
	initErr        error
	validateErr    error
	validateCalls  int
	uploadResult   *platform.UploadResult
	uploadErr      error
	registerErr    error
	imageRef       *platform.ImageRef
	uploadCalls    int
	registerCalls  int
	cleanupCalls   int
	uploadArtifact *platform.BuildArtifact
}

func (p *fakeRegistryPlugin) Name() string                                          { return p.name }
func (p *fakeRegistryPlugin) Version() string                                       { return "v0.0.1" }
func (p *fakeRegistryPlugin) SupportedFormats() []platform.ImageFormat              { return nil }
func (p *fakeRegistryPlugin) SupportedOS() []platform.OSFamily                      { return nil }
func (p *fakeRegistryPlugin) Init(_ context.Context, _ platform.PluginConfig) error { return p.initErr }
func (p *fakeRegistryPlugin) Validate(_ context.Context, _ v1alpha1.TargetSpec) error {
	p.validateCalls++
	return p.validateErr
}
func (p *fakeRegistryPlugin) Upload(_ context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	p.uploadCalls++
	p.uploadArtifact = artifact
	if p.uploadErr != nil {
		return nil, p.uploadErr
	}
	if p.uploadResult != nil {
		return p.uploadResult, nil
	}
	return &platform.UploadResult{}, nil
}
func (p *fakeRegistryPlugin) Register(_ context.Context, _ *platform.UploadResult) (*platform.ImageRef, error) {
	p.registerCalls++
	if p.registerErr != nil {
		return nil, p.registerErr
	}
	if p.imageRef != nil {
		return p.imageRef, nil
	}
	return &platform.ImageRef{}, nil
}
func (p *fakeRegistryPlugin) Cleanup(_ context.Context, _ *platform.BuildArtifact) error {
	p.cleanupCalls++
	return nil
}
func (p *fakeRegistryPlugin) HealthCheck(_ context.Context) error { return nil }

type fakeRemoteBuildPlugin struct {
	fakeRegistryPlugin
	result         *platform.RemoteBuildResult
	remoteErr      error
	remoteCalls    int
	lastRequest    *platform.RemoteBuildRequest
	cleanupCalls   int
	cleanupRequest *platform.RemoteBuildRequest
	cleanupErr     error
}

func (p *fakeRemoteBuildPlugin) SupportedBuildModes() []string {
	return []string{v1alpha1.BuildModeLocal, v1alpha1.BuildModeRemote}
}

func (p *fakeRemoteBuildPlugin) ReconcileRemoteBuild(_ context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	p.remoteCalls++
	p.lastRequest = req
	if p.remoteErr != nil {
		return nil, p.remoteErr
	}
	if p.result != nil {
		return p.result, nil
	}
	return &platform.RemoteBuildResult{Phase: platform.RemoteBuildPhaseBooting, Message: "booting"}, nil
}

func (p *fakeRemoteBuildPlugin) CleanupRemoteBuild(_ context.Context, req *platform.RemoteBuildRequest) error {
	p.cleanupCalls++
	p.cleanupRequest = req
	return p.cleanupErr
}

func conditionReason(img *v1alpha1.VMImage, conditionType string) string {
	for _, cond := range img.Status.Conditions {
		if cond.Type == conditionType {
			return cond.Reason
		}
	}
	return ""
}
