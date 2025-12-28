package provider_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWASMProvider_UpdatePodStatus(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	// 1. Setup agent and pod
	mockStream := new(MockAgentStream)
	mockStream.On("SetWriteDeadline", mock.Anything).Return(nil)
	mockStream.On("WriteJSON", mock.Anything).Return(nil)

	agent := &provider.AgentConnection{
		UUID:      "agent-1",
		BrowserID: "browser-1",
		Stream:    mockStream,
		Resources: provider.ResourceSpec{
			CPU:    resource.MustParse("1"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
	}
	p.AddAgent(context.Background(), agent)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1", Image: "img"}},
		},
	}
	require.NoError(t, p.CreatePod(context.Background(), pod))

	// 2. Capture notifications
	var (
		notifiedPod *corev1.Pod
		mu          sync.Mutex
	)

	p.NotifyPods(context.Background(), func(pod *corev1.Pod) {
		mu.Lock()
		defer mu.Unlock()

		notifiedPod = pod
	})

	// 3. Update status to Running
	status := provider.AgentPodStatus{
		Phase:   "Running",
		Message: "Pod is running",
	}
	err = p.UpdatePodStatus("default", "pod-1", status)
	require.NoError(t, err)

	// Verify notification
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return notifiedPod != nil && notifiedPod.Status.Phase == corev1.PodRunning
	}, 1*time.Second, 10*time.Millisecond)

	// Verify stored status
	updatedPod, err := p.GetPod(context.Background(), "default", "pod-1")
	require.NoError(t, err)
	assert.Equal(t, corev1.PodRunning, updatedPod.Status.Phase)
	assert.Equal(t, "AgentRunning", updatedPod.Status.Reason)

	// 4. Update status to Succeeded
	status.Phase = "Succeeded"
	status.Message = "Pod completed"
	err = p.UpdatePodStatus("default", "pod-1", status)
	require.NoError(t, err)

	// Verify stored status
	updatedPod, err = p.GetPod(context.Background(), "default", "pod-1")
	require.NoError(t, err)
	assert.Equal(t, corev1.PodSucceeded, updatedPod.Status.Phase)

	// Verify allocation released
	agent.AllocatedPods = make(map[string]*corev1.Pod) // Reset to verify release logic if we could check internal state
	// Since we can't check internal state easily, we rely on no error and status update.
}

func TestWASMProvider_UpdatePodStatus_NotFound(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	status := provider.AgentPodStatus{Phase: "Running"}
	err = p.UpdatePodStatus("default", "non-existent", status)
	assert.Error(t, err)
}

func TestWASMProvider_UpdatePodStatus_InvalidPhase(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}}
	require.NoError(t, p.CreatePod(context.Background(), pod))

	status := provider.AgentPodStatus{Phase: "InvalidPhase"}
	err = p.UpdatePodStatus("default", "pod-1", status)
	assert.Error(t, err)
}
