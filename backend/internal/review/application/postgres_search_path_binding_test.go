package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
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

func TestPreparePostgresObjectBindingsUniformRelationsAndValidation(t *testing.T) {
	path := testPostgresPath()
	mapper := privacy.NewRunMapper()
	ordersTok, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	tmpTok, err := mapper.Token(privacy.KindTable, "tmp_items")
	if err != nil {
		t.Fatal(err)
	}
	originalName := func(token string) (string, bool) {
		_, orig, ok := mapper.Original(token)
		return orig, ok
	}

	const total = 1500
	stmts := make([]StatementValidityAtOrdinal, total)
	lookups := make([]postgresRelationLookup, 0, total+2)
	for i := 0; i < total; i++ {
		ord := int32(i + 1)
		switch ord {
		case 1025:
			stmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectCreate,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: tmpTok},
			})
			l1 := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
			l1.TableName = "orders"
			l2 := testTableLookup(ord, tmpTok, map[string]string{})
			l2.TableName = "tmp_items"
			lookups = append(lookups, l1, l2)
		case 1026:
			stmts[i] = testStatement(ord)
			l1 := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
			l1.TableName = "orders"
			l2 := testTableLookup(ord, tmpTok, map[string]string{})
			l2.TableName = "tmp_items"
			lookups = append(lookups, l1, l2)
		default:
			stmts[i] = testStatement(ord)
			l := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
			l.TableName = "orders"
			lookups = append(lookups, l)
		}
	}

	binding, complete := preparePostgresObjectBindingsWithCallables(
		path, stmts, lookups, nil, true, originalName,
	)
	if !complete || !binding.Complete {
		t.Fatalf("uniform + non-uniform 1500-stmt binding failed: %+v", binding)
	}
	if len(binding.UniformRelations) != 1 {
		t.Fatalf("UniformRelations len = %d, want 1", len(binding.UniformRelations))
	}
	if got, ok := binding.UniformRelations[ordersTok]; !ok || got.Schema != "app" || !got.Present || !got.Supported {
		t.Fatalf("UniformRelations[orders] = %+v, ok=%v", got, ok)
	}
	if len(binding.Relations) != 2 {
		t.Fatalf("Relations len = %d, want 2 non-uniform entries", len(binding.Relations))
	}
	if got := binding.Relations[postgresObjectBindingKey(1026, tmpTok)]; !got.Present || got.CreatedByOrdinal != 1025 {
		t.Fatalf("non-uniform created binding = %+v", got)
	}

	// Round-trip decode and verify O(1) resolution even with OrdinalRangesTruncated=true.
	decoded, ok := decodeFrozenPostgresPathBinding(binding)
	if !ok || !reflect.DeepEqual(decoded.UniformRelations, binding.UniformRelations) {
		t.Fatalf("decode roundtrip failed: ok=%v decoded=%+v", ok, decoded)
	}
	sb := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"app": true, "public": true},
		postgres:   decoded,
	}
	if s, present, ok := sb.postgresObjectSchema(99999, ordersTok); !ok || !present || s != "app" {
		t.Fatalf("uniform postgresObjectSchema = %q/%v/%v", s, present, ok)
	}
	if schemas := sb.postgresObjectSchemas(ordersTok); !reflect.DeepEqual(schemas, []string{"app"}) {
		t.Fatalf("uniform postgresObjectSchemas = %v", schemas)
	}
	if s, ok := sb.postgresObjectSchemaForReference(provider.AnonymizedObjectReference{
		Token:                  ordersTok,
		Kind:                   "table",
		StatementOrdinal:       1,
		StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 512}},
		OrdinalRangesTruncated: true,
	}); !ok || s != "app" {
		t.Fatalf("uniform truncated reference failed: schema=%q ok=%v", s, ok)
	}
	// Non-uniform token whose occurrences differ in presence with OrdinalRangesTruncated=true must fail closed.
	if _, ok := sb.postgresObjectSchemaForReference(provider.AnonymizedObjectReference{
		Token:                  tmpTok,
		Kind:                   "table",
		StatementOrdinal:       1025,
		StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1025, End: 1026}},
		OrdinalRangesTruncated: true,
	}); ok {
		t.Fatal("non-uniform truncated reference must fail closed")
	}
	// Policy catalog authorized schemas must include UniformRelations schemas.
	sbNoSchemas := &stageSchemaBinding{defensible: true, postgres: decoded}
	authSchemas, err := policyCatalogAuthorizedSchemas(sbNoSchemas)
	if err != nil || !reflect.DeepEqual(authSchemas, []string{"app"}) {
		t.Fatalf("policyCatalogAuthorizedSchemas = %v err=%v", authSchemas, err)
	}

	// decodeFrozenPostgresPathBinding rejection cases for UniformRelations.
	badUniform := map[string]frozenPostgresPathBinding{
		"empty_token": func() frozenPostgresPathBinding {
			b := binding
			b.UniformRelations = map[string]frozenPostgresObjectBinding{"": {TableName: "orders", Schema: "app", Kind: "table", OID: 1, Present: true, Supported: true}}
			return b
		}(),
		"nonzero_created_by": func() frozenPostgresPathBinding {
			b := binding
			u := b.UniformRelations[ordersTok]
			u.CreatedByOrdinal = 1
			b.UniformRelations = map[string]frozenPostgresObjectBinding{ordersTok: u}
			return b
		}(),
		"zero_oid_present": func() frozenPostgresPathBinding {
			b := binding
			u := b.UniformRelations[ordersTok]
			u.OID = 0
			b.UniformRelations = map[string]frozenPostgresObjectBinding{ordersTok: u}
			return b
		}(),
		"overlap_with_relations": func() frozenPostgresPathBinding {
			b := binding
			b.UniformRelations = map[string]frozenPostgresObjectBinding{
				ordersTok: b.UniformRelations[ordersTok],
				tmpTok:    {TableName: "tmp_items", Schema: "app", Kind: "table", OID: 1, Present: true, Supported: true},
			}
			return b
		}(),
	}
	for name, b := range badUniform {
		if _, ok := decodeFrozenPostgresPathBinding(b); ok {
			t.Fatalf("decodeFrozenPostgresPathBinding unexpectedly accepted %s", name)
		}
	}
}

