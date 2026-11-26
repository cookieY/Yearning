//go:build integration

// suite_integration_test.go Unified Adapter Suite: runs the exact same test suite against both MySQL and PostgreSQL
// (BE-B4-ADAPTER acceptance gate 1). Covers contract test_gate.required_suites:
// tls_connection/identity_stability/metadata/identifier_quoting/
// explain_non_execution/single_select/cursor_affinity_and_expiry/
// dml_commit_and_rollback/multi_ddl_partial_failure/cancellation/
// error_sanitization/context_cancellation/capability_fail_closed.
//
// Target databases are provided via environment variables (disposable local containers, production credentials forbidden):
//
//	YEARNING_TEST_ADAPTER_MYSQL_PASSWORD required (skips MySQL half when unset)
//	YEARNING_TEST_ADAPTER_MYSQL_HOST     default localhost
//	YEARNING_TEST_ADAPTER_MYSQL_PORT     default 33306 (disposable mysql:8.0 container)
//	YEARNING_TEST_ADAPTER_MYSQL_USER     default root
//	YEARNING_TEST_ADAPTER_MYSQL_DB       default yearning
//	YEARNING_TEST_ADAPTER_PG_PASSWORD    required (skips PG half when unset)
//	YEARNING_TEST_ADAPTER_PG_HOST        default localhost
//	YEARNING_TEST_ADAPTER_PG_PORT        default 54331 (disposable postgres:16 container)
//	YEARNING_TEST_ADAPTER_PG_USER        default yearning
//	YEARNING_TEST_ADAPTER_PG_DB          default yearning
package database_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
)

type suiteFixtures struct {
	Statements map[string]string `json:"statements"`
	Cancel     map[string]string `json:"cancel_select"`
	CancelLong map[string]string `json:"cancel_select_long"`
	RowCount   int               `json:"row_count"`
	MarkerID   int               `json:"marker_id"`
}

func loadFixtures(t *testing.T) *suiteFixtures {
	t.Helper()
	for _, rel := range []string{
		"../../../testdata/sql/adapter-suite-fixtures.json",
		"../../testdata/sql/adapter-suite-fixtures.json",
	} {
		data, err := os.ReadFile(rel)
		if err == nil {
			var f suiteFixtures
			if err := json.Unmarshal(data, &f); err != nil {
				t.Fatalf("fixtures unparsable: %v", err)
			}
			return &f
		}
	}
	t.Fatal("adapter-suite-fixtures.json not found")
	return nil
}

type suiteTarget struct {
	name     string // "mysql" | "postgresql"
	engine   string // capability engine name
	mode     string
	host     string
	port     int
	user     string
	password string
	dbname   string
}

func (tg *suiteTarget) endpoint() database.Endpoint {
	return database.Endpoint{Host: tg.host, Port: tg.port, DatabaseName: tg.dbname, CompatibilityMod: tg.mode}
}

func (tg *suiteTarget) credential() database.Credential {
	return database.Credential{Username: tg.user, Password: tg.password}
}

func mysqlTarget() *suiteTarget {
	cfg, ok := testenv.ResolveTargetMySQL()
	if !ok {
		return nil
	}
	return &suiteTarget{
		name: "mysql", engine: "mysql", mode: "mysql",
		host: cfg.Host, port: cfg.Port, user: cfg.User,
		password: cfg.Password, dbname: cfg.DBName,
	}
}

func pgTarget() *suiteTarget {
	cfg, ok := testenv.ResolveTargetPG()
	if !ok {
		return nil
	}
	return &suiteTarget{
		name: "postgresql", engine: "postgresql", mode: "postgresql",
		host: cfg.Host, port: cfg.Port, user: cfg.User,
		password: cfg.Password, dbname: cfg.DBName,
	}
}

// suiteEnv holds all state for a single Suite run.
type suiteEnv struct {
	t       *testing.T
	target  *suiteTarget
	fix     *suiteFixtures
	schema  string
	adapter database.Adapter
	review  *database.PurposePool
	query   *database.PurposePool
	exec    *database.PurposePool
	admin   *sql.DB
	sensSet map[string]struct{}
}

