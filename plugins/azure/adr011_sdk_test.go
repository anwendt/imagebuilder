package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

// routeTransport answers requests by "METHOD substring" routes.
type routeTransport struct {
	routes   map[string]func(*http.Request) *http.Response
	requests []string
}

func (rt *routeTransport) Do(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.Path
	rt.requests = append(rt.requests, key)
	for route, handler := range rt.routes {
		parts := strings.SplitN(route, " ", 2)
		if parts[0] == req.Method && strings.Contains(req.URL.Path, parts[1]) {
			resp := handler(req)
			resp.Request = req
			return resp, nil
		}
	}
	return response(req, http.StatusNotFound, `{"error":{"code":"NotFound"}}`, map[string]string{"x-ms-error-code": "BlobNotFound"}), nil
}

func response(req *http.Request, status int, body string, headers map[string]string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req, ContentLength: int64(len(body))}
}

type fakeTokenCredential struct{}

func (fakeTokenCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "t", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func noRetry(rt *routeTransport) policy.ClientOptions {
	return policy.ClientOptions{Transport: rt, Retry: policy.RetryOptions{MaxRetries: -1}}
}

func testBlobStore(t *testing.T, rt *routeTransport) *blobEvidenceStore {
	t.Helper()
	cred, err := azblob.NewSharedKeyCredential("acct", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential("https://acct.blob.core.windows.net", cred, &azblob.ClientOptions{ClientOptions: noRetry(rt)})
	if err != nil {
		t.Fatal(err)
	}
	return &blobEvidenceStore{blobs: client, sharedKey: cred, storageURL: "https://acct.blob.core.windows.net", container: "stage"}
}

func TestBlobEvidenceStore_SDK(t *testing.T) {
	rt := &routeTransport{routes: map[string]func(*http.Request) *http.Response{
		"HEAD /stage/present": func(r *http.Request) *http.Response {
			return response(r, http.StatusOK, "", map[string]string{"Content-Length": "5"})
		},
		"GET /stage/present": func(r *http.Request) *http.Response {
			return response(r, http.StatusOK, "hello", map[string]string{"Content-Type": "application/octet-stream"})
		},
		"DELETE /stage/present": func(r *http.Request) *http.Response { return response(r, http.StatusAccepted, "", nil) },
		"HEAD /stage/broken": func(r *http.Request) *http.Response {
			return response(r, http.StatusForbidden, "", map[string]string{"x-ms-error-code": "AuthorizationFailure"})
		},
		"DELETE /stage/broken": func(r *http.Request) *http.Response {
			return response(r, http.StatusForbidden, "", map[string]string{"x-ms-error-code": "AuthorizationFailure"})
		},
		"GET /stage/broken": func(r *http.Request) *http.Response {
			return response(r, http.StatusForbidden, "", map[string]string{"x-ms-error-code": "AuthorizationFailure"})
		},
	}}
	store := testBlobStore(t, rt)
	ctx := context.Background()
	if size, err := store.BlobSize(ctx, "present"); err != nil || size != 5 {
		t.Fatalf("BlobSize = %d, %v", size, err)
	}
	if data, err := store.ReadBlob(ctx, "present", 3); err != nil || string(data) != "hel" {
		t.Fatalf("ReadBlob = %q, %v", data, err)
	}
	if err := store.DeleteBlob(ctx, "present"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BlobSize(ctx, "missing"); !errors.Is(err, errEvidenceBlobNotFound) {
		t.Fatalf("BlobSize missing = %v", err)
	}
	if _, err := store.ReadBlob(ctx, "missing", 3); !errors.Is(err, errEvidenceBlobNotFound) {
		t.Fatalf("ReadBlob missing = %v", err)
	}
	if err := store.DeleteBlob(ctx, "missing"); err != nil {
		t.Fatalf("DeleteBlob missing = %v", err)
	}
	if _, err := store.BlobSize(ctx, "broken"); err == nil || errors.Is(err, errEvidenceBlobNotFound) {
		t.Fatalf("BlobSize broken = %v", err)
	}
	if _, err := store.ReadBlob(ctx, "broken", 3); err == nil {
		t.Fatal("ReadBlob broken must fail")
	}
	if err := store.DeleteBlob(ctx, "broken"); err == nil {
		t.Fatal("DeleteBlob broken must fail")
	}
}

func TestBlobEvidenceStore_UserDelegationSAS(t *testing.T) {
	rt := &routeTransport{routes: map[string]func(*http.Request) *http.Response{
		"POST ": func(r *http.Request) *http.Response {
			body := `<?xml version="1.0" encoding="utf-8"?><UserDelegationKey><SignedOid>o</SignedOid><SignedTid>t</SignedTid><SignedStart>2026-11-20T00:00:00Z</SignedStart><SignedExpiry>2026-11-20T01:00:00Z</SignedExpiry><SignedService>b</SignedService><SignedVersion>2023-11-03</SignedVersion><Value>` + base64.StdEncoding.EncodeToString([]byte("delegation-key-0123456789")) + `</Value></UserDelegationKey>`
			return response(r, http.StatusOK, body, map[string]string{"Content-Type": "application/xml"})
		},
	}}
	client, err := azblob.NewClient("https://acct.blob.core.windows.net", fakeTokenCredential{}, &azblob.ClientOptions{ClientOptions: noRetry(rt)})
	if err != nil {
		t.Fatal(err)
	}
	store := &blobEvidenceStore{blobs: client, storageURL: "https://acct.blob.core.windows.net", container: "stage"}
	raw, err := store.WriteSASURL(context.Background(), "evidence/b.tar.gz", evidenceSASLifetime)
	if err != nil {
		t.Fatalf("WriteSASURL = %v", err)
	}
	if !strings.Contains(raw, "skoid=o") || !strings.Contains(raw, "sp=cw") {
		t.Fatalf("user delegation SAS = %s", redactSAS(raw, ""))
	}

	failing := &routeTransport{routes: map[string]func(*http.Request) *http.Response{
		"POST ": func(r *http.Request) *http.Response { return response(r, http.StatusForbidden, "", nil) },
	}}
	client, _ = azblob.NewClient("https://acct.blob.core.windows.net", fakeTokenCredential{}, &azblob.ClientOptions{ClientOptions: noRetry(failing)})
	store.blobs = client
	if _, err := store.WriteSASURL(context.Background(), "evidence/b.tar.gz", evidenceSASLifetime); err == nil || !strings.Contains(err.Error(), "Storage Blob Delegator") {
		t.Fatalf("WriteSASURL error = %v", err)
	}
}

func testComputeClient(t *testing.T, rt *routeTransport) *sdkClient {
	t.Helper()
	opts := &arm.ClientOptions{ClientOptions: noRetry(rt)}
	vms, err := armcompute.NewVirtualMachinesClient("sub", fakeTokenCredential{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	disks, _ := armcompute.NewDisksClient("sub", fakeTokenCredential{}, opts)
	images, _ := armcompute.NewImagesClient("sub", fakeTokenCredential{}, opts)
	versions, _ := armcompute.NewGalleryImageVersionsClient("sub", fakeTokenCredential{}, opts)
	return &sdkClient{
		cfg:             config{resourceGroup: "rg", galleryName: "gal", galleryImageName: "ubuntu"},
		vms:             vms,
		disks:           disks,
		images:          images,
		galleryVersions: versions,
	}
}

func TestSDKVerifyOps(t *testing.T) {
	ok := func(r *http.Request) *http.Response { return response(r, http.StatusOK, `{"id":"x","name":"x"}`, nil) }
	gone := func(r *http.Request) *http.Response { return response(r, http.StatusNoContent, "", nil) }
	rt := &routeTransport{routes: map[string]func(*http.Request) *http.Response{
		"PUT /virtualMachines/ib-v":                      ok,
		"DELETE /virtualMachines/ib-v":                   gone,
		"DELETE /disks/ib-d":                             gone,
		"GET /images/present":                            ok,
		"DELETE /images/present":                         gone,
		"GET /galleries/gal/images/ubuntu/versions/1":    ok,
		"DELETE /galleries/gal/images/ubuntu/versions/1": gone,
		"POST /virtualMachines/ib-v/runCommand": func(r *http.Request) *http.Response {
			return response(r, http.StatusOK, `{"value":[{"code":"ProvisioningState/succeeded","message":"ok"}]}`, nil)
		},
		"GET /images/broken": func(r *http.Request) *http.Response {
			return response(r, http.StatusForbidden, `{"error":{"code":"AuthorizationFailed"}}`, nil)
		},
	}}
	c := testComputeClient(t, rt)
	ctx := context.Background()
	ops := c.verifyCompute()
	if err := ops.CreateVM(ctx, "ib-v", armcompute.VirtualMachine{}); err != nil {
		t.Fatalf("CreateVM = %v", err)
	}
	if err := ops.DeleteVM(ctx, "ib-v"); err != nil {
		t.Fatalf("DeleteVM = %v", err)
	}
	if err := ops.DeleteDisk(ctx, "ib-d"); err != nil {
		t.Fatalf("DeleteDisk = %v", err)
	}
	if err := ops.DeleteVM(ctx, "missing"); err != nil {
		t.Fatalf("DeleteVM missing = %v", err)
	}
	owner := c.imageOwner()
	if exists, err := owner.ManagedImageExists(ctx, "present"); err != nil || !exists {
		t.Fatalf("ManagedImageExists = %v, %v", exists, err)
	}
	if exists, err := owner.ManagedImageExists(ctx, "missing"); err != nil || exists {
		t.Fatalf("ManagedImageExists missing = %v, %v", exists, err)
	}
	if _, err := owner.ManagedImageExists(ctx, "broken"); err == nil {
		t.Fatal("ManagedImageExists broken must fail")
	}
	if exists, err := owner.GalleryVersionExists(ctx, "1"); err != nil || !exists {
		t.Fatalf("GalleryVersionExists = %v, %v", exists, err)
	}
	if err := owner.DeleteManagedImage(ctx, "present"); err != nil {
		t.Fatalf("DeleteManagedImage = %v", err)
	}
	if err := owner.DeleteGalleryVersion(ctx, "1"); err != nil {
		t.Fatalf("DeleteGalleryVersion = %v", err)
	}
	statuses, err := c.shellRunner().RunShellScript(ctx, "ib-v", "echo ok")
	if err != nil || len(statuses) != 1 || value(statuses[0].Message) != "ok" {
		t.Fatalf("RunShellScript = %v, %v", statuses, err)
	}
	if _, ok := c.evidenceStore().(*blobEvidenceStore); !ok {
		t.Fatal("default evidence store must be the blob store")
	}
	fake := &fakeStore{}
	c.evidence, c.runner, c.verify, c.owner = fake, &fakeRunner{}, &fakeCompute{}, &fakeOwner{}
	if c.evidenceStore() != fake {
		t.Fatal("evidence seam ignored")
	}
	if _, ok := c.shellRunner().(*fakeRunner); !ok {
		t.Fatal("runner seam ignored")
	}
	if _, ok := c.verifyCompute().(*fakeCompute); !ok {
		t.Fatal("verify seam ignored")
	}
	if _, ok := c.imageOwner().(*fakeOwner); !ok {
		t.Fatal("owner seam ignored")
	}
}
