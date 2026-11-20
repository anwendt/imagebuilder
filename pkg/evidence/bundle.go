// Package evidence implements the ADR-011 guest evidence outbox contract that
// is shared by providers (retrieval) and the core controller (publication).
//
// The guest-side evidence provisioner writes its results into OutboxDir. The
// provider packs the directory as a gzip-compressed tar ("evidence/..."),
// validates it with ValidateBundle, deletes the outbox in the guest and returns
// the bundle to the core, which validates it again before publishing.
package evidence

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const (
	// OutboxParent is the guest directory that contains the outbox.
	OutboxParent = "/var/lib/imagebuilder"
	// OutboxName is the outbox directory name and the tar path prefix.
	OutboxName = "evidence"
	// OutboxDir is the guest outbox written by the evidence provisioner.
	OutboxDir = OutboxParent + "/" + OutboxName

	// SBOMFile is the required CycloneDX JSON SBOM.
	SBOMFile = "sbom.cdx.json"
	// TrivyFile is the required Trivy JSON vulnerability report.
	TrivyFile = "trivy-report.json"
	// CollectorFile optionally records the collector tool versions.
	CollectorFile = "collector.json"

	// MediaType is the media type of the compressed outbox bundle.
	MediaType = "application/vnd.imagebuilder.evidence.bundle.v1.tar+gzip"

	// MaxBundleBytes is the maximum size of the compressed bundle. It keeps
	// the bundle, the rendered descriptor and the provenance inside one
	// Kubernetes Secret.
	MaxBundleBytes = 900 * 1024
	// MaxUncompressedFileBytes bounds every extracted file.
	MaxUncompressedFileBytes int64 = 48 << 20
	// MaxUncompressedBytes bounds the extracted bundle.
	MaxUncompressedBytes int64 = 96 << 20
)

var allowedFiles = map[string]bool{SBOMFile: true, TrivyFile: true, CollectorFile: true}

// Bundle is a validated evidence bundle.
type Bundle struct {
	// Content is the compressed bundle as retrieved from the guest.
	Content []byte
	// SHA256 is the lowercase hex SHA-256 of Content.
	SHA256      string
	SBOM        []byte
	TrivyReport []byte
	// Collector is nil when the guest did not write collector.json.
	Collector []byte
}

// Digest returns the bundle digest in "sha256:<hex>" form.
func (b *Bundle) Digest() string {
	return "sha256:" + b.SHA256
}

// SHA256Hex returns the lowercase hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyBundle validates data and checks it against the recorded digest
// (hex, optionally prefixed with "sha256:").
func VerifyBundle(data []byte, digest string) (*Bundle, error) {
	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if want == "" {
		return nil, fmt.Errorf("evidence bundle digest is missing")
	}
	bundle, err := ValidateBundle(data)
	if err != nil {
		return nil, err
	}
	if bundle.SHA256 != want {
		return nil, fmt.Errorf("evidence bundle digest mismatch: got sha256:%s, want sha256:%s", bundle.SHA256, want)
	}
	return bundle, nil
}