func TestPostgresStreamingAndFreezeEndToEndAbove1024(t *testing.T) {
	engine := newPerfTestEngine(t)
	profile := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	mapper := privacy.NewRunMapper()

	const count = 1500
	var buf bytes.Buffer
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&buf, "UPDATE public.orders SET amount = %d WHERE id = %d;\n", i, i)
	}
	analysis, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
		runID:           "run-pg-uniform",
		rawSQL:          buf.Bytes(),
		profile:         profile,
		mapper:          mapper,
		engine:          engine,
		expectedCount:   count,
		validateSyntax:  true,
		inlineLimit:     ephemeralInlineStatementLimit,
		collectPostgres: true,
	})
	if err != nil {
		t.Fatalf("streaming pg analysis failed: %v", err)
	}
	if len(analysis.postgresStatements) != count {
		t.Fatalf("postgresStatements = %d, want %d", len(analysis.postgresStatements), count)
	}

	svc := &Services{
		PostgresPathProbe: func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error) {
			return testPostgresPath(), nil
		},
		PostgresRelationBatchResolver: func(_ context.Context, _ PostgresSearchTarget, _ FrozenPostgresSearchPath, relations []string) ([]PostgresRelationBaseline, error) {
			out := make([]PostgresRelationBaseline, 0, len(relations))
			for _, name := range relations {
				out = append(out, PostgresRelationBaseline{
					TableName:       name,
					PresentBySchema: map[string]string{"public": "table"},
					OIDBySchema:     map[string]uint32{"public": 202},
				})
			}
			return out, nil
		},
		PostgresCallableResolver: func(_ context.Context, _ PostgresSearchTarget, _ FrozenPostgresSearchPath, requests []database.PostgresCallableRequest) ([]database.PostgresCallableResolution, error) {
			out := make([]database.PostgresCallableResolution, 0, len(requests))
			for _, req := range requests {
				out = append(out, database.PostgresCallableResolution{
					Request: req,
					Winners: []database.PostgresCallableWinner{{
						Schema:     "pg_catalog",
						Signature:  "int4,int4",
						ResultType: "bool",
						OID:        96,
						PgCatalog:  true,
					}},
				})
			}
			return out, nil
		},
	}

	binding, err := svc.freezePostgresStageBinding(
		context.Background(),
		frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		analysis.postgresStatements,
		mapper,
	)
	if err != nil {
		t.Fatalf("freezePostgresStageBinding failed: %v", err)
	}
	if !binding.Complete || !binding.CallablesSafe || len(binding.UniformRelations) != 1 || len(binding.Relations) != 0 {
		t.Fatalf("unexpected frozen binding: complete=%v callablesSafe=%v uniform=%d relations=%d",
			binding.Complete, binding.CallablesSafe, len(binding.UniformRelations), len(binding.Relations))
	}

	providerObjects := make([]provider.AnonymizedObjectReference, len(analysis.groups[0].objects))
	for i, o := range analysis.groups[0].objects {
		providerObjects[i] = provider.AnonymizedObjectReference{
			Token:            o.Token,
			Kind:             o.Kind,
			Relation:         string(o.Relation),
			SchemaToken:      o.SchemaToken,
			ParentToken:      o.ParentToken,
			StatementOrdinal: o.StatementOrdinal,
		}
		for _, r := range o.StatementOrdinalRanges {
			providerObjects[i].StatementOrdinalRanges = append(providerObjects[i].StatementOrdinalRanges, provider.StatementOrdinalRange{
				Start: r.Start, End: r.End,
			})
		}
	}
	stageBinding := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"app": true, "public": true},
		postgres:   &binding,
	}
	bound, scope, complete := bindPostgresGroupMetadata([]provider.AnonymizedGroup{{
		GroupID:                     "g-1",
		RepresentativeAnonymizedSQL: analysis.groups[0].representativeAnonymizedSQL,
		MetadataContextComplete:     analysis.groups[0].metadataContextComplete,
		Objects:                     providerObjects,
	}}, stageBinding, mapper)
	if !complete || !bound[0].MetadataContextComplete || len(scope.objects) == 0 {
		t.Fatalf("bindPostgresGroupMetadata failed on uniform explicit-schema batch: complete=%v bound=%+v scope=%+v",
			complete, bound, scope)
	}
}

