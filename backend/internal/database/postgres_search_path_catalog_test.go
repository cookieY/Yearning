package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestExplainWithPostgresSearchPathValidation(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	pgAdapter := NewPostgreSQLAdapter(Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{})
	mysqlAdapter := NewMySQLAdapter(Endpoint{CompatibilityMod: "mysql"}, AdapterOptions{})
	reviewPool := &PurposePool{engine: "postgresql", purpose: PurposeReview}

	tests := map[string]struct {
		adapter      Adapter
		pool         *PurposePool
		path         PostgresSearchPath
		statementSQL string
		want         error
	}{
		"invalid snapshot": {
			adapter: pgAdapter, pool: reviewPool, path: PostgresSearchPath{},
			statementSQL: "SELECT 1", want: ErrPostgresSearchPathUnavailable,
		},
		"mysql adapter": {
			adapter: mysqlAdapter, pool: reviewPool, path: path,
			statementSQL: "SELECT 1", want: ErrUnsupportedTarget,
		},
		"wrong purpose": {
			adapter: pgAdapter,
			pool:    &PurposePool{engine: "postgresql", purpose: PurposeQuery},
			path:    path, statementSQL: "SELECT 1", want: ErrUnsupportedTarget,
		},
		"empty statement": {
			adapter: pgAdapter, pool: reviewPool, path: path,
			statementSQL: " \n\t ", want: ErrStatementRequired,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			explainer, ok := tc.adapter.(interface {
				ExplainWithPostgresSearchPath(
					context.Context,
					*PurposePool,
					PostgresSearchPath,
					string,
				) (*ExplainFact, error)
			})
			if !ok {
				t.Fatalf("adapter %T does not expose search-path Explain", tc.adapter)
			}
			fact, err := explainer.ExplainWithPostgresSearchPath(
				context.Background(), tc.pool, tc.path, tc.statementSQL,
			)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if fact != nil {
				t.Fatalf("fact = %+v, want nil", fact)
			}
		})
	}
}

func TestLookupPostgresRelationsValidation(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	tests := map[string]struct {
		pool      *PurposePool
		path      PostgresSearchPath
		relations []string
		want      error
	}{
		"nil pool": {
			path: path, relations: []string{"orders"}, want: ErrPostgresSearchPathUnavailable,
		},
		"wrong engine": {
			pool: &PurposePool{engine: "mysql", purpose: PurposeReview},
			path: path, relations: []string{"orders"}, want: ErrPostgresSearchPathUnavailable,
		},
		"wrong purpose": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeQuery},
			path: path, relations: []string{"orders"}, want: ErrPostgresSearchPathUnavailable,
		},
		"invalid snapshot": {
			pool:      &PurposePool{engine: "postgresql", purpose: PurposeReview},
			relations: []string{"orders"}, want: ErrPostgresSearchPathUnavailable,
		},
		"empty relations": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path: path, want: ErrPostgresRelationUnresolved,
		},
		"too many relations": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path: path, relations: makeCoveragePath(maxPostgresRelationLookups + 1),
			want: ErrPostgresRelationUnresolved,
		},
		"invalid relation": {
			pool: &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path: path, relations: []string{`orders"`}, want: ErrPostgresRelationUnresolved,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := LookupPostgresRelations(
				context.Background(), tc.pool, tc.path, tc.relations,
			)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got != nil {
				t.Fatalf("lookups = %+v, want nil", got)
			}
		})
	}
}

