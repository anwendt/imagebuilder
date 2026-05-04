// Package builder contains the build-engine core that runs inside the build
// container. It prepares source images, selects a build backend, and returns a
// platform-ready artifact description for upload.
package builder

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/security/netguard"
)

const (
	defaultSourceFileName   = "source.img"
	defaultArtifactFileName = "artifact"
)

// SourceArtifact describes a verified source image inside the workspace.
type SourceArtifact struct {
	Path      string
	Checksum  string
	SizeBytes int64
	Type      string
	SourceURL string
	CacheHit  bool
}

// SourceFetcher fetches and verifies a VM image source.
type SourceFetcher interface {
	Fetch(ctx context.Context, source v1alpha1.SourceSpec, workspaceDir string) (*SourceArtifact, error)
}

// CacheAwareSourceFetcher can reuse a verified source cache when the build pod
// has an explicit cache volume mounted.
type CacheAwareSourceFetcher interface {
	SourceFetcher
	FetchWithCache(ctx context.Context, source v1alpha1.SourceSpec, workspaceDir, cacheDir string, opts SourceCacheOptions) (*SourceArtifact, error)
}

// Backend turns a verified source image into a build artifact.
type Backend interface {
	Name() string
	Supports(req BuildRequest) bool
	Build(ctx context.Context, req BackendRequest) (*platform.BuildArtifact, error)
}

// BuildRequest carries all user-requested build inputs.
type BuildRequest struct {
	Image         *v1alpha1.VMImage
	WorkspaceDir  string
	CacheDir      string
	CacheTTL      time.Duration
	CacheRetain   string
	CredentialDir string
}

// Source cache retain policies. They mirror api/v1alpha1.SourceCacheSpec values.
const (
	CacheRetainAlways = "Always"
	CacheRetainNever  = "Never"
)

// SourceCacheOptions controls read-through source cache behavior.
type SourceCacheOptions struct {
	TTL          time.Duration
	RetainPolicy string
}

// BackendRequest is passed to a concrete backend after source verification.
type BackendRequest struct {
	BuildRequest
	Source *SourceArtifact
	Format platform.ImageFormat
}

// EngineOptions configures an Engine.
type EngineOptions struct {
	Fetcher  SourceFetcher
	Backends []Backend
}

// Engine orchestrates source fetching and backend selection.
type Engine struct {
	fetcher  SourceFetcher
	backends []Backend
}

// NewEngine creates a build engine.
func NewEngine(opts EngineOptions) *Engine {
	fetcher := opts.Fetcher
	if fetcher == nil {
		fetcher = NewHTTPFetcher(http.DefaultClient)
	}
	return &Engine{
		fetcher:  fetcher,
		backends: opts.Backends,
	}
}

// Build fetches the source, selects a backend, and returns the build artifact.
func (e *Engine) Build(ctx context.Context, req BuildRequest) (*platform.BuildArtifact, error) {
	if req.Image == nil {
		return nil, fmt.Errorf("build image is required")
	}
	workspaceDir, err := cleanWorkspace(req.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	req.WorkspaceDir = workspaceDir

	if req.CacheDir != "" {
		cacheDir, err := cleanDirectory(req.CacheDir, "cache")
		if err != nil {
			return nil, err
		}
		req.CacheDir = cacheDir
	}
	if req.CredentialDir != "" {
		credentialDir, err := cleanDirectory(req.CredentialDir, "credentials")
		if err != nil {
			return nil, err
		}
		req.CredentialDir = credentialDir
	}

	source, err := e.fetchSource(ctx, req, workspaceDir)
	if err != nil {
		return nil, Classify(ReasonSourceFetchFailed, fmt.Errorf("fetch source: %w", err))
	}

	format, err := requestedFormat(req.Image)
	if err != nil {
		return nil, err
	}

	for _, backend := range e.backends {
		if backend.Supports(req) {
			artifact, err := backend.Build(ctx, BackendRequest{
				BuildRequest: req,
				Source:       source,
				Format:       format,
			})
			if err != nil {
				return nil, fmt.Errorf("backend %q build: %w", backend.Name(), err)
			}
			return artifact, nil
		}
	}

	return nil, fmt.Errorf("no build backend supports source type %q", req.Image.Spec.Source.Type)
}

func (e *Engine) fetchSource(ctx context.Context, req BuildRequest, workspaceDir string) (*SourceArtifact, error) {
	if req.CacheDir != "" {
		if fetcher, ok := e.fetcher.(CacheAwareSourceFetcher); ok {
			return fetcher.FetchWithCache(ctx, req.Image.Spec.Source, workspaceDir, req.CacheDir, SourceCacheOptions{
				TTL:          req.CacheTTL,
				RetainPolicy: req.CacheRetain,
			})
		}
	}
	return e.fetcher.Fetch(ctx, req.Image.Spec.Source, workspaceDir)
}

func requestedFormat(img *v1alpha1.VMImage) (platform.ImageFormat, error) {
	if len(img.Spec.Targets) == 0 {
		return "", fmt.Errorf("at least one target is required")
	}
	format := img.Spec.Targets[0].Format
	for _, target := range img.Spec.Targets[1:] {
		if target.Format != format {
			return "", fmt.Errorf("all targets in one build must use the same artifact format, got %q and %q", format, target.Format)
		}
	}
	return platform.ImageFormat(format), nil
}

func cleanWorkspace(workspaceDir string) (string, error) {
	if workspaceDir == "" {
		return "", fmt.Errorf("workspace directory is required")
	}
	return cleanDirectory(workspaceDir, "workspace")
}

func cleanDirectory(path, label string) (string, error) {
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("clean %s path: %w", label, err)
	}
	if err := os.MkdirAll(cleaned, 0o700); err != nil {
		return "", fmt.Errorf("create %s %s: %w", label, cleaned, err)
	}
	return cleaned, nil
}

