package application

import (
	"reflect"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func testPostgresPath() FrozenPostgresSearchPath {
	return FrozenPostgresSearchPath{
		Version:        postgresSearchPathSnapshotVersion,
		CurrentUser:    "yearning_exec",
		Configured:     `"$user", public`,
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
}

func testTableLookup(ordinal int32, token string, present map[string]string) postgresRelationLookup {
	oids := make(map[string]uint32, len(present))
	for schema := range present {
		oids[schema] = testPostgresOID(schema)
	}
	return postgresRelationLookup{
		Ordinal: ordinal, TableToken: token, TableName: token,
		PresentBySchema: present, OIDBySchema: oids, Complete: true,
	}
}

func testPostgresOID(schema string) uint32 {
	value := uint32(2166136261)
	for i := 0; i < len(schema); i++ {
		value ^= uint32(schema[i])
		value *= 16777619
	}
	if value == 0 {
		return 1
	}
	return value
}

func testStatement(ordinal int32, effects ...fingerprint.ValidityEffect) StatementValidityAtOrdinal {
	return StatementValidityAtOrdinal{
		Ordinal: ordinal,
		Facts: fingerprint.StatementValidity{
			Complete: true, DefinitionsComplete: true,
			Operation: fingerprint.ValidityOperationUpdate,
			Effects:   effects,
		},
	}
}

func TestFrozenPostgresSearchPathValidate(t *testing.T) {
	path := testPostgresPath()
	if err := path.Validate(); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	equivalentSpelling := path
	equivalentSpelling.Configured = `app, public`
	if !path.Equal(equivalentSpelling) {
		t.Fatal("equivalent current_setting spelling must not count as execution drift")
	}
	path.Effective = []string{"pg_catalog", "app", "pg_temp_3", "public"}
	if err := path.Validate(); err != nil {
		t.Fatalf("implicit catalog/temp path rejected: %v", err)
	}
	path.Effective = append(path.Effective, "unexpected_implicit")
	if err := path.Validate(); err == nil {
		t.Fatal("unknown implicit schema must fail closed")
	}
}

func TestFrozenPostgresSearchPathCallableLookupSafety(t *testing.T) {
	path := testPostgresPath()
	if !path.CallableLookupSafe() {
		t.Fatal("implicit leading pg_catalog must be callable-safe")
	}
	path.Configured = "app, pg_catalog, public"
	path.Explicit = []string{"app", "pg_catalog", "public"}
	path.Effective = []string{"app", "pg_catalog", "pg_temp_3", "public"}
	if err := path.Validate(); err != nil {
		t.Fatal(err)
	}
	if path.CallableLookupSafe() {
		t.Fatal("an explicit user schema before pg_catalog must fail closed")
	}
}

func TestPostgresBindingRejectsCallableShadowingPath(t *testing.T) {
	path := testPostgresPath()
	path.Configured = "app, pg_catalog, public"
	path.Explicit = []string{"app", "pg_catalog", "public"}
	path.Effective = []string{"app", "pg_catalog", "public"}
	token := "table_token"
	binding, complete := preparePostgresObjectBindings(
		path,
		[]StatementValidityAtOrdinal{testStatement(1)},
		[]postgresRelationLookup{testTableLookup(1, token, map[string]string{
			"app": "table",
		})},
		nil,
	)
	// Relation identity and callable identity are separate concerns: the
	// relation snapshot is still exact, but without catalog facts a path that
	// lists a user schema before pg_catalog cannot prove callable safety.
	if !complete || !binding.Complete {
		t.Fatalf("relation identity must stay exact: %+v", binding)
	}
	if binding.CallablesSafe {
		t.Fatalf("callable-shadowing path was accepted: %+v", binding)
	}
	if binding.CallablesChecked || len(binding.Callables) != 0 {
		t.Fatalf("callable facts must not be invented: %+v", binding)
	}
}

func TestPostgresBindingUsesFirstMatchingRelationInEffectivePath(t *testing.T) {
	token := "table_token"
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{testStatement(1)},
		[]postgresRelationLookup{testTableLookup(1, token, map[string]string{
			"app": "table", "public": "table",
		})},
		nil,
	)
	if !complete {
		t.Fatal("two present base relations should still have a complete first-match binding")
	}
	got := binding.Relations[postgresObjectBindingKey(1, token)]
	if !got.Present || got.Schema != "app" || !got.Supported {
		t.Fatalf("binding = %+v, want first app table", got)
	}
}

func TestPostgresBindingRejectsBaselineOIDChange(t *testing.T) {
	token := "table_token"
	first := testTableLookup(1, token, map[string]string{"app": "table"})
	second := testTableLookup(2, token, map[string]string{"app": "table"})
	second.OIDBySchema["app"]++
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{testStatement(1), testStatement(2)},
		[]postgresRelationLookup{first, second},
		nil,
	)
	if complete || binding.Complete {
		t.Fatal("same-name relation with changing OID was accepted as one baseline")
	}
}

