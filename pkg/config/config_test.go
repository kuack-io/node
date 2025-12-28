package config_test

import (
	"testing"

	"kuack-node/pkg/config"

	"github.com/stretchr/testify/assert"
)

//nolint:dupl // Keep GetEnv-style helpers isolated so each scalar type exercises unique default/empty semantics with readable failures.
func TestGetEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		defaultValue string
		mockEnv      map[string]string
		expected     string
	}{
		{
			name:         "returns value when set",
			key:          "TEST_KEY",
			defaultValue: "default",
			mockEnv:      map[string]string{"TEST_KEY": "value"},
			expected:     "value",
		},
		{
			name:         "returns default when not set",
			key:          "TEST_KEY",
			defaultValue: "default",
			mockEnv:      map[string]string{},
			expected:     "default",
		},
		{
			name:         "returns default when empty",
			key:          "TEST_KEY",
			defaultValue: "default",
			mockEnv:      map[string]string{"TEST_KEY": ""},
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getter := func(key string) string {
				return tt.mockEnv[key]
			}
			assert.Equal(t, tt.expected, config.GetEnv(getter, tt.key, tt.defaultValue))
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		defaultValue bool
		mockEnv      map[string]string
		expected     bool
	}{
		{
			name:         "returns true when set to true",
			key:          "TEST_BOOL",
			defaultValue: false,
			mockEnv:      map[string]string{"TEST_BOOL": "true"},
			expected:     true,
		},
		{
			name:         "returns true when set to 1",
			key:          "TEST_BOOL",
			defaultValue: false,
			mockEnv:      map[string]string{"TEST_BOOL": "1"},
			expected:     true,
		},
		{
			name:         "returns false when set to false",
			key:          "TEST_BOOL",
			defaultValue: true,
			mockEnv:      map[string]string{"TEST_BOOL": "false"},
			expected:     false,
		},
		{
			name:         "returns default when not set",
			key:          "TEST_BOOL",
			defaultValue: true,
			mockEnv:      map[string]string{},
			expected:     true,
		},
		{
			name:         "returns true when set to TRUE",
			key:          "TEST_BOOL",
			defaultValue: false,
			mockEnv:      map[string]string{"TEST_BOOL": "TRUE"},
			expected:     true,
		},
		{
			name:         "returns false when set to 0",
			key:          "TEST_BOOL",
			defaultValue: true,
			mockEnv:      map[string]string{"TEST_BOOL": "0"},
			expected:     false,
		},
		{
			name:         "returns false when invalid",
			key:          "TEST_BOOL",
			defaultValue: true,
			mockEnv:      map[string]string{"TEST_BOOL": "invalid"},
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getter := func(key string) string {
				return tt.mockEnv[key]
			}
			assert.Equal(t, tt.expected, config.GetEnvBool(getter, tt.key, tt.defaultValue))
		})
	}
}

//nolint:dupl // Mirroring the string test layout lets the int parser cover identical edge-cases without indirection that would obscure intent.
func TestGetEnvInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		defaultValue int
		mockEnv      map[string]string
		expected     int
	}{
		{
			name:         "returns value when set",
			key:          "TEST_INT",
			defaultValue: 10,
			mockEnv:      map[string]string{"TEST_INT": "42"},
			expected:     42,
		},
		{
			name:         "returns default when not set",
			key:          "TEST_INT",
			defaultValue: 10,
			mockEnv:      map[string]string{},
			expected:     10,
		},
		{
			name:         "returns default when invalid",
			key:          "TEST_INT",
			defaultValue: 10,
			mockEnv:      map[string]string{"TEST_INT": "invalid"},
			expected:     10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getter := func(key string) string {
				return tt.mockEnv[key]
			}
			assert.Equal(t, tt.expected, config.GetEnvInt(getter, tt.key, tt.defaultValue))
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	mockEnv := map[string]string{
		"NODE_NAME":      "test-node",
		"PUBLIC_PORT":    "9090",
		"INTERNAL_PORT":  "10255",
		"TLS_CERT_FILE":  "/tmp/cert",
		"TLS_KEY_FILE":   "/tmp/key",
		"AGENT_TOKEN":    "token",
		"KUBECONFIG":     "/tmp/kubeconfig",
		"KLOG_VERBOSITY": "4",
	}

	getter := func(key string) string {
		return mockEnv[key]
	}

	cfg := config.LoadConfig(getter)

	assert.Equal(t, "test-node", cfg.NodeName)
	assert.Equal(t, 9090, cfg.PublicPort)
	assert.Equal(t, 10255, cfg.InternalPort)
	assert.Equal(t, "/tmp/cert", cfg.TLSCertFile)
	assert.Equal(t, "/tmp/key", cfg.TLSKeyFile)
	assert.Equal(t, "token", cfg.AgentToken)
	assert.Equal(t, "/tmp/kubeconfig", cfg.KubeconfigPath)
	assert.Equal(t, 4, cfg.Verbosity)
}

func TestLoadConfig_Defaults(t *testing.T) {
	t.Parallel()

	getter := func(key string) string {
		return ""
	}

	cfg := config.LoadConfig(getter)

	assert.Equal(t, config.DefaultNodeName, cfg.NodeName)
	assert.Equal(t, config.DefaultPublicPort, cfg.PublicPort)
	assert.Equal(t, config.DefaultInternalPort, cfg.InternalPort)
	assert.Empty(t, cfg.TLSCertFile)
	assert.Empty(t, cfg.TLSKeyFile)
	assert.Empty(t, cfg.AgentToken)
	assert.Empty(t, cfg.KubeconfigPath)
	assert.Equal(t, config.DefaultKlogVerbosity, cfg.Verbosity)
}

func TestInitializeKlog(t *testing.T) {
	t.Parallel()
	// This test modifies global state, so it cannot run in parallel with other tests
	// that might depend on klog (though none of the current tests do).
	// However, InitializeKlog uses a mutex and a flag to ensure it runs only once.
	// So calling it multiple times is safe.
	assert.NotPanics(t, func() {
		config.InitializeKlog(2)
	})

	// Call it again to ensure idempotency
	assert.NotPanics(t, func() {
		config.InitializeKlog(4)
	})
}