func TestLookupPostgresRelationsCatalogBoundaries(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	columns := []string{
		"relation", "all_schemas_exist", "all_schemas_visible",
		"present_by_schema",
	}
	tests := map[string]struct {
		execErr     error
		queryErr    error
		rows        [][]driver.Value
		tailErr     error
		relations   []string
		wantErr     error
		wantPresent map[string]PostgresRelationKind
	}{
		"valid table": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"public":{"kind":"r","oid":101}}`)},
			},
			wantPresent: map[string]PostgresRelationKind{
				"public": PostgresRelationTable,
			},
		},
		"valid absent": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{}`)},
			},
			wantPresent: map[string]PostgresRelationKind{},
		},
		"duplicate requested relation": {
			relations: []string{"orders", "orders"},
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"public":{"kind":"r","oid":101}}`)},
			},
			wantPresent: map[string]PostgresRelationKind{
				"public": PostgresRelationTable,
			},
		},
		"catalog search path failure": {
			execErr: errors.New("set path failed"),
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"catalog query failure": {
			queryErr: errors.New("query failed"),
			wantErr:  ErrPostgresSearchPathUnavailable,
		},
		"row scan failure": {
			rows: [][]driver.Value{
				{"orders", "not-bool", true, []byte(`{}`)},
			},
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"unknown relation": {
			rows: [][]driver.Value{
				{"other", true, true, []byte(`{"public":{"kind":"r","oid":101}}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"schema outside frozen path": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"elsewhere":{"kind":"r","oid":101}}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"duplicate relation row": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"public":{"kind":"r","oid":101}}`)},
				{"orders", true, true, []byte(`{"public":{"kind":"r","oid":101}}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"schema absent": {
			rows: [][]driver.Value{
				{"orders", false, false, []byte(`{}`)},
			},
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"schema usage absent": {
			rows: [][]driver.Value{
				{"orders", true, false, []byte(`{}`)},
			},
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"malformed presence": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`[`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"empty relation kind": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"public":{"kind":"","oid":101}}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"legacy relation string without oid": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"public":"r"}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"zero relation oid": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{"public":{"kind":"r","oid":0}}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"rows terminal failure": {
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{}`)},
			},
			tailErr: errors.New("stream failed"),
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"incomplete result": {
			relations: []string{"orders", "customers"},
			rows: [][]driver.Value{
				{"orders", true, true, []byte(`{}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						if tc.execErr != nil {
							return nil, tc.execErr
						}
						return driver.RowsAffected(0), nil
					},
					queryHook: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
						if tc.queryErr != nil {
							return nil, tc.queryErr
						}
						return &searchPathCoverageRows{
							columns: columns,
							values:  tc.rows,
							tailErr: tc.tailErr,
						}, nil
					},
				}}, nil
			}, nil)
			t.Cleanup(func() { _ = db.Close() })
			pool := newCoveragePurposePool(db, "postgresql", PurposeReview)
			relations := tc.relations
			if relations == nil {
				relations = []string{"orders"}
			}

			got, err := LookupPostgresRelations(
				context.Background(), pool, path, relations,
			)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("lookups = %+v, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LookupPostgresRelations() error = %v", err)
			}
			if len(got) != 1 || got[0].Relation != "orders" {
				t.Fatalf("lookups = %+v, want one orders lookup", got)
			}
			if !relationKindMapsEqual(got[0].PresentBySchema, tc.wantPresent) {
				t.Fatalf("present = %v, want %v", got[0].PresentBySchema, tc.wantPresent)
			}
			if len(tc.wantPresent) > 0 && got[0].OIDBySchema["public"] == 0 {
				t.Fatalf("present relation lost its catalog OID: %+v", got[0])
			}
		})
	}
}

func TestLookupPostgresRelationsCatalogAggregatesOneRowPerRelation(t *testing.T) {
	t.Parallel()

	columns := []string{
		"relation", "all_schemas_exist", "all_schemas_visible",
		"present_by_schema",
	}
	db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
		return &searchPathCoverageConn{session: &searchPathCoverageSession{
			execHook: func(context.Context, string) (driver.Result, error) {
				return driver.RowsAffected(0), nil
			},
			queryHook: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				return &searchPathCoverageRows{
					columns: columns,
					values: [][]driver.Value{
						{"customers", true, true, []byte(`{"public":{"kind":"v","oid":102}}`)},
						{"orders", true, true, []byte(`{"deploy_user":{"kind":"r","oid":201},"public":{"kind":"r","oid":202}}`)},
					},
				}, nil
			},
		}}, nil
	}, nil)
	t.Cleanup(func() { _ = db.Close() })

	got, err := LookupPostgresRelations(
		context.Background(),
		newCoveragePurposePool(db, "postgresql", PurposeReview),
		validSearchPathForTest(),
		[]string{"orders", "customers"},
	)
	if err != nil {
		t.Fatalf("LookupPostgresRelations() error = %v", err)
	}
	if len(got) != 2 ||
		got[0].Relation != "orders" ||
		got[1].Relation != "customers" {
		t.Fatalf("catalog aggregation order = %+v, want orders then customers", got)
	}
	if !relationKindMapsEqual(got[0].PresentBySchema, map[string]PostgresRelationKind{
		"deploy_user": PostgresRelationTable,
		"public":      PostgresRelationTable,
	}) {
		t.Fatalf("orders presence = %+v", got[0].PresentBySchema)
	}
	if got[0].OIDBySchema["deploy_user"] != 201 ||
		got[0].OIDBySchema["public"] != 202 ||
		got[1].OIDBySchema["public"] != 102 {
		t.Fatalf("catalog OIDs = orders:%v customers:%v",
			got[0].OIDBySchema, got[1].OIDBySchema)
	}
	if !relationKindMapsEqual(got[1].PresentBySchema, map[string]PostgresRelationKind{
		"public": PostgresRelationView,
	}) {
		t.Fatalf("customers presence = %+v", got[1].PresentBySchema)
	}
}

func TestInspectPostgresQueryerRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()
	path := validSearchPathForTest()
	tests := map[string]struct {
		path       PostgresSearchPath
		relations  []string
		probeUser  string
		execFailAt int
		queryErr   error
		catalog    [][]driver.Value
		wantErr    error
		wantLookup bool
	}{
		"empty relations": {
			path: path,
		},
		"too many relations": {
			path:      path,
			relations: makeCoveragePath(maxPostgresRelationLookups + 1),
			wantErr:   ErrPostgresRelationUnresolved,
		},
		"invalid relation": {
			path: path, relations: []string{`orders"`},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"catalog set failure": {
			path: path, relations: []string{"orders"}, execFailAt: 1,
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"probe identity changed": {
			path: path, relations: []string{"orders"}, probeUser: "other",
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"catalog query failure": {
			path: path, relations: []string{"orders"},
			queryErr: errors.New("catalog query failed"),
			wantErr:  ErrPostgresSearchPathUnavailable,
		},
		"catalog row malformed": {
			path: path, relations: []string{"orders"},
			catalog: [][]driver.Value{
				{"orders", "not-bool", true, []byte(`{}`)},
			},
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"catalog row unresolved": {
			path: path, relations: []string{"orders"},
			catalog: [][]driver.Value{
				{"orders", true, true, []byte(`{"missing":{"kind":"r","oid":1}}`)},
			},
			wantErr: ErrPostgresRelationUnresolved,
		},
		"restore failure": {
			path: path, relations: []string{"orders"}, execFailAt: 2,
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"valid duplicate relation": {
			path: path, relations: []string{"orders", "orders"},
			wantLookup: true,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				execCalls := 0
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						execCalls++
						if tc.execFailAt == execCalls {
							return nil, errors.New("injected exec failure")
						}
						return driver.RowsAffected(0), nil
					},
					queryHook: func(
						_ context.Context, query string, _ []driver.NamedValue,
					) (driver.Rows, error) {
						if tc.queryErr != nil {
							return nil, tc.queryErr
						}
						if strings.Contains(query, "relation_names") {
							rows := [][]driver.Value{{
								"orders", true, true,
								[]byte(`{"public":{"kind":"r","oid":101}}`),
							}}
							if tc.catalog != nil {
								rows = tc.catalog
							}
							return &searchPathCoverageRows{
								columns: []string{
									"relation", "all_schemas_exist",
									"all_schemas_visible", "present_by_schema",
								},
								values: rows,
							}, nil
						}
						currentUser := tc.probeUser
						if currentUser == "" {
							currentUser = path.CurrentUser
						}
						return &searchPathCoverageRows{
							columns: []string{
								"current_user", "configured", "explicit",
								"effective", "creation",
							},
							values: [][]driver.Value{
								{
									currentUser,
									path.Configured,
									`["deploy_user","public"]`,
									`["pg_catalog","deploy_user","public"]`,
									path.CreationSchema,
								},
							},
						}, nil
					},
				}}, nil
			}, nil)
			t.Cleanup(func() { _ = db.Close() })
			tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tx.Rollback() })

			snapshot, lookups, _, err := inspectPostgresQueryer(
				context.Background(), tx, tc.path, tc.relations, nil,
			)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("inspectPostgresQueryer() error = %v", err)
			}
			if !tc.path.Equal(snapshot) {
				t.Fatalf("snapshot = %+v, want %+v", snapshot, tc.path)
			}
			if tc.wantLookup &&
				(len(lookups) != 1 || lookups[0].Relation != "orders") {
				t.Fatalf("lookups = %+v, want one de-duplicated orders", lookups)
			}
		})
	}
}

