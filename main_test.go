package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"kuack-node/pkg/app"
	"kuack-node/pkg/config"
)

var (
	mainDepsMu   sync.Mutex                 //nolint:gochecknoglobals // shared seam lock keeps global hook overrides deterministic across tests
	errRunFailed = errors.New("run failed") //nolint:gochecknoglobals // package-level sentinel lets exit assertions share a single comparable error
)

func lockMainDeps(t *testing.T) func() {
	t.Helper()

	mainDepsMu.Lock()

	origNotify := notifyContext
	origLoad := loadConfigFunc
	origRun := runAppFunc
	origExit := exitOnError

	return func() {
		notifyContext = origNotify
		loadConfigFunc = origLoad
		runAppFunc = origRun
		exitOnError = origExit

		mainDepsMu.Unlock()
	}
}

func stubNotifyContext() func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
	return func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())

		return ctx, cancel
	}
}

func TestMainRunsApplicationWithoutExit(t *testing.T) {
	t.Parallel()

	restore := lockMainDeps(t)
	t.Cleanup(restore)

	notifyContext = stubNotifyContext()
	loadConfigFunc = func(getenv config.EnvGetter) *config.Config {
		require.NotNil(t, getenv)

		return &config.Config{NodeName: "test-node"}
	}

	exitOnError = func(err error) {
		t.Fatalf("exitOnError called unexpectedly: %v", err)
	}

	var called bool

	runAppFunc = func(ctx context.Context, cfg *config.Config, runner app.NodeControllerRunner) error {
		called = true

		require.NotNil(t, ctx)
		require.Equal(t, "test-node", cfg.NodeName)
		require.Nil(t, runner)

		return nil
	}

	main()

	require.True(t, called)
}

func TestMainExitsWhenRunFails(t *testing.T) {
	t.Parallel()

	restore := lockMainDeps(t)
	t.Cleanup(restore)

	notifyContext = stubNotifyContext()
	loadConfigFunc = func(config.EnvGetter) *config.Config {
		return &config.Config{NodeName: "failing-node"}
	}

	runAppFunc = func(context.Context, *config.Config, app.NodeControllerRunner) error {
		return errRunFailed
	}

	exitCalled := make(chan error, 1)
	exitOnError = func(err error) {
		exitCalled <- err
	}

	main()

	select {
	case err := <-exitCalled:
		require.ErrorIs(t, err, errRunFailed)
	default:
		t.Fatal("exitOnError was not invoked")
	}
}