func TestPreparePostgresObjectBindingsDemotesOneShotCreateDropFromUniform(t *testing.T) {
	path := testPostgresPath()
	mapper := privacy.NewRunMapper()
	mustTok := func(name string) string {
		tok, err := mapper.Token(privacy.KindTable, name)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	createTok := mustTok("one_shot_create")
	dropTok := mustTok("one_shot_drop")
	ordersTok := mustTok("orders")
	noopCreateTok := mustTok("noop_existing")
	noopDropTok := mustTok("noop_missing")
	originalName := func(token string) (string, bool) {
		_, orig, ok := mapper.Original(token)
		return orig, ok
	}

	const total = 1500
	stmts := make([]StatementValidityAtOrdinal, total)
	lookups := make([]postgresRelationLookup, 0, total+8)
	for i := 0; i < total; i++ {
		ord := int32(i + 1)
		switch ord {
		case 1:
			stmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectCreate,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: createTok},
			})
			l := testTableLookup(ord, createTok, map[string]string{})
			l.TableName = "one_shot_create"
			lookups = append(lookups, l)
		case 2:
			stmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectDrop,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: dropTok},
			})
			l := testTableLookup(ord, dropTok, map[string]string{"app": "table"})
			l.TableName = "one_shot_drop"
			lookups = append(lookups, l)
		case 3:
			// No-op CREATE TABLE IF NOT EXISTS on already-existing table preserves catalog state.
			stmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectCreate,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: noopCreateTok},
			})
			l := testTableLookup(ord, noopCreateTok, map[string]string{"app": "table"})
			l.TableName = "noop_existing"
			lookups = append(lookups, l)
		case 4:
			// No-op DROP TABLE IF EXISTS on missing table preserves catalog state.
			stmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectDrop,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: noopDropTok},
			})
			l := testTableLookup(ord, noopDropTok, map[string]string{})
			l.TableName = "noop_missing"
			lookups = append(lookups, l)
		case total:
			// Mutate orders on its very last reference (DROP TABLE orders).
			stmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectDrop,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: ordersTok},
			})
			l := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
			l.TableName = "orders"
			lookups = append(lookups, l)
		default:
			stmts[i] = testStatement(ord)
			l := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
			l.TableName = "orders"
			lookups = append(lookups, l)
		}
	}

	binding, complete := preparePostgresObjectBindingsWithCallables(
		path, stmts, lookups, nil, true, originalName,
	)
	if !complete || !binding.Complete {
		t.Fatalf("preparePostgresObjectBindings failed: complete=%v binding=%+v", complete, binding)
	}
	for _, mustDemote := range []string{createTok, dropTok, ordersTok} {
		if _, inUniform := binding.UniformRelations[mustDemote]; inUniform {
			t.Fatalf("mutated table token %s remained in UniformRelations", mustDemote)
		}
	}
	if _, ok := binding.Relations[postgresObjectBindingKey(1, createTok)]; !ok {
		t.Fatalf("missing exact ordinal 1 entry for one_shot_create")
	}
	if _, ok := binding.Relations[postgresObjectBindingKey(2, dropTok)]; !ok {
		t.Fatalf("missing exact ordinal 2 entry for one_shot_drop")
	}
	if len(binding.UniformRelations) != 2 {
		t.Fatalf("expected 2 non-mutating IF [NOT] EXISTS targets in UniformRelations, got %d", len(binding.UniformRelations))
	}
	if _, ok := decodeFrozenPostgresPathBinding(binding); !ok {
		t.Fatal("decodeFrozenPostgresPathBinding rejected demoted mutated relations")
	}
}

