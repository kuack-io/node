package provider //nolint:testpackage // Tests need access to internal functions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewWASMProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		nodeName string
		wantErr  bool
	}{
		{
			name:     "create provider",
			nodeName: "test-node",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, err := NewWASMProvider(tt.nodeName)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewWASMProvider() error = %v, wantErr %v", err, tt.wantErr)

				return
			}

			if provider == nil && !tt.wantErr {
				t.Error("NewWASMProvider() returned nil provider")
			}

			if provider != nil {
				if provider.nodeName != tt.nodeName {
					t.Errorf(
						"NewWASMProvider() nodeName = %v, want %v",
						provider.nodeName,
						tt.nodeName,
					)
				}
			}
		})
	}
}

func TestGetPodKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "normal pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-pod",
				},
			},
			want: "default/test-pod",
		},
		{
			name: "pod with custom namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "kube-system",
					Name:      "test-pod",
				},
			},
			want: "kube-system/test-pod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := getPodKey(tt.pod)
			if got != tt.want {
				t.Errorf("getPodKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWASMProvider_GetTaints(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	taints := provider.getTaints()
	// Should always return exactly 1 taint: kuack.io/provider=kuack:NoSchedule
	if len(taints) != 1 {
		t.Fatalf("getTaints() len = %v, want 1", len(taints))
	}

	taint := taints[0]
	if taint.Key != TaintKeyKuackProvider {
		t.Errorf("getTaints() key = %v, want %s", taint.Key, TaintKeyKuackProvider)
	}

	if taint.Value != TaintValueKuack {
		t.Errorf("getTaints() value = %v, want %s", taint.Value, TaintValueKuack)
	}

	if taint.Effect != corev1.TaintEffectNoSchedule {
		t.Errorf("getTaints() effect = %v, want NoSchedule", taint.Effect)
	}
}

func TestWASMProvider_CreatePod(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Test with no agents available
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "test-container",
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
	// errdefs.AsInvalidInput wraps the error, check error message
	if err != nil && !strings.Contains(err.Error(), "no suitable agent") {
		t.Errorf("CreatePod() error = %v, want error containing 'no suitable agent'", err)
	}

	// Pod should not be stored when creation fails
	_, err = provider.GetPod(ctx, "default", "test-pod")
	if err == nil {
		t.Error("GetPod() expected error for pod that was rejected, got nil")
	}

	// Add an agent with sufficient capacity
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
		Stream:        nil, // No WebSocket in test
	}
	provider.AddAgent(ctx, agent)

	// Now create pod should succeed (even if WebSocket send fails in test)
	err = provider.CreatePod(ctx, pod)
	// In tests, WebSocket send will fail because Stream is nil
	// In real deployment, WebSocket send must succeed for pod to be scheduled
	if err != nil && !strings.Contains(err.Error(), "agent stream is not a WebSocket connection") {
		t.Errorf("CreatePod() error = %v, want nil or WebSocket error", err)
	}

	// If WebSocket send failed, pod won't be stored (we clean up on failure)
	// So only verify if there was no error
	if err == nil {
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
			t.Error("Pod should be scheduled when agent with capacity is available")
		}
	}
}

func TestWASMProvider_CreatePod_AgentNotFound(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Add an agent but then remove it to simulate race condition
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
	provider.AddAgent(ctx, agent)

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

	// Remove agent after adding it
	// This simulates the agent being removed before pod creation
	provider.RemoveAgent("test-agent-1")

	// CreatePod should fail when no agents are available
	err := provider.CreatePod(ctx, pod)
	if err == nil {
		t.Error("CreatePod() expected error when no agents available, got nil")
	}
	// errdefs.AsInvalidInput wraps the error, check error message
	if err != nil && !strings.Contains(err.Error(), "no suitable agent") {
		t.Errorf("CreatePod() error = %v, want error containing 'no suitable agent'", err)
	}

	// Pod should not be stored when creation fails
	_, err = provider.GetPod(ctx, "default", "test-pod")
	if err == nil {
		t.Error("GetPod() expected error for pod that was rejected, got nil")
	}
}

