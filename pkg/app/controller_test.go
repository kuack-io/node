package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"kuack-node/pkg/app"
	"kuack-node/pkg/provider"
	"kuack-node/pkg/registry"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRunNodeController(t *testing.T) {
	t.Parallel()

	// Setup
	// MockResolver
	mockResolver := new(MockResolver)
	mockResolver.On("ResolveWasmConfig", mock.Anything, mock.Anything).Return(&registry.WasmConfig{Type: "wasi", Path: "/test.wasm"}, nil)
	p, err := provider.NewWASMProvider("test-node", mockResolver)
	require.NoError(t, err)

	kubeClient := fake.NewClientset()

	// Set kubelet version (required before GetNode())
	err = p.SetKubeletVersionFromCluster(context.Background(), kubeClient)
	require.NoError(t, err)

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

type MockResolver struct {
	mock.Mock
}

func (m *MockResolver) ResolveWasmConfig(ctx context.Context, imageRef string) (*registry.WasmConfig, error) {
	args := m.Called(ctx, imageRef)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	cfg, ok := args.Get(0).(*registry.WasmConfig)
	if !ok {
		// This should not happen in tests unless mock is set up incorrectly
		panic("mock argument 0 is not *registry.WasmConfig")
	}

	return cfg, args.Error(1)
}
