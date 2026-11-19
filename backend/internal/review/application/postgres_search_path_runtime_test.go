package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestPostgresStatementPathUnsafeRejectsUnrecognizedOperator(t *testing.T) {
	t.Parallel()
	for _, sql := range []string{
		"update app.orders set total = total @@ 1",
	} {
		if !postgresStatementPathUnsafe(sql) {
			t.Fatalf("unrecognized operator remained complete: %s", sql)
		}
	}
	for _, sql := range []string{
		"update app.orders set total = total + 1",
		"update app.orders set total = total::integer",
	} {
		if postgresStatementPathUnsafe(sql) {
			t.Fatalf("recognized operator was marked unsafe: %s", sql)
		}
	}
}

func TestPostgresPathProjectionCopiesOrderedMembers(t *testing.T) {
	t.Parallel()
	frozen := testPostgresPath()
	dbPath := databasePostgresPath(frozen)
	if err := dbPath.Validate(); err != nil {
		t.Fatal(err)
	}
	projected := frozenPostgresPathFromDatabase(dbPath)
	if !frozen.Equal(projected) {
		t.Fatalf("projected path=%+v, want %+v", projected, frozen)
	}
	dbPath.Explicit[0] = "mutated"
	dbPath.Effective[1] = "mutated"
	if !frozen.Equal(projected) {
		t.Fatal("database projection must not share backing slices")
	}
	dbPath = databasePostgresPath(frozen)
	dbPath.Explicit[0] = "mutated"
	dbPath.Effective[1] = "mutated"
	if !frozen.Equal(projected) {
		t.Fatal("review projection must not share backing slices")
	}
}

func TestProbePostgresSearchPathSeamFailsClosed(t *testing.T) {
	t.Parallel()
	target := frozenDatasourceTarget{
		DatasourceID: "ds-runtime", CompatibilityMode: "postgresql",
		Host: "db.internal", Port: 5432, DatabaseName: "analytics",
	}
	gotTarget := PostgresSearchTarget{}
	services := &Services{
		PostgresPathProbe: func(
			context.Context, PostgresSearchTarget,
		) (FrozenPostgresSearchPath, error) {
			gotTarget = postgresSearchTargetOf(target)
			return testPostgresPath(), nil
		},
	}
	path, err := services.probePostgresSearchPath(context.Background(), target)
	if err != nil || path.Validate() != nil {
		t.Fatalf("probe path=%+v err=%v", path, err)
	}
	if gotTarget.DatasourceID != target.DatasourceID ||
		gotTarget.CompatibilityMode != target.CompatibilityMode ||
		gotTarget.Host != target.Host || gotTarget.Port != target.Port ||
		gotTarget.DatabaseName == nil ||
		*gotTarget.DatabaseName != target.DatabaseName {
		t.Fatalf("projected target=%+v", gotTarget)
	}
	if empty := postgresSearchTargetOf(frozenDatasourceTarget{}); empty.DatabaseName != nil {
		t.Fatal("empty database name must stay absent")
	}

	services.PostgresPathProbe = func(
		context.Context, PostgresSearchTarget,
	) (FrozenPostgresSearchPath, error) {
		return FrozenPostgresSearchPath{}, errors.New("probe failed")
	}
	if _, err := services.probePostgresSearchPath(
		context.Background(), target,
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("probe error=%v, want unavailable", err)
	}
	services.PostgresPathProbe = func(
		context.Context, PostgresSearchTarget,
	) (FrozenPostgresSearchPath, error) {
		return FrozenPostgresSearchPath{}, nil
	}
	if _, err := services.probePostgresSearchPath(
		context.Background(), target,
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("invalid path error=%v, want unavailable", err)
	}
}