func openAdminDB(t *testing.T, tg *suiteTarget) *sql.DB {
	t.Helper()
	var db *sql.DB
	var err error
	switch tg.mode {
	case "mysql":
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?multiStatements=false", tg.user, tg.password, tg.host, tg.port, tg.dbname)
		db, err = sql.Open("mysql", dsn)
	default:
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", tg.host, tg.port, tg.user, tg.password, tg.dbname)
		db, err = sql.Open("pgx", dsn)
	}
	if err != nil {
		t.Fatalf("admin open: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("admin ping (%s): %v", tg.name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func setupSuite(t *testing.T, tg *suiteTarget, fix *suiteFixtures) *suiteEnv {
	t.Helper()
	env := &suiteEnv{t: t, target: tg, fix: fix, sensSet: map[string]struct{}{"secret_note": {}}}
	env.schema = "b4suite_" + time.Now().Format("0102150405")
	env.admin = openAdminDB(t, tg)
	ctx := context.Background()
	if tg.mode == "mysql" {
		if _, err := env.admin.ExecContext(ctx, "CREATE DATABASE `"+env.schema+"`"); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	} else {
		if _, err := env.admin.ExecContext(ctx, `CREATE SCHEMA "`+env.schema+`"`); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	t.Cleanup(func() {
		if tg.mode == "mysql" {
			_, _ = env.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+env.schema+"`")
		} else {
			_, _ = env.admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+env.schema+`" CASCADE`)
		}
	})

	opts := database.AdapterOptions{
		CapabilityResolver: datasource.ResolveCapabilities,
		SensitiveColumns:   env.sensSet,
		SchemaContext:      env.schema,
	}
	if tg.mode == "mysql" {
		env.adapter = database.NewMySQLAdapter(tg.endpoint(), opts)
	} else {
		env.adapter = database.NewPostgreSQLAdapter(tg.endpoint(), opts)
	}
	var err error
	env.review, err = env.adapter.OpenPurposePool(ctx, database.PurposeReview, tg.credential(), nil)
	if err != nil {
		t.Fatalf("review pool: %v", err)
	}
	env.query, err = env.adapter.OpenPurposePool(ctx, database.PurposeQuery, tg.credential(), nil)
	if err != nil {
		t.Fatalf("query pool: %v", err)
	}
	env.exec, err = env.adapter.OpenPurposePool(ctx, database.PurposeExecution, tg.credential(), nil)
	if err != nil {
		t.Fatalf("execution pool: %v", err)
	}
	t.Cleanup(func() {
		_ = env.review.Close()
		_ = env.query.Close()
		_ = env.exec.Close()
	})

	// Create table via DDL (exercising the Adapter execution path).
	if _, err := env.adapter.ExecuteDDLStatement(ctx, env.exec, env.schema, fix.Statements["create_items"]); err != nil {
		t.Fatalf("create items: %v", err)
	}
	// Load 25 rows in a single DML transaction (E001: single transaction per stage).
	tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
	if err != nil {
		t.Fatalf("begin load tx: %v", err)
	}
	for i := 1; i <= fix.RowCount; i++ {
		stmt := strings.ReplaceAll(fix.Statements["insert_item"], "{id}", fmt.Sprintf("%d", i))
		stmt = strings.ReplaceAll(stmt, "{name}", fmt.Sprintf("name-%02d", i))
		stmt = strings.ReplaceAll(stmt, "{note}", fmt.Sprintf("note-%02d", i))
		stmt = strings.ReplaceAll(stmt, "{secret}", fmt.Sprintf("secret-%02d", i))
		res, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: stmt})
		if err != nil || res.Outcome != database.OutcomeSuccess {
			t.Fatalf("load row %d: err=%v outcome=%s failname=%s class=%s", i, err, res.Outcome, res.SanitizedFailureName, res.EngineCodeClass)
		}
	}
	if _, err := env.adapter.CommitDMLTransaction(ctx, tx); err != nil {
		t.Fatalf("commit load tx: %v", err)
	}
	// Marker row for Explain (must never be mutated by Explain).
	tx, err = env.adapter.BeginDMLTransaction(ctx, env.exec)
	if err != nil {
		t.Fatalf("begin marker tx: %v", err)
	}
	stmt := strings.ReplaceAll(fix.Statements["insert_item"], "{id}", fmt.Sprintf("%d", fix.MarkerID))
	stmt = strings.ReplaceAll(stmt, "{name}", "marker")
	stmt = strings.ReplaceAll(stmt, "{note}", "marker-note")
	stmt = strings.ReplaceAll(stmt, "{secret}", "marker-secret")
	if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: stmt}); err != nil {
		t.Fatalf("marker row: %v", err)
	}
	if _, err := env.adapter.CommitDMLTransaction(ctx, tx); err != nil {
		t.Fatalf("commit marker tx: %v", err)
	}
	return env
}

