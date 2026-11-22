package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func testPostgresBoundaryPath() reviewapp.FrozenPostgresSearchPath {
	return reviewapp.FrozenPostgresSearchPath{
		Version:        1,
		CurrentUser:    "writer",
		Configured:     "app, public",
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
}

func testPostgresBoundaryBinding(
	path reviewapp.FrozenPostgresSearchPath,
) reviewapp.FrozenPostgresPathBinding {
	return reviewapp.FrozenPostgresPathBinding{
		Version:          3,
		Path:             path,
		Complete:         true,
		CallablesSafe:    true,
		CallablesChecked: true,
		Baseline:         map[string]reviewapp.FrozenPostgresBaselineRelation{},
		Relations:        map[string]reviewapp.FrozenPostgresObjectBinding{},
	}
}

func requirePostgresBoundaryFailure(
	t *testing.T, err error, wantReason string,
) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) ||
		de.Code != domain.CodePreflightFailed ||
		de.Name != "EXECUTION_PREFLIGHT_FAILED:"+wantReason {
		t.Fatalf("error = %v, want preflight failure %q", err, wantReason)
	}
}

type postgresBoundaryUnavailableTx struct{}

func (postgresBoundaryUnavailableTx) Commit(
	context.Context,
) (*database.StatementResult, error) {
	return nil, nil
}

func (postgresBoundaryUnavailableTx) Rollback(
	context.Context,
) (*database.StatementResult, error) {
	return nil, nil
}

var (
	postgresBoundaryDriverOnce sync.Once
	postgresBoundaryBeginError = errors.New(
		"postgres boundary transaction unavailable",
	)
)

type postgresBoundaryDriver struct{}

func (postgresBoundaryDriver) Open(string) (driver.Conn, error) {
	return postgresBoundaryDriverConn{}, nil
}

type postgresBoundaryDriverConn struct{}

func (postgresBoundaryDriverConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (postgresBoundaryDriverConn) Close() error {
	return nil
}

func (postgresBoundaryDriverConn) Begin() (driver.Tx, error) {
	return nil, postgresBoundaryBeginError
}

func (postgresBoundaryDriverConn) BeginTx(
	context.Context, driver.TxOptions,
) (driver.Tx, error) {
	return nil, postgresBoundaryBeginError
}

func newPostgresBoundaryUnavailableConn(t *testing.T) *sql.Conn {
	t.Helper()
	postgresBoundaryDriverOnce.Do(func() {
		sql.Register(
			"yearning-postgres-boundary-unavailable",
			postgresBoundaryDriver{},
		)
	})
	db, err := sql.Open("yearning-postgres-boundary-unavailable", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = db.Close()
	})
	return conn
}

func TestPostgresBoundaryReviewContextRejectsProbeBindingDrift(
	t *testing.T,
) {
	t.Parallel()
	path := testPostgresBoundaryPath()
	binding := testPostgresBoundaryBinding(path)
	probeCalls := 0
	svc := &Services{
		SearchPathProbe: func(
			context.Context,
			*database.PurposePool,
			database.PostgresSearchPath,
		) error {
			probeCalls++
			return nil
		},
	}

	incomplete := binding
	incomplete.Complete = false
	err := svc.verifyPostgresReviewContext(
		context.Background(), &database.PurposePool{}, path, &incomplete, 0,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)
	if probeCalls != 0 {
		t.Fatal("incomplete binding reached the execution-purpose pool")
	}

	drifted := binding
	drifted.Path = reviewapp.FrozenPostgresSearchPath{
		Version:        1,
		CurrentUser:    "writer",
		Configured:     "public",
		Explicit:       []string{"public"},
		Effective:      []string{"pg_catalog", "public"},
		CreationSchema: "public",
	}
	err = svc.verifyPostgresReviewContext(
		context.Background(), &database.PurposePool{}, path, &drifted, 0,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)
	if probeCalls != 1 {
		t.Fatalf("probe calls = %d, want 1 before binding comparison", probeCalls)
	}
}

