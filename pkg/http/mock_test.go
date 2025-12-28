// tests live in package http to hit unexported helpers.
package http //nolint:testpackage

import (
	"context"
	"io"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/mock"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

// MockAgentManager is a mock implementation of AgentManager interface.
type MockAgentManager struct {
	mock.Mock
}

func (m *MockAgentManager) AddAgent(ctx context.Context, agent *provider.AgentConnection) {
	m.Called(ctx, agent)
}

func (m *MockAgentManager) RemoveAgent(agentID string) {
	m.Called(agentID)
}

func (m *MockAgentManager) GetAgent(agentID string) (*provider.AgentConnection, bool) {
	args := m.Called(agentID)
	conn, _ := args.Get(0).(*provider.AgentConnection)

	return conn, args.Bool(1)
}

func (m *MockAgentManager) UpdatePodStatus(namespace, name string, status provider.AgentPodStatus) error {
	args := m.Called(namespace, name, status)

	return args.Error(0)
}

func (m *MockAgentManager) AppendPodLog(namespace, name, log string) {
	m.Called(namespace, name, log)
}

// MockLogProvider is a mock implementation of LogProvider interface.
type MockLogProvider struct {
	mock.Mock
}

func (m *MockLogProvider) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts api.ContainerLogOpts) (io.ReadCloser, error) {
	args := m.Called(ctx, namespace, podName, containerName, opts)
	reader, _ := args.Get(0).(io.ReadCloser)

	return reader, args.Error(1)
}
