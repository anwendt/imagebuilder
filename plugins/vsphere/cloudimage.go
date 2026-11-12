package vsphere

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// deriveGuestPassword returns a per-build password for the temporary root
// login of a cloud-image build VM. It is derived from the provider secret and
// the build ID, so it needs no storage, survives provider restarts and is
// worthless once the build VM is gone.
func deriveGuestPassword(providerSecret, buildID string) string {
	mac := hmac.New(sha256.New, []byte(providerSecret))
	mac.Write([]byte("imagebuilder-vsphere-guest:" + buildID))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))[:26])
}

// cloudInitUserData is the minimal cloud-init payload for a cloud-image build
// VM: it only makes the guest reachable for VMware Guest Operations through a
// temporary root password. Everything else is done by the VMImage provisioners.
func cloudInitUserData(rootPassword string) string {
	return strings.Join([]string{
		"#cloud-config",
		"disable_root: false",
		"ssh_pwauth: false",
		"chpasswd:",
		"  expire: false",
		"  users:",
		"    - name: root",
		"      password: " + rootPassword,
		"      type: text",
		"",
	}, "\n")
}

func ovfUserDataProperty(userData string) string {
	return base64.StdEncoding.EncodeToString([]byte(userData))
}

func parseSHA256(checksum string) (string, error) {
	value := strings.TrimSpace(checksum)
	if value == "" {
		return "", fmt.Errorf("source checksum is required")
	}
	lower := strings.ToLower(value)
	if i := strings.Index(lower, ":"); i >= 0 {
		if lower[:i] != "sha256" {
			return "", fmt.Errorf("unsupported source checksum algorithm %q; use sha256", lower[:i])
		}
		lower = lower[i+1:]
	}
	raw, err := hex.DecodeString(lower)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("source checksum must be a 64 character sha256 hex digest")
	}
	return lower, nil
}

func sourceFileName(rawURL string) string {
	name := "source.ova"
	if u, err := url.Parse(rawURL); err == nil {
		if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
			name = base
		}
	}
	name = strings.ReplaceAll(name, "..", "")
	name = strings.NewReplacer("/", "", "\\", "", "%", "").Replace(name)
	if name == "" {
		return "source.ova"
	}
	return name
}

// downloadVerifiedSource downloads rawURL into dir and verifies its sha256.
// A previously downloaded file that still matches is reused. A mismatching
// download never stays on disk.
func downloadVerifiedSource(ctx context.Context, client *http.Client, rawURL, checksum, dir string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") && !isLoopbackHTTP(rawURL) {
		return "", fmt.Errorf("source URL must use https")
	}
	want, err := parseSHA256(checksum)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create source download directory: %w", err)
	}
	dest := filepath.Join(dir, sourceFileName(rawURL))
	if got, err := fileSHA256(dest); err == nil && got == want {
		return dest, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build source download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download source image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download source image: unexpected HTTP status %s", resp.Status)
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", fmt.Errorf("create source download file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body); err != nil {
		cleanup()
		return "", fmt.Errorf("download source image: %w", err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		cleanup()
		return "", fmt.Errorf("source image checksum mismatch: got sha256:%s, want sha256:%s", got, want)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("close source download file: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("store source image: %w", err)
	}
	return dest, nil
}

// isLoopbackHTTP allows plain http only for local test servers.
func isLoopbackHTTP(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func fileSHA256(name string) (string, error) {
	f, err := os.Open(filepath.Clean(name)) // #nosec G304 -- name is built from the provider-owned download directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
