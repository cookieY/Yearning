// lifecycle_withdraw_cancel_test.go unit tests for withdraw-linked cancellation logic (SEC-0029 /
// RCP-20261021-WITHDRAW-CANCELS-EXECUTION): eligibility evaluation and audit payload
// shape. DB-level behavior (setting cancelling in the same transaction, reconciliation convergence,
// terminal idempotency) is covered in lifecycle_withdraw_cancel_integration_test.go (integration tag).
package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWithdrawCancelEligible(t *testing.T) {
	// Only live running attempts participate in withdraw-linked cancellation.
	if !withdrawCancelEligible("running") {
		t.Fatal("running attempt must be eligible for withdraw-linked cancel")
	}
	// Attempts already in the cancellation channel, terminal state, or not yet started are not re-cancelled (idempotent re-entry protection).
	for _, state := range []string{
		"cancelling", "created", "preflight", "succeeded", "failed",
		"partial_failed", "cancelled", "partial_cancelled", "result_unknown",
	} {
		if withdrawCancelEligible(state) {
			t.Fatalf("state %q must not be re-cancelled by withdraw linkage", state)
		}
	}
}

func TestWithdrawCancelAuditMetadata(t *testing.T) {
	raw, err := json.Marshal(withdrawCancelAuditMetadata("order no longer needed"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("metadata keys %v want exactly [linkage reason]", m)
	}
	if m["linkage"] != "change_order.withdrawn" {
		t.Fatalf("linkage marker %v want change_order.withdrawn", m["linkage"])
	}
	if m["reason"] != "order no longer needed" {
		t.Fatalf("reason %v", m["reason"])
	}
	// Withdraw reason is truncated to 256 characters (consistent with terminal command audits).
	long := strings.Repeat("x", 300)
	truncated := withdrawCancelAuditMetadata(long)["reason"].(string)
	if len(truncated) != 256 {
		t.Fatalf("reason truncation: len=%d want 256", len(truncated))
	}
}
