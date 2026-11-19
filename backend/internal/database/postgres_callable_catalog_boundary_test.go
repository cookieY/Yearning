package database

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"
)

func TestNormalizePostgresCallableRequestsBoundaries(t *testing.T) {
	t.Parallel()

	tests := map[string][]PostgresCallableRequest{
		"unknown kind": {
			{Kind: "view", Name: "orders"},
		},
		"invalid function name": {
			{Kind: PostgresCallableFunction, Name: `bad"name`},
		},
		"empty operator": {
			{Kind: PostgresCallableOperator, Name: ""},
		},
		"identifier is not an operator spelling": {
			{Kind: PostgresCallableOperator, Name: "lower"},
		},
		"oversized batch": make(
			[]PostgresCallableRequest, maxPostgresCallableLookups+1,
		),
	}
	for name, requests := range tests {
		name, requests := name, requests
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizePostgresCallableRequests(requests)
			if !errors.Is(err, ErrPostgresCallableUnresolved) {
				t.Fatalf(
					"NormalizePostgresCallableRequests() error = %v, want %v",
					err, ErrPostgresCallableUnresolved,
				)
			}
			if got != nil {
				t.Fatalf("normalized requests = %+v, want nil", got)
			}
		})
	}
	if err := ValidatePostgresCallableName("view", "orders"); !errors.Is(
		err, ErrPostgresCallableUnresolved,
	) {
		t.Fatalf("unknown callable kind error = %v, want %v", err, ErrPostgresCallableUnresolved)
	}

	got, err := NormalizePostgresCallableRequests([]PostgresCallableRequest{
		{Kind: PostgresCallableOperator, Name: "||"},
		{Kind: PostgresCallableFunction, Name: "zeta"},
		{Kind: PostgresCallableFunction, Name: "alpha"},
		{Kind: PostgresCallableOperator, Name: "@"},
		{Kind: PostgresCallableFunction, Name: "alpha"},
	})
	if err != nil {
		t.Fatalf("NormalizePostgresCallableRequests() error = %v", err)
	}
	want := []PostgresCallableRequest{
		{Kind: PostgresCallableFunction, Name: "alpha"},
		{Kind: PostgresCallableFunction, Name: "zeta"},
		{Kind: PostgresCallableOperator, Name: "@"},
		{Kind: PostgresCallableOperator, Name: "||"},
	}
	if !postgresCallableRequestsEqual(got, want) {
		t.Fatalf("normalized requests = %+v, want stable order %+v", got, want)
	}

	empty, err := NormalizePostgresCallableRequests(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty request normalization = %+v, %v; want non-nil empty slice", empty, err)
	}
}

func TestPostgresCallablePayloadPreservesFrozenIdentity(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	normalized, err := NormalizePostgresCallableRequests([]PostgresCallableRequest{
		{Kind: PostgresCallableOperator, Name: "||"},
		{Kind: PostgresCallableFunction, Name: "zeta"},
		{Kind: PostgresCallableOperator, Name: "@"},
		{Kind: PostgresCallableFunction, Name: "alpha"},
		{Kind: PostgresCallableFunction, Name: "alpha"},
	})
	if err != nil {
		t.Fatalf("NormalizePostgresCallableRequests() error = %v", err)
	}
	raw, err := postgresCallablePayload(path, normalized)
	if err != nil {
		t.Fatalf("postgresCallablePayload() error = %v", err)
	}
	var input postgresCallableBatchInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("payload is not JSON: %v; %s", err, raw)
	}
	if !stringSlicesEqual(input.Functions, []string{"alpha", "zeta"}) {
		t.Fatalf("functions = %v, want [alpha zeta]", input.Functions)
	}
	if !stringSlicesEqual(input.Operators, []string{"@", "||"}) {
		t.Fatalf("operators = %v, want [@ ||]", input.Operators)
	}
	if !stringSlicesEqual(input.Schemas, path.Effective) {
		t.Fatalf("schemas = %v, want %v", input.Schemas, path.Effective)
	}
	if input.Role != path.CurrentUser {
		t.Fatalf("role = %q, want %q", input.Role, path.CurrentUser)
	}

	if _, err := postgresCallablePayload(path, []PostgresCallableRequest{{
		Kind: "view", Name: "orders",
	}}); !errors.Is(err, ErrPostgresCallableUnresolved) {
		t.Fatalf("invalid payload kind error = %v, want %v", err, ErrPostgresCallableUnresolved)
	}
}

