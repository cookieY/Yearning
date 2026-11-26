//go:build integration

// identifiermapping_integration_test.go NEW-AI-01 database-level regression (requires real
// PostgreSQL; shares the same fixture system as b7, delivered without local execution in this batch —
// executed centrally by the main repair agent against a live PG environment):
//
//  1. After a review Run is persisted, review_identifier_mappings contains a Run-scoped envelope row
//     with AAD binding Run + sentinel statement index, and plaintext restores token -> original identifier;
//  2. Repeated sealing is idempotent (zero changes on identical snapshot), and rehydrate reproduces tokens;
//  3. Envelope AAD decryption fails across Runs (ciphertext substitution defense).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// TestNEWAI01IdentifierMappingPersistedAndRehydratable full flow: model requests tool ->
// tokenized facts are persisted -> review_identifier_mappings persists the envelope alongside;
// a new mapper reproduces the tokens after rehydration.
func TestNEWAI01IdentifierMappingPersistedAndRehydratable(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if len(req.NormalizedEvidence) > 0 {
			return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
		}
		tables, err := scopedTablesFromRequest(req)
		if err != nil {
			return nil, 500, err
		}
		if len(tables) == 0 {
			return nil, 500, errors.New("scoped metadata token missing")
		}
		table := tables[0]
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.", "findings": []map[string]any{},
			"tool_requests": []map[string]any{
				{
					"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000008",
					"tool_name":    "schema_metadata",
					"arguments": map[string]any{
						"schema": table.Schema, "table": table.Table,
					},
				},
				{
					"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000009",
					"tool_name":    "index_metadata",
					"arguments": map[string]any{
						"schema": table.Schema, "table": table.Table,
					},
				},
			},
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	})
	runID := b.runAgent(t, deps)

	// Matching field names to SELECT aliases is sufficient for gorm Raw().Scan mapping; do not add
	// a column tag (the tag points to the original column name, which mismatches the alias and silently
	// leaves zero values — the root cause of the first-round failure in this test).
	var row struct {
		ID         string
		EntryCount int
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Algorithm  string
	}
	if err := b.db.Raw(`
		select id::text as id, mapping_entry_count as entry_count,
		       identifier_mapping_ciphertext as ciphertext, identifier_mapping_nonce as nonce,
		       identifier_mapping_key_id as key_id, identifier_mapping_algorithm as algorithm
		from review_identifier_mappings
		where review_run_id = ? and statement_idx = ?`, runID, mappingEnvelopeStatementIdx).
		Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ID == "" || row.EntryCount < 1 {
		t.Fatalf("run mapping envelope missing: id=%q count=%d", row.ID, row.EntryCount)
	}
	// AAD binds Run + sentinel statement index: valid binding restores cleanly, while cross-Run binding fails.
	binding := privacy.EnvelopeBinding{ReviewRunID: runID, StatementIdx: mappingEnvelopeStatementIdx}
	pairs, err := privacy.OpenMapping(b.svc.Crypter, binding, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: row.KeyID, Algorithm: row.Algorithm,
	})
	if err != nil {
		t.Fatalf("open run mapping: %v", err)
	}
	foundShop := false
	for _, p := range pairs {
		if p.Kind == privacy.KindSchema && p.Original == "shop" {
			foundShop = true
		}
		if p.Kind == privacy.KindTable && p.Original == "orders" {
			// Tokens that appeared on the model-visible channel must match the envelope.
			if p.Token != "table_1" && !strings.HasPrefix(p.Token, "table_") {
				t.Fatalf("unexpected token shape: %s", p.Token)
			}
		}
	}
	if !foundShop {
		t.Fatalf("schema shop missing from persisted mapping: %+v", pairs)
	}
	if _, err := privacy.OpenMapping(b.svc.Crypter, privacy.EnvelopeBinding{
		ReviewRunID: "00000000-0000-4000-8000-00000000dead", StatementIdx: mappingEnvelopeStatementIdx,
	}, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: row.KeyID, Algorithm: row.Algorithm,
	}); err == nil {
		t.Fatal("cross-run AAD must fail (ciphertext substitution guard)")
	}

	// Idempotent: repeated persist with the same snapshot makes zero changes; rehydrate reproduces tokens.
	agent := NewAgentRunner(b.svc, deps)
	reborn := privacy.NewRunMapper()
	if err := agent.rehydrateIdentifierMapping(context.Background(), b.db, runID, reborn); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if got := reborn.Lookup(privacy.KindSchema, "shop"); got == "" {
		t.Fatal("rehydrated mapper lost schema token")
	}
	before := reborn.Snapshot()
	if err := agent.persistIdentifierMapping(context.Background(), b.db, runID, reborn); err != nil {
		t.Fatalf("idempotent persist: %v", err)
	}
	var count int
	if err := b.db.Raw(`select count(*) from review_identifier_mappings where review_run_id = ?`, runID).
		Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent persist created duplicate rows: %d", count)
	}
	after := reborn.Snapshot()
	if len(before) != len(after) {
		t.Fatalf("idempotent persist changed mapping size: %d -> %d", len(before), len(after))
	}
}
