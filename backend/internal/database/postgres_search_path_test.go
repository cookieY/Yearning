package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var _ searchPathDDLExecutor = (*postgresAdapter)(nil)

func TestPostgresSearchPathValidate(t *testing.T) {
	t.Parallel()

	valid := PostgresSearchPath{
		SchemaVersion:  PostgresSearchPathVersion,
		CurrentUser:    "deploy_user",
		Configured:     `"$user", public`,
		Explicit:       []string{"deploy_user", "public"},
		Effective:      []string{"pg_catalog", "deploy_user", "public"},
		CreationSchema: "deploy_user",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	tests := map[string]func(*PostgresSearchPath){
		"version": func(p *PostgresSearchPath) {
			p.SchemaVersion = 0
		},
		"empty user": func(p *PostgresSearchPath) {
			p.CurrentUser = ""
		},
		"configured oversized": func(p *PostgresSearchPath) {
			p.Configured = string(make([]byte, maxPostgresSearchPathSettingsBytes+1))
		},
		"empty explicit": func(p *PostgresSearchPath) {
			p.Explicit = nil
		},
		"empty effective": func(p *PostgresSearchPath) {
			p.Effective = nil
		},
		"invalid explicit identifier": func(p *PostgresSearchPath) {
			p.Explicit = []string{`unsafe"name`}
		},
		"duplicate explicit": func(p *PostgresSearchPath) {
			p.Explicit = []string{"public", "public"}
		},
		"invalid creation": func(p *PostgresSearchPath) {
			p.CreationSchema = ""
		},
		"creation is not first explicit member": func(p *PostgresSearchPath) {
			p.CreationSchema = "public"
		},
		"implicit catalog is not first": func(p *PostgresSearchPath) {
			p.Effective = []string{"deploy_user", "public", "pg_catalog"}
		},
		"explicit catalog position changed": func(p *PostgresSearchPath) {
			p.Explicit = []string{"deploy_user", "pg_catalog", "public"}
			p.Effective = []string{"pg_catalog", "deploy_user", "public"}
		},
		"temporary effective": func(p *PostgresSearchPath) {
			p.Effective = append(p.Effective, "pg_temp_3")
		},
		"too many effective": func(p *PostgresSearchPath) {
			p.Effective = make([]string, maxPostgresSearchPathSchemas+1)
			for i := range p.Effective {
				p.Effective[i] = "s" + string(rune('a'+i%26))
			}
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := valid
			got.Explicit = append([]string(nil), valid.Explicit...)
			got.Effective = append([]string(nil), valid.Effective...)
			mutate(&got)
			if err := got.Validate(); !errors.Is(err, ErrPostgresSearchPathInvalid) {
				t.Fatalf("Validate error = %v, want ErrPostgresSearchPathInvalid", err)
			}
		})
	}
}

