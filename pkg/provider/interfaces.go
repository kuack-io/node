package provider

import (
	"context"
	"io"
	"time"

	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	corev1 "k8s.io/api/core/v1"
)

// AgentStream defines the interface for communicating with an agent.
type AgentStream interface {
	SetWriteDeadline(t time.Time) error
	WriteJSON(v interface{}) error
}

// LogProvider defines the interface for retrieving container logs.
type LogProvider interface {
	GetContainerLogs(ctx context.Context, namespace, pod, container string, opts api.ContainerLogOpts) (io.ReadCloser, error)
}

// AgentManager defines the interface for managing agent connections and updates.
type AgentManager interface {
	GetAgent(uuid string) (*AgentConnection, bool)
	UpdatePodStatus(namespace, name string, status AgentPodStatus) error
	AppendPodLog(namespace, name, log string)
	AddAgent(ctx context.Context, agent *AgentConnection)
	RemoveAgent(uuid string)
}

// PodNotifier defines the interface for notifying about pod updates.
type PodNotifier interface {
	NotifyPods(ctx context.Context, notifyFunc func(*corev1.Pod))
}
