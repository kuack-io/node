package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuack-node/pkg/config"
	httpserver "kuack-node/pkg/http"
	"kuack-node/pkg/provider"
)

var (
	depsMu              sync.Mutex                     //nolint:gochecknoglobals // one global lock keeps hook overrides isolated across parallel tests
	errRequestFailed    = errors.New("request failed") //nolint:gochecknoglobals // shared sentinel lets multiple tests assert same error value
	errProviderBoom     = errors.New("provider boom")
	errPublicServer     = errors.New("public server failed")
	errKubeClient       = errors.New("kube client failed")
	errSelfSignedFailed = errors.New("self-signed failed")
	errDeleteFailed     = errors.New("delete failed")
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

func TestSetupComponentsReturnsPublicServerError(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	newPublicServerFunc = func(int, string, provider.AgentManager) (*httpserver.PublicServer, error) {
		return nil, errPublicServer
	}

	_, err := SetupComponents(context.Background(), baseTestConfig())
	require.ErrorIs(t, err, errPublicServer)
	require.Contains(t, err.Error(), "failed to create Public server")
}

func TestSetupComponentsReturnsKubeClientError(t *testing.T) {
	t.Parallel()

	restore := lockAppDeps(t)
	t.Cleanup(restore)

	getKubeClientFunc = func(string) (kubernetes.Interface, error) {
		return nil, errKubeClient
	}

	_, err := SetupComponents(context.Background(), baseTestConfig())
	require.ErrorIs(t, err, errKubeClient)
	require.Contains(t, err.Error(), "failed to get Kubernetes client")
}

func TestSetupComponentsReturnsSelfSignedCertError(t *testing.T) {
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

	generateSelfSignedCertFunc = func(string, string, string) error {
		return errSelfSignedFailed
	}

	_, err := SetupComponents(context.Background(), cfg)
	require.ErrorIs(t, err, errSelfSignedFailed)
	require.Contains(t, err.Error(), "failed to generate self-signed TLS certificate")
}

func TestDeleteNodeFromClusterNilClient(t *testing.T) {
	t.Parallel()

	err := DeleteNodeFromCluster(context.Background(), "test-node", nil)
	require.ErrorIs(t, err, ErrKubeClientRequired)
}

func TestDeleteNodeFromClusterEmptyNodeName(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewClientset()
	err := DeleteNodeFromCluster(context.Background(), "", kubeClient)
	require.ErrorIs(t, err, ErrNodeNameRequired)
}

func TestDeleteNodeFromClusterSuccess(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewClientset()

	// Create a node first
	_, err := kubeClient.CoreV1().Nodes().Create(context.Background(), &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-to-delete",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	// Delete the node
	err = DeleteNodeFromCluster(context.Background(), "test-node-to-delete", kubeClient)
	require.NoError(t, err)
}

func TestDeleteNodeFromClusterDeleteError(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewClientset()

	// Add reactor to simulate error
	kubeClient.PrependReactor("delete", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errDeleteFailed
	})

	err := DeleteNodeFromCluster(context.Background(), "test-node", kubeClient)
	require.ErrorIs(t, err, errDeleteFailed)
	require.Contains(t, err.Error(), "failed to delete node")
}
