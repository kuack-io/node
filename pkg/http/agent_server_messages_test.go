package http_test

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuack-node/pkg/http"
	"kuack-node/pkg/provider"
	"kuack-node/pkg/testutil"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAgentServer_Messages(t *testing.T) {
	t.Parallel()

	mockProvider := new(http.MockAgentManager)
	// AddAgent should be called on registration
	mockProvider.On("AddAgent", mock.Anything, mock.Anything).Return()
	// RemoveAgent should be called when connection closes
	mockProvider.On("RemoveAgent", mock.Anything).Return()

	cfg := &http.Config{
		ListenAddr: ":0",
		Provider:   mockProvider,
		AgentToken: "test-token",
	}

	server, err := http.NewAgentServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	// Connect with WebSocket
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agent/register"
	header := make(nethttp.Header)
	header.Set("X-Agent-Token", "test-token")

	conn, resp, err := websocket.DefaultDialer.Dial(u, header)
	require.NoError(t, err)

	if resp != nil {
		_ = resp.Body.Close()
	}

	defer func() { _ = conn.Close() }()

	// 1. Register
	regData := struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
		GPU    bool   `json:"gpu"`
		Token  string `json:"token"`
		UUID   string `json:"uuid"`
	}{
		CPU:    "4",
		Memory: "8Gi",
		GPU:    false,
		Token:  "test-token",
		UUID:   "agent-uuid-1",
	}
	dataBytes, err := json.Marshal(regData)
	require.NoError(t, err)

	regMsg := struct {
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		Data      json.RawMessage `json:"data"`
	}{
		Type:      http.MsgTypeRegister,
		Timestamp: time.Now(),
		Data:      json.RawMessage(dataBytes),
	}

	err = conn.WriteJSON(regMsg)
	require.NoError(t, err)

	// Read acknowledgment
	var ack struct {
		Type string `json:"type"`
	}

	err = conn.ReadJSON(&ack)
	require.NoError(t, err)
	require.Equal(t, "registered", ack.Type)

	// 2. Send Heartbeat
	heartbeatProcessed := make(chan struct{}, 1)

	mockProvider.On("GetAgent", "agent-uuid-1").Run(func(args mock.Arguments) {
		select {
		case heartbeatProcessed <- struct{}{}:
		default:
		}
	}).Return(&provider.AgentConnection{
		UUID: "agent-uuid-1",
	}, true)

	heartbeatData := http.HeartbeatData{
		UUID:        "agent-uuid-1",
		IsThrottled: false,
	}
	hbBytes, err := json.Marshal(heartbeatData)
	require.NoError(t, err)

	hbMsg := http.Message{
		Type:      http.MsgTypeHeartbeat,
		Timestamp: time.Now(),
		Data:      hbBytes,
	}
	err = conn.WriteJSON(hbMsg)
	require.NoError(t, err)

	testutil.WaitForSignal(t, heartbeatProcessed, time.Second, "heartbeat was not processed")
	mockProvider.AssertCalled(t, "GetAgent", "agent-uuid-1")

	// 3. Send Pod Status
	statusUpdated := make(chan struct{}, 1)

	mockProvider.On("UpdatePodStatus", "default", "pod-1", mock.Anything).Run(func(args mock.Arguments) {
		select {
		case statusUpdated <- struct{}{}:
		default:
		}
	}).Return(nil)

	statusData := http.PodStatusData{
		Namespace: "default",
		Name:      "pod-1",
		Status: provider.AgentPodStatus{
			Phase: "Running",
		},
	}
	statusBytes, err := json.Marshal(statusData)
	require.NoError(t, err)

	statusMsg := http.Message{
		Type:      http.MsgTypePodStatus,
		Timestamp: time.Now(),
		Data:      statusBytes,
	}
	err = conn.WriteJSON(statusMsg)
	require.NoError(t, err)

	testutil.WaitForSignal(t, statusUpdated, time.Second, "pod status was not updated")
	mockProvider.AssertCalled(t, "UpdatePodStatus", "default", "pod-1", mock.Anything)

	// 4. Send Pod Logs
	logsFlushed := make(chan struct{}, 1)

	mockProvider.On("AppendPodLog", "default", "pod-1", "log line").Run(func(args mock.Arguments) {
		select {
		case logsFlushed <- struct{}{}:
		default:
		}
	}).Return()

	logsData := http.PodLogsData{
		Namespace: "default",
		Name:      "pod-1",
		Log:       "log line",
	}
	logsBytes, err := json.Marshal(logsData)
	require.NoError(t, err)

	logsMsg := http.Message{
		Type:      http.MsgTypePodLogs,
		Timestamp: time.Now(),
		Data:      logsBytes,
	}
	err = conn.WriteJSON(logsMsg)
	require.NoError(t, err)

	testutil.WaitForSignal(t, logsFlushed, time.Second, "pod logs were not appended")
	mockProvider.AssertCalled(t, "AppendPodLog", "default", "pod-1", "log line")
}

