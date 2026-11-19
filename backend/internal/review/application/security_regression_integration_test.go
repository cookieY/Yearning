//go:build integration

// security_regression_integration_test.go Database-level regressions for security fixes
// (requires real PostgreSQL; shares the same fixture system as b7/b8):
//
//	SEC-0002: Provider request bytes and persisted normalized facts contain only anonymous tokens;
//	  raw payloads (local ciphertext channel) preserve real identifiers (visible via owner raw reveal);
//	SEC-0011: failure-path Settle + Release net balance never double-deducts in-flight reservations
//	  (including concurrency invariant);
//	SEC-0035: the source reveal referenced by RecordEvidenceCopy must be issued by a raw_revealed
//	  audit record for the current user and this evidence (other user / other evidence / fabricated / empty are rejected);
//	SEC-0042: end-to-end raw reveal on the real Runner path succeeds.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// TestSEC0002AgentEvidenceTokenizedAndRawRevealable full flow: model requests index_metadata tool ->
// turn-2 Provider request bytes and persisted normalized facts contain only anonymous tokens; raw
// payload preserves the real index name and can be revealed by the owner.
func TestSEC0002AgentEvidenceTokenizedAndRawRevealable(t *testing.T) {
	b := newB7Fixture(t)
	var secondRoundEvidence []byte
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if len(req.NormalizedEvidence) > 0 {
			secondRoundEvidence, _ = json.Marshal(req)
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
		// Turn 1 establishes the table metadata baseline, then requests index_metadata
		// (fakeFactSource returns the PRIMARY index).
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
	if len(secondRoundEvidence) == 0 {
		t.Fatal("provider never received tokenized evidence")
	}
	if strings.Contains(strings.ToLower(string(secondRoundEvidence)), "primary") {
		t.Fatalf("SEC-0002: provider request bytes contain real index name")
	}
	if !strings.Contains(string(secondRoundEvidence), "index_1") {
		t.Fatalf("SEC-0002: provider request bytes missing anonymous token")
	}
	factID := toolEvidenceFactNamed(t, b.db, runID, "index_metadata")
	var fact struct {
		Ciphertext []byte `gorm:"column:normalized_fact_ciphertext"`
		Nonce      []byte `gorm:"column:normalized_fact_nonce"`
		KeyID      string `gorm:"column:normalized_fact_key_id"`
		Algorithm  string `gorm:"column:normalized_fact_algorithm"`
	}
	if err := b.db.Raw(`select normalized_fact_ciphertext, normalized_fact_nonce,
		normalized_fact_key_id, normalized_fact_algorithm from evidence_facts where id = ?`, factID).
		Scan(&fact).Error; err != nil {
		t.Fatal(err)
	}
	normPlain, err := openPayload(b.svc.Crypter, "evidence_facts", factID, "normalized_fact_ciphertext", crypto.Envelope{
		Ciphertext: fact.Ciphertext, Nonce: fact.Nonce, KeyID: fact.KeyID, Algorithm: fact.Algorithm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(normPlain), "PRIMARY") {
		t.Fatalf("normalized fact must not contain real index name: %s", normPlain)
	}
	if !strings.Contains(string(normPlain), "index_1") {
		t.Fatalf("normalized fact must carry anonymous token: %s", normPlain)
	}
	// SEC-0042: end-to-end raw reveal on the real Runner path succeeds and preserves real names.
	res, err := b.svc.RevealRawEvidence(context.Background(), b.owner, RequestMeta{}, factID, "inspect")
	if err != nil {
		t.Fatalf("agent-path raw reveal must succeed: %v", err)
	}
	if !strings.Contains(string(res.RawPayload), "PRIMARY") {
		t.Fatalf("raw payload must retain real index name (L7): %s", res.RawPayload)
	}
}

// TestSEC0011FailurePathDoesNotDoubleReleaseReserved budget net-balance invariant:
// after Settle on a failure path, only the unsettled balance is released — in-flight reservations are never double-deducted.
func TestSEC0011FailurePathDoesNotDoubleReleaseReserved(t *testing.T) {
	b := newB7Fixture(t)
	b.enforceBudget(t, 10000)
	ctx := context.Background()
	day := b.svc.budgetDayOf(nowUTC())
	settings, err := b.svc.LoadBudgetSettings(ctx, b.db)
	if err != nil {
		t.Fatal(err)
	}
	const reserve = int64(100)
	// Two in-flight calls (A and B) each reserve 100.
	if err := b.svc.Reserve(ctx, b.db, day, settings.Currency, settings, reserve); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.Reserve(ctx, b.db, day, settings.Currency, settings, reserve); err != nil {
		t.Fatal(err)
	}
	// B fails: Settle settles 100 (failed calls are still faithfully billed), then releases only the remaining balance of 0.
	if err := b.svc.Settle(ctx, b.db, uuid.NewString(), b.providerID, "m", 1, 0, 0, reserve,
		settings.Currency, day, "transient_failed"); err != nil {
		t.Fatal(err)
	}
	if rem := unreleasedReserveMinor(reserve, reserve); rem != 0 {
		t.Fatalf("fully settled call must release nothing, rem=%d", rem)
	}
	// Legacy defect anchor: releasing the full 100 again here would deduct A's still-in-flight reservation
	// as well (underestimating reserved by 100 -> allowing new calls to overspend). Remains 100 after the fix.
	if err := b.svc.Reserve(ctx, b.db, day, settings.Currency, settings, reserve); err != nil {
		t.Fatalf("third reserve must be admitted within budget: %v", err)
	}
	var reserved int64
	if err := b.db.Raw(`select reserved_minor from ai_daily_budget_usage
		where budget_day = ? and currency = ?`, day, settings.Currency).Scan(&reserved).Error; err != nil {
		t.Fatal(err)
	}
	// A's in-flight 100 + third reservation of 100 = 200 (B was settled in full, so its 0 balance is not released again).
	if reserved != 2*reserve {
		t.Fatalf("reserved must equal in-flight reservations (%d), got %d", 2*reserve, reserved)
	}
}

// TestSEC0011ConcurrentFailureAccountingInvariant concurrent failures + in-flight calls:
// after N concurrent calls each Reserve -> partially Settle -> Release remaining balance,
// reserved returns to 0 and amount == N * settledAmount (run under -race).
func TestSEC0011ConcurrentFailureAccountingInvariant(t *testing.T) {
	b := newB7Fixture(t)
	b.enforceBudget(t, 1000000)
	ctx := context.Background()
	day := b.svc.budgetDayOf(nowUTC())
	settings, err := b.svc.LoadBudgetSettings(ctx, b.db)
	if err != nil {
		t.Fatal(err)
	}
	const reserve = int64(100)
	const settle = int64(40) // Partial settlement: remaining balance of 60 must be released exact
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := b.svc.Reserve(ctx, b.db, day, settings.Currency, settings, reserve); err != nil {
				t.Errorf("reserve %d: %v", i, err)
				return
			}
			if err := b.svc.Settle(ctx, b.db, uuid.NewString(), b.providerID, "m", 1,
				0, 0, settle, settings.Currency, day, "transient_failed"); err != nil {
				t.Errorf("settle %d: %v", i, err)
				return
			}
			if rem := unreleasedReserveMinor(reserve, settle); rem != reserve-settle {
				t.Errorf("remainder %d: got %d", i, rem)
				return
			}
			if err := b.svc.Release(ctx, b.db, day, settings.Currency, reserve-settle); err != nil {
				t.Errorf("release %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	var usage struct {
		Amount   int64 `gorm:"column:amount_minor"`
		Reserved int64 `gorm:"column:reserved_minor"`
	}
	if err := b.db.Raw(`select amount_minor, reserved_minor from ai_daily_budget_usage
		where budget_day = ? and currency = ?`, day, settings.Currency).Scan(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Reserved != 0 {
		t.Fatalf("reserved must drain to 0, got %d", usage.Reserved)
	}
	if usage.Amount != n*settle {
		t.Fatalf("amount must equal n*settle=%d, got %d", n*settle, usage.Amount)
	}
}

// toolEvidenceFact returns the database_tool evidence factID for this Run.
func toolEvidenceFact(t *testing.T, db *gorm.DB, runID string) string {
	t.Helper()
	var factID string
	if err := db.Raw(`
		select ef.id::text from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		join review_runs rr on rr.id = s.review_run_id
		where rr.id = ? and ef.source_kind = 'database_tool'
		order by ef.created_at desc limit 1`, runID).Scan(&factID).Error; err != nil {
		t.Fatal(err)
	}
	return factID
}

func toolEvidenceFactNamed(t *testing.T, db *gorm.DB, runID, toolName string) string {
	t.Helper()
	var factID string
	if err := db.Raw(`
		select ef.id::text
		from evidence_facts ef
		join review_tool_calls tc on tc.review_run_id = ?
			and ef.source_ref = 'tool_call/' || tc.id::text
		where tc.tool_name = ?
		order by ef.created_at desc limit 1`, runID, toolName).Scan(&factID).Error; err != nil {
		t.Fatal(err)
	}
	if factID == "" {
		t.Fatalf("%s evidence missing for run %s", toolName, runID)
	}
	return factID
}

// seedEvidenceRevealAudit directly inserts a review_evidence.raw_revealed audit row
// (constructing adversarial cases where an audit record exists but ownership mismatches: another actor or another evidence item).
func seedEvidenceRevealAudit(t *testing.T, db *gorm.DB, actorID, evidenceID, revealID string) {
	t.Helper()
	if err := db.Exec(`
		insert into audit_events (id, event_type, actor_kind, actor_id,
			actor_username_snapshot, resource_type, resource_id, action, outcome,
			metadata, occurred_at, expires_at)
		values (?, 'review_evidence.raw_revealed', 'user', ?, 'mallory', 'review_evidence', ?,
			'reveal', 'succeeded', ?::jsonb, now(), now() + interval '90 days')`,
		uuid.NewString(), actorID, evidenceID,
		fmt.Sprintf(`{"reveal_id":%q,"purpose":"inspect"}`, revealID)).Error; err != nil {
		t.Fatal(err)
	}
}

// TestSEC0035EvidenceCopyVerifiesRevealOwnership the source reveal referenced by RecordEvidenceCopy
// must be issued by a review_evidence.raw_revealed audit record for the current user and this evidence —
// another user's reveal, another evidence item's reveal, or a fabricated/empty revealID uniformly
// returns RESOURCE_NOT_FOUND (ErrNotFound -> 1002) without writing a copy audit row.
func TestSEC0035EvidenceCopyVerifiesRevealOwnership(t *testing.T) {
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
					"tool_call_id": uuid.NewString(),
					"tool_name":    "schema_metadata",
					"arguments": map[string]any{
						"schema": table.Schema, "table": table.Table,
					},
				},
				{
					// tool_call_id is the primary key of review_tool_calls: reusing it across Runs would collide on the
					// unique key and fail the second Run with INTERNAL, so generate a fresh ID on each request.
					"tool_call_id": uuid.NewString(),
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
	runA := b.runAgent(t, deps)
	runB := b.runAgent(t, deps) // Second independent evidence item (preventing cross-evidence reuse)
	factA := toolEvidenceFactNamed(t, b.db, runA, "index_metadata")
	factB := toolEvidenceFactNamed(t, b.db, runB, "index_metadata")
	if factA == factB {
		t.Fatalf("index evidence facts not distinct: %q", factA)
	}
	// User's own real reveal (issues a raw_revealed audit record via the service path).
	resA, err := b.svc.RevealRawEvidence(context.Background(), b.owner, RequestMeta{}, factA, "inspect")
	if err != nil {
		t.Fatalf("reveal factA: %v", err)
	}
	// Another user's reveal audit record for this evidence (the service path would be blocked by owner
	// validation, so insert directly to construct the adversarial state: record exists but actor_id differs).
	foreignReveal := uuid.NewString()
	seedEvidenceRevealAudit(t, b.db, uuid.NewString(), factA, foreignReveal)

	copyAudits := func(evidenceID string) int {
		t.Helper()
		var n int
		if err := b.db.Raw(`select count(*) from audit_events
			where event_type = 'review_evidence.raw_copied' and resource_id = ?`,
			evidenceID).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Valid: user's own reveal for this evidence -> copy audit is persisted and binds source_reveal_id.
	if err := b.svc.RecordEvidenceCopy(context.Background(), b.owner, RequestMeta{}, factA, resA.RevealID); err != nil {
		t.Fatalf("legitimate copy must pass: %v", err)
	}
	if n := copyAudits(factA); n != 1 {
		t.Fatalf("copy audits=%d want 1", n)
	}
	var bound string
	if err := b.db.Raw(`select metadata->>'source_reveal_id' from audit_events
		where event_type = 'review_evidence.raw_copied' and resource_id = ?`,
		factA).Scan(&bound).Error; err != nil {
		t.Fatal(err)
	}
	if bound != resA.RevealID {
		t.Fatalf("copy audit must bind source_reveal_id=%q, got %q", resA.RevealID, bound)
	}

	// Other user's reveal / other evidence's reveal / fabricated / empty — all rejected with zero new audit rows.
	denied := []struct {
		name     string
		evidence string
		revealID string
	}{
		{"foreign actor reveal", factA, foreignReveal},
		{"foreign evidence reveal", factB, resA.RevealID},
		{"fabricated reveal", factA, uuid.NewString()},
		{"empty reveal", factA, ""},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			before := copyAudits(tc.evidence)
			err := b.svc.RecordEvidenceCopy(context.Background(), b.owner, RequestMeta{}, tc.evidence, tc.revealID)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound (RESOURCE_NOT_FOUND/1002), got %v", err)
			}
			if after := copyAudits(tc.evidence); after != before {
				t.Fatalf("rejected copy must not write audit: %d -> %d", before, after)
			}
		})
	}
}