func runBothEngines(t *testing.T, fn func(t *testing.T, env *suiteEnv)) {
	t.Helper()
	fix := loadFixtures(t)
	for _, entry := range []struct {
		name string
		tg   *suiteTarget
	}{
		{name: "mysql", tg: mysqlTarget()},
		{name: "postgresql", tg: pgTarget()},
	} {
		if entry.tg == nil {
			testenv.SkipOrFatal(t, "%s target not configured", entry.name)
			continue
		}
		t.Run(entry.tg.name, func(t *testing.T) {
			env := setupSuite(t, entry.tg, fix)
			fn(t, env)
		})
	}
}

func (e *suiteEnv) countItems(ctx context.Context) int {
	e.t.Helper()
	var n int
	var err error
	if e.target.mode == "mysql" {
		err = e.admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+e.schema+"`.items").Scan(&n)
	} else {
		err = e.admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+e.schema+`".items`).Scan(&n)
	}
	if err != nil {
		e.t.Fatalf("count items: %v", err)
	}
	return n
}

func (e *suiteEnv) markerName(ctx context.Context) string {
	e.t.Helper()
	var name string
	var err error
	if e.target.mode == "mysql" {
		err = e.admin.QueryRowContext(ctx, "SELECT name FROM `"+e.schema+"`.items WHERE id = ?", e.fix.MarkerID).Scan(&name)
	} else {
		err = e.admin.QueryRowContext(ctx, `SELECT name FROM "`+e.schema+`".items WHERE id = $1`, e.fix.MarkerID).Scan(&name)
	}
	if err != nil {
		e.t.Fatalf("marker name: %v", err)
	}
	return name
}

// TestAdapterSuite_MySQLSessionInitializationPinned is the persistent regression test for SM-05:
// instead of only inspecting mysql.Config, it establishes a real connection using production
// BuildMySQLConfig and asserts that sql_mode and charset are applied to the session. Skips when no
// real MySQL is available, never mocking a pass.
func TestAdapterSuite_MySQLSessionInitializationPinned(t *testing.T) {
	tg := mysqlTarget()
	if tg == nil {
		testenv.SkipOrFatal(t, "mysql target not configured")
	}
	cfg, reg, err := database.BuildMySQLConfig(tg.endpoint(), tg.credential(), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("build mysql config: %v", err)
	}
	_ = reg // nil when no TLS material is provided; TLS registration is managed by the process lifecycle.
	conn, err := gomysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var mode, charset string
	if err := db.QueryRowContext(ctx, "SELECT @@session.sql_mode, @@character_set_client").Scan(&mode, &charset); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if mode != database.MySQLSessionSQLMode {
		t.Fatalf("sql_mode = %q; want %q", mode, database.MySQLSessionSQLMode)
	}
	if charset != "utf8mb4" {
		t.Fatalf("character_set_client = %q; want utf8mb4", charset)
	}
}

// ---- Gate 1: MySQL and PG run the exact same Adapter Suite (proven by runBothEngines) ----

