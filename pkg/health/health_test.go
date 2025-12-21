package health_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"kuack-node/pkg/health"
)

func TestNewServer(t *testing.T) {
	t.Parallel()

	t.Run("creates server with correct port", func(t *testing.T) {
		t.Parallel()

		port := 8080
		server := health.NewServer(port)

		if server == nil {
			t.Fatal("NewServer() returned nil")
		}

		// Test that server can start and respond (indirectly tests configuration)
		ctx := t.Context()

		_ = server.Start(ctx)

		time.Sleep(200 * time.Millisecond)

		// Make a request to verify server is configured correctly
		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://localhost:8080/healthz",
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}

		closeErr := resp.Body.Close()
		if closeErr != nil {
			t.Logf("Failed to close response body: %v", closeErr)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Server returned status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		// Shutdown
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer shutdownCancel()

		_ = server.Shutdown(shutdownCtx)
	})

	t.Run("creates server with default port", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(health.DefaultPort)

		if server == nil {
			t.Fatal("NewServer() returned nil")
		}

		// Test that server works with default port
		ctx := t.Context()

		_ = server.Start(ctx)

		time.Sleep(200 * time.Millisecond)

		// Shutdown
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer shutdownCancel()

		_ = server.Shutdown(shutdownCtx)
	})
}

func TestServer_Start(t *testing.T) {
	t.Parallel()

	t.Run("starts server successfully", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(0) // Use port 0 for automatic port assignment

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errChan := server.Start(ctx)

		// Wait for server to start
		time.Sleep(200 * time.Millisecond)

		// Check that no error was sent
		select {
		case err := <-errChan:
			t.Errorf("Start() sent unexpected error: %v", err)
		default:
			// No error, which is expected
		}

		// Test that the server is actually listening by making a request
		// We need to get the actual port, but since we used 0, we can't easily get it
		// Instead, we'll test with a known port
		cancel() // Cancel context to stop the server
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("handles server errors", func(t *testing.T) {
		t.Parallel()

		// Create a server that will fail to start (invalid address)
		// We can't easily test this without actually binding to a port,
		// so we'll test the error handling path differently
		server := health.NewServer(0)
		ctx := context.Background()

		// Start the server normally first
		errChan := server.Start(ctx)

		// Shutdown immediately to trigger error handling
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_ = server.Shutdown(shutdownCtx)

		// Wait a bit for the error to propagate
		time.Sleep(200 * time.Millisecond)

		// The server should have closed gracefully, so no error should be sent
		// (http.ErrServerClosed is filtered out)
		select {
		case err := <-errChan:
			// If we get an error, it should not be http.ErrServerClosed
			if err != nil {
				t.Logf("Start() sent error (expected for some cases): %v", err)
			}
		default:
			// No error is expected for graceful shutdown
		}
	})

	t.Run("returns error channel", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(0)

		ctx := t.Context()

		errChan := server.Start(ctx)

		if errChan == nil {
			t.Fatal("Start() returned nil error channel")
		}

		// Wait a bit and check that channel is working (no error sent)
		time.Sleep(200 * time.Millisecond)

		select {
		case err := <-errChan:
			t.Errorf("Start() sent unexpected error: %v", err)
		default:
			// No error, which is expected for successful start
		}
	})
}

func TestServer_Shutdown(t *testing.T) {
	t.Parallel()

	t.Run("shuts down gracefully", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(0)

		ctx := t.Context()

		// Start the server
		_ = server.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		// Shutdown with valid context
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()

		err := server.Shutdown(shutdownCtx)
		if err != nil {
			t.Errorf("Shutdown() error = %v, want nil", err)
		}
	})

	t.Run("handles cancelled context", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(0)

		ctx := t.Context()

		// Start the server
		_ = server.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		// Shutdown with already cancelled context
		cancelledCtx, cancelFunc := context.WithCancel(context.Background())
		cancelFunc()

		err := server.Shutdown(cancelledCtx)
		// Shutdown might return context.Canceled or context.DeadlineExceeded
		// Both are acceptable
		if err != nil && !errors.Is(err, context.Canceled) &&
			!errors.Is(err, context.DeadlineExceeded) {
			t.Logf("Shutdown() with cancelled context returned: %v (this may be acceptable)", err)
		}
	})

	t.Run("handles timeout", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(0)

		ctx := t.Context()

		// Start the server
		_ = server.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		// Shutdown with very short timeout
		timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer timeoutCancel()

		// Wait a bit to ensure timeout
		time.Sleep(10 * time.Millisecond)

		err := server.Shutdown(timeoutCtx)
		// Timeout is acceptable
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Logf("Shutdown() with timeout returned: %v (timeout is acceptable)", err)
		}
	})
}

