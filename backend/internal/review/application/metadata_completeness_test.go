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

func TestBindPostgresGroupMetadataExplicitSchemaReference(t *testing.T) {
	binding, mapper, tableToken, _, publicToken := testPostgresOrdinalBinding(t)
	secretToken, err := mapper.Token(privacy.KindSchema, "secret")
	if err != nil {
		t.Fatal(err)
	}
	tempToken, err := mapper.Token(privacy.KindSchema, "pg_temp_3")
	if err != nil {
		t.Fatal(err)
	}
	catalogToken, err := mapper.Token(privacy.KindSchema, "pg_catalog")
	if err != nil {
		t.Fatal(err)
	}
	binding.postgres.Path.Effective = []string{"pg_catalog", "pg_temp_3", "app", "public"}

	// Allowed schema reference + table in same schema remains complete and adds schema to scope.
	validGroups := []provider.AnonymizedGroup{{
		GroupID:                     "pg-ref-ok",
		RepresentativeAnonymizedSQL: "UPDATE public.orders SET amount = 1",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{
			{Kind: "schema", Token: publicToken, Relation: "reference"},
			{Kind: "table", Token: tableToken, SchemaToken: publicToken, Relation: "target", StatementOrdinal: 2, StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 2, End: 2}}},
		},
	}}
	out, scope, complete := bindPostgresGroupMetadata(validGroups, binding, mapper)
	if !complete || !out[0].MetadataContextComplete || !scope.schemas[publicToken] {
		t.Fatalf("allowed postgres schema reference failed: complete=%v out=%+v scope=%+v", complete, out, scope)
	}

	// pg_catalog in Effective (non-temp) is allowed as reference token.
	catGroups := []provider.AnonymizedGroup{{
		GroupID:                     "pg-ref-cat",
		RepresentativeAnonymizedSQL: "SELECT 1 FROM pg_catalog.pg_class",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{
			{Kind: "schema", Token: catalogToken, Relation: "reference"},
		},
	}}
	outCat, scopeCat, completeCat := bindPostgresGroupMetadata(catGroups, binding, mapper)
	if !completeCat || !outCat[0].MetadataContextComplete || !scopeCat.schemas[catalogToken] {
		t.Fatalf("pg_catalog reference rejected: complete=%v out=%+v", completeCat, outCat)
	}

	// Out-of-path schema, pg_temp_* schema, and non-reference schema relations must remain incomplete.
	for name, obj := range map[string]provider.AnonymizedObjectReference{
		"out_of_path": {Kind: "schema", Token: secretToken, Relation: "reference"},
		"pg_temp":     {Kind: "schema", Token: tempToken, Relation: "reference"},
		"non_ref":     {Kind: "schema", Token: publicToken, Relation: "target"},
		"empty_rel":   {Kind: "schema", Token: publicToken},
		"unknown_tok": {Kind: "schema", Token: "schema_999", Relation: "reference"},
	} {
		t.Run(name, func(t *testing.T) {
			groups := []provider.AnonymizedGroup{{
				GroupID:                     "pg-bad-" + name,
				RepresentativeAnonymizedSQL: "SELECT 1",
				MetadataContextComplete:     true,
				Objects:                     []provider.AnonymizedObjectReference{obj},
			}}
			got, _, ok := bindPostgresGroupMetadata(groups, binding, mapper)
			if ok || got[0].MetadataContextComplete {
				t.Fatalf("%s unexpectedly accepted as complete: ok=%v got=%+v", name, ok, got)
			}
		})
	}

	// CREATE INDEX on an observed table binds its parent table schema and remains complete
	// (enforcing index_metadata observation downstream); standalone index without ParentToken stays incomplete.
	idxGroups := []provider.AnonymizedGroup{{
		GroupID:                     "pg-create-index",
		RepresentativeAnonymizedSQL: "CREATE INDEX idx_001 ON schema_001.tbl_001 (col_001)",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{
			{Kind: "index", Token: "idx_001", ParentToken: tableToken, Relation: "define"},
			{Kind: "table", Token: tableToken, SchemaToken: publicToken, StatementOrdinal: 2, StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 2, End: 2}}},
		},
	}}
	outIdx, _, completeIdx := bindPostgresGroupMetadata(idxGroups, binding, mapper)
	if !completeIdx || outIdx[0].Objects[0].SchemaToken != publicToken {
		t.Fatalf("expected pg index with valid parent table to bind schema %q and stay complete: complete=%v out=%+v", publicToken, completeIdx, outIdx)
	}
	badIdxGroups := []provider.AnonymizedGroup{{
		GroupID:                     "pg-orphan-index",
		RepresentativeAnonymizedSQL: "CREATE INDEX idx_001 ON tbl_001 (col_001)",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{
			{Kind: "index", Token: "idx_001"},
		},
	}}
	if outBad, _, ok := bindPostgresGroupMetadata(badIdxGroups, binding, mapper); ok || outBad[0].MetadataContextComplete {
		t.Fatalf("expected pg index without parent table to stay incomplete: ok=%v out=%+v", ok, outBad)
	}
}

