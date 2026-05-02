package builder_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func checksumSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func testImage(source v1alpha1.SourceSpec, format string) *v1alpha1.VMImage {
	return &v1alpha1.VMImage{
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{
				Family:       "linux",
				Distribution: "ubuntu",
				Version:      "24.04",
				Arch:         "amd64",
			},
			Source: source,
			Targets: []v1alpha1.TargetSpec{
				{
					ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws"},
					Format:            format,
				},
			},
		},
	}
}

func TestHTTPFetcher_Fetch_VerifiesChecksumAndUsesPrivateFileMode(t *testing.T) {
	payload := []byte("cloud image bytes")
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme != "https" {
				t.Fatalf("scheme = %q, want https", req.URL.Scheme)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	src, err := fetcher.Fetch(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksumSHA256(payload),
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	got, err := os.ReadFile(src.Path)
	if err != nil {
		t.Fatalf("read fetched source: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("fetched bytes = %q, want %q", got, payload)
	}

	info, err := os.Stat(src.Path)
	if err != nil {
		t.Fatalf("stat fetched source: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("source file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestHTTPFetcher_Fetch_RejectsPlainHTTP(t *testing.T) {
	fetcher := builder.NewHTTPFetcher(http.DefaultClient)

	_, err := fetcher.Fetch(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "http://images.example.test/ubuntu.img",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, t.TempDir())
	if err == nil {
		t.Fatal("Fetch should reject plain HTTP URLs")
	}
}

func TestHTTPFetcher_Fetch_RejectsRawIPHost(t *testing.T) {
	fetcher := builder.NewHTTPFetcher(http.DefaultClient)

	_, err := fetcher.Fetch(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://127.0.0.1/ubuntu.img",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, t.TempDir())
	if err == nil {
		t.Fatal("Fetch should reject raw IP hosts")
	}
	if !strings.Contains(err.Error(), "raw IP") {
		t.Fatalf("error = %q, want raw IP rejection", err.Error())
	}
}

func TestHTTPFetcher_Fetch_RejectsHTTPRedirect(t *testing.T) {
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme == "https" {
				return &http.Response{
					StatusCode: http.StatusFound,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     http.Header{"Location": []string{"http://images.example.test/ubuntu.img"}},
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte("downgraded"))),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	})

	_, err := fetcher.Fetch(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, t.TempDir())
	if err == nil {
		t.Fatal("Fetch should reject HTTPS-to-HTTP redirects")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("error = %q, want redirect rejection", err.Error())
	}
}

func TestHTTPFetcher_Fetch_RejectsChecksumMismatch(t *testing.T) {
	payload := []byte("unexpected")
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	_, err := fetcher.Fetch(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, t.TempDir())
	if err == nil {
		t.Fatal("Fetch should reject checksum mismatch")
	}
}

func TestHTTPFetcher_FetchWithCache_WritesVerifiedSourceToCache(t *testing.T) {
	payload := []byte("cached cloud image bytes")
	cacheDir := t.TempDir()
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	src, err := fetcher.FetchWithCache(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksumSHA256(payload),
	}, t.TempDir(), cacheDir, builder.SourceCacheOptions{})
	if err != nil {
		t.Fatalf("FetchWithCache returned error: %v", err)
	}

	cachePath := filepath.Join(cacheDir, "sha256-"+strings.TrimPrefix(checksumSHA256(payload), "sha256:")+".img")
	cached, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("expected cache file %s: %v", cachePath, err)
	}
	if !bytes.Equal(cached, payload) {
		t.Fatalf("cached bytes = %q, want %q", cached, payload)
	}
	if src.CacheHit {
		t.Fatal("first download should not report cache hit")
	}
}

func TestHTTPFetcher_FetchWithCache_UsesValidCachedSourceWithoutNetwork(t *testing.T) {
	payload := []byte("already cached image")
	cacheDir := t.TempDir()
	checksum := checksumSHA256(payload)
	cachePath := filepath.Join(cacheDir, "sha256-"+strings.TrimPrefix(checksum, "sha256:")+".img")
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("network should not be used when cache entry is valid")
			return nil, nil
		}),
	})

	src, err := fetcher.FetchWithCache(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksum,
	}, t.TempDir(), cacheDir, builder.SourceCacheOptions{})
	if err != nil {
		t.Fatalf("FetchWithCache returned error: %v", err)
	}
	if !src.CacheHit {
		t.Fatal("expected cache hit")
	}
	got, err := os.ReadFile(src.Path)
	if err != nil {
		t.Fatalf("read workspace source: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("workspace bytes = %q, want %q", got, payload)
	}
}

