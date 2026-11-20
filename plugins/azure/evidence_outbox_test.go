package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

const testSASURL = "https://acct.blob.core.windows.net/c/p/evidence/b.tar.gz?sv=2023&sp=cw&sig=SECRETSIG%3D"

type fakeRunner struct {
	scripts []string
	vms     []string
	run     func(script string) ([]*armcompute.InstanceViewStatus, error)
}

func (f *fakeRunner) RunShellScript(_ context.Context, vmName, script string) ([]*armcompute.InstanceViewStatus, error) {
	f.vms = append(f.vms, vmName)
	f.scripts = append(f.scripts, script)
	if f.run != nil {
		return f.run(script)
	}
	return statuses("Enable succeeded: [stdout]\n" + evidenceUploadMarker), nil
}

func statuses(message string) []*armcompute.InstanceViewStatus {
	return []*armcompute.InstanceViewStatus{{Code: to.Ptr("ProvisioningState/succeeded"), Message: to.Ptr(message)}}
}

type fakeStore struct {
	blobs     map[string][]byte
	sasErr    error
	sizeErr   error
	readErr   error
	deleteErr error
	deleted   []string
	// onSAS simulates the guest upload: the bytes the guest PUTs.
	upload []byte
}

func (f *fakeStore) WriteSASURL(_ context.Context, blobName string, lifetime time.Duration) (string, error) {
	if f.sasErr != nil {
		return "", f.sasErr
	}
	if lifetime > 15*time.Minute {
		return "", errors.New("SAS lifetime too long")
	}
	if f.upload != nil {
		if f.blobs == nil {
			f.blobs = map[string][]byte{}
		}
		f.blobs[blobName] = f.upload
	}
	return testSASURL, nil
}

func (f *fakeStore) BlobSize(_ context.Context, blobName string) (int64, error) {
	if f.sizeErr != nil {
		return 0, f.sizeErr
	}
	data, ok := f.blobs[blobName]
	if !ok {
		return 0, errEvidenceBlobNotFound
	}
	return int64(len(data)), nil
}

func (f *fakeStore) ReadBlob(_ context.Context, blobName string, limit int64) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	data, ok := f.blobs[blobName]
	if !ok {
		return nil, errEvidenceBlobNotFound
	}
	if int64(len(data)) > limit {
		data = data[:limit]
	}
	return data, nil
}

func (f *fakeStore) DeleteBlob(_ context.Context, blobName string) error {
	f.deleted = append(f.deleted, blobName)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.blobs, blobName)
	return nil
}

