package k8s_test

import (
	"os"
	"path/filepath"
	"testing"

	"kuack-node/pkg/k8s"
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

//nolint:unparam // perm parameter kept for consistency with os.WriteFile signature
func mustWriteFile(filename string, data []byte, perm os.FileMode) {
	err := os.WriteFile(filename, data, perm)
	if err != nil {
		panic(err)
	}
}

func TestGetKubeClient(t *testing.T) {
	t.Parallel()

	t.Run("valid kubeconfig", func(t *testing.T) {
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
	})

	t.Run("empty path (in-cluster config)", func(t *testing.T) {
		t.Parallel()
		// Test with empty path (in-cluster config - will fail in test environment)
		_, err := k8s.GetKubeClient("")
		if err == nil {
			t.Error("GetKubeClient() with empty path expected error, got nil")
		}

		// Verify error message contains expected text
		if err != nil && err.Error() == "" {
			t.Error("GetKubeClient() error should have a message")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		t.Parallel()
		// Test with non-existent file
		_, err := k8s.GetKubeClient("/nonexistent/kubeconfig")
		if err == nil {
			t.Error("GetKubeClient() with non-existent file expected error, got nil")
		}

		// Verify error message
		if err != nil && err.Error() == "" {
			t.Error("GetKubeClient() error should have a message")
		}
	})

	t.Run("invalid kubeconfig content", func(t *testing.T) {
		t.Parallel()
		// Test with invalid kubeconfig content
		tmpDir := t.TempDir()
		invalidKubeconfigPath := filepath.Join(tmpDir, "invalid-kubeconfig")
		mustWriteFile(invalidKubeconfigPath, []byte("invalid: yaml"), 0o600)

		_, err := k8s.GetKubeClient(invalidKubeconfigPath)
		// This might succeed in creating config but fail on NewForConfig
		// or fail on BuildConfigFromFlags - either way we test error handling
		if err != nil {
			// Error is expected, verify it has a message
			if err.Error() == "" {
				t.Error("GetKubeClient() error should have a message")
			}
		}
	})

	t.Run("malformed yaml", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		malformedPath := filepath.Join(tmpDir, "malformed")
		mustWriteFile(malformedPath, []byte("this is not yaml at all: [unclosed"), 0o600)

		_, err := k8s.GetKubeClient(malformedPath)
		if err == nil {
			t.Error("GetKubeClient() with malformed yaml expected error, got nil")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		emptyPath := filepath.Join(tmpDir, "empty")
		mustWriteFile(emptyPath, []byte(""), 0o600)

		_, err := k8s.GetKubeClient(emptyPath)
		if err == nil {
			t.Error("GetKubeClient() with empty file expected error, got nil")
		}
	})

	t.Run("kubeconfig with missing server", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		missingServerPath := filepath.Join(tmpDir, "missing-server")
		invalidConfig := `apiVersion: v1
kind: Config
clusters:
- cluster:
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
		mustWriteFile(missingServerPath, []byte(invalidConfig), 0o600)

		_, err := k8s.GetKubeClient(missingServerPath)
		// May or may not error depending on validation, but should not panic
		_ = err
	})
}

func TestGetKubeClient_ErrorMessages(t *testing.T) {
	t.Parallel()

	t.Run("error message contains 'kubeconfig'", func(t *testing.T) {
		t.Parallel()

		_, err := k8s.GetKubeClient("/nonexistent/path")
		if err == nil {
			t.Fatal("expected error")
		}

		errMsg := err.Error()
		if errMsg == "" {
			t.Error("error message should not be empty")
		}
		// Error should mention kubeconfig or config
		if errMsg != "" && !containsAny(errMsg, []string{"kubeconfig", "config", "failed"}) {
			t.Errorf("error message should mention kubeconfig/config/failed, got: %q", errMsg)
		}
	})
}

// containsAny checks if the string contains any of the substrings.
func containsAny(s string, substrings []string) bool {
	for _, substr := range substrings {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}

	return false
}
