package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	"kuack-node/pkg/provider"
)

const (
	// heartbeatInterval is how often to check for agent heartbeat timeouts.
	heartbeatInterval = 30 * time.Second
	// logVerboseLevel is the klog verbosity level for verbose debug logs.
	logVerboseLevel = 4
	// readTimeout is the timeout for reading from WebSocket connections.
	readTimeout = 60 * time.Second
	// writeTimeout is the timeout for writing to WebSocket connections.
	writeTimeout = 10 * time.Second
	// websocketBufferSize is the buffer size for WebSocket read/write operations.
	websocketBufferSize = 1024
	// httpReadTimeout is the HTTP server read timeout.
	httpReadTimeout = 15 * time.Second
	// httpWriteTimeout is the HTTP server write timeout.
	httpWriteTimeout = 15 * time.Second
	// httpIdleTimeout is the HTTP server idle timeout.
	httpIdleTimeout = 60 * time.Second
	// closeCodeDuplicateBrowser is the WebSocket close code used when closing
	// a connection due to a duplicate browser connection (same browser ID).
	// Using 4001 (in the 4000-4999 range reserved for libraries/frameworks).
	closeCodeDuplicateBrowser = 4001
)

// getUpgrader returns a WebSocket upgrader.
func getUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			// Allow all origins for now - can be restricted later
			return true
		},
		ReadBufferSize:  websocketBufferSize,
		WriteBufferSize: websocketBufferSize,
	}
}

// Config holds HTTP server configuration.
type Config struct {
	ListenAddr string
	Provider   *provider.WASMProvider
}

// Server manages HTTP/WebSocket connections from browser agents.
type Server struct {
	config          *Config
	server          *http.Server
	browserIDToUUID map[string]string // Maps browser ID to agent UUID
	browserIDMutex  sync.RWMutex      // Protects browserIDToUUID map
}

// Message types for agent communication protocol.
const (
	MsgTypeRegister  = "register"
	MsgTypeHeartbeat = "heartbeat"
	MsgTypePodSpec   = "pod_spec"
	MsgTypePodStatus = "pod_status"
	MsgTypePodDelete = "pod_delete"
	MsgTypePodLogs   = "pod_logs"
)

// Message represents a protocol message.
type Message struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// RegisterData is sent by agents on connection.
type RegisterData struct {
	UUID      string            `json:"uuid"`
	BrowserID string            `json:"browserId"` // Persistent browser identifier
	CPU       string            `json:"cpu"`       // e.g., "4000m"
	Memory    string            `json:"memory"`    // e.g., "8Gi"
	GPU       bool              `json:"gpu"`
	Labels    map[string]string `json:"labels"`
}

// HeartbeatData is sent periodically by agents.
type HeartbeatData struct {
	UUID        string `json:"uuid"`
	IsThrottled bool   `json:"isThrottled"` // Page Visibility API state
}

// NewServer creates a new HTTP server instance.
func NewServer(config *Config) (*Server, error) {
	return &Server{
		config:          config,
		browserIDToUUID: make(map[string]string),
	}, nil
}

// Start begins listening for agent connections.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Create listener first to get the actual address
	lc := net.ListenConfig{}

	listener, err := lc.Listen(ctx, "tcp", s.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  httpReadTimeout,
		WriteTimeout: httpWriteTimeout,
		IdleTimeout:  httpIdleTimeout,
	}

	klog.Infof("Starting HTTP server on %s", listener.Addr().String())

	// Start server in a goroutine
	errChan := make(chan error, 1)

	go func() {
		err := s.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	klog.Info("HTTP server is ready, waiting for agent connections")

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		return fmt.Errorf("server context cancelled: %w", ctx.Err())
	case err := <-errChan:
		return fmt.Errorf("server error: %w", err)
	}
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server != nil {
		klog.Info("Shutting down HTTP server")

		err := s.server.Shutdown(ctx)
		if err != nil {
			return fmt.Errorf("failed to shutdown server: %w", err)
		}

		return nil
	}

	return nil
}

// handleRoot handles root HTTP requests.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("kuack-node HTTP server\n"))
}

