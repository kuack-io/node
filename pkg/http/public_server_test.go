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

func TestPublicServer(t *testing.T) {
	t.Parallel()

	mockProvider := new(kuackhttp.MockAgentManager)
	server, err := kuackhttp.NewPublicServer(0, "test-token", mockProvider)
	require.NoError(t, err)
	server.SetRegistryFetchArtifact(func(ctx context.Context, ref, artifactPath string, _ *v1.Platform) ([]byte, error) {
		assert.Equal(t, "demo:latest", ref)
		assert.Equal(t, "module.wasm", artifactPath)

		return []byte("wasm-bytes"), nil
	})

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Test Registry Unauthorized
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/registry", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Test Registry Authorized with valid fetch parameters
	req, err = http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		ts.URL+"/registry?token=test-token&image=demo:latest&path=module.wasm",
		nil,
	)
	require.NoError(t, err)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "wasm-bytes", string(body))
}