func testBundle(t *testing.T) []byte {
	t.Helper()
	data, err := evidence.Pack(map[string][]byte{
		evidence.SBOMFile:  []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`),
		evidence.TrivyFile: []byte(`{"SchemaVersion":2,"Results":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRedactSAS(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "full url", text: "curl failed for " + testSASURL},
		{name: "query only", text: "query sv=2023&sp=cw&sig=SECRETSIG%3D leaked"},
		{name: "other signature", text: "https://x/y?sig=OTHERSECRET&se=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactSAS(tt.text, testSASURL)
			if strings.Contains(got, "SECRETSIG") || strings.Contains(got, "OTHERSECRET") {
				t.Fatalf("redactSAS leaked a signature: %q", got)
			}
		})
	}
}

func TestAzureEvidenceUploadScript(t *testing.T) {
	tests := []struct {
		name      string
		proxy     string
		wantParts []string
		notParts  []string
	}{
		{
			name: "direct only",
			wantParts: []string{
				"tar czf - -C /var/lib/imagebuilder evidence | curl --fail --silent --show-error",
				"-H 'x-ms-blob-type: BlockBlob' --data-binary @-",
				"ib_put --noproxy '*'",
				"unset http_proxy https_proxy",
				"rm -rf \"$outbox\"",
				"test ! -e \"$outbox\"",
				"test -s \"$outbox/sbom.cdx.json\"",
				"test -s \"$outbox/trivy-report.json\"",
				evidenceUploadMarker,
			},
			notParts: []string{"--proxy "},
		},
		{
			name:      "with proxy fallback",
			proxy:     "http://proxy.example:3128",
			wantParts: []string{"ib_put --proxy 'http://proxy.example:3128'"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := azureEvidenceUploadScript(testSASURL, tt.proxy)
			for _, part := range tt.wantParts {
				if !strings.Contains(script, part) {
					t.Fatalf("script missing %q:\n%s", part, script)
				}
			}
			for _, part := range tt.notParts {
				if strings.Contains(script, part) {
					t.Fatalf("script must not contain %q:\n%s", part, script)
				}
			}
		})
	}
}

func TestCollectAzureEvidence(t *testing.T) {
	big := make([]byte, evidence.MaxBundleBytes+1)
	tests := []struct {
		name    string
		store   func(t *testing.T) *fakeStore
		runner  *fakeRunner
		wantErr string
	}{
		{name: "success", store: func(t *testing.T) *fakeStore { return &fakeStore{upload: testBundle(t)} }},
		{name: "sas fails", store: func(*testing.T) *fakeStore { return &fakeStore{sasErr: errors.New("no delegation")} }, wantErr: "create evidence upload SAS"},
		{name: "stale delete fails", store: func(*testing.T) *fakeStore { return &fakeStore{deleteErr: errors.New("denied")} }, wantErr: "remove stale evidence blob"},
		{
			name:  "run command error is redacted",
			store: func(t *testing.T) *fakeStore { return &fakeStore{upload: testBundle(t)} },
			runner: &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) {
				return nil, errors.New("transport failed for " + testSASURL)
			}},
			wantErr: "upload evidence outbox",
		},
		{
			name:  "guest upload fails",
			store: func(t *testing.T) *fakeStore { return &fakeStore{upload: testBundle(t)} },
			runner: &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) {
				return []*armcompute.InstanceViewStatus{{Code: to.Ptr("ProvisioningState/failed"), Message: to.Ptr("curl: (22) 403 sig=SECRETSIG%3D")}}, nil
			}},
			wantErr: "evidence outbox upload failed",
		},
		{
			name:  "no completion marker",
			store: func(t *testing.T) *fakeStore { return &fakeStore{upload: testBundle(t)} },
			runner: &fakeRunner{run: func(string) ([]*armcompute.InstanceViewStatus, error) {
				return statuses("nothing"), nil
			}},
			wantErr: "did not confirm completion",
		},
		{name: "blob missing", store: func(*testing.T) *fakeStore { return &fakeStore{} }, wantErr: "read uploaded evidence bundle size"},
		{name: "too large", store: func(*testing.T) *fakeStore { return &fakeStore{upload: big} }, wantErr: "exceeds the limit"},
		{name: "download fails", store: func(t *testing.T) *fakeStore { return &fakeStore{upload: testBundle(t), readErr: errors.New("boom")} }, wantErr: "download evidence bundle"},
		{name: "invalid bundle", store: func(*testing.T) *fakeStore { return &fakeStore{upload: []byte("junk")} }, wantErr: "not a gzip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store(t)
			runner := tt.runner
			if runner == nil {
				runner = &fakeRunner{}
			}
			sha, err := collectAzureEvidence(context.Background(), runner, store, "ib-x-vm", "p/evidence/x.tar.gz", "")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("collectAzureEvidence() error = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "SECRETSIG") {
					t.Fatalf("error leaks the SAS: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("collectAzureEvidence() error = %v", err)
			}
			if sha != evidence.SHA256Hex(store.blobs["p/evidence/x.tar.gz"]) {
				t.Fatalf("sha = %q", sha)
			}
			if len(runner.vms) != 1 || runner.vms[0] != "ib-x-vm" {
				t.Fatalf("runner vms = %v", runner.vms)
			}
		})
	}
}

func TestAzureDoneEvidenceBundle(t *testing.T) {
	bundle := testBundle(t)
	sha := evidence.SHA256Hex(bundle)
	ref := azureRemoteOperationRef{EvidenceBlob: "b", EvidenceSHA256: sha}
	tests := []struct {
		name       string
		store      *fakeStore
		ref        azureRemoteOperationRef
		ack        bool
		wantBundle bool
		wantErr    string
	}{
		{name: "returns bundle", store: &fakeStore{blobs: map[string][]byte{"b": bundle}}, ref: ref, wantBundle: true},
		{name: "ack deletes", store: &fakeStore{blobs: map[string][]byte{"b": bundle}}, ref: ref, ack: true},
		{name: "ack delete fails", store: &fakeStore{deleteErr: errors.New("x")}, ref: ref, ack: true, wantErr: "delete acknowledged"},
		{name: "not collected", store: &fakeStore{}, wantErr: "EvidenceBundleMissing: "},
		{name: "lost", store: &fakeStore{}, ref: ref, wantErr: "EvidenceBundleLost: "},
		{name: "read error", store: &fakeStore{readErr: errors.New("boom")}, ref: ref, wantErr: "download persisted"},
		{name: "digest mismatch", store: &fakeStore{blobs: map[string][]byte{"b": bundle}}, ref: azureRemoteOperationRef{EvidenceBlob: "b", EvidenceSHA256: strings.Repeat("0", 64)}, wantErr: "EvidenceBundleLost: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := azureDoneEvidenceBundle(context.Background(), tt.store, tt.ref, tt.ack)
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantBundle != (got != nil) {
				t.Fatalf("bundle = %v, want present %v", got, tt.wantBundle)
			}
			if got != nil && (got.SHA256 != sha || got.MediaType != evidence.MediaType) {
				t.Fatalf("bundle = %+v", got)
			}
			if tt.ack && (len(tt.store.deleted) != 1 || tt.store.blobs["b"] != nil) {
				t.Fatalf("ack did not delete the blob: %v", tt.store.deleted)
			}
		})
	}
}

