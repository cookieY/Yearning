package application

import (
	"encoding/json"
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
