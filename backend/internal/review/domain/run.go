// run.go defines the review_runs.state lifecycle (schema 000002 + ai-review-runtime-contract).
// Terminal Run states are immutable once reached, except invalidation to outdated; submitted
// reviews are never recomputed (R003).
package domain

// RunState enumerates the persisted lifecycle states of review_runs.state.
type RunState string

const (
	RunStateQueued   RunState = "queued"
	RunStateRunning  RunState = "running"
	RunStateReady    RunState = "ready"
	RunStateBlocked  RunState = "blocked"
	RunStatePartial  RunState = "partial"
	RunStateFailed   RunState = "failed"
	RunStateOutdated RunState = "outdated"
)

// ValidRunState reports whether s is a declared RunState constant.
func ValidRunState(s RunState) bool {
	switch s {
	case RunStateQueued, RunStateRunning, RunStateReady,
		RunStateBlocked, RunStatePartial, RunStateFailed, RunStateOutdated:
		return true
	}
	return false
}

// RunTerminal reports whether s is a terminal Run state.
func RunTerminal(s RunState) bool {
	switch s {
	case RunStateReady, RunStateBlocked, RunStatePartial, RunStateFailed, RunStateOutdated:
		return true
	}
	return false
}

// RunTrigger enumerates lifecycle events acting on a Review Run.
type RunTrigger string

const (
	RunEventStarted       RunTrigger = "started"
	RunEventCompleted     RunTrigger = "completed"
	RunEventRiskBlocked   RunTrigger = "risk_blocked"
	RunEventIncomplete    RunTrigger = "incomplete"
	RunEventFailed        RunTrigger = "failed"
	RunEventInputsChanged RunTrigger = "inputs_changed"
)

var runEdges = map[RunState]map[RunTrigger]RunState{
	RunStateQueued: {
		RunEventStarted:       RunStateRunning,
		RunEventFailed:        RunStateFailed,
		RunEventInputsChanged: RunStateOutdated,
	},
	RunStateRunning: {
		RunEventCompleted:     RunStateReady,
		RunEventRiskBlocked:   RunStateBlocked,
		RunEventIncomplete:    RunStatePartial,
		RunEventFailed:        RunStateFailed,
		RunEventInputsChanged: RunStateOutdated,
	},
	// Completed Run outcomes remain valid until upstream SQL or flow configuration changes.
	RunStateReady: {
		RunEventInputsChanged: RunStateOutdated,
	},
	RunStateBlocked: {
		RunEventInputsChanged: RunStateOutdated,
	},
	RunStatePartial: {
		RunEventInputsChanged: RunStateOutdated,
	},
	RunStateFailed: {
		RunEventInputsChanged: RunStateOutdated,
	},
}

// RunTransition computes the next RunState; outdated rejects all triggers as an absolute terminal state.
func RunTransition(s RunState, trigger RunTrigger) (RunState, error) {
	if s == RunStateOutdated {
		return s, ErrTerminalState
	}
	edges, ok := runEdges[s]
	if !ok {
		return s, ErrIllegalTransition
	}
	next, ok := edges[trigger]
	if !ok {
		if RunTerminal(s) {
			return s, ErrTerminalState
		}
		return s, ErrIllegalTransition
	}
	return next, nil
}

// RunOutcomeFailedStates returns non-ready terminal states indicating incomplete, failed, or blocked Runs.
func RunOutcomeFailedStates() []RunState {
	return []RunState{RunStateFailed, RunStatePartial, RunStateBlocked}
}

// StageReviewState enumerates persisted execution states for per-stage review outcomes.
type StageReviewState string

const (
	StageStatePending StageReviewState = "pending"
	StageStateRunning StageReviewState = "running"
	StageStatePassed  StageReviewState = "passed"
	StageStateBlocked StageReviewState = "blocked"
	StageStatePartial StageReviewState = "partial"
	StageStateFailed  StageReviewState = "failed"
)

// ValidStageReviewState reports whether s is a declared StageReviewState constant.
func ValidStageReviewState(s StageReviewState) bool {
	switch s {
	case StageStatePending, StageStateRunning, StageStatePassed,
		StageStateBlocked, StageStatePartial, StageStateFailed:
		return true
	}
	return false
}

// StageTerminal reports whether s is a terminal stage review state.
func StageTerminal(s StageReviewState) bool {
	switch s {
	case StageStatePassed, StageStateBlocked, StageStatePartial, StageStateFailed:
		return true
	}
	return false
}

// Severity ranks review finding severity from none through critical.
type Severity string

const (
	SeverityNone     Severity = "none"
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// SeverityRank converts a Severity into an integer ordering value (-1 if unknown).
func SeverityRank(s Severity) int {
	switch s {
	case SeverityNone, SeverityInfo:
		return 0
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	}
	return -1
}

// MaxSeverity returns the higher severity between a and b.
func MaxSeverity(a, b Severity) Severity {
	if SeverityRank(a) >= SeverityRank(b) {
		return a
	}
	return b
}

// BlockSeverityThreshold marks the minimum severity level that blocks submission.
const BlockSeverityThreshold = SeverityHigh
