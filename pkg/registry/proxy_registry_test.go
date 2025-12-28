package registry_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
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

var errPullMock = errors.New("pull error")

func createLayer(t *testing.T, files map[string][]byte) v1.Layer { //nolint:ireturn // returning v1.Layer interface keeps helper compatible with registry expectations
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for name, content := range files {
		err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(content)),
		})
		require.NoError(t, err)
		_, err = tw.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())

	return static.NewLayer(buf.Bytes(), types.DockerLayer)
}

func TestProxy_RegistryFetch(t *testing.T) {
	t.Parallel()

	// Create a layer with a file
	fileContent := []byte("hello world")
	layer := createLayer(t, map[string][]byte{"test.txt": fileContent})
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	// Test Fetch with Platform
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=test.txt&variant=wasm32-wasi", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestProxy_RegistryFetch_Whiteout(t *testing.T) {
	t.Parallel()

	// Layer 1: Add file
	layer1 := createLayer(t, map[string][]byte{"test.txt": []byte("v1")})
	// Layer 2: Delete file (whiteout)
	layer2 := createLayer(t, map[string][]byte{".wh.test.txt": []byte("")})

	img, err := mutate.AppendLayers(empty.Image, layer1, layer2)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=test.txt", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	// Should be Not Found because it was deleted in layer 2
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestProxy_RegistryFetch_ImagePullError(t *testing.T) {
	t.Parallel()

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return nil, errPullMock
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=test.txt", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestProxy_RegistryFetch_FileNotFound(t *testing.T) {
	t.Parallel()

	img := empty.Image
	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=missing.txt", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
