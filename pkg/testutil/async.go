package testutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const defaultPollInterval = 10 * time.Millisecond

// WaitForCondition blocks until condition returns true or the timeout elapses.
// Tests should prefer this over time.Sleep to avoid racey assertions that depend on wall-clock delays.
func WaitForCondition(t *testing.T, timeout time.Duration, condition func() bool, msgAndArgs ...any) {
	t.Helper()

	require.Eventually(t, condition, timeout, defaultPollInterval, msgAndArgs...)
}

// WaitForSignal waits until the provided channel receives a value or the timeout expires.
func WaitForSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()

	select {
	case <-ch:
		return
	case <-time.After(timeout):
		require.Fail(t, msg)
	}
}
