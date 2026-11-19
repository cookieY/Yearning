// safety_batch_test.go F-Q-001: semicolon-separated SELECT batch validation (per owner ruling
// enabling multi-statement execution in the query workbench). Per-statement safety invariants are uncompromised:
// any impure statement rejects the entire batch; empty segments are dropped; semicolons inside strings/comments do not split.
package domain

import (
	"fmt"
	"strings"
	"testing"
)

func TestCheckSelectBatchAccepts(t *testing.T) {
	cases := []struct {
		sql      string
		pg       bool
		wantLen  int
		wantLast string
	}{
		{"select 1", false, 1, "select 1"},
		// Trailing semicolon: single statement + empty segment.
		{"select * from demo.accounts;", true, 1, "select * from demo.accounts"},
		{"select 1;;", false, 1, "select 1"},
		{" ; select 1 ; ", false, 1, "select 1"},
		// Multiple statements: split in order and return all statements.
		{"select 1; select 2", false, 2, "select 2"},
		{"select id from a; select id, name from b; select 3", true, 3, "select 3"},
		// Semicolons inside string literals do not split statements.
		{"select ';' as v; select 2", true, 2, "select 2"},
		{"select 'a;b' from t where x = ';'; select 2", false, 2, "select 2"},
		// Comment segments and inter-statement comments.
		{"select 1 -- trailing\n; /* lead */ select 2", false, 2, "select 2"},
		// Comment-only segments are discarded.
		{"/* only comment */ select 1", true, 1, "select 1"},
		{"select 1; /* mid */; select 2", false, 2, "select 2"},
		// CTEs are validated independently per statement.
		{"with x as (select 1 as v) select * from x; select 2", true, 2, "select 2"},
	}
	for _, c := range cases {
		stmts, e := CheckSelectBatch(c.sql, c.pg)
		if e != nil {
			t.Errorf("CheckSelectBatch(%q) = %v; want accept", c.sql, e)
			continue
		}
		if len(stmts) != c.wantLen {
			t.Errorf("CheckSelectBatch(%q) len=%d want %d (%v)", c.sql, len(stmts), c.wantLen, stmts)
		}
		if stmts[len(stmts)-1] != c.wantLast {
			t.Errorf("CheckSelectBatch(%q) last=%q want %q", c.sql, stmts[len(stmts)-1], c.wantLast)
		}
	}
}

func TestCheckSelectBatchRejects(t *testing.T) {
	cases := []struct {
		sql  string
		pg   bool
		code int32
	}{
		// Any non-pure SELECT statement rejects the entire batch (4007).
		{"select 1; delete from t", false, CodeOnlySingleSelect},
		{"select 1; select 2; update t set x=1", true, CodeOnlySingleSelect},
		{"insert into t values(1); select 1", false, CodeOnlySingleSelect},
		{"select 1; select 2 into outfile '/tmp/x'", false, CodeOnlySingleSelect},
		// Dangerous function in an earlier statement is caught by per-statement validation.
		{"select sleep(1); select 2", false, CodeOnlySingleSelect},
		{"select 1; select pg_read_file('/etc/passwd')", true, CodeOnlySingleSelect},
		// Empty batch or whitespace-only input returns 1001.
		{"", false, CodeValidationFailed},
		{"   ", true, CodeValidationFailed},
		{";;;", false, CodeValidationFailed},
		{"/* only comment */", true, CodeValidationFailed},
		// DML smuggled inside a MySQL executable version comment is rejected after tokenizer expansion.
		{"select 1 /*! , delete from t */; select 2", false, CodeOnlySingleSelect},
	}
	for _, c := range cases {
		_, e := CheckSelectBatch(c.sql, c.pg)
		if e == nil {
			t.Errorf("CheckSelectBatch(%q) accepted; want reject", c.sql)
			continue
		}
		if e.Code != c.code {
			t.Errorf("CheckSelectBatch(%q) code=%d want %d", c.sql, e.Code, c.code)
		}
	}
}

// TestCheckSelectBatchStatementCap verifies the batch statement count limit (Review R1 M2):
// exceeding maxBatchStatements is rejected as a batch shape violation (4007) before reaching execution,
// while a batch right at the cap is accepted.
func TestCheckSelectBatchStatementCap(t *testing.T) {
	over := make([]string, 0, maxBatchStatements+1)
	for i := 0; i < maxBatchStatements+1; i++ {
		over = append(over, fmt.Sprintf("select %d", i))
	}
	_, e := CheckSelectBatch(strings.Join(over, "; "), true)
	if e == nil || e.Code != CodeOnlySingleSelect {
		t.Fatalf("oversized batch: want 4007, got %v", e)
	}

	at := over[:maxBatchStatements]
	stmts, e := CheckSelectBatch(strings.Join(at, "; "), true)
	if e != nil {
		t.Fatalf("batch at cap rejected: %v", e)
	}
	if len(stmts) != maxBatchStatements {
		t.Fatalf("stmts=%d want %d", len(stmts), maxBatchStatements)
	}
}

// TestCheckSelectBatchSingleUnchanged verifies that single-statement entry point semantics remain unchanged:
// CheckSingleSelect still rejects any top-level semicolon -- CheckSelectBatch is the sole multi-statement entry point.
func TestCheckSelectBatchSingleUnchanged(t *testing.T) {
	if e := CheckSingleSelect("select 1; select 2", false); e == nil || e.Code != CodeOnlySingleSelect {
		t.Fatalf("CheckSingleSelect multi-statement: %v", e)
	}
}
