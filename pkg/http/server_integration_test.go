//go:build integration

package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestServer_HandleAgent_Integration tests handleAgent with actual WebSocket connections
// This test requires network access and is run with the integration build tag
func TestServer_HandleAgent_Integration(t *testing.T) {
	p := createTestProvider()
	server, err := NewServer(&Config{
		ListenAddr: "127.0.0.1:0",
		Provider:   p,
	})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Start server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to start - use the listen address from config
	// For port 0, we need to wait a bit and then try to connect
	time.Sleep(500 * time.Millisecond)

	// Connect via WebSocket - use localhost with the configured port
	// Since we can't easily get the actual port from :0, we'll use a fixed port for integration tests
	wsURL := "ws://127.0.0.1" + strings.TrimPrefix(server.config.ListenAddr, "127.0.0.1") + "/ws"
	if server.config.ListenAddr == "127.0.0.1:0" {
		// For port 0, we need to find the actual port or use a known port
		// For integration tests, use a fixed port
		wsURL = "ws://127.0.0.1:8080/ws"
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial server: %v", err)
	}
	defer conn.Close()

	// Send registration message
	regData := RegisterData{
		UUID:   "test-agent-integration",
		CPU:    "1000m",
		Memory: "1Gi",
		GPU:    false,
		Labels: map[string]string{"test": "integration"},
	}

	regDataJSON, _ := json.Marshal(regData)
	msg := Message{
		Type:      MsgTypeRegister,
		Timestamp: time.Now(),
		Data:      regDataJSON,
	}

	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	// Read acknowledgment
	var ack Message
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("Failed to read ack: %v", err)
	}

	if ack.Type != "registered" {
		t.Errorf("Expected registered message, got %s", ack.Type)
	}

	// Give handleAgent time to process
	time.Sleep(500 * time.Millisecond)

	// Verify agent was registered
	_, ok := p.GetAgent("test-agent-integration")
	if !ok {
		t.Error("handleAgent() agent not registered")
	}

	// Send a heartbeat message
	heartbeatData := HeartbeatData{
		UUID:        "test-agent-integration",
		IsThrottled: false,
	}
	heartbeatJSON, _ := json.Marshal(heartbeatData)
	heartbeatMsg := Message{
		Type:      MsgTypeHeartbeat,
		Timestamp: time.Now(),
		Data:      heartbeatJSON,
	}

	if err := conn.WriteJSON(heartbeatMsg); err != nil {
		t.Logf("Failed to send heartbeat: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Clean up
	cancel()
	select {
	case <-errChan:
	case <-time.After(2 * time.Second):
		t.Log("Server shutdown timeout")
	}
}

// TestServer_ConnectionDrop tests behavior when a client connection is dropped
func TestServer_ConnectionDrop(t *testing.T) {
	p := createTestProvider()
	server, err := NewServer(&Config{
		ListenAddr: "127.0.0.1:0",
		Provider:   p,
	})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to start
	time.Sleep(500 * time.Millisecond)

	// Connect and register
	wsURL := "ws://127.0.0.1" + strings.TrimPrefix(server.config.ListenAddr, "127.0.0.1") + "/ws"
	if server.config.ListenAddr == "127.0.0.1:0" {
		wsURL = "ws://127.0.0.1:8080/ws"
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}

	regData := RegisterData{
		UUID:   "test-agent-drop",
		CPU:    "1000m",
		Memory: "1Gi",
		GPU:    false,
	}
	regDataJSON, _ := json.Marshal(regData)
	msg := Message{
		Type:      MsgTypeRegister,
		Timestamp: time.Now(),
		Data:      regDataJSON,
	}

	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("Failed to encode: %v", err)
	}

	// Read ack
	var ack Message
	conn.ReadJSON(&ack)

	// Wait for registration
	time.Sleep(500 * time.Millisecond)

	// Verify agent registered
	_, ok := p.GetAgent("test-agent-drop")
	if !ok {
		t.Error("Agent not registered")
	}

	// Abruptly close connection (simulate network drop)
	conn.Close()

	// Wait for server to detect disconnect
	time.Sleep(1 * time.Second)

	// Agent should be removed
	_, ok = p.GetAgent("test-agent-drop")
	if ok {
		t.Error("Agent should be removed after connection drop")
	}

	cancel()
	select {
	case <-errChan:
	case <-time.After(2 * time.Second):
	}
}

