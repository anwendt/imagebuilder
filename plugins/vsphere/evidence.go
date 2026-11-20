package vsphere

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/vmware/govmomi/guest"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/anwendt/imagebuilder/pkg/evidence"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
)

// ADR-011: with evidence publisher=controller the guest only collects its
// evidence into the outbox. The provider packs the outbox in the guest,
// downloads it over Guest Operations, validates it, deletes it in the guest,
// keeps a copy on the provider volume and returns it with every Done response
// until the core acknowledged it or the build is cleaned up.

// guestSession is the part of VMware Guest Operations the evidence retrieval
// and the verify boot need. Run returns a *guestExitError for a script that
// ran and failed.
type guestSession interface {
	Run(ctx context.Context, script string) error
	Download(ctx context.Context, guestPath string, limit int64) ([]byte, error)
}

// vmGuestSession implements guestSession for one VM and one guest login.
type vmGuestSession struct {
	c    *govmomiClient
	vm   *object.VirtualMachine
	auth *types.NamePasswordAuthentication
}

func (s *vmGuestSession) Run(ctx context.Context, script string) error {
	pm, err := guest.NewOperationsManager(s.c.vc.Client, s.vm.Reference()).ProcessManager(ctx)
	if err != nil {
		return fmt.Errorf("create vSphere guest process manager: %w", err)
	}
	return s.c.runGuestScript(ctx, s.vm, pm, s.auth, script, false)
}

// Download fetches a guest file through the URL vCenter hands out. The
// download uses the vCenter SOAP transport, so ESXi host names in the URL go
// through the inventory dialer and the provider proxy (hostresolve.go).
func (s *vmGuestSession) Download(ctx context.Context, guestPath string, limit int64) ([]byte, error) {
	fm, err := guest.NewOperationsManager(s.c.vc.Client, s.vm.Reference()).FileManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("create vSphere guest file manager: %w", err)
	}
	info, err := fm.InitiateFileTransferFromGuest(ctx, s.auth, guestPath)
	if err != nil {
		return nil, fmt.Errorf("start guest file transfer: %w", err)
	}
	if info.Size > limit {
		return nil, fmt.Errorf("guest file is %d bytes, more than the limit of %d bytes", info.Size, limit)
	}
	u, err := s.c.vc.Client.ParseURL(info.Url)
	if err != nil {
		return nil, fmt.Errorf("parse guest file transfer URL: %w", err)
	}
	body, _, err := s.c.vc.Client.Download(ctx, u, &soap.DefaultDownload)
	if err != nil {
		return nil, fmt.Errorf("download guest file: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, limit))
	if err != nil {
		return nil, fmt.Errorf("read guest file: %w", err)
	}
	return data, nil
}

func evidenceBundleID(buildID string) string {
	return sanitizeName(buildID)
}

// evidenceGuestBundlePath is the temporary guest file the outbox is packed into.
func evidenceGuestBundlePath(buildID string) string {
	return "/var/tmp/imagebuilder-evidence-" + evidenceBundleID(buildID) + ".tar.gz"
}

// collectGuestEvidence packs, downloads, validates and removes the guest
// evidence outbox. It fails closed on every error.
func collectGuestEvidence(ctx context.Context, g guestSession, buildID string) (*evidence.Bundle, error) {
	guestPath := evidenceGuestBundlePath(buildID)
	pack, err := evidence.PackScript(guestPath)
	if err != nil {
		return nil, err
	}
	if err := g.Run(ctx, pack); err != nil {
		return nil, fmt.Errorf("EvidenceCollectionFailed: pack the guest evidence outbox %s: %w", evidence.OutboxDir, err)
	}
	data, err := g.Download(ctx, guestPath, evidence.MaxBundleBytes+1)
	if err != nil {
		return nil, fmt.Errorf("EvidenceCollectionFailed: download the guest evidence bundle: %w", err)
	}
	if len(data) > evidence.MaxBundleBytes {
		return nil, fmt.Errorf("EvidenceBundleTooLarge: the compressed guest evidence outbox exceeds %d bytes (900 KiB)", evidence.MaxBundleBytes)
	}
	bundle, err := evidence.ValidateBundle(data)
	if err != nil {
		return nil, fmt.Errorf("EvidenceBundleInvalid: %w", err)
	}
	remove, err := evidence.RemoveScript(guestPath)
	if err != nil {
		return nil, err
	}
	if err := g.Run(ctx, remove); err != nil {
		return nil, fmt.Errorf("EvidenceCollectionFailed: remove the guest evidence outbox: %w", err)
	}
	return bundle, nil
}

func evidenceStorePath(dir, buildID string) string {
	return filepath.Join(dir, "evidence", evidenceBundleID(buildID)+".tar.gz")
}

// persistEvidenceBundle stores the bundle on the provider volume (0600).
func persistEvidenceBundle(dir, buildID string, data []byte) error {
	target := evidenceStorePath(dir, buildID)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create evidence bundle directory: %w", err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write evidence bundle: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store evidence bundle: %w", err)
	}
	return nil
}

func loadEvidenceBundle(dir, buildID, sha string) (*evidence.Bundle, error) {
	data, err := os.ReadFile(evidenceStorePath(dir, buildID)) // #nosec G304 -- path built from the sanitized build ID below the provider volume
	if err != nil {
		return nil, err
	}
	return evidence.VerifyBundle(data, sha)
}

func removeEvidenceBundle(dir, buildID string) error {
	if err := os.Remove(evidenceStorePath(dir, buildID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove persisted evidence bundle: %w", err)
	}
	return nil
}

// doneEvidenceBundle returns the evidence bundle for a Done response.
func doneEvidenceBundle(dir string, input vsphereRemoteBuildInput, ref vsphereRemoteOperationRef) (*platform.RemoteEvidenceBundle, error) {
	if !evidence.IsController(input.Evidence) {
		return nil, nil
	}
	if input.EvidenceBundleAcknowledged {
		return nil, removeEvidenceBundle(dir, input.BuildID)
	}
	if ref.EvidenceSHA256 == "" {
		return nil, providererrors.Terminal(fmt.Errorf("EvidenceBundleMissing: build %s finished without retrieving the guest evidence outbox; spec.evidence.publisher controller needs an evidence provisioner", input.BuildID))
	}
	bundle, err := loadEvidenceBundle(dir, input.BuildID, ref.EvidenceSHA256)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, providererrors.Terminal(fmt.Errorf("EvidenceBundleLost: the evidence bundle of build %s is no longer on the provider volume; rebuild the image", input.BuildID))
	}
	if err != nil {
		return nil, providererrors.Terminal(fmt.Errorf("EvidenceBundleInvalid: persisted evidence bundle: %w", err))
	}
	return bundle.PlatformBundle(), nil
}

// collectAndPersistEvidence runs right after the evidence provisioner.
func (c *govmomiClient) collectAndPersistEvidence(ctx context.Context, vm *object.VirtualMachine, input vsphereRemoteBuildInput) (string, error) {
	if err := c.requireOwnedBuildVM(ctx, vm); err != nil {
		return "", err
	}
	auth, err := c.remoteGuestAuthFor(input)
	if err != nil {
		return "", err
	}
	bundle, err := collectGuestEvidence(ctx, &vmGuestSession{c: c, vm: vm, auth: auth}, input.BuildID)
	if err != nil {
		return "", err
	}
	if err := persistEvidenceBundle(c.cfg.downloadDir, input.BuildID, bundle.Content); err != nil {
		return "", err
	}
	return bundle.SHA256, nil
}