func TestPostgresSearchPathEqualUsesResolutionSemantics(t *testing.T) {
	t.Parallel()

	base := PostgresSearchPath{
		SchemaVersion:  PostgresSearchPathVersion,
		CurrentUser:    "app",
		Configured:     `"$user", public`,
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
	same := base
	same.Configured = `app, public`
	same.Explicit = append([]string(nil), base.Explicit...)
	same.Effective = append([]string(nil), base.Effective...)
	if !base.Equal(same) {
		t.Fatal("equivalent resolved path must compare equal despite configured text spelling")
	}

	differentPath := same
	differentPath.Explicit = []string{"public", "app"}
	differentPath.Effective = []string{"pg_catalog", "public", "app"}
	differentPath.CreationSchema = "public"
	if base.Equal(differentPath) {
		t.Fatal("ordered path drift must compare unequal")
	}
	otherUser := same
	otherUser.CurrentUser = "other"
	if base.Equal(otherUser) {
		t.Fatal("execution role drift must compare unequal")
	}
	if !base.Matches(same) {
		t.Fatal("Matches must preserve Equal semantics")
	}
	invalid := base
	invalid.SchemaVersion = 0
	if invalid.Equal(invalid) || invalid.Matches(validSearchPathForTest()) {
		t.Fatal("invalid snapshots must never compare equal")
	}
}

func TestStatementNotSentErrorContract(t *testing.T) {
	t.Parallel()

	var nilError *StatementNotSentError
	if got := nilError.Error(); got != ErrStatementNotSent.Error() {
		t.Fatalf("nil Error() = %q, want %q", got, ErrStatementNotSent.Error())
	}
	empty := &StatementNotSentError{}
	if got := empty.Error(); got != ErrStatementNotSent.Error() {
		t.Fatalf("empty Error() = %q, want %q", got, ErrStatementNotSent.Error())
	}
	named := &StatementNotSentError{FailureName: "before_send_failed"}
	if got, want := named.Error(), ErrStatementNotSent.Error()+": before_send_failed"; got != want {
		t.Fatalf("named Error() = %q, want %q", got, want)
	}
	if got := named.Unwrap(); got != ErrStatementNotSent {
		t.Fatalf("Unwrap() = %v, want ErrStatementNotSent", got)
	}
	if !errors.Is(named, ErrStatementNotSent) {
		t.Fatal("StatementNotSentError must unwrap to ErrStatementNotSent")
	}
}

func TestValidSearchPathIdentityNameBoundaries(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value string
		want  bool
	}{
		"ordinary":             {value: "deploy user", want: true},
		"maximum bytes":        {value: strings.Repeat("a", maxPostgresSearchPathIdentityNameBytes), want: true},
		"empty":                {value: "", want: false},
		"oversized":            {value: strings.Repeat("a", maxPostgresSearchPathIdentityNameBytes+1), want: false},
		"invalid utf8":         {value: string([]byte{0xff}), want: false},
		"nul":                  {value: "app\x00name", want: false},
		"line feed":            {value: "app\nname", want: false},
		"carriage return":      {value: "app\rname", want: false},
		"embedded punctuation": {value: `app"name`, want: true},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := validSearchPathIdentityName(tc.value); got != tc.want {
				t.Fatalf("validSearchPathIdentityName() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPostgresSearchPathSQLQuotesEachMember(t *testing.T) {
	t.Parallel()

	got, err := postgresSearchPathSQL([]string{"a_1", "public"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := `SET LOCAL search_path = "a_1", "public"`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if _, err := postgresSearchPathSQL(nil, true); !errors.Is(err, ErrPostgresSearchPathInvalid) {
		t.Fatalf("empty path error = %v", err)
	}
	if _, err := postgresSearchPathSQL([]string{"public", "public"}, false); !errors.Is(err, ErrPostgresSearchPathInvalid) {
		t.Fatalf("duplicate path error = %v", err)
	}
}

func TestPostgresSearchPathProbeSQLQualifiesCatalogFunctions(t *testing.T) {
	t.Parallel()

	unqualified := regexp.MustCompile(
		`(^|[^[:alnum:]_.])(array_to_json|current_schema|current_schemas|current_setting|` +
			`has_schema_privilege|jsonb_array_elements_text|jsonb_extract_path)[[:space:]]*\(`,
	)
	for name, statement := range map[string]string{
		"probe":          postgresSearchPathProbeSQL,
		"relation batch": postgresRelationBatchLookupSQL,
	} {
		if got := unqualified.FindString(statement); got != "" {
			t.Fatalf("%s SQL contains search_path-resolved function call %q", name, got)
		}
		if regexp.MustCompile(`::text\b`).MatchString(statement) {
			t.Fatalf("%s SQL contains search_path-resolved text type", name)
		}
	}
	for _, required := range []string{
		"pg_catalog.array_to_json(",
		"pg_catalog.current_schema()",
		"pg_catalog.current_schemas(false)",
		"pg_catalog.current_schemas(true)",
		"pg_catalog.current_setting(",
		"::pg_catalog.text",
	} {
		if !strings.Contains(postgresSearchPathProbeSQL, required) {
			t.Fatalf("probe SQL is missing fixed catalog reference %q", required)
		}
	}
}

func TestPostgresSearchPathSQLModeAndValidation(t *testing.T) {
	t.Parallel()

	connection, err := postgresSearchPathSQL([]string{"app", "public"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := `SET search_path = "app", "public"`; connection != want {
		t.Fatalf("connection SQL = %q, want %q", connection, want)
	}

	tests := map[string]struct {
		path []string
	}{
		"empty":     {path: nil},
		"temporary": {path: []string{"pg_temp"}},
		"duplicate": {path: []string{"app", "app"}},
		"too many":  {path: makeCoveragePath(maxPostgresSearchPathSchemas + 1)},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := postgresSearchPathSQL(tc.path, false); !errors.Is(err, ErrPostgresSearchPathInvalid) {
				t.Fatalf("error = %v, want ErrPostgresSearchPathInvalid", err)
			}
		})
	}
}

func TestUniqueStringsPreservingOrderBoundaries(t *testing.T) {
	t.Parallel()

	if got := uniqueStringsPreservingOrder(nil); got != nil {
		t.Fatalf("nil input = %#v, want nil", got)
	}
	got := uniqueStringsPreservingOrder([]string{"public", "app", "public", "app"})
	if want := []string{"public", "app"}; !stringSlicesEqual(got, want) {
		t.Fatalf("deduplicated path = %v, want %v", got, want)
	}
	if stringSlicesEqual([]string{"app"}, []string{"public"}) {
		t.Fatal("same-length different paths must not compare equal")
	}
	if stringSlicesEqual([]string{"app"}, []string{"app", "public"}) {
		t.Fatal("different-length paths must not compare equal")
	}
}

func TestProbePostgresSearchPathValidationAndDecodeBranches(t *testing.T) {
	t.Parallel()

	if _, err := ProbePostgresSearchPath(context.Background(), nil); !errors.Is(err, ErrPostgresSearchPathUnavailable) {
		t.Fatalf("nil pool error = %v, want unavailable", err)
	}
	if _, err := ProbePostgresSearchPath(context.Background(), &PurposePool{
		engine: "mysql", purpose: PurposeExecution,
	}); !errors.Is(err, ErrPostgresSearchPathUnavailable) {
		t.Fatalf("mysql pool error = %v, want unavailable", err)
	}
	if _, err := ProbePostgresSearchPath(context.Background(), &PurposePool{
		engine: "postgresql", purpose: PurposeReview,
	}); !errors.Is(err, ErrPostgresSearchPathUnavailable) {
		t.Fatalf("review pool error = %v, want unavailable", err)
	}

	tests := map[string]struct {
		execErr   error
		queryErr  error
		values    []driver.Value
		terminal  error
		wantValid bool
	}{
		"reset failure": {
			execErr: errors.New("reset failed"),
		},
		"query failure": {
			queryErr: errors.New("query failed"),
		},
		"invalid explicit JSON": {
			values: []driver.Value{
				"deploy_user", `"$user", public`, `not-json`,
				`["pg_catalog","deploy_user","public"]`, "deploy_user",
			},
		},
		"invalid effective JSON": {
			values: []driver.Value{
				"deploy_user", `"$user", public`, `["deploy_user","public"]`,
				`not-json`, "deploy_user",
			},
		},
		"missing creation schema": {
			values: []driver.Value{
				"deploy_user", `"$user", public`, `["deploy_user","public"]`,
				`["pg_catalog","deploy_user","public"]`, nil,
			},
		},
		"invalid snapshot": {
			values: []driver.Value{
				"", `"$user", public`, `["deploy_user","public"]`,
				`["pg_catalog","deploy_user","public"]`, "deploy_user",
			},
		},
		"temporary effective schema": {
			values: []driver.Value{
				"deploy_user", `"$user", public`, `["deploy_user","public"]`,
				`["pg_catalog","pg_temp_2","deploy_user","public"]`, "deploy_user",
			},
		},
		"rows error": {
			terminal: errors.New("rows failed"),
		},
		"deduplicated valid snapshot": {
			values: []driver.Value{
				"deploy_user", `"$user", public`,
				`["deploy_user","deploy_user","public"]`,
				`["pg_catalog","deploy_user","deploy_user","public"]`,
				"deploy_user",
			},
			wantValid: true,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := newCoverageFakeDB(
				func(context.Context) (driver.Conn, error) {
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
								columns: []string{
									"current_user", "configured", "explicit",
									"effective", "creation",
								},
								values:  [][]driver.Value{tc.values},
								tailErr: tc.terminal,
							}, nil
						},
					}}, nil
				},
				nil,
			)
			t.Cleanup(func() { _ = db.Close() })
			pool := newCoveragePurposePool(db, "postgresql", PurposeExecution)

			got, err := ProbePostgresSearchPath(context.Background(), pool)
			if !tc.wantValid {
				if !errors.Is(err, ErrPostgresSearchPathUnavailable) {
					t.Fatalf("error = %v, want unavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ProbePostgresSearchPath() error = %v", err)
			}
			if !got.Matches(validSearchPathForTest()) {
				t.Fatalf("snapshot = %+v, want valid normalized snapshot", got)
			}
		})
	}
}

func TestExecuteDDLWithSearchPathBeforeSendReportsProvablePreSendFailures(t *testing.T) {
	t.Parallel()

	adapter := NewPostgreSQLAdapter(
		Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{},
	)
	if _, ok := adapter.(searchPathDDLExecutor); !ok {
		t.Fatalf("adapter type %T does not implement searchPathDDLExecutor", adapter)
	}
	invalid := &PurposePool{
		engine: "postgresql", purpose: PurposeExecution,
	}
	called := false
	_, err := ExecuteDDLWithSearchPathBeforeSend(
		context.Background(), adapter, invalid, []string{`unsafe"name`},
		"CREATE TABLE should_not_run (id integer)",
		func(context.Context) error {
			called = true
			return nil
		},
	)
	var notSent *StatementNotSentError
	if !errors.As(err, &notSent) || notSent.FailureName != "schema_context_invalid" {
		t.Fatalf("invalid path error = %v, want schema_context_invalid not-sent", err)
	}
	if called {
		t.Fatal("beforeSend ran after path validation failed")
	}

	db, openErr := sql.Open("pgx", "host=127.0.0.1 port=1 user=nobody dbname=nobody")
	if openErr != nil {
		t.Fatal(openErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	closedPool := &PurposePool{
		db: db, engine: "postgresql", purpose: PurposeExecution,
		execCancels: map[string]context.CancelFunc{}, execBackends: map[string]int64{},
	}
	_, err = ExecuteDDLWithSearchPathBeforeSend(
		context.Background(), adapter, closedPool, []string{"public"},
		"CREATE TABLE should_not_run (id integer)",
		func(context.Context) error {
			called = true
			return nil
		},
	)
	if !errors.As(err, &notSent) || notSent.FailureName != "connection_open_failed" {
		t.Fatalf("closed pool error = %v, want connection_open_failed not-sent", err)
	}
	if called {
		t.Fatal("beforeSend ran after connection initialization failed")
	}
}

func TestResolvePostgresRelationValidationAndSelection(t *testing.T) {
	t.Parallel()

	path := validSearchPathForTest()
	for name, tc := range map[string]struct {
		pool     *PurposePool
		path     PostgresSearchPath
		relation string
		want     error
	}{
		"nil pool": {
			path: path, relation: "orders", want: ErrPostgresSearchPathUnavailable,
		},
		"wrong engine": {
			pool:     &PurposePool{engine: "mysql", purpose: PurposeReview},
			path:     path,
			relation: "orders",
			want:     ErrPostgresSearchPathUnavailable,
		},
		"wrong purpose": {
			pool:     &PurposePool{engine: "postgresql", purpose: PurposeQuery},
			path:     path,
			relation: "orders",
			want:     ErrPostgresSearchPathUnavailable,
		},
		"invalid snapshot": {
			pool:     &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path:     PostgresSearchPath{},
			relation: "orders",
			want:     ErrPostgresSearchPathUnavailable,
		},
		"invalid relation": {
			pool:     &PurposePool{engine: "postgresql", purpose: PurposeReview},
			path:     path,
			relation: `orders"`,
			want:     ErrPostgresRelationUnresolved,
		},
	} {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := ResolvePostgresRelation(context.Background(), tc.pool, tc.path, tc.relation)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
		return &searchPathCoverageConn{session: &searchPathCoverageSession{
			execHook: func(context.Context, string) (driver.Result, error) {
				return driver.RowsAffected(0), nil
			},
			queryHook: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
				schema, _ := args[0].Value.(string)
				var relkind driver.Value
				if schema == "deploy_user" {
					relkind = "r"
				}
				return &searchPathCoverageRows{
					columns: []string{"exists", "usage", "relkind"},
					values:  [][]driver.Value{{true, true, relkind}},
				}, nil
			},
		}}, nil
	}, nil)
	t.Cleanup(func() { _ = db.Close() })
	pool := newCoveragePurposePool(db, "postgresql", PurposeReview)

	got, found, err := ResolvePostgresRelation(context.Background(), pool, path, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.Schema != "deploy_user" || got.Name != "orders" || got.Kind != PostgresRelationTable {
		t.Fatalf("relation = %+v found=%v, want deploy_user.orders table", got, found)
	}

	tests := map[string]struct {
		row     []driver.Value
		rowErr  error
		wantErr error
		found   bool
	}{
		"query failure": {
			rowErr: errors.New("catalog query failed"), wantErr: ErrPostgresSearchPathUnavailable,
		},
		"schema absent": {
			row:     []driver.Value{false, false, nil},
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"schema usage absent": {
			row:     []driver.Value{true, false, nil},
			wantErr: ErrPostgresSearchPathUnavailable,
		},
		"absent relation": {
			row: []driver.Value{true, true, nil},
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						return driver.RowsAffected(0), nil
					},
					queryHook: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
						if tc.rowErr != nil {
							return nil, tc.rowErr
						}
						return &searchPathCoverageRows{
							columns: []string{"exists", "usage", "relkind"},
							values:  [][]driver.Value{tc.row},
						}, nil
					},
				}}, nil
			}, nil)
			t.Cleanup(func() { _ = db.Close() })
			pool := newCoveragePurposePool(db, "postgresql", PurposeReview)

			_, found, err := ResolvePostgresRelation(
				context.Background(), pool, path, "orders",
			)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || found {
					t.Fatalf("error = %v found=%v, want %v and false", err, found, tc.wantErr)
				}
				return
			}
			if err != nil || found != tc.found {
				t.Fatalf("found = %v error = %v, want found=%v nil error", found, err, tc.found)
			}
		})
	}
}