// HTTPFetcher downloads HTTPS sources and verifies their checksum before use.
type HTTPFetcher struct {
	client   *http.Client
	resolver netguard.Resolver
}

// HTTPFetcherOption configures HTTPFetcher.
type HTTPFetcherOption func(*HTTPFetcher)

// WithResolver overrides DNS resolution used by SSRF checks.
func WithResolver(resolver netguard.Resolver) HTTPFetcherOption {
	return func(fetcher *HTTPFetcher) {
		fetcher.resolver = resolver
	}
}

// NewHTTPFetcher creates an HTTP-backed source fetcher.
func NewHTTPFetcher(client *http.Client, opts ...HTTPFetcherOption) *HTTPFetcher {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	fetcher := &HTTPFetcher{client: &clone}
	for _, opt := range opts {
		opt(fetcher)
	}
	if clone.CheckRedirect == nil {
		clone.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
			if strings.ToLower(req.URL.Scheme) != "https" {
				return fmt.Errorf("redirect to non-HTTPS URL is not allowed")
			}
			return fetcher.validateSafeSourceURL(req.Context(), req.URL)
		}
	}
	fetcher.client = &clone
	return fetcher
}

// Fetch downloads source.url into workspaceDir/source.img with mode 0600.
func (f *HTTPFetcher) Fetch(ctx context.Context, source v1alpha1.SourceSpec, workspaceDir string) (*SourceArtifact, error) {
	return f.fetch(ctx, source, workspaceDir, "", SourceCacheOptions{})
}

// FetchWithCache reuses a checksum-addressed source file from cacheDir when it
// verifies cleanly, otherwise it downloads and stores a verified copy there.
func (f *HTTPFetcher) FetchWithCache(ctx context.Context, source v1alpha1.SourceSpec, workspaceDir, cacheDir string, opts SourceCacheOptions) (*SourceArtifact, error) {
	return f.fetch(ctx, source, workspaceDir, cacheDir, opts)
}