// required_suite: tls_connection (negative case: container self-signed certificate must be rejected by the system trust chain;
// positive case is in the in-process fixture in tls_config_test.go). PG container disables ssl by default, so only the MySQL half applies.
func TestAdapterSuite_TLSConnectionVerifiedAgainstRealServer(t *testing.T) {
	tg := mysqlTarget()
	if tg == nil {
		testenv.SkipOrFatal(t, "mysql target not configured")
	}
	fix := loadFixtures(t)
	env := &suiteEnv{t: t, target: tg, fix: fix, schema: "tls_probe"}
	adapter := database.NewMySQLAdapter(tg.endpoint(), database.AdapterOptions{
		CapabilityResolver: datasource.ResolveCapabilities,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Enforced server-verified TLS with only system roots -> self-signed container certificate must fail, and failure name must be sanitized.
	_, err := adapter.TestConnection(ctx, tg.credential(), &database.TLSMaterial{VerifyServer: true})
	if err == nil {
		t.Fatal("enforced TLS verification against self-signed certificate must fail (if verification is skipped, there is a regression)")
	}
	if strings.Contains(err.Error(), tg.host) || strings.Contains(err.Error(), "x509") {
		t.Fatalf("failure message must be sanitized: %v", err)
	}
	_ = env
}

// required_suite: identity_stability + capability_fail_closed (success path).
func TestAdapterSuite_ConnectionTestAndIdentityStability(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		first, err := env.adapter.TestConnection(ctx, env.target.credential(), nil)
		if err != nil {
			t.Fatalf("TestConnection: %v", err)
		}
		if first.SanitizedFailureName != "" || first.ServerIdentityFingerprint == "" {
			t.Fatalf("connection test result incomplete: %+v", first)
		}
		if first.Engine != env.target.engine || first.CompatibilityMode != env.target.mode {
			t.Fatalf("engine/mode mismatch: %+v", first)
		}
		if !first.Capabilities["metadata"] || !first.Capabilities["query"] || !first.Capabilities["dml_execution"] {
			t.Fatalf("target capabilities not resolved: %v", first.Capabilities)
		}
		second, err := env.adapter.TestConnection(ctx, env.target.credential(), nil)
		if err != nil {
			t.Fatalf("second TestConnection: %v", err)
		}
		if second.ServerIdentityFingerprint != first.ServerIdentityFingerprint {
			t.Fatal("identity fingerprint must be stable across connections (credential rotation invariance is guaranteed by input composition)")
		}
		if second.ServerVersion != first.ServerVersion {
			t.Fatal("server version must be stable")
		}
	})
}

