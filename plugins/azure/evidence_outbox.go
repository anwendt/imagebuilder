package azure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"

	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ADR-011 evidence outbox retrieval for publisher=controller. The guest only
// collects its evidence into evidence.OutboxDir. The provider hands the guest
// a short-lived, write-only SAS for exactly one staging blob, the guest PUTs
// the packed outbox there and deletes the outbox, and the provider downloads
// and validates the blob. The blob stays as the provider-side persisted copy
// until the core acknowledged the bundle or the build is cleaned up.

const (
	evidenceSASLifetime      = 15 * time.Minute
	evidenceSASClockSkew     = 5 * time.Minute
	evidenceUploadMarker     = "IMAGEBUILDER_EVIDENCE_UPLOADED"
	evidenceBundleLostPrefix = "EvidenceBundleLost: "
	evidenceBundleMissing    = "EvidenceBundleMissing: "
)

var errEvidenceBlobNotFound = errors.New("evidence blob not found")

// azureShellRunner runs one Linux shell script through VM Run Command.
type azureShellRunner interface {
	RunShellScript(ctx context.Context, vmName, script string) ([]*armcompute.InstanceViewStatus, error)
}

// azureEvidenceStore is the staging storage used for the evidence bundle.
type azureEvidenceStore interface {
	// WriteSASURL returns a URL with a write-only SAS for exactly one blob.
	WriteSASURL(ctx context.Context, blobName string, lifetime time.Duration) (string, error)
	// BlobSize returns errEvidenceBlobNotFound when the blob does not exist.
	BlobSize(ctx context.Context, blobName string) (int64, error)
	// ReadBlob reads at most limit bytes; errEvidenceBlobNotFound when absent.
	ReadBlob(ctx context.Context, blobName string, limit int64) ([]byte, error)
	// DeleteBlob is idempotent.
	DeleteBlob(ctx context.Context, blobName string) error
}

func azureEvidenceBlobName(prefix, buildID string) string {
	return path.Join(strings.Trim(prefix, "/"), "evidence", sanitizeName(buildID)+".tar.gz")
}

var sasSignaturePattern = regexp.MustCompile(`(?i)(sig=)[^&\s'"]+`)

// redactSAS removes a SAS URL, its query and any signature from text that may
// reach errors, events or logs.
func redactSAS(text, sasURL string) string {
	if sasURL != "" {
		text = strings.ReplaceAll(text, sasURL, "<redacted-sas-url>")
		if i := strings.Index(sasURL, "?"); i >= 0 && i+1 < len(sasURL) {
			text = strings.ReplaceAll(text, sasURL[i+1:], "<redacted-sas>")
		}
	}
	return sasSignaturePattern.ReplaceAllString(text, "${1}<redacted>")
}

// azureEvidenceUploadScript packs the outbox, uploads it with the SAS URL
// (first without proxy, then through httpsProxy when configured) and removes
// the outbox. The SAS URL is only embedded in the script, never logged.
func azureEvidenceUploadScript(sasURL, httpsProxy string) string {
	lines := []string{
		"set -eu",
		"unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY no_proxy NO_PROXY",
		"outbox=" + evidence.OutboxDir,
		"test -s \"$outbox/" + evidence.SBOMFile + "\" || { echo 'evidence outbox: " + evidence.SBOMFile + " missing' >&2; exit 3; }",
		"test -s \"$outbox/" + evidence.TrivyFile + "\" || { echo 'evidence outbox: " + evidence.TrivyFile + " missing' >&2; exit 3; }",
		"ib_sas_url=" + evidence.ShellQuote(sasURL),
		"ib_put() {",
		"  tar czf - -C " + evidence.OutboxParent + " " + evidence.OutboxName + " | curl --fail --silent --show-error --max-time 300 -X PUT -H 'x-ms-blob-type: BlockBlob' --data-binary @- \"$@\" \"$ib_sas_url\"",
		"}",
		"if ! ib_put --noproxy '*'; then",
	}
	if strings.TrimSpace(httpsProxy) != "" {
		lines = append(lines,
			"  echo 'evidence upload without proxy failed; retrying through the configured proxy' >&2",
			"  ib_put --proxy "+evidence.ShellQuote(strings.TrimSpace(httpsProxy))+" || { echo 'evidence upload failed' >&2; exit 4; }",
		)
	} else {
		lines = append(lines, "  echo 'evidence upload failed' >&2", "  exit 4")
	}
	lines = append(lines,
		"fi",
		"rm -rf \"$outbox\"",
		"test ! -e \"$outbox\"",
		"echo "+evidenceUploadMarker,
	)
	return strings.Join(lines, "\n")
}