// ValidateBundle checks size, structure and content of a compressed outbox
// bundle and returns the extracted files. It fails closed on anything that is
// not exactly the ADR-011 outbox.
func ValidateBundle(data []byte) (*Bundle, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("evidence bundle is empty")
	}
	if len(data) > MaxBundleBytes {
		return nil, fmt.Errorf("evidence bundle is %d bytes and exceeds the limit of %d bytes (900 KiB compressed); reduce the outbox content", len(data), MaxBundleBytes)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("evidence bundle is not a gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read evidence bundle tar: %w", err)
		}
		name, err := outboxEntryName(hdr.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			if hdr.Typeflag != tar.TypeDir {
				return nil, fmt.Errorf("evidence bundle entry %q is not a directory", hdr.Name)
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("evidence bundle entry %q is not a regular file", hdr.Name)
		}
		if !allowedFiles[name] {
			return nil, fmt.Errorf("evidence bundle contains unexpected file %q; the outbox may only hold %s, %s and %s", hdr.Name, SBOMFile, TrivyFile, CollectorFile)
		}
		if _, seen := files[name]; seen {
			return nil, fmt.Errorf("evidence bundle contains %q more than once", name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxUncompressedFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read evidence bundle entry %q: %w", name, err)
		}
		if int64(len(body)) > MaxUncompressedFileBytes {
			return nil, fmt.Errorf("evidence bundle entry %q is larger than %d bytes", name, MaxUncompressedFileBytes)
		}
		total += int64(len(body))
		if total > MaxUncompressedBytes {
			return nil, fmt.Errorf("evidence bundle is larger than %d bytes uncompressed", MaxUncompressedBytes)
		}
		files[name] = body
	}
	bundle := &Bundle{
		Content:     data,
		SHA256:      SHA256Hex(data),
		SBOM:        files[SBOMFile],
		TrivyReport: files[TrivyFile],
		Collector:   files[CollectorFile],
	}
	if bundle.SBOM == nil {
		return nil, fmt.Errorf("evidence bundle: required %s is missing", SBOMFile)
	}
	if bundle.TrivyReport == nil {
		return nil, fmt.Errorf("evidence bundle: required %s is missing", TrivyFile)
	}
	var sbom struct {
		BOMFormat string `json:"bomFormat"`
	}
	if err := json.Unmarshal(bundle.SBOM, &sbom); err != nil {
		return nil, fmt.Errorf("evidence bundle: %s is not valid JSON: %w", SBOMFile, err)
	}
	if sbom.BOMFormat != "CycloneDX" {
		return nil, fmt.Errorf("evidence bundle: %s is not a CycloneDX document (bomFormat %q)", SBOMFile, sbom.BOMFormat)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(bundle.TrivyReport, &report); err != nil {
		return nil, fmt.Errorf("evidence bundle: %s is not valid JSON: %w", TrivyFile, err)
	}
	if _, ok := report["SchemaVersion"]; !ok {
		return nil, fmt.Errorf("evidence bundle: %s is not a Trivy JSON report (SchemaVersion missing)", TrivyFile)
	}
	if bundle.Collector != nil && !json.Valid(bundle.Collector) {
		return nil, fmt.Errorf("evidence bundle: %s is not valid JSON", CollectorFile)
	}
	return bundle, nil
}

// outboxEntryName maps a tar entry name to the file name inside the outbox.
// It returns "" for the outbox directory itself.
func outboxEntryName(raw string) (string, error) {
	name := strings.TrimPrefix(raw, "./")
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("evidence bundle entry %q is outside the evidence outbox", raw)
	}
	cleaned := path.Clean(name)
	if cleaned != strings.TrimSuffix(name, "/") {
		return "", fmt.Errorf("evidence bundle entry %q is outside the evidence outbox", raw)
	}
	if cleaned == OutboxName {
		return "", nil
	}
	if !strings.HasPrefix(cleaned, OutboxName+"/") {
		return "", fmt.Errorf("evidence bundle entry %q is outside the evidence outbox", raw)
	}
	rest := strings.TrimPrefix(cleaned, OutboxName+"/")
	if rest == "" || strings.Contains(rest, "/") {
		return "", fmt.Errorf("evidence bundle entry %q is outside the evidence outbox", raw)
	}
	return rest, nil
}

// Pack builds a bundle from outbox files, mainly for tests and fakes.
func Pack(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		if !allowedFiles[name] {
			return nil, fmt.Errorf("evidence: %q is not an outbox file", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: OutboxName + "/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		return nil, err
	}
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: OutboxName + "/" + name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckSeverities fails when the Trivy report lists a fixable vulnerability
// (FixedVersion set) with one of the failOn severities
// (spec.evidence.failOnSeverity). Unfixed findings do not block, matching
// `trivy --ignore-unfixed`; findings suppressed by a .trivyignore in the guest
// are not in the report at all.
func CheckSeverities(report []byte, failOn []string) error {
	if len(failOn) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, severity := range failOn {
		wanted[strings.ToUpper(strings.TrimSpace(severity))] = true
	}
	var parsed struct {
		Results []struct {
			Vulnerabilities []struct {
				Severity     string `json:"Severity"`
				FixedVersion string `json:"FixedVersion"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(report, &parsed); err != nil {
		return fmt.Errorf("parse Trivy report: %w", err)
	}
	count := 0
	for _, result := range parsed.Results {
		for _, vuln := range result.Vulnerabilities {
			if wanted[strings.ToUpper(vuln.Severity)] && strings.TrimSpace(vuln.FixedVersion) != "" {
				count++
			}
		}
	}
	if count > 0 {
		return fmt.Errorf("trivy report contains %d fixable vulnerabilities with severity %s", count, strings.Join(failOn, "/"))
	}
	return nil
}