func TestResolvePostgresRelationSeamValidatesExactRequest(t *testing.T) {
	t.Parallel()
	request := PostgresRelationRequest{
		Ordinal: 2, TableToken: "table_1", TableName: "orders",
	}
	valid := PostgresRelationLookup{
		Ordinal: request.Ordinal, TableToken: request.TableToken,
		TableName: request.TableName, Complete: true,
		PresentBySchema: map[string]string{"app": "table"},
	}
	services := &Services{
		PostgresRelationResolver: func(
			_ context.Context,
			_ PostgresSearchTarget,
			_ FrozenPostgresSearchPath,
			got PostgresRelationRequest,
		) (PostgresRelationLookup, error) {
			if got != request {
				t.Fatalf("request=%+v, want %+v", got, request)
			}
			return valid, nil
		},
	}
	got, err := services.resolvePostgresRelation(
		context.Background(), frozenDatasourceTarget{}, testPostgresPath(), request,
	)
	if err != nil || !reflect.DeepEqual(got, valid) {
		t.Fatalf("lookup=%+v err=%v", got, err)
	}
	valid.PresentBySchema["app"] = "view"
	if got.PresentBySchema["app"] != "table" {
		t.Fatal("resolver result map must be cloned")
	}

	services.PostgresRelationResolver = func(
		context.Context, PostgresSearchTarget, FrozenPostgresSearchPath,
		PostgresRelationRequest,
	) (PostgresRelationLookup, error) {
		return PostgresRelationLookup{}, errors.New("resolver failed")
	}
	if _, err := services.resolvePostgresRelation(
		context.Background(), frozenDatasourceTarget{}, testPostgresPath(), request,
	); err == nil {
		t.Fatal("resolver error was ignored")
	}

	invalid := map[string]PostgresRelationLookup{
		"ordinal mismatch": {
			Ordinal: 3, TableToken: request.TableToken,
			TableName: request.TableName, Complete: true,
		},
		"token mismatch": {
			Ordinal: request.Ordinal, TableToken: "other",
			TableName: request.TableName, Complete: true,
		},
		"name mismatch": {
			Ordinal: request.Ordinal, TableToken: request.TableToken,
			TableName: "other", Complete: true,
		},
		"incomplete": {
			Ordinal: request.Ordinal, TableToken: request.TableToken,
			TableName: request.TableName,
		},
		"too many schemas": {
			Ordinal: request.Ordinal, TableToken: request.TableToken,
			TableName: request.TableName, Complete: true,
			PresentBySchema: map[string]string{"pg_catalog": "table"},
		},
	}
	for name, lookup := range invalid {
		lookup := lookup
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if name == "too many schemas" {
				for i := 0; i <= postgresSearchPathMaxSchemas+3; i++ {
					lookup.PresentBySchema[string(rune('a'+i))] = "table"
				}
			}
			svc := &Services{
				PostgresRelationResolver: func(
					context.Context, PostgresSearchTarget,
					FrozenPostgresSearchPath, PostgresRelationRequest,
				) (PostgresRelationLookup, error) {
					return lookup, nil
				},
			}
			if _, err := svc.resolvePostgresRelation(
				context.Background(), frozenDatasourceTarget{},
				testPostgresPath(), request,
			); !errors.Is(err, ErrFactSourceUnavailable) {
				t.Fatalf("invalid lookup error=%v, want unavailable", err)
			}
		})
	}
}