func TestPostgresBindingDoesNotSkipNonTableRelation(t *testing.T) {
	token := "table_token"
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{testStatement(1)},
		[]postgresRelationLookup{testTableLookup(1, token, map[string]string{
			"app": "view", "public": "table",
		})},
		nil,
	)
	got := binding.Relations[postgresObjectBindingKey(1, token)]
	if got.Schema != "app" || got.Supported || complete {
		t.Fatalf("view shadowing must remain partial, got %+v complete=%v", got, complete)
	}
}

func TestPostgresBindingUsesCreationSchemaAndPriorCreate(t *testing.T) {
	token := "table_token"
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
	})
	update := testStatement(2)
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{create, update},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"public": "table"}),
			testTableLookup(2, token, map[string]string{"public": "table"}),
		},
		nil,
	)
	if !complete {
		t.Fatal("proven create then update should be complete")
	}
	for _, ordinal := range []int32{1, 2} {
		got := binding.Relations[postgresObjectBindingKey(ordinal, token)]
		if got.Schema != "app" || !got.Supported {
			t.Fatalf("ordinal %d binding = %+v, want app table identity", ordinal, got)
		}
		if wantPresent := ordinal == 2; got.Present != wantPresent {
			t.Fatalf("ordinal %d present = %v, want %v", ordinal, got.Present, wantPresent)
		}
		if ordinal == 2 &&
			(got.OID != 0 || got.CreatedByOrdinal != 1) {
			t.Fatalf("ordinal 2 created identity = %+v, want provenance 1", got)
		}
	}
}

func TestPostgresBindingAppliesPriorDropBeforeLaterLookup(t *testing.T) {
	token := "table_token"
	drop := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectDrop,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
	})
	update := testStatement(2)
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{drop, update},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"app": "table", "public": "table"}),
			testTableLookup(2, token, map[string]string{"app": "table", "public": "table"}),
		},
		nil,
	)
	if !complete {
		t.Fatal("proven drop then later lookup should be complete")
	}
	if got := binding.Relations[postgresObjectBindingKey(1, token)]; got.Schema != "app" {
		t.Fatalf("DROP target = %+v, want pre-effect app", got)
	}
	if got := binding.Relations[postgresObjectBindingKey(2, token)]; got.Schema != "public" {
		t.Fatalf("post-DROP lookup = %+v, want public shadow revealed", got)
	}
}

func TestPostgresBindingUnqualifiedDropUsesLookupOrder(t *testing.T) {
	token := "table_token"
	drop := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectDrop,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
	})
	update := testStatement(2)
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{drop, update},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"public": "table"}),
			testTableLookup(2, token, map[string]string{"public": "table"}),
		},
		nil,
	)
	if !complete {
		t.Fatalf("unqualified DROP through later path member was incomplete: %+v", binding)
	}
	if got := binding.Relations[postgresObjectBindingKey(1, token)]; got.Schema != "public" {
		t.Fatalf("DROP target = %+v, want first matching public table", got)
	}
	if got := binding.Relations[postgresObjectBindingKey(2, token)]; got.Present ||
		got.Schema != "app" {
		t.Fatalf("post-DROP absent binding = %+v", got)
	}
}