func TestAzureDoneState(t *testing.T) {
	bundle := testBundle(t)
	controller := &v1alpha1.EvidenceSpec{Publisher: v1alpha1.EvidencePublisherController}
	image := &platform.ImageRef{ID: "img"}
	ref := azureRemoteOperationRef{BuildID: "b", ImageID: "img", EvidenceBlob: "blob", EvidenceSHA256: evidence.SHA256Hex(bundle), VerifyBoot: verifyBootPassed}

	state, err := azureDoneState(context.Background(), &fakeStore{blobs: map[string][]byte{"blob": bundle}}, azureRemoteBuildInput{Evidence: controller}, ref, image, "ok")
	if err != nil || !state.Done || state.EvidenceBundle == nil {
		t.Fatalf("state = %+v, err = %v", state, err)
	}
	if !containsString(state.Hygiene.Checks, verifyBootCheck) || !containsString(state.Hygiene.Checks, "azure-evidence-outbox-retrieved") {
		t.Fatalf("hygiene checks = %v", state.Hygiene.Checks)
	}

	if _, err := azureDoneState(context.Background(), &fakeStore{}, azureRemoteBuildInput{Evidence: controller}, ref, image, "ok"); err == nil || providererrors.IsTransient(err) {
		t.Fatalf("lost bundle error = %v, want terminal", err)
	}

	state, err = azureDoneState(context.Background(), &fakeStore{}, azureRemoteBuildInput{}, azureRemoteOperationRef{}, image, "ok")
	if err != nil || state.EvidenceBundle != nil || containsString(state.Hygiene.Checks, verifyBootCheck) {
		t.Fatalf("provider-mode state = %+v, err = %v", state, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBlobEvidenceStoreWriteSASURL(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	credential, err := azblob.NewSharedKeyCredential("acct", key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store := &blobEvidenceStore{sharedKey: credential, storageURL: "https://acct.blob.core.windows.net", container: "stage", now: func() time.Time { return now }}
	raw, err := store.WriteSASURL(context.Background(), "prefix/evidence/build 1.tar.gz", evidenceSASLifetime)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.EscapedPath() != "/stage/prefix/evidence/build%201.tar.gz" {
		t.Fatalf("path = %q", u.EscapedPath())
	}
	q := u.Query()
	if q.Get("sp") != "cw" || q.Get("sr") != "b" || q.Get("spr") != "https" || q.Get("sig") == "" {
		t.Fatalf("SAS query = %v, want create+write blob SAS over https", q)
	}
	expiry, err := time.Parse(time.RFC3339, q.Get("se"))
	if err != nil || expiry.Sub(now) > 15*time.Minute {
		t.Fatalf("SAS expiry = %v (%v), want <= 15m", expiry, err)
	}
}

func TestAzureEvidenceBlobName(t *testing.T) {
	tests := []struct{ prefix, build, want string }{
		{"", "UID-1", "evidence/uid-1.tar.gz"},
		{"/imagebuilder/", "abc", "imagebuilder/evidence/abc.tar.gz"},
	}
	for _, tt := range tests {
		if got := azureEvidenceBlobName(tt.prefix, tt.build); got != tt.want {
			t.Fatalf("azureEvidenceBlobName(%q,%q) = %q, want %q", tt.prefix, tt.build, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if truncate("abcdef", 3) != "abc" || truncate("ab", 3) != "ab" {
		t.Fatal("truncate")
	}
}
