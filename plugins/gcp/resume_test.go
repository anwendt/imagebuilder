package gcp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGCSResumeSessionUploadsAndCheckpointsRanges(t *testing.T) {
	var server *httptest.Server
	var ranges []string
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("Location", server.URL+"/session/1")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/session/1":
			ranges = append(ranges, r.Header.Get("Content-Range"))
			if len(ranges) == 1 {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", gcsResumeChunkSize-1))
				w.WriteHeader(http.StatusPermanentRedirect)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	size := gcsResumeChunkSize + 1024
	client := &sdkClient{httpClient: server.Client(), uploadEndpoint: server.URL}
	state, err := client.prepareGCSResumeSession(context.Background(), "bucket", "object.tar.gz", size, "")
	if err != nil {
		t.Fatalf("prepareGCSResumeSession returned error: %v", err)
	}
	var checkpoints []int64
	if err := client.uploadGCSResume(context.Background(), bytes.NewReader(make([]byte, size)), &state, func(updated gcsResumeSession) error {
		checkpoints = append(checkpoints, updated.Offset)
		return nil
	}); err != nil {
		t.Fatalf("uploadGCSResume returned error: %v", err)
	}
	wantRanges := []string{
		fmt.Sprintf("bytes 0-%d/%d", gcsResumeChunkSize-1, size),
		fmt.Sprintf("bytes %d-%d/%d", gcsResumeChunkSize, size-1, size),
	}
	if !reflect.DeepEqual(ranges, wantRanges) {
		t.Fatalf("ranges = %#v, want %#v", ranges, wantRanges)
	}
	if !reflect.DeepEqual(checkpoints, []int64{gcsResumeChunkSize, size}) {
		t.Fatalf("checkpoints = %#v", checkpoints)
	}
}

func TestGCSRangeOffset(t *testing.T) {
	if got := gcsRangeOffset("bytes=0-1048575"); got != 1048576 {
		t.Fatalf("offset = %d", got)
	}
	if got := gcsRangeOffset(""); got != 0 {
		t.Fatalf("empty range offset = %d", got)
	}
}
