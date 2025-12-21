package app

import (
	"context"
	"fmt"
	"time"

	"kuack-node/pkg/config"
	"kuack-node/pkg/health"
	httpserver "kuack-node/pkg/http"
	"kuack-node/pkg/k8s"
	"kuack-node/pkg/provider"

	"github.com/virtual-kubelet/virtual-kubelet/node"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Components holds the main application components.
type Components struct {
	WASMProvider *provider.WASMProvider
	HTTPServer   *httpserver.Server
	HealthServer *health.Server
	KubeClient   *kubernetes.Clientset
}

// SetupComponents initializes and returns the main application components.
func SetupComponents(cfg *config.Config) (*Components, error) {
	// Create the WASM provider
	wasmProvider, err := provider.NewWASMProvider(cfg.NodeName, cfg.DisableTaint)
	if err != nil {
		return nil, fmt.Errorf("failed to create WASM provider: %w", err)
	}

	// Create HTTP server for agent connections
	httpServer, err := httpserver.NewServer(&httpserver.Config{
		ListenAddr: cfg.ListenAddr,
		Provider:   wasmProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP server: %w", err)
	}

	// Get Kubernetes client
	kubeClient, err := k8s.GetKubeClient(cfg.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Create health server for readiness/liveness probes
	healthServer := health.NewServer(provider.KubeletPort)

	return &Components{
		WASMProvider: wasmProvider,
		HTTPServer:   httpServer,
		HealthServer: healthServer,
		KubeClient:   kubeClient,
	}, nil
}

// StartHTTPServer starts the HTTP server in a goroutine and returns an error channel.
func StartHTTPServer(ctx context.Context, httpServer *httpserver.Server) <-chan error {
	httpErrChan := make(chan error, 1)

	go func() {
		err := httpServer.Start(ctx)
		if err != nil {
			select {
			case httpErrChan <- fmt.Errorf("HTTP server failed: %w", err):
			default:
				// Channel full, error already queued
			}
		}
	}()

	return httpErrChan
}

// StartHealthServer starts the health server in a goroutine and returns an error channel.
func StartHealthServer(ctx context.Context, healthServer *health.Server) <-chan error {
	healthErrChan := make(chan error, 1)

	go func() {
		errChan := healthServer.Start(ctx)
		select {
		case err := <-errChan:
			if err != nil {
				select {
				case healthErrChan <- fmt.Errorf("health server failed: %w", err):
				default:
					// Channel full, error already queued
				}
			}
		case <-ctx.Done():
			// Context cancelled, server shutting down
		}
	}()

	return healthErrChan
}

// RunNodeController creates and runs the virtual kubelet node controller.
func RunNodeController(
	ctx context.Context,
	wasmProvider *provider.WASMProvider,
	kubeClient *kubernetes.Clientset,
) error {
	nodeRunner, err := node.NewNodeController(
		wasmProvider,
		wasmProvider.GetNode(),
		kubeClient.CoreV1().Nodes(),
	)
	if err != nil {
		return fmt.Errorf("failed to create node controller: %w", err)
	}

	klog.Info("Virtual Kubelet is ready and running")

	// Run the node controller
	err = nodeRunner.Run(ctx)
	if err != nil {
		return fmt.Errorf("node controller failed: %w", err)
	}

	return nil
}

// ShutdownGracefully performs graceful shutdown of the HTTP and health servers.
func ShutdownGracefully(
	ctx context.Context,
	httpServer *httpserver.Server,
	healthServer *health.Server,
	httpErrChan <-chan error,
	healthErrChan <-chan error,
) error {
	klog.Info("Shutting down gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, config.ShutdownTimeout)
	defer shutdownCancel()

	// Shutdown health server first (it's less critical)
	if healthServer != nil {
		err := healthServer.Shutdown(shutdownCtx)
		if err != nil {
			klog.Errorf("Error during health server shutdown: %v", err)
		}
	}

	// Shutdown HTTP server
	err := httpServer.Shutdown(shutdownCtx)
	if err != nil {
		klog.Errorf("Error during HTTP server shutdown: %v", err)
	}

	// Check for server errors (must check before returning)
	// Check with a short timeout to avoid blocking, but ensure we check the channels
	select {
	case err := <-httpErrChan:
		if err != nil {
			return fmt.Errorf("HTTP server error: %w", err)
		}
	case <-time.After(config.HttpErrChanTimeout):
		// No error in httpErrChan within timeout, continue
	}

	select {
	case err := <-healthErrChan:
		if err != nil {
			return fmt.Errorf("health server error: %w", err)
		}
	case <-time.After(config.HttpErrChanTimeout):
		// No error in healthErrChan within timeout, continue
	}

	klog.Info("Shutdown complete")

	return nil
}

// Run is the main application logic, extracted for testing.
func Run(ctx context.Context, cfg *config.Config) error {
	// Initialize klog
	config.InitializeKlog(cfg.Verbosity)

	klog.Infof("Starting Virtual Kubelet for WASM workloads")
	klog.Infof("Node name: %s", cfg.NodeName)
	klog.Infof("HTTP listen address: %s", cfg.ListenAddr)
	klog.Infof("Klog verbosity: %d", cfg.Verbosity)

	// Setup components
	components, err := SetupComponents(cfg)
	if err != nil {
		return err
	}

	// Start health server (for readiness/liveness probes)
	healthErrChan := StartHealthServer(ctx, components.HealthServer)

	// Start HTTP server
	httpErrChan := StartHTTPServer(ctx, components.HTTPServer)

	// Run node controller
	err = RunNodeController(ctx, components.WASMProvider, components.KubeClient)
	if err != nil {
		return err
	}

	// Graceful shutdown
	return ShutdownGracefully(
		ctx,
		components.HTTPServer,
		components.HealthServer,
		httpErrChan,
		healthErrChan,
	)
}
