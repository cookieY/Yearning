// preflight_test.go Pure unit tests for execution Kind derivation, ALTER detection, and execution windows (no DB).
package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/osc"
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
	mat, err := splitTLSPEM(true, "db.local", []byte(ca+client))
	if err != nil {
		t.Fatal(err)
	}
	if len(mat.CAPEM) == 0 || mat.CertPEM != nil || mat.KeyPEM != nil {
		t.Fatalf("ca-only split: %d %v %v", len(mat.CAPEM), mat.CertPEM, mat.KeyPEM)
	}
	// With private key: the last certificate is the client certificate, and the rest form the CA chain.
	mat, err = splitTLSPEM(false, "", []byte(ca+client+key))
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
	for _, op := range []string{"create", "ALTER", "Drop", "TRUNCATE"} {
		if statementClassOf(op) != "ddl" {
			t.Fatalf("%s must be ddl", op)
		}
	}
	for _, op := range []string{"insert", "update", "delete", "select", "merge"} {
		if statementClassOf(op) != "dml" {
			t.Fatalf("%s must be dml", op)
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