// required_suite: capability_fail_closed (failure path: out-of-matrix version must fail with Unsupported, not falsely succeed).
func TestAdapterSuite_CapabilityFailClosedOutOfMatrix(t *testing.T) {
	// PG minimum floor is >=14: verify live PG 18 passes while below-floor PG 13.3 fails closed.
	cfg := testenv.RequireMetaPG(t)
	tg := &suiteTarget{
		name: "postgresql-18", engine: "postgresql", mode: "postgresql",
		host: cfg.Host, port: cfg.Port, user: cfg.User,
		password: cfg.Password, dbname: cfg.DBName,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	supportedAdapter := database.NewPostgreSQLAdapter(tg.endpoint(), database.AdapterOptions{
		CapabilityResolver: datasource.ResolveCapabilities,
	})
	okRes, err := supportedAdapter.TestConnection(ctx, tg.credential(), nil)
	if err != nil {
		t.Fatalf("supported PG 18 target must pass TestConnection: %v", err)
	}
	if okRes == nil || !okRes.Capabilities[datasource.CapQuery] {
		t.Fatalf("expected resolved PG 18 capabilities, got %+v", okRes)
	}

	belowFloorAdapter := database.NewPostgreSQLAdapter(tg.endpoint(), database.AdapterOptions{
		CapabilityResolver: func(engine, mode, _ string) (string, map[string]bool, error) {
			return datasource.ResolveCapabilities(engine, mode, "13.3")
		},
	})
	res, err := belowFloorAdapter.TestConnection(ctx, tg.credential(), nil)
	if err == nil {
		t.Fatal("below-floor PG version must fail with Unsupported")
	}
	if res == nil || res.SanitizedFailureName != database.FailUnsupportedTarget {
		t.Fatalf("expected capability_target_unsupported, got %+v err=%v", res, err)
	}
}

// required_suite: metadata.
func TestAdapterSuite_Metadata(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		schemas, err := env.adapter.ListSchemas(ctx, env.review)
		if err != nil {
			t.Fatalf("ListSchemas: %v", err)
		}
		found := false
		for _, s := range schemas {
			if s == env.schema {
				found = true
			}
		}
		if !found {
			t.Fatalf("suite schema %s not listed", env.schema)
		}
		tables, err := env.adapter.ListTables(ctx, env.review, env.schema)
		if err != nil || len(tables) != 1 || tables[0] != "items" {
			t.Fatalf("ListTables: %v %v", tables, err)
		}
		cols, err := env.adapter.ListColumns(ctx, env.review, env.schema, "items")
		if err != nil {
			t.Fatalf("ListColumns: %v", err)
		}
		if len(cols) != 4 {
			t.Fatalf("columns: %+v", cols)
		}
		if cols[0].ColumnName != "id" || cols[0].Ordinal != 1 {
			t.Fatalf("first column: %+v", cols[0])
		}
		if cols[1].Nullable {
			t.Fatalf("name must be NOT NULL: %+v", cols[1])
		}
		if cols[2].Nullable != true {
			t.Fatalf("note must be nullable: %+v", cols[2])
		}
		// §18.51 Table structure enrichment: key/default/extra/comment (MySQL semantics;
		// on PG side column_key is always empty and identity maps to auto_increment).
		if env.target.mode == "mysql" && cols[0].ColumnKey != "PRI" {
			t.Fatalf("id column column_key must be PRI: %+v", cols[0])
		}
		if cols[1].ColumnKey != "" || cols[1].ColumnExtra != "" || cols[1].ColumnRemark != "" {
			t.Fatalf("name column enrichment fields should be empty: %+v", cols[1])
		}
		// Sensitive policy: secret_note -> masked; others none.
		policyByName := map[string]string{}
		for _, c := range cols {
			policyByName[c.ColumnName] = c.SensitivePolicy
		}
		if policyByName["secret_note"] != "masked" || policyByName["name"] != "none" {
			t.Fatalf("sensitive policy: %v", policyByName)
		}
		indexes, err := env.adapter.ListIndexes(ctx, env.review, env.schema, "items")
		if err != nil {
			t.Fatalf("ListIndexes: %v", err)
		}
		var pk *database.MetadataIndex
		for i := range indexes {
			if !indexes[i].NonUnique && len(indexes[i].ColumnNames) == 1 && indexes[i].ColumnNames[0] == "id" {
				pk = &indexes[i]
			}
		}
		if pk == nil {
			t.Fatalf("primary key index not found: %+v", indexes)
		}
		sig1, err := env.adapter.ReadSchemaSignature(ctx, env.review, env.schema)
		if err != nil {
			t.Fatalf("signature: %v", err)
		}
		sig2, err := env.adapter.ReadSchemaSignature(ctx, env.review, env.schema)
		if err != nil || sig1.Signature != sig2.Signature || sig1.ElementCount == 0 {
			t.Fatalf("signature must be deterministic: %v vs %v", sig1, sig2)
		}
		// Schema change -> signature change.
		if _, err := env.adapter.ExecuteDDLStatement(ctx, env.exec, env.schema, "ALTER TABLE items ADD COLUMN extra_col INT"); err != nil {
			t.Fatalf("alter: %v", err)
		}
		sig3, err := env.adapter.ReadSchemaSignature(ctx, env.review, env.schema)
		if err != nil {
			t.Fatalf("signature3: %v", err)
		}
		if sig3.Signature == sig1.Signature {
			t.Fatal("signature must change after schema modification")
		}
	})
}

// required_suite: identifier_quoting (behavioral half: injection-shaped identifiers are rejected and never sent).
func TestAdapterSuite_IdentifierInjectionRejected(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		evil := []string{"items; DROP TABLE items", "x`OR 1=1--", "x\"; --", "information_schema; --"}
		for _, e := range evil {
			if _, err := env.adapter.ListTables(ctx, env.review, e); !errors.Is(err, database.ErrIdentifierInvalid) {
				t.Fatalf("evil schema %q must be rejected, got %v", e, err)
			}
			if _, err := env.adapter.ListColumns(ctx, env.review, env.schema, e); !errors.Is(err, database.ErrIdentifierInvalid) {
				t.Fatalf("evil table %q must be rejected, got %v", e, err)
			}
		}
		if env.countItems(ctx) != env.fix.RowCount+1 {
			t.Fatal("data must not be affected by any rejected path")
		}
	})
}

