package app

import "testing"

// LockDepsForTesting serializes tests which modify package-level seams.
//
// It is intentionally only available in test builds so that black-box tests
// (package app_test) can avoid flakiness when other tests temporarily override
// the seam variables.
func LockDepsForTesting(t *testing.T) func() {
	t.Helper()

	return lockAppDeps(t)
}
