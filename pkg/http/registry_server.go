package http

import (
	"context"
	"net/http"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"kuack-node/pkg/registry"
	"kuack-node/pkg/server"
)

const (
	// DefaultRegistryPort is the default port for the registry server.
	DefaultRegistryPort = 8082
)

// RegistryServer provides a proxy for fetching WASM modules from OCI registries.
type RegistryServer struct {
	*server.BaseHTTPServer

	registryProxy *registry.Proxy
}

// NewRegistryServer creates a new registry server.
func NewRegistryServer(port int, proxy *registry.Proxy) *RegistryServer {
	mux := http.NewServeMux()
	mux.Handle("/", proxy)

	return &RegistryServer{
		BaseHTTPServer: server.NewBaseHTTPServer("Registry Server", port, mux, httpReadTimeout, 0), // 0 write timeout for large downloads
		registryProxy:  proxy,
	}
}

// SetFetchArtifact allows tests to override the artifact fetch logic without reaching remote registries.
func (s *RegistryServer) SetFetchArtifact(f func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error)) {
	s.registryProxy.SetFetchArtifact(f)
}
