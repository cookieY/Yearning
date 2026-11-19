package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestMetadataFactPrivacyTokenizesColumnDefinitions(t *testing.T) {
	mapper := privacy.NewRunMapper()
	fact := map[string]any{
		"schema": "app_db",
		"table":  "accounts",
		"columns": []any{
			map[string]any{
				"column_name": "account_id",
				"ordinal":     float64(1),
				"type_class":  "integer",
				"nullable":    false,
				"has_default": true,
				"has_comment": false,
			},
			map[string]any{
				"column_name": "email",
				"ordinal":     float64(2),
				"type_class":  "string",
				"type_length": float64(320),
				"nullable":    true,
				"has_default": false,
				"has_comment": true,
			},
		},
		"indexes": []any{
			map[string]any{
				"index_name":       "accounts_email_idx",
				"columns":          []any{"email"},
				"included_columns": []any{"account_id"},
			},
		},
	}
	if err := tokenizeToolFact(fact, mapper); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{"app_db", "accounts", "account_id", "email", "accounts_email_idx"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("provider fact contains raw identifier %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"column_1"`) || !strings.Contains(body, `"column_2"`) {
		t.Fatalf("column tokens missing: %s", body)
	}
	if strings.Count(body, `"column_1"`) < 2 {
		t.Fatalf("included column token missing or not joined to existing token: %s", body)
	}
	if !strings.Contains(body, `"type_class":"string"`) || !strings.Contains(body, `"has_comment":true`) {
		t.Fatalf("controlled type facts missing: %s", body)
	}
}

func TestMetadataFactPreservesAllowedPhysicalSchemaToken(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logical, err := mapper.Token(privacy.KindSchema, "prod")
	if err != nil {
		t.Fatal(err)
	}
	table, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	binding := bindingOf("prod")
	fact := map[string]any{
		"schema": "prod", "table": "orders", "existence": "present",
		"columns":           []any{map[string]any{"column_name": "id"}},
		"metadata_complete": true,
	}
	args := map[string]any{"schema": logical, "table": table}
	if err := projectToolFactSchema(fact, args, mapper, binding); err != nil {
		t.Fatal(err)
	}
	if fact["schema"] != "prod" || fact["table"] != "orders" {
		t.Fatalf("physical fact projection = %v", fact)
	}
	if !factMatchesRequestedScope("schema_metadata", fact, map[string]any{"schema": "prod", "table": "orders"}) {
		t.Fatal("requested scope did not match projected physical fact")
	}
	if err := tokenizeToolFactWithBindings(fact, mapper, args); err != nil {
		t.Fatal(err)
	}
	if fact["schema"] != logical || fact["table"] != table {
		t.Fatalf("provider-side fact tokens = %v, want schema=%s table=%s", fact, logical, table)
	}
}

func TestMetadataFactPreservesRequestedPhysicalSchemaToken(t *testing.T) {
	mapper := privacy.NewRunMapper()
	physicalToken, err := mapper.ScopedToken(privacy.KindSchema, "", "exec_target")
	if err != nil {
		t.Fatal(err)
	}
	tableToken, err := mapper.ScopedToken(privacy.KindTable, "", "accounts")
	if err != nil {
		t.Fatal(err)
	}
	binding := bindingOf("exec_target")
	fact := map[string]any{
		"schema": "exec_target", "table": "accounts", "existence": "present",
		"metadata_complete": true,
	}
	modelArgs := map[string]any{"schema": physicalToken, "table": tableToken}
	scope := newMetadataScope()
	scope.schemas[physicalToken] = true
	scope.objects[physicalToken] = map[string]bool{tableToken: true}
	if !knownToolIdentifiers(modelArgs, mapper) || !scope.allows(modelArgs) {
		t.Fatalf("physical model request fell outside the SQL object scope: args=%v scope=%v", modelArgs, scope)
	}
	executionArgs := detokenizeToolArgs(modelArgs, mapper)
	resolved, ok := resolveStageBindingToolArgs(binding, executionArgs)
	if !ok || resolved["schema"] != "exec_target" {
		t.Fatalf("physical execution args = %v ok=%v, want schema exec_target", resolved, ok)
	}
	if err := projectToolFactSchema(fact, modelArgs, mapper, binding); err != nil {
		t.Fatal(err)
	}
	if fact["schema"] != "exec_target" || fact["table"] != "accounts" {
		t.Fatalf("physical fact projection = %v", fact)
	}
	if !factMatchesRequestedScope("schema_metadata", fact, executionArgs) {
		t.Fatalf("requested physical scope did not match fact: args=%v fact=%v", executionArgs, fact)
	}
	if err := tokenizeToolFactWithBindings(fact, mapper, modelArgs); err != nil {
		t.Fatal(err)
	}
	if fact["schema"] != physicalToken || fact["table"] != tableToken {
		t.Fatalf("provider-side fact tokens = %v, want schema=%s table=%s", fact, physicalToken, tableToken)
	}
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{"exec_target", "accounts"} {
		if strings.Contains(strings.ToLower(string(raw)), identifier) {
			t.Fatalf("provider fact contains raw %q identifier: %s", identifier, raw)
		}
	}
}
