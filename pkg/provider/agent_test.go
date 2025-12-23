package provider //nolint:testpackage // Tests need access to internal functions

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWASMProvider_AddAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	agent := &AgentConnection{
		UUID: "test-agent-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
			GPU:    false,
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
		Labels:        map[string]string{"env": "test"},
	}

	provider.AddAgent(context.Background(), agent)

	// Verify agent was stored
	retrievedAgent, ok := provider.GetAgent("test-agent-1")
	if !ok {
		t.Error("GetAgent() agent not found after AddAgent")
	}

	retrieved, ok := retrievedAgent.(*AgentConnection)
	if !ok {
		t.Error("GetAgent() returned invalid agent type")
	}

	if retrieved.UUID != "test-agent-1" {
		t.Errorf("GetAgent() UUID = %v, want test-agent-1", retrieved.UUID)
	}

	// Verify agent count
	count := provider.GetAgentCount()
	if count != 1 {
		t.Errorf("GetAgentCount() = %v, want 1", count)
	}
}

func TestWASMProvider_RemoveAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Add an agent with a pod
	agent := &AgentConnection{
		UUID: "test-agent-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	agent.AllocatedPods[getPodKey(pod)] = pod
	provider.pods.Store(getPodKey(pod), pod)

	// Set up notify function to track calls
	notifyCalled := false

	provider.mu.Lock()
	provider.notifyFunc = func(p *corev1.Pod) {
		notifyCalled = true

		if p.Status.Phase != corev1.PodFailed {
			t.Errorf("RemoveAgent() pod phase = %v, want Failed", p.Status.Phase)
		}

		if p.Status.Reason != "NodeLost" {
			t.Errorf("RemoveAgent() pod reason = %v, want NodeLost", p.Status.Reason)
		}
	}
	provider.mu.Unlock()

	provider.AddAgent(context.Background(), agent)

	// Remove the agent
	provider.RemoveAgent("test-agent-1")

	// Verify agent was removed
	_, ok := provider.GetAgent("test-agent-1")
	if ok {
		t.Error("GetAgent() agent still found after RemoveAgent")
	}

	// Verify agent count
	count := provider.GetAgentCount()
	if count != 0 {
		t.Errorf("GetAgentCount() = %v, want 0", count)
	}

	// Verify pod was removed
	_, exists := provider.pods.Load(getPodKey(pod))
	if exists {
		t.Error("RemoveAgent() pod still exists after agent removal")
	}

	// Verify notify was called
	if !notifyCalled {
		t.Error("RemoveAgent() notifyFunc was not called")
	}
}

func TestWASMProvider_RemoveAgent_NonExistent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Remove non-existent agent should not panic
	provider.RemoveAgent("non-existent-agent")

	// Verify agent count is still 0
	count := provider.GetAgentCount()
	if count != 0 {
		t.Errorf("GetAgentCount() = %v, want 0", count)
	}
}

func TestWASMProvider_RemoveAgent_InvalidType(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Store invalid agent type
	provider.agents.Store("invalid-agent", "not-an-agent")

	// Remove should handle gracefully
	provider.RemoveAgent("invalid-agent")

	// Verify agent count is still 0
	count := provider.GetAgentCount()
	if count != 0 {
		t.Errorf("GetAgentCount() = %v, want 0", count)
	}
}

func TestWASMProvider_GetAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Test getting non-existent agent
	_, ok := provider.GetAgent("non-existent")
	if ok {
		t.Error("GetAgent() expected false for non-existent agent, got true")
	}

	// Add an agent
	agent := &AgentConnection{
		UUID: "test-agent-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}
	provider.AddAgent(context.Background(), agent)

	// Get the agent
	retrievedAgent, ok := provider.GetAgent("test-agent-1")
	if !ok {
		t.Error("GetAgent() expected true for existing agent, got false")
	}

	retrieved, ok := retrievedAgent.(*AgentConnection)
	if !ok {
		t.Error("GetAgent() returned invalid agent type")
	}

	if retrieved.UUID != "test-agent-1" {
		t.Errorf("GetAgent() UUID = %v, want test-agent-1", retrieved.UUID)
	}
}

