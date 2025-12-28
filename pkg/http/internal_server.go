package http

import (
	"net/http"
	"time"

	"k8s.io/klog/v2"

	"kuack-node/pkg/health"
	"kuack-node/pkg/provider"
	"kuack-node/pkg/server"
)

const (
	internalServerReadTimeout                = 5 * time.Second
	internalServerWriteTimeout time.Duration = 0
)

// InternalServer handles internal cluster traffic (Logs and Health).
// It listens on port 10250 (Kubelet port) by default.
type InternalServer struct {
	*server.BaseHTTPServer

	logsServer   *LogsServer
	healthServer *health.Server
}

// NewInternalServer creates a new InternalServer.
func NewInternalServer(port int, logProvider provider.LogProvider) *InternalServer {
	mux := http.NewServeMux()

	// Logs Server Logic
	logsServer := NewLogsServer(0, logProvider)

	// Health Server Logic
	healthServer := health.NewServer(0)

	// Register Routes
	// 1. /containerLogs -> Logs
	mux.HandleFunc("/containerLogs/", func(w http.ResponseWriter, r *http.Request) {
		klog.Infof("InternalServer: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		logsServer.containerLogsHandler(w, r)
	})

	// 2. /healthz -> Health
	// We need to expose the handler from health package or wrap it.
	// Since health.Server uses a mux internally, we can just use its handler if exposed,
	// or simpler: just reimplement the simple health handler here or make it public in health package.
	// Let's check health package content again.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		klog.Infof("InternalServer: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		health.HealthzHandler(w, r)
	})

	s := &InternalServer{
		BaseHTTPServer: server.NewBaseHTTPServer("Internal Server", port, mux, internalServerReadTimeout, internalServerWriteTimeout),
		logsServer:     logsServer,
		healthServer:   healthServer,
	}

	return s
}
