package provider_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHasCapacity(t *testing.T) {
	t.Parallel()

	agent := &provider.AgentConnection{
		Resources: provider.ResourceSpec{
			CPU:    resource.MustParse("1"),
			Memory: resource.MustParse("1Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
	}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "Pod fits",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod1", UID: "pod1-uid"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Pod doesn't fit (CPU)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod2", UID: "pod2-uid"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("2"),
								},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Pod doesn't fit (Memory)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod3", UID: "pod3-uid"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Multiple containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod4", UID: "pod4-uid"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("500m"),
								},
							},
						},
						{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("600m"),
								},
							},
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, agent.HasCapacity(tt.pod))
		})
	}
}

func TestWASMProvider_AddAgent_AssignsPendingPods(t *testing.T) {
	t.Parallel()

	p, mockStream, agent := setupTestProvider(t)

	// 1. Create a pending pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-pod",
			Namespace: "default",
			UID:       "pod-uid-1",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "image-1",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	// Manually add pod to provider storage to simulate pending state
	// We can use CreatePod which puts it in pending state if no agents
	err := p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	// 3. Add agent
	p.AddAgent(context.Background(), agent)

	// 4. Verify pod was assigned
	assert.Eventually(t, func() bool {
		updatedPod, err := p.GetPod(context.Background(), "default", "pending-pod")
		if err != nil {
			return false
		}

		return updatedPod.Status.Reason == "AgentAssigned" &&
			updatedPod.Status.Phase == corev1.PodPending
	}, 1*time.Second, 10*time.Millisecond)

	updatedPod, err := p.GetPod(context.Background(), "default", "pending-pod")
	require.NoError(t, err)
	assert.Contains(t, updatedPod.Status.Message, "agent-1")

	// Verify stream was written to
	mockStream.AssertExpectations(t)
}

func TestWASMProvider_RemoveAgent(t *testing.T) {
	t.Parallel()

	p, _, agent := setupTestProvider(t)

	// Capture notifications
	var (
		notifiedPod *corev1.Pod
		mu          sync.Mutex
	)

	p.NotifyPods(context.Background(), func(pod *corev1.Pod) {
		mu.Lock()
		defer mu.Unlock()

		if pod.Name == "running-pod" && pod.Status.Phase == corev1.PodFailed {
			notifiedPod = pod
		}
	})

	p.AddAgent(context.Background(), agent)

	// 2. Assign a pod to the agent
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "running-pod",
			Namespace: "default",
			UID:       "pod-uid-1",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "image-1",
				},
			},
		},
	}

	// CreatePod will assign it because agent exists
	err := p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	// Verify it's assigned
	updatedPod, err := p.GetPod(context.Background(), "default", "running-pod")
	require.NoError(t, err)
	assert.Equal(t, "AgentAssigned", updatedPod.Status.Reason)

	// 3. Remove agent
	p.RemoveAgent("agent-1")

	// 4. Verify pod is failed via notification
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return notifiedPod != nil
	}, 1*time.Second, 10*time.Millisecond)

	mu.Lock()
	require.NotNil(t, notifiedPod)
	assert.Equal(t, corev1.PodFailed, notifiedPod.Status.Phase)
	assert.Equal(t, "NodeLost", notifiedPod.Status.Reason)
	mu.Unlock()

	// 5. Verify agent is gone
	_, ok := p.GetAgent("agent-1")
	assert.False(t, ok)

	// 6. Verify pod is gone from provider
	_, err = p.GetPod(context.Background(), "default", "running-pod")
	assert.Error(t, err)
}
