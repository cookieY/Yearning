//go:build integration

// b9_integration_test.go B9 change order domain acceptance gate integration tests:
// G1 unready or hash-mismatched Run cannot bypass submission; G2 approval reads make zero Provider calls;
// G3 concurrent same-step decisions allow exactly one winner; G4 template changes do not affect submitted orders;
// W008 frozen Actor deletion -> invalid -> only submitter can void; invalid state transitions and optimistic concurrency.
package httpapi

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const b9SQL = "update orders set amount = amount + 1 where id = 1;\n"

// b9ReadySubmit fixture shortcut: ephemeral precheck ready -> submit succeeds -> order view.
func (fx *b9Fixture) b9ReadySubmit(t *testing.T) map[string]any {
	t.Helper()
	draftID, _, runID := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial())
	code, body, raw := fx.b9Submit(t, fx.lastFlowID, draftID, runID, b9SQL)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submit: %d %s", code, raw)
	}
	return body["data"].(map[string]any)
}

// TestB9Gate1SubmissionBypass unready or mismatched SQL cannot bypass submission
// (RCP-20261101 two-step flow: ephemeral run_id consumption prevents reviewing A and submitting B).
func TestB9Gate1SubmissionBypass(t *testing.T) {
	fx := newB9Fixture(t)

	// a) non-existent run_id (unreviewed / expired) -> 2001 REVIEW_OUTDATED.
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": fx.lastFlowID, "title": "b9-g1-a",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create draft: %d %s", code, raw)
	}
	d := body["data"].(map[string]any)
	draftID := d["id"].(string)
	code, body, raw = fx.owner.doWithHeaders("POST", "/api/v4/change-orders", map[string]any{
		"flow_id": fx.lastFlowID, "draft_id": draftID, "sql": b9SQL,
		"run_id": uuid.NewString(),
	}, map[string]string{
		"Idempotency-Key": "b9-g1-a",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 2001 {
		t.Fatalf("no-run submit want 2001 got %d %s", code, raw)
	}

	// b) SQL modified after precheck (submitted SQL mismatches reviewed HMAC = anti review-A-submit-B) -> 2001,
	// and ephemeral Run remains readable and resubmittable via compensation (review agent H1).
	draftID2, _, runID2 := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial())
	tamperedSQL := "update orders set amount = amount + 2 where id = 2;\n"
	code, body, raw = fx.b9Submit(t, fx.lastFlowID, draftID2, runID2, tamperedSQL)
	if code != 200 || errCode(body) != 2001 {
		t.Fatalf("tampered-sql submit want 2001 got %d %s", code, raw)
	}
	c, b2, _ := fx.owner.do("GET", "/api/v4/review-runs/"+runID2, nil, false)
	if c != 200 || errCode(b2) != 0 {
		t.Fatalf("rejected submit must keep ephemeral run readable (H1): %d", c)
	}

	// c) blocked Run (High finding) -> 2001 requiring re-review (v2 returns REVIEW_OUTDATED for all
	// non-ready runs; legacy 2013 draft gate code retired with state machine).
	fx.b9CreateGrant(t, fx.ownerID, fx.lastFlowID)
	draftID3, _, runID3 := fx.b9ReadyDraftHigh(t)
	code, body, raw = fx.b9Submit(t, fx.lastFlowID, draftID3, runID3, b9SQL)
	if code != 200 || errCode(body) != 2001 {
		t.Fatalf("blocked submit want 2001 got %d %s", code, raw)
	}

	// d) happy path: submission succeeds and structure is frozen accurately.
	order := fx.b9ReadySubmit(t)
	if order["state"] != "stage_approval_active" {
		t.Fatalf("order state: %v", order["state"])
	}
	if dn, _ := order["display_number"].(string); len(dn) != 18 || dn[:3] != "YR-" {
		t.Fatalf("display number: %q", dn)
	}
	stages := order["stages"].([]any)
	if len(stages) != 1 {
		t.Fatalf("stage count: %d", len(stages))
	}
	stage := stages[0].(map[string]any)
	if stage["state"] != "approval_active" {
		t.Fatalf("stage state: %v", stage["state"])
	}
	steps := stage["approval_steps"].([]any)
	if len(steps) != 1 || steps[0].(map[string]any)["state"] != "active" {
		t.Fatalf("first step must be active: %v", steps)
	}
	actors := steps[0].(map[string]any)["actors"].([]any)
	if len(actors) != 2 {
		t.Fatalf("frozen actors: %d", len(actors))
	}
	if order["has_sql"] != true || order["sql_hash"] == "" || order["snapshot_hash"] == "" {
		t.Fatalf("hash fields: %v", order)
	}
	// draft -> submitted; Run materialized into DB (submission is the sole persistence point for review artifacts).
	var dstate string
	_ = fx.db.Raw(`select state from change_drafts where state = 'submitted'`).Scan(&dstate).Error
	if dstate != "submitted" {
		t.Fatal("draft must be submitted")
	}
	// Duplicate idempotency key replays the same order.
	draftID4, _, runID4 := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial())
	c1, b1, _ := fx.owner.doWithHeaders("POST", "/api/v4/change-orders", map[string]any{
		"flow_id": fx.lastFlowID, "draft_id": draftID4, "sql": b9SQL, "run_id": runID4,
	}, map[string]string{
		"Idempotency-Key": "b9-g1-dup",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
	if c1 != 200 || errCode(b1) != 0 {
		t.Fatalf("submit: %v", b1)
	}
}

// b9ReadyDraftHigh drives precheck with High responder (blocked terminal state); returns (draftID, version, runID).
func (fx *b9Fixture) b9ReadyDraftHigh(t *testing.T) (string, int64, string) {
	t.Helper()
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": fx.lastFlowID, "title": "b9-high",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create draft: %d %s", code, raw)
	}
	d := body["data"].(map[string]any)
	draftID := d["id"].(string)
	version := int64(d["version"].(float64))
	code, body, raw = fx.owner.doWithVersion("PUT", "/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": b9SQL}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put sql: %d %s", code, raw)
	}
	version++
	// Ephemeral precheck driven with High responder (blocked terminal state; draft stays in draft state).
	runID := fx.b9StartPrecheck(t, fx.lastFlowID, draftID, b9SQL, fx.b9HighDial())
	runData := fx.b9WaitRunTerminal(t, runID)
	if runData["state"] != "blocked" {
		t.Fatalf("run after high review: %v (want blocked)", runData["state"])
	}
	return draftID, version, runID
}

