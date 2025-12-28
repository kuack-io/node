package config

import (
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
	// DefaultPublicPort is the default public server port.
	DefaultPublicPort = 8080
	// DefaultInternalPort is the default internal server port.
	DefaultInternalPort = 10250
	// DefaultKlogVerbosity is the default klog verbosity level.
	DefaultKlogVerbosity = 2
)

var (
	//nolint:gochecknoglobals // klog's global flags can only be toggled once per process, so the guard must be package-level state
	klogInitialized bool
	klogInitMutex   sync.Mutex //nolint:gochecknoglobals // companion mutex protects the global flag without passing locks everywhere
)

// EnvGetter is a function that looks up an environment variable.
type EnvGetter func(string) string

// Config holds application configuration.
type Config struct {
	NodeName       string
	PublicPort     int
	InternalPort   int
	TLSCertFile    string
	TLSKeyFile     string
	AgentToken     string
	KubeconfigPath string
	Verbosity      int
}

// GetEnv returns the value of an environment variable or a default value if not set.
func GetEnv(get EnvGetter, key, defaultValue string) string {
	if value := get(key); value != "" {
		return value
	}

	return defaultValue
}

// GetEnvBool returns the boolean value of an environment variable or a default value if not set.
func GetEnvBool(get EnvGetter, key string, defaultValue bool) bool {
	if value := get(key); value != "" {
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
func GetEnvInt(get EnvGetter, key string, defaultValue int) int {
	if value := get(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}

	return defaultValue
}

// LoadConfig reads configuration from environment variables.
func LoadConfig(get EnvGetter) *Config {
	return &Config{
		NodeName:       GetEnv(get, "NODE_NAME", DefaultNodeName),
		PublicPort:     GetEnvInt(get, "PUBLIC_PORT", DefaultPublicPort),
		InternalPort:   GetEnvInt(get, "INTERNAL_PORT", DefaultInternalPort),
		TLSCertFile:    GetEnv(get, "TLS_CERT_FILE", ""),
		TLSKeyFile:     GetEnv(get, "TLS_KEY_FILE", ""),
		AgentToken:     GetEnv(get, "AGENT_TOKEN", ""),
		KubeconfigPath: GetEnv(get, "KUBECONFIG", ""),
		Verbosity:      GetEnvInt(get, "KLOG_VERBOSITY", DefaultKlogVerbosity),
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
