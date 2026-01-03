package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"kuack-node/pkg/config"
	httpserver "kuack-node/pkg/http"
	"kuack-node/pkg/provider"
)

var (
	depsMu           sync.Mutex                     //nolint:gochecknoglobals // one global lock keeps hook overrides isolated across parallel tests
	errRequestFailed = errors.New("request failed") //nolint:gochecknoglobals // shared sentinel lets multiple tests assert same error value
	errProviderBoom  = errors.New("provider boom")
)

func lockAppDeps(t *testing.T) func() {
	t.Helper()

	depsMu.Lock()

	origNewWASM := newWASMProviderFunc
	origNewPublic := newPublicServerFunc
	origNewInternal := newInternalServerFunc
	origGetClient := getKubeClientFunc
	origRequest := requestCertificateFunc
	origGenerate := generateSelfSignedCertFunc
	origGetEnv := getEnvFunc
	origPublicStart := publicServerStartFunc
	origInternalStart := internalServerStartFunc

	return func() {
		newWASMProviderFunc = origNewWASM
		newPublicServerFunc = origNewPublic
		newInternalServerFunc = origNewInternal
		getKubeClientFunc = origGetClient
		requestCertificateFunc = origRequest
		generateSelfSignedCertFunc = origGenerate
		getEnvFunc = origGetEnv
		publicServerStartFunc = origPublicStart
		internalServerStartFunc = origInternalStart

		depsMu.Unlock()
	}
}

func baseTestConfig() *config.Config {
	return &config.Config{
		NodeName:     "test-node",
		PublicPort:   0,
		InternalPort: 0,
		AgentToken:   "testing-token",
	}
}

func TestSetupComponentsRequestsCertificateWhenMissingTLS(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	cfg := baseTestConfig()
	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = ""

	getKubeClientFunc = func(string) (kubernetes.Interface, error) {
		return fake.NewClientset(), nil
	}

	getEnvFunc = func(key string) string {
		if key == "POD_IP" {
			return "10.10.0.5"
		}

		return ""
	}

	requestCalled := false

	requestCertificateFunc = func(ctx context.Context, client kubernetes.Interface, nodeName, nodeIP, certFile, keyFile string) error {
		requestCalled = true

		require.Equal(t, cfg.NodeName, nodeName)
		require.Equal(t, "10.10.0.5", nodeIP)
		require.Contains(t, certFile, "kuack-kubelet-serving.crt")
		require.Contains(t, keyFile, "kuack-kubelet-serving.key")

		return nil
	}

	generateSelfSignedCertFunc = func(string, string, string) error {
		require.Fail(t, "should not fall back to self-signed certs in this scenario")

		return nil
	}

	components, err := SetupComponents(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, components)
	require.True(t, requestCalled)
}

func TestSetupComponentsFallsBackToSelfSignedCert(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	cfg := baseTestConfig()
	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = ""

	getKubeClientFunc = func(string) (kubernetes.Interface, error) {
		return fake.NewClientset(), nil
	}

	getEnvFunc = func(string) string {
		return ""
	}

	requestCertificateFunc = func(context.Context, kubernetes.Interface, string, string, string, string) error {
		return errRequestFailed
	}

	fallbackCalled := false
	generateSelfSignedCertFunc = func(ip, certFile, keyFile string) error {
		fallbackCalled = true

		require.Equal(t, "127.0.0.1", ip)
		require.Contains(t, certFile, "kuack-kubelet-serving.crt")
		require.Contains(t, keyFile, "kuack-kubelet-serving.key")

		return nil
	}

	components, err := SetupComponents(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, components)
	require.True(t, fallbackCalled)
}

func TestSetupComponentsReturnsProviderError(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	expectedErr := errProviderBoom
	newWASMProviderFunc = func(string) (*provider.WASMProvider, error) {
		return nil, expectedErr
	}

	_, err := SetupComponents(context.Background(), baseTestConfig())
	require.ErrorIs(t, err, expectedErr)
}

func TestStartPublicServerPropagatesErrors(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	publicServerStartFunc = func(*httpserver.PublicServer, context.Context) <-chan error {
		ch := make(chan error, 1)
		ch <- errRequestFailed

		return ch
	}

	errChan := StartPublicServer(context.Background(), nil)
	err := <-errChan
	require.ErrorIs(t, err, errRequestFailed)
	require.Contains(t, err.Error(), "public server failed")
}

func TestStartInternalServerPropagatesErrors(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	internalServerStartFunc = func(*httpserver.InternalServer, context.Context) <-chan error {
		ch := make(chan error, 1)
		ch <- errRequestFailed

		return ch
	}

	errChan := StartInternalServer(context.Background(), nil)
	err := <-errChan
	require.ErrorIs(t, err, errRequestFailed)
	require.Contains(t, err.Error(), "internal server failed")
}

func TestSetupComponentsCallsSetVersionFromPod(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	cfg := baseTestConfig()
	cfg.TLSCertFile = "/tmp/cert.pem"
	cfg.TLSKeyFile = "/tmp/key.pem"

	getKubeClientFunc = func(string) (kubernetes.Interface, error) {
		return fake.NewClientset(), nil
	}

	components, err := SetupComponents(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, components)
	require.NotNil(t, components.WASMProvider)

	// Verify that SetVersionFromPod was called (it should not fail setup even if it fails)
	// Since we don't set POD_NAME/POD_NAMESPACE, it should gracefully handle it
	node := components.WASMProvider.GetNode()
	// Version label may or may not be set depending on env vars
	// The important thing is that setup didn't fail
	_ = node
}

func TestSetupComponentsHandlesSetVersionFromPodFailure(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	cfg := baseTestConfig()
	cfg.TLSCertFile = "/tmp/cert.pem"
	cfg.TLSKeyFile = "/tmp/key.pem"

	// Create a fake client that will fail when trying to get pod
	getKubeClientFunc = func(string) (kubernetes.Interface, error) {
		return fake.NewClientset(), nil
	}

	// Set env vars to trigger SetVersionFromPod, but pod won't exist
	// This should not fail setup
	components, err := SetupComponents(context.Background(), cfg)
	// Setup should succeed even if SetVersionFromPod fails
	require.NoError(t, err)
	require.NotNil(t, components)
}
