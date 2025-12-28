package server_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kuack-node/pkg/server"
)

func TestBaseHTTPServer_StartAndShutdown(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Use port 0 for random port
	srv := server.NewBaseHTTPServer("test-server", 0, handler, time.Second, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := srv.Start(ctx)

	// Wait for start
	time.Sleep(200 * time.Millisecond)

	// Check for immediate errors
	select {
	case err := <-errChan:
		require.NoError(t, err)
	default:
		// OK
	}

	// Shutdown
	err := srv.Shutdown(ctx)
	require.NoError(t, err)
}

func TestBaseHTTPServer_PortConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var lc net.ListenConfig

	// Find a free port first
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok, "Expected TCPAddr")

	port := addr.Port

	require.NoError(t, l.Close())

	// Occupy the port
	l2, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	require.NoError(t, err)

	defer func() {
		require.NoError(t, l2.Close())
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	srv := server.NewBaseHTTPServer("conflict-server", port, handler, time.Second, time.Second)

	errChan := srv.Start(ctx)

	// Should receive error
	select {
	case err := <-errChan:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bind")
	case <-time.After(2 * time.Second):
		require.Fail(t, "expected bind error but got timeout")
	}
}

func TestBaseHTTPServer_WithTLS(t *testing.T) {
	t.Parallel()

	srv := server.NewBaseHTTPServer("tls-server", 0, nil, time.Second, time.Second)
	srv.WithTLS("cert.pem", "key.pem")
}
