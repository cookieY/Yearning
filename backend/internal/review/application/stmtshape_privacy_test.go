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
