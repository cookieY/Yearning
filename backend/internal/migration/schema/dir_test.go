package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitStatementsHandlesQuotesAndComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "create table a (id int);\ncreate index on a (id);", []string{"create table a (id int)", "create index on a (id)"}},
		{"escaped quote", "insert into t values ('it''s;fine');select 1", []string{"insert into t values ('it''s;fine')", "select 1"}},
		{"line comment", "-- semicolon; here\nselect 1;", []string{"-- semicolon; here\nselect 1"}},
		{"block comment", "/* a;b */ select 2;", []string{"/* a;b */ select 2"}},
		{"dollar quote", "select $$a;b$$ from x;select 3", []string{"select $$a;b$$ from x", "select 3"}},
		{"tagged dollar", "select $body$x;y$body$ ;select 4", []string{"select $body$x;y$body$", "select 4"}},
		{"empty", ";;\n-- only comment", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitStatements(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements %q, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("statement %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestLoadDirRejectsUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("000001_alpha.up.sql", "create table a (id int);")
	write("notes.txt", "hello")
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected error for non-up file")
	}
}

func TestLoadDirRejectsVersionGaps(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001_a.up.sql", "000003_c.up.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("select 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected error for version gap")
	}
}

func TestAnalyzeDetectsProblems(t *testing.T) {
	files := []Migration{
		{Version: 1, Filename: "000001_a.up.sql", SHA256: "aa"},
		{Version: 2, Filename: "000002_b.up.sql", SHA256: "bb"},
	}
	failed := "boom"
	scenarios := []struct {
		name    string
		ledger  []LedgerRow
		problem string
	}{
		{"clean", []LedgerRow{{Version: 1, Filename: "000001_a.up.sql", SHA256: "aa", Success: true}}, ""},
		{"hash changed", []LedgerRow{{Version: 1, Filename: "000001_a.up.sql", SHA256: "zz", Success: true}}, "hash"},
		{"dirty failed", []LedgerRow{{Version: 1, Filename: "000001_a.up.sql", SHA256: "aa", Success: false, FailureName: &failed}}, "failed"},
		{"future version", []LedgerRow{{Version: 9, Filename: "000009_future.up.sql", SHA256: "ff", Success: true}}, "no file"},
	}
	for _, sc := range scenarios {
		_, problems := Analyze(files, sc.ledger)
		if sc.problem == "" && len(problems) != 0 {
			t.Fatalf("%s: want no problems, got %v", sc.name, problems)
		}
		if sc.problem != "" {
			found := false
			for _, p := range problems {
				if strings.Contains(p, sc.problem) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: want problem containing %q, got %v", sc.name, sc.problem, problems)
			}
		}
	}
}

func TestClassifyStatement(t *testing.T) {
	cases := map[string]string{
		"create table users (id uuid)":   "create_table",
		"CREATE UNIQUE INDEX i ON t(c)":  "create_index",
		"alter table t add column c int": "alter",
		"begin":                          "transaction_control",
		"commit":                         "transaction_control",
		"drop table t":                   "drop",
		"select 1":                       "other",
	}
	for stmt, want := range cases {
		if got := classifyStatement(stmt); got != want {
			t.Errorf("classifyStatement(%q) = %q, want %q", stmt, got, want)
		}
	}
}
