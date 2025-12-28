package registry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kuack-node/pkg/registry"
)

func TestProxy_Variants(t *testing.T) {
	t.Parallel()

	// Mock image fetcher to verify platform
	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		// We can't easily inspect options here without a custom mock that captures them.
		// But we can verify that invalid variants return 400.
		return empty.Image, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	tests := []struct {
		name           string
		variant        string
		expectedStatus int
	}{
		{
			name:           "Valid Alias",
			variant:        "wasm32-wasi",
			expectedStatus: http.StatusNotFound, // Image empty, so file not found, but variant valid
		},
		{
			name:           "Valid OS/Arch",
			variant:        "linux/amd64",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Invalid Format",
			variant:        "invalid",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Extra Slashes (Valid as Arch)",
			variant:        "os/arch/extra",
			expectedStatus: http.StatusNotFound, // Treated as OS="os", Arch="arch/extra"
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=img&path=file&variant="+tc.variant, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)
		})
	}
}

func TestProxy_ContentType(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		"test.wasm": []byte("wasm"),
		"test.js":   []byte("js"),
		"test.txt":  []byte("txt"),
	}
	layer := createLayer(t, files)
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	tests := []struct {
		filename     string
		expectedType string
	}{
		{"test.wasm", "application/wasm"},
		{"test.js", "application/javascript"},
		{"test.txt", "text/plain; charset=utf-8"},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=img&path="+tc.filename, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, tc.expectedType, resp.Header.Get("Content-Type"))
		})
	}
}
