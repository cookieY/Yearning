package testkit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func TestEnvOrAndKey32AndNewCrypter(t *testing.T) {
	const envKey = "YEARNING_TESTKIT_SAMPLE_ENV"
	t.Setenv(envKey, "")
	if got := EnvOr(envKey, "fallback-val"); got != "fallback-val" {
		t.Fatalf("EnvOr empty = %q, want fallback-val", got)
	}
	t.Setenv(envKey, "explicit-val")
	if got := EnvOr(envKey, "fallback-val"); got != "explicit-val" {
		t.Fatalf("EnvOr set = %q, want explicit-val", got)
	}

	k1 := Key32(1)
	k2 := Key32(2)
	if len(k1) != 32 || len(k2) != 32 || bytes.Equal(k1, k2) {
		t.Fatalf("Key32 must return distinct 32-byte slices")
	}

	c := NewCrypter(t)
	ref := crypto.ColumnRef{Table: "order_sql_payloads", PrimaryKey: "pk-1", Column: "sql_ciphertext"}
	env, err := c.SealColumn(crypto.PurposePayload, ref, []byte("SELECT 1"))
	if err != nil {
		t.Fatalf("SealColumn: %v", err)
	}
	plain, err := c.OpenColumn(crypto.PurposePayload, ref, env)
	if err != nil || string(plain) != "SELECT 1" {
		t.Fatalf("OpenColumn = %q err=%v", plain, err)
	}
	for _, hmacPurpose := range []string{
		crypto.HMACPurposeOIDCState,
		"identifier_token",
		"settings_impact_token",
		"sql_fingerprint",
		"cursor_affinity",
	} {
		if _, err := c.HMAC(hmacPurpose, []byte("probe")); err != nil {
			t.Fatalf("NewCrypter missing HMAC purpose %s: %v", hmacPurpose, err)
		}
	}
}

func TestAcquireLockPathProcessReentrantAndCrossProcessExclusive(t *testing.T) {
	if lockPath := os.Getenv("YEARNING_TESTKIT_SUBPROC_LOCK"); lockPath != "" {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			os.Exit(2)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			os.Exit(3) // Expected EWOULDBLOCK while parent holds lock.
		}
		os.Exit(0)
	}

	lockPath := filepath.Join(t.TempDir(), "reentrant.lock")
	t.Run("nested_reentrant_and_contended_by_child_process", func(t *testing.T) {
		AcquireLockPath(t, lockPath)
		// Second acquire in the same process must increment refcount without deadlocking.
		AcquireLockPath(t, lockPath)

		cmd := exec.Command(os.Args[0], "-test.run=^TestAcquireLockPathProcessReentrantAndCrossProcessExclusive$")
		cmd.Env = append(os.Environ(), "YEARNING_TESTKIT_SUBPROC_LOCK="+lockPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child process unexpectedly acquired held flock: err=%v out=%s", err, string(out))
		}
	})

	// After subtest t.Cleanup finishes, both refs must be released so a non-blocking flock succeeds.
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock after cleanup: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock remained held after refcounted t.Cleanup completed: %v", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
