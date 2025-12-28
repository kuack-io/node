package registry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kuack-node/pkg/registry"
)

var (
	errMock      = errors.New("mock error")
	errImageMock = errors.New("image not found")
)

func TestProxy_ServeHTTP_Errors(t *testing.T) {
	t.Parallel()

	proxy := registry.NewProxy()
	// Mock fetchArtifact to avoid external calls
	proxy.SetFetchArtifact(func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error) {
		return nil, errMock
	})

	ts := httptest.NewServer(proxy)
	t.Cleanup(ts.Close)

	tests := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
	}{
		{
			name:           "Method Not Allowed",
			method:         http.MethodPost,
			path:           "/?image=nginx:latest",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "Missing Image",
			method:         http.MethodGet,
			path:           "/?path=index.html",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Missing Path",
			method:         http.MethodGet,
			path:           "/?image=nginx:latest",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Invalid Path (Directory Traversal)",
			method:         http.MethodGet,
			path:           "/?image=nginx:latest&path=../../etc/passwd",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(context.Background(), tc.method, ts.URL+tc.path, nil)
			require.NoError(t, err)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)
		})
	}
}

func TestProxy_FetchArtifact_Errors(t *testing.T) {
	t.Parallel()

	// Setup Proxy with a mock ImageFetcher that returns errors
	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return nil, errImageMock
	})

	req := httptest.NewRequest(http.MethodGet, "/?image=nonexistent:latest&path=file.txt", nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	resp := w.Result()

	defer func() { _ = resp.Body.Close() }()

	// Should return 500 or 404 depending on implementation, assuming 500 for generic error
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
}