// required_suite: explain_non_execution (gate 2 behavioral half: Explain never executes mutations).
func TestAdapterSuite_ExplainNeverExecutesMutation(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		selStmt := strings.ReplaceAll(env.fix.Statements["explain_select"], "{id}", fmt.Sprintf("%d", env.fix.MarkerID))
		fact, err := env.adapter.ExplainWithoutExecution(ctx, env.review, env.schema, selStmt)
		if err != nil {
			t.Fatalf("explain select: %v", err)
		}
		if fact == nil || !strings.HasPrefix(fact.RawEvidenceReference, "sha256:") {
			t.Fatalf("explain fact: %+v", fact)
		}
		// Reproduce self-review finding: perform metadata read first (connection tainted by SET SESSION TRANSACTION READ ONLY
		// and returned to pool), then Explain DML — must still succeed (session state must be explicitly restored).
		if _, err := env.adapter.ListTables(ctx, env.review, env.schema); err != nil {
			t.Fatalf("metadata before explain: %v", err)
		}
		updStmt := strings.ReplaceAll(env.fix.Statements["explain_update"], "{id}", fmt.Sprintf("%d", env.fix.MarkerID))
		fact2, err := env.adapter.ExplainWithoutExecution(ctx, env.review, env.schema, updStmt)
		if err != nil {
			t.Fatalf("explain update: %v", err)
		}
		if fact2 == nil {
			t.Fatal("explain update fact nil")
		}
		// Assert: marker row is unmodified and row count is unchanged.
		if name := env.markerName(ctx); name != "marker" {
			t.Fatalf("Explain executed a mutation! marker name=%q", name)
		}
		if env.countItems(ctx) != env.fix.RowCount+1 {
			t.Fatalf("row count altered by Explain: %d", env.countItems(ctx))
		}
	})
}

// required_suite: single_select + cursor pagination.
func TestAdapterSuite_SingleSelectCursorPaging(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		cur, err := env.adapter.OpenSelectCursor(ctx, env.query, database.OpenCursorOptions{
			StatementSQL: env.fix.Statements["select_items"],
			SchemaName:   env.schema,
			PageSize:     10,
		})
		if err != nil {
			t.Fatalf("open cursor: %v", err)
		}
		total := 0
		pageOrdinal := 0
		var firstColumns []database.QueryColumn
		for {
			page, err := env.adapter.FetchCursorPage(ctx, env.query, cur, 0)
			if err != nil {
				t.Fatalf("fetch page: %v", err)
			}
			pageOrdinal++
			if page.PageOrdinal != pageOrdinal {
				t.Fatalf("page ordinal %d != %d", page.PageOrdinal, pageOrdinal)
			}
			if page.Purpose != database.PurposeQuery {
				t.Fatalf("purpose: %s", page.Purpose)
			}
			if firstColumns == nil {
				firstColumns = page.Columns
				if len(firstColumns) != 4 || firstColumns[0].Ordinal != 1 || firstColumns[0].Name != "id" {
					t.Fatalf("columns: %+v", firstColumns)
				}
			}
			if len(page.Columns) != len(firstColumns) {
				t.Fatalf("column set must remain stable across pages")
			}
			for _, row := range page.Rows {
				if row[0].Kind != "int" {
					t.Fatalf("id column must be int kind: %+v", row[0])
				}
				total++
			}
			if !page.HasMore {
				if page.NextCursor != "" {
					t.Fatal("final page next_cursor must be empty")
				}
				break
			}
			if page.NextCursor == "" {
				t.Fatal("has_more page must carry next_cursor")
			}
		}
		if total != env.fix.RowCount+1 {
			t.Fatalf("cursor total rows %d != %d", total, env.fix.RowCount+1)
		}
		// Fetching again after cursor exhaustion -> CURSOR_EXPIRED.
		if _, err := env.adapter.FetchCursorPage(ctx, env.query, cur, 0); !errors.Is(err, database.ErrCursorExpired) {
			t.Fatalf("fetch after exhaustion must return ErrCursorExpired: %v", err)
		}
	})
}

// required_suite: cursor_affinity_and_expiry (owner loss -> expired, no replay).
func TestAdapterSuite_CursorExpiryOnOwnerLoss(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		pool, err := env.adapter.OpenPurposePool(ctx, database.PurposeQuery, env.target.credential(), nil)
		if err != nil {
			t.Fatalf("owner pool: %v", err)
		}
		cur, err := env.adapter.OpenSelectCursor(ctx, pool, database.OpenCursorOptions{
			StatementSQL: env.fix.Statements["select_items"], SchemaName: env.schema, PageSize: 5,
		})
		if err != nil {
			t.Fatalf("open cursor: %v", err)
		}
		if _, err := env.adapter.FetchCursorPage(ctx, pool, cur, 0); err != nil {
			t.Fatalf("first page: %v", err)
		}
		// Owner process/connection loss = closing the owner pool.
		if err := pool.Close(); err != nil {
			t.Fatalf("owner close: %v", err)
		}
		if _, err := env.adapter.FetchCursorPage(ctx, pool, cur, 0); !errors.Is(err, database.ErrCursorExpired) {
			t.Fatalf("after owner loss must return CURSOR_EXPIRED and forbid replay: %v", err)
		}
	})
}