func TestResolvePostgresRelationBaselinesSeamsAndValidation(t *testing.T) {
	t.Parallel()
	path := testPostgresPath()
	target := frozenDatasourceTarget{}
	services := &Services{}
	got, err := services.resolvePostgresRelationBaselines(
		context.Background(), target, path, nil,
	)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty relations=%v err=%v", got, err)
	}
	if _, err := services.resolvePostgresRelationBaselines(
		context.Background(), target, path,
		make([]string, postgresSearchPathMaxResolvedTables+1),
	); err != nil {
		t.Fatalf("oversized relation set must be bounded no-op: %v", err)
	}

	services.PostgresRelationBatchResolver = func(
		_ context.Context,
		_ PostgresSearchTarget,
		_ FrozenPostgresSearchPath,
		relations []string,
	) ([]PostgresRelationBaseline, error) {
		relations[0] = "mutated"
		return []PostgresRelationBaseline{{
			TableName:       "orders",
			PresentBySchema: map[string]string{"app": "table"},
		}}, nil
	}
	relations := []string{"orders"}
	got, err = services.resolvePostgresRelationBaselines(
		context.Background(), target, path, relations,
	)
	if err != nil || relations[0] != "orders" ||
		got["orders"].PresentBySchema["app"] != "table" {
		t.Fatalf("batch baselines=%v relations=%v err=%v", got, relations, err)
	}
	services.PostgresRelationBatchResolver = func(
		context.Context, PostgresSearchTarget, FrozenPostgresSearchPath, []string,
	) ([]PostgresRelationBaseline, error) {
		return nil, errors.New("batch failed")
	}
	if _, err := services.resolvePostgresRelationBaselines(
		context.Background(), target, path, relations,
	); err == nil {
		t.Fatal("batch resolver error was ignored")
	}

	services.PostgresRelationBatchResolver = nil
	services.PostgresRelationResolver = func(
		_ context.Context,
		_ PostgresSearchTarget,
		_ FrozenPostgresSearchPath,
		request PostgresRelationRequest,
	) (PostgresRelationLookup, error) {
		if request.Ordinal != 1 || request.TableToken != request.TableName {
			t.Fatalf("resolver request=%+v", request)
		}
		return PostgresRelationLookup{
			Ordinal: request.Ordinal, TableToken: request.TableToken,
			TableName: request.TableName, Complete: true,
			PresentBySchema: map[string]string{"app": "table"},
		}, nil
	}
	got, err = services.resolvePostgresRelationBaselines(
		context.Background(), target, path, []string{"orders", "users"},
	)
	if err != nil || len(got) != 2 {
		t.Fatalf("resolver baselines=%v err=%v", got, err)
	}
	services.PostgresRelationResolver = func(
		context.Context, PostgresSearchTarget, FrozenPostgresSearchPath,
		PostgresRelationRequest,
	) (PostgresRelationLookup, error) {
		return PostgresRelationLookup{}, errors.New("resolver failed")
	}
	if _, err := services.resolvePostgresRelationBaselines(
		context.Background(), target, path, []string{"orders"},
	); err == nil {
		t.Fatal("relation resolver error was ignored")
	}

	for _, kind := range []string{
		"table", "view", "sequence", "index", "foreign_table", "other",
	} {
		if !validPostgresRelationKind(kind) {
			t.Fatalf("valid kind %q rejected", kind)
		}
	}
	if validPostgresRelationKind("unknown") {
		t.Fatal("unknown relation kind accepted")
	}
	if got := mapBaselines(map[string]PostgresRelationBaseline{
		"z": {TableName: "z"}, "a": {TableName: "a"},
	}); !reflect.DeepEqual([]string{got[0].TableName, got[1].TableName}, []string{"a", "z"}) {
		t.Fatalf("mapBaselines order=%+v", got)
	}
}

func TestValidatePostgresRelationBaselinesRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()
	path := testPostgresPath()
	valid := PostgresRelationBaseline{
		TableName:       "orders",
		PresentBySchema: map[string]string{"app": "table"},
	}
	if got, err := validatePostgresRelationBaselines(
		path, []string{"orders"}, []PostgresRelationBaseline{valid},
	); err != nil || got["orders"].PresentBySchema["app"] != "table" {
		t.Fatalf("valid baselines=%v err=%v", got, err)
	}
	tests := map[string]struct {
		relations []string
		values    []PostgresRelationBaseline
	}{
		"invalid requested identifier": {
			relations: []string{`unsafe"name`},
		},
		"missing value": {
			relations: []string{"orders"},
		},
		"unknown value": {
			relations: []string{"orders"},
			values:    []PostgresRelationBaseline{{TableName: "other"}},
		},
		"duplicate value": {
			relations: []string{"orders"},
			values:    []PostgresRelationBaseline{valid, valid},
		},
		"schema outside path": {
			relations: []string{"orders"},
			values: []PostgresRelationBaseline{{
				TableName: "orders",
				PresentBySchema: map[string]string{
					"other": "table",
				},
			}},
		},
		"invalid kind": {
			relations: []string{"orders"},
			values: []PostgresRelationBaseline{{
				TableName: "orders",
				PresentBySchema: map[string]string{
					"app": "unexpected",
				},
			}},
		},
	}
	for name, testCase := range tests {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := validatePostgresRelationBaselines(
				path, testCase.relations, testCase.values,
			); !errors.Is(err, ErrFactSourceUnavailable) {
				t.Fatalf("error=%v, want unavailable", err)
			}
		})
	}
}

