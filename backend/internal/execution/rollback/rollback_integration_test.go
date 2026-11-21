package rollback_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func openMySQLTestPool(t *testing.T) (database.Adapter, *database.PurposePool, string) {
	t.Helper()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	host := envOr("YEARNING_ROLLBACK_TEST_MYSQL_HOST", "127.0.0.1")
	port := envIntOr("YEARNING_ROLLBACK_TEST_MYSQL_PORT", 54334)
	user := envOr("YEARNING_ROLLBACK_TEST_MYSQL_USER", "root")
	pass := envOr("YEARNING_ROLLBACK_TEST_MYSQL_PASSWORD", "f12-mysql-902d734b79df")
	dbName := envOr("YEARNING_ROLLBACK_TEST_MYSQL_DB", "demo")

	ep := database.Endpoint{
		Host:             host,
		Port:             port,
		DatabaseName:     dbName,
		CompatibilityMod: "mysql",
	}
	opts := database.AdapterOptions{
		CapabilityResolver: datasource.ResolveCapabilities,
		SchemaContext:      dbName,
	}
	adapter := database.NewMySQLAdapter(ep, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := adapter.OpenPurposePool(ctx, database.PurposeExecution, database.Credential{
		Username: user,
		Password: pass,
	}, nil)
	if err != nil {
		t.Skipf("skipping MySQL live rollback integration test (container not reachable at %s:%d): %v", host, port, err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return adapter, pool, dbName
}

func openPGTestPool(t *testing.T, defaultPort int, defaultUser, defaultPass, defaultDB string) (database.Adapter, *database.PurposePool, string) {
	t.Helper()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	host := envOr("YEARNING_ROLLBACK_TEST_PG_HOST", "127.0.0.1")
	port := defaultPort
	user := defaultUser
	pass := defaultPass
	dbName := defaultDB

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
		t.Skipf("skipping PostgreSQL live rollback integration test (container not reachable at %s:%d): %v", host, port, err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return adapter, pool, "public"
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func execDDLOrFail(t *testing.T, ctx context.Context, adapter database.Adapter, pool *database.PurposePool, schema, sqlText string) {
	t.Helper()
	res, err := adapter.ExecuteDDLStatement(ctx, pool, schema, sqlText)
	if err != nil || res == nil || res.Outcome != database.OutcomeSuccess {
		t.Fatalf("ExecuteDDLStatement (%s) failed: err=%v res=%+v", sqlText, err, res)
	}
}

func TestMySQLDMLAndDDLRollbackIntegration(t *testing.T) {
	adapter, pool, schema := openMySQLTestPool(t)
	ctx := context.Background()
	table := fmt.Sprintf("rb_mysql_dml_%d", time.Now().UnixNano()%1000000)

	execDDLOrFail(t, ctx, adapter, pool, schema, fmt.Sprintf("CREATE TABLE `%s` (`id` INT NOT NULL AUTO_INCREMENT PRIMARY KEY, `name` VARCHAR(64) NOT NULL, `note` VARCHAR(64) NULL, `balance` INT NOT NULL DEFAULT 0)", table))
	t.Cleanup(func() {
		_, _ = adapter.ExecuteDDLStatement(context.Background(), pool, schema, fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table))
	})

	// 1. DML INSERT with AUTO_INCREMENT capture & rollback
	tx, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction: %v", err)
	}
	insertSQL := fmt.Sprintf("INSERT INTO `%s` (`name`, `note`, `balance`) VALUES ('alice', 'vip', 100), ('bob', NULL, 250)", table)
	cap1, err := rollback.AnalyzeStatement("mysql", schema, insertSQL)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, tx)
		t.Fatalf("expected INSERT capability supported, got: %v", err)
	}
	resInsert, evInsert, _, err := rollback.ExecuteAndCaptureMySQLDML(ctx, adapter, pool, tx, 1, cap1, 0)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, tx)
		t.Fatalf("ExecuteAndCaptureMySQLDML INSERT failed: %v", err)
	}
	if _, err := adapter.CommitDMLTransaction(ctx, tx); err != nil {
		t.Fatalf("commit INSERT: %v", err)
	}
	if resInsert.AffectedRows != 2 || len(evInsert.AfterRows) != 2 || len(evInsert.InverseStatements) != 2 {
		t.Fatalf("unexpected INSERT evidence: affected=%d after=%d inverse=%v", resInsert.AffectedRows, len(evInsert.AfterRows), evInsert.InverseStatements)
	}

	// 2. DML UPDATE capture & conflict detection + rollback
	tx2, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction 2: %v", err)
	}
	updateSQL := fmt.Sprintf("UPDATE `%s` SET `balance` = 999, `note` = 'updated' WHERE `name` = 'alice'", table)
	cap2, err := rollback.AnalyzeStatement("mysql", schema, updateSQL)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, tx2)
		t.Fatalf("AnalyzeStatement UPDATE: %v", err)
	}
	_, evUpdate, _, err := rollback.ExecuteAndCaptureMySQLDML(ctx, adapter, pool, tx2, 1, cap2, 0)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, tx2)
		t.Fatalf("ExecuteAndCaptureMySQLDML UPDATE failed: %v", err)
	}
	if _, err := adapter.CommitDMLTransaction(ctx, tx2); err != nil {
		t.Fatalf("commit UPDATE: %v", err)
	}

	// Verify precondition passes right after UPDATE
	txVerify, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction verify: %v", err)
	}
	if err := rollback.VerifyDMLStatementPreconditionInTx(ctx, pool, txVerify, "mysql", *evUpdate); err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txVerify)
		t.Fatalf("expected precondition to pass before out-of-band edit, got: %v", err)
	}
	// Execute the inverse SQL of UPDATE inside txVerify
	for _, inv := range evUpdate.InverseStatements {
		res, err := adapter.ExecuteDMLStatement(ctx, txVerify, database.DMLStatement{SQL: inv})
		if err != nil || res == nil || res.Outcome != database.OutcomeSuccess {
			_, _ = adapter.RollbackDMLTransaction(ctx, txVerify)
			t.Fatalf("execute UPDATE inverse SQL (%s) failed: err=%v res=%+v", inv, err, res)
		}
	}
	if _, err := adapter.CommitDMLTransaction(ctx, txVerify); err != nil {
		t.Fatalf("commit UPDATE rollback: %v", err)
	}

	// Verify alice's balance was restored to 100 and note to 'vip'
	_, rows, err := database.QueryPoolRollbackRows(ctx, pool, schema, fmt.Sprintf("SELECT `balance`, `note` FROM `%s` WHERE `name` = 'alice'", table))
	if err != nil || len(rows) != 1 {
		t.Fatalf("query alice after rollback: rows=%v err=%v", rows, err)
	}
	if rows[0].Cells[0].IsNull || rows[0].Cells[0].Text != "100" || rows[0].Cells[1].IsNull || rows[0].Cells[1].Text != "vip" {
		t.Fatalf("alice row not restored accurately: %+v", rows[0])
	}

	// 3. Out-of-band edit causes deterministic conflict detection
	txConflict, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction conflict: %v", err)
	}
	// Since alice's balance is now 100 (not 999), re-verifying evUpdate's precondition must detect a conflict!
	if err := rollback.VerifyDMLStatementPreconditionInTx(ctx, pool, txConflict, "mysql", *evUpdate); err == nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txConflict)
		t.Fatalf("expected conflict error when row diverges from evUpdate.AfterRows, got nil")
	}
	_, _ = adapter.RollbackDMLTransaction(ctx, txConflict)

	// 4. DML DELETE capture & rollback
	tx3, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction 3: %v", err)
	}
	deleteSQL := fmt.Sprintf("DELETE FROM `%s` WHERE `name` = 'bob'", table)
	cap3, err := rollback.AnalyzeStatement("mysql", schema, deleteSQL)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, tx3)
		t.Fatalf("AnalyzeStatement DELETE: %v", err)
	}
	_, evDelete, _, err := rollback.ExecuteAndCaptureMySQLDML(ctx, adapter, pool, tx3, 1, cap3, 0)
	if err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, tx3)
		t.Fatalf("ExecuteAndCaptureMySQLDML DELETE failed: %v", err)
	}
	if _, err := adapter.CommitDMLTransaction(ctx, tx3); err != nil {
		t.Fatalf("commit DELETE: %v", err)
	}

	// Verify precondition and execute inverse INSERT for bob
	txRestoreBob, err := adapter.BeginDMLTransaction(ctx, pool)
	if err != nil {
		t.Fatalf("BeginDMLTransaction restore bob: %v", err)
	}
	if err := rollback.VerifyDMLStatementPreconditionInTx(ctx, pool, txRestoreBob, "mysql", *evDelete); err != nil {
		_, _ = adapter.RollbackDMLTransaction(ctx, txRestoreBob)
		t.Fatalf("VerifyDMLStatementPreconditionInTx for DELETE failed: %v", err)
	}
	for _, inv := range evDelete.InverseStatements {
		res, err := adapter.ExecuteDMLStatement(ctx, txRestoreBob, database.DMLStatement{SQL: inv})
		if err != nil || res == nil || res.Outcome != database.OutcomeSuccess {
			_, _ = adapter.RollbackDMLTransaction(ctx, txRestoreBob)
			t.Fatalf("execute DELETE inverse SQL (%s) failed: err=%v res=%+v", inv, err, res)
		}
	}
	if _, err := adapter.CommitDMLTransaction(ctx, txRestoreBob); err != nil {
		t.Fatalf("commit DELETE rollback: %v", err)
	}

	// 5. DDL ADD COLUMN capture, precondition data-loss check & rollback
	addColSQL := fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `tier` VARCHAR(32) NULL", table)
	ddlCap, err := rollback.AnalyzeStatement("mysql", schema, addColSQL)
	if err != nil {
		t.Fatalf("expected ADD COLUMN supported, got: %v", err)
	}
	preSnap, err := rollback.PrepareDDLPreExecution(ctx, pool, "mysql", ddlCap)
	if err != nil {
		t.Fatalf("PrepareDDLPreExecution: %v", err)
	}
	execDDLOrFail(t, ctx, adapter, pool, schema, addColSQL)
	evDDL, err := rollback.FinalizeDDLPostExecution(ctx, pool, "mysql", 1, ddlCap, preSnap)
	if err != nil {
		t.Fatalf("FinalizeDDLPostExecution: %v", err)
	}
	if err := rollback.VerifyDDLStatementPrecondition(ctx, pool, "mysql", *evDDL); err != nil {
		t.Fatalf("VerifyDDLStatementPrecondition on empty added column failed: %v", err)
	}
	for _, inv := range evDDL.InverseStatements {
		execDDLOrFail(t, ctx, adapter, pool, schema, inv)
	}

	// 6. DDL DROP COLUMN -> ADD COLUMN and DROP TABLE -> CREATE TABLE schema-only rollback
	execDDLOrFail(t, ctx, adapter, pool, schema, fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `temp_col` VARCHAR(48) NOT NULL DEFAULT 'init'", table))
	dropColSQL := fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `temp_col`", table)
	dropColCap, err := rollback.AnalyzeStatement("mysql", schema, dropColSQL)
	if err != nil {
		t.Fatalf("AnalyzeStatement MySQL DROP COLUMN: %v", err)
	}
	preDropCol, err := rollback.PrepareDDLPreExecution(ctx, pool, "mysql", dropColCap)
	if err != nil {
		t.Fatalf("PrepareDDLPreExecution MySQL DROP COLUMN: %v", err)
	}
	execDDLOrFail(t, ctx, adapter, pool, schema, dropColSQL)
	evDropCol, err := rollback.FinalizeDDLPostExecution(ctx, pool, "mysql", 1, dropColCap, preDropCol)
	if err != nil {
		t.Fatalf("FinalizeDDLPostExecution MySQL DROP COLUMN: %v", err)
	}
	if err := rollback.VerifyDDLStatementPrecondition(ctx, pool, "mysql", *evDropCol); err != nil {
		t.Fatalf("VerifyDDLStatementPrecondition MySQL DROP COLUMN: %v", err)
	}
	for _, inv := range evDropCol.InverseStatements {
		execDDLOrFail(t, ctx, adapter, pool, schema, inv)
	}

	dropTblSQL := fmt.Sprintf("DROP TABLE `%s`", table)
	dropTblCap, err := rollback.AnalyzeStatement("mysql", schema, dropTblSQL)
	if err != nil {
		t.Fatalf("AnalyzeStatement MySQL DROP TABLE: %v", err)
	}
	preDropTbl, err := rollback.PrepareDDLPreExecution(ctx, pool, "mysql", dropTblCap)
	if err != nil {
		t.Fatalf("PrepareDDLPreExecution MySQL DROP TABLE: %v", err)
	}
	execDDLOrFail(t, ctx, adapter, pool, schema, dropTblSQL)
	evDropTbl, err := rollback.FinalizeDDLPostExecution(ctx, pool, "mysql", 1, dropTblCap, preDropTbl)
	if err != nil {
		t.Fatalf("FinalizeDDLPostExecution MySQL DROP TABLE: %v", err)
	}
	if err := rollback.VerifyDDLStatementPrecondition(ctx, pool, "mysql", *evDropTbl); err != nil {
		t.Fatalf("VerifyDDLStatementPrecondition MySQL DROP TABLE: %v", err)
	}
	for _, inv := range evDropTbl.InverseStatements {
		execDDLOrFail(t, ctx, adapter, pool, schema, inv)
	}
}

