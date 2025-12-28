package http_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kuackhttp "kuack-node/pkg/http"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestInternalServer(t *testing.T) {
	t.Parallel()

	mockLogProvider := new(kuackhttp.MockLogProvider)
	mockLogProvider.On("GetContainerLogs", mock.Anything, "default", "pod-1", "container-1", mock.Anything).
		Return(io.NopCloser(strings.NewReader("log content")), nil)

	server := kuackhttp.NewInternalServer(0, mockLogProvider)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	t.Run("Healthz", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/healthz", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("ContainerLogs", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/containerLogs/default/pod-1/container-1", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "log content", string(body))
	})
}
