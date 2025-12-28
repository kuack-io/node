package http_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	kuackhttp "kuack-node/pkg/http"

	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryServer(t *testing.T) {
	t.Parallel()

	server := kuackhttp.NewRegistryServer(0)
	server.SetFetchArtifact(func(ctx context.Context, ref, artifactPath string, _ *v1.Platform) ([]byte, error) {
		assert.Equal(t, "demo:latest", ref)
		assert.Equal(t, "file.txt", artifactPath)

		return []byte("payload"), nil
	})

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=demo:latest&path=file.txt", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "payload", string(body))
}
