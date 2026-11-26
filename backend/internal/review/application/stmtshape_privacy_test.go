package application

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestDDLCharsetAndCollationUseOnlyStructuralTokens(t *testing.T) {
	private := []string{
		"private_comment_secret",
		"private_comment_name",
		"private_default_secret",
	}
	sqls := []string{
		"CREATE TABLE orders (id int) COMMENT='charset=private_comment_secret'",
		"CREATE TABLE orders (id int) -- COLLATE private_comment_name\n",
		"CREATE TABLE orders (id int DEFAULT 'COLLATE private_default_secret')",
		"CREATE TABLE orders (id int) COLLATE private_structured_name",
	}
	for _, sql := range sqls {
		t.Run(sql, func(t *testing.T) {
			facts := extractStmtFacts([]byte(sql), "create", "ddl")
			if !facts.incomplete {
				t.Fatal("private text outside a controlled value did not make context incomplete")
			}
			if facts.m["metadata_context_complete"] != false {
				t.Fatal("incomplete DDL must override grouping metadata completeness")
			}
			encoded, err := json.Marshal(facts.m)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range private {
				if containsFold(string(encoded), marker) {
					t.Fatalf("DDL statistics leaked %q: %s", marker, encoded)
				}
			}
		})
	}
}

func TestDDLPrivateTextNeverAppearsInCompleteProviderRequest(t *testing.T) {
	const marker = "private_comment_secret"
	facts := extractStmtFacts([]byte(
		"CREATE TABLE orders (id int) COMMENT='charset="+marker+"'"),
		"create", "ddl")
	req := agent.BuildRequest("00000000-0000-4000-8000-000000000998", agent.ContextInput{
		OutputLocale: "en-US",
		Groups: []provider.AnonymizedGroup{{
			GroupID:        "group_1",
			StatementCount: 1,
			Statistics:     facts.m,
		}},
	}, nil)
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if containsFold(string(encoded), marker) {
		t.Fatalf("complete Provider request leaked hidden DDL string: %s", encoded)
	}
	if req.AnonymizedStatementGroups[0].Statistics["metadata_context_complete"] != false {
		t.Fatal("Provider request omitted the metadata incompleteness marker")
	}
}

func TestDDLCharsetAndCollationComeFromKnownSyntax(t *testing.T) {
	facts := extractStmtFacts([]byte(
		"CREATE TABLE orders (id int) CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci"),
		"create", "ddl")
	if facts.incomplete {
		t.Fatal("structured charset/collation was treated as unknown")
	}
	if facts.m["ddl_charset"] != "utf8mb4" || facts.m["ddl_collation"] != "utf8mb4_0900_ai_ci" {
		t.Fatalf("structured DDL values = %#v", facts.m)
	}
}

func TestIncompleteDDLRemainsIncompleteAfterGroupMerge(t *testing.T) {
	first := extractStmtFacts([]byte(
		"CREATE TABLE orders (id int) COMMENT='charset=private_secret'"),
		"create", "ddl")
	second := extractStmtFacts([]byte(
		"CREATE TABLE orders (id int) DEFAULT CHARSET=utf8mb4"),
		"create", "ddl")
	merged := stmtFacts{}
	mergeFactsInto(&merged, first)
	mergeFactsInto(&merged, second)
	if !merged.incomplete || merged.m["metadata_context_complete"] != false {
		t.Fatalf("group merge erased incomplete DDL context: %#v", merged)
	}
}

func TestDDLPrimaryAndForeignKeyNotFlaggedAsUnnamedIndex(t *testing.T) {
	sql := `CREATE TABLE t_idx (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		parent_id BIGINT UNSIGNED NOT NULL,
		col_a VARCHAR(32) NOT NULL DEFAULT '',
		PRIMARY KEY (id),
		KEY idx_col_a (col_a),
		CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`
	facts := extractStmtFacts([]byte(sql), "create", "ddl")
	if got := facts.m["ddl_index_count"]; got != int64(2) {
		t.Fatalf("ddl_index_count = %v, want 2 (PRIMARY + idx_col_a, excluding FOREIGN KEY)", got)
	}
	if got := facts.m["ddl_index_unnamed_count"]; got != int64(0) {
		t.Fatalf("ddl_index_unnamed_count = %v, want 0", got)
	}
	if got := facts.m["ddl_index_prefix_conformant"]; got != true {
		t.Fatalf("ddl_index_prefix_conformant = %v, want true", got)
	}

	unnamedSQL := `CREATE TABLE t_unnamed (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		col_a VARCHAR(32) NOT NULL DEFAULT '',
		PRIMARY KEY (id),
		KEY (col_a)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`
	unnamedFacts := extractStmtFacts([]byte(unnamedSQL), "create", "ddl")
	if got := unnamedFacts.m["ddl_index_unnamed_count"]; got != int64(1) {
		t.Fatalf("truly unnamed secondary KEY must yield ddl_index_unnamed_count = 1, got %v", got)
	}
}

