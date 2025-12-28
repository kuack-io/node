package health

import (
	"net/http"
	"time"

	"kuack-node/pkg/server"
)

const (
	// DefaultPort is the default port for the health server.
	DefaultPort        = 8081
	healthReadTimeout  = 5 * time.Second
	healthWriteTimeout = 5 * time.Second
)

// Server provides a simple HTTP health check endpoint.
type Server struct {
	*server.BaseHTTPServer
}

// NewServer creates a new health server.
func NewServer(port int) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", HealthzHandler)

	return &Server{
		BaseHTTPServer: server.NewBaseHTTPServer("Health Server", port, mux, healthReadTimeout, healthWriteTimeout),
	}
}

// HealthzHandler handles health check requests.
func HealthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
