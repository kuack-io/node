package http //nolint:testpackage // tests exercise internal helper functions

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

var (
	errFetchStubFailure   = errors.New("fetch stub failure")
	errRemoteImageFailure = errors.New("remote failure")
	remoteImageMu         sync.Mutex //nolint:gochecknoglobals // guards remoteImageFunc overrides in parallel tests
)

func TestNormalizeArtifactPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"/pkg/file.wasm", "pkg/file.wasm"},
		{"../escape", ""},
		{"", ""},
		{"./pkg/./file.js", "pkg/file.js"},
	}

	for idx, tt := range tests {
		testCase := tt

		t.Run(fmt.Sprintf("case_%d", idx), func(t *testing.T) {
			t.Parallel()

			if got := normalizeArtifactPath(testCase.input); got != testCase.want {
				t.Fatalf("normalizeArtifactPath(%q)=%q want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestExtractFileFromImage(t *testing.T) {
	t.Parallel()

	layer1 := buildLayerBuffer(t, map[string]string{
		"pkg/old.txt": "old",
	})
	layer2 := buildLayerBuffer(t, map[string]string{
		"pkg/old.txt":     "new",
		"pkg/target.wasm": "wasm-bytes",
	})

	first, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layer1.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("layer1 from reader: %v", err)
	}

	second, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layer2.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("layer2 from reader: %v", err)
	}

	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: first})
	if err != nil {
		t.Fatalf("append layer1: %v", err)
	}

	img, err = mutate.Append(img, mutate.Addendum{Layer: second})
	if err != nil {
		t.Fatalf("append layer2: %v", err)
	}

	data, err := extractFileFromImage(img, "/pkg/target.wasm")
	if err != nil {
		t.Fatalf("extractFileFromImage error: %v", err)
	}

	if string(data) != "wasm-bytes" {
		t.Fatalf("unexpected data %q", string(data))
	}
}

func TestRegistryProxyServeHTTPCache(t *testing.T) {
	t.Parallel()

	layerBuf := buildLayerBuffer(t, map[string]string{"pkg/app.js": "console.log('ok')"})

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layerBuf.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("layer from reader: %v", err)
	}

	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: layer})
	if err != nil {
		t.Fatalf("append layer: %v", err)
	}

	proxy := NewRegistryProxy()

	var fetchCount int

	proxy.fetchArtifact = func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error) {
		fetchCount++

		if ref != "test" {
			t.Fatalf("unexpected ref %q", ref)
		}

		if platform == nil {
			t.Fatal("expected platform to be resolved")
		}

		if platform.OS != "wasi" || platform.Architecture != "wasm32" {
			t.Fatalf("unexpected platform %+v", platform)
		}

		data, err := extractFileFromImage(img, artifactPath)
		if err != nil {
			return nil, err
		}

		return data, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/registry?image=test&path=/pkg/app.js&variant=browser", nil)
	resp := httptest.NewRecorder()

	proxy.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.Code)
	}

	if got := resp.Body.String(); got != "console.log('ok')" {
		t.Fatalf("unexpected body %q", got)
	}

	// Second identical request should hit cache and avoid another registry call.
	resp2 := httptest.NewRecorder()
	proxy.ServeHTTP(resp2, req)

	if resp2.Code != http.StatusOK {
		t.Fatalf("unexpected status on cache hit %d", resp2.Code)
	}

	if fetchCount != 1 {
		t.Fatalf("expected single fetch, got %d", fetchCount)
	}
}

func TestRegistryProxyServeHTTPValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		url         string
		wantStatus  int
		expectFetch bool
	}{
		{
			name:        "options preflight",
			method:      http.MethodOptions,
			url:         "/registry?image=test&path=pkg/app.js",
			wantStatus:  http.StatusNoContent,
			expectFetch: false,
		},
		{
			name:        "invalid method",
			method:      http.MethodPost,
			url:         "/registry?image=test&path=pkg/app.js",
			wantStatus:  http.StatusMethodNotAllowed,
			expectFetch: false,
		},
		{
			name:        "missing image",
			method:      http.MethodGet,
			url:         "/registry?path=pkg/app.js",
			wantStatus:  http.StatusBadRequest,
			expectFetch: false,
		},
		{
			name:        "missing path",
			method:      http.MethodGet,
			url:         "/registry?image=test",
			wantStatus:  http.StatusBadRequest,
			expectFetch: false,
		},
		{
			name:        "invalid variant",
			method:      http.MethodGet,
			url:         "/registry?image=test&path=pkg/app.js&variant=unknown",
			wantStatus:  http.StatusBadRequest,
			expectFetch: false,
		},
	}

	for _, tt := range tests {
		testCase := tt
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			proxy := NewRegistryProxy()

			var fetchCalled bool

			proxy.fetchArtifact = func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error) {
				fetchCalled = true

				return []byte("payload"), nil
			}

			req := httptest.NewRequest(testCase.method, testCase.url, nil)
			recorder := httptest.NewRecorder()

			proxy.ServeHTTP(recorder, req)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status=%d want %d", recorder.Code, testCase.wantStatus)
			}

			if fetchCalled && !testCase.expectFetch {
				t.Fatalf("fetchArtifact called for %s", testCase.name)
			}
		})
	}
}

func TestRegistryProxyServeHTTPFetchFailure(t *testing.T) {
	t.Parallel()

	proxy := NewRegistryProxy()
	proxy.fetchArtifact = func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error) {
		return nil, errFetchStubFailure
	}

	req := httptest.NewRequest(http.MethodGet, "/registry?image=test&path=pkg/app.js", nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want %d", recorder.Code, http.StatusBadGateway)
	}
}

func TestResolveVariantPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    *v1.Platform
		wantKey string
		wantErr bool
	}{
		{
			name:    "empty variant",
			input:   "",
			want:    nil,
			wantKey: "",
		},
		{
			name:    "browser alias",
			input:   "browser",
			want:    &v1.Platform{OS: "wasi", Architecture: "wasm32"},
			wantKey: "wasi/wasm32",
		},
		{
			name:    "explicit platform",
			input:   "linux/amd64",
			want:    &v1.Platform{OS: "linux", Architecture: "amd64"},
			wantKey: "linux/amd64",
		},
		{
			name:    "invalid variant",
			input:   "totally-unknown",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		testCase := tt
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			platform, key, err := resolveVariantPlatform(testCase.input)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", testCase.input)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if testCase.want == nil {
				if platform != nil {
					t.Fatalf("expected nil platform, got %+v", platform)
				}
			} else {
				if platform == nil {
					t.Fatal("expected platform, got nil")
				}

				if platform.OS != testCase.want.OS || platform.Architecture != testCase.want.Architecture {
					t.Fatalf("platform mismatch got %+v want %+v", platform, testCase.want)
				}
			}

			if key != testCase.wantKey {
				t.Fatalf("cache key mismatch got %q want %q", key, testCase.wantKey)
			}
		})
	}
}

func TestDetectContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "wasm", path: "module.wasm", want: "application/wasm"},
		{name: "javascript", path: "script.js", want: "application/javascript"},
		{name: "known-extension", path: "note.txt", want: mime.TypeByExtension(".txt")},
		{name: "unknown", path: "binary.bin", want: "application/octet-stream"},
	}

	for _, tt := range tests {
		caseData := tt
		t.Run(caseData.name, func(t *testing.T) {
			t.Parallel()

			if got := detectContentType(caseData.path); got != caseData.want {
				t.Fatalf("detectContentType(%q)=%q want %q", caseData.path, got, caseData.want)
			}
		})
	}
}

func TestNormalizeArchitecture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"x86_64", "amd64"},
		{"aarch64", "arm64"},
		{"armv7", "arm"},
		{"mips", "mips"},
	}

	for idx, tt := range tests {
		caseData := tt

		t.Run(fmt.Sprintf("arch_%d", idx), func(t *testing.T) {
			t.Parallel()

			if got := normalizeArchitecture(caseData.input); got != caseData.want {
				t.Fatalf("normalizeArchitecture(%q)=%q want %q", caseData.input, got, caseData.want)
			}
		})
	}
}

