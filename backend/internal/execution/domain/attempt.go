// attempt.go Controlled state machine for the Execution domain (Go-side authority for
// db/migrations/000002 CHECK constraints and the execution-failure-injection contract).
// An Attempt is a durable execution instance: Fence Token is monotonically increasing,
// send_boundary never regresses, and terminal states can never be resurrected.
package domain

import "errors"

// AttemptState is the controlled enum for execution_attempts.state.
type AttemptState string

const (
	AttemptCreated       AttemptState = "created"
	AttemptPreflight     AttemptState = "preflight"
	AttemptRunning       AttemptState = "running"
	AttemptCancelling    AttemptState = "cancelling"
	AttemptSucceeded     AttemptState = "succeeded"
	AttemptFailed        AttemptState = "failed"
	AttemptPartialFailed AttemptState = "partial_failed"
	AttemptCancelled     AttemptState = "cancelled"
	AttemptPartialCancel AttemptState = "partial_cancelled"
	AttemptResultUnknown AttemptState = "result_unknown"
)

// Terminal reports whether s is a terminal Attempt state.
func (s AttemptState) Terminal() bool {
	switch s {
	case AttemptSucceeded, AttemptFailed, AttemptPartialFailed,
		AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown:
		return true
	}
	return false
}

// Live reports whether s is active (criterion of the execution_stage_one_active_attempt index).
func (s AttemptState) Live() bool {
	switch s {
	case AttemptCreated, AttemptPreflight, AttemptRunning,
		AttemptCancelling, AttemptResultUnknown:
		return true
	}
	return false
}

// attemptEdges defines valid state transitions (created -> preflight -> running -> ...;
// result_unknown can only be advanced by the manual verification command and is never automatically rewritten).
var attemptEdges = map[AttemptState][]AttemptState{
	AttemptCreated:   {AttemptPreflight, AttemptFailed, AttemptCancelled},
	AttemptPreflight: {AttemptRunning, AttemptFailed, AttemptCancelled},
	AttemptRunning: {AttemptCancelling, AttemptSucceeded, AttemptFailed,
		AttemptPartialFailed, AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown},
	AttemptCancelling: {AttemptCancelled, AttemptPartialCancel, AttemptResultUnknown},
}

// CanTransition reports whether transitioning from -> to is valid.
func CanTransition(from, to AttemptState) bool {
	if from.Terminal() {
		return false
	}
	for _, t := range attemptEdges[from] {
		if t == to {
			return true
		}
	}
	return false
}

// AllAttemptStates returns all states (used for all-pairs expansion in illegal-transition tests).
func AllAttemptStates() []AttemptState {
	return []AttemptState{
		AttemptCreated, AttemptPreflight, AttemptRunning, AttemptCancelling,
		AttemptSucceeded, AttemptFailed, AttemptPartialFailed, AttemptCancelled,
		AttemptPartialCancel, AttemptResultUnknown,
	}
}

// SendBoundary is the controlled enum for execution_attempts.send_boundary.
// ADR-0003: the boundary only proves "whether any SQL bytes may have been handed to the target database" and never regresses.
type SendBoundary string

const (
	BoundaryNotStarted SendBoundary = "not_started"
	BoundarySending    SendBoundary = "sending"
	BoundarySent       SendBoundary = "sent"
)

// AtLeast reports whether b has reached at least the lower bound (not_started < sending < sent).
func (b SendBoundary) AtLeast(min SendBoundary) bool {
	order := map[SendBoundary]int{BoundaryNotStarted: 0, BoundarySending: 1, BoundarySent: 2}
	return order[b] >= order[min]
}

// Advance returns the non-regressing boundary value.
func (b SendBoundary) Advance(to SendBoundary) SendBoundary {
	if to.AtLeast(b) {
		return to
	}
	return b
}

// AllSendBoundaries returns the full set of send boundaries.
func AllSendBoundaries() []SendBoundary {
	return []SendBoundary{BoundaryNotStarted, BoundarySending, BoundarySent}
}

// StatementState is the controlled enum for execution_statements.state.
type StatementState string

const (
	StmtNotStarted StatementState = "not_started"
	StmtSent       StatementState = "sent"
	StmtSucceeded  StatementState = "succeeded"
	StmtFailed     StatementState = "failed"
	StmtCancelled  StatementState = "cancelled"
	StmtUnknown    StatementState = "unknown"
	StmtSkipped    StatementState = "skipped"
)

// StatementTerminal reports whether the statement is in a terminal state.
func (s StatementState) StatementTerminal() bool {
	switch s {
	case StmtSucceeded, StmtFailed, StmtCancelled, StmtUnknown, StmtSkipped:
		return true
	}
	return false
}

// ExecutionKind is the controlled enum for execution_attempts.execution_kind.
type ExecutionKind string

const (
	KindDDL   ExecutionKind = "ddl"
	KindDML   ExecutionKind = "dml"
	KindGhOst ExecutionKind = "gh_ost"
	KindPtOsc ExecutionKind = "pt_osc"
)

// IsOscKind reports whether k is an OSC tool execution (where the osc_* column group applies).
func (k ExecutionKind) IsOscKind() bool {
	return k == KindGhOst || k == KindPtOsc
}

// VerificationResult is the controlled enum for execution_verifications.result
// (sole valid conclusion set for resolve_unknown_execution).
type VerificationResult string

const (
	VerifiedSucceeded VerificationResult = "confirmed_succeeded"
	VerifiedFailed    VerificationResult = "confirmed_failed"
	VerifiedPartial   VerificationResult = "confirmed_partial"
	StillUnknown      VerificationResult = "still_unknown"
)

// ValidVerificationResult reports whether r is a contract-defined verification result.
func ValidVerificationResult(r VerificationResult) bool {
	switch r {
	case VerifiedSucceeded, VerifiedFailed, VerifiedPartial, StillUnknown:
		return true
	}
	return false
}

// ErrNotFrozenActor 3001: not a frozen executor of the current stage (admins are not exempt).
var ErrNotFrozenActor = errors.New("execution: not frozen executor")