func TestApplyTxSearchPathValidationAndExecution(t *testing.T) {
	t.Parallel()

	if err := ApplyTxSearchPath(context.Background(), nil, nil, []string{"public"}); !errors.Is(err, ErrTransactionRequired) {
		t.Fatalf("nil transaction error = %v, want ErrTransactionRequired", err)
	}
	if err := ApplyTxSearchPath(context.Background(), nil, &dmlTx{}, []string{"public"}); !errors.Is(err, ErrTransactionRequired) {
		t.Fatalf("closed transaction error = %v, want ErrTransactionRequired", err)
	}
	openWithPool := func(pool *PurposePool) *dmlTx {
		return &dmlTx{pool: pool, open: true, execCtx: context.Background()}
	}
	pgPool := &PurposePool{engine: "postgresql"}
	if err := ApplyTxSearchPath(context.Background(), nil, openWithPool(pgPool), []string{"public"}); !errors.Is(err, ErrPostgresSearchPathInvalid) {
		t.Fatalf("nil pool error = %v, want invalid", err)
	}
	if err := ApplyTxSearchPath(context.Background(), &PurposePool{engine: "mysql"}, openWithPool(pgPool), []string{"public"}); !errors.Is(err, ErrPostgresSearchPathInvalid) {
		t.Fatalf("engine mismatch error = %v, want invalid", err)
	}
	if err := ApplyTxSearchPath(context.Background(), pgPool, openWithPool(pgPool), nil); !errors.Is(err, ErrPostgresSearchPathInvalid) {
		t.Fatalf("invalid pg path error = %v, want invalid", err)
	}
	mysqlPool := &PurposePool{engine: "mysql"}
	if err := ApplyTxSearchPath(context.Background(), mysqlPool, openWithPool(mysqlPool), []string{"a", "b"}); !errors.Is(err, ErrPostgresSearchPathInvalid) {
		t.Fatalf("mysql multi-path error = %v, want invalid", err)
	}
	if err := ApplyTxSearchPath(context.Background(), mysqlPool, openWithPool(mysqlPool), []string{`a"b`}); !errors.Is(err, ErrIdentifierInvalid) {
		t.Fatalf("mysql invalid identifier error = %v, want ErrIdentifierInvalid", err)
	}
	unsupportedPool := &PurposePool{engine: "sqlite"}
	if err := ApplyTxSearchPath(context.Background(), unsupportedPool, openWithPool(unsupportedPool), []string{"main"}); !errors.Is(err, ErrUnsupportedTarget) {
		t.Fatalf("unsupported engine error = %v, want ErrUnsupportedTarget", err)
	}

	for name, tc := range map[string]struct {
		engine string
		path   []string
		want   string
	}{
		"postgres":         {engine: "postgresql", path: []string{"app", "public"}, want: `SET LOCAL search_path = "app", "public"`},
		"mysql":            {engine: "mysql", path: []string{"app"}, want: "USE `app`"},
		"mysql empty path": {engine: "mysql", path: nil, want: ""},
	} {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state := &searchPathCoverageState{}
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						return driver.RowsAffected(0), nil
					},
				}}, nil
			}, state)
			t.Cleanup(func() { _ = db.Close() })
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			sqlTx, err := conn.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlTx.Rollback() })
			pool := &PurposePool{engine: tc.engine}
			tx := &dmlTx{
				pool: pool, conn: conn, tx: sqlTx, open: true,
				execCtx: context.Background(),
			}
			if err := ApplyTxSearchPath(context.Background(), pool, tx, tc.path); err != nil {
				t.Fatal(err)
			}
			if got := state.lastExec(); got != tc.want {
				t.Fatalf("executed SQL = %q, want %q", got, tc.want)
			}
		})
	}

	db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
		return &searchPathCoverageConn{session: &searchPathCoverageSession{
			execHook: func(context.Context, string) (driver.Result, error) {
				return nil, errors.New("driver rejected")
			},
		}}, nil
	}, nil)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sqlTx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlTx.Rollback() })
	tx := &dmlTx{pool: pgPool, conn: conn, tx: sqlTx, open: true, execCtx: context.Background()}
	if err := ApplyTxSearchPath(context.Background(), pgPool, tx, []string{"public"}); err == nil ||
		!strings.Contains(err.Error(), "schema context apply failed") {
		t.Fatalf("execution failure = %v, want sanitized apply failure", err)
	}
}

