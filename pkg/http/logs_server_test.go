package http_test

import (
	"context"
	"errors"
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

var errPodNotFound = errors.New("pod not found")

func TestLogsServer(t *testing.T) {
	t.Parallel()

	mockLogProvider := new(kuackhttp.MockLogProvider)
	mockLogProvider.On("GetContainerLogs", mock.Anything, "default", "pod-1", "container-1", mock.Anything).
		Return(io.NopCloser(strings.NewReader("log content")), nil)
	mockLogProvider.On("GetContainerLogs", mock.Anything, "default", "pod-missing", "container-1", mock.Anything).
		Return(nil, errPodNotFound)

	server := kuackhttp.NewLogsServer(0, mockLogProvider)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "Valid Request",
			path:           "/containerLogs/default/pod-1/container-1",
			expectedStatus: http.StatusOK,
			expectedBody:   "log content",
		},
		{
			name:           "Invalid Path - Missing Container",
			path:           "/containerLogs/default/pod-1",
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid path\n",
		},
		{
			name:           "Pod Not Found",
			path:           "/containerLogs/default/pod-missing/container-1",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "pod not found\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+tt.path, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedBody, string(body))
		})
	}
}
