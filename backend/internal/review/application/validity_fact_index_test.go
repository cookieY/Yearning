package application

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

func validityIndexStatement(
	ordinal int32,
	requirements []fingerprint.ValidityRequirement,
	effects []fingerprint.ValidityEffect,
) StatementValidityAtOrdinal {
	return StatementValidityAtOrdinal{
		Ordinal: ordinal,
		Facts: fingerprint.StatementValidity{
			Complete:            true,
			DefinitionsComplete: true,
			Operation:           fingerprint.ValidityOperationAlter,
			Requirements:        requirements,
			Effects:             effects,
		},
	}
}

func validityIndexTable(schema, table string) fingerprint.ValidityObject {
	return fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectTable, Token: table, SchemaToken: schema,
	}
}

func validityIndexColumn(schema, table, column string) fingerprint.ValidityObject {
	return fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectColumn, Token: column,
		SchemaToken: schema, ParentToken: table,
	}
}

func validityIndexObjectFact(object fingerprint.ValidityObject) provider.NormalizedEvidenceRef {
	return provider.NormalizedEvidenceRef{
		EvidenceID: "evidence-" + object.Token,
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema":              object.SchemaToken,
			"table":               object.Token,
			"existence":           "present",
			"columns":             []any{},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true,
			"truncated":           false,
		},
	}
}

func requireValidityObject(
	object fingerprint.ValidityObject, state fingerprint.ValidityRequirementState,
) fingerprint.ValidityRequirement {
	return fingerprint.ValidityRequirement{
		Object: object, State: state, Condition: fingerprint.ValidityConditionNone,
	}
}

func TestValidityFactIndexCompleteTableExistence(t *testing.T) {
	table := validityIndexTable("schema_1", "table_1")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(table, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	for _, state := range []string{"present", "absent"} {
		t.Run(state, func(t *testing.T) {
			fact := provider.NormalizedEvidenceRef{
				EvidenceID: "evidence-" + state,
				FactKind:   tool.NameSchemaMetadata,
				Fact: map[string]any{
					"schema": "schema_1", "table": "table_1",
					"existence": state, "columns": []any{},
					"fact_schema_version": tool.FactSchemaVersion,
					"metadata_complete":   true, "truncated": false,
				},
			}
			index := validityFactIndex(program, []provider.NormalizedEvidenceRef{fact})
			got, ok := index[table]
			want := ValidityFactPresent
			if state == "absent" {
				want = ValidityFactAbsent
			}
			if !ok || got.State != want || !got.Complete ||
				len(got.EvidenceIDs) != 1 || got.EvidenceIDs[0] != fact.EvidenceID {
				t.Fatalf("table fact = %+v, present=%v, want %s with evidence %q",
					got, ok, want, fact.EvidenceID)
			}
		})
	}
}

func TestValidityFactIndexRejectsUnsupportedFactSchemaVersions(t *testing.T) {
	table := validityIndexTable("schema_1", "table_1")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(table, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}
	for _, tc := range []struct {
		name    string
		version any
		present bool
	}{
		{name: "missing"},
		{name: "version 1", version: 1},
		{name: "future version", version: tool.FactSchemaVersion + 1},
		{name: "non integral version", version: 2.5},
		{name: "string version", version: "2"},
		{name: "json version", version: float64(tool.FactSchemaVersion), present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := map[string]any{
				"schema": "schema_1", "table": "table_1",
				"existence": "present", "columns": []any{},
				"metadata_complete": true, "truncated": false,
			}
			if tc.version != nil {
				fact["fact_schema_version"] = tc.version
			}
			ref := provider.NormalizedEvidenceRef{
				EvidenceID: "evidence-version", FactKind: tool.NameSchemaMetadata, Fact: fact,
			}
			got, ok := validityFactIndex(program, []provider.NormalizedEvidenceRef{ref})[table]
			if !ok {
				t.Fatal("scoped evidence did not produce a validity fact")
			}
			if tc.present {
				if got.State != ValidityFactPresent || !got.Complete {
					t.Fatalf("supported version fact = %+v", got)
				}
				return
			}
			if got.State != ValidityFactUnknown || got.Complete {
				t.Fatalf("unsupported version proved %s: %+v", got.State, got)
			}
			if len(got.EvidenceIDs) != 1 || got.EvidenceIDs[0] != ref.EvidenceID {
				t.Fatalf("unknown fact evidence = %v", got.EvidenceIDs)
			}
		})
	}
}