func TestPostgresRelationKindCanonicalization(t *testing.T) {
	t.Parallel()

	cases := map[string]PostgresRelationKind{
		"r": PostgresRelationTable,
		"p": PostgresRelationTable,
		"v": PostgresRelationView,
		"m": PostgresRelationView,
		"S": PostgresRelationSequence,
		"i": PostgresRelationIndex,
		"I": PostgresRelationIndex,
		"f": PostgresRelationForeignTable,
		"c": PostgresRelationOther,
	}
	for relkind, want := range cases {
		if got := postgresRelationKind(relkind); got != want {
			t.Fatalf("relkind %q maps to %q, want %q", relkind, got, want)
		}
	}
	if !PostgresRelationTable.IsBaseTable() {
		t.Fatal("ordinary table must be a base table")
	}
	for _, kind := range []PostgresRelationKind{
		PostgresRelationView, PostgresRelationSequence, PostgresRelationIndex,
		PostgresRelationForeignTable, PostgresRelationOther,
	} {
		if kind.IsBaseTable() {
			t.Fatalf("%q must shadow later tables without being classified as a base table", kind)
		}
	}
}

func TestExecuteDDLWithSearchPathFailureClassification(t *testing.T) {
	t.Parallel()

	pgAdapter := NewPostgreSQLAdapter(Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{})
	mysqlAdapter := NewMySQLAdapter(Endpoint{CompatibilityMod: "mysql"}, AdapterOptions{})
	sqliteAdapter := &baseAdapter{ep: Endpoint{CompatibilityMod: "sqlite"}}
	pgExecution := &PurposePool{engine: "postgresql", purpose: PurposeExecution}
	pgReview := &PurposePool{engine: "postgresql", purpose: PurposeReview}
	mysqlExecution := &PurposePool{engine: "mysql", purpose: PurposeExecution}

	tests := map[string]struct {
		adapter      Adapter
		pool         *PurposePool
		path         []string
		statementSQL string
		want         string
	}{
		"nil adapter": {
			pool: pgExecution, path: []string{"public"}, statementSQL: "CREATE TABLE t (id int)",
			want: "adapter_unavailable",
		},
		"nil pool": {
			adapter: pgAdapter, path: []string{"public"}, statementSQL: "CREATE TABLE t (id int)",
			want: "adapter_unavailable",
		},
		"adapter engine mismatch": {
			adapter: mysqlAdapter, pool: pgExecution, path: []string{"public"},
			statementSQL: "CREATE TABLE t (id int)", want: "adapter_unavailable",
		},
		"unsupported engine": {
			adapter: sqliteAdapter,
			pool:    &PurposePool{engine: "sqlite", purpose: PurposeExecution},
			path:    []string{"main"}, statementSQL: "CREATE TABLE t (id int)",
			want: "adapter_unavailable",
		},
		"read only purpose": {
			adapter: pgAdapter, pool: pgReview, path: []string{"public"},
			statementSQL: "CREATE TABLE t (id int)", want: "read_only_target",
		},
		"empty statement": {
			adapter: pgAdapter, pool: pgExecution, path: []string{"public"},
			want: "statement_required",
		},
		"mysql path count": {
			adapter: mysqlAdapter, pool: mysqlExecution, path: []string{"app", "public"},
			statementSQL: "CREATE TABLE t (id int)", want: "schema_context_invalid",
		},
		"mysql invalid identifier": {
			adapter: mysqlAdapter, pool: mysqlExecution, path: []string{`app"name`},
			statementSQL: "CREATE TABLE t (id int)", want: "schema_context_invalid",
		},
		"postgres invalid identifier": {
			adapter: pgAdapter, pool: pgExecution, path: []string{`public"name`},
			statementSQL: "CREATE TABLE t (id int)", want: "schema_context_invalid",
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ExecuteDDLWithSearchPathBeforeSend(
				context.Background(), tc.adapter, tc.pool, tc.path, tc.statementSQL,
				func(context.Context) error {
					t.Fatal("beforeSend must not run for precondition failure")
					return nil
				},
			)
			if result != nil {
				t.Fatalf("result = %+v, want nil", result)
			}
			var notSent *StatementNotSentError
			if !errors.As(err, &notSent) || notSent.FailureName != tc.want {
				t.Fatalf("error = %v, want not-sent failure %q", err, tc.want)
			}
		})
	}
}

