package registry_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kuack-node/pkg/registry"
)

func TestProxy(t *testing.T) {
	t.Parallel()

	proxy := registry.NewProxy()
	proxy.SetFetchArtifact(func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error) {
		if ref == "nginx:latest" && artifactPath == "index.html" {
			return []byte("<html></html>"), nil
		}

		return nil, assert.AnError
	})

	ts := httptest.NewServer(proxy)
	defer ts.Close()

	// Test Fetch
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=nginx:latest&path=index.html", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "<html></html>", string(body))
}

func TestProxy_RealFetchArtifact_MockRemote(t *testing.T) {
	t.Parallel()

	// Create a layer with a file
	fileContent := []byte("hello world")

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	err := tw.WriteHeader(&tar.Header{
		Name: "test.txt",
		Mode: 0600,
		Size: int64(len(fileContent)),
	})
	require.NoError(t, err)
	_, err = tw.Write(fileContent)
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	layer := static.NewLayer(buf.Bytes(), types.DockerLayer)

	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	// Setup Proxy
	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	// Test ServeHTTP
	// Note: The query param is 'path' not 'file' based on TestProxy above
	req := httptest.NewRequest(http.MethodGet, "/?image=test-image&path=test.txt", nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	resp := w.Result()

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, fileContent, body)
}
