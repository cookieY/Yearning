package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

func testPostgresOrdinalBinding(
	t *testing.T,
) (*stageSchemaBinding, *privacy.RunMapper, string, string, string) {
	t.Helper()
	mapper := privacy.NewRunMapper()
	tableToken, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	appToken, err := mapper.Token(privacy.KindSchema, "app")
	if err != nil {
		t.Fatal(err)
	}
	publicToken, err := mapper.Token(privacy.KindSchema, "public")
	if err != nil {
		t.Fatal(err)
	}
	path := testPostgresPath()
	binding := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"app": true, "public": true},
		postgres: &frozenPostgresPathBinding{
			Version:          postgresObjectResolutionVersion,
			Path:             path,
			Complete:         true,
			CallablesSafe:    true,
			CallablesChecked: true,
			Baseline: map[string]frozenPostgresBaselineRelation{
				"orders": {
					PresentBySchema: map[string]string{"public": "table"},
					OIDBySchema:     map[string]uint32{"public": 201},
				},
			},
			Relations: map[string]frozenPostgresObjectBinding{
				postgresObjectBindingKey(1, tableToken): {
					TableName: "orders", Schema: "app", Kind: "table", OID: 101,
					Present: true, Supported: true,
				},
				postgresObjectBindingKey(2, tableToken): {
					TableName: "orders", Schema: "public", Kind: "table", OID: 201,
					Present: true, Supported: true,
				},
			},
		},
	}
	return binding, mapper, tableToken, appToken, publicToken
}

