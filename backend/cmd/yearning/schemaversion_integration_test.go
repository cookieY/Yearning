//go:build integration

// schemaversion_integration_test.go SEC-0020 real PG verification (-tags integration,
// executed during main agent final verification; skipped in unit tests). Read-only probe: runs
// the same SQL as MaxAppliedVersion against the integration DB schema_migrations ledger to verify
// syntax and coalesce(max(version),0) semantics on real PG without writing to the ledger or modifying data.
package main

import (
	"context"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
)

// integrationLedgerDSN reuses the global integration DB convention via testenv.
func integrationLedgerDSN(t *testing.T) string {
	t.Helper()
	return testenv.MetaDSN(t)
}

func TestGormSchemaLedgerAgainstRealPostgres(t *testing.T) {
	db, err := gorm.Open(postgres.Open(integrationLedgerDSN(t)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open integration postgres: %v", err)
	}
	var ledgerExists bool
	if err := db.Raw(`select to_regclass('public.schema_migrations') is not null`).Scan(&ledgerExists).Error; err != nil {
		t.Fatalf("probe schema_migrations: %v", err)
	}
	if !ledgerExists {
		testenv.SkipOrFatal(t, "schema_migrations absent in integration DB; run schema migration job first")
	}
	var failed int
	if err := db.Raw(`select count(*) filter (where not success) from schema_migrations`).Scan(&failed).Error; err != nil {
		t.Fatalf("failed-row probe: %v", err)
	}
	got, err := gormSchemaLedger{db: db}.MaxAppliedVersion(context.Background())
	if err != nil {
		t.Fatalf("MaxAppliedVersion: %v", err)
	}
	if got < 0 {
		t.Fatalf("max applied version must be non-negative, got %d", got)
	}
	if failed > 0 {
		t.Logf("integration ledger carries %d failed rows (exact_schema_version would also fail on schema_ledger)", failed)
	}
}
