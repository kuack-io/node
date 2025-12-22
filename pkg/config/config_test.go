package config_test

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kuack-node/pkg/config"
)

// Helper functions to ignore errors in tests.
func mustSetenv(key, value string) {
	err := os.Setenv(key, value)
	if err != nil {
		panic(err)
	}
}

func mustUnsetenv(key string) {
	err := os.Unsetenv(key)
	if err != nil {
		panic(err)
	}
}

var envMu sync.Mutex //nolint:gochecknoglobals // shared env guard needed across parallel tests

func lockEnv(t *testing.T) {
	t.Helper()
	envMu.Lock()
	t.Cleanup(envMu.Unlock)
}

func uniqueEnvKey(t *testing.T, base string) string {
	t.Helper()
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, t.Name())

	return base + "_" + sanitized
}

func TestGetEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		value        string
		defaultValue string
		want         string
	}{
		{
			name:         "environment variable set",
			key:          "TEST_VAR_MAIN",
			value:        "test-value",
			defaultValue: "default",
			want:         "test-value",
		},
		{
			name:         "environment variable not set",
			key:          "NONEXISTENT_VAR_MAIN",
			value:        "",
			defaultValue: "default",
			want:         "default",
		},
		{
			name:         "environment variable empty string",
			key:          "EMPTY_VAR_MAIN",
			value:        "",
			defaultValue: "default",
			want:         "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := uniqueEnvKey(t, tt.key)

			// Clean up environment before test
			mustUnsetenv(key)
			defer mustUnsetenv(key)

			if tt.value != "" {
				mustSetenv(key, tt.value)
			}

			got := config.GetEnv(key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetEnv(%q, %q) = %q, want %q", key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		value        string
		defaultValue bool
		want         bool
	}{
		{
			name:         "environment variable set to true",
			key:          "TEST_BOOL_MAIN",
			value:        "true",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to false",
			key:          "TEST_BOOL_MAIN",
			value:        "false",
			defaultValue: true,
			want:         false,
		},
		{
			name:         "environment variable set to 1",
			key:          "TEST_BOOL_MAIN",
			value:        "1",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to TRUE (case insensitive)",
			key:          "TEST_BOOL_MAIN",
			value:        "TRUE",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to True (case insensitive)",
			key:          "TEST_BOOL_MAIN",
			value:        "True",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to FALSE (case insensitive)",
			key:          "TEST_BOOL_MAIN",
			value:        "FALSE",
			defaultValue: true,
			want:         false,
		},
		{
			name:         "environment variable not set",
			key:          "NONEXISTENT_BOOL_MAIN",
			value:        "",
			defaultValue: true,
			want:         true,
		},
		{
			name:         "environment variable set to invalid value",
			key:          "TEST_BOOL_MAIN",
			value:        "invalid",
			defaultValue: false,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := uniqueEnvKey(t, tt.key)

			// Clean up environment before test
			mustUnsetenv(key)
			defer mustUnsetenv(key)

			if tt.value != "" {
				mustSetenv(key, tt.value)
			}

			got := config.GetEnvBool(key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetEnvBool(%q, %v) = %v, want %v", key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnvInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		value        string
		defaultValue int
		want         int
	}{
		{
			name:         "environment variable set to valid integer",
			key:          "TEST_INT_MAIN",
			value:        "42",
			defaultValue: 0,
			want:         42,
		},
		{
			name:         "environment variable set to zero",
			key:          "TEST_INT_MAIN",
			value:        "0",
			defaultValue: 10,
			want:         0,
		},
		{
			name:         "environment variable set to negative integer",
			key:          "TEST_INT_MAIN",
			value:        "-5",
			defaultValue: 0,
			want:         -5,
		},
		{
			name:         "environment variable not set",
			key:          "NONEXISTENT_INT_MAIN",
			value:        "",
			defaultValue: 100,
			want:         100,
		},
		{
			name:         "environment variable set to invalid value",
			key:          "TEST_INT_MAIN",
			value:        "not-a-number",
			defaultValue: 50,
			want:         50,
		},
		{
			name:         "environment variable set to empty string",
			key:          "TEST_INT_MAIN",
			value:        "",
			defaultValue: 25,
			want:         25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := uniqueEnvKey(t, tt.key)

			// Clean up environment before test
			mustUnsetenv(key)
			defer mustUnsetenv(key)

			if tt.value != "" {
				mustSetenv(key, tt.value)
			}

			got := config.GetEnvInt(key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetEnvInt(%q, %d) = %d, want %d", key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	lockEnv(t)

	// Test with default values
	mustUnsetenv("NODE_NAME")
	mustUnsetenv("HTTP_LISTEN_ADDR")
	mustUnsetenv("DISABLE_TAINT")
	mustUnsetenv("KUBECONFIG")
	mustUnsetenv("KLOG_VERBOSITY")

	defer func() {
		mustUnsetenv("NODE_NAME")
		mustUnsetenv("HTTP_LISTEN_ADDR")
		mustUnsetenv("DISABLE_TAINT")
		mustUnsetenv("KUBECONFIG")
		mustUnsetenv("KLOG_VERBOSITY")
	}()

	cfg := config.LoadConfig()
	if cfg.NodeName != config.DefaultNodeName {
		t.Errorf("LoadConfig() NodeName = %v, want %v", cfg.NodeName, config.DefaultNodeName)
	}

	if cfg.ListenAddr != config.DefaultListenAddr {
		t.Errorf("LoadConfig() ListenAddr = %v, want %v", cfg.ListenAddr, config.DefaultListenAddr)
	}

	if cfg.DisableTaint != false {
		t.Errorf("LoadConfig() DisableTaint = %v, want false", cfg.DisableTaint)
	}

	if cfg.Verbosity != config.DefaultKlogVerbosity {
		t.Errorf("LoadConfig() Verbosity = %v, want %v", cfg.Verbosity, config.DefaultKlogVerbosity)
	}

	// Test with custom values
	mustSetenv("NODE_NAME", "custom-node")
	mustSetenv("HTTP_LISTEN_ADDR", ":8080")
	mustSetenv("DISABLE_TAINT", "true")
	mustSetenv("KLOG_VERBOSITY", "5")
	mustSetenv("KUBECONFIG", "/path/to/kubeconfig")

	cfg = config.LoadConfig()
	if cfg.NodeName != "custom-node" {
		t.Errorf("LoadConfig() NodeName = %v, want custom-node", cfg.NodeName)
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("LoadConfig() ListenAddr = %v, want :8080", cfg.ListenAddr)
	}

	if cfg.DisableTaint != true {
		t.Errorf("LoadConfig() DisableTaint = %v, want true", cfg.DisableTaint)
	}

	if cfg.Verbosity != 5 {
		t.Errorf("LoadConfig() Verbosity = %v, want 5", cfg.Verbosity)
	}

	if cfg.KubeconfigPath != "/path/to/kubeconfig" {
		t.Errorf("LoadConfig() KubeconfigPath = %v, want /path/to/kubeconfig", cfg.KubeconfigPath)
	}
}

func TestInitializeKlog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		verbosity int
	}{
		{
			name:      "verbosity within valid range (0)",
			verbosity: 0,
		},
		{
			name:      "verbosity within valid range (5)",
			verbosity: 5,
		},
		{
			name:      "verbosity within valid range (10)",
			verbosity: 10,
		},
		{
			name:      "verbosity below range (-1)",
			verbosity: -1,
		},
		{
			name:      "verbosity above range (11)",
			verbosity: 11,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// InitializeKlog should not panic for any verbosity value
			config.InitializeKlog(tc.verbosity)
		})
	}

	// Test that multiple calls don't cause issues
	config.InitializeKlog(2)
	config.InitializeKlog(3)
	config.InitializeKlog(4)
}

func TestConfigConstants(t *testing.T) {
	t.Parallel()

	if config.DefaultNodeName == "" {
		t.Error("DefaultNodeName should not be empty")
	}

	if config.DefaultListenAddr == "" {
		t.Error("DefaultListenAddr should not be empty")
	}

	if config.DefaultKlogVerbosity < 0 {
		t.Error("DefaultKlogVerbosity should be non-negative")
	}

	if config.ShutdownTimeout <= 0 {
		t.Error("ShutdownTimeout should be positive")
	}

	if config.HttpErrChanTimeout <= 0 {
		t.Error("HttpErrChanTimeout should be positive")
	}
}

func TestGetEnvBool_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		value        string
		defaultValue bool
		want         bool
	}{
		{
			name:         "value is 'True' (mixed case)",
			key:          "TEST_BOOL_EDGE",
			value:        "True",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "value is 'FaLsE' (mixed case)",
			key:          "TEST_BOOL_EDGE",
			value:        "FaLsE",
			defaultValue: true,
			want:         false,
		},
		{
			name:         "value is '0'",
			key:          "TEST_BOOL_EDGE",
			value:        "0",
			defaultValue: true,
			want:         false, // strconv.ParseBool("0") returns false
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := uniqueEnvKey(t, tt.key)

			mustUnsetenv(key)
			defer mustUnsetenv(key)

			mustSetenv(key, tt.value)

			got := config.GetEnvBool(key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetEnvBool(%q, %v) = %v, want %v", key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnvInt_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          string
		value        string
		defaultValue int
		want         int
	}{
		{
			name:         "value is large positive integer",
			key:          "TEST_INT_EDGE",
			value:        "2147483647",
			defaultValue: 0,
			want:         2147483647,
		},
		{
			name:         "value is large negative integer",
			key:          "TEST_INT_EDGE",
			value:        "-2147483648",
			defaultValue: 0,
			want:         -2147483648,
		},
		{
			name:         "value has leading/trailing whitespace",
			key:          "TEST_INT_EDGE",
			value:        "  42  ",
			defaultValue: 0,
			want:         0, // Atoi doesn't trim, so it fails and returns default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := uniqueEnvKey(t, tt.key)

			mustUnsetenv(key)
			defer mustUnsetenv(key)

			mustSetenv(key, tt.value)

			got := config.GetEnvInt(key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetEnvInt(%q, %d) = %d, want %d", key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnv_EmptyString(t *testing.T) {
	t.Parallel()

	key := uniqueEnvKey(t, "TEST_EMPTY")

	mustUnsetenv(key)
	defer mustUnsetenv(key)

	// Set to empty string explicitly
	mustSetenv(key, "")

	got := config.GetEnv(key, "default")
	if got != "default" {
		t.Errorf("GetEnv(%q, %q) = %q, want %q", key, "default", got, "default")
	}
}

func TestLoadConfig_AllFields(t *testing.T) {
	t.Parallel()
	lockEnv(t)

	// Test that all fields are properly loaded
	mustSetenv("NODE_NAME", "test-node")
	mustSetenv("HTTP_LISTEN_ADDR", ":8080")
	mustSetenv("DISABLE_TAINT", "false")
	mustSetenv("KUBECONFIG", "kubeconfig.yaml")
	mustSetenv("KLOG_VERBOSITY", "3")

	defer func() {
		mustUnsetenv("NODE_NAME")
		mustUnsetenv("HTTP_LISTEN_ADDR")
		mustUnsetenv("DISABLE_TAINT")
		mustUnsetenv("KUBECONFIG")
		mustUnsetenv("KLOG_VERBOSITY")
	}()

	cfg := config.LoadConfig()

	if cfg.NodeName != "test-node" {
		t.Errorf("NodeName = %q, want %q", cfg.NodeName, "test-node")
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}

	if cfg.DisableTaint != false {
		t.Errorf("DisableTaint = %v, want %v", cfg.DisableTaint, false)
	}

	if cfg.KubeconfigPath != "kubeconfig.yaml" {
		t.Errorf("KubeconfigPath = %q, want %q", cfg.KubeconfigPath, "kubeconfig.yaml")
	}

	if cfg.Verbosity != 3 {
		t.Errorf("Verbosity = %d, want %d", cfg.Verbosity, 3)
	}
}

func TestGetEnvBool_WithStrconvParseBool(t *testing.T) {
	t.Parallel()

	// Test that strconv.ParseBool works correctly
	testCases := []string{
		"true",
		"false",
		"TRUE",
		"FALSE",
		"True",
		"False",
		"t",
		"f",
		"T",
		"F",
		"1",
		"0",
	}

	for _, tc := range testCases {
		t.Run("value_"+tc, func(t *testing.T) {
			t.Parallel()

			key := uniqueEnvKey(t, "TEST_BOOL_"+tc)

			mustUnsetenv(key)
			defer mustUnsetenv(key)

			mustSetenv(key, tc)

			// strconv.ParseBool should handle these cases
			parsed, err := strconv.ParseBool(tc)
			if err == nil {
				got := config.GetEnvBool(key, !parsed)
				if got != parsed {
					t.Errorf(
						"GetEnvBool(%q, %v) = %v, want %v (from strconv.ParseBool)",
						key,
						!parsed,
						got,
						parsed,
					)
				}
			}
		})
	}
}