func TestPostgresRelationRequestsDeduplicatesAndRejectsUnknownMapper(t *testing.T) {
	t.Parallel()
	mapper := privacy.NewRunMapper()
	tableToken, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	columnToken, err := mapper.Token(privacy.KindColumn, "id")
	if err != nil {
		t.Fatal(err)
	}
	statements := []postgresAnalyzedStatement{
		{objects: []fingerprint.ObjectReference{
			{Kind: string(privacy.KindTable), Token: tableToken},
			{Kind: string(privacy.KindTable), Token: tableToken},
			{Kind: string(privacy.KindColumn), Token: columnToken},
			{Kind: string(privacy.KindTable)},
		}},
		{objects: []fingerprint.ObjectReference{{
			Kind: string(privacy.KindTable), Token: tableToken,
		}}},
	}
	got, err := postgresRelationRequests(statements, mapper)
	if err != nil {
		t.Fatal(err)
	}
	want := []PostgresRelationRequest{
		{Ordinal: 1, TableToken: tableToken, TableName: "orders"},
		{Ordinal: 2, TableToken: tableToken, TableName: "orders"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests=%+v, want %+v", got, want)
	}
	if _, err := postgresRelationRequests(nil, nil); !errors.Is(
		err, ErrFactSourceUnavailable,
	) {
		t.Fatalf("nil mapper error=%v", err)
	}
	if _, err := postgresRelationRequests(
		[]postgresAnalyzedStatement{{objects: []fingerprint.ObjectReference{{
			Kind: string(privacy.KindTable), Token: "unknown",
		}}}}, mapper,
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("unknown mapper token error=%v", err)
	}
}

func TestFreezePostgresStageBindingSeamsFailClosed(t *testing.T) {
	t.Parallel()
	services := &Services{}
	if _, err := services.freezePostgresStageBinding(
		context.Background(), frozenDatasourceTarget{CompatibilityMode: "mysql"},
		nil, privacy.NewRunMapper(),
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("mysql error=%v", err)
	}
	if _, err := services.freezePostgresStageBinding(
		context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		nil, nil,
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("nil mapper error=%v", err)
	}
	services.PostgresPathProbe = func(
		context.Context, PostgresSearchTarget,
	) (FrozenPostgresSearchPath, error) {
		return FrozenPostgresSearchPath{}, errors.New("probe failed")
	}
	if _, err := services.freezePostgresStageBinding(
		context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		nil, privacy.NewRunMapper(),
	); err == nil {
		t.Fatal("probe error was ignored")
	}
	services.PostgresPathProbe = func(
		context.Context, PostgresSearchTarget,
	) (FrozenPostgresSearchPath, error) {
		return testPostgresPath(), nil
	}
	services.PostgresRelationBatchResolver = func(
		context.Context, PostgresSearchTarget, FrozenPostgresSearchPath, []string,
	) ([]PostgresRelationBaseline, error) {
		return nil, errors.New("baseline failed")
	}
	mapper := privacy.NewRunMapper()
	tableToken, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.freezePostgresStageBinding(
		context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		[]postgresAnalyzedStatement{{
			objects: []fingerprint.ObjectReference{{
				Kind: string(privacy.KindTable), Token: tableToken,
			}},
		}}, mapper,
	); err == nil {
		t.Fatal("baseline error was ignored")
	}
}

// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: PG stages always use the frozen search_path
// binding (probe failure -> unavailable soft failure); MySQL / other engines -> empty context.
func TestPostgresContextValueForStageStatuses(t *testing.T) {
	t.Parallel()
	svc := &Services{}

	value, err := postgresContextValueForStage(
		context.Background(), svc,
		stageRow{Engine: "mysql", CompatibilityMode: "mysql"},
		frozenDatasourceTarget{}, nil, privacy.NewRunMapper(),
	)
	if err != nil || len(value) != 0 {
		t.Fatalf("mysql context=%v err=%v", value, err)
	}

	// Probe seam absent: real probe path is unreachable (no DB row) -> unavailable soft failure.
	value, err = postgresContextValueForStage(
		context.Background(), svc,
		stageRow{Engine: "postgresql", CompatibilityMode: "postgresql"},
		frozenDatasourceTarget{}, nil, privacy.NewRunMapper(),
	)
	if err != nil || value[stagePostgresSearchPathStatusKey] != postgresSearchPathUnavailable {
		t.Fatalf("unreachable probe context=%v err=%v", value, err)
	}

	svc.PostgresPathProbe = func(
		context.Context, PostgresSearchTarget,
	) (FrozenPostgresSearchPath, error) {
		return FrozenPostgresSearchPath{}, errors.New("probe unavailable")
	}
	value, err = postgresContextValueForStage(
		context.Background(), svc,
		stageRow{Engine: "postgresql", CompatibilityMode: "postgresql"},
		frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		nil, privacy.NewRunMapper(),
	)
	if err != nil || value[stagePostgresSearchPathStatusKey] != postgresSearchPathUnavailable {
		t.Fatalf("failed frozen path context=%v err=%v", value, err)
	}

	svc.PostgresPathProbe = func(
		context.Context, PostgresSearchTarget,
	) (FrozenPostgresSearchPath, error) {
		return testPostgresPath(), nil
	}
	value, err = postgresContextValueForStage(
		context.Background(), svc,
		stageRow{Engine: "postgresql", CompatibilityMode: "postgresql"},
		frozenDatasourceTarget{CompatibilityMode: "postgresql"},
		nil, privacy.NewRunMapper(),
	)
	if err != nil ||
		value[stagePostgresSearchPathKey] == nil ||
		value[stagePostgresObjectBindingKey] == nil {
		t.Fatalf("frozen path context=%v err=%v", value, err)
	}
}

func TestPostgresObjectLookupHelpersDistinguishAbsentAndUnsupported(t *testing.T) {
	t.Parallel()
	var nilBinding *stageSchemaBinding
	if _, _, ok := nilBinding.postgresObjectSchema(1, "table_1"); ok {
		t.Fatal("nil binding resolved")
	}
	if got := nilBinding.postgresObjectSchemas("table_1"); got != nil {
		t.Fatalf("nil binding schemas=%v", got)
	}
	binding, _, tableToken, _, _ := testPostgresOrdinalBinding(t)
	binding.postgres.Relations[postgresObjectBindingKey(3, tableToken)] =
		frozenPostgresObjectBinding{Supported: true}
	binding.postgres.Relations[postgresObjectBindingKey(4, tableToken)] =
		frozenPostgresObjectBinding{
			Schema: "app", Present: true, Supported: false,
		}
	if got := binding.postgresObjectSchemas(tableToken); !reflect.DeepEqual(
		got, []string{"app", "public"},
	) {
		t.Fatalf("postgresObjectSchemas=%v", got)
	}
	schema, present, complete := binding.postgresObjectSchema(3, tableToken)
	if !complete || present || schema != "app" {
		t.Fatalf("absent relation=%q/%v/%v", schema, present, complete)
	}
	if _, _, complete := binding.postgresObjectSchema(4, tableToken); complete {
		t.Fatal("unsupported relation resolved")
	}
	if _, _, complete := binding.postgresObjectSchemaAt(0, tableToken); complete {
		t.Fatal("ordinal zero resolved")
	}
}

func TestPostgresReferenceOrdinalsRejectsInvalidRanges(t *testing.T) {
	t.Parallel()
	object := provider.AnonymizedObjectReference{
		StatementOrdinalRanges: []provider.StatementOrdinalRange{
			{Start: 3, End: 2},
		},
	}
	if got := postgresReferenceOrdinals(object); got != nil {
		t.Fatalf("invalid range ordinals=%v", got)
	}
	object.StatementOrdinalRanges = []provider.StatementOrdinalRange{
		{Start: 2, End: 4},
		{Start: 3, End: 3},
	}
	if got := postgresReferenceOrdinals(object); !reflect.DeepEqual(
		got, []int32{2, 3, 4},
	) {
		t.Fatalf("deduplicated ordinals=%v", got)
	}
	if got := postgresReferenceOrdinals(
		provider.AnonymizedObjectReference{StatementOrdinal: 0},
	); len(got) != 0 {
		t.Fatalf("zero default ordinal=%v", got)
	}
}

func TestFrozenPostgresSearchPathRejectsStructuralDrift(t *testing.T) {
	t.Parallel()
	valid := testPostgresPath()
	catalogExplicit := valid
	catalogExplicit.Explicit = []string{"app", "pg_catalog", "public"}
	catalogExplicit.Effective = []string{"app", "pg_catalog", "public"}
	if err := catalogExplicit.Validate(); err != nil {
		t.Fatalf("valid explicit catalog order rejected: %v", err)
	}

	tests := map[string]func(*FrozenPostgresSearchPath){
		"invalid creation identifier": func(path *FrozenPostgresSearchPath) {
			path.CreationSchema = `unsafe"schema`
		},
		"duplicate explicit": func(path *FrozenPostgresSearchPath) {
			path.Explicit = []string{"app", "app"}
			path.Effective = []string{"pg_catalog", "app"}
		},
		"empty effective": func(path *FrozenPostgresSearchPath) {
			path.Effective = []string{""}
		},
		"oversized effective member": func(path *FrozenPostgresSearchPath) {
			path.Effective = []string{"pg_catalog", "app", "public", string(make([]byte, 257))}
		},
		"duplicate effective": func(path *FrozenPostgresSearchPath) {
			path.Effective = []string{"pg_catalog", "app", "app", "public"}
		},
		"unknown implicit": func(path *FrozenPostgresSearchPath) {
			path.Effective = []string{"pg_catalog", "app", "public", "unknown"}
		},
		"creation is not first": func(path *FrozenPostgresSearchPath) {
			path.CreationSchema = "public"
		},
		"wrong effective order": func(path *FrozenPostgresSearchPath) {
			path.Effective = []string{"pg_catalog", "public", "app"}
		},
		"too many explicit": func(path *FrozenPostgresSearchPath) {
			path.Explicit = make([]string, postgresSearchPathMaxSchemas+1)
		},
		"too many effective": func(path *FrozenPostgresSearchPath) {
			path.Effective = make([]string, postgresSearchPathMaxSchemas+4)
		},
	}
	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := valid
			path.Explicit = append([]string(nil), valid.Explicit...)
			path.Effective = append([]string(nil), valid.Effective...)
			mutate(&path)
			if !errors.Is(path.Validate(), errPostgresSearchPathInvalid) {
				t.Fatalf("invalid path %+v was accepted", path)
			}
		})
	}
}

