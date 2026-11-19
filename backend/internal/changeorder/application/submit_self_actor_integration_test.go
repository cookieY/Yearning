//go:build integration

// submit_self_actor_integration_test.go Self-Actor submission gate integration test
// (RCP-20261031-SELF-ACTOR-ALLOWED, superseding RCP-20261021-FOUR-EYES-PRINCIPLE):
// the submitter is allowed to serve as a frozen approver or executor on the same work order —
// self-submit, self-approve, and self-execute are valid. Submitter in approver list -> allowed
// and frozen; submitter in executor list -> allowed and frozen; hit only in a later stage ->
// likewise allowed; unrelated submitter on a multi-stage, multi-step flow -> allowed with complete
// frozen surface. A self-approver can directly make an approval decision on their own work order
// after submission (decide frozen step member authorization does not exclude the submitter).
//
// Requires a real PostgreSQL instance (YEARNING_TEST_DATABASE_*; shares the database
// with the httpapi fixture, mutually excluded via the same file lock). Compiles when
// local DB is unavailable and runs in CI.
package application

import (
	"context"
	"encoding/base64"
	"fmt"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// subFx test fixture for self-Actor submission tests.
type subFx struct {
	db       *gorm.DB
	svc      *Services
	crypter  *crypto.Crypter
	recorder *auditlog.Recorder
}

// reviewServices constructs review application services (used by submit v2 ephemeral Run tests).
func (fx *subFx) reviewServices(t *testing.T) *reviewapp.Services {
	t.Helper()
	svc, err := reviewapp.NewServices(fx.db, fx.crypter, fx.recorder, nil)
	if err != nil {
		t.Fatalf("review services: %v", err)
	}
	return svc
}

func newSubFx(t *testing.T) *subFx {
	t.Helper()
	acquireCopyLock(t)
	db, err := gorm.Open(postgres.Open(copyDSN(t)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	if sqlDB, serr := db.DB(); serr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	// Clear all tables written by self-Actor tests (including users and flow template tables;
	// shared DB is mutually excluded via file lock, following the same pattern as httpapi truncateAll).
	for _, tbl := range []string{
		"change_drafts", "draft_sql_payloads", "review_runs", "review_run_sql_bindings",
		"stage_review_snapshots", "change_orders", "order_flow_snapshots", "order_sql_payloads",
		"order_stages", "order_approval_steps", "order_approval_actors", "order_execution_actors",
		"approval_decisions", "order_comments", "order_rollback_evidences", "execution_attempts",
		"domain_events", "outbox_events", "audit_events", "idempotency_records",
		"flow_approval_step_actors", "flow_execution_actors", "flow_approval_steps", "flow_stages",
		"flows", "rule_sets", "rule_set_prompt_tools", "datasources",
		"permission_group_flow_grants", "permission_group_members", "permission_groups", "users",
	} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	dir := t.TempDir()
	mk := func(seed byte) string {
		out := make([]byte, 32)
		for i := range out {
			out[i] = seed
		}
		return base64.StdEncoding.EncodeToString(out)
	}
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"t4e-itest","keys":[
	{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		mk(1), mk(4))
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return &subFx{db: db, svc: svc, crypter: crypter, recorder: recorder}
}

// seedUser creates a regular user (username has a random suffix to avoid CI unique index collisions).
func (fx *subFx) seedUser(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	if err := fx.db.Exec(`
		insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
		values (?, ?, ?, false, 1, now(), now())`,
		id, fmt.Sprintf("%s-%s", name, uuid.NewString()[:8]), name).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// subStageSpec describes a single stage: each inner slice is the candidate approvers of an approval step, plus the executor set.
type subStageSpec struct {
	ApproverSteps [][]string
	Executors     []string
}

// seedFlow creates an enabled change_review flow (single datasource across multiple stages,
// configurable approval steps and executors; ruleset has no prompt tool -> tool hash takes the none path).
func (fx *subFx) seedFlow(t *testing.T, stages []subStageSpec) string {
	t.Helper()
	ruleSetID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, '{}'::jsonb, ?, 1, ?, now(), now())`,
		ruleSetID, "t4e-rs-"+uuid.NewString()[:8], repeatHex64("11"), uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	dsID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into datasources (id, name, engine, compatibility_mode, host, port,
			version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'mysql', 'mysql', '127.0.0.1', 3306, '8.0', true, ?, 1, ?, now(), now())`,
		dsID, "t4e-ds-"+uuid.NewString()[:8], repeatHex64("22"), uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	flowID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, "t4e-flow-"+uuid.NewString()[:8], ruleSetID, uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	for i, spec := range stages {
		stageID := uuid.NewString()
		if err := fx.db.Exec(`
			insert into flow_stages (id, flow_id, position, datasource_id,
				schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
			values (?, ?, ?, ?, ?, ?, 'k1', 'aes-256-gcm')`,
			stageID, flowID, i+1, dsID, []byte{1}, []byte{2}).Error; err != nil {
			t.Fatal(err)
		}
		for j, actors := range spec.ApproverSteps {
			stepID := uuid.NewString()
			if err := fx.db.Exec(`
				insert into flow_approval_steps (id, flow_id, stage_id, position)
				values (?, ?, ?, ?)`,
				stepID, flowID, stageID, j+1).Error; err != nil {
				t.Fatal(err)
			}
			for _, uid := range actors {
				if err := fx.db.Exec(`
					insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`,
					stepID, uid).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, uid := range spec.Executors {
			if err := fx.db.Exec(`
				insert into flow_execution_actors (stage_id, user_id) values (?, ?)`,
				stageID, uid).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return flowID
}

// seedGrant grants the user submission permission for the flow (via permission group chain).
func (fx *subFx) seedGrant(t *testing.T, userID, flowID string) {
	t.Helper()
	pgID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, 1, ?, now(), now())`,
		pgID, "t4e-pg-"+uuid.NewString()[:8], uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql string
		mix []any
	}{
		{`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, []any{pgID, userID}},
		{`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, []any{pgID, flowID}},
	} {
		if err := fx.db.Exec(q.sql, q.mix...).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// seedReadyDraft creates a ready draft and persists an AEAD-sealed SQL payload
// (AAD is byte-identical to the submit unsealing path; HMAC on both sides is pinned to the same value by the fixture).
func (fx *subFx) seedReadyDraft(t *testing.T, flowID, ownerID string) string {
	t.Helper()
	draftID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into change_drafts (id, owner_user_id, flow_id, flow_version_seen, title,
			description, revision, state, version, created_at, updated_at)
		values (?, ?, ?, 1, 't4e-order', '', 1, 'draft', 1, now(), now())`,
		draftID, ownerID, flowID).Error; err != nil {
		t.Fatal(err)
	}
	plain := []byte("select 1 from t4e")
	env, err := fx.svc.Crypter.Seal(crypto.PurposePayload, plain, crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(), Table: "draft_sql_payloads",
		PrimaryKey: draftID, Column: "sql_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`
		insert into draft_sql_payloads (draft_id, sql_ciphertext, sql_nonce, sql_key_id,
			sql_algorithm, plaintext_hmac, size_bytes, statement_count, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, 1, now())`,
		draftID, env.Ciphertext, env.Nonce, env.KeyID, env.Algorithm,
		repeatHex64("ab"), len(plain)).Error; err != nil {
		t.Fatal(err)
	}
	return draftID
}

// seedReadyRun creates a state=ready Run: frozen hashes are reproduced from template facts
// using the same package-internal functions as submitOnce (byte-identical), SQL binding is pinned
// to the draft payload HMAC, and one passed snapshot is persisted per stage.
func (fx *subFx) seedReadyRun(t *testing.T, draftID, flowID string) {
	t.Helper()
	ctx := context.Background()
	stages, err := fx.svc.loadFlowStageFacts(ctx, fx.db, flowID)
	if err != nil {
		t.Fatal(err)
	}
	stepsByPos, err := fx.svc.loadFlowApprovalSteps(ctx, fx.db, flowID)
	if err != nil {
		t.Fatal(err)
	}
	approvalUnion := map[int][]string{}
	for pos, steps := range stepsByPos {
		for _, st := range steps {
			approvalUnion[pos] = append(approvalUnion[pos], st.Actors...)
		}
	}
	flowHash, err := computeFlowSnapshotHash(flowID, 1, stages, approvalUnion)
	if err != nil {
		t.Fatal(err)
	}
	var ruleSetID string
	if err := fx.db.Raw(`select rule_set_id::text from flows where id = ?`, flowID).Scan(&ruleSetID).Error; err != nil {
		t.Fatal(err)
	}
	ruleHash, toolHash, err := fx.svc.computeRuleConfigHashes(ctx, fx.db, &ruleSetID)
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into review_runs (id, draft_id, draft_revision, state, flow_snapshot_hash,
			rule_config_hash, tool_config_hash, provider_plan_hash,
			fingerprint_algorithm_version, anonymization_algorithm_version,
			statement_count, fingerprint_group_count, budget_day, finished_at, version, created_at)
		values (?, ?, 1, 'ready', ?, ?, ?, ?, 'v1', 'v1', 1, 1, current_date, now(), 1, now())`,
		runID, draftID, flowHash, ruleHash, toolHash, repeatHex64("33")).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`
		insert into review_run_sql_bindings (review_run_id, sql_payload_hmac, created_at)
		values (?, ?, now())`,
		runID, repeatHex64("ab")).Error; err != nil {
		t.Fatal(err)
	}
	for _, st := range stages {
		if err := fx.db.Exec(`
			insert into stage_review_snapshots (id, review_run_id, stage_position, datasource_id,
				datasource_identity_fingerprint, state, highest_severity, gate_passed,
				normalized_context_ciphertext, normalized_context_nonce,
				normalized_context_key_id, normalized_context_algorithm, snapshot_hash,
				created_at, finished_at)
			values (?, ?, ?, ?, ?, 'passed', 'none', true, ?, ?, 'k1', 'aes-256-gcm', ?, now(), now())`,
			uuid.NewString(), runID, st.Position, st.DatasourceID, repeatHex64("22"),
			[]byte{3}, []byte{4}, repeatHex64("44")).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// submitAs submits a ready draft as the specified submitter.
func (fx *subFx) submitAs(t *testing.T, draftID, submitterID, username string) (*OrderView, error) {
	t.Helper()
	view, _, err := fx.svc.Submit(context.Background(), Actor{UserID: submitterID, Username: username},
		RequestMeta{}, SubmitInput{DraftID: draftID, ExpectVersion: 1,
			IdempotencyKey: "t4e-" + uuid.NewString()[:8]})
	return view, err
}

// assertSelfFrozen asserts that the submitter actually appears in the work order's frozen surface
// (approve = hit count in approver union, exec = hit count in executor set) — the premise of
// valid self-Actor is that the user is in the frozen list, and authorization still checks the exact frozen user ID set.
func (fx *subFx) assertSelfFrozen(t *testing.T, orderID, submitterID string, wantApprove, wantExec int64) {
	t.Helper()
	var approveSelf, execSelf int64
	if err := fx.db.Raw(`
		select count(*) from order_approval_actors a
		join order_approval_steps s on s.id = a.step_id
		join order_stages st on st.id = s.stage_id
		where st.order_id = ? and a.reviewer_user_id_snapshot = ?`,
		orderID, submitterID).Scan(&approveSelf).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(`
		select count(*) from order_execution_actors e
		join order_stages st on st.id = e.stage_id
		where st.order_id = ? and e.executor_user_id_snapshot = ?`,
		orderID, submitterID).Scan(&execSelf).Error; err != nil {
		t.Fatal(err)
	}
	if approveSelf != wantApprove || execSelf != wantExec {
		t.Fatalf("self frozen membership approver=%d executor=%d want %d/%d",
			approveSelf, execSelf, wantApprove, wantExec)
	}
}

// TestSubmitSelfApproverAllowed Submitter is in the first-stage approver list -> allowed,
// frozen approver snapshot contains the submitter, and the submitter can subsequently approve
// their own work order directly (self-submit and self-approve are valid).
func TestSubmitSelfApproverAllowed(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "t4e-self")
	approver := fx.seedUser(t, "t4e-app")
	executor := fx.seedUser(t, "t4e-exec")
	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{approver, submitter}}, Executors: []string{executor}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)
	view, err := fx.submitAs(t, draftID, submitter, "t4e-self")
	if err != nil {
		t.Fatalf("self-approver submitter must pass: %v", err)
	}
	if view.State != "stage_approval_active" {
		t.Fatalf("order state=%s want stage_approval_active", view.State)
	}
	fx.assertSelfFrozen(t, view.ID, submitter, 1, 0)
	// Self-approval: submitter makes an approval decision on their own work order as a frozen approver.
	if _, _, derr := fx.svc.Decide(context.Background(),
		Actor{UserID: submitter, Username: "t4e-self"}, RequestMeta{},
		DecideInput{OrderID: view.ID, ExpectVersion: view.Version, Decision: "approve",
			IdempotencyKey: "t4e-dec-" + uuid.NewString()[:8]}); derr != nil {
		t.Fatalf("self-approve after submit must pass: %v", derr)
	}
}

// TestSubmitSelfExecutorAllowed Submitter is in the frozen executor set -> allowed,
// frozen executor snapshot contains the submitter (self-submit and self-execute are valid;
// execution-side begin only checks frozen executor membership and does not exclude the submitter).
func TestSubmitSelfExecutorAllowed(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "t4e-self")
	approver := fx.seedUser(t, "t4e-app")
	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{approver}}, Executors: []string{submitter}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)
	view, err := fx.submitAs(t, draftID, submitter, "t4e-self")
	if err != nil {
		t.Fatalf("self-executor submitter must pass: %v", err)
	}
	if view.State != "stage_approval_active" {
		t.Fatalf("order state=%s want stage_approval_active", view.State)
	}
	fx.assertSelfFrozen(t, view.ID, submitter, 0, 1)
}

// TestSubmitIndependentSubmitterPasses Unrelated submitter on a two-stage flow with
// two approval steps per stage is allowed, and the frozen surface is completely persisted.
func TestSubmitIndependentSubmitterPasses(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "t4e-owner")
	a1 := fx.seedUser(t, "t4e-a1")
	a2 := fx.seedUser(t, "t4e-a2")
	a3 := fx.seedUser(t, "t4e-a3")
	a4 := fx.seedUser(t, "t4e-a4")
	e1 := fx.seedUser(t, "t4e-e1")
	e2 := fx.seedUser(t, "t4e-e2")
	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{a1}, {a2}}, Executors: []string{e1}},
		{ApproverSteps: [][]string{{a3}, {a4}}, Executors: []string{e2}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)
	view, err := fx.submitAs(t, draftID, submitter, "t4e-owner")
	if err != nil {
		t.Fatalf("independent submitter must pass: %v", err)
	}
	if view.State != "stage_approval_active" {
		t.Fatalf("order state=%s want stage_approval_active", view.State)
	}
	if view.SubmitterUserID != submitter {
		t.Fatalf("submitter snapshot=%s want %s", view.SubmitterUserID, submitter)
	}
	// Frozen snapshot: 4 approvers and 2 executors, none of whom is the submitter.
	var approvers, executors, self int64
	if err := fx.db.Raw(`
		select count(*) from order_approval_actors a
		join order_approval_steps s on s.id = a.step_id
		join order_stages st on st.id = s.stage_id
		where st.order_id = ?`, view.ID).Scan(&approvers).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(`
		select count(*) from order_execution_actors e
		join order_stages st on st.id = e.stage_id
		where st.order_id = ?`, view.ID).Scan(&executors).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(`
		select (select count(*) from order_approval_actors a
		        join order_approval_steps s on s.id = a.step_id
		        join order_stages st on st.id = s.stage_id
		        where st.order_id = ? and a.reviewer_user_id_snapshot = ?)
		     + (select count(*) from order_execution_actors e
		        join order_stages st on st.id = e.stage_id
		        where st.order_id = ? and e.executor_user_id_snapshot = ?)`,
		view.ID, submitter, view.ID, submitter).Scan(&self).Error; err != nil {
		t.Fatal(err)
	}
	if approvers != 4 || executors != 2 {
		t.Fatalf("frozen actors approvers=%d executors=%d want 4/2", approvers, executors)
	}
	if self != 0 {
		t.Fatalf("independent submitter must not appear in frozen actors, got %d", self)
	}
}

// TestSubmitSelfLaterStageApproverAllowed Submitter only appears in a second-stage approval step
// -> likewise allowed (frozen lists across all stages may include the submitter).
func TestSubmitSelfLaterStageApproverAllowed(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "t4e-self")
	a1 := fx.seedUser(t, "t4e-a1")
	a2 := fx.seedUser(t, "t4e-a2")
	a3 := fx.seedUser(t, "t4e-a3")
	e1 := fx.seedUser(t, "t4e-e1")
	e2 := fx.seedUser(t, "t4e-e2")
	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{a1}, {a2}}, Executors: []string{e1}},
		{ApproverSteps: [][]string{{a3, submitter}}, Executors: []string{e2}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)
	view, err := fx.submitAs(t, draftID, submitter, "t4e-self")
	if err != nil {
		t.Fatalf("later-stage self-approver submitter must pass: %v", err)
	}
	if view.State != "stage_approval_active" {
		t.Fatalf("order state=%s want stage_approval_active", view.State)
	}
	fx.assertSelfFrozen(t, view.ID, submitter, 1, 0)
}