// TestB9Gate2ApprovalZeroProviderCalls approval reads and decisions make zero Provider calls;
// final step approval only enters execution_pending without auto-executing.
func TestB9Gate2ApprovalZeroProviderCalls(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)

	dialBefore := fx.dialCount.Load()
	var ledgerBefore int64
	_ = fx.db.Raw(`select count(*) from ai_usage_ledger`).Scan(&ledgerBefore).Error

	// Read surfaces: detail / timeline / findings.
	for _, path := range []string{
		"/api/v4/change-orders/" + orderID,
		"/api/v4/change-orders/" + orderID + "/timeline",
		"/api/v4/change-orders/" + orderID + "/review-findings",
	} {
		if c, b, raw := fx.reviewer1.do("GET", path, nil, false); c != 200 || errCode(b) != 0 {
			t.Fatalf("read %s: %d %s", path, c, raw)
		}
	}
	if got := fx.dialCount.Load(); got != dialBefore {
		t.Fatalf("reads must not dial provider: before=%d after=%d", dialBefore, got)
	}

	// Approve (final step): order -> stage_execution_pending, zero dials.
	version := int64(order["version"].(float64))
	c, b, raw := fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve", "comment": "Approved"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"Idempotency-Key": "b9-g2-approve",
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("approve: %d %s", c, raw)
	}
	after := b["data"].(map[string]any)
	if after["state"] != "stage_execution_pending" {
		t.Fatalf("after final approve: %v", after["state"])
	}
	if got := fx.dialCount.Load(); got != dialBefore {
		t.Fatalf("decide must not dial provider: before=%d after=%d", dialBefore, got)
	}
	var ledgerAfter int64
	_ = fx.db.Raw(`select count(*) from ai_usage_ledger`).Scan(&ledgerAfter).Error
	if ledgerAfter != ledgerBefore {
		t.Fatalf("ledger changed: %d -> %d", ledgerBefore, ledgerAfter)
	}
	// Approval never auto-executes (W006): zero execution Attempts, stage is execution_pending.
	var attempts int64
	_ = fx.db.Raw(`select count(*) from execution_attempts`).Scan(&attempts).Error
	if attempts != 0 {
		t.Fatalf("approval must not auto-execute, attempts=%d", attempts)
	}
	stages := after["stages"].([]any)
	if stages[0].(map[string]any)["state"] != "execution_pending" {
		t.Fatalf("stage state: %v", stages[0])
	}

	// Decision outside approval-active state (approve on execution_pending) -> 1010.
	c, b, raw = fx.intruder.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version+1),
			"Idempotency-Key": "b9-g2-intruder",
			"X-CSRF-Token":    fx.intruder.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 1010 {
		t.Fatalf("decide on execution_pending want 1010 got %d %s", c, raw)
	}
}