func TestBindGroupMetadataWithReason(t *testing.T) {
	t.Run("mysql explicit out-of-scope schema", func(t *testing.T) {
		mapper := privacy.NewRunMapper()
		otherSchema, err := mapper.ScopedToken(privacy.KindSchema, privacy.ScopeCaseSensitive, "devops")
		if err != nil {
			t.Fatal(err)
		}
		tableToken, err := mapper.ScopedToken(privacy.KindTable, "", "deploy_record")
		if err != nil {
			t.Fatal(err)
		}
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "g-mysql-out-of-scope",
			RepresentativeAnonymizedSQL: "INSERT INTO devops.deploy_record (id) VALUES (?)",
			MetadataContextComplete:     true,
			Objects: []provider.AnonymizedObjectReference{{
				Kind: "table", Token: tableToken, SchemaToken: otherSchema,
			}},
		}}
		_, _, complete, reason := bindGroupMetadataWithReason(groups, bindingOf("demo"), mapper)
		if complete || reason != "sql_schema_out_of_scope" {
			t.Fatalf("got complete=%v reason=%q, want false/sql_schema_out_of_scope", complete, reason)
		}
	})

	t.Run("mysql missing database_name", func(t *testing.T) {
		mapper := privacy.NewRunMapper()
		tableToken, err := mapper.Token(privacy.KindTable, "deploy_record")
		if err != nil {
			t.Fatal(err)
		}
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "g-mysql-missing-db",
			RepresentativeAnonymizedSQL: "INSERT INTO deploy_record (id) VALUES (?)",
			MetadataContextComplete:     true,
			Objects:                     []provider.AnonymizedObjectReference{{Kind: "table", Token: tableToken}},
		}}
		binding := &stageSchemaBinding{defensible: false, unavailableReason: "datasource_database_required"}
		_, _, complete, reason := bindGroupMetadataWithReason(groups, binding, mapper)
		if complete || reason != "datasource_database_required" {
			t.Fatalf("got complete=%v reason=%q, want false/datasource_database_required", complete, reason)
		}
	})

	t.Run("postgres explicit out-of-scope schema", func(t *testing.T) {
		binding, mapper, tableToken, _, _ := testPostgresOrdinalBinding(t)
		otherSchema, err := mapper.Token(privacy.KindSchema, "external_schema")
		if err != nil {
			t.Fatal(err)
		}
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "pg-out-of-scope",
			RepresentativeAnonymizedSQL: "INSERT INTO external_schema.orders (id) VALUES (?)",
			MetadataContextComplete:     true,
			Objects: []provider.AnonymizedObjectReference{
				{Kind: "schema", Token: otherSchema, Relation: "reference"},
				{Kind: "table", Token: tableToken, SchemaToken: otherSchema, Relation: "target", StatementOrdinal: 1, StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 1}}},
			},
		}}
		_, _, complete, reason := bindGroupMetadataWithReason(groups, binding, mapper)
		if complete || reason != "sql_schema_out_of_scope" {
			t.Fatalf("got complete=%v reason=%q, want false/sql_schema_out_of_scope", complete, reason)
		}
	})

	t.Run("postgres search_path unavailable", func(t *testing.T) {
		mapper := privacy.NewRunMapper()
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "pg-no-path",
			RepresentativeAnonymizedSQL: "SELECT 1",
			MetadataContextComplete:     true,
		}}
		binding := &stageSchemaBinding{
			defensible:        false,
			unavailableReason: "postgres_search_path_unavailable",
		}
		_, _, complete, reason := bindGroupMetadataWithReason(groups, binding, mapper)
		if complete || reason != "postgres_search_path_unavailable" {
			t.Fatalf("got complete=%v reason=%q, want false/postgres_search_path_unavailable", complete, reason)
		}
	})

	t.Run("datasource database required with objectless sql", func(t *testing.T) {
		mapper := privacy.NewRunMapper()
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "mysql-no-db",
			RepresentativeAnonymizedSQL: "SELECT 1",
			MetadataContextComplete:     true,
		}}
		binding := &stageSchemaBinding{
			defensible:        false,
			unavailableReason: "datasource_database_required",
		}
		_, _, complete, reason := bindGroupMetadataWithReason(groups, binding, mapper)
		if complete || reason != "datasource_database_required" {
			t.Fatalf("got complete=%v reason=%q, want false/datasource_database_required", complete, reason)
		}
	})

	t.Run("unqualified statement with defensible empty-default binding", func(t *testing.T) {
		mapper := privacy.NewRunMapper()
		tableToken, err := mapper.Token(privacy.KindTable, "orders")
		if err != nil {
			t.Fatal(err)
		}
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "mysql-mixed-missing-default",
			RepresentativeAnonymizedSQL: "UPDATE orders SET status = :number",
			MetadataContextComplete:     true,
			Objects:                     []provider.AnonymizedObjectReference{{Kind: "table", Token: tableToken}},
		}}
		binding := &stageSchemaBinding{
			defensible:           true,
			schemas:              map[string]bool{"demo": true},
			defaultSchemaName:    "",
			defaultSchemaSet:     true,
			hasSQLDerivedSchemas: true,
		}
		_, _, complete, reason := bindGroupMetadataWithReason(groups, binding, mapper)
		if complete || reason != "datasource_database_required" {
			t.Fatalf("got complete=%v reason=%q, want false/datasource_database_required", complete, reason)
		}
	})

	t.Run("qualified statement with schema reference, alias, and unparented column passes with empty default", func(t *testing.T) {
		mapper := privacy.NewRunMapper()
		demoTok, err := mapper.ScopedToken(privacy.KindSchema, "", "demo")
		if err != nil {
			t.Fatal(err)
		}
		tblTok, err := mapper.ScopedToken(privacy.KindTable, "", "deploy_record")
		if err != nil {
			t.Fatal(err)
		}
		aliasTok, err := mapper.ScopedToken(privacy.KindAlias, "", "d")
		if err != nil {
			t.Fatal(err)
		}
		colTok, err := mapper.ScopedToken(privacy.KindColumn, "", "project_name")
		if err != nil {
			t.Fatal(err)
		}
		groups := []provider.AnonymizedGroup{{
			GroupID:                     "mysql-qualified-schema-ref",
			RepresentativeAnonymizedSQL: "SELECT col_1 FROM sch_1.tbl_1 AS al_1",
			MetadataContextComplete:     true,
			Objects: []provider.AnonymizedObjectReference{
				{Kind: "schema", Token: demoTok, Relation: "reference"},
				{Kind: "table", Token: tblTok, SchemaToken: demoTok, Relation: "read"},
				{Kind: "alias", Token: aliasTok, Relation: "reference"},
				{Kind: "column", Token: colTok, Relation: "read"},
			},
		}}
		binding := &stageSchemaBinding{
			defensible:           true,
			schemas:              map[string]bool{"demo": true},
			defaultSchemaName:    "",
			defaultSchemaSet:     true,
			hasSQLDerivedSchemas: true,
		}
		out, scope, complete, reason := bindGroupMetadataWithReason(groups, binding, mapper)
		if !complete || reason != "" || !scope.objects[demoTok][tblTok] || out[0].Objects[0].SchemaToken != demoTok {
			t.Fatalf("got complete=%v reason=%q scope=%+v obj0=%+v", complete, reason, scope, out[0].Objects[0])
		}
	})
}
