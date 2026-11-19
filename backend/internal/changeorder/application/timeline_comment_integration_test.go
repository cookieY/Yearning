//go:build integration

// timeline_comment_integration_test.go Integration test for approval comment projection on timeline
// (NEW-FE-01: SEC-0036 fix regressed BUG-004 — after moving comment out of approval_decided event
// payloads, timeline projection still read data["comment"] from the event payload, making approval
// comments/rejection reasons invisible to the submitter). Pins the fix semantics: comments are
// batch-projected into timeline entries from approval_decisions rows by step_id, reusing the CanRead
// gate at ListTimeline entry (rejection requires a comment; approval with a comment also exposes it;
// event rows without a comment remain empty).
//
// Requires a real PostgreSQL instance (YEARNING_TEST_DATABASE_*; reuses the fixture in
// submit_self_actor_integration_test.go, mutually excluded via the shared DB file lock under the same package and tag).
// Compiles when local DB is unavailable and runs in CI.
package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// decideApproval makes a decision on the currently active step as the specified approver.
func decideApproval(t *testing.T, fx *subFx, approver, orderID string, expectVersion int64, decision, comment string) (*OrderView, error) {
	t.Helper()
	view, _, err := fx.svc.Decide(context.Background(), Actor{UserID: approver, Username: "approver"},
		RequestMeta{}, DecideInput{
			OrderID: orderID, ExpectVersion: expectVersion,
			Decision: decision, Comment: comment,
			IdempotencyKey: "tl-" + uuid.NewString()[:8],
		})
	return view, err
}

// timelineDecidedEntry retrieves the approval_decided entry from the timeline (fatals if absent).
func timelineDecidedEntry(t *testing.T, fx *subFx, viewer, orderID string) *TimelineEntryView {
	t.Helper()
	entries, _, err := fx.svc.ListTimeline(context.Background(),
		Actor{UserID: viewer, Username: "viewer"}, orderID, 50, "")
	if err != nil {
		t.Fatalf("list timeline: %v", err)
	}
	for _, e := range entries {
		if e.EventType == EventApprovalDecided {
			return e
		}
	}
	t.Fatalf("timeline has no %s entry", EventApprovalDecided)
	return nil
}

// TestTimelineProjectsRejectedDecisionComment After a rejection decision, the timeline entry
// from the submitter's perspective carries the approval comment (BUG-004 read surface); non-decision events (submitted) remain empty.
func TestTimelineProjectsRejectedDecisionComment(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "tl-sub")
	approver := fx.seedUser(t, "tl-app")
	executor := fx.seedUser(t, "tl-exec")
	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{approver}}, Executors: []string{executor}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)
	view, err := fx.submitAs(t, draftID, submitter, "tl-sub")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	const reason = "Rejection reason: unacceptable statement risk"
	if _, err := decideApproval(t, fx, approver, view.ID, view.Version, "reject", reason); err != nil {
		t.Fatalf("decide reject: %v", err)
	}
	// Read from the submitter's perspective (BUG-004 visibility subject; CanRead relation check applies).
	entry := timelineDecidedEntry(t, fx, submitter, view.ID)
	if entry.Comment == nil || *entry.Comment != reason {
		t.Fatalf("approval_decided comment=%v want %q", entry.Comment, reason)
	}
	// Event rows without a comment remain empty: the submitted entry must not expose a comment.
	entries, _, err := fx.svc.ListTimeline(context.Background(),
		Actor{UserID: submitter, Username: "tl-sub"}, view.ID, 50, "")
	if err != nil {
		t.Fatalf("list timeline: %v", err)
	}
	for _, e := range entries {
		if e.EventType == EventOrderSubmitted && e.Comment != nil {
			t.Fatalf("submitted entry must not carry comment, got %q", *e.Comment)
		}
	}
}

// TestTimelineProjectsApprovedDecisionComment An approved decision with a comment also projects
// the comment (final step approved -> order transitions to stage_execution_pending).
func TestTimelineProjectsApprovedDecisionComment(t *testing.T) {
	fx := newSubFx(t)
	submitter := fx.seedUser(t, "tl-sub2")
	approver := fx.seedUser(t, "tl-app2")
	executor := fx.seedUser(t, "tl-exec2")
	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{approver}}, Executors: []string{executor}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)
	view, err := fx.submitAs(t, draftID, submitter, "tl-sub2")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	const note = "Approved, remember to back up first"
	got, err := decideApproval(t, fx, approver, view.ID, view.Version, "approve", note)
	if err != nil {
		t.Fatalf("decide approve: %v", err)
	}
	if got.State != "stage_execution_pending" {
		t.Fatalf("order state=%s want stage_execution_pending", got.State)
	}
	entry := timelineDecidedEntry(t, fx, submitter, view.ID)
	if entry.Comment == nil || *entry.Comment != note {
		t.Fatalf("approval_decided comment=%v want %q", entry.Comment, note)
	}
}
