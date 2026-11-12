package vsphere

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeriveGuestPasswordIsDeterministicPerBuild(t *testing.T) {
	a := deriveGuestPassword("vc-secret", "build-1")
	if a != deriveGuestPassword("vc-secret", "build-1") {
		t.Fatal("password must be deterministic for the same build")
	}
	if a == deriveGuestPassword("vc-secret", "build-2") {
		t.Fatal("password must differ per build")
	}
	if a == deriveGuestPassword("other-secret", "build-1") {
		t.Fatal("password must depend on the provider secret")
	}
	if len(a) < 24 {
		t.Fatalf("password too short: %d", len(a))
	}
	if strings.Contains(a, "vc-secret") {
		t.Fatal("password must not embed the provider secret")
	}
}

func TestCloudInitUserDataSetsRootPasswordOnly(t *testing.T) {
	ud := cloudInitUserData("S3cretPw")
	for _, want := range []string{"#cloud-config", "disable_root: false", "ssh_pwauth: false", "name: root", "S3cretPw"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing %q:\n%s", want, ud)
		}
	}
}

func TestOVFUserDataPropertyIsBase64(t *testing.T) {
	value := ovfUserDataProperty("#cloud-config\nfoo: bar\n")
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("value is not base64: %v", err)
	}
	if string(decoded) != "#cloud-config\nfoo: bar\n" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestParseSHA256(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"sha256:" + sum, sum, false},
		{"SHA256:" + strings.ToUpper(sum), sum, false},
		{sum, sum, false},
		{"md5:abc", "", true},
		{"sha256:zz", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := parseSHA256(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Fatalf("parseSHA256(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestDownloadVerifiedSource(t *testing.T) {
	body := []byte("fake ova content")
	sum := sha256.Sum256(body)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	checksum := "sha256:" + hex.EncodeToString(sum[:])

	path, err := downloadVerifiedSource(context.Background(), srv.Client(), srv.URL+"/ubuntu.ova", checksum, dir)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if filepath.Base(path) != "ubuntu.ova" {
		t.Fatalf("path = %q", path)
	}
	// Second call reuses the verified file without another download.
	if _, err := downloadVerifiedSource(context.Background(), srv.Client(), srv.URL+"/ubuntu.ova", checksum, dir); err != nil {
		t.Fatalf("second download: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server hits = %d, want 1 (cached)", got)
	}
}

func TestDownloadVerifiedSourceRejectsChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	_, err := downloadVerifiedSource(context.Background(), srv.Client(), srv.URL+"/x.ova", "sha256:"+strings.Repeat("0", 64), dir)
	assertErrContains(t, err, "checksum")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("mismatching download must not stay on disk: %v", entries)
	}
}

func TestDownloadVerifiedSourceRequiresHTTPSAndChecksum(t *testing.T) {
	dir := t.TempDir()
	_, err := downloadVerifiedSource(context.Background(), http.DefaultClient, "http://example.com/x.ova", "sha256:"+strings.Repeat("0", 64), dir)
	assertErrContains(t, err, "https")
	_, err = downloadVerifiedSource(context.Background(), http.DefaultClient, "https://example.com/x.ova", "", dir)
	assertErrContains(t, err, "checksum")
}

func TestSourceFileNameIsSafe(t *testing.T) {
	if got := sourceFileName("https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.ova"); got != "ubuntu-26.04-server-cloudimg-amd64.ova" {
		t.Fatalf("got %q", got)
	}
	if got := sourceFileName("https://x/..%2f..%2fetc"); strings.Contains(got, "/") || strings.Contains(got, "..") {
		t.Fatalf("unsafe name %q", got)
	}
}
