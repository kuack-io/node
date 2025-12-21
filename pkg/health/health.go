package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"
)

const (
	// DefaultPort is the default port for the health server.
	DefaultPort = 10250
	// ReadHeaderTimeout is the timeout for reading request headers.
	ReadHeaderTimeout = 5 * time.Second
	// serverStartDelay is the delay to wait for the server to start before checking readiness.
	serverStartDelay = 100 * time.Millisecond
)

// Server provides a simple HTTP health check endpoint.
type Server struct {
	server *http.Server
	port   int
}

// NewServer creates a new health server.
func NewServer(port int) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/", healthzHandler) // Also handle root path

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: ReadHeaderTimeout,
	}

	return &Server{
		server: server,
		port:   port,
	}
}

// Start starts the health server in a goroutine.
func (s *Server) Start(ctx context.Context) <-chan error {
	errChan := make(chan error, 1)

	go func() {
		klog.Infof("Starting health server on port %d", s.port)

		err := s.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case errChan <- fmt.Errorf("health server failed: %w", err):
			default:
				// Channel full, error already queued
			}
		}
	}()

	// Wait for server to start
	go func() {
		// Give the server a moment to start
		time.Sleep(serverStartDelay)

		select {
		case <-errChan:
			// Server failed to start
		default:
			klog.Infof("Health server is ready on port %d", s.port)
		}
	}()

	return errChan
}

// Shutdown gracefully shuts down the health server.
func (s *Server) Shutdown(ctx context.Context) error {
	klog.Info("Shutting down health server...")

	return s.server.Shutdown(ctx)
}

// healthzHandler handles health check requests.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
