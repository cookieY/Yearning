package domain

import "testing"

// TestSelectBatchCorpusCommentsAndSemicolons verifies DEF-07:
// trailing line/block comments, MySQL `#` comments, string-literal semicolons,
// CTE SELECT chains, and set-returning functions are accepted safely while
// multi-statement / write / dangerous-function injections fail closed (4007).
func TestSelectBatchCorpusCommentsAndSemicolons(t *testing.T) {
	accepted := []struct {
		name     string
		postgres bool
		sql      string
		wantLen  int
	}{
		{"pg_trailing_line_comment", true, "SELECT 1 -- trailing comment", 1},
		{"pg_trailing_block_comment", true, "SELECT 1 /* block */; -- trailing", 1},
		{"pg_semicolon_in_string", true, "SELECT 'a;b;delete from users' AS txt -- c;", 1},
		{"pg_dollar_quoted_semicolon", true, "SELECT $$x;y;drop table t$$ AS txt", 1},
		{"pg_cte_pure_select", true, "WITH cte AS (SELECT 1 AS n) SELECT * FROM cte;", 1},
		{"pg_generate_series", true, "SELECT * FROM generate_series(1, 5) AS n;", 1},
		{"pg_multi_select_batch", true, "/* head */ SELECT 1; -- mid\nSELECT 'k;v';", 2},
		{"mysql_hash_comment", false, "SELECT 1 # mysql comment;\n", 1},
		{"mysql_semicolon_in_string_and_comment", false, "SELECT \"semi;colon\" FROM t /* c; */ # h;", 1},
		{"mysql_cte_batch", false, "WITH c AS (SELECT 1) SELECT * FROM c; SELECT 2 # end", 2},
	}
	for _, tc := range accepted {
		t.Run("accept_"+tc.name, func(t *testing.T) {
			stmts, err := CheckSelectBatch(tc.sql, tc.postgres)
			if err != nil {
				t.Fatalf("expected acceptance for %q (postgres=%v), got err=%+v", tc.sql, tc.postgres, err)
			}
			if len(stmts) != tc.wantLen {
				t.Fatalf("len(stmts)=%d, want %d (%v)", len(stmts), tc.wantLen, stmts)
			}
		})
	}

	rejected := []struct {
		name     string
		postgres bool
		sql      string
	}{
		{"pg_write_after_comment", true, "/* ok */ DELETE FROM users WHERE id = 1"},
		{"pg_mixed_select_then_dml", true, "SELECT 1; DROP TABLE users;"},
		{"pg_data_modifying_cte", true, "WITH del AS (DELETE FROM t RETURNING *) SELECT * FROM del"},
		{"pg_dangerous_pg_sleep", true, "SELECT pg_sleep(1)"},
		{"mysql_executable_version_comment_write", false, "SELECT 1; /*!50000 DROP TABLE t */"},
		{"mysql_dangerous_benchmark", false, "SELECT benchmark(1000, sha1('x'))"},
	}
	for _, tc := range rejected {
		t.Run("reject_"+tc.name, func(t *testing.T) {
			if _, err := CheckSelectBatch(tc.sql, tc.postgres); err == nil {
				t.Fatalf("expected rejection for %q (postgres=%v)", tc.sql, tc.postgres)
			}
		})
	}
}