func TestAgentServer_Errors(t *testing.T) {
	t.Parallel()

	setupServer := func(t *testing.T) (*httptest.Server, *http.MockAgentManager) {
		t.Helper()

		mockProvider := new(http.MockAgentManager)
		mockProvider.On("AddAgent", mock.Anything, mock.Anything).Return()
		mockProvider.On("RemoveAgent", mock.Anything).Return()

		cfg := &http.Config{
			ListenAddr: ":0",
			Provider:   mockProvider,
			AgentToken: "test-token",
		}

		server, err := http.NewAgentServer(cfg)
		require.NoError(t, err)

		ts := httptest.NewServer(server.Handler())
		t.Cleanup(ts.Close)

		return ts, mockProvider
	}

	t.Run("Invalid Token", func(t *testing.T) {
		t.Parallel()
		ts, _ := setupServer(t)
		u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agent/register"

		conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
		require.NoError(t, err)

		if resp != nil {
			_ = resp.Body.Close()
		}

		defer func() { _ = conn.Close() }()

		regData := struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
			GPU    bool   `json:"gpu"`
			Token  string `json:"token"`
			UUID   string `json:"uuid"`
		}{
			CPU:    "4",
			Memory: "8Gi",
			GPU:    false,
			Token:  "wrong-token",
			UUID:   "agent-uuid-wrong",
		}
		err = conn.WriteJSON(struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Data      any       `json:"data"`
		}{
			Type:      http.MsgTypeRegister,
			Timestamp: time.Now(),
			Data:      regData,
		})
		require.NoError(t, err)

		var errMsg struct {
			Type string `json:"type"`
			Data struct {
				Error string `json:"error"`
			} `json:"data"`
		}

		err = conn.ReadJSON(&errMsg)
		if err == nil {
			assert.Equal(t, "error", errMsg.Type)
			assert.Equal(t, "unauthorized", errMsg.Data.Error)
		}
	})

	t.Run("Invalid JSON", func(t *testing.T) {
		t.Parallel()
		ts, _ := setupServer(t)
		u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agent/register"

		conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
		require.NoError(t, err)

		if resp != nil {
			_ = resp.Body.Close()
		}

		defer func() { _ = conn.Close() }()

		err = conn.WriteMessage(websocket.TextMessage, []byte("invalid-json"))
		require.NoError(t, err)

		_, _, err = conn.ReadMessage()
		assert.Error(t, err)
	})

	t.Run("Duplicate Browser ID", func(t *testing.T) {
		t.Parallel()
		ts, mockProvider := setupServer(t)
		u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agent/register"

		var capturedAgent *provider.AgentConnection
		// Override AddAgent to capture
		mockProvider.ExpectedCalls = nil // Clear previous expectations
		mockProvider.On("AddAgent", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			var ok bool

			capturedAgent, ok = args.Get(1).(*provider.AgentConnection)
			if !ok {
				panic("AddAgent called with wrong type")
			}
		}).Return()
		mockProvider.On("RemoveAgent", mock.Anything).Return()

		// 1. Connect first agent
		conn1, resp, err := websocket.DefaultDialer.Dial(u, nil)
		require.NoError(t, err)

		if resp != nil {
			_ = resp.Body.Close()
		}

		defer func() { _ = conn1.Close() }()

		regData1 := struct {
			CPU       string `json:"cpu"`
			Memory    string `json:"memory"`
			GPU       bool   `json:"gpu"`
			Token     string `json:"token"`
			UUID      string `json:"uuid"`
			BrowserID string `json:"browserId"`
		}{
			CPU:       "4",
			Memory:    "8Gi",
			GPU:       false,
			Token:     "test-token",
			UUID:      "agent-uuid-1",
			BrowserID: "browser-1",
		}
		err = conn1.WriteJSON(struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Data      any       `json:"data"`
		}{
			Type:      http.MsgTypeRegister,
			Timestamp: time.Now(),
			Data:      regData1,
		})
		require.NoError(t, err)

		// Read ack for agent 1
		var ack1 struct {
			Type string `json:"type"`
		}

		err = conn1.ReadJSON(&ack1)
		require.NoError(t, err)
		require.Equal(t, "registered", ack1.Type)

		// Wait for AddAgent to be called and capturedAgent to be set
		testutil.WaitForCondition(t, time.Second, func() bool { return capturedAgent != nil }, "expected first agent to be registered")

		// 2. Connect second agent with same Browser ID
		// Expect GetAgent to be called for the old agent
		mockProvider.On("GetAgent", "agent-uuid-1").Return(capturedAgent, true)

		conn2, resp, err := websocket.DefaultDialer.Dial(u, nil)
		require.NoError(t, err)

		if resp != nil {
			_ = resp.Body.Close()
		}

		defer func() { _ = conn2.Close() }()

		regData2 := regData1
		regData2.UUID = "agent-uuid-2"

		err = conn2.WriteJSON(struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Data      any       `json:"data"`
		}{
			Type:      http.MsgTypeRegister,
			Timestamp: time.Now(),
			Data:      regData2,
		})
		require.NoError(t, err)

		// Read ack for agent 2
		var ack2 struct {
			Type string `json:"type"`
		}

		err = conn2.ReadJSON(&ack2)
		require.NoError(t, err)
		require.Equal(t, "registered", ack2.Type)

		// 3. Verify agent 1 is disconnected
		_, _, err = conn1.ReadMessage()
		require.Error(t, err)
	})
}