func TestFrozenPostgresBindingEncryptedContextRoundTrip(t *testing.T) {
	crypter := newSealTestCrypter(t)
	binding, _, _, _, _ := testPostgresOrdinalBinding(t)
	const runID = "00000000-0000-4000-8000-000000000301"
	const stagePosition int32 = 1

	value, err := withFrozenPostgresBinding(
		map[string]any{"accepted_summary": "review complete"}, *binding.postgres,
	)
	if err != nil {
		t.Fatal(err)
	}
	env, err := sealStageReviewContext(crypter, runID, stagePosition, value)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := frozenPostgresBindingFromContext(
		crypter, runID, stagePosition, env,
	)
	if !ok || !reflect.DeepEqual(loaded, binding.postgres) {
		t.Fatalf("loaded binding = %+v, want exact round trip", loaded)
	}
	if _, ok := frozenPostgresBindingFromContext(
		crypter, runID, stagePosition+1, env,
	); ok {
		t.Fatal("binding must be authenticated against its stage position")
	}

	tampered := env
	tampered.Ciphertext = append([]byte(nil), env.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, ok := frozenPostgresBindingFromContext(
		crypter, runID, stagePosition, tampered,
	); ok {
		t.Fatal("tampered stage context must fail closed")
	}
}

func TestFrozenPostgresBindingRejectsMismatchedPathSnapshot(t *testing.T) {
	crypter := newSealTestCrypter(t)
	binding, _, _, _, _ := testPostgresOrdinalBinding(t)
	const runID = "00000000-0000-4000-8000-000000000302"
	const stagePosition int32 = 2

	value, err := withFrozenPostgresBinding(nil, *binding.postgres)
	if err != nil {
		t.Fatal(err)
	}
	mismatched := binding.postgres.Path
	mismatched.CurrentUser = "different_execution_role"
	raw, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	value[stagePostgresSearchPathKey] = json.RawMessage(raw)
	env, err := sealStageReviewContext(crypter, runID, stagePosition, value)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := frozenPostgresBindingFromContext(
		crypter, runID, stagePosition, env,
	); ok {
		t.Fatal("top-level path and object binding must describe the same execution role/path")
	}
}

func TestFreezePostgresStageBindingUsesOneBatchPerRealRelation(t *testing.T) {
	mapper := privacy.NewRunMapper()
	firstToken, err := mapper.ScopedToken(privacy.KindTable, "", "orders")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, err := mapper.ScopedToken(
		privacy.KindTable, privacy.ScopeCaseSensitive, "orders",
	)
	if err != nil {
		t.Fatal(err)
	}
	statements := []postgresAnalyzedStatement{
		{
			result: fingerprint.Result{Validity: testStatement(1).Facts},
			objects: []fingerprint.ObjectReference{{
				Token: firstToken, Kind: string(privacy.KindTable),
				StatementOrdinal: 1,
			}},
		},
		{
			result: fingerprint.Result{Validity: testStatement(2).Facts},
			objects: []fingerprint.ObjectReference{{
				Token: secondToken, Kind: string(privacy.KindTable),
				StatementOrdinal: 2,
			}},
		},
	}
	pathProbeCalls := 0
	batchCalls := 0
	services := &Services{
		PostgresPathProbe: func(
			context.Context, PostgresSearchTarget,
		) (FrozenPostgresSearchPath, error) {
			pathProbeCalls++
			return testPostgresPath(), nil
		},
		PostgresRelationBatchResolver: func(
			_ context.Context,
			_ PostgresSearchTarget,
			_ FrozenPostgresSearchPath,
			relations []string,
		) ([]PostgresRelationBaseline, error) {
			batchCalls++
			if !reflect.DeepEqual(relations, []string{"orders"}) {
				t.Fatalf("batch relations = %v, want one exact relation name", relations)
			}
			return []PostgresRelationBaseline{{
				TableName: "orders",
				PresentBySchema: map[string]string{
					"public": "table",
				},
				OIDBySchema: map[string]uint32{
					"public": testPostgresOID("public"),
				},
			}}, nil
		},
	}
	binding, err := services.freezePostgresStageBinding(
		context.Background(),
		frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		statements,
		mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if pathProbeCalls != 1 || batchCalls != 1 {
		t.Fatalf("probe calls=%d batch calls=%d, want one each",
			pathProbeCalls, batchCalls)
	}
	if !binding.Complete {
		t.Fatalf("binding incomplete: %+v", binding)
	}
	for _, token := range []string{firstToken, secondToken} {
		ordinal := int32(1)
		if token == secondToken {
			ordinal = 2
		}
		resolution := binding.Relations[postgresObjectBindingKey(ordinal, token)]
		if !resolution.Supported || !resolution.Present || resolution.Schema != "public" {
			t.Fatalf("ordinal %d token %s binding = %+v", ordinal, token, resolution)
		}
	}
}

func TestPostgresMetadataRejectsSameGroupAcrossSchemaSwitch(t *testing.T) {
	binding, mapper, tableToken, appToken, publicToken :=
		testPostgresOrdinalBinding(t)
	both := provider.AnonymizedGroup{
		GroupID: "group-both", RepresentativeAnonymizedSQL: "UPDATE table_1",
		MetadataContextComplete: true,
		Objects: []provider.AnonymizedObjectReference{{
			Token: tableToken, Kind: string(privacy.KindTable),
			StatementOrdinal: 1,
			StatementOrdinalRanges: []provider.StatementOrdinalRange{{
				Start: 1, End: 2,
			}},
		}},
	}
	out, _, complete := bindGroupMetadata([]provider.AnonymizedGroup{both}, binding, mapper)
	if complete || out[0].MetadataContextComplete {
		t.Fatalf("one group spanning app/public must remain partial: %+v complete=%v",
			out, complete)
	}

	first := both
	first.GroupID = "group-app"
	first.Objects = append([]provider.AnonymizedObjectReference(nil), both.Objects...)
	first.Objects[0].StatementOrdinalRanges = []provider.StatementOrdinalRange{{Start: 1, End: 1}}
	second := both
	second.GroupID = "group-public"
	second.Objects = append([]provider.AnonymizedObjectReference(nil), both.Objects...)
	second.Objects[0].StatementOrdinal = 2
	second.Objects[0].StatementOrdinalRanges = []provider.StatementOrdinalRange{{Start: 2, End: 2}}
	out, scope, complete := bindGroupMetadata(
		[]provider.AnonymizedGroup{first, second}, binding, mapper,
	)
	if !complete || !out[0].MetadataContextComplete || !out[1].MetadataContextComplete {
		t.Fatalf("separate ordinal-bound groups must remain complete: %+v complete=%v",
			out, complete)
	}
	if out[0].Objects[0].SchemaToken != appToken ||
		out[1].Objects[0].SchemaToken != publicToken {
		t.Fatalf("group schemas = %q/%q, want %q/%q",
			out[0].Objects[0].SchemaToken, out[1].Objects[0].SchemaToken,
			appToken, publicToken)
	}
	if !scope.allows(map[string]any{"schema": appToken, "table": tableToken}) ||
		!scope.allows(map[string]any{"schema": publicToken, "table": tableToken}) {
		t.Fatalf("both exact ordinal bindings must be tool-scoped: %+v", scope)
	}
}

func TestPostgresKnowledgeScopeUsesExactOrdinalBinding(t *testing.T) {
	binding, mapper, tableToken, _, _ := testPostgresOrdinalBinding(t)
	appGroup := provider.AnonymizedGroup{
		GroupID: "knowledge-app", RepresentativeAnonymizedSQL: "UPDATE table_1",
		MetadataContextComplete: true,
		Objects: []provider.AnonymizedObjectReference{{
			Token: tableToken, Kind: string(privacy.KindTable),
			StatementOrdinal: 1,
			StatementOrdinalRanges: []provider.StatementOrdinalRange{{
				Start: 1, End: 1,
			}},
		}},
	}
	publicGroup := provider.AnonymizedGroup{
		GroupID: "knowledge-public", RepresentativeAnonymizedSQL: "UPDATE table_1",
		MetadataContextComplete: true,
		Objects: []provider.AnonymizedObjectReference{{
			Token: tableToken, Kind: string(privacy.KindTable),
			StatementOrdinal: 2,
			StatementOrdinalRanges: []provider.StatementOrdinalRange{{
				Start: 2, End: 2,
			}},
		}},
	}
	scope, complete, err := resolveKnowledgeScope(
		"datasource", []provider.AnonymizedGroup{appGroup, publicGroup},
		binding, mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatalf("ordinal-bound knowledge scope incomplete: %+v", scope)
	}
	want := []knowledge.TouchedObject{
		{SchemaName: "app", TableName: "orders", SchemaCaseSensitive: true},
		{SchemaName: "public", TableName: "orders", SchemaCaseSensitive: true},
	}
	if !reflect.DeepEqual(scope.Objects, want) {
		t.Fatalf("knowledge objects = %+v, want %+v", scope.Objects, want)
	}

	crossOrdinal := appGroup
	crossOrdinal.Objects = append([]provider.AnonymizedObjectReference(nil), appGroup.Objects...)
	crossOrdinal.Objects[0].StatementOrdinalRanges =
		[]provider.StatementOrdinalRange{{Start: 1, End: 2}}
	_, complete, err = resolveKnowledgeScope(
		"datasource", []provider.AnonymizedGroup{crossOrdinal}, binding, mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("a knowledge object spanning different physical schemas must be partial")
	}
}

func TestPostgresValidityProgramBindsEachStatementOrdinal(t *testing.T) {
	binding, mapper, tableToken, appToken, publicToken :=
		testPostgresOrdinalBinding(t)
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			{
				Ordinal: 1,
				Facts: fingerprint.StatementValidity{
					Complete: true, DefinitionsComplete: true,
					Operation: fingerprint.ValidityOperationUpdate,
					Requirements: []fingerprint.ValidityRequirement{{
						Object: fingerprint.ValidityObject{
							Kind:  fingerprint.ValidityObjectTable,
							Token: tableToken,
						},
						State: fingerprint.ValidityRequirementPresent,
					}},
				},
			},
			{
				Ordinal: 2,
				Facts: fingerprint.StatementValidity{
					Complete: true, DefinitionsComplete: true,
					Operation: fingerprint.ValidityOperationUpdate,
					Requirements: []fingerprint.ValidityRequirement{{
						Object: fingerprint.ValidityObject{
							Kind:  fingerprint.ValidityObjectTable,
							Token: tableToken,
						},
						State: fingerprint.ValidityRequirementPresent,
					}},
				},
			},
		},
	}
	bound, complete := bindValidityProgram(program, binding, mapper)
	if !complete || !bound.Complete {
		t.Fatalf("ordinal validity program incomplete: %+v", bound)
	}
	got := []string{
		bound.Statements[0].Facts.Requirements[0].Object.SchemaToken,
		bound.Statements[1].Facts.Requirements[0].Object.SchemaToken,
	}
	if want := []string{appToken, publicToken}; !reflect.DeepEqual(got, want) {
		t.Fatalf("validity schema tokens = %v, want %v", got, want)
	}
}