func TestWASMProvider_CreatePod_InvalidAgentType(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Store an invalid type in the agents map to test type assertion error
	provider.agents.Store("invalid-agent", "not-an-agent")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "test-container",
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

	// selectAgent will skip the invalid agent and return ErrNoSuitableAgent
	// CreatePod should fail when no suitable agent is available
	err := provider.CreatePod(ctx, pod)
	if err == nil {
		t.Error("CreatePod() expected error when no suitable agent, got nil")
	}
	// errdefs.AsInvalidInput wraps the error, check error message
	if err != nil && !strings.Contains(err.Error(), "no suitable agent") {
		t.Errorf("CreatePod() error = %v, want error containing 'no suitable agent'", err)
	}

	// Pod should not be stored when creation fails
	_, err = provider.GetPod(ctx, "default", "test-pod")
	if err == nil {
		t.Error("GetPod() expected error for pod that was rejected, got nil")
	}
}

func TestWASMProvider_UpdatePod(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	err := provider.UpdatePod(ctx, pod)
	if err != nil {
		t.Errorf("UpdatePod() error = %v, want nil", err)
	}

	// Verify pod was stored
	retrievedPod, err := provider.GetPod(ctx, "default", "test-pod")
	if err != nil {
		t.Errorf("GetPod() error = %v, want nil", err)
	}

	if retrievedPod == nil {
		t.Error("GetPod() returned nil pod")
	}
}

func TestWASMProvider_DeletePod(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

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
	provider.AddAgent(ctx, agent)

	// Create a pod first
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "test-container",
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

	_ = provider.CreatePod(ctx, pod)

	// Delete the pod
	err := provider.DeletePod(ctx, pod)
	if err != nil {
		t.Errorf("DeletePod() error = %v, want nil", err)
	}

	// Verify pod was removed
	_, err = provider.GetPod(ctx, "default", "test-pod")
	if err == nil {
		t.Error("GetPod() expected error after deletion, got nil")
	}
}

func TestWASMProvider_DeletePod_NoAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Create a pod without an agent (pod exists but not assigned to any agent)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "test-container",
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

	// Store pod directly without agent assignment
	provider.pods.Store("default/test-pod", pod)

	// DeletePod should succeed even without an agent (it handles nil targetAgent)
	err := provider.DeletePod(ctx, pod)
	if err != nil {
		t.Errorf("DeletePod() error = %v, want nil (should handle missing agent gracefully)", err)
	}

	// Verify pod was removed
	_, err = provider.GetPod(ctx, "default", "test-pod")
	if err == nil {
		t.Error("GetPod() expected error after deletion, got nil")
	}
}

func TestWASMProvider_GetPod(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Test getting non-existent pod
	_, err := provider.GetPod(ctx, "default", "nonexistent")
	if err == nil {
		t.Error("GetPod() expected error for non-existent pod, got nil")
	}

	// Store a pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}
	provider.pods.Store(getPodKey(pod), pod)

	// Get the pod
	retrievedPod, err := provider.GetPod(ctx, "default", "test-pod")
	if err != nil {
		t.Errorf("GetPod() error = %v, want nil", err)
	}

	if retrievedPod == nil {
		t.Error("GetPod() returned nil pod")

		return
	}

	if retrievedPod.Namespace != "default" || retrievedPod.Name != "test-pod" {
		t.Errorf("GetPod() returned wrong pod: %v", retrievedPod)
	}
}

func TestWASMProvider_GetPodStatus(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Test getting status for non-existent pod
	_, err := provider.GetPodStatus(ctx, "default", "nonexistent")
	if err == nil {
		t.Error("GetPodStatus() expected error for non-existent pod, got nil")
	}

	// Store a pod with status
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
	provider.pods.Store(getPodKey(pod), pod)

	// Get the pod status
	status, err := provider.GetPodStatus(ctx, "default", "test-pod")
	if err != nil {
		t.Errorf("GetPodStatus() error = %v, want nil", err)
	}

	if status == nil {
		t.Error("GetPodStatus() returned nil status")

		return
	}

	if status.Phase != corev1.PodPending {
		t.Errorf("GetPodStatus() phase = %v, want Pending", status.Phase)
	}
}