func TestPostgreSQL17And18DMLAndDDLRollbackIntegration(t *testing.T) {
	targets := []struct {
		name        string
		port        int
		user        string
		pass        string
		db          string
		expectPG18  bool
		expectedStr string
	}{
		{
			name:        "PG17_TxSelectForUpdateReturning",
			port:        envIntOr("YEARNING_ROLLBACK_TEST_PG17_PORT", 54333),
			user:        envOr("YEARNING_ROLLBACK_TEST_PG17_USER", "yr"),
			pass:        envOr("YEARNING_ROLLBACK_TEST_PG17_PASSWORD", "f12-target-a16f4908848a"),
			db:          envOr("YEARNING_ROLLBACK_TEST_PG17_DB", "target"),
			expectPG18:  false,
			expectedStr: rollback.StrategyPGDMLTxReturning,
		},
		{
			name:        "PG18_NativeReturningOldNew",
			port:        envIntOr("YEARNING_ROLLBACK_TEST_PG18_PORT", 54332),
			user:        envOr("YEARNING_ROLLBACK_TEST_PG18_USER", "yearning"),
			pass:        envOr("YEARNING_ROLLBACK_TEST_PG18_PASSWORD", "f12-meta-a4512e1f9cbe"),
			db:          envOr("YEARNING_ROLLBACK_TEST_PG18_DB", "yearning_f12b"),
			expectPG18:  true,
			expectedStr: rollback.StrategyPG18DMLReturning,
		},
	}

	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			adapter, pool, schema := openPGTestPool(t, tc.port, tc.user, tc.pass, tc.db)
			ctx := context.Background()

			table := fmt.Sprintf("rb_pg_test_%d", time.Now().UnixNano()%1000000)
			execDDLOrFail(t, ctx, adapter, pool, schema, fmt.Sprintf(`CREATE TABLE "%s"."%s" ("id" SERIAL PRIMARY KEY, "name" VARCHAR(64) NOT NULL, "score" INT NOT NULL DEFAULT 0)`, schema, table))
			t.Cleanup(func() {
				_, _ = adapter.ExecuteDDLStatement(context.Background(), pool, schema, fmt.Sprintf(`DROP TABLE IF EXISTS "%s"."%s"`, schema, table))
			})

			// 1. INSERT
			tx1, err := adapter.BeginDMLTransaction(ctx, pool)
			if err != nil {
				t.Fatalf("BeginDMLTransaction INSERT: %v", err)
			}
			insSQL := fmt.Sprintf(`INSERT INTO "%s"."%s" ("name", "score") VALUES ('charlie', 42), ('dana', 88)`, schema, table)
			capIns, err := rollback.AnalyzeStatement("postgresql", schema, insSQL)
			if err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, tx1)
				t.Fatalf("AnalyzeStatement INSERT: %v", err)
			}
			_, evIns, metaIns, strategyIns, err := rollback.ExecuteAndCapturePostgresDML(ctx, pool, tx1, 1, capIns, 0)
			if err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, tx1)
				t.Fatalf("ExecuteAndCapturePostgresDML INSERT: %v", err)
			}
			if _, err := adapter.CommitDMLTransaction(ctx, tx1); err != nil {
				t.Fatalf("commit INSERT: %v", err)
			}
			if tc.expectPG18 && metaIns.ServerVersionNum < 180000 {
				t.Fatalf("expected PG 18+, got version_num=%d", metaIns.ServerVersionNum)
			}
			if !tc.expectPG18 && metaIns.ServerVersionNum >= 180000 {
				t.Fatalf("expected PG < 18, got version_num=%d", metaIns.ServerVersionNum)
			}
			if strategyIns != tc.expectedStr {
				t.Fatalf("expected strategy %s, got %s", tc.expectedStr, strategyIns)
			}
			if len(evIns.AfterRows) != 2 || len(evIns.InverseStatements) != 2 {
				t.Fatalf("expected 2 after-image rows and 2 inverse statements, got %d / %v", len(evIns.AfterRows), evIns.InverseStatements)
			}

			// 2. UPDATE
			tx2, err := adapter.BeginDMLTransaction(ctx, pool)
			if err != nil {
				t.Fatalf("BeginDMLTransaction UPDATE: %v", err)
			}
			updSQL := fmt.Sprintf(`UPDATE "%s"."%s" SET "score" = 500 WHERE "name" = 'charlie'`, schema, table)
			capUpd, err := rollback.AnalyzeStatement("postgresql", schema, updSQL)
			if err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, tx2)
				t.Fatalf("AnalyzeStatement UPDATE: %v", err)
			}
			_, evUpd, _, strategyUpd, err := rollback.ExecuteAndCapturePostgresDML(ctx, pool, tx2, 1, capUpd, 0)
			if err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, tx2)
				t.Fatalf("ExecuteAndCapturePostgresDML UPDATE: %v", err)
			}
			if _, err := adapter.CommitDMLTransaction(ctx, tx2); err != nil {
				t.Fatalf("commit UPDATE: %v", err)
			}
			if strategyUpd != tc.expectedStr {
				t.Fatalf("expected strategy %s on UPDATE, got %s", tc.expectedStr, strategyUpd)
			}

			// Precondition check + execute UPDATE inverse
			txRbUpd, err := adapter.BeginDMLTransaction(ctx, pool)
			if err != nil {
				t.Fatalf("BeginDMLTransaction rollback UPDATE: %v", err)
			}
			if err := rollback.VerifyDMLStatementPreconditionInTx(ctx, pool, txRbUpd, "postgresql", *evUpd); err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, txRbUpd)
				t.Fatalf("VerifyDMLStatementPreconditionInTx UPDATE failed: %v", err)
			}
			for _, inv := range evUpd.InverseStatements {
				res, err := adapter.ExecuteDMLStatement(ctx, txRbUpd, database.DMLStatement{SQL: inv})
				if err != nil || res == nil || res.Outcome != database.OutcomeSuccess {
					_, _ = adapter.RollbackDMLTransaction(ctx, txRbUpd)
					t.Fatalf("execute PG UPDATE inverse (%s) failed: err=%v res=%+v", inv, err, res)
				}
			}
			if _, err := adapter.CommitDMLTransaction(ctx, txRbUpd); err != nil {
				t.Fatalf("commit PG UPDATE rollback: %v", err)
			}

			// Verify score restored to 42
			_, rows, err := database.QueryPoolRollbackRows(ctx, pool, schema, fmt.Sprintf(`SELECT "score"::text FROM "%s"."%s" WHERE "name" = 'charlie'`, schema, table))
			if err != nil || len(rows) != 1 || rows[0].Cells[0].IsNull || rows[0].Cells[0].Text != "42" {
				t.Fatalf("charlie score not restored to 42: rows=%+v err=%v", rows, err)
			}

			// 3. DELETE
			tx3, err := adapter.BeginDMLTransaction(ctx, pool)
			if err != nil {
				t.Fatalf("BeginDMLTransaction DELETE: %v", err)
			}
			delSQL := fmt.Sprintf(`DELETE FROM "%s"."%s" WHERE "name" = 'dana'`, schema, table)
			capDel, err := rollback.AnalyzeStatement("postgresql", schema, delSQL)
			if err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, tx3)
				t.Fatalf("AnalyzeStatement DELETE: %v", err)
			}
			_, evDel, _, _, err := rollback.ExecuteAndCapturePostgresDML(ctx, pool, tx3, 1, capDel, 0)
			if err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, tx3)
				t.Fatalf("ExecuteAndCapturePostgresDML DELETE: %v", err)
			}
			if _, err := adapter.CommitDMLTransaction(ctx, tx3); err != nil {
				t.Fatalf("commit DELETE: %v", err)
			}

			// Precondition check + execute DELETE inverse
			txRbDel, err := adapter.BeginDMLTransaction(ctx, pool)
			if err != nil {
				t.Fatalf("BeginDMLTransaction rollback DELETE: %v", err)
			}
			if err := rollback.VerifyDMLStatementPreconditionInTx(ctx, pool, txRbDel, "postgresql", *evDel); err != nil {
				_, _ = adapter.RollbackDMLTransaction(ctx, txRbDel)
				t.Fatalf("VerifyDMLStatementPreconditionInTx DELETE failed: %v", err)
			}
			for _, inv := range evDel.InverseStatements {
				res, err := adapter.ExecuteDMLStatement(ctx, txRbDel, database.DMLStatement{SQL: inv})
				if err != nil || res == nil || res.Outcome != database.OutcomeSuccess {
					_, _ = adapter.RollbackDMLTransaction(ctx, txRbDel)
					t.Fatalf("execute PG DELETE inverse (%s) failed: err=%v res=%+v", inv, err, res)
				}
			}
			if _, err := adapter.CommitDMLTransaction(ctx, txRbDel); err != nil {
				t.Fatalf("commit PG DELETE rollback: %v", err)
			}

			// 4. PG DDL ADD COLUMN + RENAME COLUMN rollback
			addColSQL := fmt.Sprintf(`ALTER TABLE "%s"."%s" ADD COLUMN "tag" VARCHAR(32)`, schema, table)
			ddlCap, err := rollback.AnalyzeStatement("postgresql", schema, addColSQL)
			if err != nil {
				t.Fatalf("AnalyzeStatement PG ADD COLUMN: %v", err)
			}
			preSnap, err := rollback.PrepareDDLPreExecution(ctx, pool, "postgresql", ddlCap)
			if err != nil {
				t.Fatalf("PrepareDDLPreExecution PG: %v", err)
			}
			execDDLOrFail(t, ctx, adapter, pool, schema, addColSQL)
			evDDL, err := rollback.FinalizeDDLPostExecution(ctx, pool, "postgresql", 1, ddlCap, preSnap)
			if err != nil {
				t.Fatalf("FinalizeDDLPostExecution PG: %v", err)
			}
			if err := rollback.VerifyDDLStatementPrecondition(ctx, pool, "postgresql", *evDDL); err != nil {
				t.Fatalf("VerifyDDLStatementPrecondition PG ADD COLUMN: %v", err)
			}
			for _, inv := range evDDL.InverseStatements {
				execDDLOrFail(t, ctx, adapter, pool, schema, inv)
			}

			// 5. PG DDL DROP COLUMN -> ADD COLUMN and DROP TABLE -> CREATE TABLE schema-only rollback
			execDDLOrFail(t, ctx, adapter, pool, schema, fmt.Sprintf(`ALTER TABLE "%s"."%s" ADD COLUMN "temp_col" VARCHAR(48) NOT NULL DEFAULT 'init'`, schema, table))
			dropColSQL := fmt.Sprintf(`ALTER TABLE "%s"."%s" DROP COLUMN "temp_col"`, schema, table)
			dropColCap, err := rollback.AnalyzeStatement("postgresql", schema, dropColSQL)
			if err != nil {
				t.Fatalf("AnalyzeStatement PG DROP COLUMN: %v", err)
			}
			preDropCol, err := rollback.PrepareDDLPreExecution(ctx, pool, "postgresql", dropColCap)
			if err != nil {
				t.Fatalf("PrepareDDLPreExecution PG DROP COLUMN: %v", err)
			}
			execDDLOrFail(t, ctx, adapter, pool, schema, dropColSQL)
			evDropCol, err := rollback.FinalizeDDLPostExecution(ctx, pool, "postgresql", 1, dropColCap, preDropCol)
			if err != nil {
				t.Fatalf("FinalizeDDLPostExecution PG DROP COLUMN: %v", err)
			}
			if err := rollback.VerifyDDLStatementPrecondition(ctx, pool, "postgresql", *evDropCol); err != nil {
				t.Fatalf("VerifyDDLStatementPrecondition PG DROP COLUMN: %v", err)
			}
			for _, inv := range evDropCol.InverseStatements {
				execDDLOrFail(t, ctx, adapter, pool, schema, inv)
			}

			dropTblSQL := fmt.Sprintf(`DROP TABLE "%s"."%s"`, schema, table)
			dropTblCap, err := rollback.AnalyzeStatement("postgresql", schema, dropTblSQL)
			if err != nil {
				t.Fatalf("AnalyzeStatement PG DROP TABLE: %v", err)
			}
			preDropTbl, err := rollback.PrepareDDLPreExecution(ctx, pool, "postgresql", dropTblCap)
			if err != nil {
				t.Fatalf("PrepareDDLPreExecution PG DROP TABLE: %v", err)
			}
			execDDLOrFail(t, ctx, adapter, pool, schema, dropTblSQL)
			evDropTbl, err := rollback.FinalizeDDLPostExecution(ctx, pool, "postgresql", 1, dropTblCap, preDropTbl)
			if err != nil {
				t.Fatalf("FinalizeDDLPostExecution PG DROP TABLE: %v", err)
			}
			if err := rollback.VerifyDDLStatementPrecondition(ctx, pool, "postgresql", *evDropTbl); err != nil {
				t.Fatalf("VerifyDDLStatementPrecondition PG DROP TABLE: %v", err)
			}
			for _, inv := range evDropTbl.InverseStatements {
				execDDLOrFail(t, ctx, adapter, pool, schema, inv)
			}
		})
	}
}
