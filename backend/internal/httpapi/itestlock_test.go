// itestlock_test.go delegates cross-process shared database mutex locking to platform/testkit.
package httpapi

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

// acquireItestLock holds a process-reentrant exclusive file lock until the test finishes.
func acquireItestLock(t testing.TB, name string) {
	t.Helper()
	testkit.AcquireNamedLock(t, name)
}