func TestValidityFactIndexMissingEvidenceRemainsUnknown(t *testing.T) {
	table := validityIndexTable("schema_1", "table_1")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(table, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	index := validityFactIndex(program, nil)
	if fact, ok := index[table]; ok {
		t.Fatalf("missing evidence fabricated a fact: %+v", fact)
	}
}

func TestValidityFactIndexConflictingEvidenceIsUnknown(t *testing.T) {
	table := validityIndexTable("schema_1", "table_1")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(table, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}
	present := validityIndexObjectFact(fingerprint.ValidityObject{
		Kind: table.Kind, Token: table.Token, SchemaToken: table.SchemaToken,
	})
	present.EvidenceID = "evidence-present"
	present.Fact["existence"] = "present"
	absent := validityIndexObjectFact(fingerprint.ValidityObject{
		Kind: table.Kind, Token: table.Token, SchemaToken: table.SchemaToken,
	})
	absent.EvidenceID = "evidence-absent"
	absent.Fact["existence"] = "absent"

	index := validityFactIndex(program, []provider.NormalizedEvidenceRef{absent, present})
	got, ok := index[table]
	if !ok || got.State != ValidityFactUnknown || got.Complete {
		t.Fatalf("conflicting evidence = %+v, present=%v, want incomplete unknown", got, ok)
	}
	if len(got.EvidenceIDs) != 2 ||
		got.EvidenceIDs[0] != "evidence-absent" ||
		got.EvidenceIDs[1] != "evidence-present" {
		t.Fatalf("conflict evidence order = %v", got.EvidenceIDs)
	}
}

func TestValidityFactIndexIncompleteFactsDoNotProveMembership(t *testing.T) {
	schema := "schema_1"
	table := "table_1"
	column := "column_1"
	indexName := "index_1"
	columnObject := validityIndexColumn(schema, table, column)
	indexObject := fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectIndex, Token: indexName,
		SchemaToken: schema, ParentToken: table,
	}
	program := ValidityProgram{
		Complete: false,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(columnObject, fingerprint.ValidityRequirementPresent),
				requireValidityObject(indexObject, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}
	evidence := []provider.NormalizedEvidenceRef{
		{
			EvidenceID: "truncated-columns",
			FactKind:   tool.NameSchemaMetadata,
			Fact: map[string]any{
				"schema": schema, "table": table, "existence": "present",
				"columns": []any{map[string]any{
					"column_name": column, "type_class": "other", "type_status": "unknown",
				}},
				"fact_schema_version": tool.FactSchemaVersion,
				"metadata_complete":   false, "truncated": true,
			},
		},
		{
			EvidenceID: "truncated-indexes",
			FactKind:   tool.NameIndexMetadata,
			Fact: map[string]any{
				"schema": schema, "table": table, "existence": "present",
				"indexes": []any{map[string]any{
					"index_name": indexName, "columns": []any{column},
				}},
				"fact_schema_version": tool.FactSchemaVersion,
				"metadata_complete":   false, "truncated": true,
			},
		},
	}

	index := validityFactIndex(program, evidence)
	for _, object := range []fingerprint.ValidityObject{columnObject, indexObject} {
		got, ok := index[object]
		if !ok {
			continue
		}
		if got.State != ValidityFactUnknown || got.Complete {
			t.Fatalf("%+v was proved from incomplete evidence: %+v", object, got)
		}
	}
}

func TestValidityFactIndexRequiresExactObjectIdentity(t *testing.T) {
	column := validityIndexColumn("schema_1", "table_1", "column_1")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(column, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}
	wrongScope := provider.NormalizedEvidenceRef{
		EvidenceID: "wrong-scope",
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema": "schema_2", "table": "table_1", "existence": "present",
			"columns":             []any{map[string]any{"column_name": "column_1"}},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true, "truncated": false,
		},
	}
	if fact, ok := validityFactIndex(program, []provider.NormalizedEvidenceRef{wrongScope})[column]; ok {
		t.Fatalf("wrong schema/table scope produced a column fact: %+v", fact)
	}

	correctScope := wrongScope
	correctScope.EvidenceID = "correct-scope"
	correctScope.Fact = map[string]any{
		"schema": "schema_1", "table": "table_1", "existence": "present",
		"columns":             []any{map[string]any{"column_name": "column_1"}},
		"fact_schema_version": tool.FactSchemaVersion,
		"metadata_complete":   true, "truncated": false,
	}
	got := validityFactIndex(program, []provider.NormalizedEvidenceRef{correctScope})[column]
	if got.State != ValidityFactPresent || !got.Complete ||
		len(got.EvidenceIDs) != 1 || got.EvidenceIDs[0] != "correct-scope" {
		t.Fatalf("exact column fact = %+v", got)
	}
}

