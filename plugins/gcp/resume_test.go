package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
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

func TestValidateGCSResumeOriginRejectsForeignHost(t *testing.T) {
	err := validateGCSResumeOrigin("https://attacker.example/upload/1", "https://storage.googleapis.com/upload/storage/v1")
	if err == nil {
		t.Fatal("foreign resumable session origin was accepted")
	}
}

func TestPrepareGCSResumeSessionRotatesExpiredBackendSession(t *testing.T) {
	var server *httptest.Server
	postCalls := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/expired" {
			w.WriteHeader(http.StatusGone)
			return
		}
		if r.Method == http.MethodPost {
			postCalls++
			w.Header().Set("Location", server.URL+"/session/new")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer server.Close()
	existing, err := json.Marshal(gcsResumeSession{SessionURI: server.URL + "/expired", Bucket: "bucket", Object: "object", Size: 1024, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	client := &sdkClient{httpClient: server.Client(), uploadEndpoint: server.URL}
	state, err := client.prepareGCSResumeSession(context.Background(), "bucket", "object", 1024, string(existing))
	if err != nil {
		t.Fatalf("prepareGCSResumeSession returned error: %v", err)
	}
	if state.SessionURI != server.URL+"/session/new" || postCalls != 1 || !state.CreatedAt.After(time.Time{}) {
		t.Fatalf("rotated state = %#v, postCalls=%d", state, postCalls)
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