func TestExecuteDDLWithSearchPathExecutionBoundaries(t *testing.T) {
	t.Parallel()

	const targetSQL = "CREATE TABLE t (id integer)"
	tests := map[string]struct {
		execHook         func(string) (driver.Result, error)
		beforeSendErr    error
		wantFailure      string
		wantCallBefore   bool
		wantTargetCalled bool
	}{
		"path apply failure": {
			execHook: func(query string) (driver.Result, error) {
				if strings.Contains(query, "SET search_path") {
					return nil, errors.New("path rejected")
				}
				return driver.RowsAffected(0), nil
			},
			wantFailure: "schema_context_failed",
		},
		"before send failure": {
			execHook: func(string) (driver.Result, error) {
				return driver.RowsAffected(0), nil
			},
			beforeSendErr:  errors.New("durable boundary failed"),
			wantCallBefore: true,
		},
		"target success": {
			execHook: func(string) (driver.Result, error) {
				return driver.RowsAffected(3), nil
			},
			wantCallBefore:   true,
			wantTargetCalled: true,
		},
		"target failure remains classified result": {
			execHook: func(query string) (driver.Result, error) {
				if query == targetSQL {
					return nil, errors.New("driver timeout")
				}
				return driver.RowsAffected(0), nil
			},
			wantCallBefore:   true,
			wantTargetCalled: true,
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state := &searchPathCoverageState{}
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(_ context.Context, query string) (driver.Result, error) {
						return tc.execHook(query)
					},
					queryHook: backendIdentityCoverageRows,
				}}, nil
			}, state)
			t.Cleanup(func() { _ = db.Close() })
			pool := newCoveragePurposePool(db, "postgresql", PurposeExecution)
			adapter := NewPostgreSQLAdapter(
				Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{},
			)
			beforeCalled := false
			result, err := ExecuteDDLWithSearchPathBeforeSend(
				context.Background(), adapter, pool, []string{"public"}, targetSQL,
				func(context.Context) error {
					beforeCalled = true
					return tc.beforeSendErr
				},
			)
			if beforeCalled != tc.wantCallBefore {
				t.Fatalf("beforeSend called = %v, want %v", beforeCalled, tc.wantCallBefore)
			}
			targetCalled := state.containsExec(targetSQL)
			if targetCalled != tc.wantTargetCalled {
				t.Fatalf("target SQL called = %v, want %v; execs=%v",
					targetCalled, tc.wantTargetCalled, state.execSnapshot())
			}
			if tc.beforeSendErr != nil {
				if err != tc.beforeSendErr || result != nil {
					t.Fatalf("beforeSend error = %v result=%+v, want exact error and nil result", err, result)
				}
				return
			}
			if tc.wantFailure != "" {
				var notSent *StatementNotSentError
				if !errors.As(err, &notSent) || notSent.FailureName != tc.wantFailure {
					t.Fatalf("error = %v, want not-sent failure %q", err, tc.wantFailure)
				}
				if result != nil {
					t.Fatalf("result = %+v, want nil", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExecuteDDLWithSearchPathBeforeSend() error = %v", err)
			}
			if result == nil || result.SendBoundary != SendBoundarySent {
				t.Fatalf("result = %+v, want sent statement result", result)
			}
			if name == "target success" && result.AffectedRows != 3 {
				t.Fatalf("affected rows = %d, want 3", result.AffectedRows)
			}
			if name != "target success" && result.Outcome == OutcomeSuccess {
				t.Fatalf("driver failure must not be reported success: %+v", result)
			}
		})
	}
}

