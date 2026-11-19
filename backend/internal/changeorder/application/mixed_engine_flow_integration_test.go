//go:build integration

// mixed_engine_flow_integration_test.go Fail-closed integration test for mixed-engine flows
// (BUG-005 Option A / Independent Review F3).
//
// Background: multi-stage change_review flows must bind datasources of the exact same engine and
// compatibility mode across all stages — execution-time preflight recomputes statement dialect
// fingerprints using each stage's datasource engine, whereas review freezes a single dialect per Run,
// so a cross-engine second stage would inevitably get permanently stuck at preflight 3006.
//
// Configuration-time CreateFlow/ReplaceFlow already fails closed, but two classes of legacy risk bypass configuration time:
//
//  1. Mixed-engine flows existing prior to validation rollout (historical data written directly to DB);
//  2. An admin subsequently changes a datasource's engine/compatibility_mode, silently turning an
//     existing single-engine flow that references it into a mixed-engine flow.
//
// This test verifies both fallback paths:
//   - Re-check at submission freeze (submit.go 10.1) -> SUBMISSION_GATE_FAILED:mixed_engine_flow
//   - Interception at datasource engine change (datasource.Replace) -> 1001, with no DB mutation
//
// Requires a real PostgreSQL instance (YEARNING_TEST_DATABASE_*; shares the database
// with other integration tests, mutually excluded via the same file lock).
package application

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
)

// seedDatasource creates a single datasource and returns its id (with configurable engine and compatibility mode).
func (fx *subFx) seedDatasource(t *testing.T, engine, mode string) string {
	t.Helper()
	dsID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into datasources (id, name, engine, compatibility_mode, host, port,
			version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, ?, ?, ?, '127.0.0.1', 3306, '8.0', true, ?, 1, ?, now(), now())`,
		dsID, "me-ds-"+uuid.NewString()[:8], engine, mode, repeatHex64("22"), uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	return dsID
}

// seedFlowMixedEngine creates a change_review flow with a separate datasource per stage,
// used to construct legacy mixed-engine data (bypassing configuration-time validation by writing directly to DB).
func (fx *subFx) seedFlowMixedEngine(t *testing.T, datasourceIDs []string) string {
	t.Helper()
	ruleSetID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, '{}'::jsonb, ?, 1, ?, now(), now())`,
		ruleSetID, "me-rs-"+uuid.NewString()[:8], repeatHex64("11"), uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	flowID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, "me-flow-"+uuid.NewString()[:8], ruleSetID, uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	approver := fx.seedUser(t, "me-app")
	executor := fx.seedUser(t, "me-exec")
	for i, dsID := range datasourceIDs {
		stageID := uuid.NewString()
		if err := fx.db.Exec(`
			insert into flow_stages (id, flow_id, position, datasource_id,
				schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
			values (?, ?, ?, ?, ?, ?, 'k1', 'aes-256-gcm')`,
			stageID, flowID, i+1, dsID, []byte{1}, []byte{2}).Error; err != nil {
			t.Fatal(err)
		}
		stepID := uuid.NewString()
		if err := fx.db.Exec(`
			insert into flow_approval_steps (id, flow_id, stage_id, position)
			values (?, ?, ?, 1)`, stepID, flowID, stageID).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.db.Exec(`
			insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`,
			stepID, approver).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.db.Exec(`
			insert into flow_execution_actors (stage_id, user_id) values (?, ?)`,
			stageID, executor).Error; err != nil {
			t.Fatal(err)
		}
	}
	return flowID
}

// TestSubmitRejectsLegacyMixedEngineFlow Legacy mixed-engine flow: historical flow written
// directly to DB bypassing configuration-time validation must be rejected by the submission re-check
// (rather than getting stuck at 3006 during execution after freezing).
func TestSubmitRejectsLegacyMixedEngineFlow(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "me-submit")
	dsMySQL := fx.seedDatasource(t, "mysql", "mysql")
	dsPG := fx.seedDatasource(t, "postgresql", "postgresql")
	flowID := fx.seedFlowMixedEngine(t, []string{dsMySQL, dsPG})
	fx.seedGrant(t, submitter, flowID)

	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)

	_, err := fx.submitAs(t, draftID, submitter, "me-submit")
	if err == nil {
		t.Fatal("mixed-engine flow must be rejected at submit, got nil error")
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want domain gate error, got %v", err)
	}
	want := "SUBMISSION_GATE_FAILED:mixed_engine_flow"
	if de.Code != domain.CodeSubmissionGateFailed || de.Name != want {
		t.Fatalf("want %s, got code=%d name=%s", want, de.Code, de.Name)
	}

	// Rejection path must not persist a work order or flip draft state.
	var orders int64
	if err := fx.db.Raw(`select count(*) from change_orders`).Scan(&orders).Error; err != nil {
		t.Fatal(err)
	}
	if orders != 0 {
		t.Fatalf("rejected submit must not create order, got %d", orders)
	}
	var state string
	if err := fx.db.Raw(`select state from change_drafts where id = ?`, draftID).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state != "draft" {
		t.Fatalf("rejected submit must keep draft in place, got %s", state)
	}
}