func TestPostgresPolicyCatalogSchemasExcludeImplicitCatalogAndTemp(t *testing.T) {
	binding, _, tableToken, _, _ := testPostgresOrdinalBinding(t)
	binding.postgres.Relations[postgresObjectBindingKey(3, tableToken)] =
		frozenPostgresObjectBinding{
			Schema: "pg_catalog", Present: true, Supported: true,
		}
	binding.postgres.Relations[postgresObjectBindingKey(4, tableToken)] =
		frozenPostgresObjectBinding{
			Schema: "pg_temp_3", Present: true, Supported: true,
		}
	schemas, err := policyCatalogAuthorizedSchemas(binding)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app", "public"}; !reflect.DeepEqual(schemas, want) {
		t.Fatalf("policy catalog schemas = %v, want %v", schemas, want)
	}
}

type countingExplainSource struct {
	legacyCalls int
}

func (*countingExplainSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "postgresql"}, nil
}

func (*countingExplainSource) SchemaMetadata(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (*countingExplainSource) TableStatistics(
	context.Context, string, string,
) (map[string]any, error) {
	return map[string]any{}, nil
}

func (*countingExplainSource) IndexMetadata(
	context.Context, string, string,
) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s *countingExplainSource) SafeExplain(
	context.Context, string, string,
) (map[string]any, error) {
	s.legacyCalls++
	return map[string]any{"access_method": "legacy_single_schema", "estimate": true}, nil
}