func TestPostgresBindingRejectsMalformedCatalogEvidence(t *testing.T) {
	t.Parallel()
	token := "table_token"
	validStatement := testStatement(1)
	validLookup := testTableLookup(
		1, token, map[string]string{"app": "table"},
	)
	validLookup.TableName = token
	tooManyPresence := map[string]string{}
	for i := 0; i <= postgresSearchPathMaxSchemas+3; i++ {
		tooManyPresence[string(rune('a'+i))] = "table"
	}
	tests := map[string]struct {
		path       FrozenPostgresSearchPath
		statements []StatementValidityAtOrdinal
		lookups    []postgresRelationLookup
		original   func(string) (string, bool)
	}{
		"invalid path": {
			path:       FrozenPostgresSearchPath{},
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups:    []postgresRelationLookup{validLookup},
		},
		"no statements": {
			path:    testPostgresPath(),
			lookups: []postgresRelationLookup{validLookup},
		},
		"invalid ordinal": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 0, TableToken: token, TableName: token, Complete: true,
			}},
		},
		"empty token": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 1, TableName: token, Complete: true,
			}},
		},
		"incomplete lookup": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 1, TableToken: token, TableName: token,
			}},
		},
		"empty table name": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 1, TableToken: token, Complete: true,
			}},
		},
		"too many presence schemas": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 1, TableToken: token, TableName: token, Complete: true,
				PresentBySchema: tooManyPresence,
			}},
		},
		"original mapper missing": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups:    []postgresRelationLookup{validLookup},
			original: func(string) (string, bool) {
				return "", false
			},
		},
		"original mapper mismatch": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups:    []postgresRelationLookup{validLookup},
			original: func(string) (string, bool) {
				return "other", true
			},
		},
		"duplicate lookup key": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups:    []postgresRelationLookup{validLookup, validLookup},
		},
		"schema outside path": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 1, TableToken: token, TableName: token, Complete: true,
				PresentBySchema: map[string]string{"other": "table"},
			}},
		},
		"empty relation kind": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{{
				Ordinal: 1, TableToken: token, TableName: token, Complete: true,
				PresentBySchema: map[string]string{"app": ""},
			}},
		},
		"catalog changed across tokens": {
			path:       testPostgresPath(),
			statements: []StatementValidityAtOrdinal{validStatement},
			lookups: []postgresRelationLookup{
				{
					Ordinal: 1, TableToken: "token_a", TableName: "orders",
					Complete: true,
					PresentBySchema: map[string]string{
						"app": "table",
					},
				},
				{
					Ordinal: 1, TableToken: "token_b", TableName: "orders",
					Complete: true,
					PresentBySchema: map[string]string{
						"app": "view",
					},
				},
			},
			original: func(token string) (string, bool) {
				return "orders", true
			},
		},
	}
	for name, testCase := range tests {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			binding, complete := preparePostgresObjectBindings(
				testCase.path, testCase.statements, testCase.lookups, testCase.original,
			)
			if complete {
				t.Fatalf("malformed evidence accepted: %+v", binding)
			}
		})
	}
}