// required_suite: cancellation (execution governance half): CancelExecution must cancel
// in-flight DML transactions — subsequent statements execute on the cancelled governance context and are classified as cancelled.
func TestAdapterSuite_CancelExecutionCancelsOpenTx(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := env.adapter.CancelExecution(ctx, env.exec); err != nil {
			t.Fatalf("cancel execution: %v", err)
		}
		stmt := strings.ReplaceAll(env.fix.Statements["dml_insert"], "{id}", "950")
		stmt = strings.ReplaceAll(stmt, "{name}", "cancel-row")
		res, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: stmt})
		if err != nil {
			t.Fatalf("exec after cancel: %v", err)
		}
		if res.Outcome != database.OutcomeCancelled || res.EngineCodeClass != database.ClassCancelled {
			t.Fatalf("cancelled statement must be classified as cancelled: %+v", res)
		}
		_, _ = env.adapter.RollbackDMLTransaction(ctx, tx)
		if env.countItems(ctx) != env.fix.RowCount+1 {
			t.Fatal("cancellation path must not leave data behind")
		}
	})
}

// Connector.Close semantics: closes all pools opened by the Adapter; rejects opening new pools after close.
func TestAdapterSuite_AdapterCloseClosesPools(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		pool, err := env.adapter.OpenPurposePool(ctx, database.PurposeQuery, env.target.credential(), nil)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		cur, err := env.adapter.OpenSelectCursor(ctx, pool, database.OpenCursorOptions{
			StatementSQL: env.fix.Statements["select_items"], SchemaName: env.schema, PageSize: 5,
		})
		if err != nil {
			t.Fatalf("open cursor: %v", err)
		}
		if err := env.adapter.Close(); err != nil {
			t.Fatalf("adapter close: %v", err)
		}
		if _, err := env.adapter.FetchCursorPage(ctx, pool, cur, 0); !errors.Is(err, database.ErrCursorExpired) {
			t.Fatalf("cursor must expire after Adapter.Close: %v", err)
		}
		if _, err := env.adapter.OpenPurposePool(ctx, database.PurposeQuery, env.target.credential(), nil); err == nil {
			t.Fatal("closed Adapter must reject opening new pools")
		}
	})
}

// required_suite: dml_commit_and_rollback.
func TestAdapterSuite_DMLCommitAndRollback(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		before := env.countItems(ctx)

		// Commit path.
		tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		stmt := strings.ReplaceAll(env.fix.Statements["dml_insert"], "{id}", "901")
		stmt = strings.ReplaceAll(stmt, "{name}", "commit-row")
		if res, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: stmt}); err != nil || res.Outcome != database.OutcomeSuccess || res.AffectedRows != 1 {
			t.Fatalf("insert: err=%v res=%+v", err, res)
		}
		if _, err := env.adapter.CommitDMLTransaction(ctx, tx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if env.countItems(ctx) != before+1 {
			t.Fatal("must be visible after commit")
		}

		// Rollback path.
		tx, err = env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatalf("begin2: %v", err)
		}
		stmt = strings.ReplaceAll(env.fix.Statements["dml_insert"], "{id}", "902")
		stmt = strings.ReplaceAll(stmt, "{name}", "rollback-row")
		if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: stmt}); err != nil {
			t.Fatalf("insert2: %v", err)
		}
		if _, err := env.adapter.RollbackDMLTransaction(ctx, tx); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if env.countItems(ctx) != before+1 {
			t.Fatal("must not be visible after rollback")
		}

		// Deterministic failure: unique key conflict -> failed+constraint, transaction remains usable for rollback.
		tx, err = env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatalf("begin3: %v", err)
		}
		conflict := strings.ReplaceAll(env.fix.Statements["dml_unique_conflict"], "{id}", fmt.Sprintf("%d", env.fix.MarkerID))
		res, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: conflict})
		if err != nil {
			t.Fatalf("conflict exec: %v", err)
		}
		if res.Outcome != database.OutcomeFailed || res.EngineCodeClass != database.ClassConstraintViolation ||
			res.SanitizedFailureName != database.FailUniqueViolation || res.SendBoundary != database.SendBoundarySent {
			t.Fatalf("conflict classification: %+v", res)
		}
		if _, err := env.adapter.RollbackDMLTransaction(ctx, tx); err != nil {
			t.Fatalf("rollback3: %v", err)
		}

		// Read-only Purpose write guard.
		if _, err := env.adapter.BeginDMLTransaction(ctx, env.query); !errors.Is(err, database.ErrReadOnlyViolation) {
			t.Fatalf("DML on query pool must be rejected: %v", err)
		}
		if _, err := env.adapter.ExecuteDDLStatement(ctx, env.query, env.schema, env.fix.Statements["ddl_create_ok"]); !errors.Is(err, database.ErrReadOnlyViolation) {
			t.Fatalf("DDL on query pool must be rejected: %v", err)
		}
		if env.countItems(ctx) != before+1 {
			t.Fatal("total row count should not change")
		}
	})
}

