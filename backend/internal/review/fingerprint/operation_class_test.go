package fingerprint

import (
	"strings"
	"testing"
)

func TestFirstOperationSliceAndStatementClassParityTable(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		wantWord  string
		wantClass string
	}{
		{name: "create_table", sql: "CREATE TABLE orders (id bigint primary key);", wantWord: "create", wantClass: "ddl"},
		{name: "alter_table", sql: "  \t\nALTER TABLE orders ADD COLUMN note text;", wantWord: "alter", wantClass: "ddl"},
		{name: "drop_index", sql: "DROP INDEX idx_orders_note;", wantWord: "drop", wantClass: "ddl"},
		{name: "truncate_table", sql: "TRUNCATE TABLE orders;", wantWord: "truncate", wantClass: "ddl"},
		{name: "rename_table", sql: "RENAME TABLE old_orders TO orders;", wantWord: "rename", wantClass: "ddl"},
		{name: "insert_dml", sql: "INSERT INTO orders (id) VALUES (1);", wantWord: "insert", wantClass: "dml"},
		{name: "update_dml", sql: "UPDATE orders SET note = 'x' WHERE id = 1;", wantWord: "update", wantClass: "dml"},
		{name: "delete_dml", sql: "DELETE FROM orders WHERE id = 1;", wantWord: "delete", wantClass: "dml"},
		{name: "replace_dml", sql: "REPLACE INTO orders (id) VALUES (1);", wantWord: "replace", wantClass: "dml"},
		{name: "merge_dml", sql: "MERGE INTO orders o USING staging s ON (o.id = s.id) WHEN MATCHED THEN DELETE;", wantWord: "merge", wantClass: "dml"},
		{name: "grant_privilege", sql: "GRANT SELECT ON orders TO analyst_ro;", wantWord: "grant", wantClass: "dml"},
		{name: "revoke_privilege", sql: "REVOKE SELECT ON orders FROM analyst_ro;", wantWord: "revoke", wantClass: "dml"},
		{name: "select_query", sql: "SELECT id FROM orders;", wantWord: "select", wantClass: "dml"},
		{name: "parenthesized_select", sql: "((SELECT id FROM orders UNION ALL SELECT id FROM archive));", wantWord: "select", wantClass: "dml"},
		{name: "cte_with", sql: "WITH recent AS (SELECT * FROM orders) SELECT * FROM recent;", wantWord: "with", wantClass: "dml"},
		{name: "line_comment_prefix_ddl", sql: "-- ticket #104\n-- second comment\nALTER TABLE orders DROP COLUMN legacy_flag;", wantWord: "alter", wantClass: "ddl"},
		{name: "block_comment_prefix_rename", sql: "/* hint */ /* second */ RENAME TABLE t1 TO t2;", wantWord: "rename", wantClass: "ddl"},
		{name: "mixed_comments_and_parens_cte", sql: "/* header */\n-- note\n(WITH c AS (SELECT 1) SELECT * FROM c);", wantWord: "with", wantClass: "dml"},
		{name: "unterminated_block_comment", sql: "/* unterminated", wantWord: "", wantClass: "dml"},
		{name: "empty_whitespace", sql: "   \n\t  ", wantWord: "", wantClass: "dml"},
		{name: "leading_semicolon_punct", sql: ";SELECT 1", wantWord: ";", wantClass: "dml"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := FirstOperationSlice([]byte(tc.sql))
			gotWord := strings.ToLower(string(raw))
			if gotWord != tc.wantWord {
				t.Fatalf("FirstOperationSlice(%q) = %q, want %q", tc.sql, gotWord, tc.wantWord)
			}
			if gotClass := StatementClassOf(gotWord); gotClass != tc.wantClass {
				t.Fatalf("StatementClassOf(%q) = %q, want %q", gotWord, gotClass, tc.wantClass)
			}
			if gotUpperClass := StatementClassOf(strings.ToUpper(gotWord)); gotUpperClass != tc.wantClass {
				t.Fatalf("StatementClassOf(upper %q) = %q, want %q", gotWord, gotUpperClass, tc.wantClass)
			}
		})
	}
}