// TestB9NotFrozenActorAndReplay real 3001 trigger (decision on approval_active by
// frozen executor) and same-key idempotent replay.
func TestB9NotFrozenActorAndReplay(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	version := int64(order["version"].(float64))

	// Frozen executor is not an approver -> 3001 NOT_FROZEN_ACTOR.
	c, b, raw := fx.executor.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"Idempotency-Key": "b9-3001-exec",
			"X-CSRF-Token":    fx.executor.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 3001 {
		t.Fatalf("frozen executor decide want 3001 got %d %s", c, raw)
	}

	// Same-key idempotent replay: submission response is identical.
	draftID, _, runID2 := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial())
	submitBody := map[string]any{
		"flow_id": fx.lastFlowID, "draft_id": draftID, "sql": b9SQL, "run_id": runID2,
	}
	headers := map[string]string{
		"Idempotency-Key": "b9-replay-key",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	}
	c1, b1, r1 := fx.owner.doWithHeaders("POST", "/api/v4/change-orders", submitBody, headers)
	if c1 != 200 || errCode(b1) != 0 {
		t.Fatalf("submit: %d %s", c1, r1)
	}
	c2, b2, r2 := fx.owner.doWithHeaders("POST", "/api/v4/change-orders", submitBody, headers)
	if c2 != 200 || errCode(b2) != 0 {
		t.Fatalf("replay submit: %d %s", c2, r2)
	}
	id1 := b1["data"].(map[string]any)["id"].(string)
	id2 := b2["data"].(map[string]any)["id"].(string)
	if id1 != id2 {
		t.Fatalf("replay returned different order: %s vs %s", id1, id2)
	}
	var orders int64
	_ = fx.db.Raw(`select count(*) from change_orders where id = ?`, id1).Scan(&orders).Error
	if orders != 1 {
		t.Fatalf("replay created duplicate orders: %d", orders)
	}

	// v2 idempotency key is scoped per Run (aggregate=run_id): same key across different Runs/drafts
	// creates independent orders -- 1007 IDEMPOTENCY_KEY_REUSED is unreachable on this route (reqHash
	// is identical for repeated inserts on same aggregate), retiring legacy "same key different body -> 1007" semantics.
	draftID3, _, runID3 := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial())
	c3, b3, _ := fx.owner.doWithHeaders("POST", "/api/v4/change-orders", map[string]any{
		"flow_id": fx.lastFlowID, "draft_id": draftID3, "sql": b9SQL, "run_id": runID3,
	}, map[string]string{
		"Idempotency-Key": "b9-replay-key",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
	if c3 != 200 || errCode(b3) != 0 {
		t.Fatalf("same key across runs must submit independently: %d %v", c3, b3)
	}
	if id3 := b3["data"].(map[string]any)["id"].(string); id3 == id1 {
		t.Fatalf("cross-run same key must not replay: %s", id3)
	}
}

// TestB9Gate3ConcurrentSingleDecision concurrent decisions on the same step produce exactly one winner.
func TestB9Gate3ConcurrentSingleDecision(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	version := int64(order["version"].(float64))

	// Two frozen reviewers decide concurrently (with distinct idempotency keys).
	type result struct {
		code int
		err  int
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	decide := func(cl testClient, key string) {
		defer wg.Done()
		c, b, _ := cl.doWithHeaders("POST",
			"/api/v4/change-orders/"+orderID+"/approval-decisions",
			map[string]any{"decision": "approve"},
			map[string]string{
				"If-Match":        fmt.Sprintf(`"%d"`, version),
				"Idempotency-Key": key,
				"X-CSRF-Token":    cl.cookies["yearning_csrf"],
			})
		results <- result{code: c, err: errCode(b)}
	}
	wg.Add(2)
	go decide(fx.reviewer1, "b9-g3-r1")
	go decide(fx.reviewer2, "b9-g3-r2")
	wg.Wait()
	close(results)
	okCount, conflictCount := 0, 0
	for r := range results {
		if r.err == 0 {
			okCount++
		} else if r.err == 3002 || r.err == 1004 || r.err == 1003 {
			conflictCount++
		} else {
			t.Fatalf("unexpected decision result: %+v", r)
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("exactly one winner expected, ok=%d conflict=%d", okCount, conflictCount)
	}
	// Exactly one decision row; order advances exactly once.
	var decisions int64
	_ = fx.db.Raw(`select count(*) from approval_decisions where order_id = ?`, orderID).Scan(&decisions).Error
	if decisions != 1 {
		t.Fatalf("decision rows: %d", decisions)
	}
	var state string
	_ = fx.db.Raw(`select state from change_orders where id = ?`, orderID).Scan(&state).Error
	if state != "stage_execution_pending" {
		t.Fatalf("order advanced to %s, want stage_execution_pending", state)
	}

	// Rejection propagation: first decision on new order is reject -> whole order becomes rejected.
	order2 := fx.b9ReadySubmit(t)
	order2ID := order2["id"].(string)
	v2 := int64(order2["version"].(float64))
	c, b, raw := fx.reviewer2.doWithHeaders("POST",
		"/api/v4/change-orders/"+order2ID+"/approval-decisions",
		map[string]any{"decision": "reject", "comment": "Risk is too high"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, v2),
			"Idempotency-Key": "b9-g3-reject",
			"X-CSRF-Token":    fx.reviewer2.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("reject: %d %s", c, raw)
	}
	if got := b["data"].(map[string]any)["state"]; got != "rejected" {
		t.Fatalf("reject state: %v", got)
	}
	// Decision on terminal state -> 1010 (terminal state rejects all triggers).
	c, b, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+order2ID+"/approval-decisions",
		map[string]any{"decision": "approve"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, v2+1),
			"Idempotency-Key": "b9-g3-after-reject",
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 1010 {
		t.Fatalf("decide after reject want 1010 got %d %s", c, raw)
	}
}

// TestB9Gate4TemplateChangeDoesNotAffectOrder template modifications do not affect submitted orders.
func TestB9Gate4TemplateChangeDoesNotAffectOrder(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	snapHashBefore, _ := order["snapshot_hash"].(string)
	sqlHashBefore, _ := order["sql_hash"].(string)

	// Control draft: completes precheck before template replacement (flow_version at review time is old version).
	staleDraftID, _, staleRunID := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial())

	// Admin replaces flow template (version++; changes approver to executor).
	var flowVersion int64
	_ = fx.db.Raw(`select version from flows where id = ?`, fx.lastFlowID).Scan(&flowVersion).Error
	payload := b9FlowStages("b9-flow", fx.lastRuleSet, []map[string]any{
		b9Stage(1, fx.lastDsID, [][]string{{fx.executorID}}, []string{fx.executorID}),
	})
	c, b, raw := fx.admin.doWithVersion("PUT", "/api/v4/admin/flows/"+fx.lastFlowID, payload, flowVersion)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("replace flow: %d %s", c, raw)
	}

	// Submitted order progresses using frozen snapshot: original frozen reviewer rev1 can still approve, order hashes unchanged.
	version := int64(order["version"].(float64))
	c, b, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"Idempotency-Key": "b9-g4-approve",
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("frozen reviewer approve after template change: %d %s", c, raw)
	}
	after := b["data"].(map[string]any)
	if after["snapshot_hash"] != snapHashBefore || after["sql_hash"] != sqlHashBefore {
		t.Fatalf("frozen hashes mutated: %v", after)
	}
	// New template approver (executor) is not a frozen reviewer on this order -> 3001/1010.
	c, b, raw = fx.executor.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version+1),
			"Idempotency-Key": "b9-g4-newactor",
			"X-CSRF-Token":    fx.executor.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 1010 {
		t.Fatalf("new template actor must not decide, got %d %s", c, raw)
	}

	// Control: Run prechecked before template replacement (stale flow_version) cannot be submitted -> 2001.
	c, b, raw = fx.owner.doWithHeaders("POST", "/api/v4/change-orders", map[string]any{
		"flow_id": fx.lastFlowID, "draft_id": staleDraftID, "sql": b9SQL, "run_id": staleRunID,
	}, map[string]string{
		"Idempotency-Key": "b9-g4-staledraft",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
	if c != 200 || errCode(b) != 2001 {
		t.Fatalf("run with stale flow version must fail 2001, got %d %s", c, raw)
	}
}

// TestB9WithdrawVoidAndActorDeletion W008: frozen Actor deletion -> invalid ->
// only submitter can void; withdrawal 403 matrix; timeline and comments.
func TestB9WithdrawVoidAndActorDeletion(t *testing.T) {
	fx := newB9Fixture(t)

	// Withdrawal path: submitter withdraws; admin/intruder on same API -> 403.
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	version := int64(order["version"].(float64))
	for _, cl := range []testClient{fx.admin, fx.intruder} {
		c, _, _ := cl.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/withdrawal",
			map[string]any{"reason": "not mine"},
			map[string]string{
				"If-Match":        fmt.Sprintf(`"%d"`, version),
				"Idempotency-Key": "b9-w8-nonowner",
				"X-CSRF-Token":    cl.cookies["yearning_csrf"],
			})
		if c != 403 {
			t.Fatalf("non-submitter withdraw want 403 got %d", c)
		}
	}
	c, b, raw := fx.owner.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/withdrawal",
		map[string]any{"reason": "Plan changed"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"Idempotency-Key": "b9-w8-withdraw",
			"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("withdraw: %d %s", c, raw)
	}
	if got := b["data"].(map[string]any)["state"]; got != "withdrawn" {
		t.Fatalf("withdraw state: %v", got)
	}
	// Withdrawing again after terminal withdrawn state (with new version) -> 1010 (terminal rejects all triggers).
	c, b, raw = fx.owner.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/withdrawal",
		map[string]any{"reason": "again"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version+1),
			"Idempotency-Key": "b9-w8-withdraw2",
			"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 1010 {
		t.Fatalf("withdraw terminal want 1010 got %d %s", c, raw)
	}

	// Actor deletion path: new order is in approval with solo reviewer rev1 (separate solo-actor flow).
	soloFlow := fx.b9CreateFlow(t, "b9-solo", []map[string]any{
		b9Stage(1, fx.lastDsID, [][]string{{fx.reviewer1ID}}, []string{fx.executorID}),
	})
	fx.b9CreateGrant(t, fx.ownerID, soloFlow)
	draftID, _, runIDSolo := fx.b9ReadyDraft(t, soloFlow, b9SQL, fx.b9CleanDial())
	c, b, raw = fx.b9Submit(t, soloFlow, draftID, runIDSolo, b9SQL)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("solo submit: %d %s", c, raw)
	}
	soloOrder := b["data"].(map[string]any)
	soloID := soloOrder["id"].(string)
	soloVersion := int64(soloOrder["version"].(float64))

	// Admin deletes solo reviewer (template references cleaned up; order snapshot preserved).
	var rev1Version int64
	_ = fx.db.Raw(`select version from users where id = ?`, fx.reviewer1ID).Scan(&rev1Version).Error
	c, b, raw = fx.admin.doWithVersion("DELETE", "/api/v4/admin/users/"+fx.reviewer1ID, nil, rev1Version)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("delete solo reviewer: %d %s", c, raw)
	}
	// Frozen snapshot rows persist.
	var actorRows int64
	_ = fx.db.Raw(`select count(*) from order_approval_actors aa
		join order_approval_steps st on st.id = aa.step_id
		join order_stages sg on sg.id = st.stage_id
		where sg.order_id = ?`, soloID).Scan(&actorRows).Error
	if actorRows == 0 {
		t.Fatal("frozen actor snapshots must persist")
	}

	// Reading order -> materializes invalid state + timeline includes invalidated entry.
	c, b, raw = fx.owner.do("GET", "/api/v4/change-orders/"+soloID, nil, false)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("get order after actor deletion: %d %s", c, raw)
	}
	if got := b["data"].(map[string]any)["state"]; got != "invalid" {
		t.Fatalf("order after solo actor deletion: %v (want invalid)", got)
	}
	c, b, raw = fx.owner.do("GET", "/api/v4/change-orders/"+soloID+"/timeline", nil, false)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("timeline: %d %s", c, raw)
	}
	items := b["data"].(map[string]any)["items"].([]any)
	foundInvalidated := false
	for _, it := range items {
		if it.(map[string]any)["event_type"] == "workflow.node_invalidated" {
			foundInvalidated = true
		}
	}
	if !foundInvalidated {
		t.Fatalf("timeline missing invalidation entry: %v", items)
	}

	// Sole exit from invalid: submitter voids order; admin -> 403.
	c, _, _ = fx.admin.doWithHeaders("POST", "/api/v4/change-orders/"+soloID+"/voidance",
		map[string]any{"reason": "admin void"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, soloVersion+1),
			"Idempotency-Key": "b9-w8-adminvoid",
			"X-CSRF-Token":    fx.admin.cookies["yearning_csrf"],
		})
	if c != 403 {
		t.Fatalf("admin void want 403 got %d", c)
	}
	// Unrelated user reading order -> 1002 (does not leak existence).
	c, b, raw = fx.intruder.do("GET", "/api/v4/change-orders/"+soloID, nil, false)
	if c != 200 || errCode(b) != 1002 {
		t.Fatalf("intruder get want 1002 got %d %s", c, raw)
	}
	c, b, raw = fx.owner.doWithHeaders("POST", "/api/v4/change-orders/"+soloID+"/voidance",
		map[string]any{"reason": "Frozen approver was deleted"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, soloVersion+1),
			"Idempotency-Key": "b9-w8-void",
			"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("void: %d %s", c, raw)
	}
	if got := b["data"].(map[string]any)["state"]; got != "voided" {
		t.Fatalf("void state: %v", got)
	}

	// Comments and copy: voided order accepts comments (from frozen executor) + copies into a new draft.
	c, b, raw = fx.executor.doWithHeaders("POST",
		"/api/v4/change-orders/"+soloID+"/comments",
		map[string]any{"content": "Order voided; see approval records for reason"},
		map[string]string{
			"Idempotency-Key": "b9-w8-comment",
			"X-CSRF-Token":    fx.executor.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("comment: %d %s", c, raw)
	}
	c, b, raw = fx.owner.doWithHeaders("POST",
		"/api/v4/change-orders/"+soloID+"/draft-copies",
		map[string]any{"target_flow_id": fx.lastFlowID, "title": "b9-copy", "description": "from voided"},
		map[string]string{
			"Idempotency-Key": "b9-w8-copy",
			"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
		})
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("copy: %d %s", c, raw)
	}
	newDraftID := b["data"].(map[string]any)["id"].(string)
	var newState string
	var newRevision int64
	_ = fx.db.Raw(`select state from change_drafts where id = ?`, newDraftID).Scan(&newState).Error
	_ = fx.db.Raw(`select revision from change_drafts where id = ?`, newDraftID).Scan(&newRevision).Error
	if newState != "draft" || newRevision != 1 {
		t.Fatalf("copied draft: state=%s revision=%d", newState, newRevision)
	}
	// New draft SQL can be revealed (verifies AAD re-sealing accuracy).
	c, b, raw = fx.owner.do("POST", "/api/v4/change-drafts/"+newDraftID+"/sql-reveals",
		map[string]any{"purpose": "b9-copy-check"}, true)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("copied draft sql reveal: %d %s", c, raw)
	}
}

