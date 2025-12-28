package provider_test

import (
	"context"
	"testing"

	"kuack-node/pkg/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestProvider_Lifecycle(t *testing.T) {
	t.Parallel()

	// Create provider
	p, mockStream, agent := setupTestProvider(t)
	assert.NotNil(t, p)

	// Set notify func
	p.NotifyPods(context.Background(), func(pod *corev1.Pod) {})

	// Verify Node
	node := p.GetNode()
	assert.Equal(t, "node-1", node.Name)
	assert.Equal(t, "kuack", node.Spec.Taints[0].Value)

	// Add Agent
	p.AddAgent(context.Background(), agent)
	assert.Equal(t, 1, p.GetAgentCount())

	// Create Pod
	pod := testutil.NewPod("test-pod", "default", testutil.WithContainerResources("1", "1Gi"))

	err := p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	// Verify mock was called
	mockStream.AssertExpectations(t)

	// Verify pod status
	gotPod, err := p.GetPod(context.Background(), "default", "test-pod")
	require.NoError(t, err)
	assert.Equal(t, corev1.PodPending, gotPod.Status.Phase)
	assert.Equal(t, "AgentAssigned", gotPod.Status.Reason)

	// Test DeletePod
	mockStream.On("WriteJSON", mock.Anything).Return(nil) // Expect delete message

	err = p.DeletePod(context.Background(), pod)
	require.NoError(t, err)

	// Test GetPods
	pods, err := p.GetPods(context.Background())
	require.NoError(t, err)
	// Pod should be gone after DeletePod?
	// DeletePod removes it from provider?
	// Let's check DeletePod implementation.
	// It calls deletePodFromAgent and p.pods.Delete(key).
	assert.Empty(t, pods)
}

func TestProvider_UpdateAndStatus(t *testing.T) {
	t.Parallel()

	p, _, agent := setupTestProvider(t)
	p.NotifyPods(context.Background(), func(pod *corev1.Pod) {})

	pod := testutil.NewPod("test-pod", "default", testutil.WithContainerResources("1", "1Gi"))

	// Manually add pod to provider (bypass CreatePod to avoid agent selection logic for this test)
	// But p.pods is private.
	// So we must use CreatePod.
	// But CreatePod requires an agent.
	// So we need to register an agent.

	p.AddAgent(context.Background(), agent)

	err := p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	// Test GetPodStatus
	status, err := p.GetPodStatus(context.Background(), "default", "test-pod")
	require.NoError(t, err)
	assert.Equal(t, corev1.PodPending, status.Phase)

	// Test UpdatePod
	pod.Status.Phase = corev1.PodRunning
	err = p.UpdatePod(context.Background(), pod)
	require.NoError(t, err)

	// Verify update
	gotPod, err := p.GetPod(context.Background(), "default", "test-pod")
	require.NoError(t, err)
	assert.Equal(t, corev1.PodRunning, gotPod.Status.Phase)
}
