//go:build integration

// integration_test.go B6 acceptance gate tests (real PostgreSQL):
//
//	G1 FakeRunner completes full state flow;
//	G2 Duplicate requests produce no duplicate active Runs or events;
//	G3 Worker interruption recovers only safe steps;
//	G4 Raw Evidence seven-day boundary.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func integrationDSN(t *testing.T) string {
	t.Helper()
	return testkit.PGDSN(t)
}

type fx struct {
	db     *gorm.DB
	svc    *Services
	ret    *RetentionService
	userID string
	flowID string
	owner  Actor
}

func newFixture(t *testing.T) *fx {
	t.Helper()
	db := testkit.OpenPG(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("postgres sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(32)
	sqlDB.SetMaxIdleConns(8)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	truncateReviewTables(t, db)

	crypter := testkit.NewCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	svc, err := NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.DatasourceIdentityProbe = deterministicTestDatasourceIdentity
	ret, err := NewRetentionService(db)
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{db: db, svc: svc, ret: ret, userID: uuid.NewString(), owner: Actor{UserID: uuid.NewString(), Username: "alice"}}
	f.flowID = f.seedFlow(t)
	return f
}

func deterministicTestDatasourceIdentity(
	_ context.Context, _ *gorm.DB, _ *crypto.Crypter, row datasourceTargetRow,
) (database.ServerIdentity, error) {
	databaseName := ""
	if row.DatabaseName != nil {
		databaseName = *row.DatabaseName
	}
	identity := database.ServerIdentity{
		EngineFamily:         row.CompatibilityMode,
		DatabaseName:         databaseName,
		ConfiguredEndpointID: fmt.Sprintf("%s:%d", row.Host, row.Port),
	}
	switch row.CompatibilityMode {
	case "mysql":
		identity.ServerVersion = "8.0.36"
		identity.InstanceMarker = "mysql:itest-" + row.ID
		identity.LowerCaseTableNames = "0"
	case "postgresql":
		identity.ServerVersion = "17.2"
		identity.SystemIdentifier = "itest-" + row.ID
		identity.InstanceMarker = "pg:" + identity.SystemIdentifier
		identity.DatabaseOID = 42
	default:
		return database.ServerIdentity{}, ErrFactSourceUnavailable
	}
	return identity, nil
}

func truncateReviewTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	tables := []string{
		"evidence_raw_payloads", "evidence_facts", "review_finding_evidence", "review_findings",
		"stage_review_snapshots", "sql_statements", "sql_fingerprint_groups", "review_runs",
		"change_drafts", "draft_sql_payloads", "async_tasks", "idempotency_records",
		"domain_events", "outbox_events", "audit_events",
		"flow_approval_step_actors", "flow_execution_actors", "flow_approval_steps",
		"flow_stages", "flows", "permission_group_flow_grants", "permission_group_members",
		"permission_groups", "datasource_credentials", "datasource_tls_materials",
		"datasources", "rule_sets", "prompt_tools", "users",
	}
	for _, tbl := range tables {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
}

// seedFlow directly inserts flow / datasource / rule set / grant rows (bypassing the B3 admin surface to focus on the B6 domain).
func (f *fx) seedFlow(t *testing.T) string {
	t.Helper()
	dsID := uuid.NewString()
	if err := f.db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b6-ds', 'mysql', 'mysql', 'native', 'db.example.com', 3306, 'shop', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ab", 32), f.userID).Error; err != nil {
		t.Fatalf("seed datasource: %v", err)
	}
	// B8 ruling: a rule set is a pure Prompt Tool composition with rules always {}; business findings
	// come exclusively from skills (model path), so the model-less Fake path produces no business findings.
	ruleView, err := f.svc.CreateRuleSet(context.Background(), f.userID, "b6-rules-"+uuid.NewString()[:8], true,
		[]byte(`{}`), nil)
	if err != nil {
		t.Fatalf("seed rule set: %v", err)
	}
	ruleID := ruleView.ID
	flowID := uuid.NewString()
	if err := f.db.Exec(`insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, 'b6-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, ruleID, f.userID).Error; err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	stageID := uuid.NewString()
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: stages no longer seal Schema mappings;
	// tool scope = datasources.database_name ('shop').
	// The NOT NULL mapping ciphertext column is retained; its content is no longer consumed, so dummy placeholder bytes are inserted.
	if err := f.db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 1, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageID, flowID, dsID, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	stepID := uuid.NewString()
	if err := f.db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position)
		values (?, ?, ?, 1)`, stepID, flowID, stageID).Error; err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := f.db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`,
		stepID, f.userID).Error; err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	if err := f.db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`,
		stageID, f.userID).Error; err != nil {
		t.Fatalf("seed exec actor: %v", err)
	}
	groupID := uuid.NewString()
	if err := f.db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'b6-group', true, 1, ?, now(), now())`, groupID, f.userID).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := f.db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`,
		groupID, f.owner.UserID).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := f.db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`,
		groupID, flowID).Error; err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	return flowID
}

// createDraftWithSQL is a shortcut helper: creates a draft and replaces its SQL.
func (f *fx) createDraftWithSQL(t *testing.T, sql string) *DraftView {
	t.Helper()
	view, err := f.svc.CreateDraft(context.Background(), f.owner, CreateDraftInput{
		FlowID: f.flowID, Title: "b6", Description: "",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	view, err = f.svc.ReplaceSQL(context.Background(), f.owner, view.ID, []byte(sql), view.Version)
	if err != nil {
		t.Fatalf("replace sql: %v", err)
	}
	return view
}

// startEphemeralTerminal is the ephemeral precheck driver (RCP-20261101-EPHEMERAL-PRECHECK
// keystone): StartEphemeralRun executes the review in a goroutine and polls until a terminal state,
// returning the in-memory Run and runID. Replaces the legacy DB task chain (StartRun+drain is retired).
func (f *fx) startEphemeralTerminal(t *testing.T, actor Actor, deps AgentDeps, in EphemeralStartInput) (*EphemeralRun, string) {
	t.Helper()
	runID, err := f.svc.StartEphemeralRun(context.Background(), actor, NewAgentRunner(f.svc, deps), in)
	if err != nil {
		t.Fatalf("start ephemeral run: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		er := f.svc.EphemeralGetForTest(runID, actor.UserID)
		if er != nil && er.TerminalState() {
			return er, runID
		}
		if time.Now().After(deadline) {
			state := "missing"
			if er != nil {
				state = string(er.State())
			}
			// A panic inside the goroutine manifests as a poll timeout: fail with context via Fatal for easier debugging.
			t.Fatalf("ephemeral run %s not terminal (state=%s)", runID, state)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// materializeEphemeral materializes on submit: restores the legacy DB assertion surface (review_runs /
// findings / evidence / snapshots / tool calls) after materialization. Runs that fail during grouping
// are not materialized (the fingerprint_group_count CHECK on review_runs only holds grouped inputs)
// and are asserted against the in-memory model.
func (f *fx) materializeEphemeral(t *testing.T, er *EphemeralRun, draftID string, revision int64) {
	t.Helper()
	if err := f.db.Transaction(func(tx *gorm.DB) error {
		return f.svc.MaterializeEphemeralRunTx(context.Background(), tx, er, draftID, revision)
	}); err != nil {
		t.Fatalf("materialize ephemeral run: %v", err)
	}
}

// ephemeralRunCode returns the business error code of an in-memory Run (or nil if no error).
func ephemeralRunCode(er *EphemeralRun) *int32 {
	if er.Run.BusinessErrCode == nil {
		return nil
	}
	code := *er.Run.BusinessErrCode
	return &code
}

// ephemeralRunCodeString formats the error code for debug-friendly output.
func ephemeralRunCodeString(er *EphemeralRun) string {
	if code := ephemeralRunCode(er); code != nil {
		return fmt.Sprint(*code)
	}
	return "nil"
}

func count(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("count %s: %v", query, err)
	}
	return n
}

// ---- G1: Ephemeral precheck full state flow (RCP-20261101 migration; legacy FakeRunner task chain retired) ----

// TestGate1EphemeralFullStateFlow benign SQL -> ready; revising SQL -> old Run becomes outdated;
// fingerprint cap exceeded -> in-memory terminal state failed (not materialized). RCP-20261101:
// draft state transitions retired with the state machine — terminal semantics live on the Run row
// (materialized row or er in-memory model); the ephemeral path emits no domain events, retiring legacy event/outbox assertions with the task chain.
func TestGate1EphemeralFullStateFlow(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()

	// Phase 1: benign SQL -> ready.
	sqlText := "UPDATE orders SET amount = amount + 1 WHERE id = 1;\nINSERT INTO orders (id) VALUES (1);\nSELECT id FROM orders WHERE id = 1;"
	draft := b.createDraftWithSQL(t, sqlText)
	er, runID := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText), IdempotencyKey: "k-ready-1"})
	if er.State() != domain.RunStateReady {
		t.Fatalf("benign run want ready, got %s (code=%s)", er.State(), ephemeralRunCodeString(er))
	}
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	run, err := b.svc.GetRunOwned(ctx, b.owner, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.State != string(domain.RunStateReady) {
		t.Fatalf("benign run want ready, got %s (gate=%+v failure=%v)", run.State, run.Gate, run.FailureCode)
	}
	// R009: statements and groups are persisted.
	if n := count(t, b.db, `select count(*) from sql_statements where review_run_id = ?`, runID); n != 3 {
		t.Fatalf("statements want 3, got %d", n)
	}
	if n := count(t, b.db, `select count(*) from sql_fingerprint_groups where review_run_id = ?`, runID); n != 3 {
		t.Fatalf("groups want 3, got %d", n)
	}
	// Stage snapshots: 1 passed snapshot per flow stage.
	if n := count(t, b.db, `select count(*) from stage_review_snapshots where review_run_id = ? and state = 'passed'`, runID); n != 1 {
		t.Fatalf("stage snapshots want 1 passed, got %d", n)
	}

	// Phase 2: revising SQL -> old Run becomes outdated (invalidateActiveRuns invalidates materialized rows by revision;
	// RCP-20261101: draft state transitions retired with the state machine).
	view2, err := b.svc.ReplaceSQL(ctx, b.owner, draft.ID, []byte("UPDATE orders SET amount = amount + 2 WHERE id = 2;"), draft.Version)
	if err != nil {
		t.Fatalf("revise: %v", err)
	}
	if view2.State != string(domain.DraftStateDraft) || view2.Revision != 3 {
		t.Fatalf("after revise want draft/rev3, got %s/%d", view2.State, view2.Revision)
	}
	oldRun, _ := b.svc.GetRunOwned(ctx, b.owner, runID)
	if oldRun.State != string(domain.RunStateOutdated) {
		t.Fatalf("old run want outdated, got %s", oldRun.State)
	}

	// Phase 3: risky SQL (UPDATE without WHERE) -> when the model path produces no business findings,
	// a complete technical Gate results in ready (B8 ruling: business findings come only from skills;
	// the blocking path is covered by B7/B8 skill + model gates).
	draft3 := b.createDraftWithSQL(t, "UPDATE orders SET amount = 0;")
	er3, runID3 := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft3.ID, SQL: []byte("UPDATE orders SET amount = 0;"), IdempotencyKey: "k-blocked-1"})
	if er3.State() != domain.RunStateReady {
		t.Fatalf("no-finding run want ready, got %s (code=%s)", er3.State(), ephemeralRunCodeString(er3))
	}
	b.materializeEphemeral(t, er3, draft3.ID, draft3.Revision)
	run3, _ := b.svc.GetRunOwned(ctx, b.owner, runID3)
	if run3.State != string(domain.RunStateReady) || !run3.Gate.Passed {
		t.Fatalf("no-finding run want ready/pass, got %s gate=%+v", run3.State, run3.Gate)
	}
	if n := count(t, b.db, `select count(*) from review_findings`); n != 0 {
		t.Fatalf("clean responder must not synthesize business findings, got %d", n)
	}

	// Phase 4: unique fingerprint cap exceeded -> grouping fails closed: in-memory terminal state failed/2006,
	// not materialized (the fingerprint_group_count CHECK on review_runs only holds grouped inputs).
	var tooMany strings.Builder
	for i := 1; i <= 1001; i++ {
		if i > 1 {
			tooMany.WriteString("\n")
		}
		tooMany.WriteString("UPDATE orders SET amount = ")
		for j := 1; j <= i; j++ {
			if j > 1 {
				tooMany.WriteString(", ")
			}
			tooMany.WriteString(fmt.Sprintf("c%d = %d", j, j))
		}
		tooMany.WriteString(" WHERE id = 1;")
	}
	tooManySQL := tooMany.String()
	draft4 := b.createDraftWithSQL(t, tooManySQL)
	er4, runID4 := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft4.ID, SQL: []byte(tooManySQL), IdempotencyKey: "k-failed-1"})
	if er4.State() != domain.RunStateFailed {
		t.Fatalf("too-many fingerprints want failed, got %s (code=%s)", er4.State(), ephemeralRunCodeString(er4))
	}
	if code := ephemeralRunCode(er4); code == nil || *code != domain.CodeTooManyFingerprints {
		t.Fatalf("too-many fingerprints want %d, got %s", domain.CodeTooManyFingerprints, ephemeralRunCodeString(er4))
	}
	// Grouping failure produces no run row (not materialized = no row).
	if _, err := b.svc.GetRunOwned(ctx, b.owner, runID4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed run row must not exist, got %v", err)
	}
}

// ---- G2: Ephemeral idempotency (RCP-20261101: in-memory window prevents double-click double-runs) ----

// TestGate2IdempotentStartRun replaying the same key reuses the same Run; replaying after terminal state
// still returns the same Run; concurrent active Run cap returns 1012. RCP-20261101: legacy DB idempotency
// records (request hash comparison 1007, reviewing draft 1010) retired along with StartRun — ephemeral
// idempotency is an in-process window where the same key always replays the first Run (consistent with post-migration httpapi semantics).
func TestGate2IdempotentStartRun(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	sqlText := "update orders set amount = amount + 1 where id = 1;\n"
	draft := b.createDraftWithSQL(t, sqlText)

	startWithKey := func(key string) (string, error) {
		runID, err := b.svc.StartEphemeralRun(ctx, b.owner,
			NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial)), EphemeralStartInput{
				FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText), IdempotencyKey: key,
			})
		return runID, err
	}
	first, err := startWithKey("dup-key-1")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, err := startWithKey("dup-key-1")
	if err != nil {
		t.Fatalf("duplicate start must replay, got %v", err)
	}
	if second != first {
		t.Fatalf("replay mismatch: %s vs %s", second, first)
	}

	// Replaying after reaching a terminal state still returns the same Run (in-memory copy remains readable within the TTL window).
	deadline := time.Now().Add(20 * time.Second)
	var er *EphemeralRun
	for {
		er = b.svc.EphemeralGetForTest(first, b.owner.UserID)
		if er != nil && er.TerminalState() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run not terminal: %v", er)
		}
		time.Sleep(25 * time.Millisecond)
	}
	third, err := startWithKey("dup-key-1")
	if err != nil {
		t.Fatalf("post-terminal replay: %v", err)
	}
	if third != first {
		t.Fatalf("post-complete replay mismatch: %s vs %s", third, first)
	}
	// Idempotency prevents duplicate execution: there is only one terminal Run (uniquely materialized).
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	if n := count(t, b.db, `select count(*) from review_runs where id = ?`, first); n != 1 {
		t.Fatalf("idempotent start must not create second run, got %d", n)
	}

	// Concurrent active Run cap (under ephemeral semantics, the capacity guard equivalent to reviewing 1010 is 1012):
	// suspend 10 active Runs with a blocking dial (maxUserActiveRuns=10), and verify the 11th is rejected.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	hang := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		<-block
		raw, _ := json.Marshal(map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review passed.", "findings": []any{}, "tool_requests": []any{},
		})
		return raw, 200, nil
	})
	hangSQL := "select 1;\n"
	for i := 0; i < maxUserActiveRuns; i++ {
		if _, err := b.svc.StartEphemeralRun(ctx, b.owner, NewAgentRunner(b.svc, hang), EphemeralStartInput{
			FlowID: b.flowID, SQL: []byte(hangSQL), IdempotencyKey: fmt.Sprintf("hang-%d-%s", i, runKeySuffix()),
		}); err != nil {
			t.Fatalf("seed active run %d: %v", i, err)
		}
	}
	if _, err := b.svc.StartEphemeralRun(ctx, b.owner, NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial)), EphemeralStartInput{
		FlowID: b.flowID, SQL: []byte(hangSQL), IdempotencyKey: "cap-" + runKeySuffix(),
	}); !domain.IsCode(err, 1012) {
		t.Fatalf("active cap want 1012, got %v", err)
	}
}

// ---- G3: Worker interruption recovery removed under RCP-20261101 ----

// TestGate3WorkerInterruptionRecoversSafely removed under RCP-20261101.
// Legacy premise: reviews ran on the async_tasks task chain (two-stage sql_grouping -> ai_review tasks,
// lease / fence heartbeats, idempotent replay after crash), and the test injected crashes via RunnerHooks
// panics to verify safe recovery after lease expiration and fence rejection. Ephemeral precheck converges
// all grouping + review into the in-process goroutine of StartEphemeralRun: there are no task rows, no
// leases, and no cross-process replay window — the object of interruption recovery (the DB task chain)
// no longer exists; crash means in-memory state is lost = ruling semantics (empty on restart), simply
// re-run precheck. Lease/fence primitives in package tasks remain covered by their own unit tests and other domains that still use the task queue.

// addSecondStage extends the flow into a two-stage flow.
func (f *fx) addSecondStage(t *testing.T) {
	t.Helper()
	dsID := uuid.NewString()
	if err := f.db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b6-ds2', 'postgresql', 'postgresql', 'native', 'pg.example.com', 5432, 'analytics', '16.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ef", 32), f.userID).Error; err != nil {
		t.Fatalf("seed ds2: %v", err)
	}
	stageID := uuid.NewString()
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: PG stages uniformly freeze execution-identity
	// search_path; since the fixture execution target is unreachable, inject a fixed binding via the test seam.
	stubPostgresStageSeams(f)
	if err := f.db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 2, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageID, f.flowID, dsID, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatalf("seed stage2: %v", err)
	}
	stepID := uuid.NewString()
	if err := f.db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position)
		values (?, ?, ?, 1)`, stepID, f.flowID, stageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepID, f.userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`, stageID, f.userID).Error; err != nil {
		t.Fatal(err)
	}
	// Advancing the flow version would cause existing drafts to drift; this test extends the flow before creating the draft.
}

// ---- G4: Raw Evidence seven-day boundary ----

// TestGate4RawEvidenceSevenDayBoundary ephemeral precheck evidence collection -> materialization -> retention window
// (RCP-20261101: evidence rows are persisted via materialization, keeping Reveal and retention surfaces isomorphic to the legacy path).
func TestGate4RawEvidenceSevenDayBoundary(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	sqlText := "update orders set amount = amount + 1 where id = 1;\n"
	draft := b.createDraftWithSQL(t, sqlText)
	er, start := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText), IdempotencyKey: "ret-1"})
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	// Submission semantics: materialization consumes (Take) the in-memory copy so subsequent reads go to
	// DB rows — otherwise reveal would take the in-memory branch (revealEphemeralRaw) and never reach the DB retention window assertion.
	if b.svc.EphemeralTake(start, b.owner.UserID) == nil {
		t.Fatal("ephemeral run missing for take")
	}
	// Locate evidence (scoped to this Run).
	var evidenceID string
	if err := b.db.Raw(`select ef.id::text from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? limit 1`, start).Scan(&evidenceID).Error; err != nil || evidenceID == "" {
		t.Fatalf("evidence missing: %v", err)
	}
	// expires_at == created_at + 7 days (guaranteed by DB CHECK; asserted explicitly).
	var ageDays float64
	_ = b.db.Raw(`select extract(epoch from (expires_at - created_at))/86400 from evidence_raw_payloads where evidence_fact_id = ?`, evidenceID).Scan(&ageDays).Error
	if ageDays != 7 {
		t.Fatalf("retention window want 7 days, got %f", ageDays)
	}
	// Reveal within the window succeeds.
	res, err := b.svc.RevealRawEvidence(ctx, b.owner, RequestMeta{}, evidenceID, "inspect")
	if err != nil {
		t.Fatalf("in-window reveal: %v", err)
	}
	if res.Watermark == "" || len(res.RawPayload) == 0 {
		t.Fatal("reveal must carry watermark and payload")
	}
	// Backdate timestamps (equivalent to replaying the retention policy against original timestamps after PITR recovery).
	if err := b.db.Exec(`update evidence_raw_payloads set created_at = now() - interval '8 days',
		expires_at = now() - interval '1 day' where evidence_fact_id = ?`, evidenceID).Error; err != nil {
		t.Fatal(err)
	}
	// Reveal after expiration is rejected.
	_, err = b.svc.RevealRawEvidence(ctx, b.owner, RequestMeta{}, evidenceID, "inspect")
	if !domain.IsCode(err, 1002) {
		t.Fatalf("expired reveal want 1002, got %v", err)
	}
	// Retention deletes expired rows (bounded at 10000).
	deleted, err := b.ret.DeleteExpired(ctx, "evidence_raw_payloads")
	if err != nil {
		t.Fatalf("retention delete: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("retention deleted want 1, got %d", deleted)
	}
	if n := count(t, b.db, `select count(*) from evidence_raw_payloads`); n != 0 {
		t.Fatalf("raw payloads after retention want 0, got %d", n)
	}
	// Normalized facts are retained permanently (business facts).
	if n := count(t, b.db, `select count(*) from evidence_facts`); n == 0 {
		t.Fatal("normalized facts must be permanent")
	}
	// Reveal after deletion: raw row no longer exists -> 1002 (sentinel or domain error).
	_, err = b.svc.RevealRawEvidence(ctx, b.owner, RequestMeta{}, evidenceID, "inspect")
	if !errors.Is(err, ErrNotFound) && !domain.IsCode(err, 1002) {
		t.Fatalf("post-retention reveal want 1002, got %v", err)
	}
	// Copy audit can still be recorded (the fact remains; after SEC-0035 it must reference the user's own
	// real reveal for this evidence — the audit record from the in-window reveal above still exists).
	if err := b.svc.RecordEvidenceCopy(ctx, b.owner, RequestMeta{}, evidenceID, res.RevealID); err != nil {
		t.Fatalf("copy audit: %v", err)
	}
	// SEC-0035: fabricated reveal_id -> 1002 (without leaking existence).
	if err := b.svc.RecordEvidenceCopy(ctx, b.owner, RequestMeta{}, evidenceID, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("copy audit with fabricated reveal want ErrNotFound, got %v", err)
	}
}

// TestRetentionBatchCap caps single-transaction deletion at 10000 rows.
func TestRetentionBatchCap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Directly generate 10005 expired idempotency records.
	if err := f.db.Exec(`
		insert into idempotency_records (id, requesting_user_id, route_template, aggregate_id,
			idempotency_key, request_hash, state, created_at, expires_at)
		select gen_random_uuid(), ?, 'route', gen_random_uuid(),
		       'key-' || g, repeat('0', 64), 'in_progress', now() - interval '2 days', now() - interval '1 day'
		from generate_series(1, 10005) g`, f.owner.UserID).Error; err != nil {
		t.Fatalf("seed idempotency: %v", err)
	}
	first, err := f.ret.DeleteExpired(ctx, "idempotency_records")
	if err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if first != RetentionBatchLimit {
		t.Fatalf("first batch want %d, got %d", RetentionBatchLimit, first)
	}
	second, err := f.ret.DeleteExpired(ctx, "idempotency_records")
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if second != 5 {
		t.Fatalf("second batch want 5, got %d", second)
	}
}

// TestOrphanSweepAfterDraftDelete (RCP-20260826-01): after deleting a draft,
// insert_only child rows remain -> retention sweeps in dependency order -> all orphans are reclaimed.
// RCP-20261101: Runs materialized from ephemeral prechecks attach to the draft and participate identically in cascading and sweeping.
func TestOrphanSweepAfterDraftDelete(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	runMaterialized := func(sqlText, key string) string {
		t.Helper()
		draft := b.createDraftWithSQL(t, sqlText)
		er, runID := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
			FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText), IdempotencyKey: key})
		b.materializeEphemeral(t, er, draft.ID, draft.Revision)
		return runID
	}
	start := runMaterialized("update orders set amount = amount + 1 where id = 1;\ninsert into orders (id) values (9);\n", "sweep-1")
	// Child rows exist.
	if n := count(t, b.db, `select count(*) from sql_statements s where s.review_run_id = ?`, start); n != 2 {
		t.Fatalf("statements before sweep want 2, got %d", n)
	}
	// After the B8 ruling, the model-less path produces no business findings; verifying dependency-ordered
	// sweeping requires a finding row, so insert a synthetic row directly using the same pattern as the forged order_stages reference below.
	var snapID0, factID0 string
	_ = b.db.Raw(`select id::text from stage_review_snapshots where review_run_id = ? limit 1`, start).Scan(&snapID0).Error
	if snapID0 == "" {
		t.Fatal("snapshot missing before sweep")
	}
	_ = b.db.Raw(`select id::text from evidence_facts where stage_review_id = ? limit 1`, snapID0).Scan(&factID0).Error
	if err := b.db.Exec(`insert into review_findings (id, stage_review_id, category, severity, title, message, suggestion, created_at)
		values (?, ?, 'correctness', 'medium', 'sweep-probe', 'synthetic finding for sweep order', null, now())`,
		uuid.NewString(), snapID0).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into review_finding_evidence (finding_id, evidence_fact_id)
		select id, ? from review_findings where stage_review_id = ? and title = 'sweep-probe'`, factID0, snapID0).Error; err != nil {
		t.Fatal(err)
	}
	if n := count(t, b.db, `select count(*) from review_findings`); n == 0 {
		t.Fatal("findings before sweep missing")
	}
	if n := count(t, b.db, `select count(*) from evidence_facts`); n == 0 {
		t.Fatal("facts before sweep missing")
	}
	// Delete draft (aggregate deletion; child rows become unreachable).
	b.deleteDraftOfRun(t, start)
	if n := count(t, b.db, `select count(*) from review_runs`); n != 0 {
		t.Fatalf("runs after delete want 0, got %d", n)
	}
	// Child rows still exist (awaiting retention).
	if n := count(t, b.db, `select count(*) from sql_statements`); n != 2 {
		t.Fatalf("statements after delete want 2 (pre-sweep), got %d", n)
	}
	// Reverse-reference precheck: forged order_stages reference -> 1006.
	start2 := runMaterialized("update orders set amount = amount + 3 where id = 3;\n", "sweep-2")
	var snapID string
	_ = b.db.Raw(`select id::text from stage_review_snapshots where review_run_id = ? limit 1`, start2).Scan(&snapID).Error
	if err := b.db.Exec(`insert into order_stages (id, order_id, position, datasource_id, datasource_name_snapshot,
		datasource_engine_snapshot, datasource_identity_fingerprint, stage_review_snapshot_id, review_snapshot_hash,
		review_gate_passed, target_snapshot_ciphertext, target_snapshot_nonce, target_snapshot_key_id,
		target_snapshot_algorithm, state, version)
		values (?, ?, 1, '00000000-0000-0000-0000-000000000001', 'ds', 'mysql', repeat('11',32), ?, repeat('22',32),
		true, decode('aa','hex'), decode('bb','hex'), 'pay-1', 'aes-256-gcm', 'pending', 1)`,
		uuid.NewString(), uuid.NewString(), snapID).Error; err != nil {
		t.Fatal(err)
	}
	draft2ID := erOf(t, b, start2)
	draft2, err := b.svc.GetDraft(ctx, b.owner, draft2ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.svc.DeleteDraft(ctx, b.owner, draft2ID, draft2.Version); !errors.Is(err, ErrReferenced) {
		t.Fatalf("referenced draft delete want ErrReferenced, got %v", err)
	}
	// Orphan sweep: a single pass in dependency order reclaims all orphan rows of draft1.
	deleted, err := b.ret.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted["sql_statements"] != 2 || deleted["sql_fingerprint_groups"] != 2 {
		t.Fatalf("sweep counts unexpected: %+v", deleted)
	}
	// draft2 was not deleted: its rows are not orphans and remain after sweeping.
	if n := count(t, b.db, `select count(*) from sql_statements where review_run_id = ?`, start2); n != 1 {
		t.Fatalf("draft2 statements must survive sweep, got %d", n)
	}
	if n := count(t, b.db, `select count(*) from stage_review_snapshots where review_run_id = ?`, start2); n != 1 {
		t.Fatalf("draft2 snapshot must survive sweep, got %d", n)
	}
	// draft1 orphan rows are reduced to zero: counted as rows not belonging to run2.
	if n := count(t, b.db, `select count(*) from sql_statements where review_run_id <> ?`, start2); n != 0 {
		t.Fatalf("orphan statements after sweep want 0, got %d", n)
	}
	if n := count(t, b.db, `select count(*) from sql_fingerprint_groups where review_run_id <> ?`, start2); n != 0 {
		t.Fatalf("orphan groups after sweep want 0, got %d", n)
	}
	if n := count(t, b.db, `select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id <> ?`, start2); n != 0 {
		t.Fatalf("orphan facts after sweep want 0, got %d", n)
	}
	if n := count(t, b.db, `select count(*) from evidence_raw_payloads erp
		join evidence_facts ef on ef.id = erp.evidence_fact_id
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id <> ?`, start2); n != 0 {
		t.Fatalf("orphan raws after sweep want 0, got %d", n)
	}
}

