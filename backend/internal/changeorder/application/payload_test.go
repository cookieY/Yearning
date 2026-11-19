// payload_test.go Unit test for domain event payload schema conformance (SEC-0036):
// approvalDecided def has additionalProperties:false — extra fields like comment in the payload
// would be rejected or dead-lettered by outbox consumers/validators, dropping approval notifications/projections.
package application

import (
	"encoding/json"
	"testing"
)

// approvalDecidedKeys is the complete set of properties declared by approvalDecided def in
// domain-event.schema.json (additionalProperties:false; key set changes require synchronous schema review).
var approvalDecidedKeys = []string{
	"order_id", "stage_id", "step_id", "reviewer_user_id", "decision", "aggregate_version",
}

func TestApprovalDecidedPayloadMatchesSchema(t *testing.T) {
	raw, err := json.Marshal(ApprovalDecidedPayload{
		OrderID:          "0b9d8e42-6d1f-4c1e-9b7a-3f2c8d5e1a01",
		StageID:          "0b9d8e42-6d1f-4c1e-9b7a-3f2c8d5e1a02",
		StepID:           "0b9d8e42-6d1f-4c1e-9b7a-3f2c8d5e1a03",
		ReviewerUserID:   "0b9d8e42-6d1f-4c1e-9b7a-3f2c8d5e1a04",
		Decision:         "approve",
		AggregateVersion: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, raw)

	// Zero-value payload must likewise not leak residual omitempty keys such as comment.
	rawEmpty, err := json.Marshal(ApprovalDecidedPayload{Decision: "reject", AggregateVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, rawEmpty)
}

func assertExactKeys(t *testing.T, raw []byte) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != len(approvalDecidedKeys) {
		t.Fatalf("payload keys %v want exactly %v", m, approvalDecidedKeys)
	}
	for _, k := range approvalDecidedKeys {
		if _, ok := m[k]; !ok {
			t.Fatalf("payload missing required key %q: %s", k, raw)
		}
	}
	if _, ok := m["comment"]; ok {
		t.Fatalf("comment must not appear in approval_decided payload: %s", raw)
	}
}
