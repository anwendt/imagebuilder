package builder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateGCEArchiveContainsDiskRaw(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "disk.raw")
	archive := filepath.Join(dir, "image.tar.gz")
	if err := os.WriteFile(raw, []byte("raw-disk-data"), 0o600); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if err := createGCEArchive(context.Background(), ExecRunner{}, raw, archive); err != nil {
		t.Fatalf("createGCEArchive: %v", err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("tar entry: %v", err)
	}
	if header.Name != "disk.raw" || header.Size != gceDiskSizeUnit || header.Format != tar.FormatGNU {
		t.Fatalf("header=%#v", header)
	}
	content := make([]byte, len("raw-disk-data"))
	_, err = io.ReadFull(tr, content)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if string(content) != "raw-disk-data" {
		t.Fatalf("content=%q", content)
	}
	if info, err := os.Stat(raw); err != nil || info.Size() != gceDiskSizeUnit {
		t.Fatalf("padded raw info=%#v error=%v", info, err)
	}
}

func TestRoundUpGCEImageSize(t *testing.T) {
	if got := roundUp(gceDiskSizeUnit+1, gceDiskSizeUnit); got != 2*gceDiskSizeUnit {
		t.Fatalf("rounded size=%d", got)
	}
}

func TestQEMUOutputFormatSupportsGCEArchive(t *testing.T) {
	format, ok := qemuOutputFormat("gcetarball")
	if !ok || format != "raw" {
		t.Fatalf("format=%q ok=%t", format, ok)
	}
}
