//nolint:testpackage // internal test file to access unexported metrics directly
package provider

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMetricsGaugeMutations(t *testing.T) { //nolint:paralleltest // global Prometheus collectors are shared process-wide
	originalAgents := testutil.ToFloat64(agentsConnected)
	originalPods := testutil.ToFloat64(podsRunning)
	originalCPU := testutil.ToFloat64(cpuCapacity)
	originalMemory := testutil.ToFloat64(memoryCapacity)

	t.Cleanup(func() {
		agentsConnected.Set(originalAgents)
		podsRunning.Set(originalPods)
		cpuCapacity.Set(originalCPU)
		memoryCapacity.Set(originalMemory)
	})

	agentsConnected.Inc()
	require.InDelta(t, originalAgents+1, testutil.ToFloat64(agentsConnected), 0.1)
	agentsConnected.Dec()
	require.InDelta(t, originalAgents, testutil.ToFloat64(agentsConnected), 0.1)

	podsRunning.Set(originalPods + 2)
	require.InDelta(t, originalPods+2, testutil.ToFloat64(podsRunning), 0.1)

	cpuCapacity.Set(originalCPU + 3.5)
	require.InDelta(t, originalCPU+3.5, testutil.ToFloat64(cpuCapacity), 0.1)

	memoryCapacity.Set(originalMemory + 1024)
	require.InDelta(t, originalMemory+1024, testutil.ToFloat64(memoryCapacity), 0.1)
}

func TestMetrics_AddAgent(t *testing.T) { //nolint:paralleltest
	// Global metrics are shared state, so we cannot run this in parallel.
	p, err := NewWASMProvider("node-1")
	require.NoError(t, err)

	// Set kubelet version using a fake Kubernetes client
	fakeClient := fake.NewClientset()
	err = p.SetKubeletVersionFromCluster(context.Background(), fakeClient)
	require.NoError(t, err)

	agent := &AgentConnection{
		UUID:      "agent-1",
		BrowserID: "browser-1",
		Resources: ResourceSpec{
			CPU:    resource.MustParse("4"),
			Memory: resource.MustParse("8Gi"),
		},
		AllocatedPods: make(map[string]*corev1.Pod),
	}

	// Initial count
	initial := testutil.ToFloat64(agentsConnected)

	// Add Agent
	p.AddAgent(context.Background(), agent)

	// Verify count increased
	assert.InDelta(t, initial+1, testutil.ToFloat64(agentsConnected), 0.1)

	// Remove Agent
	p.RemoveAgent(agent.UUID)

	// Verify count decreased
	assert.InDelta(t, initial, testutil.ToFloat64(agentsConnected), 0.1)
}
