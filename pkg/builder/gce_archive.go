package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const (
	gceArchiveDiskName = "disk.raw"
	gceDiskSizeUnit    = int64(1 << 30)
	gceMaximumDiskSize = int64(2 << 40)
)

// createGCEArchive packages a raw disk in the sparse old-GNU tar.gz shape
// required by Compute Engine's manual image import API. The archive contains
// exactly one regular file named disk.raw whose size is a whole GiB.
func createGCEArchive(ctx context.Context, runner CommandRunner, rawPath, archivePath string) (retErr error) {
	info, err := os.Stat(rawPath) // #nosec G304 -- Controller-owned workspace path.
	if err != nil {
		return fmt.Errorf("stat raw GCE disk: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("raw GCE disk must be a non-empty regular file")
	}
	paddedSize := roundUp(info.Size(), gceDiskSizeUnit)
	if paddedSize > gceMaximumDiskSize {
		return fmt.Errorf("raw GCE disk size %d exceeds the 2 TiB Compute Engine import limit", paddedSize)
	}
	if err := os.Truncate(rawPath, paddedSize); err != nil { // #nosec G304 -- Controller-owned workspace path.
		return fmt.Errorf("pad raw GCE disk to a whole GiB: %w", err)
	}

	archiveDir := filepath.Dir(rawPath)
	diskPath := filepath.Join(archiveDir, gceArchiveDiskName)
	removeLink := filepath.Clean(rawPath) != filepath.Clean(diskPath)
	if removeLink {
		if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale GCE archive link: %w", err)
		}
		if err := os.Link(rawPath, diskPath); err != nil {
			return fmt.Errorf("create GCE archive disk link: %w", err)
		}
		defer os.Remove(diskPath)
	}
	defer func() {
		if retErr != nil {
			_ = os.Remove(archivePath)
		}
	}()

	if err := runner.Run(ctx, Command{
		Name: "tar",
		Args: []string{"--format=oldgnu", "--sparse", "-C", archiveDir, "-czf", archivePath, gceArchiveDiskName},
		Dir:  archiveDir,
	}); err != nil {
		return fmt.Errorf("create sparse old-GNU GCE image archive: %w", err)
	}
	return nil
}

func roundUp(value, unit int64) int64 {
	return ((value + unit - 1) / unit) * unit
}