func TestResolvePostgresCallablesValidation(t *testing.T) {
	t.Parallel()

	validPath := validSearchPathForTest()
	validRequest := []PostgresCallableRequest{{
		Kind: PostgresCallableFunction,
		Name: "lower",
	}}
	tests := map[string]struct {
		pool     *PurposePool
		path     PostgresSearchPath
		requests []PostgresCallableRequest
		wantErr  error
	}{
		"nil pool": {
			path: validPath, requests: validRequest,
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"wrong engine": {
			pool: &PurposePool{engine: "mysql", purpose: PurposeReview},
			path: validPath, requests: validRequest,
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"wrong purpose": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeQuery},
			path: validPath, requests: validRequest,
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"invalid path": {
			pool:     &PurposePool{engine: "postgresql", purpose: PurposeReview},
			requests: validRequest,
			wantErr:  ErrPostgresSearchPathUnavailable,
		},
		"invalid request kind": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path: validPath,
			requests: []PostgresCallableRequest{{
				Kind: "view", Name: "orders",
			}},
			wantErr: ErrPostgresCallableUnresolved,
		},
		"invalid function name": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path: validPath,
			requests: []PostgresCallableRequest{{
				Kind: PostgresCallableFunction, Name: `bad"name`,
			}},
			wantErr: ErrPostgresCallableUnresolved,
		},
		"oversized batch": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path: validPath,
			requests: make(
				[]PostgresCallableRequest, maxPostgresCallableLookups+1,
			),
			wantErr: ErrPostgresCallableUnresolved,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolvePostgresCallables(
				context.Background(), tc.pool, tc.path, tc.requests,
			)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolvePostgresCallables() error = %v, want %v", err, tc.wantErr)
			}
			if got != nil {
				t.Fatalf("resolutions = %+v, want nil", got)
			}
		})
	}
}

func TestResolvePostgresCallablesCatalogFailures(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	requests := []PostgresCallableRequest{{
		Kind: PostgresCallableFunction,
		Name: "lower",
	}}
	tests := map[string]struct {
		beginErr error
		execErr  error
		queryErr error
		values   [][]driver.Value
		tailErr  error
		wantErr  error
		wantAny  bool
		requests []PostgresCallableRequest
	}{
		"transaction begin failure": {
			beginErr: errors.New("begin failed"),
			wantAny:  true,
		},
		"catalog search path failure": {
			execErr: errors.New("set search path failed"),
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"catalog query failure": {
			queryErr: errors.New("query failed"),
			wantErr:  ErrPostgresSearchPathUnavailable,
		},
		"empty result set": {
			values:  [][]driver.Value{},
			wantErr: ErrPostgresCallableUnresolved,
		},
		"empty result stream failure": {
			values:  [][]driver.Value{},
			tailErr: errors.New("stream failed before first row"),
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"result scan failure": {
			values:  [][]driver.Value{{int64(1)}},
			wantErr: ErrPostgresCallableUnresolved,
		},
		"extra result row": {
			values: [][]driver.Value{
				{`[]`},
				{`[]`},
			},
			wantErr: ErrPostgresCallableUnresolved,
		},
		"result stream failure after row": {
			values:  [][]driver.Value{{`[]`}},
			tailErr: errors.New("stream failed after row"),
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"malformed catalog JSON": {
			values:  [][]driver.Value{{`[`}},
			wantErr: ErrPostgresCallableUnresolved,
		},
		"empty request batch": {
			requests: []PostgresCallableRequest{},
			wantErr:  ErrPostgresCallableUnresolved,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					beginErr: tc.beginErr,
					execHook: func(context.Context, string) (driver.Result, error) {
						if tc.execErr != nil {
							return nil, tc.execErr
						}
						return driver.RowsAffected(0), nil
					},
					queryHook: func(
						context.Context, string, []driver.NamedValue,
					) (driver.Rows, error) {
						if tc.queryErr != nil {
							return nil, tc.queryErr
						}
						return &searchPathCoverageRows{
							columns: []string{"callables"},
							values:  tc.values,
							tailErr: tc.tailErr,
						}, nil
					},
				}}, nil
			}, nil)
			pool := newCoveragePurposePool(db, "postgresql", PurposeReview)
			t.Cleanup(func() { _ = pool.Close() })

			caseRequests := requests
			if tc.requests != nil {
				caseRequests = tc.requests
			}
			got, err := ResolvePostgresCallables(
				context.Background(), pool, path, caseRequests,
			)
			if tc.wantAny {
				if err == nil {
					t.Fatal("ResolvePostgresCallables() error = nil, want failure")
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolvePostgresCallables() error = %v, want %v", err, tc.wantErr)
			}
			if got != nil {
				t.Fatalf("resolutions = %+v, want nil", got)
			}
		})
	}
}

