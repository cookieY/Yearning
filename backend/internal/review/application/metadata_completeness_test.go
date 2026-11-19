package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestRequestedMetadataCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name string
		fact map[string]any
		want bool
	}{
		{"present", map[string]any{"existence": "present", "metadata_complete": true}, true},
		{"absent", map[string]any{"existence": "absent", "metadata_complete": true}, true},
		{"unknown", map[string]any{"existence": "unknown", "metadata_complete": true}, false},
		{"truncated", map[string]any{"existence": "present", "metadata_complete": true, "__truncated__": true}, false},
		{"nested truncated", map[string]any{"existence": "present", "metadata_complete": true, "columns": []any{"__truncated__"}}, false},
		{"incomplete", map[string]any{"existence": "present", "metadata_complete": false}, false},
		{"unknown column type", map[string]any{"existence": "present", "metadata_complete": false, "columns": []any{map[string]any{"type_status": "unknown"}}}, false},
		{"unknown estimate", map[string]any{"existence": "present", "metadata_complete": true, "estimates": map[string]any{"status": "unsupported", "rows": nil}}, false},
		{"unknown column type", map[string]any{"metadata_complete": true, "columns": []any{map[string]any{"type_status": "unknown"}}}, false},
		{"observed estimate", map[string]any{"existence": "present", "metadata_complete": true, "estimates": map[string]any{"status": "observed", "rows": int64(0)}}, true},
		{"malformed existence", map[string]any{"existence": "maybe", "metadata_complete": true}, false},
		{"malformed type status", map[string]any{"type_status": "trusted", "metadata_complete": true}, false},
		{"non string status", map[string]any{"status": true, "metadata_complete": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := metadataFactComplete(tc.fact); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestMetadataScopeRequiresExactObservedObject(t *testing.T) {
	scope := newMetadataScope()
	scope.objects["schema_1"] = map[string]bool{"table_1": true}
	scope.objects["schema_2"] = map[string]bool{"table_1": true}
	fact := func(schema, table string) provider.NormalizedEvidenceRef {
		return provider.NormalizedEvidenceRef{FactKind: "schema_metadata", Fact: map[string]any{
			"schema": schema, "table": table, "existence": "absent",
			"columns": []any{}, "metadata_complete": true,
		}}
	}
	if scope.observed(nil) || scope.observed([]provider.NormalizedEvidenceRef{fact("schema_1", "table_1")}) {
		t.Fatal("missing or cross-schema evidence fulfilled object baseline")
	}
	if !scope.observed([]provider.NormalizedEvidenceRef{fact("schema_1", "table_1"), fact("schema_2", "table_1")}) {
		t.Fatal("observed absence is a complete fact, not a hardcoded risk")
	}
	if scope.allows(map[string]any{"schema": "schema_1", "table": "table_2"}) {
		t.Fatal("unrelated table was allowed")
	}
}

func TestMetadataScopeRequiresSchemaMetadataNotEstimatesOrIndexProfiles(t *testing.T) {
	scope := newMetadataScope()
	scope.objects["schema_1"] = map[string]bool{"table_1": true}
	fact := func(kind string) provider.NormalizedEvidenceRef {
		return provider.NormalizedEvidenceRef{FactKind: kind, Fact: map[string]any{
			"schema": "schema_1", "table": "table_1", "existence": "present",
			"metadata_complete": true,
		}}
	}
	for _, kind := range []string{"table_statistics", "index_metadata"} {
		if scope.observed([]provider.NormalizedEvidenceRef{fact(kind)}) {
			t.Fatalf("%s alone satisfied the table definition baseline", kind)
		}
	}
	schemaFact := fact("schema_metadata")
	schemaFact.Fact["columns"] = []any{}
	if !scope.observed([]provider.NormalizedEvidenceRef{
		fact("table_statistics"), fact("index_metadata"), schemaFact,
	}) {
		t.Fatal("complete schema metadata did not satisfy the per-table baseline")
	}
}

func TestMetadataBindingPreservesExplicitPhysicalScope(t *testing.T) {
	mapper := privacy.NewRunMapper()
	qualifiedSchema, _ := mapper.Token(privacy.KindSchema, "audit")
	groups := []provider.AnonymizedGroup{{RepresentativeAnonymizedSQL: "select * from audit.table_2", MetadataContextComplete: true,
		Objects: []provider.AnonymizedObjectReference{{Token: "table_2", Kind: "table", SchemaToken: qualifiedSchema}}}}
	bound, scope, complete := bindGroupMetadata(groups, bindingOf("review_default", "audit"), mapper)
	if !complete || !scope.objects[qualifiedSchema]["table_2"] {
		t.Fatal("explicit physical scope was lost")
	}
	if bound[0].Objects[0].SchemaToken != qualifiedSchema {
		t.Fatal("binding failed to preserve the explicit physical scope")
	}
	if knownToolIdentifiers(map[string]any{"schema": "audit"}, mapper) || knownToolIdentifiers(map[string]any{"table": qualifiedSchema}, mapper) {
		t.Fatal("raw or wrong-kind identifier accepted")
	}
	if !knownToolIdentifiers(map[string]any{"schema": qualifiedSchema}, mapper) {
		t.Fatal("known schema token rejected")
	}
}

// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: default scope directly uses the token of the allowed
// physical schema (with no logical-alias translation).
func TestMetadataBindingUsesDefaultSchemaForModelScope(t *testing.T) {
	mapper := privacy.NewRunMapper()
	defaultToken, _ := mapper.Token(privacy.KindSchema, "prod")
	outOfScope, _ := mapper.Token(privacy.KindSchema, "app")
	groups := []provider.AnonymizedGroup{{
		RepresentativeAnonymizedSQL: "select * from table_1",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{
			{Token: "table_1", Kind: "table"},
		},
	}}
	bound, scope, complete := bindGroupMetadata(groups, bindingOf("prod"), mapper)
	if !complete || !scope.objects[defaultToken]["table_1"] || scope.objects[outOfScope]["table_1"] {
		t.Fatalf("default scope must use the allowed schema token: object=%+v scope=%+v", bound[0].Objects, scope)
	}
	if !scope.allows(map[string]any{"schema": defaultToken, "table": "table_1"}) {
		t.Fatal("model arguments with the allowed schema token did not match the object scope")
	}
}

func TestMetadataBindingExplicitLogicalSchemaIsIncompleteAndOutOfScope(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logical, _ := mapper.ScopedToken(privacy.KindSchema, "", "app")
	table, _ := mapper.ScopedToken(privacy.KindTable, "", "orders")
	groups := []provider.AnonymizedGroup{{
		GroupID:                     "group-explicit-logical",
		RepresentativeAnonymizedSQL: "update app.orders set amount = amount + 1",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{{
			Token: table, Kind: "table", SchemaToken: logical,
		}},
	}}
	bound, scope, complete := bindGroupMetadata(groups, bindingOf("prod"), mapper)
	if complete || bound[0].MetadataContextComplete {
		t.Fatal("explicit logical schema must make metadata context incomplete")
	}
	for _, args := range []map[string]any{
		{"schema": logical},
		{"schema": logical, "table": table},
		{"schema": logical, "group_id": "group-explicit-logical"},
		{"group_id": "group-explicit-logical"},
	} {
		if scope.allows(args) {
			t.Fatalf("explicit logical schema escaped object scope: %v", args)
		}
	}
}

func TestMetadataBindingExplicitPhysicalSchemaRemainsScoped(t *testing.T) {
	mapper := privacy.NewRunMapper()
	physical, _ := mapper.ScopedToken(privacy.KindSchema, "", "prod")
	logical, _ := mapper.ScopedToken(privacy.KindSchema, "", "app")
	table, _ := mapper.ScopedToken(privacy.KindTable, "", "orders")
	groups := []provider.AnonymizedGroup{{
		GroupID:                     "group-explicit-physical",
		RepresentativeAnonymizedSQL: "update prod.orders set amount = amount + 1",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{{
			Token: table, Kind: "table", SchemaToken: physical,
		}},
	}}
	_, scope, complete := bindGroupMetadata(groups, bindingOf("prod"), mapper)
	if !complete {
		t.Fatal("explicit allowed physical schema must remain eligible")
	}
	for _, args := range []map[string]any{
		{"schema": physical},
		{"schema": physical, "table": table},
		{"group_id": "group-explicit-physical"},
	} {
		if !scope.allows(args) {
			t.Fatalf("explicit physical schema left its object scope: %v", args)
		}
	}
	for _, args := range []map[string]any{
		{"schema": logical},
		{"schema": logical, "table": table},
		{"schema": logical, "group_id": "group-explicit-physical"},
	} {
		if scope.allows(args) {
			t.Fatalf("logical alias was globally opened for explicit physical SQL: %v", args)
		}
	}
}

func TestUnsupportedMetadataObjectCannotBypassBaseline(t *testing.T) {
	for _, object := range []provider.AnonymizedObjectReference{
		{Token: "index_1", Kind: "index", Relation: "target"},
		{Token: "schema_1", Kind: "schema", Relation: "target"},
		{Token: "constraint", Kind: "constraint", Relation: "target", ParentToken: "table_1"},
	} {
		groups := []provider.AnonymizedGroup{{RepresentativeAnonymizedSQL: "drop object", MetadataContextComplete: true, Objects: []provider.AnonymizedObjectReference{object}}}
		_, _, complete := bindGroupMetadata(groups, nil, privacy.NewRunMapper())
		if complete {
			t.Fatalf("unsupported %s object skipped baseline", object.Kind)
		}
	}
}

func TestIndexMetadataMustBeObservedForSQLIndexReference(t *testing.T) {
	groups := []provider.AnonymizedGroup{{Objects: []provider.AnonymizedObjectReference{
		{Token: "index_1", Kind: "index", ParentToken: "table_1", SchemaToken: "schema_1", Relation: "define"},
	}}}
	fact := provider.NormalizedEvidenceRef{FactKind: "schema_metadata", Fact: map[string]any{
		"schema": "schema_1", "table": "table_1", "existence": "present",
		"columns": []any{}, "metadata_complete": true,
	}}
	if metadataAdditionalFactsObserved(groups, []provider.NormalizedEvidenceRef{fact}) {
		t.Fatal("table existence replaced required index profile")
	}
	fact.FactKind = "index_metadata"
	fact.Fact["indexes"] = []any{}
	if !metadataAdditionalFactsObserved(groups, []provider.NormalizedEvidenceRef{fact}) {
		t.Fatal("complete scoped index profile was ignored")
	}
}
