package provider

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// AddAgent registers a new browser agent.
func (p *WASMProvider) AddAgent(ctx context.Context, agent *AgentConnection) {
	p.agents.Store(agent.UUID, agent)
	p.resources.AddAgent(agent.UUID, agent.Resources)
	klog.Infof("Agent %s registered with capacity: CPU=%s, Memory=%s", agent.UUID, agent.Resources.CPU.String(), agent.Resources.Memory.String())
}

// RemoveAgent unregisters a browser agent and fails its pods.
func (p *WASMProvider) RemoveAgent(uuid string) {
	agentVal, ok := p.agents.LoadAndDelete(uuid)
	if !ok {
		return
	}

	agent, ok := agentVal.(*AgentConnection)
	if !ok {
		return
	}

	// Fail all pods allocated to this agent
	agent.mu.RLock()

	for podKey, pod := range agent.AllocatedPods {
		pod = pod.DeepCopy()
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Reason = "NodeLost"
		pod.Status.Message = "Browser agent disconnected"

		// Notify Kubernetes to reschedule
		if p.notifyFunc != nil {
			p.notifyFunc(pod)
		}

		// Remove from provider storage
		p.pods.Delete(podKey)
	}

	agent.mu.RUnlock()

	// Update resource tracking
	p.resources.RemoveAgent(uuid)
}

// GetAgent retrieves an agent by UUID.
func (p *WASMProvider) GetAgent(uuid string) (any, bool) {
	return p.agents.Load(uuid)
}

// GetAgentCount returns the number of connected agents.
func (p *WASMProvider) GetAgentCount() int {
	count := 0

	p.agents.Range(func(_, _ any) bool {
		count++

		return true
	})

	return count
}
