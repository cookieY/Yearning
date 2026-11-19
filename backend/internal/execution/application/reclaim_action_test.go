// reclaim_action_test.go SEC-0001 table-driven unit tests for the pure reclaim adjudication function:
// any reclaim with a send boundary other than not_started becomes result_unknown (never replayed
// from ordinal 1); cancelling with provably zero sends converges to a cancelled terminal state; all others claim normally.
package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/execution/domain"
)

func TestAdjudicateReclaim(t *testing.T) {
	cases := []struct {
		name     string
		state    domain.AttemptState
		boundary domain.SendBoundary
		want     reclaimAction
	}{
		{"running_zero_send_normal_claim", domain.AttemptRunning, domain.BoundaryNotStarted, reclaimExecute},
		{"created_zero_send_normal_claim", domain.AttemptCreated, domain.BoundaryNotStarted, reclaimExecute},
		{"preflight_zero_send_normal_claim", domain.AttemptPreflight, domain.BoundaryNotStarted, reclaimExecute},
		{"died_after_socket_write_sending", domain.AttemptRunning, domain.BoundarySending, reclaimResultUnknown},
		{"response_observed_sent", domain.AttemptRunning, domain.BoundarySent, reclaimResultUnknown},
		{"cancelling_and_crossed_boundary", domain.AttemptCancelling, domain.BoundarySending, reclaimResultUnknown},
		{"cancelling_and_sent", domain.AttemptCancelling, domain.BoundarySent, reclaimResultUnknown},
		{"cancelling_zero_send_converges_as_cancelled", domain.AttemptCancelling, domain.BoundaryNotStarted, reclaimCancelFinalize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adjudicateReclaim(tc.state, tc.boundary); got != tc.want {
				t.Fatalf("adjudicateReclaim(%s, %s) = %d want %d", tc.state, tc.boundary, got, tc.want)
			}
		})
	}
	// Exhaustive invariant (fence_token_never_regresses/no_automatic_replay):
	// no combination of non-terminal state x non-not_started boundary may ever enter the execute path.
	for _, st := range domain.AllAttemptStates() {
		if st.Terminal() {
			continue // Terminal states return early in runAttempt and do not enter adjudication
		}
		for _, b := range domain.AllSendBoundaries() {
			if b != domain.BoundaryNotStarted && adjudicateReclaim(st, b) == reclaimExecute {
				t.Fatalf("invariant violated: (%s, %s) must never enter execute path", st, b)
			}
		}
	}
}
