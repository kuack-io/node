package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"kuack-node/pkg/app"
	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRunNodeController(t *testing.T) {
	t.Parallel()

	// Setup
	p, err := provider.NewWASMProvider("test-node")
	require.NoError(t, err)

	kubeClient := fake.NewClientset()
	started := make(chan struct{}, 1)

	kubeClient.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
		select {
		case started <- struct{}{}:
		default:
		}

		return false, nil, nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Run in a goroutine
	errChan := make(chan error, 1)

	go func() {
		errChan <- app.RunNodeController(ctx, p, kubeClient)
	}()

	// Wait for controller to start watching pods
	select {
	case <-started:
		// Controller has started
	case <-time.After(5 * time.Second):
		require.Fail(t, "Timed out waiting for controller to start watching pods")
	}

	// Cancel context to stop it
	cancel()

	// Should return nil (or context canceled error depending on implementation)
	select {
	case err := <-errChan:
		// node.PodController.Run returns nil when context is canceled usually
		// But RunNodeController wraps it.
		if err != nil {
			// Check if error is context.Canceled or wraps it
			if !errors.Is(err, context.Canceled) && err.Error() != "node controller failed: context canceled" {
				// Sometimes it might be wrapped differently or just string match if not properly wrapped
				// But let's try ErrorIs first.
				// If RunNodeController returns "node controller failed: %w", and the cause is context.Canceled.
				require.ErrorIs(t, err, context.Canceled)
			}
		}
	case <-time.After(1 * time.Second):
		require.Fail(t, "RunNodeController did not return after context cancellation")
	}
}