func TestPostgresObjectSchemaForReferenceTruncatedNonUniformParity(t *testing.T) {
	path := testPostgresPath()
	mapper := privacy.NewRunMapper()
	ordersTok, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	otherTok, err := mapper.Token(privacy.KindTable, "items")
	if err != nil {
		t.Fatal(err)
	}
	originalName := func(token string) (string, bool) {
		_, orig, ok := mapper.Original(token)
		return orig, ok
	}

	// Case A: 513..1024-lookup interleaved batch (useUniform=false, UniformRelations=nil).
	const midTotal = 900
	midStmts := make([]StatementValidityAtOrdinal, midTotal)
	midLookups := make([]postgresRelationLookup, 0, midTotal)
	for i := 0; i < midTotal; i++ {
		ord := int32(i + 1)
		midStmts[i] = testStatement(ord)
		if ord%2 == 1 || ord > 600 {
			l := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
			l.TableName = "orders"
			midLookups = append(midLookups, l)
		} else {
			l := testTableLookup(ord, otherTok, map[string]string{"app": "table"})
			l.TableName = "items"
			midLookups = append(midLookups, l)
		}
	}
	midBinding, midOK := preparePostgresObjectBindingsWithCallables(path, midStmts, midLookups, nil, true, originalName)
	if !midOK || len(midBinding.UniformRelations) != 0 {
		t.Fatalf("mid-size binding unexpected: ok=%v uniform=%d", midOK, len(midBinding.UniformRelations))
	}
	midSB := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"app": true, "public": true},
		postgres:   &midBinding,
	}
	if gotSchema, ok := midSB.postgresObjectSchemaForReference(provider.AnonymizedObjectReference{
		Token:                  ordersTok,
		Kind:                   "table",
		StatementOrdinal:       1,
		StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 1}, {Start: 3, End: 3}},
		OrdinalRangesTruncated: true,
	}); !ok || gotSchema != "app" {
		t.Fatalf("513..1024 truncated uniform Relations lookup = %q/%v, want app/true", gotSchema, ok)
	}

	// Case B: >1024-lookup batch where orders is demoted out of UniformRelations due to trailing DROP,
	// yet all frozen Relations entries for orders share identical (Present=true, Schema="app").
	const bigTotal = 1200
	bigStmts := make([]StatementValidityAtOrdinal, bigTotal)
	bigLookups := make([]postgresRelationLookup, 0, bigTotal)
	for i := 0; i < bigTotal; i++ {
		ord := int32(i + 1)
		if ord == bigTotal {
			bigStmts[i] = testStatement(ord, fingerprint.ValidityEffect{
				Action: fingerprint.ValidityEffectDrop,
				Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: ordersTok},
			})
		} else {
			bigStmts[i] = testStatement(ord)
		}
		l := testTableLookup(ord, ordersTok, map[string]string{"app": "table"})
		l.TableName = "orders"
		bigLookups = append(bigLookups, l)
	}
	bigBinding, bigOK := preparePostgresObjectBindingsWithCallables(path, bigStmts, bigLookups, nil, true, originalName)
	if !bigOK || len(bigBinding.UniformRelations) != 0 {
		t.Fatalf("demoted big binding unexpected: ok=%v uniform=%d", bigOK, len(bigBinding.UniformRelations))
	}
	bigSB := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"app": true, "public": true},
		postgres:   &bigBinding,
	}
	if gotSchema, ok := bigSB.postgresObjectSchemaForReference(provider.AnonymizedObjectReference{
		Token:                  ordersTok,
		Kind:                   "table",
		StatementOrdinal:       1,
		StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 1}},
		OrdinalRangesTruncated: true,
	}); !ok || gotSchema != "app" {
		t.Fatalf("demoted truncated Relations lookup = %q/%v, want app/true", gotSchema, ok)
	}

	// Case C: Tail divergence in Relations outside visible StatementOrdinalRanges must fail closed.
	bigBinding.Relations[postgresObjectBindingKey(bigTotal, ordersTok)] = frozenPostgresObjectBinding{
		TableName: "orders", Schema: "public", Kind: "table", OID: 202, Present: true, Supported: true,
	}
	if _, ok := bigSB.postgresObjectSchemaForReference(provider.AnonymizedObjectReference{
		Token:                  ordersTok,
		Kind:                   "table",
		StatementOrdinal:       1,
		StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 1}},
		OrdinalRangesTruncated: true,
	}); ok {
		t.Fatal("truncated reference with divergent tail schema must fail closed")
	}
}

