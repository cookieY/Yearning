// Package domain defines the pure domain model for the Review bounded context: Draft/Run state
// machines, SubmissionGate evaluation, domain events, and error types without persistence side effects.
package domain

import "errors"

// DraftState enumerates the persisted lifecycle states of change_drafts.state.
type DraftState string

const (
	DraftStateDraft     DraftState = "draft"
	DraftStateReviewing DraftState = "reviewing"
	DraftStateReady     DraftState = "ready"
	DraftStateBlocked   DraftState = "blocked"
	DraftStatePartial   DraftState = "partial"
	DraftStateFailed    DraftState = "failed"
	DraftStateOutdated  DraftState = "outdated"
	DraftStateSubmitted DraftState = "submitted"
)

// ValidDraftState reports whether s is a declared DraftState constant.
func ValidDraftState(s DraftState) bool {
	switch s {
	case DraftStateDraft, DraftStateReviewing, DraftStateReady,
		DraftStateBlocked, DraftStatePartial, DraftStateFailed,
		DraftStateOutdated, DraftStateSubmitted:
		return true
	}
	return false
}

// DraftTrigger enumerates user actions and asynchronous review events acting on a Draft.
type DraftTrigger string

const (
	// User-initiated triggers.
	DraftActionRunReview DraftTrigger = "run_review"
	DraftActionSubmit    DraftTrigger = "submit"

	// System-driven review lifecycle events.
	DraftEventReviewPassed     DraftTrigger = "review_passed"
	DraftEventRiskGateBlocked  DraftTrigger = "risk_gate_blocked"
	DraftEventReviewIncomplete DraftTrigger = "review_incomplete"
	DraftEventReviewFailed     DraftTrigger = "review_failed"
	DraftEventInputsChanged    DraftTrigger = "review_inputs_changed"
)

// Legal state transition matrix for change_drafts.state.
var draftEdges = map[DraftState]map[DraftTrigger]DraftState{
	DraftStateDraft: {
		DraftActionRunReview: DraftStateReviewing,
		// Draft submission allows direct promotion only when no blocking flow gate applies.
		DraftActionSubmit: DraftStateSubmitted,
	},
	DraftStateOutdated: {
		DraftActionRunReview: DraftStateReviewing,
	},
	DraftStateFailed: {
		DraftActionRunReview: DraftStateReviewing,
	},
	DraftStatePartial: {
		DraftActionRunReview: DraftStateReviewing,
	},
	DraftStateBlocked: {
		DraftActionRunReview: DraftStateReviewing,
	},
	DraftStateReviewing: {
		DraftEventReviewPassed:     DraftStateReady,
		DraftEventRiskGateBlocked:  DraftStateBlocked,
		DraftEventReviewIncomplete: DraftStatePartial,
		DraftEventReviewFailed:     DraftStateFailed,
	},
	DraftStateReady: {
		// Re-running review, submitting the ready draft, or invalidating inputs when flow config drifts.
		DraftActionRunReview:    DraftStateReviewing,
		DraftActionSubmit:       DraftStateSubmitted,
		DraftEventInputsChanged: DraftStateOutdated,
	},
}

// ErrIllegalTransition indicates an undeclared (state, trigger) transition edge.
var ErrIllegalTransition = errors.New("review: illegal state transition")

// ErrTerminalState indicates an attempted transition out of a terminal Draft or Run state.
var ErrTerminalState = errors.New("review: terminal state")

// DraftTerminal reports whether s is the terminal submitted state.
func DraftTerminal(s DraftState) bool { return s == DraftStateSubmitted }

// DraftTransition computes the next DraftState or returns ErrTerminalState / ErrIllegalTransition.
func DraftTransition(s DraftState, trigger DraftTrigger) (DraftState, error) {
	if DraftTerminal(s) {
		return s, ErrTerminalState
	}
	edges, ok := draftEdges[s]
	if !ok {
		return s, ErrIllegalTransition
	}
	next, ok := edges[trigger]
	if !ok {
		return s, ErrIllegalTransition
	}
	return next, nil
}

// DraftRunReviewSources returns every DraftState eligible to start a new Review Run.
func DraftRunReviewSources() []DraftState {
	return []DraftState{DraftStateDraft, DraftStateOutdated, DraftStateReady, DraftStateFailed, DraftStatePartial, DraftStateBlocked}
}

// DraftEditable reports whether SQL or target metadata can be edited while in state s.
func DraftEditable(s DraftState) bool {
	switch s {
	case DraftStateDraft, DraftStateOutdated, DraftStateFailed,
		DraftStatePartial, DraftStateBlocked, DraftStateReady:
		return true
	}
	return false
}

// DraftActiveReview reports whether a draft currently has an active review in progress.
func DraftActiveReview(s DraftState) bool {
	return s == DraftStateReviewing
}
