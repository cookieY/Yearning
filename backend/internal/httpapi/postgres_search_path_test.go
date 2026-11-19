//go:build integration

// postgres_search_path_test.go verifies the empty-mapping PostgreSQL
// search_path contract across the HTTP review, submission and execution
// boundaries. The target database is real PostgreSQL 16; only the Provider
// is a deterministic local fake, and every Provider request is captured.
//
// After RCP-20261101-EPHEMERAL-PRECHECK, the review side uses the ephemeral precheck two-step flow:
// POST /api/v4/review-runs (zero DB persistence; snapshot lives only in in-memory EphemeralRun) ->
// POST /api/v4/change-orders (materializes Run inside the submission transaction).
//
// Removed coverage note (legacy premises eliminated by RCP-20261101):
//   - TestPostgresSearchPathHTTPMissingHistoricalSnapshotRejected: previously tampered with
//     stage_review_snapshots normalized context in DB after precheck (rewriteDraftStageContext
//     deleting postgres_search_path key) before submitting, verifying submission rejected missing
//     historical snapshots (2001). Under ephemeral precheck, snapshots exist only in memory before
//     materializing inside the submission transaction, so that path no longer exists.
//   - requireSearchPathDraftReady / rewriteDraftStageContext relied on precheck advancing
//     change_drafts to ready and writing stage_review_snapshots rows, and were removed accordingly.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

type postgresSearchPathTarget struct {
	admin         *sql.DB
	host          string
	port          int
	database      string
	reviewUser    string
	reviewPass    string
	executionRole string
	executionPass string
	firstSchema   string
	secondSchema  string
}

func newPostgresSearchPathTarget(
	t *testing.T, fx *b10Fixture,
) *postgresSearchPathTarget {
	t.Helper()
	cfg := resolveB11TargetPG(t, fx)
	databaseName := os.Getenv("YEARNING_TEST_ADAPTER_PG_DB")
	if databaseName == "" {
		databaseName = "postgres"
	}
	admin := openTargetPGNamed(
		t, cfg.Host, fmt.Sprint(cfg.Port), databaseName,
		cfg.AdminUser, cfg.AdminPassword, cfg.ApplicationName,
	)
	target := &postgresSearchPathTarget{
		admin: admin, host: cfg.Host, port: cfg.Port, database: databaseName,
		reviewUser: cfg.AdminUser, reviewPass: cfg.AdminPassword,
	}
	scope := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	target.executionRole = "yr_search_exec_" + scope
	target.executionPass = "PgPathPass_" + scope
	// The leading "$user" path member only resolves when the execution role
	// also has a same-named schema, which is the normal per-user deployment.
	target.firstSchema = target.executionRole
	target.secondSchema = "PGPathLater_" + scope

	created := false
	t.Cleanup(func() {
		if created {
			cleanupPostgresSearchPathTarget(t, target)
		}
		_ = admin.Close()
	})
	execTarget(t, admin, `create role `+quoteSearchPathIdent(target.executionRole)+
		` login password `+quoteSearchPathLiteral(target.executionPass)+
		` nosuperuser nocreatedb nocreaterole`)
	created = true
	for _, schema := range []string{target.firstSchema, target.secondSchema} {
		execTarget(t, admin, `create schema `+quoteSearchPathIdent(schema)+
			` authorization `+quoteSearchPathIdent(target.executionRole))
	}
	target.setSearchPath(t, "$user", target.secondSchema)
	return target
}

func cleanupPostgresSearchPathTarget(
	t *testing.T, target *postgresSearchPathTarget,
) {
	t.Helper()
	maintenance := openTargetPGNamed(
		t, target.host, fmt.Sprint(target.port), target.database,
		target.reviewUser, target.reviewPass, "pg_search_path_cleanup",
	)
	defer func() { _ = maintenance.Close() }()
	_, _ = maintenance.Exec(`
		select pg_terminate_backend(pid)
		from pg_stat_activity
		where datname = $1 and usename = $2 and pid <> pg_backend_pid()`,
		target.database, target.executionRole)
	_, _ = maintenance.Exec(`drop owned by ` + quoteSearchPathIdent(target.executionRole) + ` cascade`)
	_, _ = maintenance.Exec(`drop role if exists ` + quoteSearchPathIdent(target.executionRole))
}

func quoteSearchPathIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteSearchPathLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func (target *postgresSearchPathTarget) setSearchPath(
	t *testing.T, members ...string,
) {
	t.Helper()
	quoted := make([]string, len(members))
	for i, member := range members {
		if member == "$user" {
			quoted[i] = `"$user"`
		} else {
			quoted[i] = quoteSearchPathIdent(member)
		}
	}
	execTarget(t, target.admin, `alter role `+quoteSearchPathIdent(target.executionRole)+
		` in database `+quoteSearchPathIdent(target.database)+
		` set search_path to `+strings.Join(quoted, ", "))
}

func (target *postgresSearchPathTarget) createDatasource(
	t *testing.T, fx *b10Fixture, name string,
) string {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", map[string]any{
		"name": name, "engine": "postgresql", "compatibility_mode": "postgresql",
		"deployment_kind": "native", "host": target.host, "port": target.port,
		"database_name": target.database, "version_constraint": "16.x", "enabled": true,
		"credentials": []map[string]any{
			{
				"purpose": "review", "username": target.reviewUser,
				"password": map[string]any{"value": target.reviewPass},
			},
			{
				"purpose": "execution", "username": target.executionRole,
				"password": map[string]any{"value": target.executionPass},
			},
		},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create PostgreSQL datasource: %d %s", code, raw)
	}
	id := body["data"].(map[string]any)["id"].(string)
	if err := fx.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		strings.Repeat("ef", 32), id).Error; err != nil {
		t.Fatal(err)
	}
	target.installRealIdentityProbe(t, fx, id)
	return id
}

// installRealIdentityProbe keeps the general HTTP fixture deterministic while
// allowing these end-to-end tests to freeze and revalidate the real PostgreSQL
// identity used by the production search_path probe.
func (target *postgresSearchPathTarget) installRealIdentityProbe(
	t *testing.T, fx *b10Fixture, datasourceID string,
) {
	t.Helper()
	adapter := database.NewPostgreSQLAdapter(database.Endpoint{
		Host:             target.host,
		Port:             target.port,
		DatabaseName:     target.database,
		CompatibilityMod: "postgresql",
	}, database.AdapterOptions{})
	identity, err := adapter.ReadServerIdentity(
		context.Background(),
		database.Credential{
			Username: target.reviewUser,
			Password: target.reviewPass,
		},
		nil,
	)
	_ = adapter.Close()
	if err != nil {
		t.Fatalf("read real PostgreSQL identity: %v", err)
	}

	field := reflect.ValueOf(fx.svc).Elem().FieldByName("DatasourceIdentityProbe")
	if !field.IsValid() || !field.CanSet() {
		t.Fatal("application datasource identity probe seam unavailable")
	}
	previous := field
	replacement := reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
		row := args[3]
		if row.FieldByName("ID").String() != datasourceID {
			return previous.Call(args)
		}
		actual := *identity
		if databaseName := row.FieldByName("DatabaseName"); !databaseName.IsNil() {
			actual.DatabaseName = databaseName.Elem().String()
		}
		return []reflect.Value{
			reflect.ValueOf(actual),
			reflect.Zero(field.Type().Out(1)),
		}
	})
	field.Set(replacement)
}

func (target *postgresSearchPathTarget) createEmptyMappingFlow(
	t *testing.T, fx *b10Fixture, name, datasourceID string,
) string {
	t.Helper()
	stage := b9Stage(
		1, datasourceID,
		[][]string{{fx.reviewer1ID, fx.reviewer2ID}},
		[]string{fx.executorID},
	)
	flowID := fx.b9CreateFlow(t, name, []map[string]any{stage})
	fx.b9CreateGrant(t, fx.ownerID, flowID)
	return flowID
}

type searchPathRequestCapture struct {
	mu       sync.Mutex
	payloads []string
}

type postgresSearchPathFacts struct {
	*b9FakeFacts
	target *postgresSearchPathTarget
}

