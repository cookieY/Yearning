// factprivacy_test.go SEC-0002 regression: tool fact identifier tokenization and leak assertions.
// Provider request bytes must never contain real schema/table/column/index names from the test DB,
// covering the four built-in tools that carry identifiers (datasource_identity carries no identifiers).
// Both structured output modes of the Provider (json_schema/json_object) share the exact same request
// payload serialization path; mode-level byte assertions live in openai_privacy_test.go in package provider.
package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// rawFactSource returns fixed real facts by tool name (simulating factsource.go output).
type rawFactSource struct {
	facts map[string]map[string]any
}

func (r *rawFactSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql"}, nil
}
func (r *rawFactSource) SchemaMetadata(context.Context, string) (map[string]any, error) {
	return r.facts[tool.NameSchemaMetadata], nil
}
func (r *rawFactSource) TableMetadata(context.Context, string, string) (map[string]any, error) {
	return r.facts[tool.NameSchemaMetadata], nil
}
func (r *rawFactSource) TableStatistics(context.Context, string, string) (map[string]any, error) {
	return r.facts[tool.NameTableStatistics], nil
}
func (r *rawFactSource) IndexMetadata(context.Context, string, string) (map[string]any, error) {
	return r.facts[tool.NameIndexMetadata], nil
}
func (r *rawFactSource) SafeExplain(context.Context, string, string) (map[string]any, error) {
	return r.facts[tool.NameSafeExplain], nil
}

// sec0002Facts holds real-identifier facts for the four tools (isomorphic to factsource.go output).
func sec0002Facts() map[string]map[string]any {
	return map[string]map[string]any{
		tool.NameSchemaMetadata: {
			"schema": "app_db", "table_count": 2,
			"tables": []string{"users", "order_items"},
		},
		tool.NameTableStatistics: {
			"schema": "app_db", "table": "users",
			"column_count": 5, "index_count": 2, "has_primary_key": true, "estimate": true,
		},
		tool.NameIndexMetadata: {
			"schema": "app_db", "table": "users",
			"indexes": []map[string]any{
				{"index_name": "PRIMARY", "non_unique": false, "columns": []string{"id"}},
				{"index_name": "idx_users_email", "non_unique": true, "columns": []string{"email", "user_name"}},
			},
		},
		tool.NameSafeExplain: {
			"access_method": "Index Scan", "estimated_rows": 42, "estimated_cost": 1.5,
			"index_names": []string{"idx_users_email"}, "warnings": []string{}, "estimate": true,
		},
	}
}

// sec0002Forbidden is the full set of real identifiers in the test DB (leak assertion forbidden set).
func sec0002Forbidden() []privacy.Forbidden {
	out := []privacy.Forbidden{}
	for _, v := range []string{"app_db", "users", "order_items", "PRIMARY",
		"idx_users_email", "id", "email", "user_name"} {
		out = append(out, privacy.Forbidden{Value: v, What: "identifier"})
	}
	return out
}