func (f *HTTPFetcher) fetch(ctx context.Context, source v1alpha1.SourceSpec, workspaceDir, cacheDir string, opts SourceCacheOptions) (*SourceArtifact, error) {
	if strings.ToLower(source.Type) != "cloud-image" && strings.ToLower(source.Type) != "iso" {
		return nil, fmt.Errorf("source type %q is not fetchable via URL", source.Type)
	}
	parsedURL, err := url.Parse(source.URL)
	if err != nil {
		return nil, fmt.Errorf("parse source URL: %w", err)
	}
	if err := f.validateSafeSourceURL(ctx, parsedURL); err != nil {
		return nil, err
	}

	algo, expected, err := parseChecksum(source.Checksum)
	if err != nil {
		return nil, err
	}

	workspaceDir, err = cleanWorkspace(workspaceDir)
	if err != nil {
		return nil, err
	}
	dstPath := filepath.Join(workspaceDir, defaultSourceFileName)

	if cacheDir != "" {
		cacheDir, err = cleanDirectory(cacheDir, "cache")
		if err != nil {
			return nil, err
		}
		cachePath := filepath.Join(cacheDir, cacheFileName(algo, expected))
		if artifact, ok, err := f.copyValidCache(ctx, cachePath, dstPath, source, algo, expected, opts); err != nil {
			return nil, err
		} else if ok {
			return artifact, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create source request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download source: unexpected HTTP status %d", resp.StatusCode)
	}

	artifact, err := writeVerifiedSource(dstPath, resp.Body, source, algo, expected)
	if err != nil {
		return nil, err
	}

	if cacheDir != "" && normalizeCacheRetainPolicy(opts.RetainPolicy) != CacheRetainNever {
		cachePath := filepath.Join(cacheDir, cacheFileName(algo, expected))
		if err := copyFile(ctx, artifact.Path, cachePath); err != nil {
			return nil, fmt.Errorf("write source cache: %w", err)
		}
	}

	return artifact, nil
}

func (f *HTTPFetcher) copyValidCache(ctx context.Context, cachePath, dstPath string, source v1alpha1.SourceSpec, algo, expected string, opts SourceCacheOptions) (*SourceArtifact, bool, error) {
	expired, err := cacheEntryExpired(cachePath, opts.TTL)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		_ = os.Remove(cachePath)
		return nil, false, nil
	}
	if expired {
		_ = os.Remove(cachePath)
		return nil, false, nil
	}
	size, ok, err := verifyFileChecksum(cachePath, algo, expected)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		_ = os.Remove(cachePath)
		return nil, false, nil
	}
	if !ok {
		_ = os.Remove(cachePath)
		return nil, false, nil
	}
	if err := copyFile(ctx, cachePath, dstPath); err != nil {
		return nil, false, fmt.Errorf("copy cached source: %w", err)
	}
	if normalizeCacheRetainPolicy(opts.RetainPolicy) == CacheRetainNever {
		_ = os.Remove(cachePath)
	}
	return &SourceArtifact{
		Path:      dstPath,
		Checksum:  fmt.Sprintf("%s:%s", algo, expected),
		SizeBytes: size,
		Type:      source.Type,
		SourceURL: source.URL,
		CacheHit:  true,
	}, true, nil
}

func cacheEntryExpired(cachePath string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		return false, err
	}
	return time.Since(info.ModTime()) > ttl, nil
}

func normalizeCacheRetainPolicy(policy string) string {
	switch policy {
	case CacheRetainNever:
		return CacheRetainNever
	default:
		return CacheRetainAlways
	}
}

func writeVerifiedSource(dstPath string, body io.Reader, source v1alpha1.SourceSpec, algo, expected string) (*SourceArtifact, error) {
	hasher := newHash(algo)
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- Destination is the controller-owned workspace/cache path.
	if err != nil {
		return nil, fmt.Errorf("create source file: %w", err)
	}
	written, copyErr := io.Copy(io.MultiWriter(dst, hasher), body)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return nil, fmt.Errorf("write source file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return nil, fmt.Errorf("close source file: %w", closeErr)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		_ = os.Remove(dstPath)
		return nil, fmt.Errorf("checksum mismatch for source: %s:%s does not match %s", algo, actual, source.Checksum)
	}

	return &SourceArtifact{
		Path:      dstPath,
		Checksum:  fmt.Sprintf("%s:%s", algo, actual),
		SizeBytes: written,
		Type:      source.Type,
		SourceURL: source.URL,
		CacheHit:  false,
	}, nil
}

func cacheFileName(algo, expected string) string {
	return fmt.Sprintf("%s-%s.img", algo, expected)
}

func verifyFileChecksum(path, algo, expected string) (int64, bool, error) {
	file, err := os.Open(path) // #nosec G304 -- Path is a controller-owned cached source artifact path.
	if err != nil {
		return 0, false, err
	}
	defer file.Close()

	hasher := newHash(algo)
	size, err := io.Copy(hasher, file)
	if err != nil {
		return 0, false, fmt.Errorf("read cached source: %w", err)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	return size, actual == expected, nil
}

func fileChecksum(path, algo string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- Path is a controller-owned artifact path used only for checksum calculation.
	if err != nil {
		return "", fmt.Errorf("open file for checksum: %w", err)
	}
	defer file.Close()

	hasher := newHash(algo)
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("read file for checksum: %w", err)
	}
	return fmt.Sprintf("%s:%s", algo, hex.EncodeToString(hasher.Sum(nil))), nil
}