func (facts *postgresSearchPathFacts) TableMetadata(
	ctx context.Context, schema, table string,
) (map[string]any, error) {
	var present bool
	if err := facts.target.admin.QueryRowContext(ctx, `
		select exists (
			select 1
			from pg_catalog.pg_class c
			join pg_catalog.pg_namespace n on n.oid = c.relnamespace
			where n.nspname = $1 and c.relname = $2
		)`, schema, table).Scan(&present); err != nil {
		return nil, err
	}
	if present {
		return facts.b9FakeFacts.TableMetadata(ctx, schema, table)
	}
	return map[string]any{
		"schema": schema, "table": table, "existence": "absent",
		"columns":           []map[string]any{},
		"metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}

func (capture *searchPathRequestCapture) responder(
	fx *b10Fixture,
) func(provider.Request) ([]byte, int, error) {
	clean := fx.b9CleanDial()
	return func(req provider.Request) ([]byte, int, error) {
		raw, err := json.Marshal(req)
		if err != nil {
			return nil, 500, err
		}
		capture.mu.Lock()
		capture.payloads = append(capture.payloads, string(raw))
		capture.mu.Unlock()
		return clean(req)
	}
}

func (capture *searchPathRequestCapture) assertNoSecrets(
	t *testing.T, secrets ...string,
) {
	t.Helper()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	all := strings.Join(capture.payloads, "\n")
	for _, secret := range secrets {
		if secret != "" && strings.Contains(all, secret) {
			t.Fatalf("Provider request leaked trusted PostgreSQL identifier %q", secret)
		}
	}
}

// readEphemeralSearchPath reads the frozen PostgreSQL search_path the
// ephemeral precheck sealed into its in-memory stage_position=1 snapshot.
// After RCP-20261101 precheck writes zero DB rows: the snapshot lives in the EphemeralRun in-memory model
// and materializes to DB only upon submission -- assertions on what precheck froze read from here (read-only after terminal state).
func readEphemeralSearchPath(
	t *testing.T, fx *b10Fixture, runID string,
) reviewapp.FrozenPostgresSearchPath {
	t.Helper()
	er := fx.svc.EphemeralGetForTest(runID, fx.ownerID)
	if er == nil {
		t.Fatalf("ephemeral run %s missing from memory store", runID)
	}
	found := false
	var ct, nonce []byte
	var keyID, algorithm string
	for i := range er.Snapshots {
		if er.Snapshots[i].StagePosition != 1 {
			continue
		}
		ct = er.Snapshots[i].NormalizedContextCiphertext
		nonce = er.Snapshots[i].NormalizedContextNonce
		keyID = er.Snapshots[i].NormalizedContextKeyID
		algorithm = er.Snapshots[i].NormalizedContextAlgorithm
		found = true
		break
	}
	if !found {
		t.Fatalf("ephemeral run %s has no stage_position=1 snapshot", runID)
	}
	value := openStageContextForSearchPath(t, fx, runID, 1, ct, nonce, keyID, algorithm)
	raw, ok := value["postgres_search_path"]
	if !ok {
		t.Fatal("frozen PostgreSQL search_path missing")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var path reviewapp.FrozenPostgresSearchPath
	if err := json.Unmarshal(encoded, &path); err != nil {
		t.Fatalf("decode frozen search_path: %v", err)
	}
	if err := path.Validate(); err != nil {
		t.Fatalf("invalid frozen search_path: %v", err)
	}
	return path
}

func openStageContextForSearchPath(
	t *testing.T, fx *b10Fixture, runID string, position int32,
	ciphertext, nonce []byte, keyID, algorithm string,
) map[string]any {
	t.Helper()
	plain, err := fx.svc.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID, Algorithm: algorithm,
	}, crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(),
		Table:        "stage_review_snapshots",
		PrimaryKey:   fmt.Sprintf("%s#%d", runID, position),
		Column:       "normalized_context_ciphertext",
		Purpose:      crypto.PurposePayload,
	})
	if err != nil {
		t.Fatalf("open stage context: %v", err)
	}
	value := map[string]any{}
	if err := json.Unmarshal(plain, &value); err != nil {
		t.Fatalf("decode stage context: %v", err)
	}
	return value
}

func submitAndApproveSearchPathOrder(
	t *testing.T, fx *b10Fixture, flowID, statement string,
	respond func(provider.Request) ([]byte, int, error),
) (string, int64) {
	t.Helper()
	draftID, _, runID := fx.b9ReadyDraft(t, flowID, statement, respond)
	return approveSearchPathDraft(t, fx, flowID, draftID, runID, statement)
}

func approveSearchPathDraft(
	t *testing.T, fx *b10Fixture, flowID, draftID, runID, statement string,
) (string, int64) {
	t.Helper()
	code, body, raw := fx.b9Submit(t, flowID, draftID, runID, statement)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submit search_path order: %d %s", code, raw)
	}
	order := body["data"].(map[string]any)
	orderID := order["id"].(string)
	orderVersion := int64(order["version"].(float64))
	code, body, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve", "comment": "search-path-review"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, orderVersion),
			"Idempotency-Key": "pg-path-approve-" + uuid.NewString()[:8],
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("approve search_path order: %d %s", code, raw)
	}
	return orderID, fx.b10Version(t, orderID)
}