func TestPostgresBoundaryDMLTransactionRejectsUnverifiableEvidence(
	t *testing.T,
) {
	t.Parallel()
	path := testPostgresBoundaryPath()
	binding := testPostgresBoundaryBinding(path)
	svc := &Services{}

	err := svc.verifyPostgresReviewContextOnDMLTransaction(
		context.Background(),
		&database.PurposePool{},
		postgresBoundaryUnavailableTx{},
		reviewapp.FrozenPostgresSearchPath{},
		nil,
		0,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	err = svc.verifyPostgresReviewContextOnDMLTransaction(
		context.Background(),
		&database.PurposePool{},
		postgresBoundaryUnavailableTx{},
		path,
		nil,
		0,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	err = svc.verifyPostgresReviewContextOnDMLTransaction(
		context.Background(),
		&database.PurposePool{},
		postgresBoundaryUnavailableTx{},
		path,
		&binding,
		-1,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	malformedCallable := binding
	malformedCallable.CallablesRequested = []string{"function:"}
	err = svc.verifyPostgresReviewContextOnDMLTransaction(
		context.Background(),
		&database.PurposePool{},
		postgresBoundaryUnavailableTx{},
		path,
		&malformedCallable,
		1,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	err = svc.verifyPostgresReviewContextOnDMLTransaction(
		context.Background(),
		&database.PurposePool{},
		postgresBoundaryUnavailableTx{},
		path,
		&binding,
		1,
	)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_changed")
}

func TestPostgresBoundaryDDLConnectionRejectsUnverifiableEvidence(t *testing.T) {
	t.Parallel()
	path := testPostgresBoundaryPath()
	binding := testPostgresBoundaryBinding(path)
	svc := &Services{}

	err := svc.verifyPostgresReviewContextOnDDLConnection(
		context.Background(), nil, path, &binding, 0,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	err = svc.verifyPostgresReviewContextOnDDLConnection(
		context.Background(), &sql.Conn{}, path, nil, 0,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	err = svc.verifyPostgresReviewContextOnDDLConnection(
		context.Background(), &sql.Conn{}, path, &binding, -1,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	malformedCallable := binding
	malformedCallable.CallablesRequested = []string{"broken"}
	err = svc.verifyPostgresReviewContextOnDDLConnection(
		context.Background(), &sql.Conn{}, path, &malformedCallable, 1,
	)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	unavailableConn := newPostgresBoundaryUnavailableConn(t)
	err = svc.verifyPostgresReviewContextOnDDLConnection(
		context.Background(), unavailableConn, path, &binding, 1,
	)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_changed")
}

func TestPostgresBoundaryObjectAndCallableVerificationFailsClosed(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	path := testPostgresBoundaryPath()
	binding := testPostgresBoundaryBinding(path)

	unchecked := binding
	unchecked.CallablesChecked = false
	err := verifyPostgresObjectBindings(ctx, nil, path, unchecked, 0)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_changed")

	for _, malformed := range []string{"broken", ":name", "function:"} {
		malformed := malformed
		t.Run(malformed, func(t *testing.T) {
			t.Parallel()
			requests, ok := postgresCallableRequestsFromBinding(
				reviewapp.FrozenPostgresPathBinding{
					CallablesRequested: []string{malformed},
				},
			)
			if ok || requests != nil {
				t.Fatalf(
					"malformed request %q produced requests=%+v ok=%v",
					malformed, requests, ok,
				)
			}
		})
	}

	malformed := binding
	malformed.CallablesRequested = []string{"function:"}
	err = verifyPostgresCallablesOnPool(ctx, nil, path, malformed)
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_invalid",
	)

	validRequest := binding
	validRequest.CallablesRequested = []string{"function:lower"}
	err = verifyPostgresCallablesOnPool(ctx, nil, path, validRequest)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_changed")
}

func TestPostgresBoundaryCallableVerifierRejectsDuplicateResolution(
	t *testing.T,
) {
	t.Parallel()
	var binding reviewapp.FrozenPostgresPathBinding
	if err := json.Unmarshal([]byte(`{
		"callables_checked": true,
		"callables_requested": ["function:lower", "operator:~~"],
		"callables": {
			"function:lower": {
				"kind": "function",
				"name": "lower",
				"winners": [{
					"schema": "pg_catalog",
					"signature": "25",
					"result_type": "25",
					"oid": 870,
					"pg_catalog": true
				}]
			}
		}
	}`), &binding); err != nil {
		t.Fatal(err)
	}
	request := database.PostgresCallableRequest{
		Kind: database.PostgresCallableFunction,
		Name: "lower",
	}
	resolution := database.PostgresCallableResolution{
		Request: request,
		Winners: []database.PostgresCallableWinner{{
			Schema:     "pg_catalog",
			Signature:  "25",
			ResultType: "25",
			OID:        870,
			PgCatalog:  true,
		}},
	}
	err := verifyPostgresCallables(
		binding,
		[]database.PostgresCallableResolution{resolution, resolution},
	)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_changed")
}

func TestPostgresBoundaryReviewedBindingPositionExplicitSchema(
	t *testing.T,
) {
	t.Parallel()
	path := testPostgresBoundaryPath()
	present := database.PostgresRelationLookup{
		Relation: "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{
			"app": database.PostgresRelationTable,
		},
		OIDBySchema: map[string]uint32{"app": 101},
	}
	schema, kind, oid, ok := reviewedBindingPosition(
		path, present, true, "app",
	)
	if !ok || schema != "app" || kind != "table" || oid != 101 {
		t.Fatalf(
			"explicit present position = %q/%q/%d/%v",
			schema, kind, oid, ok,
		)
	}

	absent := database.PostgresRelationLookup{
		Relation:        "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{},
		OIDBySchema:     map[string]uint32{},
	}
	schema, kind, oid, ok = reviewedBindingPosition(
		path, absent, true, "public",
	)
	if ok || schema != "public" || kind != "" || oid != 0 {
		t.Fatalf(
			"explicit absent position = %q/%q/%d/%v",
			schema, kind, oid, ok,
		)
	}
}

func TestPostgresBoundaryOpenContextRejectsMissingFrozenValues(
	t *testing.T,
) {
	t.Parallel()
	crypter := newExecutionSearchPathTestCrypter(t)
	runID := "boundary-run"
	envelope, err := crypter.Seal(
		crypto.PurposePayload,
		[]byte(`{"accepted_summary":"missing frozen values"}`),
		crypto.AAD{
			DeploymentID: crypter.DeploymentID(),
			Table:        "stage_review_snapshots",
			PrimaryKey:   "boundary-run#2",
			Column:       "normalized_context_ciphertext",
			Purpose:      crypto.PurposePayload,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Services{Crypter: crypter}
	_, err = svc.openFrozenPostgresReviewContext(frozenStageReviewRow{
		ReviewRunID:   runID,
		StagePosition: 2,
		Ciphertext:    envelope.Ciphertext,
		Nonce:         envelope.Nonce,
		KeyID:         envelope.KeyID,
		Algorithm:     envelope.Algorithm,
	})
	requirePostgresBoundaryFailure(
		t, err, "postgres_search_path_snapshot_unreadable",
	)
}

func TestPostgresBoundaryUniformRelationsPassAndDrift(t *testing.T) {
	t.Parallel()
	path := testPostgresBoundaryPath()
	binding := testPostgresBoundaryBinding(path)
	binding.UniformRelations = map[string]reviewapp.FrozenPostgresObjectBinding{
		"table_1": {
			TableName: "orders", Schema: "app", Kind: "table",
			OID: 101, Present: true, Supported: true,
		},
	}
	binding.Relations["2:table_2"] = reviewapp.FrozenPostgresObjectBinding{
		TableName: "orders", Schema: "app", Kind: "table",
		OID: 101, Present: true, Supported: true,
	}

	names, err := reviewedPostgresRelationNames(binding, 2)
	if err != nil {
		t.Fatalf("reviewedPostgresRelationNames failed: %v", err)
	}
	if len(names) != 1 || names[0] != "orders" {
		t.Fatalf("reviewedPostgresRelationNames = %v, want deduplicated [orders]", names)
	}

	matchingLookup := []database.PostgresRelationLookup{{
		Relation:        "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{"app": database.PostgresRelationTable},
		OIDBySchema:     map[string]uint32{"app": 101},
	}}
	if err := verifyPostgresInspection(path, binding, 2, matchingLookup); err != nil {
		t.Fatalf("verifyPostgresInspection failed on matching uniform lookup: %v", err)
	}

	// Drift in OID or missing lookup must reject with postgres_search_path_changed.
	driftedLookup := []database.PostgresRelationLookup{{
		Relation:        "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{"app": database.PostgresRelationTable},
		OIDBySchema:     map[string]uint32{"app": 999},
	}}
	requirePostgresBoundaryFailure(t, verifyPostgresInspection(path, binding, 2, driftedLookup), "postgres_search_path_changed")
	requirePostgresBoundaryFailure(t, verifyPostgresInspection(path, binding, 2, nil), "postgres_search_path_changed")

	// Unsupported relation in UniformRelations must reject with postgres_search_path_changed.
	unsupported := testPostgresBoundaryBinding(path)
	unsupported.UniformRelations = map[string]reviewapp.FrozenPostgresObjectBinding{
		"table_1": {TableName: "orders", Schema: "app", Kind: "view", OID: 101, Present: true, Supported: false},
	}
	_, err = reviewedPostgresRelationNames(unsupported, 1)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_changed")
	requirePostgresBoundaryFailure(t, verifyPostgresObjectBindings(context.Background(), nil, path, unsupported, 1), "postgres_search_path_changed")
	requirePostgresBoundaryFailure(t, verifyPostgresInspection(path, unsupported, 1, matchingLookup), "postgres_search_path_changed")

	// Overlapping UniformRelations and Relations token must reject with snapshot_invalid.
	overlap := testPostgresBoundaryBinding(path)
	overlap.UniformRelations = map[string]reviewapp.FrozenPostgresObjectBinding{
		"table_1": {TableName: "orders", Schema: "app", Kind: "table", OID: 101, Present: true, Supported: true},
	}
	overlap.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
		"2:table_1": {TableName: "orders", Schema: "app", Kind: "table", OID: 101, Present: true, Supported: true},
	}
	_, err = reviewedPostgresRelationNames(overlap, 2)
	requirePostgresBoundaryFailure(t, err, "postgres_search_path_snapshot_invalid")
	requirePostgresBoundaryFailure(t, verifyPostgresInspection(path, overlap, 2, matchingLookup), "postgres_search_path_snapshot_invalid")
}

func TestPostgresBoundaryOneShotCreateDropNotCheckedAtSubsequentDDL(t *testing.T) {
	t.Parallel()
	path := testPostgresBoundaryPath()
	binding := testPostgresBoundaryBinding(path)
	// Orders is uniform across all ordinals; new_tbl was created at ordinal 1 only
	// and demoted out of UniformRelations into Relations["1:table_new"].
	binding.UniformRelations = map[string]reviewapp.FrozenPostgresObjectBinding{
		"table_orders": {
			TableName: "orders", Schema: "app", Kind: "table",
			OID: 101, Present: true, Supported: true,
		},
	}
	binding.Relations["1:table_new"] = reviewapp.FrozenPostgresObjectBinding{
		TableName: "new_tbl", Schema: "app", Kind: "table",
		OID: 0, Present: false, Supported: true,
	}

	// At ordinal 1, new_tbl is expected absent while orders is expected present.
	lookupOrd1 := []database.PostgresRelationLookup{
		{
			Relation:        "orders",
			PresentBySchema: map[string]database.PostgresRelationKind{"app": database.PostgresRelationTable},
			OIDBySchema:     map[string]uint32{"app": 101},
		},
		{
			Relation:        "new_tbl",
			PresentBySchema: map[string]database.PostgresRelationKind{},
			OIDBySchema:     map[string]uint32{},
		},
	}
	if err := verifyPostgresInspection(path, binding, 1, lookupOrd1); err != nil {
		t.Fatalf("ordinal 1 inspection failed: %v", err)
	}

	// After ordinal 1 creates new_tbl, ordinal 2 checks only UniformRelations (orders) and ordinal-2
	// relations; live new_tbl existing now must NOT cause false drift failure.
	lookupOrd2 := []database.PostgresRelationLookup{
		{
			Relation:        "orders",
			PresentBySchema: map[string]database.PostgresRelationKind{"app": database.PostgresRelationTable},
			OIDBySchema:     map[string]uint32{"app": 101},
		},
		{
			Relation:        "new_tbl",
			PresentBySchema: map[string]database.PostgresRelationKind{"app": database.PostgresRelationTable},
			OIDBySchema:     map[string]uint32{"app": 202},
		},
	}
	namesOrd2, err := reviewedPostgresRelationNames(binding, 2)
	if err != nil {
		t.Fatalf("reviewedPostgresRelationNames(2) failed: %v", err)
	}
	if len(namesOrd2) != 1 || namesOrd2[0] != "orders" {
		t.Fatalf("reviewedPostgresRelationNames(2) = %v, want [orders]", namesOrd2)
	}
	if err := verifyPostgresInspection(path, binding, 2, lookupOrd2); err != nil {
		t.Fatalf("ordinal 2 inspection falsely failed due to ordinal-1 new_tbl creation: %v", err)
	}
}
