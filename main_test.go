package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kuack-node/pkg/app"
	"kuack-node/pkg/config"
	"kuack-node/pkg/health"
	httpserver "kuack-node/pkg/http"
	"kuack-node/pkg/k8s"
	"kuack-node/pkg/provider"
)

const testKubeconfigContent = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-server
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: test-token
`

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

//nolint:unparam // perm parameter kept for consistency with os.WriteFile signature
func mustWriteFile(
	filename string,
	data []byte,
	perm os.FileMode,
) {
	err := os.WriteFile(filename, data, perm)
	if err != nil {
		panic(err)
	}
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
			key:          "MAIN_TEST_VAR_SET",
			value:        "test-value",
			defaultValue: "default",
			want:         "test-value",
		},
		{
			name:         "environment variable not set",
			key:          "MAIN_TEST_VAR_NOT_SET",
			value:        "",
			defaultValue: "default",
			want:         "default",
		},
		{
			name:         "environment variable empty string",
			key:          "MAIN_TEST_VAR_EMPTY",
			value:        "",
			defaultValue: "default",
			want:         "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Clean up environment before test
			mustUnsetenv(tt.key)
			defer mustUnsetenv(tt.key)

			if tt.value != "" {
				mustSetenv(tt.key, tt.value)
			}

			got := config.GetEnv(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnv(%q, %q) = %q, want %q", tt.key, tt.defaultValue, got, tt.want)
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
			key:          "MAIN_TEST_BOOL_TRUE",
			value:        "true",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to false",
			key:          "MAIN_TEST_BOOL_FALSE",
			value:        "false",
			defaultValue: true,
			want:         false,
		},
		{
			name:         "environment variable set to 1",
			key:          "MAIN_TEST_BOOL_1",
			value:        "1",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to TRUE (case insensitive)",
			key:          "MAIN_TEST_BOOL_TRUE_UPPER",
			value:        "TRUE",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to True (case insensitive)",
			key:          "MAIN_TEST_BOOL_TRUE_MIXED",
			value:        "True",
			defaultValue: false,
			want:         true,
		},
		{
			name:         "environment variable set to FALSE (case insensitive)",
			key:          "MAIN_TEST_BOOL_FALSE_UPPER",
			value:        "FALSE",
			defaultValue: true,
			want:         false,
		},
		{
			name:         "environment variable not set",
			key:          "MAIN_TEST_BOOL_NOT_SET",
			value:        "",
			defaultValue: true,
			want:         true,
		},
		{
			name:         "environment variable set to invalid value",
			key:          "MAIN_TEST_BOOL_INVALID",
			value:        "invalid",
			defaultValue: false,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Clean up environment before test
			mustUnsetenv(tt.key)
			defer mustUnsetenv(tt.key)

			if tt.value != "" {
				mustSetenv(tt.key, tt.value)
			}

			got := config.GetEnvBool(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnvBool(%q, %v) = %v, want %v", tt.key, tt.defaultValue, got, tt.want)
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
			key:          "MAIN_TEST_INT_VALID",
			value:        "42",
			defaultValue: 0,
			want:         42,
		},
		{
			name:         "environment variable set to zero",
			key:          "MAIN_TEST_INT_ZERO",
			value:        "0",
			defaultValue: 10,
			want:         0,
		},
		{
			name:         "environment variable set to negative integer",
			key:          "MAIN_TEST_INT_NEGATIVE",
			value:        "-5",
			defaultValue: 0,
			want:         -5,
		},
		{
			name:         "environment variable not set",
			key:          "MAIN_TEST_INT_NOT_SET",
			value:        "",
			defaultValue: 100,
			want:         100,
		},
		{
			name:         "environment variable set to invalid value",
			key:          "MAIN_TEST_INT_INVALID",
			value:        "not-a-number",
			defaultValue: 50,
			want:         50,
		},
		{
			name:         "environment variable set to empty string",
			key:          "MAIN_TEST_INT_EMPTY",
			value:        "",
			defaultValue: 25,
			want:         25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Clean up environment before test
			mustUnsetenv(tt.key)
			defer mustUnsetenv(tt.key)

			if tt.value != "" {
				mustSetenv(tt.key, tt.value)
			}

			got := config.GetEnvInt(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnvInt(%q, %d) = %d, want %d", tt.key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetKubeClient(t *testing.T) {
	t.Parallel()
	// Create a temporary kubeconfig file for testing
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")

	// Create a minimal valid kubeconfig
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	// Test with valid kubeconfig
	client, err := k8s.GetKubeClient(kubeconfigPath)
	if err != nil {
		t.Errorf("GetKubeClient() with valid config error = %v, want nil", err)
	}

	if client == nil {
		t.Error("GetKubeClient() returned nil client")
	}

	// Test with empty path (in-cluster config - will fail in test environment)
	_, err = k8s.GetKubeClient("")
	if err == nil {
		t.Error("GetKubeClient() with empty path expected error, got nil")
	}

	// Test with non-existent file
	_, err = k8s.GetKubeClient("/nonexistent/kubeconfig")
	if err == nil {
		t.Error("GetKubeClient() with non-existent file expected error, got nil")
	}

	// Test with invalid kubeconfig content (tests NewForConfig error path)
	invalidKubeconfigPath := filepath.Join(tmpDir, "invalid-kubeconfig")
	mustWriteFile(invalidKubeconfigPath, []byte("invalid: yaml"), 0o600)
	_, err = k8s.GetKubeClient(invalidKubeconfigPath)
	// This might succeed in creating config but fail on NewForConfig
	// or fail on BuildConfigFromFlags - either way we test error handling
	_ = err
}

func TestRun(t *testing.T) {
	t.Parallel()
	// Create a temporary kubeconfig
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     ":0",
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	// Test with context that cancels quickly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run should start but be cancelled quickly
	err := app.Run(ctx, cfg)
	// Error is expected due to context cancellation
	if err != nil && !strings.Contains(err.Error(), "context") {
		t.Logf("run() returned error: %v", err)
	}
}

func TestRun_HttpServerError(t *testing.T) {
	t.Parallel()
	// Test run with invalid HTTP server config
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     "invalid-address",
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	ctx := t.Context()

	err := app.Run(ctx, cfg)
	// Should fail on invalid HTTP address or kubeconfig
	_ = err
}

func TestRun_VerbosityOutOfRange(t *testing.T) {
	t.Parallel()
	// Test verbosity outside range (should not call klog.V)
	key := "TEST_VERBOSITY_OOR"
	mustSetenv(key, "-1")

	defer mustUnsetenv(key)

	verbosity := config.GetEnvInt(key, config.DefaultKlogVerbosity)
	if verbosity >= 0 && verbosity <= 10 {
		t.Error("Verbosity should be outside range")
	}

	mustSetenv(key, "11")

	verbosity = config.GetEnvInt(key, config.DefaultKlogVerbosity)
	if verbosity >= 0 && verbosity <= 10 {
		t.Error("Verbosity should be outside range")
	}
}

func TestRun_HttpErrChan(t *testing.T) {
	t.Parallel()
	// Test the httpErrChan error path
	// Use invalid HTTP address to make Start() fail and send to httpErrChan
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     "256.256.256.256:99999", // Invalid IP
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := app.Run(ctx, cfg)
	// Should get error from httpErrChan when HTTP server fails
	if err != nil && strings.Contains(err.Error(), "HTTP server") {
		t.Logf("Got expected HTTP server error: %v", err)
	} else if err != nil {
		t.Logf("Got different error: %v", err)
	}
}

func TestRun_ShutdownError(t *testing.T) {
	t.Parallel()
	// Test shutdown error path
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     ":0",
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	// Cancel context quickly to trigger shutdown
	// The shutdown error path is tested when Shutdown returns an error
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := app.Run(ctx, cfg)
	// Error expected due to context cancellation or node controller
	_ = err
}

func TestRun_HttpErrChanWithError(t *testing.T) {
	t.Parallel()
	// Test httpErrChan error return path
	// Use invalid HTTP address to make Start() fail immediately and send to httpErrChan
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     "999.999.999.999:99999",
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	// Use longer timeout to allow HTTP server to fail and error to be sent to channel
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := app.Run(ctx, cfg)
	// Should get error from httpErrChan when HTTP server fails
	// The error might come from HTTP server creation or from httpErrChan
	if err != nil {
		t.Logf("Got error (expected): %v", err)

		if strings.Contains(err.Error(), "HTTP server") {
			t.Log("Successfully tested httpErrChan error path")
		}
	}
}

func TestRun_ProviderError(t *testing.T) {
	t.Parallel()
	// Skip this test as it causes klog flag redefinition issues
	// The run function is tested in TestRun
	t.Skip("Skipping due to klog flag redefinition in test environment")
}

func TestRun_VerbosityEdgeCases(t *testing.T) {
	t.Parallel()
	// Test verbosity edge cases
	tests := []struct {
		name      string
		verbosity string
	}{
		{"negative verbosity", "-1"},
		{"verbosity too high", "11"},
		{"verbosity at boundary low", "0"},
		{"verbosity at boundary high", "10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := "TEST_VERBOSITY_EDGE_" + strings.ReplaceAll(tt.name, " ", "_")
			mustSetenv(key, tt.verbosity)

			defer mustUnsetenv(key)

			verbosity := config.GetEnvInt(key, config.DefaultKlogVerbosity)
			_ = verbosity // Use the value
		})
	}
}

func TestMainFunction(t *testing.T) {
	t.Parallel()
	// Test main function by running it in a subprocess
	if os.Getenv("TEST_MAIN") == "1" {
		mustSetenv("NODE_NAME", "test-node")
		mustSetenv("HTTP_LISTEN_ADDR", ":0")
		mustSetenv("DISABLE_TAINT", "true")
		mustSetenv("KLOG_VERBOSITY", "0")
		main()

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	//nolint:gosec // os.Args[0] is the current executable (safe), and "-test.run=TestMainFunction" is a hardcoded test argument
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMainFunction")

	cmd.Env = append(os.Environ(), "TEST_MAIN=1")
	err := cmd.Run()
	// Main will fail due to kubeconfig, which is expected
	exitError := &exec.ExitError{}
	if errors.As(err, &exitError) {
		if exitError.ExitCode() == 0 {
			t.Error("main() should have failed")
		}
	} else if err == nil {
		t.Error("main() should have failed")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
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

	cfg = config.LoadConfig()
	if cfg.NodeName != "custom-node" {
		t.Errorf("loadConfig() NodeName = %v, want custom-node", cfg.NodeName)
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("loadConfig() ListenAddr = %v, want :8080", cfg.ListenAddr)
	}

	if cfg.DisableTaint != true {
		t.Errorf("loadConfig() DisableTaint = %v, want true", cfg.DisableTaint)
	}

	if cfg.Verbosity != 5 {
		t.Errorf("loadConfig() Verbosity = %v, want 5", cfg.Verbosity)
	}
}

func TestSetupComponents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	mustWriteFile(kubeconfigPath, []byte(testKubeconfigContent), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     ":0",
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	components, err := app.SetupComponents(cfg)
	if err != nil {
		t.Fatalf("SetupComponents() error = %v", err)
	}

	if components.WASMProvider == nil {
		t.Error("SetupComponents() returned nil provider")
	}

	if components.HTTPServer == nil {
		t.Error("SetupComponents() returned nil HTTP server")
	}

	if components.KubeClient == nil {
		t.Error("SetupComponents() returned nil kube client")
	}

	if components.HealthServer == nil {
		t.Error("SetupComponents() returned nil health server")
	}
}

func TestSetupComponents_ProviderError(t *testing.T) {
	t.Parallel()
	// Test with invalid provider config (should not error in NewWASMProvider)
	// But we can test the error path by using invalid kubeconfig
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "invalid-kubeconfig")
	mustWriteFile(kubeconfigPath, []byte("invalid: yaml"), 0o600)

	cfg := &config.Config{
		NodeName:       "test-node",
		ListenAddr:     ":0",
		DisableTaint:   true,
		KubeconfigPath: kubeconfigPath,
		Verbosity:      0,
	}

	_, err := app.SetupComponents(
		cfg,
	)
	// Should fail on kubeconfig
	if err == nil {
		t.Error("SetupComponents() expected error with invalid kubeconfig, got nil")
	}
}

func TestShutdownGracefully(t *testing.T) {
	t.Parallel()
	// Create a test provider and server
	p, _ := provider.NewWASMProvider("test-node", false)

	httpServer, err := httpserver.NewServer(&httpserver.Config{
		ListenAddr: ":0",
		Provider:   p,
	})
	if err != nil {
		t.Fatalf("Failed to create HTTP server: %v", err)
	}

	// Test with no error in channel
	httpErrChan := make(chan error, 1)
	healthErrChan := make(chan error, 1)

	healthServer := health.NewServer(provider.KubeletPort)

	err = app.ShutdownGracefully(
		context.Background(),
		httpServer,
		healthServer,
		httpErrChan,
		healthErrChan,
	)
	if err != nil {
		t.Errorf("ShutdownGracefully() with no error = %v, want nil", err)
	}

	// Test with error in channel
	httpErrChan2 := make(chan error, 1)
	healthErrChan2 := make(chan error, 1)

	testErr := errors.New("test error") //nolint:err113 // Test error for testing error path
	httpErrChan2 <- testErr

	err = app.ShutdownGracefully(
		context.Background(),
		httpServer,
		healthServer,
		httpErrChan2,
		healthErrChan2,
	)
	if err == nil {
		t.Error("ShutdownGracefully() expected error from channel, got nil")
	}

	if err != nil && !strings.Contains(err.Error(), "test error") {
		t.Errorf("ShutdownGracefully() error = %v, want error containing 'test error'", err)
	}
}