func TestPostgresSearchPathHTTPReviewSubmitAndDML(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "path_events_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key, amount integer)`)
	execTarget(t, target.admin,
		`insert into `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` values (1, 10)`)
	execTarget(t, target.admin,
		`grant select, update on `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` to `+quoteSearchPathIdent(target.executionRole))

	datasourceID := target.createDatasource(t, fx, "pg-search-path-dml")
	flowID := target.createEmptyMappingFlow(t, fx, "pg-search-path-dml-flow", datasourceID)
	capture := &searchPathRequestCapture{}
	statement := `update ` + table + ` set amount = amount + 5 where id = 1;`
	draftID, _, runID := fx.b9ReadyDraft(
		t, flowID, statement, capture.responder(fx),
	)
	path := readEphemeralSearchPath(t, fx, runID)
	if path.CurrentUser != target.executionRole {
		t.Fatalf("frozen current_user = %q, want execution role %q",
			path.CurrentUser, target.executionRole)
	}
	if want := []string{target.firstSchema, target.secondSchema}; !equalStringList(path.Explicit, want) {
		t.Fatalf("frozen explicit path = %v, want %v", path.Explicit, want)
	}
	if path.CreationSchema != target.firstSchema {
		t.Fatalf("creation schema = %q, want %q", path.CreationSchema, target.firstSchema)
	}
	if !containsSearchPathMember(path.Effective, "pg_catalog") {
		t.Fatalf("effective path lacks implicit pg_catalog: %v", path.Effective)
	}

	code, body, raw := fx.b9Submit(t, flowID, draftID, runID, statement)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submit: %d %s", code, raw)
	}
	for _, secret := range []string{
		target.executionRole, target.firstSchema, target.secondSchema, table,
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("submission response leaked %q: %s", secret, raw)
		}
	}
	order := body["data"].(map[string]any)
	orderID := order["id"].(string)
	orderVersion := int64(order["version"].(float64))
	code, body, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve", "comment": "go"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, orderVersion),
			"Idempotency-Key": "pg-path-main-approve-" + uuid.NewString()[:8],
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("approve: %d %s", code, raw)
	}
	version := fx.b10Version(t, orderID)
	code, body, raw = fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("begin: %d %s", code, raw)
	}
	attemptID := body["data"].(map[string]any)["id"].(string)
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		attemptState, boundary, _, _ := fx.b10AttemptRow(t, attemptID)
		var failure sql.NullString
		if err := fx.db.Raw(`select failure_name from execution_attempts where id = ?`, attemptID).
			Scan(&failure).Error; err != nil {
			t.Fatal(err)
		}
		t.Fatalf("order state = %q, want completed (attempt=%s boundary=%s failure=%s statements=%+v)",
			got, attemptState, boundary, failure.String, fx.b10Statements(t, attemptID))
	}
	rows := queryTarget(t, target.admin,
		`select amount from `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` where id = 1`)
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "15" {
		t.Fatalf("later-schema DML result = %v, want [[15]]", rows)
	}
	capture.assertNoSecrets(t,
		target.executionRole, target.firstSchema, target.secondSchema, table,
	)
}

func TestPostgresSearchPathHTTPEarlierViewShadowingFailsClosed(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "shadowed_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	first := quoteSearchPathIdent(target.firstSchema)
	second := quoteSearchPathIdent(target.secondSchema)
	execTarget(t, target.admin,
		`create view `+first+`.`+quoteSearchPathIdent(table)+` as select 1 as id, 10 as amount`)
	execTarget(t, target.admin,
		`create table `+second+`.`+quoteSearchPathIdent(table)+` (id integer primary key, amount integer)`)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-shadow")
	flowID := target.createEmptyMappingFlow(t, fx, "pg-search-path-shadow-flow", datasourceID)

	statement := `update ` + table + ` set amount = 20 where id = 1;`
	draftID, _, runID := startSearchPathDraftReview(t, fx, flowID, statement)
	run := fx.b9WaitRunTerminal(t, runID)
	if run["state"] == "ready" {
		t.Fatalf("earlier view shadowing must fail closed: run state=%v failure=%v stages=%v",
			run["state"], run["failure_code"], run["stage_results"])
	}
	code, body, raw := fx.b9Submit(t, flowID, draftID, runID, statement)
	if code != 200 || errCode(body) != 2001 {
		t.Fatalf("shadowed relation submission code = %d/%v, want 200/2001: %s",
			code, errCode(body), raw)
	}
	var orders int64
	if err := fx.db.Raw(`select count(*) from change_orders`).Scan(&orders).Error; err != nil {
		t.Fatal(err)
	}
	if orders != 0 {
		t.Fatalf("shadowed relation unexpectedly created %d order(s)", orders)
	}
}