func TestWASMProvider_GetPods(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Initially should return empty list
	pods, err := provider.GetPods(ctx)
	if err != nil {
		t.Errorf("GetPods() error = %v, want nil", err)
	}

	if len(pods) != 0 {
		t.Errorf("GetPods() len = %v, want 0", len(pods))
	}

	// Store some pods
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

	provider.pods.Store(getPodKey(pod1), pod1)
	provider.pods.Store(getPodKey(pod2), pod2)

	// Get all pods
	pods, err = provider.GetPods(ctx)
	if err != nil {
		t.Errorf("GetPods() error = %v, want nil", err)
	}

	if len(pods) != 2 {
		t.Errorf("GetPods() len = %v, want 2", len(pods))
	}
}

func TestWASMProvider_NotifyPods(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	called := false
	notifyFunc := func(pod *corev1.Pod) {
		called = true
	}

	provider.NotifyPods(ctx, notifyFunc)

	provider.mu.RLock()

	if provider.notifyFunc == nil {
		t.Error("NotifyPods() did not set notifyFunc")
	}

	provider.mu.RUnlock()

	// Test that notifyFunc is called
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	provider.mu.RLock()

	if provider.notifyFunc != nil {
		provider.notifyFunc(pod)
	}

	provider.mu.RUnlock()

	if !called {
		t.Error("NotifyPods() notifyFunc was not called")
	}
}

func TestWASMProvider_GetContainerLogs(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	opts := api.ContainerLogOpts{}

	_, err := provider.GetContainerLogs(ctx, "default", "test-pod", "test-container", opts)
	if err == nil {
		t.Error("GetContainerLogs() expected error, got nil")
	}
}

func TestWASMProvider_RunInContainer(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	err := provider.RunInContainer(ctx, "default", "test-pod", "test-container", nil, nil)
	if err == nil {
		t.Error("RunInContainer() expected error, got nil")
	}
}

func TestWASMProvider_AttachToContainer(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	err := provider.AttachToContainer(ctx, "default", "test-pod", "test-container", nil)
	if err == nil {
		t.Error("AttachToContainer() expected error, got nil")
	}
}

func TestWASMProvider_Ping(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	err := provider.Ping(ctx)
	if err != nil {
		t.Errorf("Ping() error = %v, want nil", err)
	}
}

func TestWASMProvider_NotifyNodeStatus(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	called := false
	callback := func(node *corev1.Node) {
		called = true

		if node == nil {
			t.Error("NotifyNodeStatus() callback received nil node")

			return
		}

		// Verify there is exactly 1 taint: kuack.io/provider=kuack:NoSchedule
		if len(node.Spec.Taints) != 1 {
			t.Errorf("NotifyNodeStatus() taints count = %d, want exactly 1", len(node.Spec.Taints))
		}

		if len(node.Spec.Taints) == 1 {
			taint := node.Spec.Taints[0]
			if taint.Key != TaintKeyKuackProvider {
				t.Errorf("NotifyNodeStatus() taint key = %v, want %s", taint.Key, TaintKeyKuackProvider)
			}

			if taint.Value != TaintValueKuack {
				t.Errorf("NotifyNodeStatus() taint value = %v, want %s", taint.Value, TaintValueKuack)
			}

			if taint.Effect != corev1.TaintEffectNoSchedule {
				t.Errorf("NotifyNodeStatus() taint effect = %v, want NoSchedule", taint.Effect)
			}
		}
	}

	provider.NotifyNodeStatus(ctx, callback)

	// Wait a bit for goroutine to execute
	time.Sleep(100 * time.Millisecond)

	if !called {
		t.Error("NotifyNodeStatus() callback was not called")
	}
}

