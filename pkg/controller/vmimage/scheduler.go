package vmimage

import (
	"context"
	"fmt"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

const (
	defaultMaxConcurrentBuilds       = 3
	defaultBuildLeaseDurationSeconds = int32(6 * 60 * 60)

	buildLeasePrefix = "imagebuilder-build"
)

type buildSlotAcquisition struct {
	Acquired bool
	Reason   string
	Refs     []string
}

func (r *VMImageReconciler) acquireBuildSlots(ctx context.Context, img *v1alpha1.VMImage) (buildSlotAcquisition, error) {
	if len(img.Status.BuildLeaseRefs) > 0 {
		return buildSlotAcquisition{Acquired: true, Refs: img.Status.BuildLeaseRefs}, nil
	}

	namespace := r.schedulerNamespace(img)
	holder := leaseHolder(img)
	globalMax := r.maxConcurrentBuilds()
	var refs []string
	if globalMax > 0 {
		ref, ok, err := r.acquireLeaseSlot(ctx, namespace, "global", "", globalMax, holder, img)
		if err != nil {
			return buildSlotAcquisition{}, err
		}
		if !ok {
			return buildSlotAcquisition{Reason: fmt.Sprintf("global build concurrency limit %d reached", globalMax)}, nil
		}
		refs = append(refs, ref)
	}

	return buildSlotAcquisition{Acquired: true, Refs: refs}, nil
}

func (r *VMImageReconciler) acquireLeaseSlot(ctx context.Context, namespace, scope, key string, max int, holder string, img *v1alpha1.VMImage) (string, bool, error) {
	for slot := 0; slot < max; slot++ {
		name := globalLeaseName(slot)
		existing := &coordinationv1.Lease{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, existing)
		if apierrors.IsNotFound(err) {
			lease := buildLease(namespace, name, scope, key, slot, holder, img)
			if err := r.Create(ctx, lease); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return "", false, fmt.Errorf("create build lease %s/%s: %w", namespace, name, err)
			}
			return leaseRef(namespace, name), true, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("get build lease %s/%s: %w", namespace, name, err)
		}
		if existing.Spec.HolderIdentity != nil && *existing.Spec.HolderIdentity == holder {
			return leaseRef(namespace, name), true, nil
		}
		if buildLeaseExpired(existing, time.Now()) {
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return "", false, fmt.Errorf("delete expired build lease %s/%s: %w", namespace, name, err)
			}
			lease := buildLease(namespace, name, scope, key, slot, holder, img)
			if err := r.Create(ctx, lease); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return "", false, fmt.Errorf("create build lease %s/%s after expiry: %w", namespace, name, err)
			}
			return leaseRef(namespace, name), true, nil
		}
	}
	return "", false, nil
}

func (r *VMImageReconciler) releaseImageBuildSlots(ctx context.Context, img *v1alpha1.VMImage) error {
	if len(img.Status.BuildLeaseRefs) == 0 {
		return nil
	}
	return r.releaseBuildSlots(ctx, img.Status.BuildLeaseRefs, leaseHolder(img))
}

func (r *VMImageReconciler) renewImageBuildSlots(ctx context.Context, img *v1alpha1.VMImage) error {
	if len(img.Status.BuildLeaseRefs) == 0 {
		return nil
	}
	return r.renewBuildSlots(ctx, img.Status.BuildLeaseRefs, leaseHolder(img), img)
}