func TestPostgresBindingCreatePreservesLaterSameName(t *testing.T) {
	token := "table_token"
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
	})
	update := testStatement(2)
	drop := testStatement(3, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectDrop,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
	})
	updateAgain := testStatement(4)
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{create, update, drop, updateAgain},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"public": "table"}),
			testTableLookup(2, token, map[string]string{"public": "table"}),
			testTableLookup(3, token, map[string]string{"public": "table"}),
			testTableLookup(4, token, map[string]string{"public": "table"}),
		},
		nil,
	)
	if !complete {
		t.Fatal("create/drop at creation schema must preserve and reveal later schema state")
	}
	checks := map[int32]string{1: "app", 2: "app", 3: "app", 4: "public"}
	for ordinal, want := range checks {
		got := binding.Relations[postgresObjectBindingKey(ordinal, token)]
		if !got.Supported || got.Schema != want {
			t.Fatalf("ordinal %d binding = %+v, want table identity in %s", ordinal, got, want)
		}
		if wantPresent := ordinal != 1; got.Present != wantPresent {
			t.Fatalf("ordinal %d present = %v, want %v", ordinal, got.Present, wantPresent)
		}
	}
}

func TestPostgresBindingCreateCannotPromoteExistingView(t *testing.T) {
	token := "table_token"
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
		Condition: fingerprint.ValidityConditionIfNotExists,
	})
	update := testStatement(2)
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{create, update},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"app": "view"}),
			testTableLookup(2, token, map[string]string{"app": "view"}),
		},
		nil,
	)
	if complete || binding.Complete {
		t.Fatal("an existing view in the creation schema cannot prove table creation")
	}
	for _, ordinal := range []int32{1, 2} {
		got := binding.Relations[postgresObjectBindingKey(ordinal, token)]
		if got.Schema != "app" || !got.Present || got.Supported {
			t.Fatalf("ordinal %d binding = %+v, want unsupported app view", ordinal, got)
		}
	}
}

func TestPostgresBindingConditionalCreateExistingTableIsNoop(t *testing.T) {
	token := "table_token"
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
		Condition: fingerprint.ValidityConditionIfNotExists,
	})
	update := testStatement(2)
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{create, update},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"app": "table"}),
			testTableLookup(2, token, map[string]string{"app": "table"}),
		},
		nil,
	)
	if !complete {
		t.Fatal("conditional create against an existing table is a proven no-op")
	}
	for _, ordinal := range []int32{1, 2} {
		got := binding.Relations[postgresObjectBindingKey(ordinal, token)]
		if got.Schema != "app" || !got.Present || !got.Supported {
			t.Fatalf("ordinal %d binding = %+v, want existing app table", ordinal, got)
		}
	}
}

func TestPostgresBindingExplicitEffectsShareOriginalRelationIdentity(t *testing.T) {
	createToken := "create_token"
	updateToken := "update_token"
	dropToken := "drop_token"
	afterToken := "after_token"
	schemaToken := "schema_token"
	createObject := fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectTable, Token: createToken, SchemaToken: schemaToken,
	}
	dropObject := fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectTable, Token: dropToken, SchemaToken: schemaToken,
	}
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate, Object: createObject,
	})
	drop := testStatement(3, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectDrop, Object: dropObject,
	})
	originals := map[string]string{
		createToken: "shared_relation", updateToken: "shared_relation",
		dropToken: "shared_relation", afterToken: "shared_relation",
		schemaToken: "app",
	}
	originalName := func(token string) (string, bool) {
		name, ok := originals[token]
		return name, ok
	}
	lookups := []postgresRelationLookup{
		testTableLookup(1, createToken, map[string]string{"public": "table"}),
		testTableLookup(2, updateToken, map[string]string{"public": "table"}),
		testTableLookup(3, dropToken, map[string]string{"public": "table"}),
		testTableLookup(4, afterToken, map[string]string{"public": "table"}),
	}
	for i := range lookups {
		lookups[i].TableName = "shared_relation"
	}
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{create, testStatement(2), drop, testStatement(4)},
		lookups, originalName,
	)
	if !complete {
		t.Fatal("explicit create/drop effects must participate in later unqualified lookup")
	}
	checks := map[int32]string{1: "app", 2: "app", 3: "app", 4: "public"}
	tokens := map[int32]string{
		1: createToken, 2: updateToken, 3: dropToken, 4: afterToken,
	}
	for ordinal, want := range checks {
		got := binding.Relations[postgresObjectBindingKey(ordinal, tokens[ordinal])]
		if got.Schema != want || !got.Supported {
			t.Fatalf("ordinal %d binding = %+v, want %s table identity", ordinal, got, want)
		}
		if wantPresent := ordinal != 1; got.Present != wantPresent {
			t.Fatalf("ordinal %d present = %v, want %v", ordinal, got.Present, wantPresent)
		}
	}
}