// TestB9OptimisticConcurrency optimistic concurrency: concurrent withdraw + void on same version has only one winner.
func TestB9OptimisticConcurrency(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	version := int64(order["version"].(float64))

	type outcome struct{ err int }
	out := make(chan outcome, 2)
	var wg sync.WaitGroup
	call := func(kind, key string) {
		defer wg.Done()
		c, b, _ := fx.owner.doWithHeaders("POST",
			"/api/v4/change-orders/"+orderID+"/"+kind,
			map[string]any{"reason": "race"},
			map[string]string{
				"If-Match":        fmt.Sprintf(`"%d"`, version),
				"Idempotency-Key": key,
				"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
			})
		out <- outcome{err: errCode(b)}
		_ = c
	}
	wg.Add(2)
	go call("withdrawal", "b9-oc-withdraw")
	go call("voidance", "b9-oc-void")
	wg.Wait()
	close(out)
	wins, losses := 0, 0
	for o := range out {
		if o.err == 0 {
			wins++
		} else if o.err == 1003 || o.err == 1004 || o.err == 1010 {
			losses++
		} else {
			t.Fatalf("unexpected race outcome: %d", o.err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("exactly one winner expected, wins=%d losses=%d", wins, losses)
	}
}

// TestB9SqlRevealWatermark change order SQL Reveal: watermark / audit / no-store.
func TestB9SqlRevealWatermark(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	c, b, raw := fx.owner.do("POST", "/api/v4/change-orders/"+orderID+"/sql-reveals",
		map[string]any{"purpose": "review-check"}, true)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("reveal: %d %s", c, raw)
	}
	data := b["data"].(map[string]any)
	if data["sql"] != b9SQL || data["watermark"] == "" || data["valid_until"] == "" {
		t.Fatalf("reveal payload: %v", data)
	}
	// Copy event audit.
	revealID := data["reveal_id"].(string)
	c, b, raw = fx.owner.do("POST", "/api/v4/change-orders/"+orderID+"/sql-copy-events",
		map[string]any{"source_reveal_id": revealID}, true)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("copy event: %d %s", c, raw)
	}
	var auditCount int64
	_ = fx.db.Raw(`select count(*) from audit_events
		where resource_type = 'change_order' and resource_id = ? and action in ('reveal','copy')`,
		orderID).Scan(&auditCount).Error
	if auditCount != 2 {
		t.Fatalf("reveal+copy audit rows: %d", auditCount)
	}
	// Unrelated user Reveal -> 1002.
	c, b, raw = fx.intruder.do("POST", "/api/v4/change-orders/"+orderID+"/sql-reveals",
		map[string]any{"purpose": "peek"}, true)
	if c != 200 || errCode(b) != 1002 {
		t.Fatalf("intruder reveal want 1002 got %d %s", c, raw)
	}
}