func TestHTTPFetcher_FetchWithCache_RefetchesCorruptCache(t *testing.T) {
	payload := []byte("correct image")
	cacheDir := t.TempDir()
	checksum := checksumSHA256(payload)
	cachePath := filepath.Join(cacheDir, "sha256-"+strings.TrimPrefix(checksum, "sha256:")+".img")
	if err := os.WriteFile(cachePath, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt cache file: %v", err)
	}

	networkCalls := 0
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			networkCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	src, err := fetcher.FetchWithCache(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksum,
	}, t.TempDir(), cacheDir, builder.SourceCacheOptions{})
	if err != nil {
		t.Fatalf("FetchWithCache returned error: %v", err)
	}
	if networkCalls != 1 {
		t.Fatalf("networkCalls = %d, want 1", networkCalls)
	}
	if src.CacheHit {
		t.Fatal("corrupt cache should not report cache hit")
	}
}

func TestHTTPFetcher_FetchWithCache_RefetchesExpiredCache(t *testing.T) {
	payload := []byte("fresh image")
	cacheDir := t.TempDir()
	checksum := checksumSHA256(payload)
	cachePath := filepath.Join(cacheDir, "sha256-"+strings.TrimPrefix(checksum, "sha256:")+".img")
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatalf("age cache file: %v", err)
	}

	networkCalls := 0
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			networkCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	src, err := fetcher.FetchWithCache(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksum,
	}, t.TempDir(), cacheDir, builder.SourceCacheOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("FetchWithCache returned error: %v", err)
	}
	if networkCalls != 1 {
		t.Fatalf("networkCalls = %d, want 1", networkCalls)
	}
	if src.CacheHit {
		t.Fatal("expired cache should not report cache hit")
	}
}

func TestHTTPFetcher_FetchWithCache_RemovesCacheEntryWhenRetainNever(t *testing.T) {
	payload := []byte("cached image")
	cacheDir := t.TempDir()
	checksum := checksumSHA256(payload)
	cachePath := filepath.Join(cacheDir, "sha256-"+strings.TrimPrefix(checksum, "sha256:")+".img")
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("network should not be used when cache entry is valid")
			return nil, nil
		}),
	})

	src, err := fetcher.FetchWithCache(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksum,
	}, t.TempDir(), cacheDir, builder.SourceCacheOptions{RetainPolicy: builder.CacheRetainNever})
	if err != nil {
		t.Fatalf("FetchWithCache returned error: %v", err)
	}
	if !src.CacheHit {
		t.Fatal("expected cache hit before retain cleanup")
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("cache entry should be removed, stat err = %v", err)
	}
}

func TestHTTPFetcher_FetchWithCache_DoesNotStoreDownloadedSourceWhenRetainNever(t *testing.T) {
	payload := []byte("downloaded image")
	cacheDir := t.TempDir()
	checksum := checksumSHA256(payload)
	fetcher := builder.NewHTTPFetcher(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	src, err := fetcher.FetchWithCache(context.Background(), v1alpha1.SourceSpec{
		Type:     "cloud-image",
		URL:      "https://images.example.test/ubuntu.img",
		Checksum: checksum,
	}, t.TempDir(), cacheDir, builder.SourceCacheOptions{RetainPolicy: builder.CacheRetainNever})
	if err != nil {
		t.Fatalf("FetchWithCache returned error: %v", err)
	}
	if src.CacheHit {
		t.Fatal("download should not report cache hit")
	}
	cachePath := filepath.Join(cacheDir, "sha256-"+strings.TrimPrefix(checksum, "sha256:")+".img")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("cache entry should not be written, stat err = %v", err)
	}
}

