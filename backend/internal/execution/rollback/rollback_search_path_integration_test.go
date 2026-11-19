//go:build integration

package rollback_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func acquireRollbackPGIntegrationLock(t *testing.T) {
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

func openConfiguredPGRollbackPool(t *testing.T) (database.Adapter, *database.PurposePool) {
	t.Helper()
	acquireRollbackPGIntegrationLock(t)
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	host := os.Getenv("YEARNING_TEST_DATABASE_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := 54332
	if rawPort := os.Getenv("YEARNING_TEST_DATABASE_PORT"); rawPort != "" {
		if p, err := strconv.Atoi(rawPort); err == nil && p > 0 {
			port = p
		}
	}
	user := os.Getenv("YEARNING_TEST_DATABASE_USER")
	if user == "" {
		user = "yearning"
	}
	pass := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	dbName := os.Getenv("YEARNING_TEST_DATABASE_DBNAME")
	if dbName == "" {
		dbName = "yearning_b13_test"
	}

	if dsnEnv := os.Getenv("YEARNING_TEST_PG_DSN"); dsnEnv != "" {
		if u, err := url.Parse(dsnEnv); err == nil {
			if u.Hostname() != "" {
				host = u.Hostname()
			}
			if u.Port() != "" {
				if p, perr := strconv.Atoi(u.Port()); perr == nil && p > 0 {
					port = p
				}
			}
			if u.User != nil {
				if u.User.Username() != "" {
					user = u.User.Username()
				}
				if p, ok := u.User.Password(); ok && p != "" {
					pass = p
				}
			}
			if trimmed := strings.TrimPrefix(u.Path, "/"); trimmed != "" {
				dbName = trimmed
			}
		}
	}
	if pass == "" {
		t.Skip("PostgreSQL test password not configured (set YEARNING_TEST_DATABASE_PASSWORD or YEARNING_TEST_PG_DSN)")
	}

	ep := database.Endpoint{
		Host:             host,
		Port:             port,
		DatabaseName:     dbName,
		CompatibilityMod: "postgresql",
	}
	opts := database.AdapterOptions{
		CapabilityResolver: datasource.ResolveCapabilities,
		SchemaContext:      "public",
	}
	adapter := database.NewPostgreSQLAdapter(ep, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := adapter.OpenPurposePool(ctx, database.PurposeExecution, database.Credential{
		Username: user,
		Password: pass,
	}, nil)
	if err != nil {
		t.Skipf("PostgreSQL test DB unreachable (%v)", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return adapter, pool
}

func TestPostgresRollbackExplicitSchemaAndSearchPathResolution(t *testing.T) {
	adapter, pool := openConfiguredPGRollbackPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	appSchema := "rb_sp_app_" + suffix
	pubSchema := "rb_sp_pub_" + suffix

	execDDLOrFail(t, ctx, adapter, pool, "public", fmt.Sprintf(`CREATE SCHEMA "%s"`, appSchema))
	execDDLOrFail(t, ctx, adapter, pool, "public", fmt.Sprintf(`CREATE SCHEMA "%s"`, pubSchema))
	t.Cleanup(func() {
		_, _ = adapter.ExecuteDDLStatement(context.Background(), pool, "public", fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, appSchema))
		_, _ = adapter.ExecuteDDLStatement(context.Background(), pool, "public", fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, pubSchema))
	})

	// Seed pubSchema with orders, missing_tbl, and shadow_tbl; seed appSchema with t1 and a shadowing view shadow_tbl.
	execDDLOrFail(t, ctx, adapter, pool, pubSchema, fmt.Sprintf(`CREATE TABLE "%s"."orders" ("id" INT PRIMARY KEY, "v" INT NOT NULL)`, pubSchema))
	execDDLOrFail(t, ctx, adapter, pool, pubSchema, fmt.Sprintf(`CREATE TABLE "%s"."missing_tbl" ("id" INT PRIMARY KEY, "v" INT NOT NULL)`, pubSchema))
	execDDLOrFail(t, ctx, adapter, pool, pubSchema, fmt.Sprintf(`CREATE TABLE "%s"."shadow_tbl" ("id" INT PRIMARY KEY, "v" INT NOT NULL)`, pubSchema))
	execDDLOrFail(t, ctx, adapter, pool, appSchema, fmt.Sprintf(`CREATE TABLE "%s"."t1" ("id" INT PRIMARY KEY)`, appSchema))
	execDDLOrFail(t, ctx, adapter, pool, appSchema, fmt.Sprintf(`CREATE VIEW "%s"."shadow_tbl" AS SELECT "id", "v" FROM "%s"."shadow_tbl"`, appSchema, pubSchema))

	txSeed, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction seed: %v", err)
	}
	if _, err := adapter.ExecuteDMLStatement(ctx, txSeed, database.DMLStatement{
		SQL: fmt.Sprintf(`INSERT INTO "%s"."missing_tbl" ("id", "v") VALUES (1, 10)`, pubSchema),
	}); err != nil {
		t.Fatalf("seed missing_tbl: %v", err)
	}
	if _, err := adapter.CommitDMLTransaction(ctx, txSeed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	spCtx := database.WithRollbackSearchPath(ctx, []string{appSchema, pubSchema})

	// 1. Explicit CREATE TABLE appSchema.orders must NOT match pubSchema.orders.
	parsedCreateExp, err := rollback.AnalyzeStatement("postgresql", pubSchema, fmt.Sprintf(`CREATE TABLE "%s"."orders" ("id" INT PRIMARY KEY)`, appSchema))
	if err != nil {
		t.Fatalf("AnalyzeStatement explicit CREATE TABLE: %v", err)
	}
	metaCreateExp, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedCreateExp)
	if err != nil {
		t.Fatalf("PrepareDDLPreExecution explicit CREATE TABLE unexpectedly failed: %v", err)
	}
	if metaCreateExp.Exists || parsedCreateExp.SchemaName != appSchema {
		t.Fatalf("explicit CREATE TABLE resolved wrong target: exists=%v schema=%q", metaCreateExp.Exists, parsedCreateExp.SchemaName)
	}

	// 2. Unqualified CREATE TABLE orders with creation schema appSchema must NOT match pubSchema.orders.
	parsedCreateUnq, err := rollback.AnalyzeStatement("postgresql", appSchema, `CREATE TABLE orders (id INT PRIMARY KEY)`)
	if err != nil {
		t.Fatalf("AnalyzeStatement unqualified CREATE TABLE: %v", err)
	}
	metaCreateUnq, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedCreateUnq)
	if err != nil {
		t.Fatalf("PrepareDDLPreExecution unqualified CREATE TABLE unexpectedly failed: %v", err)
	}
	if metaCreateUnq.Exists || parsedCreateUnq.SchemaName != appSchema {
		t.Fatalf("unqualified CREATE TABLE resolved wrong target: exists=%v schema=%q", metaCreateUnq.Exists, parsedCreateUnq.SchemaName)
	}

	// 3. Explicit ALTER TABLE appSchema.t1 RENAME TO orders must NOT collide with pubSchema.orders.
	parsedRename, err := rollback.AnalyzeStatement("postgresql", pubSchema, fmt.Sprintf(`ALTER TABLE "%s"."t1" RENAME TO "orders"`, appSchema))
	if err != nil {
		t.Fatalf("AnalyzeStatement explicit RENAME TABLE: %v", err)
	}
	if _, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedRename); err != nil {
		t.Fatalf("PrepareDDLPreExecution explicit RENAME TABLE unexpectedly failed: %v", err)
	}
	if parsedRename.SchemaName != appSchema || parsedRename.PostSchemaName != appSchema {
		t.Fatalf("explicit RENAME TABLE schema drift: schema=%q postSchema=%q", parsedRename.SchemaName, parsedRename.PostSchemaName)
	}

	// 4. Explicit UPDATE appSchema.missing_tbl must fail closed instead of hijacking pubSchema.missing_tbl.
	txExpDML, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction explicit DML: %v", err)
	}
	if _, err := adapter.ExecuteDMLStatement(ctx, txExpDML, database.DMLStatement{
		SQL: fmt.Sprintf(`SET LOCAL search_path = "%s", "%s"`, appSchema, pubSchema),
	}); err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txExpDML)
		t.Fatalf("SET LOCAL search_path: %v", err)
	}
	parsedUpdExp, err := rollback.AnalyzeStatement("postgresql", appSchema, fmt.Sprintf(`UPDATE "%s"."missing_tbl" SET v = 99 WHERE id = 1`, appSchema))
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txExpDML)
		t.Fatalf("AnalyzeStatement explicit UPDATE: %v", err)
	}
	_, _, _, _, err = rollback.ExecuteAndCapturePostgresDML(ctx, adapter, pool, txExpDML, 1, parsedUpdExp, 100)
	_, _ = adapter.RollbackDMLTransaction(ctx, txExpDML)
	var capErr *rollback.CapabilityError
	if !errors.As(err, &capErr) || capErr.Reason != "target_table_not_found" {
		t.Fatalf("expected target_table_not_found for explicit missing schema table, got %v", err)
	}

	// 5. Unqualified UPDATE shadow_tbl when shadowed by a view in appSchema must fail closed.
	txShadow, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction shadow view: %v", err)
	}
	if _, err := adapter.ExecuteDMLStatement(ctx, txShadow, database.DMLStatement{
		SQL: fmt.Sprintf(`SET LOCAL search_path = "%s", "%s"`, appSchema, pubSchema),
	}); err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txShadow)
		t.Fatalf("SET LOCAL search_path shadow: %v", err)
	}
	parsedShadow, err := rollback.AnalyzeStatement("postgresql", appSchema, `UPDATE shadow_tbl SET v = 99 WHERE id = 1`)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txShadow)
		t.Fatalf("AnalyzeStatement shadow UPDATE: %v", err)
	}
	_, _, _, _, err = rollback.ExecuteAndCapturePostgresDML(ctx, adapter, pool, txShadow, 1, parsedShadow, 100)
	_, _ = adapter.RollbackDMLTransaction(ctx, txShadow)
	if !errors.As(err, &capErr) || capErr.Reason != "target_table_not_found" {
		t.Fatalf("expected target_table_not_found when view shadows table on search_path, got %v", err)
	}

	// 6. Unqualified UPDATE missing_tbl with search_path = appSchema, pubSchema resolves pubSchema.missing_tbl.
	txUnqDML, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction unqualified DML: %v", err)
	}
	if _, err := adapter.ExecuteDMLStatement(ctx, txUnqDML, database.DMLStatement{
		SQL: fmt.Sprintf(`SET LOCAL search_path = "%s", "%s"`, appSchema, pubSchema),
	}); err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txUnqDML)
		t.Fatalf("SET LOCAL search_path unqualified: %v", err)
	}
	parsedUpdUnq, err := rollback.AnalyzeStatement("postgresql", appSchema, `UPDATE missing_tbl SET v = 42 WHERE id = 1`)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txUnqDML)
		t.Fatalf("AnalyzeStatement unqualified UPDATE: %v", err)
	}
	resUnq, evUnq, _, _, err := rollback.ExecuteAndCapturePostgresDML(ctx, adapter, pool, txUnqDML, 1, parsedUpdUnq, 100)
	_, _ = adapter.RollbackDMLTransaction(ctx, txUnqDML)
	if err != nil || resUnq == nil || evUnq == nil {
		t.Fatalf("unqualified search_path UPDATE failed: res=%+v ev=%+v err=%v", resUnq, evUnq, err)
	}
	if parsedUpdUnq.SchemaName != pubSchema || evUnq.SchemaName != pubSchema {
		t.Fatalf("expected unqualified UPDATE to resolve %q, got parsed=%q ev=%q", pubSchema, parsedUpdUnq.SchemaName, evUnq.SchemaName)
	}

	// 7. DROP INDEX explicit vs unqualified search_path resolution.
	idxName := "idx_rb_sp_" + suffix
	execDDLOrFail(t, ctx, adapter, pool, pubSchema, fmt.Sprintf(`CREATE INDEX "%s" ON "%s"."missing_tbl" ("v")`, idxName, pubSchema))

	parsedDropIdxWrongExp, err := rollback.AnalyzeStatement("postgresql", appSchema, fmt.Sprintf(`DROP INDEX "%s"."%s"`, appSchema, idxName))
	if err != nil {
		t.Fatalf("AnalyzeStatement explicit wrong DROP INDEX: %v", err)
	}
	if _, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedDropIdxWrongExp); !errors.As(err, &capErr) || capErr.Reason != "drop_index_target_not_found" {
		t.Fatalf("expected drop_index_target_not_found for explicit wrong-schema DROP INDEX, got %v", err)
	}

	parsedDropIdxRightExp, err := rollback.AnalyzeStatement("postgresql", appSchema, fmt.Sprintf(`DROP INDEX "%s"."%s"`, pubSchema, idxName))
	if err != nil {
		t.Fatalf("AnalyzeStatement explicit right DROP INDEX: %v", err)
	}
	if _, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedDropIdxRightExp); err != nil {
		t.Fatalf("PrepareDDLPreExecution explicit right DROP INDEX failed: %v", err)
	}
	if parsedDropIdxRightExp.SchemaName != pubSchema || parsedDropIdxRightExp.TableName != "missing_tbl" {
		t.Fatalf("explicit right DROP INDEX resolved %s.%s", parsedDropIdxRightExp.SchemaName, parsedDropIdxRightExp.TableName)
	}

	parsedDropIdxUnq, err := rollback.AnalyzeStatement("postgresql", "", fmt.Sprintf(`DROP INDEX "%s"`, idxName))
	if err != nil {
		t.Fatalf("AnalyzeStatement unqualified DROP INDEX: %v", err)
	}
	if _, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedDropIdxUnq); err != nil {
		t.Fatalf("PrepareDDLPreExecution unqualified DROP INDEX failed: %v", err)
	}
	if parsedDropIdxUnq.SchemaName != pubSchema || parsedDropIdxUnq.TableName != "missing_tbl" {
		t.Fatalf("unqualified DROP INDEX resolved %s.%s, want %s.missing_tbl", parsedDropIdxUnq.SchemaName, parsedDropIdxUnq.TableName, pubSchema)
	}

	// 8. Unqualified ALTER TABLE missing_tbl RENAME TO missing_tbl_v2 resolves schema & postSchema via search_path.
	parsedUnqRename, err := rollback.AnalyzeStatement("postgresql", appSchema, `ALTER TABLE missing_tbl RENAME TO missing_tbl_v2`)
	if err != nil {
		t.Fatalf("AnalyzeStatement unqualified RENAME TABLE: %v", err)
	}
	if _, err := rollback.PrepareDDLPreExecution(spCtx, pool, "postgresql", parsedUnqRename); err != nil {
		t.Fatalf("PrepareDDLPreExecution unqualified RENAME TABLE failed: %v", err)
	}
	if parsedUnqRename.SchemaName != pubSchema || parsedUnqRename.PostSchemaName != pubSchema {
		t.Fatalf("unqualified RENAME TABLE schema=%q postSchema=%q, want both %q", parsedUnqRename.SchemaName, parsedUnqRename.PostSchemaName, pubSchema)
	}
}
