package provider_test

import (
	"context"
	"testing"
	"time"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/registry"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

type MockAgentStream struct {
	mock.Mock
}

func (m *MockAgentStream) SetWriteDeadline(t time.Time) error {
	args := m.Called(t)

	return args.Error(0)
}

func (m *MockAgentStream) WriteJSON(v any) error {
	args := m.Called(v)

	return args.Error(0)
}

type MockResolver struct {
	mock.Mock
}

func (m *MockResolver) ResolveWasmConfig(ctx context.Context, imageRef string) (*registry.WasmConfig, error) {
	args := m.Called(ctx, imageRef)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	cfg, ok := args.Get(0).(*registry.WasmConfig)
	if !ok {
		// This should not happen in tests unless mock is set up incorrectly
		panic("mock argument 0 is not *registry.WasmConfig")
	}

	return cfg, args.Error(1)
}

func setupTestProvider(t *testing.T) (*provider.WASMProvider, *MockAgentStream, *provider.AgentConnection) {
	t.Helper()

	// Use MockResolver by default
	mockResolver := new(MockResolver)
	mockResolver.On("ResolveWasmConfig", mock.Anything, mock.Anything).Return(&registry.WasmConfig{
		Type: "wasi",
		Path: "/test.wasm",
	}, nil)

	p, err := provider.NewWASMProvider("node-1", mockResolver)
	require.NoError(t, err)

	// Set kubelet version using a fake Kubernetes client
	// This is required before GetNode() can be called
	fakeClient := fake.NewClientset()
	err = p.SetKubeletVersionFromCluster(context.Background(), fakeClient)
	require.NoError(t, err)

	mockStream := new(MockAgentStream)
	mockStream.On("SetWriteDeadline", mock.Anything).Return(nil)
	mockStream.On("WriteJSON", mock.Anything).Return(nil)

	agent := &provider.AgentConnection{
		UUID:      "agent-1",
		BrowserID: "browser-1",
		Stream:    mockStream,
		Resources: provider.ResourceSpec{
			CPU:    resource.MustParse("4"),
			Memory: resource.MustParse("8Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
	}

	return p, mockStream, agent
}