func TestEngine_Build_CloudImageCreatesArtifact(t *testing.T) {
	payload := []byte("cloud image bytes")
	workspace := t.TempDir()
	engine := builder.NewEngine(builder.EngineOptions{
		Fetcher: builder.NewHTTPFetcher(&http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(payload)),
					Header:     make(http.Header),
				}, nil
			}),
		}),
		Backends: []builder.Backend{builder.NewCloudImageBackend()},
	})

	artifact, err := engine.Build(context.Background(), builder.BuildRequest{
		Image:        testImage(v1alpha1.SourceSpec{Type: "cloud-image", URL: "https://images.example.test/ubuntu.img", Checksum: checksumSHA256(payload)}, "raw"),
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if artifact.Format != platform.FormatRaw {
		t.Fatalf("format = %q, want raw", artifact.Format)
	}
	if artifact.OS != platform.OSFamilyLinux {
		t.Fatalf("OS = %q, want linux", artifact.OS)
	}
	if artifact.Checksum != checksumSHA256(payload) {
		t.Fatalf("checksum = %q, want %q", artifact.Checksum, checksumSHA256(payload))
	}
	if filepath.Dir(artifact.Path) != workspace {
		t.Fatalf("artifact path %q should be inside workspace %q", artifact.Path, workspace)
	}
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatalf("artifact file missing: %v", err)
	}
}

func TestEngine_Build_PassesCacheDirToFetcher(t *testing.T) {
	fetcher := &recordingCacheFetcher{
		source: &builder.SourceArtifact{
			Path:     filepath.Join(t.TempDir(), "source.img"),
			Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	if err := os.WriteFile(fetcher.source.Path, []byte("source"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	engine := builder.NewEngine(builder.EngineOptions{
		Fetcher:  fetcher,
		Backends: []builder.Backend{builder.NewCloudImageBackend()},
	})
	cacheDir := t.TempDir()

	_, err := engine.Build(context.Background(), builder.BuildRequest{
		Image:        testImage(v1alpha1.SourceSpec{Type: "cloud-image", URL: "https://images.example.test/ubuntu.img", Checksum: fetcher.source.Checksum}, "raw"),
		WorkspaceDir: t.TempDir(),
		CacheDir:     cacheDir,
		CacheTTL:     30 * time.Minute,
		CacheRetain:  builder.CacheRetainNever,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if fetcher.cacheDir != cacheDir {
		t.Fatalf("cacheDir = %q, want %q", fetcher.cacheDir, cacheDir)
	}
	if fetcher.cacheTTL != 30*time.Minute {
		t.Fatalf("cacheTTL = %s, want 30m", fetcher.cacheTTL)
	}
	if fetcher.retain != builder.CacheRetainNever {
		t.Fatalf("retain = %q, want %q", fetcher.retain, builder.CacheRetainNever)
	}
}

func TestEngine_Build_ReturnsErrorWhenNoBackendSupportsRequest(t *testing.T) {
	engine := builder.NewEngine(builder.EngineOptions{
		Fetcher: builder.StaticFetcher{
			Source: &builder.SourceArtifact{Path: filepath.Join(t.TempDir(), "source.img")},
		},
	})

	_, err := engine.Build(context.Background(), builder.BuildRequest{
		Image:        testImage(v1alpha1.SourceSpec{Type: "iso", URL: "https://images.example.test/os.iso", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "raw"),
		WorkspaceDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Build should fail when no backend supports the request")
	}
}

func TestEngine_Build_ClassifiesSourceFetchFailure(t *testing.T) {
	engine := builder.NewEngine(builder.EngineOptions{
		Fetcher: builder.StaticFetcher{Err: fmt.Errorf("network unavailable")},
		Backends: []builder.Backend{
			builder.NewCloudImageBackend(),
		},
	})

	_, err := engine.Build(context.Background(), builder.BuildRequest{
		Image:        testImage(v1alpha1.SourceSpec{Type: "cloud-image", URL: "https://images.example.test/ubuntu.img", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "raw"),
		WorkspaceDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Build should fail")
	}
	if got := builder.ErrorReason(err); got != builder.ReasonSourceFetchFailed {
		t.Fatalf("ErrorReason = %q, want %q", got, builder.ReasonSourceFetchFailed)
	}
}

type recordingCacheFetcher struct {
	source   *builder.SourceArtifact
	cacheDir string
	cacheTTL time.Duration
	retain   string
}

func (f *recordingCacheFetcher) Fetch(context.Context, v1alpha1.SourceSpec, string) (*builder.SourceArtifact, error) {
	return f.source, nil
}

func (f *recordingCacheFetcher) FetchWithCache(_ context.Context, _ v1alpha1.SourceSpec, _ string, cacheDir string, opts builder.SourceCacheOptions) (*builder.SourceArtifact, error) {
	f.cacheDir = cacheDir
	f.cacheTTL = opts.TTL
	f.retain = opts.RetainPolicy
	return f.source, nil
}