func TestPostgresBindingRenameAndTemporaryRelationsStayPartial(t *testing.T) {
	token := "table_token"
	rename := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectRename,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
		Replacement: &fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: "renamed_token",
		},
	})
	path := testPostgresPath()
	path.Effective = []string{"pg_catalog", "pg_temp_3", "app", "public"}
	binding, complete := preparePostgresObjectBindings(
		path,
		[]StatementValidityAtOrdinal{rename},
		[]postgresRelationLookup{testTableLookup(1, token, map[string]string{"app": "table"})},
		nil,
	)
	if complete || binding.Complete {
		t.Fatal("rename and temporary resolved path must remain partial")
	}
}

func TestPostgresStatementPathUnsafe(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"create temp table scratch (id bigint)":                        true,
		"create global temporary table scratch (id bigint)":            true,
		"select * into temp scratch from public.source":                true,
		"select pg_catalog.set_config('search_path', 'public', false)": true,
		"set search_path to public":                                    true,
		"set session authorization other_role":                         true,
		"reset all":                                                    true,
		"discard temp":                                                 true,
		"create unlogged table durable (id bigint)":                    false,
		"create table durable (id bigint)":                             false,
		"select 'create temp table text_only' from public.source":      false,
		"update public.source set value = 1":                           false,
		"set statement_timeout = '5s'":                                 false,
		"select * from public.source":                                  false,
	}
	for statement, want := range tests {
		if got := postgresStatementPathUnsafe(statement); got != want {
			t.Fatalf("postgresStatementPathUnsafe(%q) = %v, want %v",
				statement, got, want)
		}
	}
	if !postgresStatementPathUnsafe("select 'unterminated") {
		t.Fatal("malformed statement must remain partial")
	}
}

func TestPostgresBindingUnsafePathStatementStaysPartial(t *testing.T) {
	t.Parallel()
	token := "table_token"
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
		},
	})
	binding, complete := preparePostgresObjectBindings(
		testPostgresPath(),
		[]StatementValidityAtOrdinal{create},
		[]postgresRelationLookup{
			testTableLookup(1, token, map[string]string{"app": "table"}),
		},
		nil,
		map[int32]bool{1: true},
	)
	if complete || binding.Complete {
		t.Fatal("session-local path effect must remain partial")
	}
	got := binding.Relations[postgresObjectBindingKey(1, token)]
	if got.Supported {
		t.Fatalf("unsafe object binding = %+v, want unsupported", got)
	}
}

func TestStagePostgresSchemasExcludeImplicitLookupOnlyMembers(t *testing.T) {
	binding := &stageSchemaBinding{postgres: &frozenPostgresPathBinding{
		Version: postgresObjectResolutionVersion,
		Path: FrozenPostgresSearchPath{
			Explicit:  []string{"app", "public"},
			Effective: []string{"pg_catalog", "app", "pg_temp_3", "public"},
		},
		Complete:      true,
		CallablesSafe: true,
	}}
	if got, want := binding.allPostgresSchemas(), []string{"app", "public"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SET candidate schemas = %v, want %v", got, want)
	}
}