func TestValidityFactIndexIndexFactsProvideKeyAndIncludedColumns(t *testing.T) {
	schema := "schema_1"
	table := "table_1"
	keyColumn := validityIndexColumn(schema, table, "column_1")
	includeColumn := validityIndexColumn(schema, table, "column_2")
	indexObject := fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectIndex, Token: "index_1",
		SchemaToken: schema, ParentToken: table,
	}
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(indexObject, fingerprint.ValidityRequirementPresent),
				requireValidityObject(keyColumn, fingerprint.ValidityRequirementPresent),
				requireValidityObject(includeColumn, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}
	indexFact := provider.NormalizedEvidenceRef{
		EvidenceID: "index-profile",
		FactKind:   tool.NameIndexMetadata,
		Fact: map[string]any{
			"schema": schema, "table": table, "existence": "present",
			"indexes": []any{map[string]any{
				"index_name":          indexObject.Token,
				"columns":             []any{keyColumn.Token},
				"included_columns":    []any{includeColumn.Token},
				"expression_count":    0,
				"has_expression":      false,
				"prefix_column_count": 0,
				"has_prefix":          false,
			}},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true, "truncated": false,
		},
	}
	otherTable := provider.NormalizedEvidenceRef{
		EvidenceID: "other-table",
		FactKind:   tool.NameIndexMetadata,
		Fact: map[string]any{
			"schema": schema, "table": "table_2", "existence": "present",
			"indexes": []any{map[string]any{
				"index_name": indexObject.Token, "columns": []any{includeColumn.Token},
			}},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true, "truncated": false,
		},
	}

	index := validityFactIndex(program, []provider.NormalizedEvidenceRef{otherTable, indexFact})
	for _, object := range []fingerprint.ValidityObject{indexObject, keyColumn, includeColumn} {
		got := index[object]
		if got.State != ValidityFactPresent || !got.Complete ||
			len(got.EvidenceIDs) != 1 || got.EvidenceIDs[0] != indexFact.EvidenceID {
			t.Fatalf("%+v index-derived fact = %+v", object, got)
		}
	}
}

