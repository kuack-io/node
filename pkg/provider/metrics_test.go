package provider_test

import (
	"context"
	"testing"

	"kuack-node/pkg/provider"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsGaugeMutations(t *testing.T) { //nolint:paralleltest // global Prometheus collectors are shared process-wide
	originalAgents := testutil.ToFloat64(provider.AgentsConnected)
	originalPods := testutil.ToFloat64(provider.PodsRunning)
	originalCPU := testutil.ToFloat64(provider.CpuCapacity)
	originalMemory := testutil.ToFloat64(provider.MemoryCapacity)

	t.Cleanup(func() {
		provider.AgentsConnected.Set(originalAgents)
		provider.PodsRunning.Set(originalPods)
		provider.CpuCapacity.Set(originalCPU)
		provider.MemoryCapacity.Set(originalMemory)
	})

	provider.AgentsConnected.Inc()
	require.InDelta(t, originalAgents+1, testutil.ToFloat64(provider.AgentsConnected), 0.1)
	provider.AgentsConnected.Dec()
	require.InDelta(t, originalAgents, testutil.ToFloat64(provider.AgentsConnected), 0.1)

	provider.PodsRunning.Set(originalPods + 2)
	require.InDelta(t, originalPods+2, testutil.ToFloat64(provider.PodsRunning), 0.1)

	provider.CpuCapacity.Set(originalCPU + 3.5)
	require.InDelta(t, originalCPU+3.5, testutil.ToFloat64(provider.CpuCapacity), 0.1)

	provider.MemoryCapacity.Set(originalMemory + 1024)
	require.InDelta(t, originalMemory+1024, testutil.ToFloat64(provider.MemoryCapacity), 0.1)
}

func TestMetrics_AddAgent(t *testing.T) { //nolint:paralleltest
	// Global metrics are shared state, so we cannot run this in parallel.
	p, _, agent := setupTestProvider(t)

	// Initial count
	initial := testutil.ToFloat64(provider.AgentsConnected)

	// Add Agent
	p.AddAgent(context.Background(), agent)

	// Verify count increased
	assert.InDelta(t, initial+1, testutil.ToFloat64(provider.AgentsConnected), 0.1)

	// Remove Agent
	p.RemoveAgent(agent.UUID)

	// Verify count decreased
	assert.InDelta(t, initial, testutil.ToFloat64(provider.AgentsConnected), 0.1)
}