func containsFold(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if strings.EqualFold(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func TestMergeStmtFactsIntoDMLAndDDLParity(t *testing.T) {
	batches := [][]struct {
		sql   string
		op    string
		class string
	}{
		{
			{sql: "UPDATE orders SET amount = 1 WHERE id = 1", op: "update", class: "dml"},
			{sql: "UPDATE orders SET amount = 2 WHERE id = 2 AND status = NULL ORDER BY id LIMIT 10", op: "update", class: "dml"},
			{sql: "UPDATE orders SET amount = 3 WHERE id IN (SELECT id FROM other)", op: "update", class: "dml"},
		},
		{
			{sql: "DELETE FROM orders WHERE id = 1", op: "delete", class: "dml"},
			{sql: "DELETE FROM orders WHERE id = 2 ORDER BY id LIMIT 5", op: "delete", class: "dml"},
		},
		{
			{sql: "INSERT INTO orders VALUES (1, 'a')", op: "insert", class: "dml"},
			{sql: "INSERT INTO orders (id, note) VALUES (2, NULL)", op: "insert", class: "dml"},
			{sql: "INSERT INTO orders (id, note) SELECT id, note FROM archive", op: "insert", class: "dml"},
		},
		{
			{sql: "REPLACE INTO orders VALUES (1, 'a')", op: "replace", class: "dml"},
			{sql: "REPLACE INTO orders SELECT id, note FROM archive", op: "replace", class: "dml"},
		},
		{
			{sql: "SELECT id FROM orders WHERE id = 1", op: "select", class: "dml"},
		},
		{
			{sql: "CREATE TABLE t (id INT PRIMARY KEY)", op: "create", class: "ddl"},
		},
	}

	for bIdx, batch := range batches {
		var got, want stmtFacts
		for _, item := range batch {
			mergeStmtFactsInto(&got, []byte(item.sql), item.op, item.class)
			mergeFactsInto(&want, extractStmtFacts([]byte(item.sql), item.op, item.class))
		}
		if got.incomplete != want.incomplete || !reflect.DeepEqual(got.m, want.m) {
			t.Fatalf("batch %d mismatch:\n got  = %#v\n want = %#v", bIdx, got, want)
		}
	}
}

func TestRenameTableAcrossDBDetection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"unqualified_same_db", "RENAME TABLE orders_old TO orders_archive", false},
		{"explicit_same_db", "RENAME TABLE shop.orders_old TO shop.orders_archive", false},
		{"explicit_same_db_quoted_case", "RENAME TABLE `SHOP`.`orders_old` TO `shop`.`orders_archive`", false},
		{"src_unqualified_dst_qualified", "RENAME TABLE orders_old TO shop.orders_archive", false},
		{"src_qualified_dst_unqualified", "RENAME TABLE shop.orders_old TO orders_archive", false},
		{"cross_db", "RENAME TABLE shop.orders_old TO archive.orders_old", true},
		{"cross_db_quoted", "RENAME TABLE `shop`.`orders_old` TO `archive`.`orders_old`", true},
		{"multi_pair_all_same_db", "RENAME TABLE shop.a TO shop.b, shop.c TO shop.d", false},
		{"multi_pair_one_cross_db", "RENAME TABLE shop.a TO shop.b, shop.c TO archive.d", true},
		{"leading_comment_trick", "/* TO archive.fake */ RENAME TABLE shop.a TO shop.b", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := extractStmtFacts([]byte(tc.sql), "rename", "ddl")
			if facts.m["ddl_is_rename_table"] != true {
				t.Fatalf("ddl_is_rename_table = %v, want true", facts.m["ddl_is_rename_table"])
			}
			if facts.m["ddl_across_db_rename"] != tc.want {
				t.Fatalf("ddl_across_db_rename = %v, want %v (sql=%q)", facts.m["ddl_across_db_rename"], tc.want, tc.sql)
			}
		})
	}
}

func TestRenameNonTableRoutesToDDLOther(t *testing.T) {
	t.Parallel()

	facts := extractStmtFacts([]byte("RENAME USER 'a'@'%' TO 'b'@'%'"), "rename", "ddl")
	if len(facts.m) != 0 {
		t.Fatalf("non-TABLE RENAME USER must emit no branch facts, got %#v", facts.m)
	}
}

func TestAlterTableRenameToKeepsAlterBranch(t *testing.T) {
	t.Parallel()

	for _, sql := range []string{
		"ALTER TABLE shop.orders RENAME TO shop.orders_v2",
		"ALTER TABLE shop.orders ADD COLUMN note VARCHAR(32) COMMENT 'rename table'",
	} {
		sql := sql
		t.Run(sql, func(t *testing.T) {
			t.Parallel()
			facts := extractStmtFacts([]byte(sql), "alter", "ddl")
			if _, ok := facts.m["ddl_is_rename_table"]; ok {
				t.Fatalf("ALTER statement must not emit ddl_is_rename_table: %#v", facts.m)
			}
			if _, ok := facts.m["ddl_across_db_rename"]; ok {
				t.Fatalf("ALTER statement must not emit ddl_across_db_rename: %#v", facts.m)
			}
			if facts.m["ddl_alters_column_type"] != false || facts.m["ddl_changes_column_position"] != false {
				t.Fatalf("unexpected alter facts: %#v", facts.m)
			}
		})
	}
}
