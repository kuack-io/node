package provider_test

import (
	"testing"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/testutil"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResourceTracker(t *testing.T) {
	t.Parallel()

	rt := provider.NewResourceTracker()

	// Initial state
	allocatable := rt.GetAllocatable()
	assert.Equal(t, int64(0), allocatable.Cpu().Value())
	assert.Equal(t, int64(0), allocatable.Memory().Value())

	// Add Agent
	agentRes := provider.ResourceSpec{
		CPU:    resource.MustParse("4"),
		Memory: resource.MustParse("8Gi"),
	}
	rt.AddAgent("agent-1", agentRes)

	allocatable = rt.GetAllocatable()
	assert.Equal(t, int64(4), allocatable.Cpu().Value())
	assert.Equal(t, int64(8*1024*1024*1024), allocatable.Memory().Value())

	// Allocate Pod
	pod := testutil.NewPod("pod-1", metav1.NamespaceDefault, testutil.WithContainerResources("1", "1Gi"))
	rt.AllocatePod(pod)

	allocatable = rt.GetAllocatable()
	assert.Equal(t, int64(3), allocatable.Cpu().Value())
	assert.Equal(t, int64(7*1024*1024*1024), allocatable.Memory().Value())

	// Deallocate Pod
	rt.DeallocatePod(pod)

	allocatable = rt.GetAllocatable()
	assert.Equal(t, int64(4), allocatable.Cpu().Value())
	assert.Equal(t, int64(8*1024*1024*1024), allocatable.Memory().Value())

	// Remove Agent
	rt.RemoveAgent("agent-1")

	allocatable = rt.GetAllocatable()
	assert.Equal(t, int64(0), allocatable.Cpu().Value())
	assert.Equal(t, int64(0), allocatable.Memory().Value())
}

func TestResourceTracker_OverAllocation(t *testing.T) {
	t.Parallel()

	rt := provider.NewResourceTracker()
	rt.AddAgent("agent-1", provider.ResourceSpec{
		CPU:    resource.MustParse("1"),
		Memory: resource.MustParse("1Gi"),
	})

	pod := testutil.NewPod("over-max", metav1.NamespaceDefault, testutil.WithContainerResources("2", "2Gi"))
	rt.AllocatePod(pod)

	allocatable := rt.GetAllocatable()
	// Should be 0, not negative
	assert.Equal(t, int64(0), allocatable.Cpu().Value())
	assert.Equal(t, int64(0), allocatable.Memory().Value())
}