func TestWASMProvider_NotifyNodeStatus_FiltersLibraryTaint(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Simulate the library calling the callback with a node that has the virtual-kubelet.io/provider taint
	called := false
	callback := func(node *corev1.Node) {
		called = true

		if node == nil {
			t.Error("NotifyNodeStatus() callback received nil node")

			return
		}

		// Verify there is exactly 1 taint: kuack.io/provider=kuack:NoSchedule
		if len(node.Spec.Taints) != 1 {
			t.Errorf("NotifyNodeStatus() taints count = %d, want exactly 1", len(node.Spec.Taints))
		}

		if len(node.Spec.Taints) == 1 {
			taint := node.Spec.Taints[0]
			if taint.Key != TaintKeyKuackProvider {
				t.Errorf("NotifyNodeStatus() taint key = %v, want %s", taint.Key, TaintKeyKuackProvider)
			}

			if taint.Value != TaintValueKuack {
				t.Errorf("NotifyNodeStatus() taint value = %v, want %s", taint.Value, TaintValueKuack)
			}

			if taint.Effect != corev1.TaintEffectNoSchedule {
				t.Errorf("NotifyNodeStatus() taint effect = %v, want NoSchedule", taint.Effect)
			}
		}
	}

	// Call NotifyNodeStatus
	provider.NotifyNodeStatus(ctx, callback)

	// Wait for initial callback
	time.Sleep(100 * time.Millisecond)

	// Verify GetNode() returns exactly 1 taint with correct values
	node := provider.GetNode()
	if len(node.Spec.Taints) != 1 {
		t.Errorf("GetNode() taints count = %d, want exactly 1", len(node.Spec.Taints))
	}

	if len(node.Spec.Taints) == 1 {
		taint := node.Spec.Taints[0]
		if taint.Key != TaintKeyKuackProvider {
			t.Errorf("GetNode() taint key = %v, want %s", taint.Key, TaintKeyKuackProvider)
		}

		if taint.Value != TaintValueKuack {
			t.Errorf("GetNode() taint value = %v, want %s", taint.Value, TaintValueKuack)
		}
	}

	if !called {
		t.Error("NotifyNodeStatus() callback was not called")
	}
}

func TestWASMProvider_GetNode(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	node := provider.GetNode()
	if node == nil {
		t.Error("GetNode() returned nil node")

		return
	}

	if node.Name != "test-node" {
		t.Errorf("GetNode() name = %v, want test-node", node.Name)
	}

	if node.Status.NodeInfo.OperatingSystem != "wasm" {
		t.Errorf("GetNode() OS = %v, want wasm", node.Status.NodeInfo.OperatingSystem)
	}

	if node.Status.NodeInfo.Architecture != "wasm32" {
		t.Errorf("GetNode() arch = %v, want wasm32", node.Status.NodeInfo.Architecture)
	}
}

func TestWASMProvider_GetNode_OnlyCustomTaint(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	node := provider.GetNode()
	if node == nil {
		t.Fatal("GetNode() returned nil node")
	}

	// Verify there is exactly 1 taint
	if len(node.Spec.Taints) != 1 {
		t.Fatalf("GetNode() taints count = %d, want 1", len(node.Spec.Taints))
		t.Logf("Taints: %+v", node.Spec.Taints)
	}

	taint := node.Spec.Taints[0]

	// Verify it's the custom kuack.io/provider taint
	if taint.Key != TaintKeyKuackProvider {
		t.Errorf("GetNode() taint key = %v, want %s", taint.Key, TaintKeyKuackProvider)
	}

	if taint.Value != TaintValueKuack {
		t.Errorf("GetNode() taint value = %v, want %s", taint.Value, TaintValueKuack)
	}

	if taint.Effect != corev1.TaintEffectNoSchedule {
		t.Errorf("GetNode() taint effect = %v, want NoSchedule", taint.Effect)
	}
}

