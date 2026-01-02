package app_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kuack-node/pkg/app"
	"kuack-node/pkg/config"
	"kuack-node/pkg/http"
	"kuack-node/pkg/provider"

	"k8s.io/client-go/kubernetes"
)

func setupTestConfig(t *testing.T) (*config.Config, func()) {
	t.Helper()

	// Create dummy kubeconfig
	kubeconfigContent := `
apiVersion: v1
clusters:
- cluster:
    server: https://localhost:6443
    insecure-skip-tls-verify: true
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
`
	tmpKubeconfig, err := os.CreateTemp(t.TempDir(), "kubeconfig")
	require.NoError(t, err)

	_, err = tmpKubeconfig.WriteString(kubeconfigContent)
	require.NoError(t, err)
	err = tmpKubeconfig.Close()
	require.NoError(t, err)

	// Create dummy cert/key
	tmpCert, err := os.CreateTemp(t.TempDir(), "cert")
	require.NoError(t, err)
	err = tmpCert.Close()
	require.NoError(t, err)

	tmpKey, err := os.CreateTemp(t.TempDir(), "key")
	require.NoError(t, err)
	err = tmpKey.Close()
	require.NoError(t, err)

	cfg := &config.Config{
		NodeName:       "test-node",
		PublicPort:     0,
		InternalPort:   0,
		AgentToken:     "token",
		KubeconfigPath: tmpKubeconfig.Name(),
		TLSCertFile:    tmpCert.Name(),
		TLSKeyFile:     tmpKey.Name(),
	}

	cleanup := func() {
		_ = os.Remove(tmpKubeconfig.Name())
		_ = os.Remove(tmpCert.Name())
		_ = os.Remove(tmpKey.Name())
	}

	return cfg, cleanup
}

func TestSetupComponents(t *testing.T) {
	t.Parallel()

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

	// Setup
	p, err := provider.NewWASMProvider("test-node")
	require.NoError(t, err)

	// Use port 0 for random port
	server, err := http.NewPublicServer(0, "test-token", p)
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

	// Setup
	p, err := provider.NewWASMProvider("test-node")
	require.NoError(t, err)

	// Use port 0 for random port
	server := http.NewInternalServer(0, p)

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
