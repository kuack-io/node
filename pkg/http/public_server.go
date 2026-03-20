package http

import (
	"net/http"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/server"
)

// PublicServer handles public-facing traffic (Agents and Registry).
// It listens on port 8080 by default.
type PublicServer struct {
	*server.BaseHTTPServer

	agentServer *AgentServer
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

	// Register Routes
	// 1. / -> Agent WebSocket
	mux.HandleFunc("/", agentServer.handleWebSocket)

	s := &PublicServer{
		BaseHTTPServer: server.NewBaseHTTPServer("Public Server", port, mux, httpReadTimeout, httpWriteTimeout),
		agentServer:    agentServer,
	}

	return s, nil
}