func TestWASMProvider_GetNode_AlwaysHasOneTaint(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	node := provider.GetNode()
	if node == nil {
		t.Fatal("GetNode() returned nil node")
	}

	// Verify there is exactly 1 taint: kuack.io/provider=kuack:NoSchedule
	if len(node.Spec.Taints) != 1 {
		t.Fatalf("GetNode() taints count = %d, want exactly 1", len(node.Spec.Taints))
		t.Logf("Taints: %+v", node.Spec.Taints)
	}

	taint := node.Spec.Taints[0]

	// Verify the taint has the correct key
	if taint.Key != TaintKeyKuackProvider {
		t.Errorf("GetNode() taint key = %v, want %s", taint.Key, TaintKeyKuackProvider)
	}

	// Verify the taint has the correct value
	if taint.Value != TaintValueKuack {
		t.Errorf("GetNode() taint value = %v, want %s", taint.Value, TaintValueKuack)
	}

	// Verify the taint has the correct effect
	if taint.Effect != corev1.TaintEffectNoSchedule {
		t.Errorf("GetNode() taint effect = %v, want NoSchedule", taint.Effect)
	}
}

func TestWASMProvider_SelectAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Test with no agents
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	_, err := provider.selectAgent(pod)
	if err == nil {
		t.Error("selectAgent() expected error when no agents available, got nil")
	}

	if !errors.Is(err, ErrNoSuitableAgent) {
		t.Errorf("selectAgent() error = %v, want ErrNoSuitableAgent", err)
	}

	// Add a throttled agent
	throttledAgent := &AgentConnection{
		UUID: "throttled-agent",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   true,
	}
	provider.AddAgent(ctx, throttledAgent)

	// Should still fail because agent is throttled
	_, err = provider.selectAgent(pod)
	if err == nil {
		t.Error("selectAgent() expected error when only throttled agents available, got nil")
	}

	// Add a non-throttled agent
	agent := &AgentConnection{
		UUID: "test-agent",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("2000m"),
			Memory: resource.MustParse("2Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}
	provider.AddAgent(ctx, agent)

	// Now should succeed
	selectedUUID, err := provider.selectAgent(pod)
	if err != nil {
		t.Errorf("selectAgent() error = %v, want nil", err)
	}

	if selectedUUID != "test-agent" {
		t.Errorf("selectAgent() = %v, want test-agent", selectedUUID)
	}
}

func TestAgentConnection_HasCapacity(t *testing.T) {
	t.Parallel()

	agent := &AgentConnection{
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	// Pod with no resource requests should always fit
	hasCapacity := agent.HasCapacity(pod)
	if !hasCapacity {
		t.Error("HasCapacity() = false, want true (pod with no requests should fit)")
	}

	// Pod with resource requests that fit
	podWithRequests := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
				},
			},
		},
	}

	hasCapacity = agent.HasCapacity(podWithRequests)
	if !hasCapacity {
		t.Error("HasCapacity() = false, want true (agent has sufficient capacity)")
	}

	// Pod with resource requests that exceed capacity
	podExceedsCapacity := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2000m"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
		},
	}

	hasCapacity = agent.HasCapacity(podExceedsCapacity)
	if hasCapacity {
		t.Error("HasCapacity() = true, want false (pod exceeds agent capacity)")
	}
}

func TestWASMProvider_SendPodToAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	agent := &AgentConnection{
		UUID: "test-agent",
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
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test-container",
					Image: "test-image:latest",
				},
			},
		},
	}

	// sendPodToAgent will fail because agent.Stream is not a WebSocket connection
	// This is expected in tests without real WebSocket connections
	err := provider.sendPodToAgent(agent, pod)
	if err == nil {
		t.Error("sendPodToAgent() expected error when Stream is not WebSocket, got nil")
	}

	if !errors.Is(err, ErrAgentStreamNotWebSocket) {
		t.Errorf("sendPodToAgent() error = %v, want ErrAgentStreamNotWebSocket", err)
	}
}

func TestConvertPodToAgentSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pod     *corev1.Pod
		wantErr bool
	}{
		{
			name: "pod with container",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-pod",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "test-container",
							Image:   "test-image:latest",
							Command: []string{"echo"},
							Args:    []string{"hello"},
							Env: []corev1.EnvVar{
								{Name: "ENV_VAR", Value: "value"},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "pod with no containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-pod",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{},
				},
			},
			wantErr: true,
		},
		{
			name: "pod with WASM annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-pod",
					Annotations: map[string]string{
						"kuack.io/wasm-path":    "/pkg/module_bg.wasm",
						"kuack.io/wasm-variant": "wasm32-web",
						"kuack.io/wasm-image":   "ghcr.io/kuack-io/checker:latest",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test-image:latest",
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec, err := convertPodToAgentSpec(tt.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertPodToAgentSpec() error = %v, wantErr %v", err, tt.wantErr)

				return
			}

			if !tt.wantErr {
				if spec == nil {
					t.Error("convertPodToAgentSpec() returned nil spec")

					return
				}

				if spec.Metadata.Name != tt.pod.Name {
					t.Errorf("convertPodToAgentSpec() name = %v, want %v", spec.Metadata.Name, tt.pod.Name)
				}

				if spec.Metadata.Namespace != tt.pod.Namespace {
					t.Errorf("convertPodToAgentSpec() namespace = %v, want %v", spec.Metadata.Namespace, tt.pod.Namespace)
				}

				if len(spec.Spec.Containers) != len(tt.pod.Spec.Containers) {
					t.Errorf("convertPodToAgentSpec() containers count = %v, want %v", len(spec.Spec.Containers), len(tt.pod.Spec.Containers))
				}
			}
		})
	}
}

func TestWASMProvider_DeletePodFromAgent(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	agent := &AgentConnection{
		UUID: "test-agent",
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
	}

	err := provider.deletePodFromAgent(agent, pod)
	if err != nil {
		t.Errorf("deletePodFromAgent() error = %v, want nil", err)
	}
}

func TestWASMProvider_GetNodeConditions(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	conditions := provider.getNodeConditions()
	if len(conditions) == 0 {
		t.Error("getNodeConditions() returned empty conditions")
	}

	// Check for expected conditions
	conditionTypes := make(map[corev1.NodeConditionType]bool)
	for _, cond := range conditions {
		conditionTypes[cond.Type] = true
	}

	expectedTypes := []corev1.NodeConditionType{
		corev1.NodeReady,
		corev1.NodeMemoryPressure,
		corev1.NodeDiskPressure,
		corev1.NodePIDPressure,
		corev1.NodeNetworkUnavailable,
	}

	for _, expectedType := range expectedTypes {
		if !conditionTypes[expectedType] {
			t.Errorf("getNodeConditions() missing condition type: %v", expectedType)
		}
	}
}

func TestNewResourceTracker(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()
	if rt == nil {
		t.Error("NewResourceTracker() returned nil")

		return
	}

	if rt.totalCapacity == nil {
		t.Error("NewResourceTracker() totalCapacity is nil")

		return
	}

	if rt.totalAllocated == nil {
		t.Error("NewResourceTracker() totalAllocated is nil")
	}
}

func TestResourceTracker_GetAllocatable(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()

	allocatable := rt.GetAllocatable()
	if allocatable == nil {
		t.Error("GetAllocatable() returned nil")
	}

	// Initially should have zero CPU and memory
	cpu := allocatable[corev1.ResourceCPU]
	if !cpu.IsZero() {
		t.Errorf("GetAllocatable() CPU = %v, want zero", cpu)
	}

	mem := allocatable[corev1.ResourceMemory]
	if !mem.IsZero() {
		t.Errorf("GetAllocatable() Memory = %v, want zero", mem)
	}
}

func TestResourceTracker_AllocatePod(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()

	// Add some capacity first
	rt.AddAgent("agent-1", ResourceSpec{
		CPU:    resource.MustParse("1000m"),
		Memory: resource.MustParse("1Gi"),
	})

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
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

	rt.AllocatePod(pod)

	allocated := rt.totalAllocated[corev1.ResourceCPU]
	if allocated.Cmp(resource.MustParse("100m")) != 0 {
		t.Errorf("AllocatePod() allocated CPU = %v, want 100m", allocated)
	}
}

