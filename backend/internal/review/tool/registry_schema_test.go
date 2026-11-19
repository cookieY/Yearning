// registry_schema_test.go NEW-AI-03 regression: JSON Schema shape of ToolDescriptor parameter
// declarations — model bootstrap requires knowable parameter names/types/requiredness;
// declarations must share the same source of truth as strict validation in validateArguments (ArgumentNames allowlist).
package tool

import (
	"encoding/json"
	"testing"
)

func TestInputSchemaShape(t *testing.T) {
	r := NewRegistry()
	cases := map[string]struct {
		required int
		props    []string
	}{
		NameDatasourceIdentity: {required: 0, props: []string{}},
		NameSchemaMetadata:     {required: 2, props: []string{"schema", "table"}},
		NameTableStatistics:    {required: 2, props: []string{"schema", "table"}},
		NameIndexMetadata:      {required: 2, props: []string{"schema", "table"}},
		NameSafeExplain:        {required: 2, props: []string{"schema", "group_id"}},
	}
	for name, want := range cases {
		d, ok := r.Descriptor(name)
		if !ok {
			t.Fatalf("%s missing from registry", name)
		}
		schema := d.InputSchema()
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v", name, schema["type"])
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) != len(want.props) {
			t.Fatalf("%s properties = %v, want %v", name, schema["properties"], want.props)
		}
		for _, p := range want.props {
			spec, ok := props[p].(map[string]any)
			if !ok || spec["type"] != "string" {
				t.Fatalf("%s property %q must be string-typed: %v", name, p, props[p])
			}
		}
		req, ok := schema["required"].([]string)
		if !ok || len(req) != want.required {
			t.Fatalf("%s required = %v, want %d entries", name, schema["required"], want.required)
		}
		// Declarations must be valid JSON (Provider request serialization channel).
		if _, err := json.Marshal(schema); err != nil {
			t.Fatalf("%s schema not serializable: %v", name, err)
		}
	}
}

func TestSchemaMetadataOptionalTableIsValidatedWhenPresent(t *testing.T) {
	r := NewRegistry()
	d, ok := r.Descriptor(NameSchemaMetadata)
	if !ok {
		t.Fatal("schema_metadata missing from registry")
	}
	if err := validateArguments(d, map[string]any{"schema": "schema_1", "table": "table_1"}); err != nil {
		t.Fatalf("optional table must validate: %v", err)
	}
	for _, malformed := range []any{"", 42, nil} {
		if err := validateArguments(d, map[string]any{"schema": "schema_1", "table": malformed}); err == nil {
			t.Fatalf("present optional table must be a non-empty string, got %#v", malformed)
		}
	}
}

func TestInputSchemaMatchesArgumentValidation(t *testing.T) {
	// Declared properties and required sets must share the same source of truth as validateArguments:
	// arguments constructed from declared required fields must pass validation; undeclared arguments must be rejected.
	r := NewRegistry()
	for _, name := range r.Names() {
		d, _ := r.Descriptor(name)
		schema := d.InputSchema()
		args := map[string]any{}
		for _, req := range schema["required"].([]string) {
			args[req] = "x"
		}
		if err := validateArguments(d, args); err != nil {
			t.Fatalf("%s: args built from declared required set must validate: %v", name, err)
		}
		args["undeclared"] = "y"
		if err := validateArguments(d, args); err == nil {
			t.Fatalf("%s: undeclared argument must be rejected", name)
		}
	}
}
