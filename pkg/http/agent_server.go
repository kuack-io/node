package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"kuack-node/pkg/provider"
	"kuack-node/pkg/server"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
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
	Provider   provider.AgentManager
	AgentToken string
}

// AgentServer manages HTTP/WebSocket connections from browser agents.
type AgentServer struct {
	*server.BaseHTTPServer

	config          *Config
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

// PodStatusData represents a status update pushed from an agent.
type PodStatusData struct {
	Namespace string                  `json:"namespace"`
	Name      string                  `json:"name"`
	Status    provider.AgentPodStatus `json:"status"`
}

// PodLogsData represents a log chunk pushed from an agent.
type PodLogsData struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Log       string `json:"log"`
}

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
	Token     string            `json:"token"`
}

// HeartbeatData is sent periodically by agents.
type HeartbeatData struct {
	UUID        string `json:"uuid"`
	IsThrottled bool   `json:"isThrottled"` // Page Visibility API state
}

// NewAgentServer creates a new HTTP server instance.
func NewAgentServer(config *Config) (*AgentServer, error) {
	s := &AgentServer{
		config:          config,
		browserIDToUUID: make(map[string]string),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWebSocket)

	var port int

	if config.ListenAddr != "" {
		// Parse port from ListenAddr
		_, portStr, err := net.SplitHostPort(config.ListenAddr)
		if err != nil {
			return nil, fmt.Errorf("invalid listen address: %w", err)
		}

		var errAtoi error

		port, errAtoi = strconv.Atoi(portStr)
		if errAtoi != nil {
			return nil, fmt.Errorf("invalid port: %w", errAtoi)
		}
	}

	s.BaseHTTPServer = server.NewBaseHTTPServer("Agent Server", port, mux, httpReadTimeout, httpWriteTimeout)

	return s, nil
}

// handleWebSocket handles WebSocket connections from agents.
func (s *AgentServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
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
func (s *AgentServer) handleAgent(ctx context.Context, conn *websocket.Conn) {
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

	// Validate token if configured
	if s.config.AgentToken != "" {
		if regData.Token != s.config.AgentToken {
			klog.Warningf("Agent registration failed: invalid token from UUID %s", regData.UUID)
			// Send an error message and close connection
			errMsg := Message{
				Type:      "error",
				Timestamp: time.Now(),
				Data:      json.RawMessage(`{"error": "unauthorized"}`),
			}
			_ = s.sendMessage(conn, &errMsg)

			return
		}
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
func (s *AgentServer) handleMessage(agentUUID string, msg *Message) {
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
		if s.config.Provider == nil {
			klog.Warning("Received pod status but provider is nil")

			return
		}

		var statusData PodStatusData

		err := json.Unmarshal(msg.Data, &statusData)
		if err != nil {
			klog.Errorf("Failed to unmarshal pod status: %v", err)

			return
		}

		if statusData.Namespace == "" || statusData.Name == "" {
			klog.Warning("Pod status missing namespace or name")

			return
		}

		err = s.config.Provider.UpdatePodStatus(statusData.Namespace, statusData.Name, statusData.Status)
		if err != nil {
			klog.Errorf(
				"Failed to update pod status for %s/%s: %v",
				statusData.Namespace,
				statusData.Name,
				err,
			)
		}

		klog.V(logVerboseLevel).
			Infof("Updated pod status for %s/%s from agent %s", statusData.Namespace, statusData.Name, agentUUID)

	case MsgTypePodLogs:
		if s.config.Provider == nil {
			klog.Warning("Received pod logs but provider is nil")

			return
		}

		var logsData PodLogsData

		err := json.Unmarshal(msg.Data, &logsData)
		if err != nil {
			klog.Errorf("Failed to unmarshal pod logs: %v", err)

			return
		}

		if logsData.Namespace == "" || logsData.Name == "" {
			klog.Warning("Pod logs missing namespace or name")

			return
		}

		s.config.Provider.AppendPodLog(logsData.Namespace, logsData.Name, logsData.Log)
		klog.V(logVerboseLevel).Infof("Received pod logs for %s/%s from agent %s", logsData.Namespace, logsData.Name, agentUUID)

	default:
		klog.Warningf("Unknown message type: %s", msg.Type)
	}
}

// handleHeartbeat processes heartbeat from an agent.
func (s *AgentServer) handleHeartbeat(agentUUID string, isThrottled bool) {
	agent, ok := s.config.Provider.GetAgent(agentUUID)
	if !ok {
		return
	}

	agent.LastHeartbeat = time.Now()
	agent.IsThrottled = isThrottled

	if isThrottled {
		klog.V(logVerboseLevel).Infof("Agent %s is throttled (background tab)", agentUUID)
	}
}

// monitorHeartbeat checks for agent timeouts.
func (s *AgentServer) monitorHeartbeat(ctx context.Context, agentUUID string) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agent, ok := s.config.Provider.GetAgent(agentUUID)
			if !ok {
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
func (s *AgentServer) registerAgent(ctx context.Context, agent *provider.AgentConnection) {
	s.config.Provider.AddAgent(ctx, agent)
	klog.Infof("Agent %s registered with provider", agent.UUID)
}

// handleDuplicateBrowserConnection checks if a browser ID is already connected
// and closes the old connection if found.
func (s *AgentServer) handleDuplicateBrowserConnection(browserID, newAgentUUID string) {
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

	oldAgent, ok := s.config.Provider.GetAgent(existingUUID)
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
func (s *AgentServer) handleAgentDisconnect(agentUUID string) {
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
func (s *AgentServer) sendMessage(conn *websocket.Conn, msg *Message) error {
	return conn.WriteJSON(msg)
}