// TestSEC0002ToolFactsTokenizedNoIdentifierToProvider exercises the real
// Registry.Execute -> tokenizeToolFact pipeline: normalized facts contain only anonymous tokens,
// and Provider request bytes (NormalizedEvidenceRef JSON) contain no real schema/table/column/index names.
func TestSEC0002ToolFactsTokenizedNoIdentifierToProvider(t *testing.T) {
	facts := sec0002Facts()
	src := &rawFactSource{facts: facts}
	registry := tool.NewRegistry()
	mapper := privacy.NewRunMapper()
	for _, toolName := range []string{tool.NameSchemaMetadata, tool.NameTableStatistics,
		tool.NameIndexMetadata, tool.NameSafeExplain} {
		// Strict argument set for each tool (Registry rejects unknown arguments).
		args := map[string]any{"schema": "app_db"}
		switch toolName {
		case tool.NameSchemaMetadata:
			args["table"] = "users"
		case tool.NameTableStatistics, tool.NameIndexMetadata:
			args["table"] = "users"
		case tool.NameSafeExplain:
			args["group_id"] = "g"
		}
		res := registry.Execute(context.Background(), src, "call-"+toolName, toolName, args)
		if !res.Success {
			t.Fatalf("%s: registry execute failed: %v", toolName, res.ErrorCode)
		}
		if err := tokenizeToolFact(res.NormalizedFact, mapper); err != nil {
			t.Fatalf("%s: tokenize: %v", toolName, err)
		}
		// Provider request byte shape: serialized NormalizedEvidenceRef
		// (sent by the openai adapter as user JSON to the Provider).
		raw, err := json.Marshal(provider.NormalizedEvidenceRef{
			EvidenceID: "evidence-1", FactKind: toolName, Fact: res.NormalizedFact})
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ToLower(string(raw))
		// Byte-level assertion: multi-character real identifiers must not appear in request bytes.
		// ("PRIMARY" collides as a substring with contract key has_primary_key, and "id" collides as a substring
		// with evidence_id; both are covered by the value-level full-token match below.)
		for _, name := range []string{"app_db", "users", "order_items",
			"idx_users_email", "email", "user_name"} {
			if strings.Contains(body, name) {
				t.Fatalf("%s: provider request bytes contain real identifier: %.200s", toolName, body)
			}
		}
		if !strings.Contains(body, "schema_1") && toolName != tool.NameSafeExplain {
			t.Fatalf("%s: schema token missing: %s", toolName, body)
		}
		if toolName == tool.NameSafeExplain && !strings.Contains(body, "index_2") {
			t.Fatalf("%s: index token missing: %s", toolName, body)
		}
		// Value-level leak assertion (including full-token matching for the short name "id").
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if err := privacy.AssertNoLeakValues(collectFactStringValues(payload), sec0002Forbidden()); err != nil {
			t.Fatalf("%s: value-level leak assertion: %v", toolName, err)
		}
	}
}

// TestSEC0002TokensStableAcrossToolCalls within the same RunMapper, identical (kind, originalName) pairs
// always receive the same token while distinct identifiers increment in order — giving the model stable
// references across multiple tool-call turns and stages.
func TestSEC0002TokensStableAcrossToolCalls(t *testing.T) {
	mapper := privacy.NewRunMapper()
	fact := func() map[string]any {
		return map[string]any{"schema": "app_db", "table": "users",
			"indexes": []any{map[string]any{"index_name": "idx_users_email",
				"columns": []any{"email"}}}}
	}
	first := fact()
	if err := tokenizeToolFact(first, mapper); err != nil {
		t.Fatal(err)
	}
	second := fact()
	if err := tokenizeToolFact(second, mapper); err != nil {
		t.Fatal(err)
	}
	if first["schema"] != second["schema"] || first["table"] != second["table"] {
		t.Fatalf("tokens unstable across calls: %v vs %v", first, second)
	}
	idx1 := first["indexes"].([]any)[0].(map[string]any)
	idx2 := second["indexes"].([]any)[0].(map[string]any)
	if idx1["index_name"] != idx2["index_name"] {
		t.Fatalf("index tokens unstable: %v vs %v", idx1["index_name"], idx2["index_name"])
	}
	// A new table name receives a new table ordinal without reusing existing ones.
	fresh := map[string]any{"schema": "app_db", "table": "order_items"}
	if err := tokenizeToolFact(fresh, mapper); err != nil {
		t.Fatal(err)
	}
	if fresh["table"] == first["table"] {
		t.Fatalf("distinct tables must map to distinct tokens: %v", fresh["table"])
	}
}

