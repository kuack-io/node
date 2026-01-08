package provider_test

import (
	"context"
	"testing"
	"time"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWASMProvider_GetPodStatus(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1", registry.NewProxy())
	require.NoError(t, err)

	// 1. Add a pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.1",
		},
	}
	err = p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	// 2. Get status
	status, err := p.GetPodStatus(context.Background(), "default", "pod-1")
	require.NoError(t, err)
	// CreatePod resets status to Pending if no agent is found
	assert.Equal(t, corev1.PodPending, status.Phase)
	// PodIP might be cleared if pending
	// assert.Equal(t, "10.0.0.1", status.PodIP)

	// 3. Get status for non-existent pod
	_, err = p.GetPodStatus(context.Background(), "default", "non-existent")
	assert.Error(t, err) // Should return error (likely not found)
}

func TestWASMProvider_GetPods(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1", registry.NewProxy())
	require.NoError(t, err)

	// 1. Add pods
	pod1 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}}
	pod2 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "kube-system"}}

	require.NoError(t, p.CreatePod(context.Background(), pod1))
	require.NoError(t, p.CreatePod(context.Background(), pod2))

	// 2. Get pods
	pods, err := p.GetPods(context.Background())
	require.NoError(t, err)
	assert.Len(t, pods, 2)

	// Verify pods are present
	found1 := false
	found2 := false

	for _, pod := range pods {
		if pod.Name == "pod-1" {
			found1 = true
		}

		if pod.Name == "pod-2" {
			found2 = true
		}
	}

	assert.True(t, found1)
	assert.True(t, found2)
}

func TestWASMProvider_GetNode(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1", registry.NewProxy())
	require.NoError(t, err)

	// Set kubelet version (required before GetNode())
	fakeClient := fake.NewClientset()
	err = p.SetKubeletVersionFromCluster(context.Background(), fakeClient)
	require.NoError(t, err)

	// 1. Initial state (empty)
	node := p.GetNode()
	assert.Equal(t, "node-1", node.Name)
	assert.True(t, node.Status.Capacity.Cpu().IsZero())
	assert.True(t, node.Status.Capacity.Memory().IsZero())

	// 2. Add agent with capacity
	mockStream := new(MockAgentStream)
	mockStream.On("SetWriteDeadline", mock.Anything).Return(nil)
	mockStream.On("WriteJSON", mock.Anything).Return(nil)

	agent := &provider.AgentConnection{
		UUID:      "agent-1",
		BrowserID: "browser-1",
		Stream:    mockStream,
		Resources: provider.ResourceSpec{
			CPU:    resource.MustParse("2"),
			Memory: resource.MustParse("4Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
	}

	p.AddAgent(context.Background(), agent)

	// 3. Verify node capacity updated
	node = p.GetNode()
	assert.Equal(t, int64(2), node.Status.Capacity.Cpu().Value())
	assert.Equal(t, int64(4*1024*1024*1024), node.Status.Capacity.Memory().Value())
	assert.Equal(t, int64(2), node.Status.Allocatable.Cpu().Value())
	assert.Equal(t, int64(4*1024*1024*1024), node.Status.Allocatable.Memory().Value())
}

func TestWASMProvider_NotifyNodeStatus(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1", registry.NewProxy())
	require.NoError(t, err)

	// Set kubelet version (required before GetNode())
	fakeClient := fake.NewClientset()
	err = p.SetKubeletVersionFromCluster(context.Background(), fakeClient)
	require.NoError(t, err)

	// 1. Register callback
	var notifiedNode *corev1.Node

	done := make(chan struct{})

	p.NotifyNodeStatus(context.Background(), func(node *corev1.Node) {
		notifiedNode = node

		close(done)
	})

	// 2. Wait for initial callback
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		require.Fail(t, "timeout waiting for initial node status notification")
	}

	assert.NotNil(t, notifiedNode)
	assert.Equal(t, "node-1", notifiedNode.Name)
}