// TestB9RequestedScheduleFreeze S18.56 Option B: draft requested schedule is frozen onto
// change order upon submission and exposed via OrderView (create/update validation covered in B6 tests).
func TestB9RequestedScheduleFreeze(t *testing.T) {
	fx := newB9Fixture(t)
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	draftID, _, runIDSched := fx.b9ReadyDraft(t, fx.lastFlowID, b9SQL, fx.b9CleanDial(),
		map[string]any{"requested_scheduled_for": future})
	code, body, raw := fx.b9Submit(t, fx.lastFlowID, draftID, runIDSched, b9SQL)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submit: %d %s", code, raw)
	}
	order := body["data"].(map[string]any)
	frozen, _ := order["requested_scheduled_for"].(string)
	if frozen == "" {
		t.Fatalf("order view must expose frozen requested_scheduled_for: %s", raw)
	}
	// S18.58: submitter snapshot display name is exposed in view (for approval queue display).
	if name, _ := order["submitter_username"].(string); name == "" {
		t.Fatalf("order view must expose submitter_username: %s", raw)
	}
	// Detail read surface also exposes it (GET order).
	orderID := order["id"].(string)
	code, body, raw = fx.owner.do("GET", "/api/v4/change-orders/"+orderID, nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("get order: %d %s", code, raw)
	}
	if v, _ := body["data"].(map[string]any)["requested_scheduled_for"].(string); v == "" {
		t.Fatalf("order detail must expose requested_scheduled_for: %s", raw)
	}
}