func TestInspectPostgresTransactionRequiresExactOpenHandle(t *testing.T) {
	t.Parallel()
	path := validSearchPathForTest()
	pool := &PurposePool{engine: "postgresql"}
	db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
		return &searchPathCoverageConn{session: &searchPathCoverageSession{
			queryHook: func(
				context.Context, string, []driver.NamedValue,
			) (driver.Rows, error) {
				return &searchPathCoverageRows{
					columns: []string{
						"current_user", "configured", "explicit",
						"effective", "creation",
					},
					values: [][]driver.Value{{
						path.CurrentUser, path.Configured,
						`["deploy_user","public"]`,
						`["pg_catalog","deploy_user","public"]`,
						path.CreationSchema,
					}},
				}, nil
			},
		}}, nil
	}, nil)
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	for name, value := range map[string]DMLTransaction{
		"closed":            &dmlTx{open: false, pool: pool, tx: tx},
		"mismatched pool":   &dmlTx{open: true, pool: &PurposePool{engine: "postgresql"}, tx: tx},
		"non-postgres pool": &dmlTx{open: true, pool: &PurposePool{engine: "mysql"}, tx: tx},
	} {
		if _, _, _, err := InspectPostgresDMLTransaction(
			context.Background(),
			func() *PurposePool {
				if name == "non-postgres pool" {
					return value.(*dmlTx).pool
				}
				return pool
			}(),
			value,
			path,
			nil,
			nil,
		); !errors.Is(err, ErrPostgresSearchPathUnavailable) {
			t.Fatalf("%s error = %v, want unavailable", name, err)
		}
	}
	snapshot, lookups, _, err := InspectPostgresDMLTransaction(
		context.Background(), pool, &dmlTx{open: true, pool: pool, tx: tx},
		path, nil, nil,
	)
	if err != nil || !path.Equal(snapshot) || len(lookups) != 0 {
		t.Fatalf("valid handle snapshot=%+v lookups=%+v err=%v", snapshot, lookups, err)
	}
}

func TestInspectPostgresDDLConnectionRejectsUnavailableTransaction(t *testing.T) {
	t.Parallel()
	path := validSearchPathForTest()
	if _, _, _, err := InspectPostgresDDLConnection(
		context.Background(), nil, path, nil, nil,
	); !errors.Is(err, ErrPostgresSearchPathUnavailable) {
		t.Fatalf("nil connection error = %v, want unavailable", err)
	}

	db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
		return &searchPathCoverageConn{session: &searchPathCoverageSession{
			beginErr: errors.New("begin failed"),
		}}, nil
	}, nil)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, _, _, err := InspectPostgresDDLConnection(
		context.Background(), conn, path, nil, nil,
	); !errors.Is(err, ErrPostgresSearchPathUnavailable) {
		t.Fatalf("begin error = %v, want unavailable", err)
	}
}