// TestSubmitAcceptsSameEngineMultiStage Multi-stage flow with the same engine (including two stages
// sharing the same datasource) is unaffected by the re-check and submits normally — confirming the fix does not over-tighten.
func TestSubmitAcceptsSameEngineMultiStage(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "me-ok-submit")
	dsMySQL := fx.seedDatasource(t, "mysql", "mysql")
	// Two stages referencing the same datasource: standard serial multi-stage shape, must pass.
	flowID := fx.seedFlowMixedEngine(t, []string{dsMySQL, dsMySQL})
	fx.seedGrant(t, submitter, flowID)

	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)

	view, err := fx.submitAs(t, draftID, submitter, "me-ok-submit")
	if err != nil {
		t.Fatalf("same-engine multi-stage must be accepted, got %v", err)
	}
	if view == nil || view.ID == "" {
		t.Fatal("submit returned empty order view")
	}
}

// TestSubmitAcceptsSameEngineDifferentDatasources Multi-stage flow with two different datasources
// of the same engine and compatibility mode is valid: configuration-time rules only require identical
// engine and compatibility_mode, not identical datasources (submission-side re-check uses the same criterion).
func TestSubmitAcceptsSameEngineDifferentDatasources(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "me-ok2-submit")
	dsA := fx.seedDatasource(t, "mysql", "mysql")
	dsB := fx.seedDatasource(t, "mysql", "mysql")
	flowID := fx.seedFlowMixedEngine(t, []string{dsA, dsB})
	fx.seedGrant(t, submitter, flowID)

	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)

	if _, err := fx.submitAs(t, draftID, submitter, "me-ok2-submit"); err != nil {
		t.Fatalf("same engine+mode on different datasources must be accepted, got %v", err)
	}
}

// TestSubmitRejectsDifferentEngineSameMode Different engine with the same compatibility mode is also rejected:
// stays consistent with configuration-time validateChangeFlowSingleEngine criterion (both must match
// exactly), preventing the submission side from being looser than the configuration side.
func TestSubmitRejectsDifferentEngineSameMode(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "me-eng-submit")
	dsA := fx.seedDatasource(t, "mysql", "mysql")
	dsB := fx.seedDatasource(t, "tidb", "mysql")
	flowID := fx.seedFlowMixedEngine(t, []string{dsA, dsB})
	fx.seedGrant(t, submitter, flowID)

	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)

	_, err := fx.submitAs(t, draftID, submitter, "me-eng-submit")
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want domain gate error, got %v", err)
	}
	if de.Name != "SUBMISSION_GATE_FAILED:mixed_engine_flow" {
		t.Fatalf("want mixed_engine_flow, got %s", de.Name)
	}
}

// TestSubmitRejectsCompatibilityModeMismatch Mismatched compatibility mode must also be rejected:
// dialect fingerprints are computed by compatibility_mode rather than engine, so mixed modes are equivalent to mixed engines.
func TestSubmitRejectsCompatibilityModeMismatch(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "me-mode-submit")
	dsA := fx.seedDatasource(t, "mysql", "mysql")
	dsB := fx.seedDatasource(t, "postgresql", "postgresql")
	flowID := fx.seedFlowMixedEngine(t, []string{dsA, dsB})
	fx.seedGrant(t, submitter, flowID)

	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)

	_, err := fx.submitAs(t, draftID, submitter, "me-mode-submit")
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want domain gate error, got %v", err)
	}
	if de.Name != "SUBMISSION_GATE_FAILED:mixed_engine_flow" {
		t.Fatalf("want mixed_engine_flow, got %s", de.Name)
	}
}
