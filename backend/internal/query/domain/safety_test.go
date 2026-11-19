// safety_test.go single-SELECT safety checker acceptance tests (PRD 06 §9):
// write statements, multi-statements, data-modifying CTEs, comment bypasses, side-effecting functions, and write clauses are all rejected.
package domain

import "testing"

func TestCheckSingleSelectAccepts(t *testing.T) {
	cases := []struct {
		sql string
		pg  bool
	}{
		{"select 1", false},
		{"SELECT id, name FROM users WHERE id = ?", false},
		{"select a.id, b.name from t a join u b on a.x = b.y where a.k > 10 order by b.name limit 5", false},
		{"with t as (select id from x) select * from t", true},
		{"WITH RECURSIVE r AS (SELECT 1 AS n UNION ALL SELECT n + 1 FROM r WHERE n < 5) SELECT n FROM r", true},
		{"with a as (select id from x), b as (select id from a) select b.id from b", true},
		{"select 'semi;colon' as v, `odd;name` from t", false}, // Semicolons inside strings or quoted identifiers are not statement separators
		{"select ';' from t", true},                                   // Same as above (PostgreSQL)
		{"select case when a > 1 then 'x' else 'y' end from t", true}, // CASE expression
		{"select count(*) c, max(id) from t group by k having count(*) > 1", true},
		{"/* comment */ select id /* inline */ from t -- tail", false},
		{"select json_agg(x) from (select id as x from t) q", true},
		{"select current_date, now()", true},
	}
	for _, c := range cases {
		if e := CheckSingleSelect(c.sql, c.pg); e != nil {
			t.Errorf("CheckSingleSelect(%q) = %v; want accept", c.sql, e)
		}
	}
}

func TestCheckSingleSelectRejects(t *testing.T) {
	cases := []struct {
		sql string
		pg  bool
	}{
		// Write statements and statement heads.
		{"insert into t values (1)", false},
		{"update t set a = 1", false},
		{"delete from t", false},
		{"create table x (id int)", false},
		{"alter table t add column c int", false},
		{"drop table t", false},
		{"truncate table t", false},
		{"replace into t values (1)", false},
		{"merge into t using s on t.id = s.id when matched then update set a = 1", true},
		{"grant select on t to u", false},
		{"call proc()", false},
		{"do $$ begin end $$", true},
		{"copy t from stdin", true},
		{"set statement_timeout = 0", false},
		{"start transaction", false},
		{"begin", false},
		{"show tables", false},
		{"explain select 1", false},
		{"explain analyze select 1", true},
		{"values (1)", true},
		{"vacuum t", true},
		{"select 1 into newtable from t", true},
		{"prepare p as select 1", true},
		// Multi-statements and semicolons.
		{"select 1; select 2", false},
		{"select 1;", false},
		{"select 1;delete from t", false},
		{"", false},
		{"   ", false},
		{"select 1\x00", false},
		// Data-modifying CTEs (top-level and nested).
		{"with d as (delete from t returning *) select * from d", true},
		{"with u as (update t set a = 1 returning *) select * from u", true},
		{"with i as (insert into t values (1) returning *) select * from i", true},
		{"select * from (with d as (delete from t returning *) select * from d) q", true},
		{"with w as (with d as (delete from t returning *) select * from d) select * from w", true},
		// SELECT write/locking clauses.
		{"select * from t into outfile '/tmp/x'", false},
		{"select a into @v from t", false},
		{"select * from t for update", false},
		{"select * from t for share", true},
		{"select * from t lock in share mode", false},
		// Side-effecting and blocking functions.
		{"select sleep(1)", false},
		{"select benchmark(1000000, md5('x'))", false},
		{"select load_file('/etc/passwd')", false},
		{"select get_lock('k', 1)", false},
		{"select pg_sleep(1)", true},
		{"select pg_read_file('/etc/passwd')", true},
		{"select dblink('dbname=x', 'select 1')", true},
		{"select nextval('seq')", true},
		{"select setval('seq', 1)", true},
		{"select set_config('a', 'b', false)", true},
		{"select pg_notify('ch', 'msg')", true},
		{"select lo_import('/tmp/f')", true},
		{"select pg_advisory_lock(1)", true},
		// Lexical failures fail closed.
		{"select 'unterminated", false},
		{"select /* unterminated", false},
		{"select \x01\x02", false},
	}
	for _, c := range cases {
		if e := CheckSingleSelect(c.sql, c.pg); e == nil {
			t.Errorf("CheckSingleSelect(%q) accepted; want reject", c.sql)
		}
	}
}

