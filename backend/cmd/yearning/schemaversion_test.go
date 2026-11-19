// schemaversion_test.go SEC-0020/NEW-DEPLOY-02 regression: expected version calculation
// and ledger comparison semantics for the exact_schema_version readiness check. For the real PG path,
// see schemaversion_integration_test.go (-tags integration, not run during unit tests).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMigrations creates migration files matching the contract format (NNNNNN_name.up.sql).
func writeMigrations(t *testing.T, dir string, versions []int, extraFiles ...string) {
	t.Helper()
	for _, v := range versions {
		name := filepath.Join(dir, fmt.Sprintf("%06d_name.up.sql", v))
		if err := os.WriteFile(name, []byte("select 1;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range extraFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComputeSchemaExpectationFromDir(t *testing.T) {
	dir := t.TempDir()
	writeMigrations(t, dir, []int{1, 2, 3}, "README.md")
	t.Setenv("YEARNING_SCHEMA_MIGRATIONS_DIR", dir)
	exp := computeSchemaExpectation()
	if exp.err != nil {
		t.Fatalf("valid migrations dir must compute expectation: %v", exp.err)
	}
	if exp.expected != 3 {
		t.Fatalf("expected max version 3, got %d", exp.expected)
	}
	if exp.dir != dir {
		t.Fatalf("expectation must carry the resolved dir: %q", exp.dir)
	}
}

func TestComputeSchemaExpectationDefaultsToImageDir(t *testing.T) {
	t.Setenv("YEARNING_SCHEMA_MIGRATIONS_DIR", "")
	// Default path does not exist on developer machines: must report a directory error rather than panicking or silently returning 0.
	exp := computeSchemaExpectation()
	if exp.err == nil {
		t.Fatalf("missing default dir must error, got expected=%d dir=%q", exp.expected, exp.dir)
	}
	if exp.dir != "/usr/share/yearning/db-migrations" {
		t.Fatalf("default dir must match release image COPY path, got %q", exp.dir)
	}
}

func TestComputeSchemaExpectationRejectsBadDirs(t *testing.T) {
	t.Run("missing dir", func(t *testing.T) {
		t.Setenv("YEARNING_SCHEMA_MIGRATIONS_DIR", filepath.Join(t.TempDir(), "absent"))
		if exp := computeSchemaExpectation(); exp.err == nil {
			t.Fatal("missing dir must error")
		}
	})
	t.Run("empty dir", func(t *testing.T) {
		t.Setenv("YEARNING_SCHEMA_MIGRATIONS_DIR", t.TempDir())
		if exp := computeSchemaExpectation(); exp.err == nil {
			t.Fatal("empty dir must error")
		}
	})
	t.Run("contract-violating filename", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrations(t, dir, []int{1})
		if err := os.WriteFile(filepath.Join(dir, "0002_weird.UP.SQL"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if exp := computeSchemaExpectation(); exp.err == nil {
			t.Fatal("unexpected file in migrations dir must error (fail-closed)")
		}
	})
	t.Run("version gap", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrations(t, dir, []int{1, 3})
		if exp := computeSchemaExpectation(); exp.err == nil {
			t.Fatal("non-contiguous versions must error (LoadDir contract)")
		}
	})
}

// stubLedger is a test stub for schemaLedgerReader.
type stubLedger struct {
	version int64
	err     error
}

func (s stubLedger) MaxAppliedVersion(context.Context) (int64, error) {
	return s.version, s.err
}

func TestCheckSchemaVersionExactSemantics(t *testing.T) {
	exp := schemaExpectation{dir: "/img/migrations", expected: 19}

	if err := checkSchemaVersionExact(context.Background(), stubLedger{version: 19}, exp); err != nil {
		t.Fatalf("ledger at expected version must be ready: %v", err)
	}
	// Stale ledger (migration stopped at an older version or schema Job was skipped): not ready.
	err := checkSchemaVersionExact(context.Background(), stubLedger{version: 18}, exp)
	if err == nil {
		t.Fatal("ledger behind expected version must not be ready")
	}
	for _, want := range []string{"18", "19"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("mismatch error must carry expected and actual versions (missing %q): %v", want, err)
		}
	}
	// Ledger ahead of image (old image rolled back onto a database with a newer schema): not ready.
	if err := checkSchemaVersionExact(context.Background(), stubLedger{version: 20}, exp); err == nil {
		t.Fatal("ledger ahead of image migrations must not be ready")
	}
	// Empty ledger: not ready.
	if err := checkSchemaVersionExact(context.Background(), stubLedger{version: 0}, exp); err == nil {
		t.Fatal("empty ledger must not be ready")
	}
	// Unreadable ledger: propagate error.
	if err := checkSchemaVersionExact(context.Background(), stubLedger{err: errors.New("relation missing")}, exp); err == nil {
		t.Fatal("ledger read error must propagate")
	}
}

// TestCheckSchemaVersionExactFailClosedOnDirError: when the migrations directory is unavailable
// in production, the check always fails (fail-closed) and includes the directory path in the error.
func TestCheckSchemaVersionExactFailClosedOnDirError(t *testing.T) {
	exp := schemaExpectation{dir: "/img/migrations", err: errors.New("no *.up.sql migrations found")}
	err := checkSchemaVersionExact(context.Background(), stubLedger{version: 19}, exp)
	if err == nil {
		t.Fatal("unavailable expectation must fail readiness")
	}
	if !strings.Contains(err.Error(), "/img/migrations") {
		t.Fatalf("error must name the migrations dir: %v", err)
	}
}