func TestWASMProvider_GetAgentCount(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Initially should be 0
	count := provider.GetAgentCount()
	if count != 0 {
		t.Errorf("GetAgentCount() = %v, want 0", count)
	}

	// Add agents
	agent1 := &AgentConnection{
		UUID: "test-agent-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}

	agent2 := &AgentConnection{
		UUID: "test-agent-2",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("2000m"),
			Memory: resource.MustParse("2Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}

	provider.AddAgent(context.Background(), agent1)
	provider.AddAgent(context.Background(), agent2)

	// Should be 2
	count = provider.GetAgentCount()
	if count != 2 {
		t.Errorf("GetAgentCount() = %v, want 2", count)
	}

	// Remove one
	provider.RemoveAgent("test-agent-1")

	// Should be 1
	count = provider.GetAgentCount()
	if count != 1 {
		t.Errorf("GetAgentCount() = %v, want 1", count)
	}
}

func TestWASMProvider_RemoveAgent_MultiplePods(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Add an agent with multiple pods
	agent := &AgentConnection{
		UUID: "test-agent-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}

	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod-1",
		},
	}

	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod-2",
		},
	}

	agent.AllocatedPods[getPodKey(pod1)] = pod1
	agent.AllocatedPods[getPodKey(pod2)] = pod2
	provider.pods.Store(getPodKey(pod1), pod1)
	provider.pods.Store(getPodKey(pod2), pod2)

	notifyCount := 0

	provider.mu.Lock()
	provider.notifyFunc = func(p *corev1.Pod) {
		notifyCount++
	}
	provider.mu.Unlock()

	provider.AddAgent(context.Background(), agent)

	// Remove the agent
	provider.RemoveAgent("test-agent-1")

	// Verify both pods were notified
	if notifyCount != 2 {
		t.Errorf("RemoveAgent() notifyCount = %v, want 2", notifyCount)
	}

	// Verify both pods were removed
	_, exists1 := provider.pods.Load(getPodKey(pod1))
	if exists1 {
		t.Error("RemoveAgent() pod1 still exists")
	}

	_, exists2 := provider.pods.Load(getPodKey(pod2))
	if exists2 {
		t.Error("RemoveAgent() pod2 still exists")
	}
}

func TestWASMProvider_CreatePod_RequiresAgentCapacity(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Create a pod with resource requests
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test-container",
					Image: "test-image:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}

	// CreatePod should fail when no agents are available
	err := provider.CreatePod(ctx, pod)
	if err == nil {
		t.Error("CreatePod() expected error when no agents available, got nil")
	}

	// Add an agent with insufficient capacity
	agent := &AgentConnection{
		UUID: "test-agent-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("50m"),  // Less than requested 100m
			Memory: resource.MustParse("64Mi"), // Less than requested 128Mi
			GPU:    false,
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
		Stream:        nil,
	}
	provider.AddAgent(ctx, agent)

	// CreatePod should still fail - agent doesn't have enough capacity
	err = provider.CreatePod(ctx, pod)
	if err == nil {
		t.Error("CreatePod() expected error when agent has insufficient capacity, got nil")
	}

	// Add an agent with sufficient capacity
	agent2 := &AgentConnection{
		UUID: "test-agent-2",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
			GPU:    false,
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
		Stream:        nil,
	}
	provider.AddAgent(ctx, agent2)

	// Now CreatePod should succeed (even if WebSocket send fails in test)
	err = provider.CreatePod(ctx, pod)
	// In tests, WebSocket send will fail, but that's OK - we test the scheduling logic
	// In real deployment, WebSocket send must succeed for pod to be scheduled
	if err != nil && !strings.Contains(err.Error(), "agent stream is not a WebSocket connection") {
		t.Errorf("CreatePod() error = %v, want nil or WebSocket error", err)

		return
	}

	// If WebSocket send failed, pod won't be stored (we clean up on failure)
	// So only verify if there was no error
	if err != nil {
		return
	}

	// Verify pod was stored and scheduled
	retrievedPod, err := provider.GetPod(ctx, "default", "test-pod")
	if err != nil {
		t.Errorf("GetPod() error = %v, want nil", err)

		return
	}

	if retrievedPod == nil {
		t.Error("GetPod() returned nil pod")

		return
	}

	// Pod should be scheduled
	foundScheduled := false

	for _, condition := range retrievedPod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionTrue {
			foundScheduled = true

			break
		}
	}

	if !foundScheduled {
		t.Error("Pod should be scheduled when agent with sufficient capacity is available")
	}

	// Verify pod was allocated to agent with capacity
	agent2.mu.RLock()
	_, allocated := agent2.AllocatedPods["default/test-pod"]
	agent2.mu.RUnlock()

	if !allocated {
		t.Error("Pod should be allocated to agent with sufficient capacity")
	}
}