// handleWebSocket handles WebSocket connections from agents.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := getUpgrader()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		klog.Errorf("Failed to upgrade connection to WebSocket: %v", err)

		return
	}

	defer func() {
		closeErr := conn.Close()
		if closeErr != nil {
			klog.V(logVerboseLevel).Infof("Error closing WebSocket connection: %v", closeErr)
		}
	}()

	// Set timeouts
	err = conn.SetReadDeadline(time.Now().Add(readTimeout))
	if err != nil {
		klog.V(logVerboseLevel).Infof("Error setting read deadline: %v", err)
	}

	err = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err != nil {
		klog.V(logVerboseLevel).Infof("Error setting write deadline: %v", err)
	}

	ctx := r.Context()
	s.handleAgent(ctx, conn)
}

// handleAgent manages a single agent connection.
func (s *Server) handleAgent(ctx context.Context, conn *websocket.Conn) {
	klog.Info("New agent connection established")

	var agentUUID string

	defer func() {
		if agentUUID != "" {
			s.handleAgentDisconnect(agentUUID)
		}
	}()

	// Read registration message
	var registerMsg Message

	err := conn.ReadJSON(&registerMsg)
	if err != nil {
		klog.Errorf("Failed to read registration: %v", err)

		return
	}

	if registerMsg.Type != MsgTypeRegister {
		klog.Errorf("Expected register message, got: %s", registerMsg.Type)

		return
	}

	var regData RegisterData

	err = json.Unmarshal(registerMsg.Data, &regData)
	if err != nil {
		klog.Errorf("Failed to unmarshal registration data: %v", err)

		return
	}

	agentUUID = regData.UUID
	browserID := regData.BrowserID

	// Enforce "one agent per browser" policy
	// If another connection exists with the same browser ID, close it first
	if browserID != "" {
		s.handleDuplicateBrowserConnection(browserID, agentUUID)

		// Register this browser ID -> UUID mapping
		s.browserIDMutex.Lock()
		s.browserIDToUUID[browserID] = agentUUID
		s.browserIDMutex.Unlock()
	}

	klog.Infof("Agent registered: %s (Browser ID: %s, CPU: %s, Memory: %s, GPU: %v)",
		agentUUID, browserID, regData.CPU, regData.Memory, regData.GPU)

	// Create agent connection
	agent := &provider.AgentConnection{
		UUID:      agentUUID,
		BrowserID: browserID,
		Stream:    conn,
		Resources: provider.ResourceSpec{
			CPU:    resource.MustParse(regData.CPU),
			Memory: resource.MustParse(regData.Memory),
			GPU:    regData.GPU,
		},
		AllocatedPods: make(map[string]*corev1.Pod),
		LastHeartbeat: time.Now(),
		Labels:        regData.Labels,
	}

	// Register agent with provider
	s.registerAgent(ctx, agent)

	// Send acknowledgment
	ack := Message{
		Type:      "registered",
		Timestamp: time.Now(),
		Data:      json.RawMessage(`{"status": "ok"}`),
	}

	err = s.sendMessage(conn, &ack)
	if err != nil {
		klog.Errorf("Failed to send ack: %v", err)

		return
	}

	// Start heartbeat monitor
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()

	go s.monitorHeartbeat(heartbeatCtx, agentUUID)

	// Message handling loop
	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Update read deadline
			err = conn.SetReadDeadline(time.Now().Add(readTimeout))
			if err != nil {
				klog.V(logVerboseLevel).Infof("Error setting read deadline: %v", err)
			}

			var msg Message

			err := conn.ReadJSON(&msg)
			if err != nil {
				if websocket.IsUnexpectedCloseError(
					err,
					websocket.CloseGoingAway,
					websocket.CloseAbnormalClosure,
				) {
					klog.Errorf("WebSocket error: %v", err)
				} else {
					klog.Infof("Agent %s disconnected", agentUUID)
				}

				return
			}

			// Update write deadline before sending response
			err = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err != nil {
				klog.V(logVerboseLevel).Infof("Error setting write deadline: %v", err)
			}

			s.handleMessage(agentUUID, &msg)
		}
	}
}

