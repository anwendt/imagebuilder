package sdk

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"
)

func TestValidatingReaderAcceptsMatchingStream(t *testing.T) {
	payload := []byte("direct-stream")
	checksum := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	var progress int64
	reader, err := NewValidatingReader(bytes.NewReader(payload), int64(len(payload)), checksum, func(read int64) error {
		progress = read
		return nil
	})
	if err != nil {
		t.Fatalf("NewValidatingReader: %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) || progress != int64(len(payload)) {
		t.Fatalf("payload=%q progress=%d", got, progress)
	}
	if err := reader.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestValidatingReaderRejectsChecksumSizeAndPartialConsumption(t *testing.T) {
	payload := []byte("artifact")
	t.Run("checksum", func(t *testing.T) {
		reader, err := NewValidatingReader(bytes.NewReader(payload), int64(len(payload)), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, reader)
		if err := reader.Verify(); err == nil {
			t.Fatal("checksum mismatch accepted")
		}
	})
	t.Run("size", func(t *testing.T) {
		reader, err := NewValidatingReader(bytes.NewReader(payload), int64(len(payload)+1), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, reader)
		if err := reader.Verify(); err == nil {
			t.Fatal("size mismatch accepted")
		}
	})
	t.Run("partial", func(t *testing.T) {
		reader, err := NewValidatingReader(bytes.NewReader(payload), int64(len(payload)), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1)
		_, _ = reader.Read(buffer)
		if err := reader.Verify(); err == nil {
			t.Fatal("partial stream accepted")
		}
	})
}

func TestValidatingReaderRejectsInvalidChecksumSyntaxAndProgressError(t *testing.T) {
	if _, err := NewValidatingReader(bytes.NewReader(nil), 0, "md5:abc", nil); err == nil {
		t.Fatal("non-SHA256 checksum accepted")
	}
	expected := fmt.Errorf("progress failed")
	reader, err := NewValidatingReader(bytes.NewReader([]byte("x")), 1, "", func(int64) error { return expected })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, reader); err == nil {
		t.Fatal("progress reporter error ignored")
	}
	if err := reader.Verify(); err == nil {
		t.Fatal("progress reporter error not preserved")
	}
}