func TestPostgresSearchPathHTTPDDLUsesFrozenCreationSchema(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-ddl")
	flowID := target.createEmptyMappingFlow(t, fx, "pg-search-path-ddl-flow", datasourceID)
	facts := &postgresSearchPathFacts{
		b9FakeFacts: &b9FakeFacts{},
		target:      target,
	}
	firstTable := "created_first_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	secondTable := "created_second_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	statement := `create table ` + firstTable + ` (id integer, amount integer);` + "\n" +
		`create table ` + secondTable + ` (id integer, amount integer);`
	draftID, _, runID := searchPathReadyDraftWithFacts(
		t, fx, flowID, statement, fx.b9CleanDial(), facts,
	)
	orderID, version := approveSearchPathDraft(t, fx, flowID, draftID, runID, statement)
	code, body, raw := fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("begin DDL: %d %s", code, raw)
	}
	attemptID := body["data"].(map[string]any)["id"].(string)
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive DDL: %v", err)
	}
	for _, table := range []string{firstTable, secondTable} {
		rows := queryTarget(t, target.admin, `
			select to_regclass($1) is not null`,
			target.firstSchema+"."+table)
		if len(rows) != 1 || rows[0][0] != "true" {
			t.Fatalf("unqualified DDL did not create %s in current_schema: %v", table, rows)
		}
		rows = queryTarget(t, target.admin, `
			select to_regclass($1) is not null`,
			target.secondSchema+"."+table)
		if len(rows) != 1 || rows[0][0] != "false" {
			t.Fatalf("unqualified DDL leaked into later schema for %s: %v", table, rows)
		}
	}
}

func TestPostgresSearchPathHTTPSubmissionDriftRejected(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "drift_submit_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key, amount integer)`)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-submit-drift")
	flowID := target.createEmptyMappingFlow(
		t, fx, "pg-search-path-submit-drift-flow", datasourceID,
	)
	statement := `update ` + table + ` set amount = 1 where id = 1;`
	draftID, _, runID := fx.b9ReadyDraft(t, flowID, statement, fx.b9CleanDial())
	// The precheck froze the execution-role search_path in the ephemeral
	// snapshot. Repointing the role afterwards makes the submission-time
	// re-probe detect drift and reject with REVIEW_OUTDATED.
	target.setSearchPath(t, target.secondSchema, target.firstSchema)
	code, body, raw := fx.b9Submit(t, flowID, draftID, runID, statement)
	if code != 200 || errCode(body) != 2001 {
		t.Fatalf("submission path drift code = %d/%v, want 200/2001: %s",
			code, errCode(body), raw)
	}
}

func TestPostgresSearchPathHTTPSnapshotStageBindingMismatchRejectsBegin(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "binding_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key)`)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-bind-mismatch")
	flowID := target.createEmptyMappingFlow(
		t, fx, "pg-search-path-bind-mismatch-flow", datasourceID,
	)
	orderID, version := submitAndApproveSearchPathOrder(t, fx, flowID,
		`update `+table+` set id = id where id = 1;`, fx.b9CleanDial())

	if err := fx.db.Exec(`
		update stage_review_snapshots s
		set stage_position = s.stage_position + 1
		from order_stages os
		where os.order_id = ? and os.stage_review_snapshot_id = s.id`, orderID).Error; err != nil {
		t.Fatal(err)
	}
	code, body, raw := fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 3006 {
		t.Fatalf("snapshot binding mismatch code = %d/%v, want 200/3006: %s",
			code, errCode(body), raw)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("snapshot binding mismatch order state = %s, want failed", got)
	}
	var reason string
	if err := fx.db.Raw(`
		select event_data->>'reason_code' from domain_events
		where subject = ? and event_type = 'execution.failed'
		order by occurred_at desc limit 1`,
		"change-orders/"+orderID).Scan(&reason).Error; err != nil {
		t.Fatal(err)
	}
	if reason != "postgres_search_path_snapshot_mismatch" {
		t.Fatalf("snapshot binding mismatch reason = %q, want preserved mismatch", reason)
	}
	var attempts int
	if err := fx.db.Raw(
		`select count(*) from execution_attempts where order_id = ?`, orderID,
	).Scan(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("begin-time binding mismatch created %d attempts", attempts)
	}
}