func TestBindValidityProgramExplicitPhysicalSchema(t *testing.T) {
	mapper := privacy.NewRunMapper()
	physical, _ := mapper.Token(privacy.KindSchema, "prod")
	table, _ := mapper.Token(privacy.KindTable, "orders")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(validityIndexTable(physical, table),
					fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	bound, ok := bindValidityProgram(program, bindingOf("prod"), mapper)
	if !ok || !bound.Complete {
		t.Fatalf("explicit physical binding failed: ok=%v program=%+v", ok, bound)
	}
	if got := bound.Statements[0].Facts.Requirements[0].Object.SchemaToken; got != physical {
		t.Fatalf("explicit physical schema changed to %q", got)
	}
}

func TestBindValidityProgramUnqualifiedUsesCanonicalPhysicalDefault(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logical, _ := mapper.Token(privacy.KindSchema, "app")
	table, _ := mapper.Token(privacy.KindTable, "orders")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(validityIndexTable("", table),
					fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	bound, ok := bindValidityProgram(program, bindingOf("prod"), mapper)
	if !ok || !bound.Complete {
		t.Fatalf("unqualified binding failed: ok=%v program=%+v", ok, bound)
	}
	got := bound.Statements[0].Facts.Requirements[0].Object.SchemaToken
	kind, original, found := mapper.Original(got)
	if !found || kind != privacy.KindSchema || original != "prod" || got == logical {
		t.Fatalf("unqualified schema token = %q (%q/%q/%v), want anonymous prod token",
			got, kind, original, found)
	}
	if program.Statements[0].Facts.Requirements[0].Object.SchemaToken != "" {
		t.Fatal("bindValidityProgram mutated its input")
	}
}

func TestBindValidityProgramOutOfScopeExplicitSchemaFailsClosed(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logical, _ := mapper.Token(privacy.KindSchema, "app")
	table, _ := mapper.Token(privacy.KindTable, "orders")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(validityIndexTable(logical, table),
					fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	bound, ok := bindValidityProgram(program, bindingOf("prod"), mapper)
	if ok || bound.Complete || bound.Statements[0].Facts.Complete {
		t.Fatalf("explicit out-of-scope schema was accepted: ok=%v program=%+v", ok, bound)
	}
	evidence := []provider.NormalizedEvidenceRef{{
		EvidenceID: "logical-fact",
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema": logical, "table": table, "existence": "present",
			"columns": []any{}, "fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete": true, "truncated": false,
		},
	}}
	if fact, found := validityFactIndex(bound, evidence)[validityIndexTable(logical, table)]; found {
		t.Fatalf("invalid explicit logical schema produced evidence: %+v", fact)
	}
}

func TestBindValidityProgramCanonicalizesOrderedObjectIdentity(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logical, _ := mapper.Token(privacy.KindSchema, "app")
	table, _ := mapper.Token(privacy.KindTable, "orders")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(
				1,
				[]fingerprint.ValidityRequirement{
					requireValidityObject(validityIndexTable("", table),
						fingerprint.ValidityRequirementAbsent),
				},
				[]fingerprint.ValidityEffect{{
					Action:    fingerprint.ValidityEffectCreate,
					Object:    validityIndexTable("", table),
					Condition: fingerprint.ValidityConditionNone,
				}},
			),
			validityIndexStatement(
				2,
				[]fingerprint.ValidityRequirement{
					requireValidityObject(validityIndexTable("", table),
						fingerprint.ValidityRequirementPresent),
				},
				[]fingerprint.ValidityEffect{{
					Action:    fingerprint.ValidityEffectDrop,
					Object:    validityIndexTable("", table),
					Condition: fingerprint.ValidityConditionNone,
				}},
			),
		},
	}
	// The second SQL statement explicitly names prod.orders, represented by
	// the physical schema token. Both statements still address one table.
	physical, _ := mapper.Token(privacy.KindSchema, "prod")
	program.Statements[1].Facts.Requirements[0].Object.SchemaToken = physical
	program.Statements[1].Facts.Effects[0].Object.SchemaToken = physical

	bound, ok := bindValidityProgram(
		program, bindingOf("prod"), mapper,
	)
	if !ok || !bound.Complete {
		t.Fatalf("canonical binding failed: ok=%v program=%+v", ok, bound)
	}
	first := bound.Statements[0].Facts.Requirements[0].Object
	second := bound.Statements[1].Facts.Requirements[0].Object
	if first.SchemaToken == "" || first.SchemaToken != second.SchemaToken ||
		first.SchemaToken == logical {
		t.Fatalf("ordered object identity diverged: first=%+v second=%+v logical=%q",
			first, second, logical)
	}

	refs := []provider.NormalizedEvidenceRef{{
		EvidenceID: "initial-absent",
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema": physical, "table": table, "existence": "absent",
			"columns": []any{}, "fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete": true, "truncated": false,
		},
	}}
	runtimeRefs := bindValidityEvidence(
		refs, bindingOf("prod"), mapper,
	)
	facts := validityFactIndex(bound, runtimeRefs)
	result, err := NewPreconditionEvaluator().Evaluate(context.Background(), ValidityInput{
		Engine: "postgresql", Program: bound, Facts: facts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ValidityValid || len(result.Violations) != 0 {
		t.Fatalf("create then explicitly qualified drop = %+v", result)
	}
}

func TestBindValidityEvidenceCopiesAndCanonicalizesRuntimeFacts(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logical, _ := mapper.Token(privacy.KindSchema, "prod")
	table, _ := mapper.Token(privacy.KindTable, "orders")
	column, _ := mapper.Token(privacy.KindColumn, "id")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(
					validityIndexTable("", table), fingerprint.ValidityRequirementPresent,
				),
			}, nil),
		},
	}
	binding := bindingOf("prod")
	bound, ok := bindValidityProgram(program, binding, mapper)
	if !ok {
		t.Fatal("program binding failed")
	}
	canonical := bound.Statements[0].Facts.Requirements[0].Object.SchemaToken
	refs := []provider.NormalizedEvidenceRef{{
		EvidenceID: "profile",
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema": logical, "table": table, "existence": "present",
			"columns": []any{map[string]any{
				"column_name": column, "type_status": "observed",
			}},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true, "truncated": false,
		},
	}}

	runtimeRefs := bindValidityEvidence(refs, binding, mapper)
	if len(runtimeRefs) != 1 || runtimeRefs[0].Fact["schema"] != canonical {
		t.Fatalf("runtime schema = %+v, want canonical %q", runtimeRefs, canonical)
	}
	if refs[0].Fact["schema"] != logical {
		t.Fatalf("Provider evidence was mutated: %+v", refs[0].Fact)
	}
	runtimeRefs[0].Fact["columns"].([]any)[0].(map[string]any)["column_name"] = "changed"
	if refs[0].Fact["columns"].([]any)[0].(map[string]any)["column_name"] != column {
		t.Fatal("runtime evidence shares nested maps or slices with Provider evidence")
	}
}

