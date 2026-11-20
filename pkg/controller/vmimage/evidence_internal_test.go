package vmimage

import (
	"errors"
	"testing"
)

func TestRemoteFailureReason(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: nil, want: "RemoteBuildFailed"},
		{err: errors.New("boom"), want: "RemoteBuildFailed"},
		{err: errors.New("desc = VerifyBootFailed: cloud-init degraded"), want: "VerifyBootFailed"},
		{err: errors.New("EvidenceBundleTooLarge: 1 MiB"), want: "RemoteEvidenceRejected"},
		{err: errors.New("EvidenceCollectionFailed: tar"), want: "RemoteEvidenceRejected"},
		{err: errors.New("mentions VerifyBootFailed without colon"), want: "RemoteBuildFailed"},
	}
	for _, tt := range tests {
		if got := remoteFailureReason(tt.err); got != tt.want {
			t.Fatalf("remoteFailureReason(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
