// schemaversion.go SEC-0020/NEW-DEPLOY-02: exact_schema_version readiness
// check (kubernetes-deployment-contract probes.readiness[1]).
//
// Expected version is computed once at startup from the in-image migrations directory (immutable
// image content aligned with COPY db/migrations /usr/share/yearning/db-migrations in
// docker/release/Dockerfile); directory parsing reuses internal/migration/schema.LoadDir (sharing
// filename contract, version contiguity, and SHA-256 rules without duplication).
//
// Check semantics: the highest successfully applied (success=true) version in schema_migrations
// must equal the expected version exactly — skipping the schema Job during release, stopping at an
// older migration version, or running an older image against a newer schema all report not ready
// (fail-closed). Error messages include both expected and actual versions.
package main

import (
	"context"
	"fmt"
	"os"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/migration/schema"
)

// defaultSchemaMigrationsDir is the in-image migrations directory (Dockerfile COPY path).
const defaultSchemaMigrationsDir = "/usr/share/yearning/db-migrations"

// schemaMigrationsDirEnv is the env var overriding the migrations directory (non-image runs can point to the repo dir).
const schemaMigrationsDirEnv = "YEARNING_SCHEMA_MIGRATIONS_DIR"

// schemaExpectation holds the expected schema version computed once at startup.
type schemaExpectation struct {
	dir      string
	expected int64
	err      error // directory missing/empty/violating filename contract; mounted and always fails in production
}

// computeSchemaExpectation computes the highest expected version from the migrations directory.
// Reuses schema.LoadDir: non-migration files, violations of the NNNNNN_name.up.sql contract, or
// versions not contiguous from 1 all return an error (fail-closed, never a silent max).
func computeSchemaExpectation() schemaExpectation {
	dir := os.Getenv(schemaMigrationsDirEnv)
	if dir == "" {
		dir = defaultSchemaMigrationsDir
	}
	migrations, err := schema.LoadDir(dir)
	if err != nil {
		return schemaExpectation{dir: dir, err: err}
	}
	if len(migrations) == 0 {
		return schemaExpectation{dir: dir, err: fmt.Errorf("no *.up.sql migrations found in %s", dir)}
	}
	// LoadDir returns migrations in ascending version order, so the last element is the highest expected version.
	return schemaExpectation{dir: dir, expected: migrations[len(migrations)-1].Version}
}

// schemaLedgerReader abstracts reading the schema_migrations ledger (stubbed in unit tests; gorm implementation below).
type schemaLedgerReader interface {
	// MaxAppliedVersion returns the highest successfully applied (success=true) version, or 0 for an empty ledger.
	MaxAppliedVersion(ctx context.Context) (int64, error)
}

// gormSchemaLedger is the gorm-backed reader for the schema_migrations ledger.
type gormSchemaLedger struct {
	db *gorm.DB
}

func (g gormSchemaLedger) MaxAppliedVersion(ctx context.Context) (int64, error) {
	var applied int64
	if err := g.db.WithContext(ctx).Raw(
		`select coalesce(max(version), 0) from schema_migrations where success`).Scan(&applied).Error; err != nil {
		return 0, fmt.Errorf("schema ledger unreadable: %w", err)
	}
	return applied, nil
}

// checkSchemaVersionExact performs the exact_schema_version readiness check: ready only when
// the highest successful ledger version equals the image expected version; always fails when
// the expectation is unavailable (directory missing/empty/invalid) — fail-closed in production
// (not mounted in development; see tiering in runServe).
func checkSchemaVersionExact(ctx context.Context, ledger schemaLedgerReader, exp schemaExpectation) error {
	if exp.err != nil {
		return fmt.Errorf("expected schema version unavailable (migrations dir %q): %w", exp.dir, exp.err)
	}
	actual, err := ledger.MaxAppliedVersion(ctx)
	if err != nil {
		return err
	}
	if actual != exp.expected {
		return fmt.Errorf(
			"schema version mismatch: ledger max applied=%d, expected=%d (migrations dir %q); schema migration job incomplete or image/ledger drift",
			actual, exp.expected, exp.dir)
	}
	return nil
}
