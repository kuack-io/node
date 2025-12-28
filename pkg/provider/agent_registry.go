package provider

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// AgentRegistry manages connected agents.
type AgentRegistry struct {
	agents sync.Map // UUID -> *AgentConnection
}

// NewAgentRegistry creates a new AgentRegistry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{}
}

// Register registers a new agent.
func (r *AgentRegistry) Register(agent *AgentConnection) {
	r.agents.Store(agent.UUID, agent)
}

// Unregister removes an agent.
func (r *AgentRegistry) Unregister(uuid string) {
	r.agents.Delete(uuid)
}

// UnregisterAndGet removes an agent and returns it.
func (r *AgentRegistry) UnregisterAndGet(uuid string) (*AgentConnection, bool) {
	val, ok := r.agents.LoadAndDelete(uuid)
	if !ok {
		return nil, false
	}

	agent, ok := val.(*AgentConnection)
	if !ok {
		return nil, false
	}

	return agent, true
}

// Get retrieves an agent by UUID.
func (r *AgentRegistry) Get(uuid string) (*AgentConnection, bool) {
	val, ok := r.agents.Load(uuid)
	if !ok {
		return nil, false
	}

	agent, ok := val.(*AgentConnection)
	if !ok {
		return nil, false
	}

	return agent, true
}

// List returns a list of all connected agents.
func (r *AgentRegistry) List() []*AgentConnection {
	var agents []*AgentConnection
	r.agents.Range(func(key, value any) bool {
		agent, ok := value.(*AgentConnection)
		if !ok {
			return true
		}

		agents = append(agents, agent)

		return true
	})

	return agents
}

// Range iterates over all agents.
func (r *AgentRegistry) Range(f func(uuid string, agent *AgentConnection) bool) {
	r.agents.Range(func(key, value any) bool {
		uuid, ok := key.(string)
		if !ok {
			return true
		}

		agent, ok := value.(*AgentConnection)
		if !ok {
			return true
		}

		return f(uuid, agent)
	})
}

// AgentConnection represents a connected browser agent.
type AgentConnection struct {
	UUID          string
	BrowserID     string      // Persistent browser identifier (from localStorage)
	Stream        AgentStream // WebTransport stream
	Resources     ResourceSpec
	AllocatedPods map[string]*corev1.Pod
	LastHeartbeat time.Time
	IsThrottled   bool
	Labels        map[string]string
	mu            sync.RWMutex
}

// ResourceSpec describes agent capabilities.
type ResourceSpec struct {
	CPU    resource.Quantity
	Memory resource.Quantity
	GPU    bool
}
