package http_test

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuack-node/pkg/http"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAgentServer_Register(t *testing.T) {
	t.Parallel()

	mockProvider := new(http.MockAgentManager)
	// AddAgent should be called
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
	defer ts.Close()

	// Connect with WebSocket
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agent/register"
	header := make(nethttp.Header)
	header.Set("X-Agent-Token", "test-token")

	conn, resp, err := websocket.DefaultDialer.Dial(u, header)
	require.NoError(t, err)

	if resp != nil {
		_ = resp.Body.Close()
	}

	defer func() {
		_ = conn.Close()
	}()

	// Send register message
	regData := struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
		GPU    bool   `json:"gpu"`
		Token  string `json:"token"`
	}{
		CPU:    "4",
		Memory: "8Gi",
		GPU:    false,
		Token:  "test-token",
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

	// Verify AddAgent was called
	mockProvider.AssertCalled(t, "AddAgent", mock.Anything, mock.Anything)
}