func TestPostgresBindingRemainsPartialForUnknownEffects(t *testing.T) {
	t.Parallel()
	token := "table_token"
	path := testPostgresPath()
	create := testStatement(1, fingerprint.ValidityEffect{
		Action: fingerprint.ValidityEffectCreate,
		Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: token,
			SchemaToken: "missing_schema",
		},
	})
	tests := map[string]struct {
		statements []StatementValidityAtOrdinal
		lookups    []postgresRelationLookup
		original   func(string) (string, bool)
	}{
		"parser incomplete": {
			statements: []StatementValidityAtOrdinal{{
				Ordinal: 1,
				Facts: fingerprint.StatementValidity{
					Complete: false,
					Effects:  create.Facts.Effects,
				},
			}},
			lookups: []postgresRelationLookup{
				testTableLookup(1, token, map[string]string{"app": "table"}),
			},
		},
		"parser unsupported": {
			statements: []StatementValidityAtOrdinal{{
				Ordinal: 1,
				Facts: fingerprint.StatementValidity{
					Complete: true, Unsupported: "rename",
					Effects: create.Facts.Effects,
				},
			}},
			lookups: []postgresRelationLookup{
				testTableLookup(1, token, map[string]string{"app": "table"}),
			},
		},
		"create target outside path": {
			statements: []StatementValidityAtOrdinal{create},
			lookups: []postgresRelationLookup{
				testTableLookup(1, token, map[string]string{"app": "table"}),
			},
			original: func(value string) (string, bool) {
				if value == "missing_schema" {
					return "other", true
				}
				return token, true
			},
		},
		"effect missing lookup": {
			statements: []StatementValidityAtOrdinal{create},
		},
		"non-table replacement": {
			statements: []StatementValidityAtOrdinal{testStatement(1,
				fingerprint.ValidityEffect{
					Action: fingerprint.ValidityEffectRename,
					Object: fingerprint.ValidityObject{
						Kind: fingerprint.ValidityObjectColumn, Token: "column_1",
					},
					Replacement: &fingerprint.ValidityObject{
						Kind: fingerprint.ValidityObjectTable, Token: token,
					},
				},
			)},
			lookups: []postgresRelationLookup{
				testTableLookup(1, token, map[string]string{"app": "table"}),
			},
		},
	}
	for name, testCase := range tests {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			binding, complete := preparePostgresObjectBindings(
				path, testCase.statements, testCase.lookups, testCase.original,
			)
			if complete || binding.Complete {
				t.Fatalf("unknown effect accepted: %+v", binding)
			}
		})
	}
}

