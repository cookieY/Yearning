// pool_cursor_exec_comment_test.go NEW-QUERY-01 adapter-layer adversarial acceptance test:
// unversioned `/*!` and MariaDB `/*M!` comment bodies are SQL executed by the server; after
// expansion by the tokenizer, the adapter-layer shallow guard must intercept dangerous function
// calls and INTO write surfaces inside them; `/*!50000` serves as the existing regression anchor;
// regular comments and hints remain allowed (including regular comments whose bodies contain
// dangerous shapes — the server does not execute them).
package database

import "testing"

func TestValidateCursorStatementExecutableComment(t *testing.T) {
	bad := []struct {
		stmt     string
		postgres bool
	}{
		{"select 1 /*! , sleep(10) */", false},
		{"select 1 /*! , load_file('/etc/passwd') */", false},
		{"select 1 /*! into outfile '/tmp/x' */", false},
		{"select 1 /*M! , sleep(10) */", false},
		{"select 1 /*M!100100 , sleep(10) */", false},
		{"select 1 /*!50000 , sleep(10) */", false},
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
		{"select 1 /* ordinary */", false},
		{"select 1 /* sleep(10) */", false},
		{"select 1 /*+ MAX_EXECUTION_TIME(1000) */", false},
		// PG family: /*! is a regular comment.
		{"select 1 /*! , pg_sleep(10) */", true},
	}
	for _, c := range ok {
		if err := validateCursorStatement(c.stmt, c.postgres); err != nil {
			t.Errorf("validateCursorStatement(%q, postgres=%v) = %v; want accept", c.stmt, c.postgres, err)
		}
	}
}