// handleMessage processes messages from agents.
func (s *Server) handleMessage(agentUUID string, msg *Message) {
	switch msg.Type {
	case MsgTypeHeartbeat:
		var heartbeatData HeartbeatData

		err := json.Unmarshal(msg.Data, &heartbeatData)
		if err != nil {
			klog.Errorf("Failed to unmarshal heartbeat: %v", err)

			return
		}

		s.handleHeartbeat(agentUUID, heartbeatData.IsThrottled)

	case MsgTypePodStatus:
		// TODO: Handle pod status updates from agent
		klog.V(logVerboseLevel).Infof("Received pod status from agent %s", agentUUID)

	case MsgTypePodLogs:
		// TODO: Handle log streaming from agent
		klog.V(logVerboseLevel).Infof("Received pod logs from agent %s", agentUUID)

	default:
		klog.Warningf("Unknown message type: %s", msg.Type)
	}
}

// handleHeartbeat processes heartbeat from an agent.
func (s *Server) handleHeartbeat(agentUUID string, isThrottled bool) {
	agentVal, ok := s.config.Provider.GetAgent(agentUUID)
	if !ok {
		return
	}

	agent, ok := agentVal.(*provider.AgentConnection)
	if !ok {
		klog.Errorf("Invalid agent type for UUID: %s", agentUUID)

		return
	}

	agent.LastHeartbeat = time.Now()
	agent.IsThrottled = isThrottled

	if isThrottled {
		klog.V(logVerboseLevel).Infof("Agent %s is throttled (background tab)", agentUUID)
	}
}

// monitorHeartbeat checks for agent timeouts.
func (s *Server) monitorHeartbeat(ctx context.Context, agentUUID string) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agentVal, ok := s.config.Provider.GetAgent(agentUUID)
			if !ok {
				return
			}

			agent, ok := agentVal.(*provider.AgentConnection)
			if !ok {
				klog.Errorf("Invalid agent type for UUID: %s", agentUUID)

				return
			}

			if time.Since(agent.LastHeartbeat) > 90*time.Second {
				klog.Warningf("Agent %s heartbeat timeout, disconnecting", agentUUID)
				s.handleAgentDisconnect(agentUUID)

				return
			}
		}
	}
}

// registerAgent adds an agent to the provider.
func (s *Server) registerAgent(ctx context.Context, agent *provider.AgentConnection) {
	s.config.Provider.AddAgent(ctx, agent)
	klog.Infof("Agent %s registered with provider", agent.UUID)
}

// handleDuplicateBrowserConnection checks if a browser ID is already connected
// and closes the old connection if found.
func (s *Server) handleDuplicateBrowserConnection(browserID, newAgentUUID string) {
	s.browserIDMutex.Lock()
	existingUUID, exists := s.browserIDToUUID[browserID]
	s.browserIDMutex.Unlock()

	if !exists || existingUUID == newAgentUUID {
		return
	}

	klog.Infof(
		"Browser ID %s already connected with UUID %s, closing old connection",
		browserID,
		existingUUID,
	)

	oldAgentVal, ok := s.config.Provider.GetAgent(existingUUID)
	if !ok {
		return
	}

	oldAgent, ok := oldAgentVal.(*provider.AgentConnection)
	if !ok {
		return
	}

	oldConn, ok := oldAgent.Stream.(*websocket.Conn)
	if !ok {
		return
	}

	// Close the old WebSocket connection with custom close code
	// to signal duplicate browser connection - client should not reconnect
	closeMsg := websocket.FormatCloseMessage(
		closeCodeDuplicateBrowser,
		"New connection from same browser",
	)
	_ = oldConn.WriteControl(
		websocket.CloseMessage,
		closeMsg,
		time.Now().Add(writeTimeout),
	)
	_ = oldConn.Close()

	// Remove the old agent
	s.handleAgentDisconnect(existingUUID)
}

// handleAgentDisconnect cleans up when an agent disconnects.
func (s *Server) handleAgentDisconnect(agentUUID string) {
	klog.Infof("Handling disconnect for agent %s", agentUUID)

	// Find and remove browser ID mapping
	s.browserIDMutex.Lock()

	for browserID, uuid := range s.browserIDToUUID {
		if uuid == agentUUID {
			delete(s.browserIDToUUID, browserID)
			klog.V(logVerboseLevel).
				Infof("Removed browser ID mapping: %s -> %s", browserID, agentUUID)

			break
		}
	}

	s.browserIDMutex.Unlock()

	s.config.Provider.RemoveAgent(agentUUID)
}

// sendMessage sends a message to an agent via WebSocket.
func (s *Server) sendMessage(conn *websocket.Conn, msg *Message) error {
	return conn.WriteJSON(msg)
}
