package sdk

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"
)

// ValidatingReader enforces the declared stream size and SHA-256 checksum while
// data is consumed by the target platform. Call Verify only after the upload
// API has consumed the reader to EOF successfully.
type ValidatingReader struct {
	reader       io.Reader
	hash         hash.Hash
	expectedSize int64
	expectedHash []byte
	read         int64
	reachedEOF   bool
	onProgress   func(int64) error
	progressErr  error
}

func NewValidatingReader(reader io.Reader, totalSize int64, checksum string, onProgress func(int64) error) (*ValidatingReader, error) {
	if reader == nil {
		return nil, fmt.Errorf("artifact stream is required")
	}
	if totalSize < 0 {
		return nil, fmt.Errorf("artifact size must not be negative")
	}
	expected, err := parseSHA256(checksum)
	if err != nil {
		return nil, err
	}
	return &ValidatingReader{
		reader:       reader,
		hash:         sha256.New(),
		expectedSize: totalSize,
		expectedHash: expected,
		onProgress:   onProgress,
	}, nil
}

func (r *ValidatingReader) Read(p []byte) (int, error) {
	if r.progressErr != nil {
		return 0, r.progressErr
	}
	if r.reachedEOF {
		return 0, io.EOF
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.read += int64(n)
		if r.expectedSize > 0 && r.read > r.expectedSize {
			return n, fmt.Errorf("artifact stream exceeded declared size %d", r.expectedSize)
		}
		_, _ = r.hash.Write(p[:n])
		if r.onProgress != nil {
			if progressErr := r.onProgress(r.read); progressErr != nil {
				r.progressErr = progressErr
				return n, progressErr
			}
		}
	}
	if err == io.EOF {
		r.reachedEOF = true
	}
	return n, err
}

func (r *ValidatingReader) Verify() error {
	if r.progressErr != nil {
		return r.progressErr
	}
	if !r.reachedEOF && r.expectedSize > 0 && r.read == r.expectedSize {
		probe := make([]byte, 1)
		n, err := r.reader.Read(probe)
		if n > 0 {
			return fmt.Errorf("artifact stream exceeded declared size %d", r.expectedSize)
		}
		if err != io.EOF {
			return fmt.Errorf("verify artifact stream termination: %w", err)
		}
		r.reachedEOF = true
	}
	if !r.reachedEOF {
		return fmt.Errorf("target upload returned before consuming the complete artifact stream")
	}
	if r.expectedSize > 0 && r.read != r.expectedSize {
		return fmt.Errorf("artifact stream size %d does not match declared size %d", r.read, r.expectedSize)
	}
	if len(r.expectedHash) > 0 && subtle.ConstantTimeCompare(r.hash.Sum(nil), r.expectedHash) != 1 {
		return fmt.Errorf("artifact stream SHA-256 does not match declared checksum")
	}
	return nil
}

func parseSHA256(checksum string) ([]byte, error) {
	checksum = strings.TrimSpace(checksum)
	if checksum == "" {
		return nil, nil
	}
	algorithm, value, found := strings.Cut(checksum, ":")
	if !found || !strings.EqualFold(algorithm, "sha256") {
		return nil, fmt.Errorf("artifact checksum must use sha256:<hex>")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("artifact SHA-256 checksum must contain %d hexadecimal characters", sha256.Size*2)
	}
	return decoded, nil
}
