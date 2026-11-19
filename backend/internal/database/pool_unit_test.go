package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

type recordingExecer struct {
	stmts []string
	fail  map[string]error
}

func (r *recordingExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	r.stmts = append(r.stmts, query)
	if r.fail != nil {
		if err, ok := r.fail[query]; ok {
			return nil, err
		}
	}
	return nil, nil
}

func TestSessionDefaultsAllowlist(t *testing.T) {
	p := &PurposePool{
		engine:  "postgresql",
		purpose: PurposeQuery,
		opts:    AdapterOptions{StatementTimeoutMS: 30000},
	}
	rec := &recordingExecer{}
	if err := p.applySessionDefaults(context.Background(), rec); err != nil {
		t.Fatalf("allowlisted statement_timeout: %v", err)
	}
	if len(rec.stmts) != 1 || !strings.HasPrefix(rec.stmts[0], "SET statement_timeout") {
		t.Fatalf("unexpected stmts %v", rec.stmts)
	}
	// MySQL dialect mapping: statement_timeout -> max_execution_time(ms),
	// lock_timeout -> innodb_lock_wait_timeout(seconds, rounded up).
	mp := &PurposePool{
		engine:  "mysql",
		purpose: PurposeExecution,
		opts:    AdapterOptions{StatementTimeoutMS: 30000, LockTimeoutMS: 1500},
	}
	mrec := &recordingExecer{}
	if err := mp.applySessionDefaults(context.Background(), mrec); err != nil {
		t.Fatalf("mysql mapped session vars: %v", err)
	}
	joined := strings.Join(mrec.stmts, ";")
	if !strings.Contains(joined, "SET max_execution_time = 30000") {
		t.Fatalf("mysql statement_timeout mapping: %v", mrec.stmts)
	}
	if !strings.Contains(joined, "SET innodb_lock_wait_timeout = 2") {
		t.Fatalf("mysql lock_timeout mapping (1500ms→2s): %v", mrec.stmts)
	}
	// query purpose does not allow lock_timeout (including MySQL equivalent variables).
	p2 := &PurposePool{engine: "postgresql", purpose: PurposeQuery, opts: AdapterOptions{LockTimeoutMS: 1000}}
	rec2 := &recordingExecer{}
	if err := p2.applySessionDefaults(context.Background(), rec2); err != nil {
		t.Fatalf("lock_timeout for query skipped silently: %v", err)
	}
	if len(rec2.stmts) != 0 {
		t.Fatalf("query purpose must not set lock_timeout, got %v", rec2.stmts)
	}
	// Value allowlist rejects injection shapes.
	if !safeSessionValue("5000ms") {
		t.Fatal("5000ms should pass")
	}
	if safeSessionValue("1; DROP TABLE x") {
		t.Fatal("injection-shaped value must fail")
	}
	// OceanBase MySQL mode does not implement max_execution_time or innodb_lock_wait_timeout
	// and returns MySQL error 1193 (ER_UNKNOWN_SYSTEM_VARIABLE). applySessionDefaults must
	// tolerate 1193 while still failing closed on non-1193 errors.
	obRec := &recordingExecer{
		fail: map[string]error{
			"SET max_execution_time = 30000":   &mysql.MySQLError{Number: 1193, Message: "Unknown system variable 'max_execution_time'"},
			"SET innodb_lock_wait_timeout = 2": &mysql.MySQLError{Number: 1193, Message: "Unknown system variable 'innodb_lock_wait_timeout'"},
		},
	}
	if err := mp.applySessionDefaults(context.Background(), obRec); err != nil {
		t.Fatalf("OceanBase unknown system variable 1193 must be tolerated: %v", err)
	}
	fatalRec := &recordingExecer{
		fail: map[string]error{
			"SET max_execution_time = 30000": &mysql.MySQLError{Number: 1045, Message: "Access denied"},
		},
	}
	if err := mp.applySessionDefaults(context.Background(), fatalRec); err == nil {
		t.Fatal("non-1193 session error must fail closed")
	}
}

func TestValidateCursorStatement(t *testing.T) {
	ok := []string{
		"SELECT id FROM t", "select 1", "WITH c AS (SELECT 1) SELECT * FROM c", "  select\n1",
		// BUG-011: semicolons inside comments and string literals are not statement separators. Raw-text
		// strings.Contains(stmt, ";") previously falsely rejected all of these as 4011.
		"select 1 -- note; here",
		"select 1 /* block; semi */",
		"select 'a;b'",
		"select 1 # note; here",
		"/* leading */ select 1",
		"select 1 -- trailing",
	}
	for _, s := range ok {
		if err := validateCursorStatement(s, false); err != nil {
			t.Fatalf("%q should pass: %v", s, err)
		}
	}
	bad := []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET x = 1",
		"SELECT 1; DROP TABLE t",
		"SELECT 1; ",
		"",
		"select 1; select 2",
	}
	for _, s := range bad {
		if err := validateCursorStatement(s, false); err == nil {
			t.Fatalf("%q must be rejected", s)
		}
	}
}