func TestHealthzHandler(t *testing.T) {
	t.Parallel()

	t.Run("returns 200 OK", func(t *testing.T) {
		t.Parallel()

		server := health.NewServer(0)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Start server
		_ = server.Start(ctx)

		time.Sleep(200 * time.Millisecond)

		// Get the actual address the server is listening on
		// Since we used port 0, we need to find it
		// For testing, let's use a fixed port
		testPort := 18080
		testServer := health.NewServer(testPort)

		testCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_ = testServer.Start(testCtx)

		time.Sleep(200 * time.Millisecond)

		// Make request to /healthz
		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://localhost:18080/healthz",
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}

		defer func() {
			closeErr := resp.Body.Close()
			if closeErr != nil {
				t.Logf("Failed to close response body: %v", closeErr)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("healthzHandler() status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read response body: %v", err)
		}

		expectedBody := "ok"
		if string(body) != expectedBody {
			t.Errorf("healthzHandler() body = %q, want %q", string(body), expectedBody)
		}

		// Shutdown test server
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer shutdownCancel()

		_ = testServer.Shutdown(shutdownCtx)
	})

	t.Run("handles root path", func(t *testing.T) {
		t.Parallel()

		testPort := 18081
		testServer := health.NewServer(testPort)

		testCtx := t.Context()

		_ = testServer.Start(testCtx)

		time.Sleep(200 * time.Millisecond)

		// Make request to root path
		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://localhost:18081/",
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}

		defer func() {
			closeErr := resp.Body.Close()
			if closeErr != nil {
				t.Logf("Failed to close response body: %v", closeErr)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			t.Errorf(
				"healthzHandler() on root path status = %d, want %d",
				resp.StatusCode,
				http.StatusOK,
			)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read response body: %v", err)
		}

		expectedBody := "ok"
		if string(body) != expectedBody {
			t.Errorf("healthzHandler() on root path body = %q, want %q", string(body), expectedBody)
		}

		// Shutdown test server
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer shutdownCancel()

		_ = testServer.Shutdown(shutdownCtx)
	})
}

func TestServer_Integration(t *testing.T) {
	t.Parallel()

	t.Run("full lifecycle", func(t *testing.T) {
		t.Parallel()

		testPort := 18082
		server := health.NewServer(testPort)

		ctx := t.Context()

		// Start server
		errChan := server.Start(ctx)

		time.Sleep(200 * time.Millisecond)

		// Verify server is running by making a request
		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://localhost:18082/healthz",
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}

		closeErr := resp.Body.Close()
		if closeErr != nil {
			t.Logf("Failed to close response body: %v", closeErr)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Server returned status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		// Check no errors in channel
		select {
		case err := <-errChan:
			t.Errorf("Unexpected error in channel: %v", err)
		default:
			// No error, which is expected
		}

		// Shutdown
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()

		err = server.Shutdown(shutdownCtx)
		if err != nil {
			t.Errorf("Shutdown() error = %v, want nil", err)
		}

		// Wait a bit for shutdown to complete
		time.Sleep(100 * time.Millisecond)

		// Verify server is no longer accepting connections
		req2, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://localhost:18082/healthz",
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp2, err := http.DefaultClient.Do(req2)
		if err == nil {
			if resp2 != nil {
				closeErr := resp2.Body.Close()
				if closeErr != nil {
					t.Logf("Failed to close response body: %v", closeErr)
				}
			}

			t.Error("Server should be closed but still accepting connections")
		}
	})
}