func TestPostgresSearchPathHTTPScheduledDriftFailsOriginalOrder(t *testing.T) {
	fx := newB11Fixture(t)
	target := newPostgresSearchPathTarget(t, fx.b10Fixture)
	table := "scheduled_drift_" +
		strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key)`)
	datasourceID := target.createDatasource(
		t, fx.b10Fixture, "pg-search-path-scheduled-drift",
	)
	flowID := target.createEmptyMappingFlow(
		t, fx.b10Fixture, "pg-search-path-scheduled-drift-flow", datasourceID,
	)
	orderID, version := submitAndApproveSearchPathOrder(t, fx.b10Fixture, flowID,
		`update `+table+` set id = id where id = 1;`, fx.b9CleanDial())
	if code, body, raw := fx.b11CreateScheduleHTTP(
		t, orderID, version, time.Now().Add(10*time.Minute),
	); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule search_path order: %d %s", code, raw)
	}
	if err := fx.db.Exec(`
		update execution_schedules
		set scheduled_for = now() - interval '2 seconds'
		where order_id = ?`, orderID).Error; err != nil {
		t.Fatal(err)
	}
	target.setSearchPath(t, target.secondSchema, target.firstSchema)

	res, err := fx.exec.ProcessDueSchedules(context.Background())
	if err != nil {
		t.Fatalf("scheduled search_path tick: %v", err)
	}
	if res.Blocked != 1 || res.Claimed != 0 {
		t.Fatalf("scheduled search_path result = %+v, want one blocked", res)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("scheduled drift order state = %s, want failed", got)
	}
	var scheduleState, reason string
	if err := fx.db.Raw(
		`select state from execution_schedules where order_id = ?`, orderID,
	).Scan(&scheduleState).Error; err != nil {
		t.Fatal(err)
	}
	if scheduleState != "blocked" {
		t.Fatalf("scheduled drift schedule state = %s, want blocked", scheduleState)
	}
	if err := fx.db.Raw(`
		select event_data->>'reason_code' from domain_events
		where subject = ? and event_type = 'execution.failed'
		order by occurred_at desc limit 1`,
		"change-orders/"+orderID).Scan(&reason).Error; err != nil {
		t.Fatal(err)
	}
	if reason != "postgres_search_path_changed" {
		t.Fatalf("scheduled drift reason = %q, want path drift", reason)
	}
	var attempts int
	if err := fx.db.Raw(
		`select count(*) from execution_attempts where order_id = ?`, orderID,
	).Scan(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("scheduled drift created %d attempts", attempts)
	}
}

func TestPostgresSearchPathHTTPScheduledRejectionEventFailureRollsBackTerminalState(
	t *testing.T,
) {
	fx := newB11Fixture(t)
	target := newPostgresSearchPathTarget(t, fx.b10Fixture)
	table := "scheduled_atomic_" +
		strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key)`)
	datasourceID := target.createDatasource(
		t, fx.b10Fixture, "pg-search-path-scheduled-atomic",
	)
	flowID := target.createEmptyMappingFlow(
		t, fx.b10Fixture, "pg-search-path-scheduled-atomic-flow", datasourceID,
	)
	orderID, version := submitAndApproveSearchPathOrder(t, fx.b10Fixture, flowID,
		`update `+table+` set id = id where id = 1;`, fx.b9CleanDial())
	if code, body, raw := fx.b11CreateScheduleHTTP(
		t, orderID, version, time.Now().Add(10*time.Minute),
	); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule search_path order: %d %s", code, raw)
	}
	if err := fx.db.Exec(`
		update execution_schedules
		set scheduled_for = now() - interval '2 seconds'
		where order_id = ?`, orderID).Error; err != nil {
		t.Fatal(err)
	}

	var orderBefore, stageBefore, scheduleBefore string
	if err := fx.db.Raw(
		`select state from change_orders where id = ?`, orderID,
	).Scan(&orderBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(`
		select s.state
		from stage_review_snapshots s
		join order_stages os on os.stage_review_snapshot_id = s.id
		where os.order_id = ? order by s.stage_position limit 1`, orderID,
	).Scan(&stageBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(
		`select state from execution_schedules where order_id = ?`, orderID,
	).Scan(&scheduleBefore).Error; err != nil {
		t.Fatal(err)
	}

	constraint := "reject_search_path_execution_failed_" + orderID[:8]
	if err := fx.db.Exec(`
		alter table domain_events
		add constraint ` + quoteSearchPathIdent(constraint) + `
		check (event_type <> 'execution.failed')`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = fx.db.Exec(
			`alter table domain_events drop constraint if exists ` +
				quoteSearchPathIdent(constraint),
		).Error
	})
	target.setSearchPath(t, target.secondSchema, target.firstSchema)

	res, tickErr := fx.exec.ProcessDueSchedules(context.Background())

	var orderAfter, stageAfter, scheduleAfter string
	if err := fx.db.Raw(
		`select state from change_orders where id = ?`, orderID,
	).Scan(&orderAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(`
		select s.state
		from stage_review_snapshots s
		join order_stages os on os.stage_review_snapshot_id = s.id
		where os.order_id = ? order by s.stage_position limit 1`, orderID,
	).Scan(&stageAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(
		`select state from execution_schedules where order_id = ?`, orderID,
	).Scan(&scheduleAfter).Error; err != nil {
		t.Fatal(err)
	}
	if orderAfter != orderBefore || stageAfter != stageBefore ||
		scheduleAfter != scheduleBefore {
		t.Fatalf(
			"partial terminal state: before order/stage/schedule=%s/%s/%s after=%s/%s/%s",
			orderBefore, stageBefore, scheduleBefore,
			orderAfter, stageAfter, scheduleAfter,
		)
	}
	var attempts int
	if err := fx.db.Raw(
		`select count(*) from execution_attempts where order_id = ?`, orderID,
	).Scan(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("failed terminalization created %d attempts", attempts)
	}
	var eventTypes []string
	if err := fx.db.Raw(`
		select event_type from domain_events
		where subject = ? order by sequence`, "change-orders/"+orderID).
		Scan(&eventTypes).Error; err != nil {
		t.Fatal(err)
	}
	if tickErr == nil {
		t.Fatalf(
			"scheduled rejection must surface the injected event write failure; result=%+v events=%v",
			res, eventTypes,
		)
	}
	if res.Blocked != 0 || res.Claimed != 0 || res.Skipped != 1 {
		t.Fatalf("failed terminalization result = %+v, want one skipped", res)
	}
}

func TestPostgresSearchPathHTTPBeginDriftFailsOriginalOrder(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "begin_drift_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key)`)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-begin-drift")
	flowID := target.createEmptyMappingFlow(
		t, fx, "pg-search-path-begin-drift-flow", datasourceID,
	)
	orderID, version := submitAndApproveSearchPathOrder(t, fx, flowID,
		`update `+table+` set id = id where id = 1;`, fx.b9CleanDial())

	target.setSearchPath(t, target.secondSchema, target.firstSchema)
	code, body, raw := fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 3006 {
		t.Fatalf("begin-time drift code = %d/%v, want 200/3006: %s",
			code, errCode(body), raw)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("begin-time drift order state = %s, want failed", got)
	}
	var attempts int
	if err := fx.db.Raw(
		`select count(*) from execution_attempts where order_id = ?`, orderID,
	).Scan(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("begin-time drift created %d attempts", attempts)
	}
}

func TestPostgresSearchPathHTTPPreSendDriftSendsNoSQL(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "drift_exec_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	qualified := quoteSearchPathIdent(target.secondSchema) + `.` + quoteSearchPathIdent(table)
	execTarget(t, target.admin, `create table `+qualified+` (id integer primary key, amount integer)`)
	execTarget(t, target.admin, `insert into `+qualified+` values (1, 10)`)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-exec-drift")
	flowID := target.createEmptyMappingFlow(t, fx, "pg-search-path-exec-drift-flow", datasourceID)
	orderID, version := submitAndApproveSearchPathOrder(t, fx, flowID,
		`update `+table+` set amount = 99 where id = 1;`, fx.b9CleanDial())

	// The review, submit and begin checks pass. Change the role only after the
	// Runner has opened the target and passed its early probe, proving the final
	// check immediately before the first DML statement rejects newly introduced
	// drift.
	code, body, raw := fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("begin before drift: %d %s", code, raw)
	}
	attemptID := body["data"].(map[string]any)["id"].(string)
	drifted := false
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterTargetResolved: func(string) {
			drifted = true
			target.setSearchPath(t, target.secondSchema, target.firstSchema)
		},
	})
	defer fx.runner.SetFaultHooks(nil)
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive drift attempt: %v", err)
	}
	if !drifted {
		t.Fatal("test did not exercise drift after target resolution")
	}
	state, boundary, _, _ := fx.b10AttemptRow(t, attemptID)
	if state != "failed" || boundary != "not_started" {
		t.Fatalf("Runner drift attempt state/boundary = %s/%s, want failed/not_started",
			state, boundary)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("Runner drift order state = %s, want failed", got)
	}
	statements := fx.b10Statements(t, attemptID)
	for _, statement := range statements {
		if statement.State != "not_started" {
			t.Fatalf("Runner pre-send drift advanced statement ledger: %+v", statements)
		}
	}
	rows := queryTarget(t, target.admin, `select amount from `+qualified+` where id = 1`)
	if len(rows) != 1 || rows[0][0] != "10" {
		t.Fatalf("pre-send drift changed target data: %v", rows)
	}
}

