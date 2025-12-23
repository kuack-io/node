package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	// ShutdownTimeout is the maximum time to wait for graceful shutdown.
	ShutdownTimeout = 30 * time.Second
	// HttpErrChanTimeout is the timeout for checking HTTP error channel during shutdown.
	HttpErrChanTimeout = 50 * time.Millisecond

	// DefaultNodeName is the default node name.
	DefaultNodeName = "wasm-node"
	// DefaultListenAddr is the default HTTP listen address.
	DefaultListenAddr = ":8080"
	// DefaultKlogVerbosity is the default klog verbosity level.
	DefaultKlogVerbosity = 2
)

var (
	//nolint:gochecknoglobals // klog initialization must happen only once across all tests
	klogInitialized bool
	klogInitMutex   sync.Mutex //nolint:gochecknoglobals // mutex for klog initialization
)

// Config holds application configuration.
type Config struct {
	NodeName       string
	ListenAddr     string
	KubeconfigPath string
	Verbosity      int
}

// GetEnv returns the value of an environment variable or a default value if not set.
func GetEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}

// GetEnvBool returns the boolean value of an environment variable or a default value if not set.
func GetEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			return parsed
		}
		// Also accept "true"/"false" strings (case-insensitive)
		return strings.EqualFold(value, "true") || value == "1"
	}

	return defaultValue
}

// GetEnvInt returns the integer value of an environment variable or a default value if not set.
func GetEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}

	return defaultValue
}

// LoadConfig reads configuration from environment variables.
func LoadConfig() *Config {
	listenAddr := GetEnv("HTTP_LISTEN_ADDR", DefaultListenAddr)

	return &Config{
		NodeName:       GetEnv("NODE_NAME", DefaultNodeName),
		ListenAddr:     listenAddr,
		KubeconfigPath: GetEnv("KUBECONFIG", ""),
		Verbosity:      GetEnvInt("KLOG_VERBOSITY", DefaultKlogVerbosity),
	}
}

// InitializeKlog initializes klog with the specified verbosity.
func InitializeKlog(verbosity int) {
	klogInitMutex.Lock()
	defer klogInitMutex.Unlock()

	if !klogInitialized {
		klog.InitFlags(nil)

		klogInitialized = true
	}

	// Convert int to int32 for klog.Level, ensuring it fits within int32 range
	if verbosity >= 0 && verbosity <= 10 {
		_ = klog.V(klog.Level(int32(verbosity)))
	}
}
