package provider //nolint:testpackage // Tests need access to internal functions

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWASMProvider_AddAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node", false)

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

	provider.AddAgent(agent)

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

	provider, _ := NewWASMProvider("test-node", false)

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

	provider.AddAgent(agent)

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

	provider, _ := NewWASMProvider("test-node", false)

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

	provider, _ := NewWASMProvider("test-node", false)

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

	provider, _ := NewWASMProvider("test-node", false)

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
	provider.AddAgent(agent)

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

	provider, _ := NewWASMProvider("test-node", false)

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

	provider.AddAgent(agent1)
	provider.AddAgent(agent2)

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

	provider, _ := NewWASMProvider("test-node", false)

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

	provider.AddAgent(agent)

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