func TestPostgresSearchPathHTTPPreSendDriftAfterTargetOpenRejectsDDL(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	datasourceID := target.createDatasource(t, fx, "pg-search-path-ddl-target-drift")
	flowID := target.createEmptyMappingFlow(
		t, fx, "pg-search-path-ddl-target-drift-flow", datasourceID,
	)
	table := "target_drift_ddl_" +
		strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	facts := &postgresSearchPathFacts{
		b9FakeFacts: &b9FakeFacts{},
		target:      target,
	}
	statement := `create table ` + quoteSearchPathIdent(table) +
		` (id integer primary key, amount integer);`
	draftID, _, runID := searchPathReadyDraftWithFacts(
		t, fx, flowID, statement, fx.b9CleanDial(), facts,
	)
	orderID, version := approveSearchPathDraft(t, fx, flowID, draftID, runID, statement)
	code, body, raw := fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("begin DDL before drift: %d %s", code, raw)
	}
	attemptID := body["data"].(map[string]any)["id"].(string)
	drifted := false
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterTargetResolved: func(string) {
			drifted = true
			target.setSearchPath(t, target.secondSchema, target.firstSchema)
		},
	})
	defer fx.runner.SetFaultHooks(nil)
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive DDL drift attempt: %v", err)
	}
	if !drifted {
		t.Fatal("test did not exercise DDL drift after target resolution")
	}
	state, boundary, _, _ := fx.b10AttemptRow(t, attemptID)
	if state != "failed" || boundary != "not_started" {
		t.Fatalf("DDL drift attempt state/boundary = %s/%s, want failed/not_started",
			state, boundary)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("DDL drift order state = %s, want failed", got)
	}
	for _, statement := range fx.b10Statements(t, attemptID) {
		if statement.State != "not_started" {
			t.Fatalf("DDL path drift advanced statement ledger: %+v", statement)
		}
	}
	for _, schema := range []string{target.firstSchema, target.secondSchema} {
		qualified := quoteSearchPathIdent(schema) + "." + quoteSearchPathIdent(table)
		rows := queryTarget(t, target.admin,
			`select to_regclass($1) is not null`, qualified)
		if len(rows) != 1 || rows[0][0] != "false" {
			t.Fatalf("DDL path drift created %s: %v", qualified, rows)
		}
	}
}

// startSearchPathDraftReview creates draft + PUTs sql + starts ephemeral precheck and polls to terminal state
// for negative-path cases (where fail-closed terminal state is not ready, so ready is intentionally not asserted).
// Returns (draftID, version, runID); version is the draft version after PUT sql.
func startSearchPathDraftReview(
	t *testing.T, fx *b10Fixture, flowID, statement string,
) (string, int64, string) {
	t.Helper()
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": flowID, "title": "pg-search-path-negative", "description": "negative",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create negative draft: %d %s", code, raw)
	}
	draft := body["data"].(map[string]any)
	draftID := draft["id"].(string)
	version := int64(draft["version"].(float64))
	code, body, raw = fx.owner.doWithVersion("PUT",
		"/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": statement}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put negative SQL: %d %s", code, raw)
	}
	version++
	runID := fx.b9StartPrecheck(t, flowID, draftID, statement, fx.b9CleanDial())
	fx.b9WaitRunTerminal(t, runID)
	return draftID, version, runID
}