// erOf retrieves the draft ID attached to a Run (materialized row).
func erOf(t *testing.T, b *b7fx, runID string) string {
	t.Helper()
	var draftID string
	if err := b.db.Raw(`select draft_id::text from review_runs where id = ?`, runID).Row().Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	return draftID
}

// deleteDraftOfRun deletes the draft attached to a Run (via the real DeleteDraft aggregate path).
func (b *b7fx) deleteDraftOfRun(t *testing.T, runID string) {
	t.Helper()
	draftID := erOf(t, b, runID)
	draft, err := b.svc.GetDraft(context.Background(), b.owner, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.svc.DeleteDraft(context.Background(), b.owner, draftID, draft.Version); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
}

// TestCancellationPath removed under RCP-20261101.
// Legacy premise: reviews ran on the async_tasks task chain — CancelReviewTask transitioned the task to
// cancelled and converged the draft/Run (cancel before grouping -> draft partial; cancel after terminal ->
// no-op). Ephemeral precheck has no task rows to cancel: the review session completes in milliseconds
// inside the in-process goroutine of StartEphemeralRun, and the cancellation entry (CancelReviewTask,
// task-oriented) was retired along with the task chain; terminal non-regression semantics are covered by
// TestGate2IdempotentStartRun (replaying after terminal returns the same Run) and httpapi R003 assertions.

// stubPostgresStageSeams injects fixed search_path bindings (PostgresPathProbe/RelationBatch/Callable
// test seams) for integration fixtures whose PostgreSQL execution targets are unreachable.
// After RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS removed the explicit-mapping bypass, PG stages uniformly
// freeze execution-identity search_path, and fixtures obtain a defensible binding through this seam.
func stubPostgresStageSeams(f *fx) {
	path := FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "fixture", Configured: "public",
		Explicit: []string{"public"}, Effective: []string{"pg_catalog", "public"},
		CreationSchema: "public",
	}
	f.svc.PostgresPathProbe = func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error) {
		return path, nil
	}
	f.svc.PostgresRelationBatchResolver = func(
		_ context.Context, _ PostgresSearchTarget, _ FrozenPostgresSearchPath, relations []string,
	) ([]PostgresRelationBaseline, error) {
		out := make([]PostgresRelationBaseline, 0, len(relations))
		for _, name := range relations {
			out = append(out, PostgresRelationBaseline{
				TableName:       name,
				PresentBySchema: map[string]string{"public": "table"},
				OIDBySchema:     map[string]uint32{"public": 16384},
			})
		}
		return out, nil
	}
	f.svc.PostgresCallableResolver = func(
		_ context.Context, _ PostgresSearchTarget, _ FrozenPostgresSearchPath,
		requests []database.PostgresCallableRequest,
	) ([]database.PostgresCallableResolution, error) {
		out := make([]database.PostgresCallableResolution, 0, len(requests))
		for _, request := range requests {
			out = append(out, database.PostgresCallableResolution{
				Request: request,
				Winners: []database.PostgresCallableWinner{{
					Schema: "pg_catalog", Signature: request.Name + "()",
					ResultType: "bool", OID: 1, PgCatalog: true,
				}},
			})
		}
		return out, nil
	}
}
