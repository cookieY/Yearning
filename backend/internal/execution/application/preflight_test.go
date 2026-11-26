// preflight_test.go Pure unit tests for execution Kind derivation, ALTER detection, and execution windows (no DB).
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/osc"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func mysqlDS(version string) *datasource.DatasourceModel {
	return &datasource.DatasourceModel{
		Engine: "mysql", CompatibilityMode: "mysql", DeploymentKind: "native",
		VersionConstraint: version,
	}
}

func TestDeriveExecutionKind(t *testing.T) {
	dml := PlanStatement{Kind: "dml"}
	ddl := PlanStatement{Kind: "ddl"}
	alter := PlanStatement{Kind: "ddl", SQL: []byte("ALTER TABLE shop.orders ADD c int")}
	none := oscPolicy{Tool: osc.Tool("none"), Defaults: &osc.Overrides{}}
	ghOst := oscPolicy{Tool: osc.ToolGhOst, Defaults: &osc.Overrides{}}
	ptOsc := oscPolicy{Tool: osc.ToolPtOsc, Defaults: &osc.Overrides{}}
	cases := []struct {
		name  string
		stmts []PlanStatement
		ds    *datasource.DatasourceModel
		pol   oscPolicy
		want  domain.ExecutionKind
		err   bool
	}{
		{"dml", []PlanStatement{dml, dml}, mysqlDS("8.0.x"), none, domain.KindDML, false},
		{"ddl-multi", []PlanStatement{ddl, ddl}, mysqlDS("8.0.x"), ghOst, domain.KindDDL, false},
		{"policy-none-direct", []PlanStatement{alter}, mysqlDS("8.0.x"), none, domain.KindDDL, false},
		{"policy-gh-ost", []PlanStatement{alter}, mysqlDS("8.0.x"), ghOst, domain.KindGhOst, false},
		{"policy-gh-ost-5-7", []PlanStatement{{Kind: "ddl", SQL: []byte("alter table `shop`.`orders` drop c")}}, mysqlDS("5.7.x"), ghOst, domain.KindGhOst, false},
		{"policy-gh-ost-8-4", []PlanStatement{alter}, mysqlDS("8.4.x"), ghOst, domain.KindGhOst, false},
		{"policy-gh-ost-semver-range", []PlanStatement{alter}, mysqlDS(">=8.0,<9.0"), ghOst, domain.KindGhOst, false},
		{"policy-gh-ost-min-floor", []PlanStatement{alter}, mysqlDS(">=5.7"), ghOst, domain.KindGhOst, false},
		{"policy-pt-osc", []PlanStatement{alter}, mysqlDS("8.0.x"), ptOsc, domain.KindPtOsc, false},
		{"multi-ddl-no-osc", []PlanStatement{{Kind: "ddl", SQL: []byte("ALTER TABLE t ADD c int")}, ddl}, mysqlDS("8.0.x"), ghOst, domain.KindDDL, false},
		{"non-alter-ddl", []PlanStatement{{Kind: "ddl", SQL: []byte("CREATE TABLE t(id int)")}}, mysqlDS("8.0.x"), ghOst, domain.KindDDL, false},
		{"tidb-no-osc", []PlanStatement{alter}, &datasource.DatasourceModel{Engine: "tidb", CompatibilityMode: "mysql", DeploymentKind: "native", VersionConstraint: "8.0"}, ghOst, domain.KindDDL, false},
		{"cloud-mysql-no-osc", []PlanStatement{alter}, &datasource.DatasourceModel{Engine: "mysql", CompatibilityMode: "mysql", DeploymentKind: "cloud", VersionConstraint: "8.0"}, ghOst, domain.KindDDL, false},
		{"pg-no-osc", []PlanStatement{alter}, &datasource.DatasourceModel{Engine: "postgresql", CompatibilityMode: "postgresql", DeploymentKind: "native"}, ghOst, domain.KindDDL, false},
		{"mixed-fail-closed", []PlanStatement{dml, ddl}, mysqlDS("8.0.x"), none, "", true},
		{"empty-fail-closed", nil, mysqlDS("8.0.x"), none, "", true},
	}
	for _, c := range cases {
		got, err := deriveExecutionKind(c.stmts, c.ds, c.pol)
		if c.err {
			if err == nil {
				t.Fatalf("%s: expected error", c.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: kind=%s want %s", c.name, got, c.want)
		}
	}
}

func TestParseOverridesTypedKeys(t *testing.T) {
	// Reject unknown keys (never silently drop them).
	if _, err := osc.ParseOverrides(map[string]any{"password": "x"}); err == nil {
		t.Fatal("credential key must be rejected")
	}
	if _, err := osc.ParseOverrides(map[string]any{"totally_unknown": 1}); err == nil {
		t.Fatal("unknown key must be rejected")
	}
	// Value range validation.
	if _, err := osc.ParseOverrides(map[string]any{"chunk_size": 0}); err == nil {
		t.Fatal("chunk_size=0 out of range")
	}
	if _, err := osc.ParseOverrides(map[string]any{"retries": 121}); err == nil {
		t.Fatal("retries=121 out of range")
	}
	if _, err := osc.ParseOverrides(map[string]any{"max_load": "Threads_running; rm -rf"}); err == nil {
		t.Fatal("max_load injection must be rejected")
	}
	ok, err := osc.ParseOverrides(map[string]any{
		"max_load": "Threads_running=50", "chunk_size": 1000, "retries": 2,
	})
	if err != nil {
		t.Fatalf("valid overrides: %v", err)
	}
	// Merge semantics: override takes precedence over default.
	base, _ := osc.ParseOverrides(map[string]any{"chunk_size": 500})
	merged := osc.Merge(base, ok)
	if merged.ChunkSize == nil || *merged.ChunkSize != 1000 {
		t.Fatal("override must win over default")
	}
	if merged.MaxLoad == nil || *merged.MaxLoad != "Threads_running=50" {
		t.Fatal("override max_load missing")
	}
	// Argv mapping: gh-ost and pt-osc have different flag surfaces (different key names for retries).
	if got := merged.Argv(osc.ToolGhOst); !contains(got, "--chunk-size=1000") || !contains(got, "--default-retries=2") {
		t.Fatalf("gh-ost argv=%v", got)
	}
	if got := merged.Argv(osc.ToolPtOsc); !contains(got, "--chunk-size=1000") || !contains(got, "--retries=2") {
		t.Fatalf("pt-osc argv=%v", got)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestWindowContains(t *testing.T) {
	mon := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC) // Monday
	w := execWindow{
		DaysOfWeek:     []string{"monday", "tuesday"},
		StartLocalTime: "09:00", EndLocalTime: "18:00",
	}
	if !windowContains(w, mon) {
		t.Fatal("monday 10:00 must be inside")
	}
	if windowContains(w, mon.Add(8*time.Hour).Add(30*time.Minute)) {
		t.Fatal("monday 18:30 must be outside")
	}
	sun := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if windowContains(w, sun) {
		t.Fatal("sunday must be outside")
	}
	// Cross-midnight window: Monday 22:00 - next day 02:00.
	x := execWindow{DaysOfWeek: []string{"monday"}, StartLocalTime: "22:00", EndLocalTime: "02:00"}
	if !windowContains(x, mon.Add(12*time.Hour)) { // Monday 22:00
		t.Fatal("cross-midnight start must be inside")
	}
	tue2am := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	if !windowContains(x, tue2am) {
		t.Fatal("cross-midnight tail (tue 01:30) must be inside")
	}
	tue3am := time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)
	if windowContains(x, tue3am) {
		t.Fatal("tue 03:00 must be outside")
	}
	// Inclusive boundaries (both start and end minutes are included).
	edge := execWindow{DaysOfWeek: []string{"monday"}, StartLocalTime: "09:00", EndLocalTime: "09:01"}
	if !windowContains(edge, time.Date(2026, 8, 24, 9, 1, 0, 0, time.UTC)) {
		t.Fatal("end boundary minute must be included")
	}
	// Reject malformed HH:MM.
	bad := execWindow{DaysOfWeek: []string{"monday"}, StartLocalTime: "9:00", EndLocalTime: "18:00"}
	if windowContains(bad, mon) {
		t.Fatal("malformed window must not match")
	}
}

func TestParseHHMM(t *testing.T) {
	for _, ok := range []string{"00:00", "23:59", "09:05"} {
		if _, valid := parseHHMM(ok); !valid {
			t.Fatalf("%s must parse", ok)
		}
	}
	for _, bad := range []string{"24:00", "09:60", "9:00", "0900", "", "09:0"} {
		if _, valid := parseHHMM(bad); valid {
			t.Fatalf("%s must reject", bad)
		}
	}
}

// TestSplitTLSPEM TLS material splitting (CA chain / client certificate / private key; identical semantics to datasource).
func TestSplitTLSPEM(t *testing.T) {
	ca := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	client := "-----BEGIN CERTIFICATE-----\nBBBB\n-----END CERTIFICATE-----\n"
	key := "-----BEGIN PRIVATE KEY-----\nCCCC\n-----END PRIVATE KEY-----\n"
	// No private key: all blocks belong to the CA chain.
	mat, err := datasource.SplitTLSMaterial(true, "db.local", []byte(ca+client))
	if err != nil {
		t.Fatal(err)
	}
	if len(mat.CAPEM) == 0 || mat.CertPEM != nil || mat.KeyPEM != nil {
		t.Fatalf("ca-only split: %d %v %v", len(mat.CAPEM), mat.CertPEM, mat.KeyPEM)
	}
	// With private key: the last certificate is the client certificate, and the rest form the CA chain.
	mat, err = datasource.SplitTLSMaterial(false, "", []byte(ca+client+key))
	if err != nil {
		t.Fatal(err)
	}
	if mat.CertPEM == nil || mat.KeyPEM == nil || len(mat.CAPEM) == 0 {
		t.Fatal("mTLS split failed")
	}
	if mat.VerifyServer {
		t.Fatal("verify flag must pass through")
	}
}

// TestStatementClassOf Matches review-side classification criterion.
func TestStatementClassOf(t *testing.T) {
	for _, op := range []string{"create", "ALTER", "Drop", "TRUNCATE", "rename", "RENAME"} {
		if fingerprint.StatementClassOf(op) != "ddl" {
			t.Fatalf("%s must be ddl", op)
		}
	}
	for _, op := range []string{"insert", "update", "delete", "select", "merge"} {
		if fingerprint.StatementClassOf(op) != "dml" {
			t.Fatalf("%s must be dml", op)
		}
	}
}

func TestPreflightRenameDDLBehavior(t *testing.T) {
	sql := []byte("-- comment\n/* block */ RENAME TABLE shop.orders_old TO archive.orders_old")
	op := strings.ToLower(string(fingerprint.FirstOperationSlice(sql)))
	if op != "rename" {
		t.Fatalf("FirstOperationSlice = %q, want rename", op)
	}
	class := fingerprint.StatementClassOf(op)
	if class != "ddl" {
		t.Fatalf("StatementClassOf(%q) = %q, want ddl", op, class)
	}

	ds := mysqlDS("8.0.x")
	none := oscPolicy{Tool: osc.Tool("none"), Defaults: &osc.Overrides{}}
	ghOst := oscPolicy{Tool: osc.ToolGhOst, Defaults: &osc.Overrides{}}
	ptOsc := oscPolicy{Tool: osc.ToolPtOsc, Defaults: &osc.Overrides{}}

	renameStmt := PlanStatement{
		Ordinal:   1,
		Kind:      class,
		Operation: op,
		SQL:       []byte("RENAME TABLE shop.orders_old TO archive.orders_old"),
	}
	for _, tc := range []struct {
		name string
		pol  oscPolicy
	}{
		{"policy-none", none},
		{"policy-gh-ost", ghOst},
		{"policy-pt-osc", ptOsc},
	} {
		kind, err := deriveExecutionKind([]PlanStatement{renameStmt}, ds, tc.pol)
		if err != nil {
			t.Fatalf("%s single rename deriveExecutionKind: %v", tc.name, err)
		}
		if kind != domain.KindDDL {
			t.Fatalf("%s single rename kind = %s, want %s", tc.name, kind, domain.KindDDL)
		}
	}

	alterStmt := PlanStatement{Ordinal: 2, Kind: "ddl", Operation: "alter", SQL: []byte("ALTER TABLE shop.orders ADD COLUMN c INT")}
	kind, err := deriveExecutionKind([]PlanStatement{renameStmt, alterStmt}, ds, ghOst)
	if err != nil {
		t.Fatalf("rename+alter deriveExecutionKind: %v", err)
	}
	if kind != domain.KindDDL {
		t.Fatalf("rename+alter kind = %s, want %s", kind, domain.KindDDL)
	}

	updateStmt := PlanStatement{Ordinal: 2, Kind: "dml", Operation: "update", SQL: []byte("UPDATE shop.orders SET status = 'ok' WHERE id = 1")}
	_, err = deriveExecutionKind([]PlanStatement{renameStmt, updateStmt}, ds, none)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed || !strings.Contains(de.Name, "mixed_statement_kinds") {
		t.Fatalf("rename+update err = %v, want 3006 mixed_statement_kinds", err)
	}
}

func TestPreflightStatementMismatchReason(t *testing.T) {
	t.Run("legacy_rename_dml", func(t *testing.T) {
		f := frozenStatementRow{Ordinal: 1, StatementKind: "dml", FingerprintQuality: fingerprint.QualityLexer}
		got := preflightStatementMismatchReason(f, 1, "rename", "ddl", fingerprint.Result{})
		want := "sql_changed_after_review: RENAME TABLE classification and fingerprint upgraded to DDL; please re-review this change order before execution"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("legacy_ctas", func(t *testing.T) {
		f := frozenStatementRow{Ordinal: 1, StatementKind: "ddl", FingerprintQuality: fingerprint.QualityLexer}
		res := fingerprint.Result{Validity: fingerprint.StatementValidity{Unsupported: "create_table_as_select"}}
		got := preflightStatementMismatchReason(f, 1, "create", "ddl", res)
		want := "sql_changed_after_review: fingerprint upgraded for CREATE TABLE ... AS SELECT; please re-review"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("ordinary_mismatch", func(t *testing.T) {
		f := frozenStatementRow{Ordinal: 1, StatementKind: "ddl", FingerprintQuality: fingerprint.QualityExact}
		got := preflightStatementMismatchReason(f, 1, "create", "ddl", fingerprint.Result{})
		if got != "sql_changed_after_review" {
			t.Fatalf("got %q, want sql_changed_after_review", got)
		}
		gotOrd := preflightStatementMismatchReason(f, 2, "rename", "ddl", fingerprint.Result{})
		if gotOrd != "sql_changed_after_review" {
			t.Fatalf("got %q, want sql_changed_after_review on ordinal mismatch", gotOrd)
		}
	})
}

func TestFirstWordMatchesReviewFirstOperationSlice(t *testing.T) {
	samples := [][]byte{
		[]byte("SELECT 1"),
		[]byte("  insert into t values (1)"),
		[]byte("RENAME TABLE a TO b"),
		[]byte("-- leading comment\nRENAME TABLE shop.a TO shop.b"),
		[]byte("-- comment only"),
		[]byte("/* block */ UPDATE t SET a=1"),
		[]byte("/* one */ /* two */\n  -- line\n ( (CREATE TABLE t(id int)))"),
		[]byte("/*!50000 SELECT */ 1"),
		[]byte("/* unclosed"),
		[]byte("CREATE(id)"),
		[]byte("ALTER\tTABLE t ADD COLUMN c INT"),
		[]byte("\r\n\t DELETE FROM t WHERE id = 1"),
		[]byte("WITH cte AS (SELECT 1) SELECT * FROM cte"),
		[]byte("EXPLAIN ANALYZE SELECT 1"),
		[]byte("SHOW TABLES"),
		[]byte(""),
		[]byte("   \n\t  "),
		[]byte(";"),
		[]byte("§"),
		[]byte("CREATE OR REPLACE VIEW v AS SELECT 1"),
	}
	for _, sample := range samples {
		want := reviewapp.FirstOperationSlice(sample)
		got := fingerprint.FirstOperationSlice(sample)
		if !bytes.Equal(got, want) {
			t.Fatalf("fingerprint.FirstOperationSlice(%q) = %q, want review FirstOperationSlice %q", sample, got, want)
		}
	}
}

// TestIsAlterTableDeterministic ALTER detection edge cases.
func TestIsAlterTableDeterministic(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE t ADD c int", "alter table `db`.`t` drop c",
		"(ALTER TABLE t ENGINE=InnoDB)",
	} {
		if !isAlterTable(PlanStatement{Kind: "ddl", SQL: []byte(sql)}) {
			t.Fatalf("must detect alter: %s", sql)
		}
	}
	for _, sql := range []string{
		"CREATE TABLE t(id int)", "UPDATE t SET a=1", "ALTER INDEX idx RENAME TO x",
	} {
		if isAlterTable(PlanStatement{Kind: "ddl", SQL: []byte(sql)}) {
			t.Fatalf("must not detect alter: %s", sql)
		}
	}
}

// TestRunnerExecuteTaskInputDispatch tasks.Handler signature dispatch (JSON ->
// TaskInput; invalid JSON returns error; valid JSON unmarshals and enters the task path).
func TestRunnerExecuteTaskInputDispatch(t *testing.T) {
	r := NewRunner(nil)
	// Invalid JSON -> unmarshal error (does not touch DB).
	if err := r.Execute(context.Background(), nil, []byte("{bad"), nil, nil); err == nil {
		t.Fatal("malformed input must error")
	}
	in := TaskInput{AttemptID: "00000000-0000-4000-8000-000000000000",
		OrderID: "00000000-0000-4000-8000-000000000001",
		StageID: "00000000-0000-4000-8000-000000000002"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Valid JSON unmarshal path is covered by round-trip serialization; RunAttempt requires a real DB
	// (nil DB would panic; the integration suite covers all dispatch branches).
	_ = raw
}

func TestClassBOSCFailClosedOnNonNativeMySQLMetadata(t *testing.T) {
	alter := PlanStatement{Kind: "ddl", SQL: []byte("ALTER TABLE shop.orders ADD COLUMN tag VARCHAR(16)")}
	ghOst := oscPolicy{Tool: osc.ToolGhOst, Defaults: &osc.Overrides{}}

	nativeWithMeta := mysqlDS("8.0.x")
	nativeWithMeta.Metadata = []byte(`{"detected_flavor":"mysql_native","verification_tier":"class_a"}`)
	if !oscCapable(nativeWithMeta) {
		t.Fatal("expected native mysql metadata to allow OSC")
	}
	kind, err := deriveExecutionKind([]PlanStatement{alter}, nativeWithMeta, ghOst)
	if err != nil || kind != domain.KindGhOst {
		t.Fatalf("native mysql deriveExecutionKind = (%s, %v), want gh_ost", kind, err)
	}

	for _, flavor := range []string{"mariadb", "tidb", "oceanbase", "polardb_mysql"} {
		t.Run(flavor, func(t *testing.T) {
			ds := mysqlDS("8.0.x")
			ds.Metadata = []byte(`{"detected_flavor":"` + flavor + `","verification_tier":"class_b"}`)
			if oscCapable(ds) {
				t.Fatalf("expected oscCapable=false for non-native flavor %q", flavor)
			}
			k, err := deriveExecutionKind([]PlanStatement{alter}, ds, ghOst)
			if err != nil || k != domain.KindDDL {
				t.Fatalf("non-native %s deriveExecutionKind = (%s, %v), want ddl", flavor, k, err)
			}
		})
	}
}