func (r *VMImageReconciler) renewBuildSlots(ctx context.Context, refs []string, holder string, img *v1alpha1.VMImage) error {
	now := metav1.NewMicroTime(time.Now())
	duration := defaultBuildLeaseDurationSeconds
	for _, ref := range refs {
		namespace, name, err := splitLeaseRef(ref)
		if err != nil {
			return err
		}
		lease := &coordinationv1.Lease{}
		err = r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, lease)
		if apierrors.IsNotFound(err) {
			lease = recoveredBuildLease(namespace, name, holder, img, now, duration)
			if err := r.Create(ctx, lease); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("recreate missing build lease %s: %w", ref, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("get build lease %s for renewal: %w", ref, err)
		}
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != holder && !buildLeaseExpired(lease, time.Now()) {
			return fmt.Errorf("build lease %s is held by %q, expected %q", ref, *lease.Spec.HolderIdentity, holder)
		}
		lease.Spec.HolderIdentity = &holder
		if lease.Spec.AcquireTime == nil {
			lease.Spec.AcquireTime = &now
		}
		if lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds <= 0 {
			lease.Spec.LeaseDurationSeconds = &duration
		}
		lease.Spec.RenewTime = &now
		if lease.Labels == nil {
			lease.Labels = map[string]string{}
		}
		lease.Labels["app.kubernetes.io/managed-by"] = "imagebuilder"
		lease.Labels["imagebuilder.io/lease-type"] = "build-slot"
		lease.Labels["imagebuilder.io/vmimage"] = img.Name
		lease.Labels["imagebuilder.io/vmimage-ns"] = img.Namespace
		if err := r.Update(ctx, lease); err != nil {
			return fmt.Errorf("renew build lease %s: %w", ref, err)
		}
	}
	return nil
}

func (r *VMImageReconciler) releaseBuildSlots(ctx context.Context, refs []string, holder string) error {
	for _, ref := range refs {
		namespace, name, err := splitLeaseRef(ref)
		if err != nil {
			return err
		}
		lease := &coordinationv1.Lease{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, lease); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("get build lease %s: %w", ref, err)
		}
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != holder {
			continue
		}
		if err := r.Delete(ctx, lease); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete build lease %s: %w", ref, err)
		}
	}
	return nil
}

func (r *VMImageReconciler) schedulerNamespace(img *v1alpha1.VMImage) string {
	if r.SchedulerNamespace != "" {
		return r.SchedulerNamespace
	}
	return img.Namespace
}

func (r *VMImageReconciler) maxConcurrentBuilds() int {
	if r.MaxConcurrentBuilds < 0 {
		return 0
	}
	if r.MaxConcurrentBuilds == 0 {
		return defaultMaxConcurrentBuilds
	}
	return r.MaxConcurrentBuilds
}

func buildLease(namespace, name, scope, key string, slot int, holder string, img *v1alpha1.VMImage) *coordinationv1.Lease {
	now := metav1.NewMicroTime(time.Now())
	duration := defaultBuildLeaseDurationSeconds
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "imagebuilder",
				"imagebuilder.io/lease-type":   "build-slot",
				"imagebuilder.io/scope":        scope,
				"imagebuilder.io/slot":         fmt.Sprintf("%d", slot),
				"imagebuilder.io/vmimage":      img.Name,
				"imagebuilder.io/vmimage-ns":   img.Namespace,
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			AcquireTime:          &now,
			RenewTime:            &now,
		},
	}
}

func recoveredBuildLease(namespace, name, holder string, img *v1alpha1.VMImage, now metav1.MicroTime, duration int32) *coordinationv1.Lease {
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "imagebuilder",
				"imagebuilder.io/lease-type":   "build-slot",
				"imagebuilder.io/vmimage":      img.Name,
				"imagebuilder.io/vmimage-ns":   img.Namespace,
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			AcquireTime:          &now,
			RenewTime:            &now,
		},
	}
}

func leaseHolder(img *v1alpha1.VMImage) string {
	return fmt.Sprintf("%s/%s/%s", img.Namespace, img.Name, img.UID)
}

func globalLeaseName(slot int) string {
	return fmt.Sprintf("%s-global-%d", buildLeasePrefix, slot)
}

func leaseRef(namespace, name string) string {
	return namespace + "/" + name
}

func splitLeaseRef(ref string) (string, string, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid build lease ref %q", ref)
	}
	return parts[0], parts[1], nil
}

func buildLeaseExpired(lease *coordinationv1.Lease, now time.Time) bool {
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return true
	}
	if lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds <= 0 {
		return false
	}
	var lastRenew time.Time
	if lease.Spec.RenewTime != nil {
		lastRenew = lease.Spec.RenewTime.Time
	} else if lease.Spec.AcquireTime != nil {
		lastRenew = lease.Spec.AcquireTime.Time
	}
	if lastRenew.IsZero() {
		return false
	}
	return now.After(lastRenew.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second))
}

var _ client.Object = &coordinationv1.Lease{}
