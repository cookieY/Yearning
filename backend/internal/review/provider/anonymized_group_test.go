package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnonymizedGroupObjectReferenceJSONShape(t *testing.T) {
	group := AnonymizedGroup{
		GroupID:                     "g-1",
		RepresentativeShape:         "kind=dml op=update where=true",
		RepresentativeAnonymizedSQL: "update table_1 set column_1 = :int where column_2 = :int",
		MetadataContextComplete:     true,
		StatementCount:              2,
		StatementOrdinal:            1,
		StatementOrdinalRanges:      []StatementOrdinalRange{{Start: 1, End: 2}},
		Objects: []AnonymizedObjectReference{{
			Token:            "column_1",
			Kind:             "column",
			Relation:         "write",
			SchemaToken:      "schema_1",
			ParentToken:      "table_1",
			StatementOrdinal: 1,
		}},
	}
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"group_id", "representative_shape", "representative_anonymized_sql",
		"metadata_context_complete", "statement_count", "statement_ordinal",
		"statement_ordinal_ranges", "objects",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing JSON field %q: %s", key, raw)
		}
	}
	ranges, ok := decoded["statement_ordinal_ranges"].([]any)
	if !ok || len(ranges) != 1 {
		t.Fatalf("statement ordinal ranges shape = %#v", decoded["statement_ordinal_ranges"])
	}
	objects, ok := decoded["objects"].([]any)
	if !ok || len(objects) != 1 {
		t.Fatalf("objects shape = %#v", decoded["objects"])
	}
	object := objects[0].(map[string]any)
	for _, key := range []string{
		"token", "kind", "relation", "schema_token", "parent_token", "statement_ordinal",
	} {
		if _, ok := object[key]; !ok {
			t.Fatalf("missing object field %q: %s", key, raw)
		}
	}
}

func TestAnonymizedGroupProviderBytesExcludeRawIdentifiers(t *testing.T) {
	req := Request{
		RequestSchemaVersion: 1,
		ReviewRunID:          "run-1",
		SystemPrompt:         "review normally",
		AnonymizedStatementGroups: []AnonymizedGroup{{
			GroupID:                     "g-1",
			RepresentativeShape:         "kind=dml op=update where=true",
			RepresentativeAnonymizedSQL: "update table_1 set column_1 = :string where column_2 = :int",
			MetadataContextComplete:     true,
			StatementCount:              1,
			StatementOrdinal:            1,
			Objects: []AnonymizedObjectReference{
				{Token: "table_1", Kind: "table", Relation: "target", SchemaToken: "schema_1", StatementOrdinal: 1},
				{Token: "column_1", Kind: "column", Relation: "write", ParentToken: "table_1", StatementOrdinal: 1},
			},
		}},
		OutputSchema: []byte(`{"type":"object"}`),
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"app", "orders", "amount", "actual-password", "52", "internal-comment",
	} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("provider request leaked %q: %s", forbidden, raw)
		}
	}
}