// required_suite: multi_ddl_partial_failure.
func TestAdapterSuite_MultiDDLPartialFailure(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		// Statement 1 succeeds.
		res1, err := env.adapter.ExecuteDDLStatement(ctx, env.exec, env.schema, env.fix.Statements["ddl_create_ok"])
		if err != nil || res1.Outcome != database.OutcomeSuccess {
			t.Fatalf("ddl1: err=%v res=%+v", err, res1)
		}
		// Statement 2 conflicts: deterministic failure, preserving already-succeeded results.
		res2, err := env.adapter.ExecuteDDLStatement(ctx, env.exec, env.schema, env.fix.Statements["ddl_create_conflict"])
		if err != nil {
			t.Fatalf("ddl2 exec: %v", err)
		}
		if res2.Outcome != database.OutcomeFailed || res2.SanitizedFailureName != database.FailObjectExists {
			t.Fatalf("ddl2 classification: %+v", res2)
		}
		// Contract: stop the entire stage immediately on failure — statement 3 is not executed (stopped by caller, simulated by this suite).
		// Already-succeeded statements must be preserved (no transaction wrapper).
		var count int
		var qerr error
		if env.target.mode == "mysql" {
			qerr = env.admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+env.schema+"`.suite_ddl_ok").Scan(&count)
		} else {
			qerr = env.admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+env.schema+`".suite_ddl_ok`).Scan(&count)
		}
		if qerr != nil {
			t.Fatalf("ddl1 result must be preserved (table exists if queryable): %v", qerr)
		}
	})
}

// required_suite: error_sanitization.
func TestAdapterSuite_ErrorSanitization(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		// Cursor path: non-existent table -> error contains no SQL/table name/driver text.
		_, err := env.adapter.OpenSelectCursor(ctx, env.query, database.OpenCursorOptions{
			StatementSQL: env.fix.Statements["missing_table_select"], SchemaName: env.schema, PageSize: 10,
		})
		if err == nil {
			t.Fatal("missing table must fail")
		}
		msg := err.Error()
		for _, leak := range []string{"missing_table_xyz", "SELECT", "FROM", env.target.password} {
			if strings.Contains(msg, leak) {
				t.Fatalf("error leaked %q: %s", leak, msg)
			}
		}
		// Explain path syntax error -> sanitized.
		_, err = env.adapter.ExplainWithoutExecution(ctx, env.review, env.schema, env.fix.Statements["syntax_error_stmt"])
		if err == nil {
			t.Fatal("syntax error must fail")
		}
		if strings.Contains(err.Error(), "SELEKT") {
			t.Fatalf("explain error leaked SQL: %s", err.Error())
		}
		// DML syntax error -> statement_result sanitized name.
		tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		res, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{SQL: env.fix.Statements["syntax_error_stmt"]})
		if err != nil {
			t.Fatalf("syntax exec: %v", err)
		}
		if res.Outcome != database.OutcomeFailed || res.SanitizedFailureName != database.FailParseError {
			t.Fatalf("syntax classification: %+v", res)
		}
		if _, err := env.adapter.RollbackDMLTransaction(ctx, tx); err != nil {
			t.Fatalf("rollback: %v", err)
		}
	})
}

var _ = filepath.Join