func TestBindValidityProgramRejectsQuotedUnquotedSchemaAmbiguity(t *testing.T) {
	mapper := privacy.NewRunMapper()
	_, _ = mapper.Token(privacy.KindSchema, "app")
	folded, _ := mapper.ScopedToken(privacy.KindSchema, "", "prod")
	exact, _ := mapper.ScopedToken(
		privacy.KindSchema, privacy.ScopeCaseSensitive, "prod",
	)
	if folded == exact {
		t.Fatal("test requires distinct quoted and unquoted schema tokens")
	}
	table, _ := mapper.Token(privacy.KindTable, "orders")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(validityIndexTable("", table),
					fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	bound, ok := bindValidityProgram(
		program, bindingOf("prod"), mapper,
	)
	if ok || bound.Complete || bound.Statements[0].Facts.Complete {
		t.Fatalf("ambiguous quoted/unquoted schema was accepted: ok=%v program=%+v", ok, bound)
	}
}

func TestBindValidityProgramRejectsQuotedUnquotedTableAmbiguity(t *testing.T) {
	mapper := privacy.NewRunMapper()
	_, _ = mapper.Token(privacy.KindSchema, "prod")
	folded, _ := mapper.Token(privacy.KindTable, "orders")
	exact, _ := mapper.ScopedToken(
		privacy.KindTable, privacy.ScopeCaseSensitive, "orders",
	)
	if folded == exact {
		t.Fatal("test requires distinct quoted and unquoted table tokens")
	}
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(validityIndexTable("", folded),
					fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}

	bound, ok := bindValidityProgram(
		program, bindingOf("prod"), mapper,
	)
	if ok || bound.Complete || bound.Statements[0].Facts.Complete {
		t.Fatalf("ambiguous quoted/unquoted table was accepted: ok=%v program=%+v", ok, bound)
	}
}

func TestValidityFactIndexOutputIsAnonymous(t *testing.T) {
	mapper := privacy.NewRunMapper()
	schema, _ := mapper.Token(privacy.KindSchema, "tenant_raw")
	table, _ := mapper.Token(privacy.KindTable, "orders_raw")
	column, _ := mapper.Token(privacy.KindColumn, "secret_raw")
	tableObject := validityIndexTable(schema, table)
	columnObject := validityIndexColumn(schema, table, column)
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(tableObject, fingerprint.ValidityRequirementPresent),
				requireValidityObject(columnObject, fingerprint.ValidityRequirementPresent),
			}, nil),
		},
	}
	evidence := []provider.NormalizedEvidenceRef{{
		EvidenceID: "anonymous-evidence",
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema": schema, "table": table, "existence": "present",
			"columns":             []any{map[string]any{"column_name": column}},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true, "truncated": false,
		},
	}}

	out := fmt.Sprintf("%#v", validityFactIndex(program, evidence))
	for _, raw := range []string{"tenant_raw", "orders_raw", "secret_raw"} {
		if strings.Contains(out, raw) {
			t.Fatalf("validity fact index retains raw identifier %q: %s", raw, out)
		}
	}
}

