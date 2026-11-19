// schema_binding_test.go SEC-0041 unit tests: tool argument boundary checks against the stage's
// frozen object scope (pure functions; see sec0041_schema_binding_integration_test.go for end-to-end behavior).
// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: flow stages no longer configure logical-to-physical Schema
// mappings, so the binding set is the set of allowed physical schema names (MySQL = datasource default
// database; PostgreSQL = explicit segments of the frozen search_path).
package application

import (
	"context"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

func bindingOf(schemas ...string) *stageSchemaBinding {
	set := map[string]bool{}
	for _, s := range schemas {
		set[s] = true
	}
	return &stageSchemaBinding{defensible: true, schemas: set}
}

// TestSEC0041BindingAllowsInScope in-scope objects are allowed.
func TestSEC0041BindingAllowsInScope(t *testing.T) {
	b := bindingOf("shop", "report")
	cases := []struct {
		name string
		args map[string]any
	}{
		{"schema_metadata", map[string]any{"schema": "shop"}},
		{"table_statistics", map[string]any{"schema": "report", "table": "orders"}},
		{"index_metadata", map[string]any{"schema": "shop", "table": "t_1"}},
		{"safe_explain", map[string]any{"schema": "shop", "group_id": "g-1"}},
		{"no_schema_arg", map[string]any{"group_id": "g-1"}},
	}
	for _, c := range cases {
		if !stageBindingAllowsTool(b, c.args) {
			t.Fatalf("%s: in-scope args must pass", c.name)
		}
	}
}

// TestSEC0041BindingRejectsOutOfScope out-of-scope schemas, unavailable bindings,
// and malformed table names are uniformly rejected (causing the caller to book 2012 without touching the DB).
func TestSEC0041BindingRejectsOutOfScope(t *testing.T) {
	b := bindingOf("shop")
	cases := []struct {
		name string
		b    *stageSchemaBinding
		args map[string]any
	}{
		{"schema_out_of_scope", b, map[string]any{"schema": "otherdb"}},
		{"table_pair_out_of_scope", b, map[string]any{"schema": "otherdb", "table": "orders"}},
		{"undefensible_binding", &stageSchemaBinding{}, map[string]any{"schema": "shop"}},
		{"nil_binding", nil, map[string]any{"schema": "shop"}},
		{"table_malformed_identifier", b, map[string]any{"schema": "shop", "table": "bad name"}},
		{"table_meta_chars", b, map[string]any{"schema": "shop", "table": "orders`; drop"}},
	}
	for _, c := range cases {
		if stageBindingAllowsTool(c.b, c.args) {
			t.Fatalf("%s: must be rejected", c.name)
		}
	}
}

// TestSEC0041BindingEmptySchemaDelegatesToRegistry missing/empty schema arguments are delegated to
// registry required-argument validation for rejection (likewise booked as 2012 without touching the DB);
// the boundary layer never overrides other checks to allow them.
func TestSEC0041BindingEmptySchemaDelegatesToRegistry(t *testing.T) {
	b := bindingOf("shop")
	if !stageBindingAllowsTool(b, map[string]any{}) {
		t.Fatal("empty args must pass boundary (registry rejects missing required)")
	}
	if !stageBindingAllowsTool(b, map[string]any{"schema": ""}) {
		t.Fatal("empty schema must pass boundary (registry rejects empty required)")
	}
	if !stageBindingAllowsTool(&stageSchemaBinding{}, map[string]any{}) {
		t.Fatal("no schema arg is unaffected by fail-closed binding")
	}
}

// TestSEC0041AllowedPhysicalSchemaResolvesAsItself SQL qualifiers are never rewritten:
// an allowed physical schema resolves as itself, and caller-visible arguments are not mutated.
func TestSEC0041AllowedPhysicalSchemaResolvesAsItself(t *testing.T) {
	b := bindingOf("exec_target")
	resolved, ok := resolveStageBindingToolArgs(b, map[string]any{
		"schema": "exec_target",
		"table":  "accounts",
	})
	if !ok || resolved["schema"] != "exec_target" {
		t.Fatalf("allowed schema must resolve as itself: %v ok=%v", resolved["schema"], ok)
	}
	args := map[string]any{"schema": "exec_target"}
	_, _ = resolveStageBindingToolArgs(b, args)
	if args["schema"] != "exec_target" {
		t.Fatalf("caller-visible model args mutated: %v", args)
	}
	if _, ok := resolveStageBindingToolArgs(b, map[string]any{"schema": "outside"}); ok {
		t.Fatal("out-of-scope physical schema must be rejected")
	}
}

type explainRecordingSource struct {
	tool.FactSource
	schema string
}

func (s *explainRecordingSource) SafeExplain(_ context.Context, schema, _ string) (map[string]any, error) {
	s.schema = schema
	return map[string]any{"access_method": "index_scan", "estimate": true}, nil
}

// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: SQL is sent to EXPLAIN verbatim
// with no logical-alias rewrite channel; the binding simply resolves the schema argument to an allowed physical name.
func TestSchemaBoundFactSourceUsesPhysicalSchemaWithoutSQLRewrite(t *testing.T) {
	raw := &explainRecordingSource{}
	source := bindSchemaFactSource(raw, bindingOf("prod"))
	res := tool.NewRegistry().Execute(context.Background(), source, "call-1", tool.NameSafeExplain,
		map[string]any{"schema": "prod", "group_id": "group-1"})
	if !res.Success {
		t.Fatalf("safe_explain failed: %v", res.ErrorCode)
	}
	if raw.schema != "prod" {
		t.Fatalf("execution-bound safe_explain = schema %q, want physical prod", raw.schema)
	}
	unbound := bindSchemaFactSource(&explainRecordingSource{}, &stageSchemaBinding{})
	if _, ok := unbound.(*explainRecordingSource); !ok {
		t.Fatal("undefensible binding must pass the raw source through")
	}
}
