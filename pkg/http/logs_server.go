package http

import (
	"io"
	"net/http"
	"strings"
	"time"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/server"

	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	"k8s.io/klog/v2"
)

const (
	logsServerName                         = "Logs Server"
	logsServerReadTimeout                  = 5 * time.Second
	logsServerWriteTimeout   time.Duration = 0
	logRequestExpectedParts                = 3
	logRequestVerbosityLevel               = 4
	defaultLogBufferSize                   = 1024
)

// LogsServer provides the Kubelet API endpoints (logs, exec, etc.).
type LogsServer struct {
	*server.BaseHTTPServer

	provider provider.LogProvider
}

// NewLogsServer creates a new Kubelet API server.
func NewLogsServer(port int, p provider.LogProvider) *LogsServer {
	s := &LogsServer{
		provider: p,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/containerLogs/", s.containerLogsHandler)

	s.BaseHTTPServer = server.NewBaseHTTPServer(logsServerName, port, mux, logsServerReadTimeout, logsServerWriteTimeout)

	return s
}

// containerLogsHandler handles container log requests.
// Path format: /containerLogs/{namespace}/{pod}/{container}.
func (s *LogsServer) containerLogsHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/containerLogs/")
	parts := strings.Split(path, "/")

	if len(parts) < logRequestExpectedParts {
		http.Error(w, "Invalid path", http.StatusBadRequest)

		return
	}

	namespace := parts[0]
	podName := parts[1]
	containerName := parts[2]

	klog.V(logRequestVerbosityLevel).Infof("Received log request for %s/%s/%s", namespace, podName, containerName)

	opts := api.ContainerLogOpts{
		Follow: r.URL.Query().Get("follow") == "true",
	}

	rc, err := s.provider.GetContainerLogs(r.Context(), namespace, podName, containerName, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)

		return
	}

	defer func() {
		closeErr := rc.Close()
		if closeErr != nil {
			klog.Errorf("Failed to close log stream: %v", closeErr)
		}
	}()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	buf := make([]byte, defaultLogBufferSize)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			_, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				klog.Errorf("Failed to write logs: %v", writeErr)

				return
			}

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}

		if err != nil {
			if err != io.EOF {
				klog.Errorf("Error reading logs: %v", err)
			}

			return
		}
	}
}
