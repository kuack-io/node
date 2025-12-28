package http

import (
	"context"
	"net/http"

	"github.com/google/go-containerregistry/pkg/v1"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/server"
)

// PublicServer handles public-facing traffic (Agents and Registry).
// It listens on port 8080 by default.
type PublicServer struct {
	*server.BaseHTTPServer

	agentServer    *AgentServer
	registryServer *RegistryServer
}

// NewPublicServer creates a new PublicServer.
func NewPublicServer(port int, token string, provider provider.AgentManager) (*PublicServer, error) {
	mux := http.NewServeMux()

	// Initialize sub-servers
	// We pass nil for mux because we'll register their handlers manually on the main mux
	// or we can refactor them to be just handlers/controllers.
	// For now, let's reuse the logic by extracting handlers.

	// Agent Server Logic
	agentServer, err := NewAgentServer(&Config{
		ListenAddr: "", // Not used when embedded
		Provider:   provider,
		AgentToken: token,
	})
	if err != nil {
		return nil, err
	}

	// Registry Server Logic
	registryServer := NewRegistryServer(0) // Port not used

	// Register Routes
	// 1. / -> Agent WebSocket
	mux.HandleFunc("/", agentServer.handleWebSocket)

	// 2. /registry -> Registry Proxy
	mux.HandleFunc("/registry", func(w http.ResponseWriter, r *http.Request) {
		// Check token if configured
		if token != "" {
			queryToken := r.URL.Query().Get("token")
			if queryToken != token {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)

				return
			}
		}

		registryServer.registryProxy.ServeHTTP(w, r)
	})

	s := &PublicServer{
		BaseHTTPServer: server.NewBaseHTTPServer("Public Server", port, mux, httpReadTimeout, httpWriteTimeout),
		agentServer:    agentServer,
		registryServer: registryServer,
	}

	return s, nil
}

// SetRegistryFetchArtifact lets tests stub registry responses without touching remote registries.
func (s *PublicServer) SetRegistryFetchArtifact(f func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error)) {
	s.registryServer.SetFetchArtifact(f)
}