func TestValidityIdentityBindingsDoNotRetainRawNames(t *testing.T) {
	mapper := privacy.NewRunMapper()
	allowed, _ := mapper.Token(privacy.KindSchema, "tenant_physical_raw")
	table, _ := mapper.Token(privacy.KindTable, "orders_raw")
	column, _ := mapper.Token(privacy.KindColumn, "secret_raw")
	binding := bindingOf("tenant_physical_raw")
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			validityIndexStatement(1, []fingerprint.ValidityRequirement{
				requireValidityObject(
					validityIndexTable("", table), fingerprint.ValidityRequirementPresent,
				),
				requireValidityObject(
					validityIndexColumn("", table, column),
					fingerprint.ValidityRequirementPresent,
				),
			}, nil),
		},
	}
	bound, ok := bindValidityProgram(program, binding, mapper)
	if !ok {
		t.Fatal("program binding failed")
	}
	refs := []provider.NormalizedEvidenceRef{{
		EvidenceID: "anonymous-evidence",
		FactKind:   tool.NameSchemaMetadata,
		Fact: map[string]any{
			"schema": allowed, "table": table, "existence": "present",
			"columns": []any{map[string]any{
				"column_name": column, "type_status": "observed",
			}},
			"fact_schema_version": tool.FactSchemaVersion,
			"metadata_complete":   true, "truncated": false,
		},
	}}
	runtimeRefs := bindValidityEvidence(refs, binding, mapper)
	index := validityFactIndex(bound, runtimeRefs)
	out := fmt.Sprintf("%#v %#v %#v", bound, runtimeRefs, index)
	for _, raw := range []string{
		"tenant_alias_raw", "tenant_physical_raw", "orders_raw", "secret_raw",
	} {
		if strings.Contains(out, raw) {
			t.Fatalf("runtime validity identity retains raw name %q: %s", raw, out)
		}
	}
}