func TestExecuteDDLWithSearchPathWrapperDelegates(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		engine string
		path   []string
	}{
		"postgres": {engine: "postgresql", path: []string{"public"}},
		"mysql":    {engine: "mysql", path: []string{"public"}},
	} {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter := NewPostgreSQLAdapter(
				Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{},
			)
			if tc.engine == "mysql" {
				adapter = NewMySQLAdapter(
					Endpoint{CompatibilityMod: "mysql"}, AdapterOptions{},
				)
			}
			db := newCoverageFakeDB(func(context.Context) (driver.Conn, error) {
				return &searchPathCoverageConn{session: &searchPathCoverageSession{
					execHook: func(context.Context, string) (driver.Result, error) {
						return driver.RowsAffected(1), nil
					},
					queryHook: backendIdentityCoverageRows,
				}}, nil
			}, nil)
			t.Cleanup(func() { _ = db.Close() })
			pool := newCoveragePurposePool(db, tc.engine, PurposeExecution)

			result, err := ExecuteDDLWithSearchPath(
				context.Background(), adapter, pool, tc.path,
				"CREATE TABLE t (id integer)",
			)
			if err != nil {
				t.Fatalf("ExecuteDDLWithSearchPath() error = %v", err)
			}
			if result == nil || result.Outcome != OutcomeSuccess || result.AffectedRows != 1 {
				t.Fatalf("result = %+v, want successful one-row DDL result", result)
			}
		})
	}
}

