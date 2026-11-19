//go:build integration

// identifier_mapping_reveal_integration_test.go RCP-20261022-IDENTIFIER-MAPPING-REVEAL
// read-side acceptance: owner reveals successfully (both tokens and all forms), non-owner gets 1002,
// unknown token fails closed with 1002, Run without mapping returns 1002, invalid request shape returns 1001,
// per-call audit, and tampered envelope returns 2009.
package httpapi

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// seedDraftRun seeds minimal change_drafts + review_runs rows directly (without full precheck pipeline),
// returning (draftID, runID). Flow relationship is irrelevant to reveal read side (traverses run -> draft -> owner).
func (fx *b6Fixture) seedDraftRun(t *testing.T, suffix string) (string, string) {
	t.Helper()
	// Deterministic valid UUID (8-4-4-4-12 hex).
	draftID := "0d000000-0000-4000-8000-0000000000" + suffix
	runID := "0e000000-0000-4000-8000-0000000000" + suffix
	if err := fx.db.Exec(`insert into change_drafts
		(id, owner_user_id, flow_id, flow_version_seen, title, created_at, updated_at)
		values (?, ?, ?, 1, 'mapping-reveal-itest', now(), now())`,
		draftID, fx.ownerID, fx.flowID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`insert into review_runs
		(id, draft_id, draft_revision, state, flow_snapshot_hash, rule_config_hash,
		 tool_config_hash, provider_plan_hash, fingerprint_algorithm_version,
		 anonymization_algorithm_version, statement_count, fingerprint_group_count, budget_day, created_at, finished_at)
		values (?, ?, 1, 'ready', repeat('a', 64), repeat('b', 64), repeat('c', 64), repeat('d', 64), 1, 1, 1, 1, now()::date, now(), now())`,
		runID, draftID).Error; err != nil {
		t.Fatal(err)
	}
	return draftID, runID
}

// seedMappingEnvelope persists a mapping envelope directly for the run (reusing production SealMapping path).
func (fx *b6Fixture) seedMappingEnvelope(t *testing.T, runID string, pairs []privacy.MappingPair) {
	t.Helper()
	env, err := privacy.SealMapping(fx.svc.Crypter, privacy.EnvelopeBinding{
		ReviewRunID: runID, StatementIdx: 1}, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`insert into review_identifier_mappings
		(id, review_run_id, statement_idx, identifier_mapping_ciphertext, identifier_mapping_nonce,
		 identifier_mapping_key_id, identifier_mapping_algorithm, mapping_entry_count, created_at)
		values (gen_random_uuid(), ?, 1, ?, ?, ?, 'aes-256-gcm', ?, now())
		on conflict (review_run_id, statement_idx) do update set
		  identifier_mapping_ciphertext = excluded.identifier_mapping_ciphertext,
		  identifier_mapping_nonce = excluded.identifier_mapping_nonce,
		  identifier_mapping_key_id = excluded.identifier_mapping_key_id,
		  mapping_entry_count = excluded.mapping_entry_count`,
		runID, env.Ciphertext, env.Nonce, env.KeyID, len(pairs)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestIdentifierMappingRevealOwnerTokensAllAudit(t *testing.T) {
	fx := newB6Fixture(t)
	_, runID := fx.seedDraftRun(t, "a1")
	pairs := []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		{Kind: privacy.KindColumn, Original: "amount", Token: "column_1"},
	}
	fx.seedMappingEnvelope(t, runID, pairs)

	// owner + tokens form.
	c, body, raw := fx.owner.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{"tokens": []string{"table_1"}}, true)
	if c != 200 || errCode(body) != 0 {
		t.Fatalf("owner tokens reveal: %d %s", c, raw)
	}
	data := body["data"].(map[string]any)
	if data["review_run_id"] != runID {
		t.Fatalf("run id: %v", data["review_run_id"])
	}
	maps := data["mappings"].([]any)
	if len(maps) != 1 {
		t.Fatalf("mappings: %d", len(maps))
	}
	m := maps[0].(map[string]any)
	if m["token"] != "table_1" || m["kind"] != "table" || m["identifier"] != "orders" {
		t.Fatalf("mapping: %v", m)
	}

	// owner + all form.
	c, body, raw = fx.owner.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{"all": true}, true)
	if c != 200 || errCode(body) != 0 {
		t.Fatalf("owner all reveal: %d %s", c, raw)
	}
	if n := len(body["data"].(map[string]any)["mappings"].([]any)); n != 2 {
		t.Fatalf("all mappings: %d", n)
	}

	// Audited per call: two actions produce two audit records.
	var audits int64
	_ = fx.db.Raw(`select count(*) from audit_events
		where event_type = 'review_evidence.mapping_revealed'`).Scan(&audits).Error
	if audits != 2 {
		t.Fatalf("mapping_revealed audits: %d", audits)
	}
}

func TestIdentifierMappingRevealAuthorizationAndFailClosed(t *testing.T) {
	fx := newB6Fixture(t)
	_, runID := fx.seedDraftRun(t, "b1")
	fx.seedMappingEnvelope(t, runID, []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
	})

	// Non-owner (intruder) -> 1002, without leaking existence.
	if c, b, _ := fx.intruder.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{"all": true}, true); c != 200 || errCode(b) != 1002 {
		t.Fatalf("intruder reveal: %d", c)
	}

	// Unknown token -> 1002 fail-closed.
	if c, b, _ := fx.owner.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{"tokens": []string{"table_1", "table_999"}}, true); c != 200 || errCode(b) != 1002 {
		t.Fatalf("unknown token: %d %v", c, b)
	}

	// Request shape: both tokens and all present -> 1001; both empty -> 1001.
	if c, b, _ := fx.owner.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{"tokens": []string{"table_1"}, "all": true}, true); c != 200 || errCode(b) != 1001 {
		t.Fatalf("both forms: %d", c)
	}
	if c, b, _ := fx.owner.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{}, true); c != 200 || errCode(b) != 1001 {
		t.Fatalf("empty request: %d", c)
	}

	// Run without mapping envelope -> 1002.
	_, emptyRun := fx.seedDraftRun(t, "b2")
	if c, b, _ := fx.owner.do("POST", "/api/v4/review-runs/"+emptyRun+"/identifier-mapping-reveals",
		map[string]any{"all": true}, true); c != 200 || errCode(b) != 1002 {
		t.Fatalf("no envelope: %d", c)
	}
}

func TestIdentifierMappingRevealTamperedEnvelope(t *testing.T) {
	fx := newB6Fixture(t)
	_, runID := fx.seedDraftRun(t, "c1")
	fx.seedMappingEnvelope(t, runID, []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
	})
	// Tamper with first byte of ciphertext: AAD authentication failure -> 2009.
	if err := fx.db.Exec(`update review_identifier_mappings
		set identifier_mapping_ciphertext = identifier_mapping_ciphertext || bytea '\x01'
		where review_run_id = ?`, runID).Error; err != nil {
		t.Fatal(err)
	}
	if c, b, _ := fx.owner.do("POST", "/api/v4/review-runs/"+runID+"/identifier-mapping-reveals",
		map[string]any{"all": true}, true); c != 200 || errCode(b) != 2009 {
		t.Fatalf("tampered envelope: %d %v", c, b)
	}
}