type pathAwareExplainSource struct {
	tool.FactSource
	path FrozenPostgresSearchPath
}

func (s *pathAwareExplainSource) SafeExplainWithPostgresSearchPath(
	_ context.Context,
	schema, _ string,
	path FrozenPostgresSearchPath,
) (map[string]any, error) {
	if schema != "app" {
		return nil, tool.ErrToolFailed
	}
	s.path = path
	return map[string]any{"access_method": "index_scan", "estimate": true}, nil
}

func TestPostgresSchemaBoundSafeExplainRequiresFrozenPath(t *testing.T) {
	binding, _, _, _, _ := testPostgresOrdinalBinding(t)
	legacy := &countingExplainSource{}
	pathAware := &pathAwareExplainSource{FactSource: legacy}
	source := bindSchemaFactSource(pathAware, binding)
	fact, err := source.SafeExplain(context.Background(), "app", "group-1")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.legacyCalls != 0 {
		t.Fatal("search_path-aware Explain must not fall back to single-schema Explain")
	}
	if !pathAware.path.Equal(binding.postgres.Path) ||
		fact["access_method"] != "index_scan" {
		t.Fatalf("path-aware Explain = path %+v fact %+v", pathAware.path, fact)
	}

	legacyOnly := bindSchemaFactSource(legacy, binding)
	if _, err := legacyOnly.SafeExplain(
		context.Background(), "app", "group-1",
	); !errors.Is(err, tool.ErrToolFailed) {
		t.Fatalf("capability-less source error = %v, want tool failed", err)
	}
	if legacy.legacyCalls != 0 {
		t.Fatal("capability-less source must not receive a single-schema fallback")
	}

	temporary := binding.postgres.Path
	temporary.Effective = []string{"pg_catalog", "pg_temp_3", "app", "public"}
	binding.postgres.Path = temporary
	pathAware.path = FrozenPostgresSearchPath{}
	legacyOnly = bindSchemaFactSource(pathAware, binding)
	if _, err := legacyOnly.SafeExplain(
		context.Background(), "app", "group-1",
	); !errors.Is(err, tool.ErrToolFailed) {
		t.Fatalf("temporary path error = %v, want fail closed", err)
	}
	if !pathAware.path.Equal(FrozenPostgresSearchPath{}) {
		t.Fatal("temporary search_path must be rejected before reaching the fact source")
	}
}