// TestSEC0002FailClosedOnUnknownFieldLeak defense in depth: if an already-tokenized identifier
// reappears via an unknown key, the leak assertion must reject the entire fact (failing closed, never
// degrading to sending raw identifiers). Free-text list fields (allowlisted keys like warnings) have
// their content stripped in full — see the NEW-AI-02 regression, outside the scope of this test case.
func TestSEC0002FailClosedOnUnknownFieldLeak(t *testing.T) {
	fact := map[string]any{
		"schema": "app_db", "table": "users",
		"note": "table users is large", // Unknown free-text field carrying a real table name
	}
	if err := tokenizeToolFact(fact, privacy.NewRunMapper()); err == nil {
		t.Fatal("expected fail-closed error for identifier leak via unknown field")
	}
}

// TestNEWAI02CrossCallEchoFailsClosed NEW-AI-02 regression: the forbidden set must cover identifiers
// tokenized in prior calls — after call 1 tokenizes "users", echoing "users" via an unknown free-text
// field in call 2's fact must fail closed (before the fix, the forbidden set started empty on each call
// and contained only values tokenized in that call, letting cross-call echoes slip through).
func TestNEWAI02CrossCallEchoFailsClosed(t *testing.T) {
	mapper := privacy.NewRunMapper()
	// First call: tokenize schema/table/index (mapper snapshot captures these values).
	first := map[string]any{"schema": "app_db", "table": "users",
		"index_names": []any{"idx_users_email"}}
	if err := tokenizeToolFact(first, mapper); err != nil {
		t.Fatal(err)
	}
	// Second call: the fact itself carries no identifier fields, but an unknown free-text field echoes
	// the real table name from the first call — must fail closed.
	second := map[string]any{"table_count": 2, "note": "users grew 3x"}
	if err := tokenizeToolFact(second, mapper); err == nil {
		t.Fatal("cross-call identifier echo must fail closed")
	}
	// Echoing a previously tokenized index name is likewise caught.
	third := map[string]any{"table_count": 2, "note": "missing idx_users_email"}
	if err := tokenizeToolFact(third, mapper); err == nil {
		t.Fatal("cross-call index-name echo must fail closed")
	}
	// An identifier tokenized in the current call echoed via an unknown field within the same fact is also caught
	// (the snapshot is constructed after tokenization).
	same := map[string]any{"schema": "app_db", "note": "app_db is audited"}
	if err := tokenizeToolFact(same, mapper); err == nil {
		t.Fatal("same-call echo via unknown field must fail closed")
	}
	// Clean facts are unaffected by the expanded forbidden set.
	clean := map[string]any{"schema": "app_db", "table": "users", "column_count": 5}
	if err := tokenizeToolFact(clean, mapper); err != nil {
		t.Fatalf("clean fact must pass: %v", err)
	}
}

// TestNEWAI02FreeTextFieldsStripped NEW-AI-02 Layer 1: content of free-text list fields
// (allowlist factFreeTextListFields) never enters the model channel — leak assertions cannot see
// identifiers that were never tokenized (such as arbitrary index names inside engine warnings), so
// free-text content is stripped in full; raw content is preserved exclusively in the RawFact controlled
// ciphertext channel (L7 fidelity lives on the RawFact side of registry.Execute, bypassing this function).
func TestNEWAI02FreeTextFieldsStripped(t *testing.T) {
	mapper := privacy.NewRunMapper()
	fact := map[string]any{
		"access_method": "Index Scan", "estimated_rows": 42,
		"index_names": []any{"idx_users_email"},
		"warnings":    []any{"never-tokenized-idx warning text", "another engine note"},
		"estimate":    true,
	}
	if err := tokenizeToolFact(fact, mapper); err != nil {
		t.Fatal(err)
	}
	if got := fact["warnings"]; got == nil {
		t.Fatalf("warnings key must stay as empty placeholder, got %v", got)
	} else if list, ok := got.([]any); !ok || len(list) != 0 {
		t.Fatalf("warnings content must be stripped, got %v", got)
	}
	// Warning text containing never-tokenized identifiers must not remain in fact string values.
	for _, v := range collectFactStringValues(fact) {
		if strings.Contains(v, "never-tokenized-idx") {
			t.Fatalf("free text content leaked into model channel: %q", v)
		}
	}
	// Clean warnings (containing no identifiers) are likewise stripped — content-level allowlist semantics
	// perform no content inspection (since content inspection cannot prove the absence of identifiers).
	fact2 := map[string]any{"warnings": []any{"harmless note"}}
	if err := tokenizeToolFact(fact2, mapper); err != nil {
		t.Fatal(err)
	}
	if list := fact2["warnings"].([]any); len(list) != 0 {
		t.Fatalf("harmless free text must also be stripped: %v", list)
	}
}