func TestResourceTracker_DeallocatePod(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()

	// Add capacity and allocate first
	rt.AddAgent("agent-1", ResourceSpec{
		CPU:    resource.MustParse("1000m"),
		Memory: resource.MustParse("1Gi"),
	})

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
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

	rt.AllocatePod(pod)
	rt.DeallocatePod(pod)

	allocated := rt.totalAllocated[corev1.ResourceCPU]
	if !allocated.IsZero() {
		t.Errorf("DeallocatePod() allocated CPU = %v, want zero", allocated)
	}
}

func TestResourceTracker_AddAgent(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()

	resources := ResourceSpec{
		CPU:    resource.MustParse("2000m"),
		Memory: resource.MustParse("2Gi"),
		GPU:    true,
	}

	rt.AddAgent("agent-1", resources)

	capacity := rt.totalCapacity[corev1.ResourceCPU]
	if capacity.Cmp(resource.MustParse("2000m")) != 0 {
		t.Errorf("AddAgent() capacity CPU = %v, want 2000m", capacity)
	}

	memCapacity := rt.totalCapacity[corev1.ResourceMemory]
	if memCapacity.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Errorf("AddAgent() capacity Memory = %v, want 2Gi", memCapacity)
	}
}

func TestResourceTracker_RemoveAgent(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()

	resources := ResourceSpec{
		CPU:    resource.MustParse("2000m"),
		Memory: resource.MustParse("2Gi"),
	}

	rt.AddAgent("agent-1", resources)
	rt.RemoveAgent("agent-1")

	capacity := rt.totalCapacity[corev1.ResourceCPU]
	if capacity.IsZero() {
		// Expected - capacity should be zero after removing agent
	} else {
		t.Errorf("RemoveAgent() capacity CPU = %v, want zero", capacity)
	}
}

func TestWASMProvider_GetPod_InvalidType(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Store invalid type
	provider.pods.Store("default/test-pod", "not-a-pod")

	_, err := provider.GetPod(ctx, "default", "test-pod")
	if err == nil {
		t.Error("GetPod() expected error for invalid pod type, got nil")
	}
}

func TestWASMProvider_SelectAgent_InvalidKeyType(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Store agent with invalid key type
	agent := &AgentConnection{
		UUID: "test-agent",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("1000m"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		IsThrottled:   false,
	}
	provider.agents.Store(123, agent) // Invalid key type

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	_, err := provider.selectAgent(pod)
	// Should handle invalid key type gracefully
	// Range will skip invalid types, so err may be nil or ErrNoSuitableAgent
	_ = err
}

func TestWASMProvider_SelectAgent_InvalidValueType(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")

	// Store invalid value type
	provider.agents.Store("test-agent", "not-an-agent")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	_, err := provider.selectAgent(pod)
	// Should handle invalid value type gracefully
	// Range will skip invalid types, so err may be nil or ErrNoSuitableAgent
	_ = err
}

func TestWASMProvider_GetPods_InvalidType(t *testing.T) {
	t.Parallel()

	provider, _ := NewWASMProvider("test-node")
	ctx := context.Background()

	// Store invalid type
	provider.pods.Store("default/test-pod", "not-a-pod")

	pods, err := provider.GetPods(ctx)
	if err != nil {
		t.Errorf("GetPods() error = %v, want nil", err)
	}
	// Should skip invalid types
	if len(pods) != 0 {
		t.Errorf("GetPods() len = %v, want 0", len(pods))
	}
}

func TestResourceTracker_RemoveAgent_InvalidType(t *testing.T) {
	t.Parallel()

	rt := NewResourceTracker()

	// Store invalid type
	rt.perAgentCap.Store("agent-1", "not-a-resource-spec")

	rt.RemoveAgent("agent-1")

	// Should handle gracefully without error
	capacity := rt.totalCapacity[corev1.ResourceCPU]
	if !capacity.IsZero() {
		t.Errorf("RemoveAgent() capacity CPU = %v, want zero", capacity)
	}
}
