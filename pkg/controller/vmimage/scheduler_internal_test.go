package vmimage

import (
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildLeaseExpired(t *testing.T) {
	holder := "default/test/uid"
	duration := int32(60)
	oldRenew := metav1.NewMicroTime(time.Now().Add(-2 * time.Minute))
	freshRenew := metav1.NewMicroTime(time.Now())

	tests := []struct {
		name  string
		lease *coordinationv1.Lease
		want  bool
	}{
		{
			name: "expired",
			lease: &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &duration,
				RenewTime:            &oldRenew,
			}},
			want: true,
		},
		{
			name: "fresh",
			lease: &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &duration,
				RenewTime:            &freshRenew,
			}},
			want: false,
		},
		{
			name: "empty holder",
			lease: &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
				LeaseDurationSeconds: &duration,
				RenewTime:            &freshRenew,
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildLeaseExpired(tt.lease, time.Now()); got != tt.want {
				t.Fatalf("buildLeaseExpired = %v, want %v", got, tt.want)
			}
		})
	}
}