func runCommandOutput(statuses []*armcompute.InstanceViewStatus) string {
	var b strings.Builder
	for _, status := range statuses {
		b.WriteString(value(status.Message))
		b.WriteString("\n")
	}
	return b.String()
}

// collectAzureEvidence uploads the guest outbox to the staging blob, removes
// it in the guest and validates the uploaded bundle. It returns the blob name
// and the bundle sha256.
func collectAzureEvidence(ctx context.Context, runner azureShellRunner, store azureEvidenceStore, vmName, blobName, httpsProxy string) (string, error) {
	if err := store.DeleteBlob(ctx, blobName); err != nil {
		return "", fmt.Errorf("remove stale evidence blob: %w", err)
	}
	sasURL, err := store.WriteSASURL(ctx, blobName, evidenceSASLifetime)
	if err != nil {
		return "", fmt.Errorf("create evidence upload SAS: %w", err)
	}
	statuses, err := runner.RunShellScript(ctx, vmName, azureEvidenceUploadScript(sasURL, httpsProxy))
	if err != nil {
		return "", errors.New("upload evidence outbox through Azure Run Command: " + redactSAS(err.Error(), sasURL))
	}
	if err := azureRunCommandError(statuses); err != nil {
		return "", errors.New("evidence outbox upload failed: " + redactSAS(err.Error(), sasURL))
	}
	if !strings.Contains(runCommandOutput(statuses), evidenceUploadMarker) {
		return "", errors.New("evidence outbox upload did not confirm completion: " + truncate(redactSAS(runCommandOutput(statuses), sasURL), 512))
	}
	size, err := store.BlobSize(ctx, blobName)
	if err != nil {
		return "", fmt.Errorf("read uploaded evidence bundle size: %w", err)
	}
	if size > evidence.MaxBundleBytes {
		_ = store.DeleteBlob(ctx, blobName)
		return "", fmt.Errorf("evidence bundle is %d bytes and exceeds the limit of %d bytes (900 KiB compressed); reduce the outbox content", size, evidence.MaxBundleBytes)
	}
	data, err := store.ReadBlob(ctx, blobName, evidence.MaxBundleBytes+1)
	if err != nil {
		return "", fmt.Errorf("download evidence bundle: %w", err)
	}
	bundle, err := evidence.ValidateBundle(data)
	if err != nil {
		_ = store.DeleteBlob(ctx, blobName)
		return "", err
	}
	return bundle.SHA256, nil
}

