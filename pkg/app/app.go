package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"kuack-node/pkg/config"
	"kuack-node/pkg/health"
	httpserver "kuack-node/pkg/http"
	"kuack-node/pkg/k8s"
	"kuack-node/pkg/provider"

	"github.com/virtual-kubelet/virtual-kubelet/node"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

var (
	// ErrNodeControllerPanic is returned when the node controller panics.
	ErrNodeControllerPanic = errors.New("node controller panicked")
)

const podControllerWorkers = 5

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
	wasmProvider, err := provider.NewWASMProvider(cfg.NodeName)
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
	// Create a pod controller to handle pod lifecycle events
	// We need to watch pods assigned to this node
	nodeName := wasmProvider.GetNode().Name

	// Create informer factory with field selector to only watch pods for this node
	podInformerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		0, // resync period
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", nodeName).String()
		}),
	)

	// Create a separate informer factory for other resources (ConfigMaps, Secrets, Services)
	// These are not scoped to the node, so we need a standard informer factory
	scmInformerFactory := informers.NewSharedInformerFactory(kubeClient, 0)

	// Get the informers
	podInformer := podInformerFactory.Core().V1().Pods()
	configMapInformer := scmInformerFactory.Core().V1().ConfigMaps()
	secretInformer := scmInformerFactory.Core().V1().Secrets()
	serviceInformer := scmInformerFactory.Core().V1().Services()

	// Create event broadcaster
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events(corev1.NamespaceAll)})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "kuack-node"})

	podControllerConfig := node.PodControllerConfig{
		PodClient:         kubeClient.CoreV1(),
		PodInformer:       podInformer,
		ConfigMapInformer: configMapInformer,
		SecretInformer:    secretInformer,
		ServiceInformer:   serviceInformer,
		Provider:          wasmProvider,
		EventRecorder:     recorder,
	}

	podController, err := node.NewPodController(podControllerConfig)
	if err != nil {
		return fmt.Errorf("failed to create pod controller: %w", err)
	}

	// Start the informer factories
	podInformerFactory.Start(ctx.Done())
	scmInformerFactory.Start(ctx.Done())

	// Wait for cache sync
	podInformerFactory.WaitForCacheSync(ctx.Done())
	scmInformerFactory.WaitForCacheSync(ctx.Done())

	// Run the pod controller in a separate goroutine
	go func() {
		err := podController.Run(ctx, podControllerWorkers)
		if err != nil {
			klog.Errorf("Pod controller failed: %v", err)
		}
	}()

	nodeRunner, err := node.NewNodeController(
		wasmProvider,
		wasmProvider.GetNode(),
		kubeClient.CoreV1().Nodes(),
	)
	if err != nil {
		return fmt.Errorf("failed to create node controller: %w", err)
	}

	klog.Info("Virtual Kubelet is ready and running")

	// Run the node controller with panic recovery
	// The virtual-kubelet library may panic if the node doesn't exist in the cluster
	var (
		runErr     error
		panicValue any
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Store panic value to include in error message
				panicValue = r
				runErr = ErrNodeControllerPanic
			}
		}()

		runErr = nodeRunner.Run(ctx)
	}()

	if panicValue != nil {
		return fmt.Errorf("%w: %v", runErr, panicValue)
	}

	if runErr != nil {
		return fmt.Errorf("node controller failed: %w", runErr)
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
