package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kuack-node/pkg/app"
	"kuack-node/pkg/config"
	kuackhttp "kuack-node/pkg/http"
	"kuack-node/pkg/provider"
	tlsutil "kuack-node/pkg/tls"

	"k8s.io/client-go/kubernetes"
)

func setupTestConfig(t *testing.T) (*config.Config, func()) {
	t.Helper()

	fakeAPIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SetupComponents() calls Discovery().ServerVersion(), which hits GET /version.
		if r.Method == http.MethodGet && r.URL.Path == "/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"major":"1","minor":"34","gitVersion":"v1.34.0"}`))

			return
		}

		http.NotFound(w, r)
	}))

	// Create dummy kubeconfig
	kubeconfigContent := fmt.Sprintf(`
apiVersion: v1
clusters:
- cluster:
    server: %s
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: admin
  name: admin
current-context: admin
kind: Config
preferences: {}
users:
- name: admin
  user:
    username: admin
    password: password
`, fakeAPIServer.URL)
	tmpKubeconfig, err := os.CreateTemp(t.TempDir(), "kubeconfig")
	require.NoError(t, err)

	_, err = tmpKubeconfig.WriteString(kubeconfigContent)
	require.NoError(t, err)
	err = tmpKubeconfig.Close()
	require.NoError(t, err)

	// Create a real cert/key for TLS server startup in tests.
	tmpCertPath := filepath.Join(t.TempDir(), "cert.pem")
	tmpKeyPath := filepath.Join(t.TempDir(), "key.pem")
	err = tlsutil.GenerateSelfSignedCert("127.0.0.1", tmpCertPath, tmpKeyPath)
	require.NoError(t, err)

	cfg := &config.Config{
		NodeName:       "test-node",
		PublicPort:     0,
		InternalPort:   0,
		AgentToken:     "token",
		KubeconfigPath: tmpKubeconfig.Name(),
		TLSCertFile:    tmpCertPath,
		TLSKeyFile:     tmpKeyPath,
	}

	cleanup := func() {
		fakeAPIServer.Close()

		_ = os.Remove(tmpKubeconfig.Name())
		_ = os.Remove(tmpCertPath)
		_ = os.Remove(tmpKeyPath)
	}

	return cfg, cleanup
}

func TestSetupComponents(t *testing.T) {
	t.Parallel()

	restore := app.LockDepsForTesting(t)
	t.Cleanup(restore)

	cfg, cleanup := setupTestConfig(t)
	defer cleanup()

	ctx := context.Background()
	components, err := app.SetupComponents(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, components)
	require.NotNil(t, components.WASMProvider)
	require.NotNil(t, components.PublicServer)
	require.NotNil(t, components.InternalServer)
	require.NotNil(t, components.KubeClient)
}

func TestRun(t *testing.T) {
	t.Parallel()

	restore := app.LockDepsForTesting(t)
	t.Cleanup(restore)

	cfg, cleanup := setupTestConfig(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	// Mock runner that cancels context to stop Run
	mockRunner := func(ctx context.Context, p *provider.WASMProvider, k kubernetes.Interface) error {
		// Verify arguments
		require.NotNil(t, p)
		require.NotNil(t, k)

		// Signal that we are running
		cancel()

		return nil
	}

	err := app.Run(ctx, cfg, mockRunner)
	require.NoError(t, err)
}

func TestStartPublicServerAndShutdown(t *testing.T) {
	t.Parallel()

	restore := app.LockDepsForTesting(t)
	t.Cleanup(restore)

	// Setup
	p, err := provider.NewWASMProvider("test-node")
	require.NoError(t, err)

	// Use port 0 for random port
	server, err := kuackhttp.NewPublicServer(0, "test-token", p)
	require.NoError(t, err)

	ctx := t.Context()

	// Test Start
	errChan := app.StartPublicServer(ctx, server)

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Verify no immediate error
	select {
	case err := <-errChan:
		require.NoError(t, err, "Public server failed to start")
	default:
		// OK
	}

	// Test Shutdown
	err = app.ShutdownGracefully(ctx, server, nil, errChan, nil, "", nil)
	require.NoError(t, err)
}

func TestStartInternalServerAndShutdown(t *testing.T) {
	t.Parallel()

	restore := app.LockDepsForTesting(t)
	t.Cleanup(restore)

	// Setup
	p, err := provider.NewWASMProvider("test-node")
	require.NoError(t, err)

	// Use port 0 for random port
	server := kuackhttp.NewInternalServer(0, p)

	ctx := t.Context()

	// Test Start
	errChan := app.StartInternalServer(ctx, server)

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Verify no immediate error
	select {
	case err := <-errChan:
		require.NoError(t, err, "Internal server failed to start")
	default:
		// OK
	}

	// Test Shutdown
	err = app.ShutdownGracefully(ctx, nil, server, nil, errChan, "", nil)
	require.NoError(t, err)
}

func TestShutdownGracefully_Errors(t *testing.T) {
	t.Parallel()

	restore := app.LockDepsForTesting(t)
	t.Cleanup(restore)

	ctx := context.Background()
	publicErrChan := make(chan error, 1)
	internalErrChan := make(chan error, 1)

	// Simulate error
	publicErrChan <- assertError("public error")

	err := app.ShutdownGracefully(ctx, nil, nil, publicErrChan, internalErrChan, "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "public server error")

	// Reset
	publicErrChan = make(chan error, 1)

	internalErrChan = make(chan error, 1)
	internalErrChan <- assertError("internal error")

	err = app.ShutdownGracefully(ctx, nil, nil, publicErrChan, internalErrChan, "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "internal server error")
}

func assertError(msg string) error {
	return &testError{msg: msg}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
