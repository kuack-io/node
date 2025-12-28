package provider_test

import (
	"testing"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/assert"
)

func TestAgentRegistry(t *testing.T) {
	t.Parallel()

	registry := provider.NewAgentRegistry()

	agent1 := &provider.AgentConnection{
		UUID:      "uuid-1",
		BrowserID: "browser-1",
	}
	agent2 := &provider.AgentConnection{
		UUID:      "uuid-2",
		BrowserID: "browser-2",
	}

	// Test Register
	registry.Register(agent1)
	registry.Register(agent2)

	// Test Get
	got1, ok := registry.Get("uuid-1")
	assert.True(t, ok)
	assert.Equal(t, agent1, got1)

	got2, ok := registry.Get("uuid-2")
	assert.True(t, ok)
	assert.Equal(t, agent2, got2)

	_, ok = registry.Get("non-existent")
	assert.False(t, ok)

	// Test List
	list := registry.List()
	assert.Len(t, list, 2)
	assert.Contains(t, list, agent1)
	assert.Contains(t, list, agent2)

	// Test Range
	count := 0

	registry.Range(func(uuid string, agent *provider.AgentConnection) bool {
		count++

		return true
	})
	assert.Equal(t, 2, count)

	// Test Unregister
	registry.Unregister("uuid-1")
	_, ok = registry.Get("uuid-1")
	assert.False(t, ok)

	// Test UnregisterAndGet
	got2, ok = registry.UnregisterAndGet("uuid-2")
	assert.True(t, ok)
	assert.Equal(t, agent2, got2)

	_, ok = registry.Get("uuid-2")
	assert.False(t, ok)

	_, ok = registry.UnregisterAndGet("non-existent")
	assert.False(t, ok)
}
