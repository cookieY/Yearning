package testkit

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
)

type reentrantLockState struct {
	mu   sync.Mutex
	file *os.File
	refs int
}

var (
	namedLocksMu sync.Mutex
	namedLocks   = map[string]*reentrantLockState{}
)

func lockStateFor(path string) *reentrantLockState {
	namedLocksMu.Lock()
	defer namedLocksMu.Unlock()
	st, ok := namedLocks[path]
	if !ok {
		st = &reentrantLockState{}
		namedLocks[path] = st
	}
	return st
}

// AcquireLockPath acquires a process-reentrant reference-counted exclusive flock on path
// and schedules its release in t.Cleanup. Nested calls within the same test process
// increment the reference count without re-flocking a second file descriptor (avoiding
// intra-process self-deadlock), while separate `go test` package binaries serialize cleanly.
func AcquireLockPath(t testing.TB, path string) {
	t.Helper()
	st := lockStateFor(path)
	st.mu.Lock()
	if st.refs == 0 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			st.mu.Unlock()
			t.Fatalf("testkit: open lock %s: %v", path, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			st.mu.Unlock()
			t.Fatalf("testkit: flock %s: %v", path, err)
		}
		st.file = f
	}
	st.refs++
	st.mu.Unlock()

	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			st.mu.Lock()
			defer st.mu.Unlock()
			st.refs--
			if st.refs == 0 && st.file != nil {
				_ = syscall.Flock(int(st.file.Fd()), syscall.LOCK_UN)
				_ = st.file.Close()
				st.file = nil
			}
		})
	})
}

// AcquireNamedLock acquires a process-reentrant exclusive flock named `yearningx-itest-<name>.lock`
// inside os.TempDir() and releases it in t.Cleanup.
func AcquireNamedLock(t testing.TB, name string) {
	t.Helper()
	AcquireLockPath(t, filepath.Join(os.TempDir(), "yearningx-itest-"+name+".lock"))
}

// AcquireSharedLock acquires the canonical cross-package integration database flock
// (`yearningx-itest-shared.lock`) so parallel package binaries cannot race on TRUNCATE.
func AcquireSharedLock(t testing.TB) {
	t.Helper()
	AcquireNamedLock(t, "shared")
}

// PGDSN resolves the metadata PostgreSQL libpq DSN via testenv.MetaDSN (failing closed
// when YEARNING_REQUIRE_INTEGRATION=1).
func PGDSN(t testing.TB, opts ...testenv.Option) string {
	t.Helper()
	return testenv.MetaDSN(t, opts...)
}

// OpenPG resolves PGDSN, acquires AcquireSharedLock, opens a silent GORM PostgreSQL
// connection, and registers sql.DB closure before lock release via LIFO t.Cleanup.
func OpenPG(t testing.TB, opts ...testenv.Option) *gorm.DB {
	t.Helper()
	dsn := PGDSN(t, opts...)
	AcquireSharedLock(t)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		testenv.SkipOrFatal(t, "PostgreSQL test database not reachable (%v)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("testkit: postgres sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
