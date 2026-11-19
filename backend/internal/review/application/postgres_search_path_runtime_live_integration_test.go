//go:build integration

package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// TestPostgresSearchPathRuntimeLiveProductionPaths exercises the production
// connection, credential, identity, search-path, relation and callable paths.
// The valid frozen paths beyond the initial probe are supplied as data because
// this test must not mutate role or database defaults shared with other tests.
func TestPostgresSearchPathRuntimeLiveProductionPaths(t *testing.T) {
	password := os.Getenv("YEARNING_TEST_ADAPTER_PG_PASSWORD")
	if password == "" {
		t.Skip("target PostgreSQL not configured: set YEARNING_TEST_ADAPTER_PG_PASSWORD")
	}
	port, err := strconv.Atoi(itestEnv("YEARNING_TEST_ADAPTER_PG_PORT", "54333"))
	if err != nil {
		t.Fatalf("YEARNING_TEST_ADAPTER_PG_PORT is not an integer: %v", err)
	}
	host := itestEnv("YEARNING_TEST_ADAPTER_PG_HOST", "127.0.0.1")
	user := itestEnv("YEARNING_TEST_ADAPTER_PG_USER", "yearning")
	databaseName := itestEnv("YEARNING_TEST_ADAPTER_PG_DB", "yearning")
	t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", "1")

	ctx := context.Background()
	adminDB, err := sql.Open("pgx", fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, databaseName,
	))
	if err != nil {
		t.Fatalf("open target PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("target PostgreSQL unreachable: %v", err)
	}
	admin, err := adminDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire target PostgreSQL connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	suffix := time.Now().UnixNano()
	schema := fmt.Sprintf("runtime_live_%d", suffix)
	table := fmt.Sprintf("relation_%d", suffix)
	absentTable := fmt.Sprintf("absent_%d", suffix)
	shadowFunction := fmt.Sprintf("shadow_%d", suffix)
	operatorFunction := fmt.Sprintf("add_%d", suffix)
	callableIdentityExec(t, admin, fmt.Sprintf(
		`create schema %s`, quoteCallableIdentityIdent(schema)))
	t.Cleanup(func() {
		_, _ = admin.ExecContext(
			context.Background(),
			fmt.Sprintf(
				`drop schema if exists %s cascade`,
				quoteCallableIdentityIdent(schema),
			),
		)
	})
	callableIdentityExec(t, admin, fmt.Sprintf(
		`create table %s.%s (id bigint primary key, label text not null)`,
		quoteCallableIdentityIdent(schema), quoteCallableIdentityIdent(table)))
	callableIdentityExec(t, admin, fmt.Sprintf(
		`create function %s.%s(uuid) returns integer language sql immutable as $$ select 42 $$`,
		quoteCallableIdentityIdent(schema),
		quoteCallableIdentityIdent(shadowFunction)))
	callableIdentityExec(t, admin, fmt.Sprintf(
		`create function %s.%s(integer, integer) returns integer language sql immutable as $$ select $1 + $2 $$`,
		quoteCallableIdentityIdent(schema),
		quoteCallableIdentityIdent(operatorFunction)))
	callableIdentityExec(t, admin, fmt.Sprintf(
		`create operator %s.@ (leftarg = integer, rightarg = integer, function = %s.%s)`,
		quoteCallableIdentityIdent(schema),
		quoteCallableIdentityIdent(schema),
		quoteCallableIdentityIdent(operatorFunction)))

	tableOID := runtimeLiveCatalogOID(t, admin,
		`select c.oid::bigint
		   from pg_catalog.pg_class c
		   join pg_catalog.pg_namespace n on n.oid = c.relnamespace
		  where n.nspname = $1 and c.relname = $2`,
		schema, table)
	var (
		lowerOID       int64
		lowerSignature string
		lowerResult    string
	)
	if err := admin.QueryRowContext(ctx, `
		select p.oid::bigint, p.proargtypes::text,
		       p.prorettype::pg_catalog.regtype::text
		  from pg_catalog.pg_proc p
		  join pg_catalog.pg_namespace n on n.oid = p.pronamespace
		 where n.nspname = 'pg_catalog' and p.proname = 'lower'
		   and p.proargtypes::text = '25'`,
	).Scan(&lowerOID, &lowerSignature, &lowerResult); err != nil {
		t.Fatalf("read pg_catalog lower(text) identity: %v", err)
	}
	if lowerOID <= 0 || lowerSignature != "25" || lowerResult != "text" {
		t.Fatalf("lower(text) identity = oid:%d signature:%q result:%q",
			lowerOID, lowerSignature, lowerResult)
	}
	shadowOID := runtimeLiveCatalogOID(t, admin, `
		select p.oid::bigint
		  from pg_catalog.pg_proc p
		  join pg_catalog.pg_namespace n on n.oid = p.pronamespace
		 where n.nspname = $1 and p.proname = $2`,
		schema, shadowFunction)
	operatorOID := runtimeLiveCatalogOID(t, admin, `
		select o.oid::bigint
		  from pg_catalog.pg_operator o
		  join pg_catalog.pg_namespace n on n.oid = o.oprnamespace
		 where n.nspname = $1 and o.oprname = '@'
		   and o.oprleft = 'integer'::pg_catalog.regtype
		   and o.oprright = 'integer'::pg_catalog.regtype`,
		schema)

	f := newFixture(t)
	crypter, recorder := newFactoryTestCrypterAndRecorder(t, f)
	svc, err := NewServices(f.db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Force the live review identity probe; the test fixture normally installs
	// a deterministic seam.
	svc.DatasourceIdentityProbe = nil

	datasourceID := insertFactoryDatasource(
		t, f, host, port, databaseName)
	reviewCredential := sealFactoryReviewCredential(
		t, crypter, datasourceID, user, password, true)
	insertFactoryCredential(t, f, datasourceID, reviewCredential)
	executionCredential := sealFactoryReviewCredential(
		t, crypter, datasourceID, user, password, true)
	insertRuntimeLiveCredential(
		t, f, datasourceID, "execution", executionCredential)

	row, err := loadDatasourceTargetRow(ctx, f.db, datasourceID)
	if err != nil {
		t.Fatal(err)
	}
	probeAdapter, _, _, identity, err := probeDatasourceConnection(
		ctx, f.db, crypter, row)
	if err != nil {
		t.Fatalf("probe target identity with review credential: %v", err)
	}
	_ = probeAdapter.Close()
	if identity == nil {
		t.Fatal("production identity probe returned nil")
	}
	fingerprint, err := configuredIdentityFingerprint(
		crypter, row, identity.InstanceMarker)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(
		`update datasources set identity_fingerprint = ? where id = ?`,
		fingerprint, datasourceID,
	).Error; err != nil {
		t.Fatal(err)
	}
	row, err = loadDatasourceTargetRow(ctx, f.db, datasourceID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := svc.probeFrozenDatasourceTarget(ctx, row)
	if err != nil {
		t.Fatalf("freeze production datasource target: %v", err)
	}
	if err := target.validate(); err != nil {
		t.Fatalf("frozen production target is invalid: %v", err)
	}

	probedPath, err := svc.probePostgresSearchPath(ctx, target)
	if err != nil {
		t.Fatalf("probe production execution search_path: %v", err)
	}
	if err := probedPath.Validate(); err != nil {
		t.Fatalf("production search_path probe returned invalid snapshot: %v", err)
	}
	if probedPath.CurrentUser == "" || probedPath.CurrentUser != identityMarkerUser(t, admin) {
		t.Fatalf("probed current user = %q", probedPath.CurrentUser)
	}

	// Do not SET search_path on the fixture or alter role defaults. The runtime
	// resolver consumes a trusted frozen value as data, including complete
	// absence entries and catalog identity from the isolated schema.
	path := FrozenPostgresSearchPath{
		Version:        postgresSearchPathSnapshotVersion,
		CurrentUser:    probedPath.CurrentUser,
		Configured:     fmt.Sprintf("%q, public", schema),
		Explicit:       []string{schema, "public"},
		Effective:      []string{"pg_catalog", schema, "public"},
		CreationSchema: schema,
	}
	if err := path.Validate(); err != nil {
		t.Fatalf("constructed frozen path is invalid: %v", err)
	}

	request := PostgresRelationRequest{
		Ordinal: 7, TableToken: "table_2", TableName: table,
	}
	relation, err := svc.resolvePostgresRelation(ctx, target, path, request)
	if err != nil {
		t.Fatalf("resolve relation through production runtime: %v", err)
	}
	if relation.Ordinal != request.Ordinal ||
		relation.TableToken != request.TableToken ||
		relation.TableName != request.TableName ||
		!relation.Complete ||
		len(relation.PresentBySchema) != 1 ||
		relation.PresentBySchema[schema] != "table" ||
		relation.OIDBySchema[schema] != tableOID {
		t.Fatalf("production relation lookup = %+v", relation)
	}

	absentRequest := PostgresRelationRequest{
		Ordinal: 8, TableToken: "table_3", TableName: absentTable,
	}
	absent, err := svc.resolvePostgresRelation(
		ctx, target, path, absentRequest)
	if err != nil {
		t.Fatalf("resolve absent relation through production runtime: %v", err)
	}
	if absent.Ordinal != absentRequest.Ordinal ||
		absent.TableToken != absentRequest.TableToken ||
		absent.TableName != absentRequest.TableName ||
		!absent.Complete ||
		len(absent.PresentBySchema) != 0 ||
		len(absent.OIDBySchema) != 0 {
		t.Fatalf("complete absence lookup = %+v", absent)
	}

	baselines, err := svc.resolvePostgresRelationBaselines(
		ctx, target, path, []string{table, absentTable})
	if err != nil {
		t.Fatalf("resolve production relation baselines: %v", err)
	}
	if len(baselines) != 2 ||
		baselines[table].PresentBySchema[schema] != "table" ||
		baselines[table].OIDBySchema[schema] != tableOID ||
		len(baselines[absentTable].PresentBySchema) != 0 ||
		len(baselines[absentTable].OIDBySchema) != 0 {
		t.Fatalf("production relation baselines = %+v", baselines)
	}

	if callables, err := svc.resolvePostgresCallables(
		ctx, target, path, nil,
	); err != nil || callables != nil {
		t.Fatalf("zero callable requests = %+v, err=%v", callables, err)
	}
	callableRequests := []database.PostgresCallableRequest{
		{Kind: database.PostgresCallableFunction, Name: "lower"},
		{Kind: database.PostgresCallableFunction, Name: shadowFunction},
		{Kind: database.PostgresCallableOperator, Name: "@"},
	}
	callables, err := svc.resolvePostgresCallables(
		ctx, target, path, callableRequests)
	if err != nil {
		t.Fatalf("resolve callables through production runtime: %v", err)
	}
	if len(callables) != len(callableRequests) {
		t.Fatalf("resolved %d callables for %d requests",
			len(callables), len(callableRequests))
	}
	assertRuntimeLiveCallableWinner(t, callables,
		callableRequests[0], "pg_catalog", lowerSignature,
		lowerResult, uint32(lowerOID), true)
	assertRuntimeLiveCallableWinner(t, callables,
		callableRequests[1], schema, "2950",
		"integer", shadowOID, false)
	assertRuntimeLiveCallableWinner(t, callables,
		callableRequests[2], schema, "integer,integer",
		"integer", operatorOID, false)

	// Configuration drift is rejected before credentials or a live connection
	// are used.
	configDrift := target
	configDrift.Host = "127.0.0.2"
	if _, err := svc.resolvePostgresRelation(
		ctx, configDrift, path, request,
	); !errors.Is(err, ErrFactSourceIdentityChanged) {
		t.Fatalf("configuration drift error = %v, want identity changed", err)
	}

	// Live identity drift is rejected after the execution credential and target
	// connection are proven real.
	identityDrift := target
	identityDrift.InstanceMarker += "-drift"
	if _, err := svc.resolvePostgresRelation(
		ctx, identityDrift, path, request,
	); !errors.Is(err, ErrFactSourceIdentityChanged) {
		t.Fatalf("identity drift error = %v, want identity changed", err)
	}
	if _, err := svc.resolvePostgresCallables(
		ctx, identityDrift, path,
		[]database.PostgresCallableRequest{callableRequests[0]},
	); !errors.Is(err, ErrFactSourceIdentityChanged) {
		t.Fatalf("callable identity drift error = %v, want identity changed", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.resolvePostgresRelation(
		canceled, target, path, request,
	); !errors.Is(err, ErrFactSourceIdentityChanged) {
		t.Fatalf("canceled relation context error = %v, want fail closed", err)
	}

	missingExecutionID := insertFactoryDatasource(
		t, f, host, port, databaseName)
	missingExecution := target
	missingExecution.DatasourceID = missingExecutionID
	missingExecution.ConfiguredIdentityFP = make32Hex(0)
	if _, err := svc.probePostgresSearchPath(
		ctx, missingExecution,
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("missing execution credential error = %v, want unavailable", err)
	}

	missingReviewID := insertFactoryDatasource(
		t, f, host, port, databaseName)
	missingReviewExecution := sealFactoryReviewCredential(
		t, crypter, missingReviewID, user, password, true)
	insertRuntimeLiveCredential(
		t, f, missingReviewID, "execution", missingReviewExecution)
	missingReview := target
	missingReview.DatasourceID = missingReviewID
	missingReview.ConfiguredIdentityFP = make32Hex(0)
	if _, err := svc.resolvePostgresCallables(
		ctx, missingReview, path,
		[]database.PostgresCallableRequest{callableRequests[0]},
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("missing review credential callable error = %v, want unavailable", err)
	}
	if _, err := svc.resolvePostgresRelationBaselines(
		ctx, missingReview, path, []string{table},
	); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("missing review credential baseline error = %v, want unavailable", err)
	}
}

func insertRuntimeLiveCredential(
	t *testing.T,
	f *fx,
	datasourceID string,
	purpose string,
	credential factoryCredential,
) {
	t.Helper()
	if purpose != "review" && purpose != "execution" {
		t.Fatalf("unsupported credential purpose %q", purpose)
	}
	if err := f.db.Exec(`insert into datasource_credentials
		(id, datasource_id, purpose, credential_ciphertext, credential_nonce,
		 credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, true, now(), now())`,
		credential.id, datasourceID, purpose,
		credential.ciphertext, credential.nonce,
		credential.keyID, credential.algorithm,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func runtimeLiveCatalogOID(
	t *testing.T,
	conn *sql.Conn,
	query string,
	args ...any,
) uint32 {
	t.Helper()
	var oid int64
	if err := conn.QueryRowContext(
		context.Background(), query, args...,
	).Scan(&oid); err != nil {
		t.Fatalf("read fixture catalog OID: %v", err)
	}
	if oid <= 0 || oid > int64(^uint32(0)) {
		t.Fatalf("fixture catalog OID out of range: %d", oid)
	}
	return uint32(oid)
}

func identityMarkerUser(t *testing.T, conn *sql.Conn) string {
	t.Helper()
	var user string
	if err := conn.QueryRowContext(
		context.Background(), `select current_user`,
	).Scan(&user); err != nil {
		t.Fatalf("read fixture current_user: %v", err)
	}
	return user
}

func assertRuntimeLiveCallableWinner(
	t *testing.T,
	resolutions []database.PostgresCallableResolution,
	request database.PostgresCallableRequest,
	schema string,
	signature string,
	resultType string,
	oid uint32,
	pgCatalog bool,
) {
	t.Helper()
	key := database.PostgresCallablePayloadKey(request.Kind, request.Name)
	for _, resolution := range resolutions {
		if resolution.Request != request {
			continue
		}
		if !resolution.Comparable() {
			t.Fatalf("%s has no visible callable identity", key)
		}
		for _, winner := range resolution.Winners {
			if winner.Schema != schema ||
				winner.Signature != signature ||
				winner.ResultType != resultType ||
				winner.OID != oid ||
				winner.PgCatalog != pgCatalog {
				continue
			}
			if resolution.Unsafe() != !pgCatalog {
				t.Fatalf("%s unsafe=%v, pg_catalog=%v",
					key, resolution.Unsafe(), pgCatalog)
			}
			return
		}
		t.Fatalf("%s missing winner schema=%q signature=%q result=%q oid=%d pg_catalog=%v; got %+v",
			key, schema, signature, resultType, oid, pgCatalog,
			resolution.Winners)
	}
	t.Fatalf("%s missing from resolutions: %+v", key, resolutions)
}