func TestResolvePostgresCallablesHonorsContextCancellation(t *testing.T) {
	path := validSearchPathForTest()
	requests := []PostgresCallableRequest{{
		Kind: PostgresCallableFunction,
		Name: "lower",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
		return &searchPathCoverageConn{session: &searchPathCoverageSession{
			execHook: func(context.Context, string) (driver.Result, error) {
				cancel()
				return driver.RowsAffected(0), nil
			},
			queryHook: func(
				ctx context.Context, _ string, _ []driver.NamedValue,
			) (driver.Rows, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}}, nil
	}, nil)
	pool := newCoveragePurposePool(db, "postgresql", PurposeReview)
	t.Cleanup(func() { _ = pool.Close() })

	got, err := ResolvePostgresCallables(ctx, pool, path, requests)
	if !errors.Is(err, ErrPostgresSearchPathUnavailable) {
		t.Fatalf("ResolvePostgresCallables() error = %v, want unavailable", err)
	}
	if got != nil {
		t.Fatalf("resolutions = %+v, want nil", got)
	}
}

func TestBuildPostgresCallableResolutionsRejectsInvalidWinnerFacts(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	functionRequest := []PostgresCallableRequest{{
		Kind: PostgresCallableFunction,
		Name: "lower",
	}}
	operatorRequest := []PostgresCallableRequest{{
		Kind: PostgresCallableOperator,
		Name: "@",
	}}
	validFunction := postgresCallableBatchRow{
		Kind: string(PostgresCallableFunction),
		Name: "lower",
		Winners: []postgresCallableBatchWinner{{
			Schema:     "pg_catalog",
			Signature:  "text",
			ResultType: "text",
			OID:        101,
			PgCatalog:  true,
		}},
	}
	validOperator := postgresCallableBatchRow{
		Kind: string(PostgresCallableOperator),
		Name: "@",
		Winners: []postgresCallableBatchWinner{{
			Schema:     "pg_catalog",
			Signature:  "integer",
			ResultType: "integer",
			OID:        102,
			PgCatalog:  true,
		}},
	}
	tests := map[string]struct {
		requests []PostgresCallableRequest
		values   []postgresCallableBatchRow
	}{
		"unknown result kind": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: "view", Name: "lower",
			}},
		},
		"invalid result name": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: `bad"name`,
			}},
		},
		"duplicate result identity": {
			requests: functionRequest,
			values:   []postgresCallableBatchRow{validFunction, validFunction},
		},
		"unknown result identity": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "upper",
			}},
		},
		"wrong observed kind": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableOperator),
				Name: "lower",
			}},
		},
		"invalid winner schema": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "lower",
				Winners: []postgresCallableBatchWinner{{
					Schema: `bad"schema`, Signature: "text",
					ResultType: "text", OID: 101,
				}},
			}},
		},
		"empty winner signature": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "lower",
				Winners: []postgresCallableBatchWinner{{
					Schema: "pg_catalog", ResultType: "text", OID: 101,
					PgCatalog: true,
				}},
			}},
		},
		"zero winner OID": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "lower",
				Winners: []postgresCallableBatchWinner{{
					Schema: "pg_catalog", Signature: "text",
					ResultType: "text", PgCatalog: true,
				}},
			}},
		},
		"pg_catalog flag mismatch": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "lower",
				Winners: []postgresCallableBatchWinner{{
					Schema: "pg_catalog", Signature: "text",
					ResultType: "text", OID: 101,
				}},
			}},
		},
		"winner schema outside frozen path": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "lower",
				Winners: []postgresCallableBatchWinner{{
					Schema: "elsewhere", Signature: "text",
					ResultType: "text", OID: 101,
				}},
			}},
		},
		"duplicate winner signature": {
			requests: functionRequest,
			values: []postgresCallableBatchRow{{
				Kind: string(PostgresCallableFunction),
				Name: "lower",
				Winners: []postgresCallableBatchWinner{
					{
						Schema: "pg_catalog", Signature: "text",
						ResultType: "text", OID: 101, PgCatalog: true,
					},
					{
						Schema: "deploy_user", Signature: "text",
						ResultType: "text", OID: 201,
					},
				},
			}},
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := buildPostgresCallableResolutions(
				path, tc.requests, tc.values,
			)
			if !errors.Is(err, ErrPostgresCallableUnresolved) {
				t.Fatalf(
					"buildPostgresCallableResolutions() error = %v, want %v",
					err, ErrPostgresCallableUnresolved,
				)
			}
			if got != nil {
				t.Fatalf("resolutions = %+v, want nil", got)
			}
		})
	}

	absent, err := buildPostgresCallableResolutions(
		path, functionRequest, nil,
	)
	if err != nil {
		t.Fatalf("missing requested identity error = %v", err)
	}
	if len(absent) != 1 ||
		absent[0].Request != functionRequest[0] ||
		absent[0].Comparable() ||
		absent[0].Unsafe() ||
		len(absent[0].Winners) != 0 {
		t.Fatalf(
			"missing requested identity = %+v, want successful empty winner evidence",
			absent,
		)
	}

	emptyWinner := postgresCallableBatchRow{
		Kind:    string(PostgresCallableFunction),
		Name:    "lower",
		Winners: []postgresCallableBatchWinner{},
	}
	got, err := buildPostgresCallableResolutions(
		path, functionRequest, []postgresCallableBatchRow{emptyWinner},
	)
	if err != nil {
		t.Fatalf("empty winner evidence error = %v", err)
	}
	if len(got) != 1 || got[0].Comparable() || got[0].Unsafe() {
		t.Fatalf("empty winner evidence = %+v, want comparable=false unsafe=false", got)
	}

	for name, tc := range map[string]struct {
		requests []PostgresCallableRequest
		value    postgresCallableBatchRow
	}{
		"valid function": {
			requests: functionRequest,
			value:    validFunction,
		},
		"valid operator": {
			requests: operatorRequest,
			value:    validOperator,
		},
	} {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := buildPostgresCallableResolutions(
				path, tc.requests, []postgresCallableBatchRow{tc.value},
			)
			if err != nil {
				t.Fatalf("buildPostgresCallableResolutions() error = %v", err)
			}
			if len(got) != 1 || !got[0].Comparable() || got[0].Unsafe() {
				t.Fatalf("resolution = %+v, want comparable pg_catalog evidence", got)
			}
			if len(got[0].Winners) != 1 ||
				got[0].Winners[0].ResultType != tc.value.Winners[0].ResultType {
				t.Fatalf(
					"winner result type = %+v, want preserved %q",
					got[0].Winners,
					tc.value.Winners[0].ResultType,
				)
			}
		})
	}
}