type searchPathCoverageState struct {
	mu      sync.Mutex
	execs   []string
	queries []string
}

func (s *searchPathCoverageState) recordExec(query string) {
	s.mu.Lock()
	s.execs = append(s.execs, query)
	s.mu.Unlock()
}

func (s *searchPathCoverageState) recordQuery(query string) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
}

func (s *searchPathCoverageState) lastExec() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.execs) == 0 {
		return ""
	}
	return s.execs[len(s.execs)-1]
}

func (s *searchPathCoverageState) containsExec(query string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, got := range s.execs {
		if got == query {
			return true
		}
	}
	return false
}

func (s *searchPathCoverageState) execSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

type searchPathCoverageConnector struct {
	connect func(context.Context) (driver.Conn, error)
}

func (c *searchPathCoverageConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.connect(ctx)
}

func (c *searchPathCoverageConnector) Driver() driver.Driver {
	return searchPathCoverageDriver{}
}

type searchPathCoverageDriver struct{}

func (searchPathCoverageDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("open unused")
}

type searchPathCoverageSession struct {
	beginErr  error
	execHook  func(context.Context, string) (driver.Result, error)
	queryHook func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
}

type searchPathCoverageConn struct {
	session *searchPathCoverageSession
}

func (c *searchPathCoverageConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unused")
}