func TestStatementTableTargetSchemaRejectsAmbiguousIdentity(t *testing.T) {
	t.Parallel()
	object := fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectTable, Token: "table_token",
		SchemaToken: "schema_token",
	}
	flat := fingerprint.StatementValidity{
		Complete: true,
		Requirements: []fingerprint.ValidityRequirement{
			{Object: object},
			{Object: fingerprint.ValidityObject{
				Kind: fingerprint.ValidityObjectTable, Token: "table_token",
			}},
		},
	}
	if _, explicit, complete := statementTableTargetSchema(
		flat, "table_token", func(string) (string, bool) { return "app", true },
	); !explicit || complete {
		t.Fatalf("mixed qualification explicit=%v complete=%v", explicit, complete)
	}
	if _, _, complete := statementTableTargetSchema(
		flat, "table_token", nil,
	); complete {
		t.Fatal("qualified object without mapper was accepted")
	}
	if _, _, complete := statementTableTargetSchema(
		flat, "table_token", func(string) (string, bool) { return "", false },
	); complete {
		t.Fatal("unknown schema token was accepted")
	}
	if _, _, complete := statementTableTargetSchema(
		flat, "table_token", func(string) (string, bool) {
			return `unsafe"schema`, true
		},
	); complete {
		t.Fatal("unsafe schema was accepted")
	}
	conflicting := flat
	conflicting.Requirements = []fingerprint.ValidityRequirement{
		{Object: object},
		{Object: fingerprint.ValidityObject{
			Kind: fingerprint.ValidityObjectTable, Token: "table_token",
			SchemaToken: "other_schema_token",
		}},
	}
	if _, _, complete := statementTableTargetSchema(
		conflicting, "table_token", func(token string) (string, bool) {
			return token, true
		},
	); complete {
		t.Fatal("conflicting qualified schemas were accepted")
	}
	if _, explicit, complete := statementTableTargetSchema(
		flat, "other_table", func(string) (string, bool) { return "app", true },
	); explicit || !complete {
		t.Fatalf("unrelated token explicit=%v complete=%v", explicit, complete)
	}
}