func parseChecksum(raw string) (string, string, error) {
	algo, value, ok := strings.Cut(raw, ":")
	if !ok {
		return "", "", fmt.Errorf("checksum must use algorithm:hex format")
	}
	algo = strings.ToLower(algo)
	value = strings.ToLower(value)
	switch algo {
	case "sha256":
		if len(value) != sha256.Size*2 {
			return "", "", fmt.Errorf("sha256 checksum must be %d hex characters", sha256.Size*2)
		}
	case "sha512":
		if len(value) != sha512.Size*2 {
			return "", "", fmt.Errorf("sha512 checksum must be %d hex characters", sha512.Size*2)
		}
	default:
		return "", "", fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", "", fmt.Errorf("checksum is not valid hex: %w", err)
	}
	return algo, value, nil
}

func newHash(algo string) hash.Hash {
	if algo == "sha512" {
		return sha512.New()
	}
	return sha256.New()
}

// StaticFetcher is a test helper and simple integration hook for callers that
// already have a verified source artifact.
type StaticFetcher struct {
	Source *SourceArtifact
	Err    error
}

func (f StaticFetcher) Fetch(context.Context, v1alpha1.SourceSpec, string) (*SourceArtifact, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if f.Source == nil {
		return nil, fmt.Errorf("static source artifact is nil")
	}
	return f.Source, nil
}

// CloudImageBackend prepares a prebuilt cloud image as the requested artifact
// without booting a VM. It is the first minimal backend; QEMU and
// diskimage-builder backends can be added behind the same Backend interface.
type CloudImageBackend struct{}

func NewCloudImageBackend() *CloudImageBackend {
	return &CloudImageBackend{}
}

func (b *CloudImageBackend) Name() string { return "cloud-image" }

func (b *CloudImageBackend) Supports(req BuildRequest) bool {
	return req.Image != nil && req.Image.Spec.Source.Type == "cloud-image"
}

func (b *CloudImageBackend) Build(ctx context.Context, req BackendRequest) (*platform.BuildArtifact, error) {
	if req.Source == nil {
		return nil, fmt.Errorf("source artifact is required")
	}
	artifactPath := filepath.Join(req.WorkspaceDir, fmt.Sprintf("%s.%s", defaultArtifactFileName, req.Format))
	if err := copyFile(ctx, req.Source.Path, artifactPath); err != nil {
		return nil, err
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("stat artifact: %w", err)
	}
	return &platform.BuildArtifact{
		Path:      artifactPath,
		Format:    req.Format,
		Checksum:  req.Source.Checksum,
		SizeBytes: info.Size(),
		OS:        platform.OSFamily(req.Image.Spec.OS.Family),
		Metadata:  buildMetadata(req.BuildRequest, "cloud-image"),
	}, nil
}

func buildMetadata(req BuildRequest, backend string) map[string]string {
	metadata := map[string]string{
		"backend":      backend,
		"distribution": req.Image.Spec.OS.Distribution,
		"version":      req.Image.Spec.OS.Version,
		"arch":         req.Image.Spec.OS.Arch,
	}
	if req.Image.Name != "" {
		metadata["buildID"] = req.Image.Name
	}
	return metadata
}

func copyFile(ctx context.Context, srcPath, dstPath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	src, err := os.Open(srcPath) // #nosec G304 -- Source path is the controller-owned build artifact path.
	if err != nil {
		return fmt.Errorf("open source artifact: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- Destination is the controller-owned output artifact path.
	if err != nil {
		return fmt.Errorf("create build artifact: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return fmt.Errorf("write build artifact: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("close build artifact: %w", err)
	}
	return nil
}

func (f *HTTPFetcher) validateSafeSourceURL(ctx context.Context, parsedURL *url.URL) error {
	return netguard.ValidatePublicHTTPSURL(ctx, "source URL", parsedURL.String(), netguard.Options{
		Resolver: f.resolver,
	})
}