func TestExplainPostgresWithSearchPathValidation(t *testing.T) {
	t.Parallel()

	pgAdapter := NewPostgreSQLAdapter(Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{})
	mysqlAdapter := NewMySQLAdapter(Endpoint{CompatibilityMod: "mysql"}, AdapterOptions{})
	reviewPool := &PurposePool{engine: "postgresql", purpose: PurposeReview}
	tests := map[string]struct {
		adapter      Adapter
		pool         *PurposePool
		path         []string
		statementSQL string
		want         error
		wantText     string
	}{
		"nil adapter": {
			pool: reviewPool, path: []string{"public"}, statementSQL: "SELECT 1",
			want: ErrUnsupportedTarget,
		},
		"mysql adapter": {
			adapter: mysqlAdapter, pool: reviewPool, path: []string{"public"},
			statementSQL: "SELECT 1", want: ErrUnsupportedTarget,
		},
		"nil pool": {
			adapter: pgAdapter, path: []string{"public"}, statementSQL: "SELECT 1",
			want: ErrUnsupportedTarget,
		},
		"wrong pool engine": {
			adapter: pgAdapter,
			pool:    &PurposePool{engine: "mysql", purpose: PurposeReview},
			path:    []string{"public"}, statementSQL: "SELECT 1",
			want: ErrUnsupportedTarget,
		},
		"wrong purpose": {
			adapter: pgAdapter,
			pool:    &PurposePool{engine: "postgresql", purpose: PurposeQuery},
			path:    []string{"public"}, statementSQL: "SELECT 1",
			want: ErrUnsupportedTarget,
		},
		"invalid path": {
			adapter: pgAdapter, pool: reviewPool, path: nil, statementSQL: "SELECT 1",
			want: ErrPostgresSearchPathInvalid,
		},
		"empty statement": {
			adapter: pgAdapter, pool: reviewPool, path: []string{"public"},
			want: ErrStatementRequired,
		},
		"statement head rejected": {
			adapter: pgAdapter, pool: reviewPool, path: []string{"public"},
			statementSQL: "CREATE TABLE t (id int)",
			wantText:     "explain statement rejected",
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fact, err := ExplainPostgresWithSearchPath(
				context.Background(), tc.adapter, tc.pool, tc.path, tc.statementSQL,
			)
			if tc.wantText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("error = %v, want text %q", err, tc.wantText)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if fact != nil {
				t.Fatalf("fact = %+v, want nil", fact)
			}
		})
	}
}

func TestExplainPostgresWithSearchPathExecutionBoundaries(t *testing.T) {
	t.Parallel()

	pgAdapter := NewPostgreSQLAdapter(Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{})
	tests := map[string]struct {
		execErr    error
		queryErr   error
		values     [][]driver.Value
		tailErr    error
		wantText   string
		wantFact   bool
		wantMethod string
		wantRows   int64
		wantCost   float64
		wantIndex  string
	}{
		"path apply failure": {
			execErr:  errors.New("set failed"),
			wantText: "schema context apply failed",
		},
		"explain rejected": {
			queryErr: errors.New("query failed"),
			wantText: "explain rejected",
		},
		"invalid JSON": {
			values:   [][]driver.Value{{"not-json"}},
			wantText: "explain output unparsable",
		},
		"rows failure": {
			values: [][]driver.Value{
				{`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":1,"Total Cost":1}}]`},
			},
			tailErr:  errors.New("stream failed"),
			wantText: "explain output unreadable",
		},
		"valid plan": {
			values: [][]driver.Value{
				{`[{"Plan":{"Node Type":"Index Scan","Plan Rows":12,"Total Cost":3.5,"Index Name":"idx_orders"}}]`},
			},
			wantFact:   true,
			wantMethod: "Index Scan",
			wantRows:   12,
			wantCost:   3.5,
			wantIndex:  "idx_orders",
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						if tc.execErr != nil {
							return nil, tc.execErr
						}
						return driver.RowsAffected(0), nil
					},
					queryHook: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
						if tc.queryErr != nil {
							return nil, tc.queryErr
						}
						return &searchPathCoverageRows{
							columns: []string{"QUERY PLAN"},
							values:  tc.values,
							tailErr: tc.tailErr,
						}, nil
					},
				}}, nil
			}, nil)
			t.Cleanup(func() { _ = db.Close() })
			pool := newCoveragePurposePool(db, "postgresql", PurposeReview)

			fact, err := ExplainPostgresWithSearchPath(
				context.Background(), pgAdapter, pool, []string{"deploy_user", "public"},
				"SELECT * FROM orders",
			)
			if !tc.wantFact {
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("error = %v, want text %q", err, tc.wantText)
				}
				if fact != nil {
					t.Fatalf("fact = %+v, want nil", fact)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExplainPostgresWithSearchPath() error = %v", err)
			}
			if fact == nil || fact.AccessMethod != tc.wantMethod ||
				fact.EstimatedRows != tc.wantRows || fact.EstimatedCost != tc.wantCost ||
				len(fact.IndexNames) != 1 || fact.IndexNames[0] != tc.wantIndex {
				t.Fatalf("fact = %+v, want parsed index plan", fact)
			}
		})
	}
}

func relationKindMapsEqual(
	left, right map[string]PostgresRelationKind,
) bool {
	if len(left) != len(right) {
		return false
	}
	for schema, kind := range left {
		if right[schema] != kind {
			return false
		}
	}
	return true
}