// TestServer_ConcurrentClients tests handling of multiple concurrent client connections
func TestServer_ConcurrentClients(t *testing.T) {
	p := createTestProvider()
	server, err := NewServer(&Config{
		ListenAddr: "127.0.0.1:0",
		Provider:   p,
	})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to start
	time.Sleep(300 * time.Millisecond)

	var addr net.Addr
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		ln, err := net.Listen("tcp", server.config.ListenAddr)
		if err == nil {
			addr = ln.Addr()
			ln.Close()
			break
		}
	}

	if addr == nil {
		t.Fatal("Server address not available")
	}

	wsURL := "ws://" + addr.String() + "/ws"

	numClients := 5
	registered := make(chan string, numClients)
	errors := make(chan error, numClients)

	// Connect multiple clients concurrently
	for i := 0; i < numClients; i++ {
		go func(id int) {
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				errors <- err
				return
			}
			defer conn.Close()

			agentUUID := fmt.Sprintf("test-agent-%d", id)
			regData := RegisterData{
				UUID:   agentUUID,
				CPU:    "1000m",
				Memory: "1Gi",
				GPU:    false,
			}
			regDataJSON, _ := json.Marshal(regData)
			msg := Message{
				Type:      MsgTypeRegister,
				Timestamp: time.Now(),
				Data:      regDataJSON,
			}

			if err := conn.WriteJSON(msg); err != nil {
				errors <- err
				return
			}

			// Read ack
			var ack Message
			conn.ReadJSON(&ack)

			registered <- agentUUID
		}(i)
	}

	// Wait for all registrations
	timeout := time.After(5 * time.Second)
	registeredCount := 0
	for registeredCount < numClients {
		select {
		case uuid := <-registered:
			registeredCount++
			t.Logf("Client %s registered", uuid)
		case err := <-errors:
			t.Errorf("Client error: %v", err)
		case <-timeout:
			t.Fatalf("Timeout waiting for registrations, got %d/%d", registeredCount, numClients)
		}
	}

	// Verify all agents are registered
	time.Sleep(500 * time.Millisecond)
	for i := 0; i < numClients; i++ {
		agentUUID := fmt.Sprintf("test-agent-%d", i)
		_, ok := p.GetAgent(agentUUID)
		if !ok {
			t.Errorf("Agent %s not registered", agentUUID)
		}
	}

	// Verify agent count
	count := p.GetAgentCount()
	if count != numClients {
		t.Errorf("Expected %d agents, got %d", numClients, count)
	}

	cancel()
	select {
	case <-errChan:
	case <-time.After(2 * time.Second):
	}
}

// TestServer_Reconnection tests agent reconnection after disconnection
func TestServer_Reconnection(t *testing.T) {
	p := createTestProvider()
	server, err := NewServer(&Config{
		ListenAddr: "127.0.0.1:0",
		Provider:   p,
	})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to start
	time.Sleep(500 * time.Millisecond)

	wsURL := "ws://127.0.0.1" + strings.TrimPrefix(server.config.ListenAddr, "127.0.0.1") + "/ws"
	if server.config.ListenAddr == "127.0.0.1:0" {
		wsURL = "ws://127.0.0.1:8080/ws"
	}
	agentUUID := "test-agent-reconnect"

	// First connection
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}

	regData := RegisterData{
		UUID:   agentUUID,
		CPU:    "1000m",
		Memory: "1Gi",
		GPU:    false,
	}
	regDataJSON, _ := json.Marshal(regData)
	msg := Message{
		Type:      MsgTypeRegister,
		Timestamp: time.Now(),
		Data:      regDataJSON,
	}

	if err := conn1.WriteJSON(msg); err != nil {
		t.Fatalf("Failed to encode: %v", err)
	}

	// Read ack
	var ack Message
	conn1.ReadJSON(&ack)

	time.Sleep(500 * time.Millisecond)

	// Verify first connection
	_, ok := p.GetAgent(agentUUID)
	if !ok {
		t.Error("Agent not registered on first connection")
	}

	// Disconnect
	conn1.Close()

	time.Sleep(1 * time.Second)

	// Verify agent removed
	_, ok = p.GetAgent(agentUUID)
	if ok {
		t.Error("Agent should be removed after disconnect")
	}

	// Reconnect with same UUID
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to reconnect: %v", err)
	}
	defer conn2.Close()

	if err := conn2.WriteJSON(msg); err != nil {
		t.Fatalf("Failed to encode on reconnect: %v", err)
	}

	// Read ack
	conn2.ReadJSON(&ack)

	time.Sleep(500 * time.Millisecond)

	// Verify reconnection
	_, ok = p.GetAgent(agentUUID)
	if !ok {
		t.Error("Agent not registered after reconnection")
	}

	cancel()
	select {
	case <-errChan:
	case <-time.After(2 * time.Second):
	}
}