func TestResolvePostgresCallablesStablePayloadAndOrdering(t *testing.T) {
	path := validSearchPathForTest()
	requests := []PostgresCallableRequest{
		{Kind: PostgresCallableOperator, Name: "||"},
		{Kind: PostgresCallableFunction, Name: "zeta"},
		{Kind: PostgresCallableFunction, Name: "alpha"},
		{Kind: PostgresCallableOperator, Name: "@"},
		{Kind: PostgresCallableFunction, Name: "alpha"},
	}
	catalogJSON := `[
		{"kind":"operator","name":"||","winners":[
			{"schema":"deploy_user","signature":"text,text","oid":402,"result_type":"text","pg_catalog":false}
		]},
		{"kind":"function","name":"zeta","winners":[]},
		{"kind":"operator","name":"@","winners":[
			{"schema":"pg_catalog","signature":"integer,integer","oid":302,"result_type":"text","pg_catalog":true}
		]},
		{"kind":"function","name":"alpha","winners":[
			{"schema":"deploy_user","signature":"text","oid":202,"result_type":"text","pg_catalog":false},
			{"schema":"pg_catalog","signature":"integer","oid":102,"result_type":"text","pg_catalog":true}
		]}
	]`

	for _, purpose := range []Purpose{PurposeReview, PurposeExecution} {
		purpose := purpose
		t.Run(string(purpose), func(t *testing.T) {
			var querySQL string
			var queryPayload string
			var queryArgCount int
			state := &searchPathCoverageState{}
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						return driver.RowsAffected(0), nil
					},
					queryHook: func(
						_ context.Context, query string, args []driver.NamedValue,
					) (driver.Rows, error) {
						querySQL = query
						queryArgCount = len(args)
						if queryArgCount == 1 {
							queryPayload, _ = args[0].Value.(string)
						}
						return &searchPathCoverageRows{
							columns: []string{"callables"},
							values:  [][]driver.Value{{catalogJSON}},
						}, nil
					},
				}}, nil
			}, state)
			pool := newCoveragePurposePool(db, "postgresql", purpose)
			t.Cleanup(func() { _ = pool.Close() })

			got, err := ResolvePostgresCallables(
				context.Background(), pool, path, requests,
			)
			if err != nil {
				t.Fatalf("ResolvePostgresCallables() error = %v", err)
			}
			if querySQL != postgresCallableBatchLookupSQL {
				t.Fatal("catalog query did not use the fixed callable lookup SQL")
			}
			if queryArgCount != 1 {
				t.Fatalf("catalog query argument count = %d, want 1", queryArgCount)
			}
			if !state.containsExec(postgresCatalogSearchPath) {
				t.Fatalf("executed statements = %v, want %q", state.execSnapshot(), postgresCatalogSearchPath)
			}

			var input postgresCallableBatchInput
			if err := json.Unmarshal([]byte(queryPayload), &input); err != nil {
				t.Fatalf("catalog payload is not JSON: %v; %s", err, queryPayload)
			}
			if !stringSlicesEqual(input.Functions, []string{"alpha", "zeta"}) ||
				!stringSlicesEqual(input.Operators, []string{"@", "||"}) ||
				!stringSlicesEqual(input.Schemas, path.Effective) ||
				input.Role != path.CurrentUser {
				t.Fatalf("catalog payload identity drift = %+v", input)
			}

			wantRequests := []PostgresCallableRequest{
				{Kind: PostgresCallableFunction, Name: "alpha"},
				{Kind: PostgresCallableFunction, Name: "zeta"},
				{Kind: PostgresCallableOperator, Name: "@"},
				{Kind: PostgresCallableOperator, Name: "||"},
			}
			if len(got) != len(wantRequests) {
				t.Fatalf("resolution count = %d, want %d: %+v", len(got), len(wantRequests), got)
			}
			for i, wantRequest := range wantRequests {
				if got[i].Request != wantRequest {
					t.Fatalf("resolution[%d] request = %+v, want %+v", i, got[i].Request, wantRequest)
				}
			}
			if len(got[0].Winners) != 2 ||
				got[0].Winners[0].Signature != "integer" ||
				got[0].Winners[0].OID != 102 ||
				got[0].Winners[1].Signature != "text" ||
				got[0].Winners[1].OID != 202 {
				t.Fatalf("alpha winners are not deterministically ordered: %+v", got[0].Winners)
			}
			if !got[0].Comparable() || !got[0].Unsafe() {
				t.Fatalf("alpha evidence = %+v, want comparable and unsafe", got[0])
			}
			if got[1].Comparable() || got[1].Unsafe() || len(got[1].Winners) != 0 {
				t.Fatalf("zeta evidence = %+v, want empty winner evidence", got[1])
			}
			if !got[2].Comparable() || got[2].Unsafe() {
				t.Fatalf("@ evidence = %+v, want comparable and safe", got[2])
			}
			if !got[3].Comparable() || !got[3].Unsafe() {
				t.Fatalf("|| evidence = %+v, want comparable and unsafe", got[3])
			}
		})
	}
}

func postgresCallableRequestsEqual(
	got, want []PostgresCallableRequest,
) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