func TestFrozenPostgresEncodingRejectsMalformedValues(t *testing.T) {
	t.Parallel()
	validPath := testPostgresPath()
	validBinding := frozenPostgresPathBinding{
		Version:          postgresObjectResolutionVersion,
		Path:             validPath,
		Complete:         true,
		CallablesSafe:    true,
		CallablesChecked: true,
		Baseline: map[string]frozenPostgresBaselineRelation{
			"table_1": {
				PresentBySchema: map[string]string{"app": "table"},
				OIDBySchema:     map[string]uint32{"app": 101},
			},
		},
		Relations: map[string]frozenPostgresObjectBinding{
			"1:table_1": {
				TableName: "table_1", Schema: "app", Kind: "table", OID: 101,
				Present: true, Supported: true,
			},
		},
	}
	for name, value := range map[string]any{
		"missing path":       nil,
		"unknown path field": map[string]any{"unknown": true},
		"invalid path value": map[string]any{"version": 1},
		"unmarshal failure":  make(chan int),
	} {
		if _, ok := decodeFrozenPostgresPath(value); ok {
			t.Fatalf("%s path was accepted", name)
		}
	}
	for name, value := range map[string]any{
		"missing binding":   nil,
		"unknown field":     map[string]any{"unknown": true},
		"wrong version":     map[string]any{"version": 2},
		"invalid path":      map[string]any{"version": 1},
		"unmarshal failure": make(chan int),
		"short relation key": map[string]any{
			"version": 1, "path": validPath,
			"relations": map[string]any{"x": map[string]any{
				"present": false, "schema": "app", "supported": true,
			}},
		},
		"invalid present schema": map[string]any{
			"version": 1, "path": validPath,
			"relations": map[string]any{"1:table_1": map[string]any{
				"present": true, "schema": `unsafe"schema`, "supported": true,
			}},
		},
		"absent schema mismatch": map[string]any{
			"version": 1, "path": validPath,
			"relations": map[string]any{"1:table_1": map[string]any{
				"present": false, "schema": "public", "supported": true,
			}},
		},
		"present oid zero without provenance": map[string]any{
			"version": 1, "path": validPath,
			"baseline": map[string]any{},
			"relations": map[string]any{"1:table_1": map[string]any{
				"table_name": "table_1", "present": true, "schema": "app",
				"kind": "table", "supported": true,
			}},
		},
		"missing baseline": map[string]any{
			"version":   1,
			"path":      validPath,
			"relations": map[string]any{},
		},
	} {
		if _, ok := decodeFrozenPostgresPathBinding(value); ok {
			t.Fatalf("%s binding was accepted", name)
		}
	}
	decoded, ok := decodeFrozenPostgresPathBinding(validBinding)
	if !ok || !reflect.DeepEqual(decoded, &validBinding) {
		t.Fatalf("valid binding=%+v ok=%v", decoded, ok)
	}

	if _, err := withFrozenPostgresBinding(nil, frozenPostgresPathBinding{}); !errors.Is(
		err, errPostgresSearchPathInvalid,
	) {
		t.Fatalf("invalid binding error=%v", err)
	}
	value, err := withFrozenPostgresBinding(
		map[string]any{"kept": true}, validBinding,
	)
	if err != nil || value["kept"] != true ||
		value[stagePostgresSearchPathKey] == nil ||
		value[stagePostgresObjectBindingKey] == nil {
		t.Fatalf("encoded binding=%v err=%v", value, err)
	}
}

func TestPostgresPresenceAndSchemaOrderingHelpers(t *testing.T) {
	t.Parallel()
	if !equalPostgresPresence(
		map[string]string{"app": "table"},
		map[string]string{"app": "table"},
	) || equalPostgresPresence(
		map[string]string{"app": "table"},
		map[string]string{"app": "view"},
	) || equalPostgresPresence(
		map[string]string{"app": "table"},
		map[string]string{"app": "table", "public": "table"},
	) {
		t.Fatal("presence equality is incorrect")
	}
	if !equalStrings([]string{"app", "public"}, []string{"app", "public"}) ||
		equalStrings([]string{"app"}, []string{"app", "public"}) ||
		equalStrings([]string{"app", "public"}, []string{"app", "private"}) {
		t.Fatal("ordered string equality is incorrect")
	}
	binding := &stageSchemaBinding{postgres: &frozenPostgresPathBinding{
		Path: FrozenPostgresSearchPath{
			Explicit: []string{
				"app", "pg_catalog", "public", "app", "pg_temp_3",
			},
		},
	}}
	if got := binding.allPostgresSchemas(); !reflect.DeepEqual(
		got, []string{"app", "public"},
	) {
		t.Fatalf("allPostgresSchemas=%v", got)
	}
}
