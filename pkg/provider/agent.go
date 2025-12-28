package provider

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// AddAgent registers a new browser agent.
func (p *WASMProvider) AddAgent(ctx context.Context, agent *AgentConnection) {
	p.agentRegistry.Register(agent)
	p.resources.AddAgent(agent.UUID, agent.Resources)

	// Metrics
	agentsConnected.Inc()
	cpuCapacity.Add(agent.Resources.CPU.AsApproximateFloat64())
	memoryCapacity.Add(agent.Resources.Memory.AsApproximateFloat64())

	klog.Infof("Agent %s registered with capacity: CPU=%s, Memory=%s", agent.UUID, agent.Resources.CPU.String(), agent.Resources.Memory.String())

	// Try to assign pending pods to this new agent
	go p.assignPendingPodsToAgent(ctx, agent)

	// Notify that node status has changed (capacity updated)
	p.mu.RLock()
	notify := p.nodeStatusNotify
	p.mu.RUnlock()

	if notify != nil {
		go func() {
			node := p.GetNode()
			notify(node)
		}()
	}
}

// assignPendingPodsToAgent attempts to assign pending pods to a newly connected agent.
func (p *WASMProvider) assignPendingPodsToAgent(ctx context.Context, agent *AgentConnection) {
	var pendingPods []*corev1.Pod

	// Collect all pending pods that are not yet assigned to an agent
	p.pods.Range(func(key, value any) bool {
		if ctx != nil && ctx.Err() != nil {
			return false
		}

		pod, ok := value.(*corev1.Pod)
		if !ok {
			return true
		}

		podKey, ok := key.(string)
		if !ok {
			return true
		}

		// Check if pod is in Pending state and not assigned to any agent
		if pod.Status.Phase == corev1.PodPending {
			// Check if this pod is already assigned to an agent
			isAssigned := false

			p.agentRegistry.Range(func(_ string, ag *AgentConnection) bool {
				ag.mu.RLock()
				_, exists := ag.AllocatedPods[podKey]
				ag.mu.RUnlock()

				if exists {
					isAssigned = true

					return false // Stop iteration
				}

				return true
			})

			if !isAssigned {
				pendingPods = append(pendingPods, pod.DeepCopy())
			}
		}

		return true
	})

	if len(pendingPods) == 0 {
		return
	}

	klog.Infof("Attempting to assign %d pending pod(s) to newly connected agent %s", len(pendingPods), agent.UUID)

	// Try to assign each pending pod
	for _, pod := range pendingPods {
		if ctx != nil && ctx.Err() != nil {
			return
		}

		// Check if agent still has capacity
		agent.mu.RLock()
		hasCapacity := agent.HasCapacity(pod)
		agent.mu.RUnlock()

		if !hasCapacity {
			klog.Infof("Agent %s no longer has capacity to accept more pods", agent.UUID)

			break
		}

		// Assign pod to agent
		agent.mu.Lock()
		agent.AllocatedPods[getPodKey(pod)] = pod
		agent.mu.Unlock()

		// Send pod specification to agent via WebSocket
		err := p.sendPodToAgent(agent, pod)
		if err != nil {
			// Remove from agent allocation on send failure
			agent.mu.Lock()
			delete(agent.AllocatedPods, getPodKey(pod))
			agent.mu.Unlock()

			klog.Errorf("Failed to send pending pod %s/%s to agent %s: %v", pod.Namespace, pod.Name, agent.UUID, err)

			continue
		}

		// Update pod status to indicate it's been assigned
		pod.Status.Phase = corev1.PodPending
		pod.Status.Reason = "AgentAssigned"
		pod.Status.Message = "Pod assigned to agent " + agent.UUID

		// Update stored pod
		p.pods.Store(getPodKey(pod), pod)

		klog.Infof("Successfully assigned pending pod %s/%s to agent %s", pod.Namespace, pod.Name, agent.UUID)

		// Notify pod status update
		p.mu.RLock()
		notifyFunc := p.notifyFunc
		p.mu.RUnlock()

		if notifyFunc != nil {
			notifyFunc(pod)
		}
	}
}

// RemoveAgent unregisters a browser agent and fails its pods.
func (p *WASMProvider) RemoveAgent(uuid string) {
	agent, ok := p.agentRegistry.UnregisterAndGet(uuid)
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

	// Metrics
	agentsConnected.Dec()
	cpuCapacity.Sub(agent.Resources.CPU.AsApproximateFloat64())
	memoryCapacity.Sub(agent.Resources.Memory.AsApproximateFloat64())

	// Notify that node status has changed (capacity updated)
	p.mu.RLock()
	notify := p.nodeStatusNotify
	p.mu.RUnlock()

	if notify != nil {
		go func() {
			node := p.GetNode()
			notify(node)
		}()
	}
}

// GetAgent retrieves an agent by UUID.
func (p *WASMProvider) GetAgent(uuid string) (*AgentConnection, bool) {
	return p.agentRegistry.Get(uuid)
}

// GetAgentCount returns the number of connected agents.
func (p *WASMProvider) GetAgentCount() int {
	count := 0

	p.agentRegistry.Range(func(_ string, _ *AgentConnection) bool {
		count++

		return true
	})

	return count
}