// azureDoneEvidenceBundle returns the persisted bundle for a Done response in
// controller mode. After the core acknowledged it, the blob is deleted and no
// bundle is returned.
func azureDoneEvidenceBundle(ctx context.Context, store azureEvidenceStore, ref azureRemoteOperationRef, acknowledged bool) (*platform.RemoteEvidenceBundle, error) {
	if ref.EvidenceBlob == "" || ref.EvidenceSHA256 == "" {
		return nil, errors.New(evidenceBundleMissing + "the build finished without a collected evidence outbox; the evidence provisioner must run")
	}
	if acknowledged {
		if err := store.DeleteBlob(ctx, ref.EvidenceBlob); err != nil {
			return nil, fmt.Errorf("delete acknowledged evidence bundle: %w", err)
		}
		return nil, nil
	}
	data, err := store.ReadBlob(ctx, ref.EvidenceBlob, evidence.MaxBundleBytes+1)
	if errors.Is(err, errEvidenceBlobNotFound) {
		return nil, errors.New(evidenceBundleLostPrefix + "the persisted evidence bundle no longer exists in the staging container; rebuild the image")
	}
	if err != nil {
		return nil, fmt.Errorf("download persisted evidence bundle: %w", err)
	}
	bundle, err := evidence.VerifyBundle(data, ref.EvidenceSHA256)
	if err != nil {
		return nil, errors.New(evidenceBundleLostPrefix + err.Error())
	}
	return bundle.PlatformBundle(), nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// blobEvidenceStore implements azureEvidenceStore on the staging account.
type blobEvidenceStore struct {
	blobs      *azblob.Client
	sharedKey  *azblob.SharedKeyCredential
	storageURL string
	container  string
	now        func() time.Time
}

func (s *blobEvidenceStore) blobURL(blobName string) string {
	segments := strings.Split(blobName, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return s.storageURL + "/" + url.PathEscape(s.container) + "/" + strings.Join(segments, "/")
}

func (s *blobEvidenceStore) WriteSASURL(ctx context.Context, blobName string, lifetime time.Duration) (string, error) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	start := now().UTC().Add(-evidenceSASClockSkew)
	expiry := now().UTC().Add(lifetime)
	values := azureWriteSASValues(s.container, blobName, start, expiry)
	var (
		query sas.QueryParameters
		err   error
	)
	if s.sharedKey != nil {
		query, err = values.SignWithSharedKey(s.sharedKey)
	} else {
		var credential *service.UserDelegationCredential
		credential, err = s.blobs.ServiceClient().GetUserDelegationCredential(ctx, service.KeyInfo{
			Start:  to.Ptr(start.Format(sas.TimeFormat)),
			Expiry: to.Ptr(expiry.Format(sas.TimeFormat)),
		}, nil)
		if err != nil {
			return "", fmt.Errorf("get user delegation key (needs Storage Blob Delegator): %w", err)
		}
		query, err = values.SignWithUserDelegation(credential)
	}
	if err != nil {
		return "", fmt.Errorf("sign evidence SAS: %w", err)
	}
	return s.blobURL(blobName) + "?" + query.Encode(), nil
}

// azureWriteSASValues describes a create+write SAS for exactly one blob over HTTPS.
func azureWriteSASValues(container, blobName string, start, expiry time.Time) sas.BlobSignatureValues {
	return sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     start,
		ExpiryTime:    expiry,
		Permissions:   (&sas.BlobPermissions{Create: true, Write: true}).String(),
		ContainerName: container,
		BlobName:      blobName,
	}
}

func (s *blobEvidenceStore) BlobSize(ctx context.Context, blobName string) (int64, error) {
	props, err := s.blobs.ServiceClient().NewContainerClient(s.container).NewBlobClient(blobName).GetProperties(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) || isHTTPStatus(err, http.StatusNotFound) {
			return 0, errEvidenceBlobNotFound
		}
		return 0, err
	}
	if props.ContentLength == nil {
		return 0, fmt.Errorf("evidence blob has no content length")
	}
	return *props.ContentLength, nil
}

func (s *blobEvidenceStore) ReadBlob(ctx context.Context, blobName string, limit int64) ([]byte, error) {
	resp, err := s.blobs.DownloadStream(ctx, s.container, blobName, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) || isHTTPStatus(err, http.StatusNotFound) {
			return nil, errEvidenceBlobNotFound
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(resp.Body, limit)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *blobEvidenceStore) DeleteBlob(ctx context.Context, blobName string) error {
	if _, err := s.blobs.DeleteBlob(ctx, s.container, blobName, nil); err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) || isHTTPStatus(err, http.StatusNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (c *sdkClient) evidenceStore() azureEvidenceStore {
	if c.evidence != nil {
		return c.evidence
	}
	return &blobEvidenceStore{blobs: c.blobs, sharedKey: c.sharedKey, storageURL: c.storageURL, container: c.cfg.storageContainer}
}

func (c *sdkClient) shellRunner() azureShellRunner {
	if c.runner != nil {
		return c.runner
	}
	return vmRunCommand{c: c}
}

// vmRunCommand runs scripts through armcompute Run Command.
type vmRunCommand struct{ c *sdkClient }

func (r vmRunCommand) RunShellScript(ctx context.Context, vmName, script string) ([]*armcompute.InstanceViewStatus, error) {
	poller, err := r.c.vms.BeginRunCommand(ctx, r.c.cfg.resourceGroup, vmName, armcompute.RunCommandInput{
		CommandID: to.Ptr("RunShellScript"),
		Script:    stringPtrs([]string{script}),
	}, nil)
	if err != nil {
		return nil, err
	}
	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}
