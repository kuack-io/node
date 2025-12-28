package provider_test

import (
	"testing"
	"time"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type MockAgentStream struct {
	mock.Mock
}

func (m *MockAgentStream) SetWriteDeadline(t time.Time) error {
	args := m.Called(t)

	return args.Error(0)
}

func (m *MockAgentStream) WriteJSON(v interface{}) error {
	args := m.Called(v)

	return args.Error(0)
}

func setupTestProvider(t *testing.T) (*provider.WASMProvider, *MockAgentStream, *provider.AgentConnection) {
	t.Helper()

	p, err := provider.NewWASMProvider("node-1")
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