func (c *searchPathCoverageConn) Close() error { return nil }

func (c *searchPathCoverageConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *searchPathCoverageConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if c.session.beginErr != nil {
		return nil, c.session.beginErr
	}
	return &searchPathCoverageTx{session: c.session}, nil
}

func (c *searchPathCoverageConn) ExecContext(
	ctx context.Context, query string, _ []driver.NamedValue,
) (driver.Result, error) {
	return c.session.ExecContext(ctx, query)
}

func (c *searchPathCoverageConn) QueryContext(
	ctx context.Context, query string, args []driver.NamedValue,
) (driver.Rows, error) {
	return c.session.QueryContext(ctx, query, args)
}

type searchPathCoverageTx struct {
	session *searchPathCoverageSession
}

func (*searchPathCoverageTx) Commit() error   { return nil }
func (*searchPathCoverageTx) Rollback() error { return nil }

func (t *searchPathCoverageTx) ExecContext(
	ctx context.Context, query string, _ []driver.NamedValue,
) (driver.Result, error) {
	return t.session.ExecContext(ctx, query)
}

func (t *searchPathCoverageTx) QueryContext(
	ctx context.Context, query string, args []driver.NamedValue,
) (driver.Rows, error) {
	return t.session.QueryContext(ctx, query, args)
}

func (s *searchPathCoverageSession) ExecContext(
	ctx context.Context, query string,
) (driver.Result, error) {
	if s.execHook != nil {
		return s.execHook(ctx, query)
	}
	return driver.RowsAffected(0), nil
}

func (s *searchPathCoverageSession) QueryContext(
	ctx context.Context, query string, args []driver.NamedValue,
) (driver.Rows, error) {
	if s.queryHook != nil {
		return s.queryHook(ctx, query, args)
	}
	return backendIdentityCoverageRows(ctx, query, args)
}

type searchPathCoverageRows struct {
	columns []string
	values  [][]driver.Value
	next    int
	tailErr error
}

func (r *searchPathCoverageRows) Columns() []string { return r.columns }
func (*searchPathCoverageRows) Close() error        { return nil }

func (r *searchPathCoverageRows) Next(dest []driver.Value) error {
	if r.next < len(r.values) {
		copy(dest, r.values[r.next])
		r.next++
		return nil
	}
	if r.tailErr != nil {
		err := r.tailErr
		r.tailErr = nil
		return err
	}
	return io.EOF
}

func newCoverageFakeDB(
	connect func(context.Context) (driver.Conn, error),
	state *searchPathCoverageState,
) *sql.DB {
	if state == nil {
		state = &searchPathCoverageState{}
	}
	wrapped := func(ctx context.Context) (driver.Conn, error) {
		conn, err := connect(ctx)
		if err != nil {
			return nil, err
		}
		if connWithState, ok := conn.(*searchPathCoverageConn); ok {
			originalExec := connWithState.session.execHook
			originalQuery := connWithState.session.queryHook
			connWithState.session.execHook = func(ctx context.Context, query string) (driver.Result, error) {
				state.recordExec(query)
				if originalExec != nil {
					return originalExec(ctx, query)
				}
				return driver.RowsAffected(0), nil
			}
			connWithState.session.queryHook = func(
				ctx context.Context, query string, args []driver.NamedValue,
			) (driver.Rows, error) {
				state.recordQuery(query)
				if originalQuery != nil {
					return originalQuery(ctx, query, args)
				}
				return backendIdentityCoverageRows(ctx, query, args)
			}
		}
		return conn, nil
	}
	return sql.OpenDB(&searchPathCoverageConnector{connect: wrapped})
}

func newCoveragePurposePool(db *sql.DB, engine string, purpose Purpose) *PurposePool {
	return &PurposePool{
		engine: engine, purpose: purpose, db: db,
		cursors:      map[string]*liveCursor{},
		execCancels:  map[string]context.CancelFunc{},
		execBackends: map[string]int64{},
	}
}

func backendIdentityCoverageRows(
	context.Context, string, []driver.NamedValue,
) (driver.Rows, error) {
	return &searchPathCoverageRows{
		columns: []string{"backend_id"},
		values:  [][]driver.Value{{int64(42)}},
	}, nil
}

func validSearchPathForTest() PostgresSearchPath {
	return PostgresSearchPath{
		SchemaVersion:  PostgresSearchPathVersion,
		CurrentUser:    "deploy_user",
		Configured:     `"$user", public`,
		Explicit:       []string{"deploy_user", "public"},
		Effective:      []string{"pg_catalog", "deploy_user", "public"},
		CreationSchema: "deploy_user",
	}
}

func makeCoveragePath(n int) []string {
	path := make([]string, n)
	for i := range path {
		path[i] = fmt.Sprintf("schema_%02d", i)
	}
	return path
}
