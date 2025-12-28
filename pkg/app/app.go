package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"kuack-node/pkg/config"
	httpserver "kuack-node/pkg/http"
	"kuack-node/pkg/k8s"
	"kuack-node/pkg/provider"
	tlsutil "kuack-node/pkg/tls"

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

// allow overriding external dependencies in tests.
//
//nolint:gochecknoglobals // package-level seams let tests stub kube clients, TLS, and HTTP servers without plumbing args everywhere
var (
	newWASMProviderFunc   = provider.NewWASMProvider
	newPublicServerFunc   = httpserver.NewPublicServer
	newInternalServerFunc = httpserver.NewInternalServer
	getKubeClientFunc     = func(path string) (kubernetes.Interface, error) {
		return k8s.GetKubeClient(path)
	}
	requestCertificateFunc     = tlsutil.RequestCertificateFromK8s
	generateSelfSignedCertFunc = tlsutil.GenerateSelfSignedCert
	getEnvFunc                 = os.Getenv
	publicServerStartFunc      = func(server *httpserver.PublicServer, ctx context.Context) <-chan error {
		return server.Start(ctx)
	}
	internalServerStartFunc = func(server *httpserver.InternalServer, ctx context.Context) <-chan error {
		return server.Start(ctx)
	}
)

// Components holds the main application components.
type Components struct {
	WASMProvider   *provider.WASMProvider
	PublicServer   *httpserver.PublicServer
	InternalServer *httpserver.InternalServer
	KubeClient     kubernetes.Interface
}

// SetupComponents initializes and returns the main application components.
func SetupComponents(ctx context.Context, cfg *config.Config) (*Components, error) {
	// Create the WASM provider
	wasmProvider, err := newWASMProviderFunc(cfg.NodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to create WASM provider: %w", err)
	}

	// Create Public Server (Agent + Registry)
	publicServer, err := newPublicServerFunc(cfg.PublicPort, cfg.AgentToken, wasmProvider)
	if err != nil {
		return nil, fmt.Errorf("failed to create Public server: %w", err)
	}

	// Get Kubernetes client first (needed for CSR)
	kubeClient, err := getKubeClientFunc(cfg.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Request TLS certificates from Kubernetes CSR API if not provided
	certFile := cfg.TLSCertFile
	keyFile := cfg.TLSKeyFile

	if certFile == "" || keyFile == "" {
		// Use /tmp for certificate storage
		certFile = "/tmp/kuack-kubelet-serving.crt"
		keyFile = "/tmp/kuack-kubelet-serving.key"

		// Get the node IP (POD_IP environment variable)
		nodeIP := getEnvFunc("POD_IP")
		if nodeIP == "" {
			nodeIP = "127.0.0.1" // Fallback to localhost
		}

		// Always request a new certificate on startup to ensure it matches the current Pod IP
		// This is important in environments where Pod IPs change frequently (like k3d/devspace)

		// Request certificate from Kubernetes CSR API
		err = requestCertificateFunc(ctx, kubeClient, cfg.NodeName, nodeIP, certFile, keyFile)
		if err != nil {
			klog.Warningf("Failed to request certificate from Kubernetes CSR API: %v. Falling back to self-signed certificate.", err)

			// Fallback to self-signed certificate
			err = generateSelfSignedCertFunc(nodeIP, certFile, keyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to generate self-signed TLS certificate: %w", err)
			}
		}
	}

	// Create Internal Server (Logs + Health) with TLS
	internalServer := newInternalServerFunc(cfg.InternalPort, wasmProvider)
	internalServer.WithTLS(certFile, keyFile)

	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	return &Components{
		WASMProvider:   wasmProvider,
		PublicServer:   publicServer,
		InternalServer: internalServer,
		KubeClient:     kubeClient,
	}, nil
}

// StartPublicServer starts the Public server in a goroutine and returns an error channel.
func StartPublicServer(ctx context.Context, server *httpserver.PublicServer) <-chan error {
	errChan := make(chan error, 1)

	go func() {
		serverErrChan := publicServerStartFunc(server, ctx)
		select {
		case err := <-serverErrChan:
			if err != nil {
				select {
				case errChan <- fmt.Errorf("public server failed: %w", err):
				default:
				}
			}
		case <-ctx.Done():
		}
	}()

	return errChan
}

// StartInternalServer starts the Internal server in a goroutine and returns an error channel.
func StartInternalServer(ctx context.Context, server *httpserver.InternalServer) <-chan error {
	errChan := make(chan error, 1)

	go func() {
		serverErrChan := internalServerStartFunc(server, ctx)
		select {
		case err := <-serverErrChan:
			if err != nil {
				select {
				case errChan <- fmt.Errorf("internal server failed: %w", err):
				default:
				}
			}
		case <-ctx.Done():
		}
	}()

	return errChan
}

// RunNodeController creates and runs the virtual kubelet node controller.
func RunNodeController(
	ctx context.Context,
	wasmProvider *provider.WASMProvider,
	kubeClient kubernetes.Interface,
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

// ShutdownGracefully performs graceful shutdown of the servers.
func ShutdownGracefully(
	ctx context.Context,
	publicServer *httpserver.PublicServer,
	internalServer *httpserver.InternalServer,
	publicErrChan <-chan error,
	internalErrChan <-chan error,
) error {
	klog.Info("Shutting down gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, config.ShutdownTimeout)
	defer shutdownCancel()

	// Shutdown Internal server
	if internalServer != nil {
		err := internalServer.Shutdown(shutdownCtx)
		if err != nil {
			klog.Errorf("Error during Internal server shutdown: %v", err)
		}
	}

	// Shutdown Public server
	if publicServer != nil {
		err := publicServer.Shutdown(shutdownCtx)
		if err != nil {
			klog.Errorf("Error during Public server shutdown: %v", err)
		}
	}

	// Check for server errors
	select {
	case err := <-publicErrChan:
		if err != nil {
			return fmt.Errorf("public server error: %w", err)
		}
	case <-time.After(config.HttpErrChanTimeout):
	}

	select {
	case err := <-internalErrChan:
		if err != nil {
			return fmt.Errorf("internal server error: %w", err)
		}
	case <-time.After(config.HttpErrChanTimeout):
	}

	klog.Info("Shutdown complete")

	return nil
}

// NodeControllerRunner is a function that runs the node controller.
type NodeControllerRunner func(context.Context, *provider.WASMProvider, kubernetes.Interface) error

// Run is the main application logic, extracted for testing.
func Run(ctx context.Context, cfg *config.Config, nodeRunner NodeControllerRunner) error {
	// Initialize klog
	config.InitializeKlog(cfg.Verbosity)

	klog.Infof("Starting Virtual Kubelet for WASM workloads")
	klog.Infof("Node name: %s", cfg.NodeName)
	klog.Infof("Klog verbosity: %d", cfg.Verbosity)

	// Setup components
	components, err := SetupComponents(ctx, cfg)
	if err != nil {
		return err
	}

	// Start Public Server
	publicErrChan := StartPublicServer(ctx, components.PublicServer)

	// Start Internal Server
	internalErrChan := StartInternalServer(ctx, components.InternalServer)

	// Run node controller
	if nodeRunner == nil {
		nodeRunner = RunNodeController
	}

	err = nodeRunner(ctx, components.WASMProvider, components.KubeClient)
	if err != nil {
		return err
	}

	// Graceful shutdown
	return ShutdownGracefully(
		ctx,
		components.PublicServer,
		components.InternalServer,
		publicErrChan,
		internalErrChan,
	)
}