// TestSEC0002TokenizeRejectsInvalidInputs nil mapper / nil fact / identifiers containing mapping
// delimiter characters always fail closed.
func TestSEC0002TokenizeRejectsInvalidInputs(t *testing.T) {
	if err := tokenizeToolFact(nil, privacy.NewRunMapper()); err == nil {
		t.Fatal("nil fact must fail")
	}
	if err := tokenizeToolFact(map[string]any{"schema": "ok"}, nil); err == nil {
		t.Fatal("nil mapper must fail")
	}
	bad := map[string]any{"tables": []any{"na\tme"}}
	if err := tokenizeToolFact(bad, privacy.NewRunMapper()); err == nil {
		t.Fatal("identifier with mapping delimiter must fail closed")
	}
	// Empty strings are skipped (do not constitute identifiers and do not fail).
	ok := map[string]any{"schema": "", "tables": []any{"", "users"}}
	if err := tokenizeToolFact(ok, privacy.NewRunMapper()); err != nil {
		t.Fatalf("empty identifiers should be skipped: %v", err)
	}
	if ok["tables"].([]any)[1] != "table_1" {
		t.Fatalf("non-empty table must be tokenized: %v", ok["tables"])
	}
}

// TestSEC0002DetokenizeToolArgsRoundTrip tool argument detokenization closed loop: the model uses
// tokens from tokenized facts as next-hop schema/table arguments — which must be restored to real
// identifiers before querying the DB; cross-category tokens and unknown values pass through unchanged.
func TestSEC0002DetokenizeToolArgsRoundTrip(t *testing.T) {
	mapper := privacy.NewRunMapper()
	fact := map[string]any{"schema": "app_db", "table": "users",
		"tables": []any{"users", "orders"}}
	if err := tokenizeToolFact(fact, mapper); err != nil {
		t.Fatal(err)
	}
	schemaTok, _ := fact["schema"].(string)
	tableTok, _ := fact["table"].(string)
	if schemaTok == "app_db" || tableTok == "users" {
		t.Fatalf("facts not tokenized: %v", fact)
	}
	resolved := detokenizeToolArgs(map[string]any{"schema": schemaTok, "table": tableTok}, mapper)
	if resolved["schema"] != "app_db" || resolved["table"] != "users" {
		t.Fatalf("token args must resolve to originals: %v", resolved)
	}
	// Original arguments are not mutated (the ledger still records the model-visible form).
	orig := map[string]any{"schema": schemaTok}
	_ = detokenizeToolArgs(orig, mapper)
	if orig["schema"] != schemaTok {
		t.Fatalf("caller args must not be mutated: %v", orig)
	}
	// Cross-category tokens (a table token placed in the schema argument) and unknown values pass through unchanged.
	cross := detokenizeToolArgs(map[string]any{"schema": tableTok}, mapper)
	if cross["schema"] != tableTok {
		t.Fatalf("cross-kind token must pass through: %v", cross)
	}
	raw := detokenizeToolArgs(map[string]any{"schema": "public"}, mapper)
	if raw["schema"] != "public" {
		t.Fatalf("unknown value must pass through: %v", raw)
	}
	// Non-string arguments do not participate in detokenization.
	mixed := detokenizeToolArgs(map[string]any{"schema": 42}, mapper)
	if mixed["schema"] != 42 {
		t.Fatalf("non-string arg must pass through: %v", mixed)
	}
}

