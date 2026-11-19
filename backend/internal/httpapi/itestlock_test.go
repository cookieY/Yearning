// itestlock_test.go cross-process shared database mutex lock: when go test runs multiple
// packages in parallel, httpapi (b3/b6) and review/application fixtures both truncate shared tables
// and must serialize via flock to avoid truncating each other.
package httpapi

import (
	"os"
	"syscall"
	"testing"
)

// acquireItestLock holds an exclusive file lock until the test finishes.
func acquireItestLock(t testing.TB, name string) {
	t.Helper()
	path := os.TempDir() + "/yearningx-itest-" + name + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		t.Fatalf("flock %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}
