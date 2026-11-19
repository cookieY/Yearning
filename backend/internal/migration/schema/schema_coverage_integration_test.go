//go:build integration

package schema

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func acquireSchemaIntegrationLock(t *testing.T) {
	t.Helper()
	for _, name := range []string{"/yearningx-itest-shared.lock", "/yearningx-b13-assistant-test.lock"} {
		f, err := os.OpenFile(os.TempDir()+name, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("open lock %s: %v", name, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			t.Fatalf("flock %s: %v", name, err)
		}
		file := f
		t.Cleanup(func() {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
		})
	}
}

func TestSchemaMigratorLiveIntegration(t *testing.T) {
	acquireSchemaIntegrationLock(t)

	dsn := os.Getenv("YEARNING_TEST_PG_DSN")
	if dsn == "" {
		pw := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
		if pw == "" {
			t.Skip("PostgreSQL test DSN not configured")
		}
		host := os.Getenv("YEARNING_TEST_DATABASE_HOST")
		if host == "" {
			host = "127.0.0.1"
		}
		port := os.Getenv("YEARNING_TEST_DATABASE_PORT")
		if port == "" {
			port = "54332"
		}
		user := os.Getenv("YEARNING_TEST_DATABASE_USER")
		if user == "" {
			user = "yearning"
		}
		dbName := os.Getenv("YEARNING_TEST_DATABASE_DBNAME")
		if dbName == "" {
			dbName = "yearning_b13_test"
		}
		dsn = "postgres://" + user + ":" + pw + "@" + host + ":" + port + "/" + dbName + "?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pgxpool: %v", err)
	}
	defer pool.Close()

	files, err := LoadDir("../../../../db/migrations")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	cat, err := LoadCatalogContract("../../../../api/contracts/postgresql-column-catalog.json")
	if err != nil {
		t.Fatalf("LoadCatalogContract: %v", err)
	}

	m := &Migrator{Pool: pool}
	// Ensure schema_migrations ledger is synced with the 28 applied files on the test database.
	if err := m.ensureLedger(ctx, files); err != nil {
		t.Fatalf("ensureLedger: %v", err)
	}
	if _, err := pool.Exec(ctx, `delete from schema_migrations where not success or version > $1`, int64(len(files))); err != nil {
		t.Fatalf("cleanup dirty ledger rows: %v", err)
	}
	for _, f := range files {
		if _, err := pool.Exec(ctx, `
			insert into schema_migrations (version, filename, sha256, started_at, finished_at, success)
			values ($1, $2, $3, now(), now(), true)
			on conflict (version) do update set filename = excluded.filename, sha256 = excluded.sha256, success = true, failure_name = null`,
			f.Version, f.Filename, f.SHA256); err != nil {
			t.Fatalf("seed ledger version %d: %v", f.Version, err)
		}
	}

	if _, err := m.Status(ctx, files); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if _, err := m.Preflight(ctx, files); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if _, err := m.Plan(ctx, files); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := m.Apply(ctx, files); err != nil {
		t.Fatalf("Apply (noop on applied DB): %v", err)
	}
	if _, err := m.Verify(ctx, cat); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Exercise Plan + Apply on a synthetic pending migration (version 29) and clean up afterward.
	synthVer := int64(len(files) + 1)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `drop table if exists _schema_cov_probe`)
		_, _ = pool.Exec(ctx, `delete from schema_migrations where version = $1`, synthVer)
	})
	filesPlus := append(append([]Migration(nil), files...), Migration{
		Version:    synthVer,
		Filename:   "000029_cov_probe.up.sql",
		SHA256:     strings.Repeat("a", 64),
		Statements: []string{"BEGIN", "CREATE TABLE IF NOT EXISTS _schema_cov_probe (id int primary key)", "DROP TABLE _schema_cov_probe", "COMMIT"},
	})
	if planRep, err := m.Plan(ctx, filesPlus); err != nil || len(planRep.Pending) != 1 {
		t.Fatalf("Plan pending synthetic migration: %+v (err=%v)", planRep, err)
	}
	if appRep, err := m.Apply(ctx, filesPlus); err != nil || !appRep.Success || len(appRep.Applied) != 1 {
		t.Fatalf("Apply pending synthetic migration: %+v (err=%v)", appRep, err)
	}

	// Exercise failure path on version 30 and clean up afterward.
	failVer := synthVer + 1
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `delete from schema_migrations where version = $1`, failVer)
	})
	filesFail := append(append([]Migration(nil), filesPlus...), Migration{
		Version:    failVer,
		Filename:   "000030_cov_fail.up.sql",
		SHA256:     strings.Repeat("b", 64),
		Statements: []string{"SELECT * FROM _nonexistent_table_for_cov_fail"},
	})
	if _, err := m.Apply(ctx, filesFail); err == nil {
		t.Fatal("expected error on failing synthetic migration")
	}
	_, _ = pool.Exec(ctx, `delete from schema_migrations where version >= $1`, synthVer)
}
