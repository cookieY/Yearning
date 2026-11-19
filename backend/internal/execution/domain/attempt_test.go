// attempt_test.go Execution domain state machine unit tests: full enumeration of legal transitions,
// terminal state closure, monotonic non-regressing send boundary, and verification result vocabulary.
package domain

import "testing"

func TestAttemptTerminalStates(t *testing.T) {
	terminal := map[AttemptState]bool{
		AttemptSucceeded: true, AttemptFailed: true, AttemptPartialFailed: true,
		AttemptCancelled: true, AttemptPartialCancel: true, AttemptResultUnknown: true,
	}
	for _, s := range AllAttemptStates() {
		if s.Terminal() != terminal[s] {
			t.Fatalf("state %s terminal=%v want %v", s, s.Terminal(), terminal[s])
		}
	}
}

func TestAttemptLiveStates(t *testing.T) {
	live := map[AttemptState]bool{
		AttemptCreated: true, AttemptPreflight: true, AttemptRunning: true,
		AttemptCancelling: true, AttemptResultUnknown: true,
	}
	for _, s := range AllAttemptStates() {
		if s.Live() != live[s] {
			t.Fatalf("state %s live=%v want %v", s, s.Live(), live[s])
		}
	}
}

func TestAttemptLegalTransitions(t *testing.T) {
	legal := map[AttemptState][]AttemptState{
		AttemptCreated:    {AttemptPreflight, AttemptFailed, AttemptCancelled},
		AttemptPreflight:  {AttemptRunning, AttemptFailed, AttemptCancelled},
		AttemptRunning:    {AttemptCancelling, AttemptSucceeded, AttemptFailed, AttemptPartialFailed, AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown},
		AttemptCancelling: {AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown},
	}
	seen := 0
	for from, targets := range legal {
		for _, to := range targets {
			if !CanTransition(from, to) {
				t.Fatalf("expected legal %s -> %s", from, to)
			}
			seen++
		}
	}
	if seen != 16 {
		t.Fatalf("legal transition count = %d want 16", seen)
	}
}

// legalPairs expands all legal (from, to) pairs from the edge table.
func legalPairs() map[[2]AttemptState]bool {
	m := map[[2]AttemptState]bool{}
	for from, targets := range map[AttemptState][]AttemptState{
		AttemptCreated:    {AttemptPreflight, AttemptFailed, AttemptCancelled},
		AttemptPreflight:  {AttemptRunning, AttemptFailed, AttemptCancelled},
		AttemptRunning:    {AttemptCancelling, AttemptSucceeded, AttemptFailed, AttemptPartialFailed, AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown},
		AttemptCancelling: {AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown},
	} {
		for _, to := range targets {
			m[[2]AttemptState{from, to}] = true
		}
	}
	return m
}

func TestAttemptExactPairMatrix(t *testing.T) {
	legal := legalPairs()
	for _, from := range AllAttemptStates() {
		for _, to := range AllAttemptStates() {
			got := CanTransition(from, to)
			want := legal[[2]AttemptState{from, to}]
			if got != want {
				t.Fatalf("transition %s -> %s = %v want %v", from, to, got, want)
			}
		}
	}
}

func TestTerminalNeverTransitions(t *testing.T) {
	for _, from := range AllAttemptStates() {
		if !from.Terminal() {
			continue
		}
		for _, to := range AllAttemptStates() {
			if CanTransition(from, to) {
				t.Fatalf("terminal %s must not transition to %s", from, to)
			}
		}
	}
}

func TestSendBoundaryMonotone(t *testing.T) {
	if BoundaryNotStarted.AtLeast(BoundarySending) {
		t.Fatal("not_started must not be >= sending")
	}
	if !BoundarySent.AtLeast(BoundarySending) {
		t.Fatal("sent must be >= sending")
	}
	if BoundarySending.Advance(BoundaryNotStarted) != BoundarySending {
		t.Fatal("boundary must never regress")
	}
	if BoundaryNotStarted.Advance(BoundarySent) != BoundarySent {
		t.Fatal("boundary must advance to sent")
	}
}

func TestVerificationVocabulary(t *testing.T) {
	for _, r := range []VerificationResult{VerifiedSucceeded, VerifiedFailed, VerifiedPartial, StillUnknown} {
		if !ValidVerificationResult(r) {
			t.Fatalf("expected valid %s", r)
		}
	}
	for _, r := range []VerificationResult{"confirmed", "", "CONFIRMED_SUCCEEDED"} {
		if ValidVerificationResult(r) {
			t.Fatalf("expected invalid %q", r)
		}
	}
}

func TestSendBoundaryVocabulary(t *testing.T) {
	got := AllSendBoundaries()
	if len(got) != 3 {
		t.Fatalf("boundaries=%v want 3", got)
	}
}

func TestStatementTerminalStates(t *testing.T) {
	terminal := map[StatementState]bool{
		StmtSucceeded: true, StmtFailed: true, StmtCancelled: true,
		StmtUnknown: true, StmtSkipped: true,
	}
	for _, s := range []StatementState{StmtNotStarted, StmtSent, StmtSucceeded, StmtFailed,
		StmtCancelled, StmtUnknown, StmtSkipped} {
		if s.StatementTerminal() != terminal[s] {
			t.Fatalf("statement %s terminal=%v", s, s.StatementTerminal())
		}
	}
}

func TestOscKindSet(t *testing.T) {
	if !KindPtOsc.IsOscKind() || !KindGhOst.IsOscKind() {
		t.Fatal("osc kinds must be recognized")
	}
	if KindDDL.IsOscKind() || KindDML.IsOscKind() {
		t.Fatal("non-osc kinds must be excluded")
	}
}