func TestCheckSingleSelectErrorCodes(t *testing.T) {
	if e := CheckSingleSelect("select 1; select 2", false); e.Code != CodeOnlySingleSelect {
		t.Errorf("semicolon case code = %d; want %d", e.Code, CodeOnlySingleSelect)
	}
	if e := CheckSingleSelect("", false); e.Code != CodeValidationFailed {
		t.Errorf("empty case code = %d; want %d", e.Code, CodeValidationFailed)
	}
	// Comments cannot disguise forbidden statement heads.
	if e := CheckSingleSelect("/* lead */ delete from t", false); e == nil {
		t.Error("comment-led delete accepted")
	}
	// Write statements inside MySQL executable version comments are expanded into tokens and rejected.
	if e := CheckSingleSelect("select /*!40101 delete from t */ 1", false); e == nil {
		t.Error("executable version comment hides delete")
	}
}

// TestCheckSingleSelectRejectsQuotedIdentifierCalls SEC-0008 adversarial verification
// (independent review A probe vectors): side-effecting function calls via quoted identifiers -- bare word,
// quoted, schema-qualified, mixed case, and MySQL backticks -- are all rejected (4007).
func TestCheckSingleSelectRejectsQuotedIdentifierCalls(t *testing.T) {
	cases := []struct {
		sql string
		pg  bool
	}{
		// PostgreSQL double-quoted identifiers (raw text previously bypassed TokWord matching).
		{`select "pg_read_file"('/etc/passwd')`, true},
		{`select "pg_read_binary_file"('/etc/passwd')`, true},
		{`select "pg_sleep"(10)`, true},
		{`select "dblink"('dbname=x', 'select 1')`, true},
		{`select pg_catalog."pg_read_file"('/etc/passwd')`, true},
		{`select "pg_catalog"."pg_read_file"('/etc/passwd')`, true},
		{`select public."set_config"('a', 'b', false)`, true},
		// Mixed case (TokQuotedIdent preserves original case and must be normalized).
		{`select "PG_READ_FILE"('/etc/passwd')`, true},
		{`select "Pg_Sleep"(3)`, true},
		{`select PG_CATALOG."PG_SLEEP"(1)`, true},
		// MySQL backtick-quoted identifiers.
		{"select `sleep`(1)", false},
		{"select `benchmark`(1000000, md5('x'))", false},
		{"select `load_file`('/etc/passwd')", false},
		{"select mysql.`get_lock`('k', 1)", false},
		{"select `SLEEP`(2)", false},
		// Multi-statement mix with quoted identifiers.
		{`select "pg_sleep"(1); select 2`, true},
		// Bare-word schema-qualified regression cases (already covered, pinned here).
		{`select pg_catalog.pg_sleep(1)`, true},
	}
	for _, c := range cases {
		e := CheckSingleSelect(c.sql, c.pg)
		if e == nil {
			t.Errorf("CheckSingleSelect(%q) accepted; want reject", c.sql)
			continue
		}
		if e.Code != CodeOnlySingleSelect {
			t.Errorf("CheckSingleSelect(%q) code = %d; want %d (4007)", c.sql, e.Code, CodeOnlySingleSelect)
		}
	}
	// Quoted identifiers in non-function positions are not falsely rejected (columns/aliases/tables sharing names with dangerous functions).
	for _, c := range []struct {
		sql string
		pg  bool
	}{
		{`select "pg_sleep" from t`, true},
		{`select x as "pg_read_file" from t`, true},
		{"select `sleep` from t", false},
		{`select count(*) from "pg_sleep"`, true},
	} {
		if e := CheckSingleSelect(c.sql, c.pg); e != nil {
			t.Errorf("CheckSingleSelect(%q) = %v; want accept (quoted identifier in non-function position)", c.sql, e)
		}
	}
}

// TestCheckSingleSelectMySQLDashDashNoSpace SEC-0056 adversarial verification:
// MySQL `--x` (no whitespace after `--`) is not a comment; the token stream must see the semicolon and reject multi-statement queries,
// whereas `-- x` (with whitespace) remains a valid comment (semantic control case).
func TestCheckSingleSelectMySQLDashDashNoSpace(t *testing.T) {
	bad := []string{
		"select 1 --1; drop table t",
		"select 1 --x; delete from t",
		"select 1--2;update t set x=1",
		"select 1 --; select 2 --3; select 3",
	}
	for _, sql := range bad {
		if e := CheckSingleSelect(sql, false); e == nil {
			t.Errorf("CheckSingleSelect(%q) accepted; want reject (--x semicolon-swallowing bypass)", sql)
		}
	}
	// `-- x` (valid comment) and `--x` inside string literals are not falsely rejected.
	good := []string{
		"select 1 -- trailing comment",
		"select '--not-comment' from t",
		"select 1 -- x\n",
	}
	for _, sql := range good {
		if e := CheckSingleSelect(sql, false); e != nil {
			t.Errorf("CheckSingleSelect(%q) = %v; want accept", sql, e)
		}
	}
	// PostgreSQL family: `--x` is a valid comment (preserving dialect semantics), and semicolons inside comments do not form multiple statements.
	if e := CheckSingleSelect("select 1 --1", true); e != nil {
		t.Errorf("pg `--1` comment = %v; want accept", e)
	}
}
