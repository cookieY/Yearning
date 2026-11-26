//go:build integration

package rollback_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func openConfiguredPGRollbackPool(t *testing.T) (database.Adapter, *database.PurposePool) {
	t.Helper()
	testkit.AcquireSharedLock(t)
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	cfg := testenv.RequireMetaPG(t, testenv.WithDefaultPort(54332), testenv.WithDefaultDBName("yearning_b13_test"))

	ep := database.Endpoint{
		Host:             cfg.Host,
		Port:             cfg.Port,
		DatabaseName:     cfg.DBName,
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
		Username: cfg.User,
		Password: cfg.Password,
	}, nil)
	if err != nil {
		testenv.SkipOrFatal(t, "PostgreSQL test DB unreachable (%v)", err)
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
	_, _, _, _, err = rollback.ExecuteAndCapturePostgresDML(ctx, pool, txExpDML, 1, parsedUpdExp, 100)
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
	_, _, _, _, err = rollback.ExecuteAndCapturePostgresDML(ctx, pool, txShadow, 1, parsedShadow, 100)
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
	resUnq, evUnq, _, _, err := rollback.ExecuteAndCapturePostgresDML(ctx, pool, txUnqDML, 1, parsedUpdUnq, 100)
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