func TestToolFactBindingsReuseExactQuotedRunTokens(t *testing.T) {
	mapper := privacy.NewRunMapper()
	schemaExact, err := mapper.ScopedToken(privacy.KindSchema, privacy.ScopeCaseSensitive, "AppSchema")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Token(privacy.KindSchema, "AppSchema"); err != nil {
		t.Fatal(err)
	}
	tableExact, err := mapper.ScopedToken(privacy.KindTable, privacy.ScopeCaseSensitive, "Foo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Token(privacy.KindTable, "Foo"); err != nil {
		t.Fatal(err)
	}
	tableLower, err := mapper.ScopedToken(privacy.KindTable, privacy.ScopeCaseSensitive, "foo")
	if err != nil {
		t.Fatal(err)
	}
	columnExact, err := mapper.ScopedToken(privacy.KindColumn, privacy.ScopeCaseSensitive, "Bar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Token(privacy.KindColumn, "Bar"); err != nil {
		t.Fatal(err)
	}
	columnLower, err := mapper.ScopedToken(privacy.KindColumn, privacy.ScopeCaseSensitive, "bar")
	if err != nil {
		t.Fatal(err)
	}
	indexExact, err := mapper.ScopedToken(privacy.KindIndex, privacy.ScopeCaseSensitive, "IDX_Foo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Token(privacy.KindIndex, "IDX_Foo"); err != nil {
		t.Fatal(err)
	}

	factFor := func(table string) map[string]any {
		return map[string]any{
			"schema": "AppSchema",
			"table":  table,
			"columns": []any{
				map[string]any{"column_name": "Bar", "type_class": "integer"},
				map[string]any{"column_name": "bar", "type_class": "string"},
			},
			"indexes": []any{map[string]any{
				"index_name": "IDX_Foo", "columns": []any{"Bar", "bar"},
			}},
		}
	}
	fact := factFor("Foo")
	if err := tokenizeToolFactWithBindings(fact, mapper, map[string]any{
		"schema": schemaExact, "table": tableExact,
	}); err != nil {
		t.Fatal(err)
	}
	if fact["schema"] != schemaExact || fact["table"] != tableExact {
		t.Fatalf("top-level binding not reused: %v", fact)
	}
	columns := fact["columns"].([]any)
	if columns[0].(map[string]any)["column_name"] != columnExact ||
		columns[1].(map[string]any)["column_name"] != columnLower {
		t.Fatalf("case-sensitive columns collapsed: %v", columns)
	}
	index := fact["indexes"].([]any)[0].(map[string]any)
	if index["index_name"] != indexExact {
		t.Fatalf("quoted index token not reused: %v", index)
	}

	lower := factFor("foo")
	if err := tokenizeToolFactWithBindings(lower, mapper, map[string]any{
		"schema": schemaExact, "table": tableLower,
	}); err != nil {
		t.Fatal(err)
	}
	if lower["table"] != tableLower || lower["table"] == tableExact {
		t.Fatalf("Foo and foo collapsed to one table token: Foo=%s foo=%s",
			tableExact, lower["table"])
	}

	mismatch := factFor("other")
	if err := tokenizeToolFactWithBindings(mismatch, mapper, map[string]any{
		"schema": schemaExact, "table": tableExact,
	}); err == nil {
		t.Fatal("fact/table binding mismatch must fail closed")
	}
}

