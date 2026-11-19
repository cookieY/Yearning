//go:build integration

// newai03_bootstrap_integration_test.go NEW-AI-03 end-to-end regression (real
// PostgreSQL + Fake Provider, sharing the same fixture system as b7/SEC-0041; delivered without local
// execution in this batch — executed centrally by the main repair agent against a live PG environment):
//
//	token-only bootstrap: a scripted fake model extracts anonymous tokens exclusively from the system
//	  prompt legend to invoke schema_metadata -> tool is allowed and reaches FactSource, persisting
//	  tokenized facts (tool facts are non-empty);
//	out-of-bounds real name: the model uses a real schema name outside the legend -> booked as 2012,
//	  zero FactSource calls (never touching the DB), and stage gate_reason_codes marks evidence_unavailable
//	  (unavailable evidence is identifiable rather than silently degrading to model_only).
//
// Relationship to the existing SEC-0002 regression (security_regression_integration_test.go):
// that test's fake model called tools directly with real names shop/orders (a valid path — shop is
// inside the frozen binding set, whose behavior is unchanged), which did not cover the real Provider
// scenario where the model can only see the legend; this file closes that gap with a legend-driven script.
package application

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// legendSchemaTokenRe extracts the first schema token from the system prompt legend section
// (NEW-AI-03: the sole source of schema values available to a real model).
var legendSchemaTokenRe = regexp.MustCompile(`schema_[0-9]+`)

// TestNEWAI03TokenOnlyBootstrapToolFactsNonEmpty a scripted fake model invokes schema_metadata using
// only the token from the legend: the tool is allowed, FactSource is reached, and tokenized facts are persisted (non-empty).
func TestNEWAI03TokenOnlyBootstrapToolFactsNonEmpty(t *testing.T) {
	b := newB7Fixture(t)
	turn := 0
	sawLegend := false
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		turn++
		if turn > 1 {
			return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
		}
		// Model perspective: the legend is the sole source of schema values; extraction failure fails the test
		// immediately (missing bootstrap channel).
		matches := legendSchemaTokenRe.FindStringSubmatch(req.SystemPrompt)
		if matches == nil {
			t.Fatal("schema legend token missing from system prompt (bootstrap channel broken)")
		}
		tables, err := scopedTablesFromRequest(req)
		if err != nil {
			return nil, 500, err
		}
		if len(tables) == 0 {
			return nil, 500, errors.New("table legend token missing from provider context")
		}
		if tables[0].Schema != matches[0] {
			return nil, 500, errors.New("schema object token diverges from system-prompt legend")
		}
		sawLegend = true
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.", "findings": []map[string]any{},
			"tool_requests": []map[string]any{{
				"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000009",
				"tool_name":    "schema_metadata",
				"arguments": map[string]any{
					"schema": tables[0].Schema, "table": tables[0].Table,
				},
			}},
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	})
	runID := b.runAgent(t, deps)
	if !sawLegend {
		t.Fatal("provider never received the token legend")
	}
	// Tool is allowed and reaches FactSource (token detokenizes -> in-binding real schema).
	if b.facts.scopedMetadataCalls != 1 {
		t.Fatalf("legend tokens must reach FactSource: scopedMetadataCalls=%d", b.facts.scopedMetadataCalls)
	}
	factID := toolEvidenceFact(t, b.db, runID)
	if factID == "" {
		t.Fatal("tool facts must be non-empty via legend bootstrap")
	}
	// Persisted normalized fact contains only tokens (no real schema name).
	var fact struct {
		Ciphertext []byte `gorm:"column:normalized_fact_ciphertext"`
		Nonce      []byte `gorm:"column:normalized_fact_nonce"`
		KeyID      string `gorm:"column:normalized_fact_key_id"`
		Algorithm  string `gorm:"column:normalized_fact_algorithm"`
	}
	if err := b.db.Raw(`select normalized_fact_ciphertext, normalized_fact_nonce,
		normalized_fact_key_id, normalized_fact_algorithm from evidence_facts where id = ?::uuid`, factID).
		Scan(&fact).Error; err != nil {
		t.Fatal(err)
	}
	opened, err := openPayload(b.svc.Crypter, "evidence_facts", factID, "normalized_fact_ciphertext", crypto.Envelope{
		Ciphertext: fact.Ciphertext, Nonce: fact.Nonce, KeyID: fact.KeyID, Algorithm: fact.Algorithm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(opened), `"schema":"shop"`) {
		t.Fatalf("normalized fact must be tokenized: %s", opened)
	}
	if strings.Contains(string(opened), `"table":"orders"`) {
		t.Fatalf("normalized fact must tokenize table: %s", opened)
	}
	if !strings.Contains(string(opened), "schema_") {
		t.Fatalf("normalized fact must carry anonymous token: %s", opened)
	}
}

// TestNEWAI03OutOfLegendRealNameFailsClosed model uses a real schema name outside the legend:
// booked as 2012 with zero FactSource calls; stage gate_reason_codes includes
// evidence_unavailable (identifiable zero-evidence state).
func TestNEWAI03OutOfLegendRealNameFailsClosed(t *testing.T) {
	b := newB7Fixture(t)
	turn := 0
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		turn++
		var tools []map[string]any
		if turn == 1 {
			tools = []map[string]any{{
				"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000009",
				"tool_name":    "schema_metadata",
				"arguments":    map[string]any{"schema": "otherdb"},
			}}
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.",
			"findings": []map[string]any{{
				"finding_key": "skill.nowhere", "category": "correctness", "severity": "high",
				"title": "Unbounded mutation without WHERE", "message": "The statement has no WHERE condition.", "suggestion": "Add a WHERE condition.",
				"fingerprint_group_ids": []string{req.AnonymizedStatementGroups[0].GroupID},
				"evidence_ids":          []string{}, "fact_basis": "model_only",
			}},
			"tool_requests": tools,
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	})
	runID := b.runAgent(t, deps)
	// Never touches the DB: zero calls to evidence collection methods.
	if b.facts.calls != 0 || b.facts.metadataCalls != 0 {
		t.Fatalf("out-of-legend call must not touch fact source: calls=%d metadata=%d",
			b.facts.calls, b.facts.metadataCalls)
	}
	// Booked as 2012.
	var code *int
	var success bool
	if err := b.db.Raw(`select error_code, success from review_tool_calls
		where review_run_id = ? and tool_kind = 'builtin_database_fact'`, runID).
		Row().Scan(&code, &success); err != nil {
		t.Fatal(err)
	}
	if success || code == nil || *code != 2012 {
		t.Fatalf("out-of-legend tool call must be 2012-failed: success=%v code=%v", success, code)
	}
	// Stage gate_reason_codes marks evidence_unavailable.
	// gate_reason_codes is text[]: after casting to scalar via ::text it must be scanned into a string
	// (a []byte target would cause GORM to convert element-by-element as a uint8 slice and fail).
	var reasonsRaw string
	if err := b.db.Raw(`select gate_reason_codes::text from stage_review_snapshots
		where review_run_id = ? limit 1`, runID).Scan(&reasonsRaw).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reasonsRaw), "evidence_unavailable") {
		t.Fatalf("evidence_unavailable reason missing: %s", reasonsRaw)
	}
	var state string
	var businessCode *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).
		Row().Scan(&state, &businessCode); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStatePartial) || businessCode == nil || *businessCode != 2007 {
		t.Fatalf("failed evidence request must make review partial, got %s/%v", state, businessCode)
	}
}
