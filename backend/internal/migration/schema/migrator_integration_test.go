//go:build integration

package schema

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

func TestSchemaMigratorLiveIntegration(t *testing.T) {
	testkit.AcquireSharedLock(t)

	cfg := testenv.RequireMetaPG(t, testenv.WithDefaultPort(54332), testenv.WithDefaultDBName("yearning_b13_test"))

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.URLDSN())
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

	// Exercise Plan + Apply on a synthetic pending migration and clean up afterward.
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

	// Exercise failure path on the next synthetic version and clean up afterward.
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
	if _, err := pool.Exec(ctx, `delete from schema_migrations where version >= $1`, synthVer); err != nil {
		t.Fatalf("cleanup synthetic schema_migrations: %v", err)
	}
}

func TestDomainEventsActorKindMigration000031(t *testing.T) {
	testkit.AcquireSharedLock(t)

	cfg := testenv.RequireMetaPG(t, testenv.WithDefaultPort(54332), testenv.WithDefaultDBName("yearning_b13_test"))
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.URLDSN())
	if err != nil {
		t.Fatalf("connect pgxpool: %v", err)
	}
	defer pool.Close()

	files, err := LoadDir("../../../../db/migrations")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	var mig000031 *Migration
	for i := range files {
		if files[i].Version == 31 {
			mig000031 = &files[i]
			break
		}
	}
	if mig000031 == nil {
		t.Fatal("migration 000031 not found in db/migrations")
	}

	// Execute backfill and CHECK constraint verification inside an isolated rolled-back transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `alter table domain_events drop constraint if exists domain_events_actor_kind_nonempty`); err != nil {
		t.Fatalf("drop existing constraint: %v", err)
	}

	idMissing := uuid.NewString()
	idBlank := uuid.NewString()
	idSystem := uuid.NewString()
	corr := uuid.NewString()
	subject := "change-orders/" + uuid.NewString()

	for seq, tc := range []struct {
		id    string
		actor string
	}{
		{id: idMissing, actor: `{"user_id":"u-1","username":"alice"}`},
		{id: idBlank, actor: `{"kind":"   ","user_id":"u-2","username":"bob"}`},
		{id: idSystem, actor: `{"kind":"system","username":"system"}`},
	} {
		if _, err := tx.Exec(ctx, `
			insert into domain_events (id, event_type, subject, sequence, correlation_id, actor, event_data, occurred_at)
			values ($1, 'execution.started', $2, $3, $4, $5::jsonb, '{}'::jsonb, now())`,
			tc.id, subject, seq+1, corr, tc.actor); err != nil {
			t.Fatalf("seed legacy domain_event %s: %v", tc.id, err)
		}
	}

	// Execute migration 000031 body statements (excluding outer BEGIN/COMMIT).
	for _, stmt := range mig000031.Statements {
		upper := strings.ToUpper(strings.TrimSpace(stmt))
		if upper == "BEGIN" || upper == "COMMIT" {
			continue
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("execute 000031 statement %q: %v", stmt, err)
		}
	}

	for _, want := range []struct {
		id   string
		kind string
	}{
		{id: idMissing, kind: "user"},
		{id: idBlank, kind: "user"},
		{id: idSystem, kind: "system"},
	} {
		var got string
		if err := tx.QueryRow(ctx, `select actor->>'kind' from domain_events where id = $1`, want.id).Scan(&got); err != nil {
			t.Fatalf("query backfilled kind for %s: %v", want.id, err)
		}
		if got != want.kind {
			t.Fatalf("row %s actor.kind = %q, want %q", want.id, got, want.kind)
		}
	}

	// Verify CHECK constraint rejects missing, null, non-string, empty, and blank actor.kind with SQLSTATE 23514.
	for i, badActor := range []string{`{"user_id":"u-bad"}`, `{"kind":null}`, `{"kind":123}`, `{"kind":""}`, `{"kind":"  "}`} {
		if _, err := tx.Exec(ctx, `savepoint sp_check`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err := tx.Exec(ctx, `
			insert into domain_events (id, event_type, subject, sequence, correlation_id, actor, event_data, occurred_at)
			values ($1, 'execution.started', $2, $3, $4, $5::jsonb, '{}'::jsonb, now())`,
			uuid.NewString(), subject, 100+i, corr, badActor)
		if err == nil {
			t.Fatalf("expected CHECK violation for bad actor %s, got nil", badActor)
		}
		if !txutil.IsCheckViolation(err) {
			t.Fatalf("expected SQLSTATE 23514 check violation for %s, got: %v", badActor, err)
		}
		if _, err := tx.Exec(ctx, `rollback to savepoint sp_check`); err != nil {
			t.Fatalf("rollback savepoint: %v", err)
		}
	}
}