func TestToolFactExposesAllRealSQLReferenceTokens(t *testing.T) {
	mapper := privacy.NewRunMapper()
	columnFolded, err := mapper.Token(privacy.KindColumn, "id")
	if err != nil {
		t.Fatal(err)
	}
	columnExact, err := mapper.ScopedToken(
		privacy.KindColumn, privacy.ScopeCaseSensitive, "id",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"cte:fold", "alias:exact", "constraint:fold"} {
		if _, err := mapper.ScopedToken(privacy.KindColumn, scope, "id"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mapper.Token(privacy.KindAlias, "id"); err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Token(privacy.KindConstraint, "id"); err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.Token(privacy.KindGeneric, "id"); err != nil {
		t.Fatal(err)
	}

	upperFolded, err := mapper.Token(privacy.KindColumn, "FooColumn")
	if err != nil {
		t.Fatal(err)
	}
	upperExact, err := mapper.ScopedToken(
		privacy.KindColumn, privacy.ScopeCaseSensitive, "FooColumn",
	)
	if err != nil {
		t.Fatal(err)
	}
	lowerFolded, err := mapper.Token(privacy.KindColumn, "fooColumn")
	if err != nil {
		t.Fatal(err)
	}
	lowerExact, err := mapper.ScopedToken(
		privacy.KindColumn, privacy.ScopeCaseSensitive, "fooColumn",
	)
	if err != nil {
		t.Fatal(err)
	}
	if lowerFolded != upperFolded {
		t.Fatalf("case-insensitive SQL identity must share folded token: %s vs %s",
			lowerFolded, upperFolded)
	}
	if lowerExact == upperExact {
		t.Fatalf("quoted case-distinct identities must keep separate exact tokens: %s",
			lowerExact)
	}

	indexFolded, err := mapper.Token(privacy.KindIndex, "idx_id")
	if err != nil {
		t.Fatal(err)
	}
	indexExact, err := mapper.ScopedToken(
		privacy.KindIndex, privacy.ScopeCaseSensitive, "idx_id",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapper.ScopedToken(
		privacy.KindIndex, "cte:exact", "idx_id",
	); err != nil {
		t.Fatal(err)
	}

	fact := map[string]any{
		"schema": "app_db",
		"table":  "users",
		"columns": []any{
			map[string]any{"column_name": "id", "type_class": "integer"},
			map[string]any{"column_name": "FooColumn", "type_class": "string"},
		},
		"indexes": []any{
			map[string]any{"index_name": "idx_id", "columns": []any{"id"}},
		},
	}
	if err := tokenizeToolFact(fact, mapper); err != nil {
		t.Fatal(err)
	}

	columns := fact["columns"].([]any)
	idFact := columns[0].(map[string]any)
	if got := idFact["column_name"]; got != columnExact {
		t.Fatalf("primary exact column token = %v, want %s", got, columnExact)
	}
	assertFactTokenList(t, idFact["sql_reference_tokens"],
		[]string{columnFolded, columnExact})

	upperFact := columns[1].(map[string]any)
	if got := upperFact["column_name"]; got != upperExact {
		t.Fatalf("primary exact column token = %v, want %s", got, upperExact)
	}
	assertFactTokenList(t, upperFact["sql_reference_tokens"],
		[]string{upperFolded, upperExact})
	for _, got := range upperFact["sql_reference_tokens"].([]any) {
		if got == lowerExact {
			t.Fatalf("case-distinct exact token %q leaked into FooColumn facts", got)
		}
	}

	index := fact["indexes"].([]any)[0].(map[string]any)
	if got := index["index_name"]; got != indexExact {
		t.Fatalf("primary exact index token = %v, want %s", got, indexExact)
	}
	assertFactTokenList(t, index["sql_reference_tokens"],
		[]string{indexFolded, indexExact})

	raw, err := json.Marshal(provider.NormalizedEvidenceRef{
		EvidenceID: "evidence-1",
		FactKind:   tool.NameSchemaMetadata,
		Fact:       fact,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, original := range []string{"FooColumn", "fooColumn"} {
		if strings.Contains(body, original) {
			t.Fatalf("provider request contains raw identifier %q: %s", original, body)
		}
	}
}

func assertFactTokenList(t *testing.T, value any, want []string) {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("token list type = %T, want []any (%v)", value, value)
	}
	if len(list) != len(want) {
		t.Fatalf("token list = %v, want %v", list, want)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("token list = %v, want %v", list, want)
		}
	}
}