func TestPlatformCacheKey(t *testing.T) {
	t.Parallel()

	if key := platformCacheKey(nil); key != "" {
		t.Fatalf("expected empty key for nil platform, got %q", key)
	}

	pl := &v1.Platform{OS: "linux", Architecture: "amd64"}
	if key := platformCacheKey(pl); key != "linux/amd64" {
		t.Fatalf("unexpected key %q", key)
	}
}

func TestWhiteoutTarget(t *testing.T) {
	t.Parallel()

	got := whiteoutTarget("pkg/.wh.old.txt")
	if got != "pkg/old.txt" {
		t.Fatalf("unexpected target %q", got)
	}
}

func TestFetchArtifactFromRegistry_Success(t *testing.T) {
	t.Parallel()

	img := buildImageWithFiles(t, map[string]string{"pkg/app.js": "console.log('ok')"})
	ref := "example.com/test:tag"

	stubRemoteImage(t, img, nil, func(r name.Reference, opts []remote.Option) {
		if r.Name() != ref {
			t.Fatalf("unexpected ref %q", r.Name())
		}

		if len(opts) != 2 { // WithContext + WithUserAgent
			t.Fatalf("unexpected options length %d", len(opts))
		}
	})

	data, err := fetchArtifactFromRegistry(context.Background(), ref, "pkg/app.js", nil)
	if err != nil {
		t.Fatalf("fetchArtifactFromRegistry() error = %v", err)
	}

	if got := string(data); got != "console.log('ok')" {
		t.Fatalf("unexpected data %q", got)
	}
}

func TestFetchArtifactFromRegistry_WithPlatform(t *testing.T) {
	t.Parallel()

	img := buildImageWithFiles(t, map[string]string{"pkg/app.wasm": "bytes"})
	pl := &v1.Platform{OS: "wasi", Architecture: "wasm32"}

	stubRemoteImage(t, img, nil, func(_ name.Reference, opts []remote.Option) {
		if len(opts) != 3 { // WithContext + WithUserAgent + WithPlatform
			t.Fatalf("unexpected options length %d", len(opts))
		}
	})

	data, err := fetchArtifactFromRegistry(context.Background(), "example.com/test:platform", "pkg/app.wasm", pl)
	if err != nil {
		t.Fatalf("fetchArtifactFromRegistry() error = %v", err)
	}

	if string(data) != "bytes" {
		t.Fatalf("unexpected data %q", string(data))
	}
}

func TestFetchArtifactFromRegistry_Error(t *testing.T) {
	t.Parallel()

	stubRemoteImage(t, nil, errRemoteImageFailure, nil)

	_, err := fetchArtifactFromRegistry(context.Background(), "example.com/test:error", "pkg/missing", nil)
	if !errors.Is(err, errRemoteImageFailure) {
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		t.Fatalf("unexpected error %v", err)
	}
}

func stubRemoteImage(
	t *testing.T,
	image v1.Image,
	retErr error,
	assertFn func(name.Reference, []remote.Option),
) {
	t.Helper()

	remoteImageMu.Lock()

	prev := remoteImageFunc
	remoteImageFunc = func(ref name.Reference, opts ...remote.Option) (v1.Image, error) {
		if assertFn != nil {
			assertFn(ref, opts)
		}

		if retErr != nil {
			return nil, retErr
		}

		return image, nil
	}

	t.Cleanup(func() {
		remoteImageFunc = prev

		remoteImageMu.Unlock()
	})
}

func buildLayerBuffer(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)

	for name, content := range files {
		err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o600,
			Size: int64(len(content)),
		})
		if err != nil {
			t.Fatalf("write header: %v", err)
		}

		_, err = tw.Write([]byte(content))
		if err != nil {
			t.Fatalf("write content: %v", err)
		}
	}

	err := tw.Close()
	if err != nil {
		t.Fatalf("close writer: %v", err)
	}

	return buf
}

type testImage struct {
	v1.Image
}

func buildImageWithFiles(t *testing.T, files map[string]string) *testImage {
	t.Helper()

	layerBuf := buildLayerBuffer(t, files)

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layerBuf.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("layer from reader: %v", err)
	}

	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: layer})
	if err != nil {
		t.Fatalf("append layer: %v", err)
	}

	return &testImage{Image: img}
}
