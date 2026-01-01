package provider_test

import (
	"context"
	"testing"
	"time"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestProvider_CreatePod_NoAgent(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
		},
	}

	// Should succeed but remain in Pending state
	err = p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	got, err := p.GetPod(context.Background(), "default", "pod-1")
	require.NoError(t, err)
	assert.Equal(t, corev1.PodPending, got.Status.Phase)
}

func TestProvider_CreatePod_AlreadyExists(t *testing.T) {
	t.Parallel()

	p, _, agent := setupTestProvider(t)

	// Add agent
	p.AddAgent(context.Background(), agent)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
		},
	}

	// First create succeeds
	err := p.CreatePod(context.Background(), pod)
	require.NoError(t, err)

	// Second create should also succeed (idempotent/update)
	err = p.CreatePod(context.Background(), pod)
	require.NoError(t, err)
}

func TestProvider_DeletePod_NotFound(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
	}

	// Should succeed (idempotent)
	err = p.DeletePod(context.Background(), pod)
	require.NoError(t, err)
}

func TestProvider_GetPod_NotFound(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	_, err = p.GetPod(context.Background(), "default", "missing")
	require.Error(t, err)
}

func TestProvider_GetPodStatus_NotFound(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	_, err = p.GetPodStatus(context.Background(), "default", "missing")
	require.Error(t, err)
}

func TestProvider_GetPods(t *testing.T) {
	t.Parallel()

	p, _, agent := setupTestProvider(t)

	// Add agent
	p.AddAgent(context.Background(), agent)

	// Create 2 pods
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
		},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
		},
	}

	require.NoError(t, p.CreatePod(context.Background(), pod1))
	require.NoError(t, p.CreatePod(context.Background(), pod2))

	pods, err := p.GetPods(context.Background())
	require.NoError(t, err)
	assert.Len(t, pods, 2)
}

func TestProvider_GetNode(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	// Set kubelet version (required before GetNode())
	fakeClient := fake.NewClientset()
	err = p.SetKubeletVersionFromCluster(context.Background(), fakeClient)
	require.NoError(t, err)

	node := p.GetNode()
	assert.Equal(t, "node-1", node.Name)
	assert.Equal(t, "wasm", node.Status.NodeInfo.OperatingSystem)
}

func TestProvider_NotifyNodeStatus(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	// Set kubelet version (required before GetNode())
	fakeClient := fake.NewClientset()
	err = p.SetKubeletVersionFromCluster(context.Background(), fakeClient)
	require.NoError(t, err)

	ch := make(chan *corev1.Node, 1)

	p.NotifyNodeStatus(context.Background(), func(node *corev1.Node) {
		ch <- node
	})

	select {
	case node := <-ch:
		assert.Equal(t, "node-1", node.Name)
	case <-time.After(1 * time.Second):
		require.Fail(t, "timeout waiting for node status notification")
	}
}