func TestNormalizeValueKinds(t *testing.T) {
	if v := normalizeValue(nil); v.Kind != "null" {
		t.Fatal("null kind")
	}
	if v := normalizeValue(true); v.Kind != "bool" || v.Text != "true" {
		t.Fatal("bool kind")
	}
	if v := normalizeValue(int64(42)); v.Kind != "int" || v.Text != "42" {
		t.Fatal("int kind")
	}
	if v := normalizeValue([]byte("42")); v.Kind != "int" || v.Text != "42" {
		t.Fatalf("mysql text-protocol int must normalize to int kind, got %+v", v)
	}
	if v := normalizeValue([]byte("1.5")); v.Kind != "float" || v.Text != "1.5" {
		t.Fatalf("float kind: %+v", v)
	}
	if v := normalizeValue([]byte("hello")); v.Kind != "string" || v.Text != "hello" {
		t.Fatal("string kind")
	}
	big := make([]byte, maxCellBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if v := normalizeValue(big); v.Kind != "bytes_placeholder" || v.Text != cellPlaceholder {
		t.Fatal("oversize cell must be placeholder")
	}
	if v := normalizeValue([]byte{0xff, 0xfe}); v.Kind != "bytes_placeholder" {
		t.Fatal("non-utf8 cell must be placeholder")
	}
}

func TestOpenPoolRejectsUnknownEngine(t *testing.T) {
	_, err := openPool(context.Background(), "oracle", Endpoint{Host: "x", Port: 1, CompatibilityMod: "oracle"},
		PurposeQuery, Credential{}, nil, AdapterOptions{})
	if !errors.Is(err, ErrUnsupportedTarget) {
		t.Fatalf("unknown engine must fail closed, got %v", err)
	}
	_, err = openPool(context.Background(), "mysql", Endpoint{Host: "x", Port: 1, CompatibilityMod: "mysql"},
		"bogus", Credential{}, nil, AdapterOptions{})
	if err == nil {
		t.Fatal("invalid purpose must fail")
	}
}

func TestReadOnlyPurposeGuards(t *testing.T) {
	if !ReadOnlyPurpose(PurposeReview) || !ReadOnlyPurpose(PurposeQuery) {
		t.Fatal("review/query read-only")
	}
	if ReadOnlyPurpose(PurposeExecution) {
		t.Fatal("execution writable")
	}
}

func TestStatementFailureClassification(t *testing.T) {
	// Deterministic rejection -> failed (transaction is not sealed; caller may roll back).
	dupErr := &mysql.MySQLError{Number: 1062}
	res := statementFailure("mysql", time.Now().UTC(), dupErr)
	if res.Outcome != OutcomeFailed || res.SanitizedFailureName != FailUniqueViolation {
		t.Fatalf("deterministic failure: %+v", res)
	}
	// Unknown driver error -> result_unknown (caller must seal and release resources).
	res = statementFailure("mysql", time.Now().UTC(), errors.New("connection reset by peer"))
	if res.Outcome != OutcomeResultUnknown || res.SendBoundary != SendBoundarySent {
		t.Fatalf("ambiguous failure: %+v", res)
	}
	// Cancellation -> cancelled.
	res = statementFailure("postgresql", time.Now().UTC(), context.Canceled)
	if res.Outcome != OutcomeCancelled {
		t.Fatalf("cancelled: %+v", res)
	}
}

// TestAdapterSuiteFixturePathResolves Proves in CI that the unified Suite fixture path resolves
// (Copilot questioned this relative path twice; this assertion runs even without the integration build tag).
func TestAdapterSuiteFixturePathResolves(t *testing.T) {
	candidates := []string{
		"../../../testdata/sql/adapter-suite-fixtures.json",
		"../../testdata/sql/adapter-suite-fixtures.json",
	}
	found := false
	for _, rel := range candidates {
		if _, err := os.Stat(rel); err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("adapter suite fixtures not resolvable from package directory")
	}
}

// TestValidateCursorStatementDangerousCalls SEC-0008 adapter-layer second line of defense:
// the pre-execution cursor guard equally rejects side-effecting function calls in quoted identifier
// form (bare word / quoted / schema-qualified / case variants) without false positives on non-function positions.
func TestValidateCursorStatementDangerousCalls(t *testing.T) {
	bad := []struct {
		stmt     string
		postgres bool
	}{
		{`select "pg_read_file"('/etc/passwd')`, true},
		{`select pg_catalog."pg_sleep"(10)`, true},
		{`select "PG_SLEEP"(1)`, true},
		{`select "pg_catalog"."pg_read_file"('/x')`, true},
		{"select `sleep`(1)", false},
		{"select `SLEEP`(2)", false},
		{"select mysql.`get_lock`('k',1)", false},
		{`select pg_sleep(1)`, true},
		{"select sleep(1)", false},
	}
	for _, c := range bad {
		if err := validateCursorStatement(c.stmt, c.postgres); err == nil {
			t.Errorf("validateCursorStatement(%q) accepted; want reject", c.stmt)
		}
	}
	ok := []struct {
		stmt     string
		postgres bool
	}{
		{`select "pg_sleep" from t`, true},
		{`select x as "pg_read_file" from t`, true},
		{"select `sleep` from t", false},
		{"select 1", false},
		{"select now()", true},
	}
	for _, c := range ok {
		if err := validateCursorStatement(c.stmt, c.postgres); err != nil {
			t.Errorf("validateCursorStatement(%q) = %v; want accept", c.stmt, err)
		}
	}
	// Lexical failure fails closed (unterminated string literal).
	if err := validateCursorStatement("select 'unterminated", false); err == nil {
		t.Error("unparsable statement accepted")
	}
}