func TestPostgresExplicitExternalAndTargetSchemaBinding(t *testing.T) {
	t.Parallel()
	mapper := privacy.NewRunMapper()
	eventsTok, err := mapper.Token(privacy.KindTable, "events")
	if err != nil {
		t.Fatal(err)
	}
	accountsTok, err := mapper.Token(privacy.KindTable, "accounts")
	if err != nil {
		t.Fatal(err)
	}
	analyticsTok, err := mapper.Token(privacy.KindSchema, "analytics")
	if err != nil {
		t.Fatal(err)
	}
	toastTok, err := mapper.Token(privacy.KindSchema, "pg_toast")
	if err != nil {
		t.Fatal(err)
	}
	originalName := func(tok string) (string, bool) {
		_, orig, ok := mapper.Original(tok)
		return orig, ok
	}

	basePath := testPostgresPath() // Explicit: ["app", "public"], Effective: ["pg_catalog", "app", "public"]
	stmts := []StatementValidityAtOrdinal{
		{
			Ordinal: 1,
			Facts: fingerprint.StatementValidity{
				Complete: true, DefinitionsComplete: true,
				Operation: fingerprint.ValidityOperationInsert,
				Requirements: []fingerprint.ValidityRequirement{{
					Object: fingerprint.ValidityObject{
						Kind: fingerprint.ValidityObjectTable, Token: eventsTok, SchemaToken: analyticsTok,
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
						Kind: fingerprint.ValidityObjectTable, Token: accountsTok,
					},
					State: fingerprint.ValidityRequirementPresent,
				}},
			},
		},
	}
	lookups := []postgresRelationLookup{
		{
			Ordinal: 1, TableToken: eventsTok, TableName: "events", Complete: true,
			PresentBySchema: map[string]string{"analytics": "table"},
			OIDBySchema:     map[string]uint32{"analytics": 501},
		},
		{
			Ordinal: 2, TableToken: accountsTok, TableName: "accounts", Complete: true,
			PresentBySchema: map[string]string{"public": "table"},
			OIDBySchema:     map[string]uint32{"public": 502},
		},
	}

	// Explicit outside schema + unqualified search_path resolution stay strictly separated.
	binding, ok := preparePostgresObjectBindingsWithOptions(
		basePath, stmts, lookups, nil, true, []string{"analytics", "public"}, "", originalName,
	)
	if !ok || !binding.Complete || !reflect.DeepEqual(binding.ExplicitSchemas, []string{"analytics"}) {
		t.Fatalf("explicit external schema binding = %+v ok=%v", binding, ok)
	}
	decoded, decOK := decodeFrozenPostgresPathBinding(binding)
	if !decOK || decoded.Relations[postgresObjectBindingKey(1, eventsTok)].Schema != "analytics" ||
		decoded.Relations[postgresObjectBindingKey(2, accountsTok)].Schema != "public" {
		t.Fatalf("decoded multi-schema binding = %+v ok=%v", decoded, decOK)
	}
	sb := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"app": true, "public": true, "analytics": true},
		postgres:   decoded,
	}
	if !sb.postgresAllowsSchemaToken(mapper, analyticsTok) || sb.postgresAllowsSchemaToken(mapper, toastTok) ||
		!sb.allowsPostgresLookupSchema("analytics") || sb.allowsPostgresLookupSchema("other") {
		t.Fatal("unexpected schema token authorization on external schema binding")
	}
	if got := sb.allPostgresSchemas(); !reflect.DeepEqual(got, []string{"app", "public", "analytics"}) {
		t.Fatalf("allPostgresSchemas = %v", got)
	}
	if auth, aerr := policyCatalogAuthorizedSchemas(&stageSchemaBinding{defensible: true, postgres: decoded}); aerr != nil ||
		!reflect.DeepEqual(auth, []string{"analytics", "public"}) {
		t.Fatalf("policyCatalogAuthorizedSchemas = %v err=%v", auth, aerr)
	}

	// Unqualified table present ONLY in ExplicitSchemas must NOT match unqualified lookup (strict search_path).
	onlyExternalLookup := []postgresRelationLookup{
		lookups[0],
		{
			Ordinal: 2, TableToken: accountsTok, TableName: "accounts", Complete: true,
			PresentBySchema: map[string]string{"analytics": "table"},
			OIDBySchema:     map[string]uint32{"analytics": 502},
		},
	}
	shadowed, shadowOK := preparePostgresObjectBindingsWithOptions(
		basePath, stmts, onlyExternalLookup, nil, true, []string{"analytics"}, "", originalName,
	)
	if !shadowOK || shadowed.Relations[postgresObjectBindingKey(2, accountsTok)].Present {
		t.Fatalf("unqualified table must not resolve to external ExplicitSchemas: %+v", shadowed.Relations)
	}

	// Disallowed schemas are rejected in ExplicitSchemas, extraction, and TargetSchema override.
	for _, bad := range []string{"pg_toast", "pg_toast_1", "pg_temp_1", "information_schema", "pg_catalog"} {
		if _, ok := normalizePostgresExplicitSchemas(basePath, []string{bad}); ok {
			t.Fatalf("normalizePostgresExplicitSchemas(%q) unexpectedly succeeded", bad)
		}
		if err := ValidateTargetDatabase(bad); err == nil {
			t.Fatalf("ValidateTargetDatabase(%q) unexpectedly succeeded", bad)
		}
	}
	extra, hasDisallowed := postgresExplicitSQLSchemas([]postgresAnalyzedStatement{{
		objects: []fingerprint.ObjectReference{
			{Kind: "schema", Token: analyticsTok, Relation: "reference"},
			{Kind: "schema", Token: toastTok, Relation: "reference"},
		},
	}}, mapper, basePath.Effective)
	if !hasDisallowed || !reflect.DeepEqual(extra, []string{"analytics"}) {
		t.Fatalf("postgresExplicitSQLSchemas = (%v, %v)", extra, hasDisallowed)
	}

	// TargetSchema override round-trip and mismatch rejection.
	targetPath := FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "analytics",
		Explicit: []string{"analytics"}, Effective: []string{"pg_catalog", "analytics"}, CreationSchema: "analytics",
	}
	targetBinding, targetOK := preparePostgresObjectBindingsWithOptions(
		targetPath, stmts[:1], lookups[:1], nil, true, []string{"analytics"}, "analytics", originalName,
	)
	if !targetOK || len(targetBinding.ExplicitSchemas) != 0 || targetBinding.TargetSchema != "analytics" {
		t.Fatalf("targetSchema binding = %+v ok=%v", targetBinding, targetOK)
	}
	if decTarget, ok := decodeFrozenPostgresPathBinding(targetBinding); !ok || decTarget.TargetSchema != "analytics" {
		t.Fatalf("decoded targetSchema binding = %+v ok=%v", decTarget, ok)
	}
	mismatched := targetBinding
	mismatched.TargetSchema = "other"
	if _, ok := decodeFrozenPostgresPathBinding(mismatched); ok {
		t.Fatal("mismatched TargetSchema vs Path must fail decode")
	}

	// Unreadable/error status mapping preserves concrete reason codes.
	crypter := newSealTestCrypter(t)
	for _, tc := range []struct {
		status string
		want   string
	}{
		{status: "sql_schema_out_of_scope", want: "sql_schema_out_of_scope"},
		{status: "catalog_schema_missing", want: "catalog_schema_missing"},
		{status: "catalog_permission_denied", want: "catalog_permission_denied"},
		{status: "other", want: "postgres_search_path_unavailable"},
	} {
		env, err := sealStageReviewContext(crypter, "run-pg-status", 1, map[string]any{
			stagePostgresSearchPathStatusKey: tc.status,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := postgresUnavailableReasonFromContext(crypter, "run-pg-status", 1, env); got != tc.want {
			t.Fatalf("status=%q got=%q want=%q", tc.status, got, tc.want)
		}
	}
}
