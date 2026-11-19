// Package domain defines the pure domain model for the ChangeOrder domain: work order and stage
// state machines, approval semantics, and business errors (state-machines.json: change_order).
// All transition functions are side-effect-free; persistence adapters apply their results within
// the same transaction using aggregate version CAS (state-machine-test-policy implementation_boundary).
package domain

import "errors"

// OrderState is the controlled enum for change_orders.state
// (state-machines.json: change_order.states, 18 states).
type OrderState string

const (
	StateSubmitted            OrderState = "submitted"
	StateStageApproval        OrderState = "stage_approval_active"
	StateStageExecution       OrderState = "stage_execution_pending"
	StateScheduled            OrderState = "scheduled"
	StateRunning              OrderState = "running"
	StateCompleted            OrderState = "completed"
	StateRejected             OrderState = "rejected"
	StateWithdrawn            OrderState = "withdrawn"
	StateWithdrawnPartial     OrderState = "withdrawn_after_partial_execution"
	StateVoided               OrderState = "voided"
	StateFailed               OrderState = "failed"
	StatePartialFailed        OrderState = "partial_failed"
	StateCancelled            OrderState = "cancelled"
	StatePartialCancelled     OrderState = "partial_cancelled"
	StateResultUnknown        OrderState = "result_unknown"
	StateBlockedDSUnavailable OrderState = "blocked_datasource_unavailable"
	StateMissedSchedule       OrderState = "missed_schedule"
	StateInvalid              OrderState = "invalid"
)

// ValidOrderState reports whether s is a contract-defined state; unknown persisted states
// must be exposed as PERSISTENCE_ENUM_UNKNOWN (P1 transport error) and must never be mapped to a default state.
func ValidOrderState(s OrderState) bool {
	switch s {
	case StateSubmitted, StateStageApproval, StateStageExecution, StateScheduled,
		StateRunning, StateCompleted, StateRejected, StateWithdrawn, StateWithdrawnPartial,
		StateVoided, StateFailed, StatePartialFailed, StateCancelled, StatePartialCancelled,
		StateResultUnknown, StateBlockedDSUnavailable, StateMissedSchedule, StateInvalid:
		return true
	}
	return false
}

// OrderTerminal reports whether s is a terminal state (machine terminal declaration).
// Note that invalid is non-terminal: its sole subsequent user action is void (graph_invariants).
func OrderTerminal(s OrderState) bool {
	switch s {
	case StateCompleted, StateRejected, StateWithdrawn, StateWithdrawnPartial,
		StateVoided, StateFailed, StatePartialFailed, StateCancelled,
		StatePartialCancelled, StateMissedSchedule:
		return true
	}
	return false
}

// OrderTrigger is a change_order state machine trigger (action = user command,
// event = system/worker outcome).
type OrderTrigger string

const (
	// User commands (B9 implementation: approve_final_step/reject/withdraw/void;
	// withdraw_after_partial_fact is the machine variant of withdraw on running/result_unknown
	// source states).
	ActionApproveFinalStep OrderTrigger = "approve_final_step"
	ActionReject           OrderTrigger = "reject"
	ActionExecute          OrderTrigger = "execute"
	ActionSchedule         OrderTrigger = "schedule"
	ActionCancelSchedule   OrderTrigger = "cancel_schedule"
	ActionWithdraw         OrderTrigger = "withdraw"
	ActionWithdrawPartial  OrderTrigger = "withdraw_after_partial_fact"
	ActionVoid             OrderTrigger = "void"
	// Manual verification actions (result_unknown convergence, wired in B10/B12).
	ActionVerifySucceededFinal    OrderTrigger = "verify_succeeded_final_stage"
	ActionVerifySucceededNonFinal OrderTrigger = "verify_succeeded_nonfinal_stage"
	ActionVerifyNonFinalInvalid   OrderTrigger = "verify_succeeded_nonfinal_stage_with_invalid_next_node"
	ActionVerifyFailed            OrderTrigger = "verify_failed"
	ActionVerifyPartial           OrderTrigger = "verify_partial"
	ActionVerifyStillUnknown      OrderTrigger = "verify_still_unknown"
	// System events.
	EventFirstStageActivated          OrderTrigger = "first_stage_activated"
	EventScheduleDue                  OrderTrigger = "schedule_due"
	EventScheduleMissed               OrderTrigger = "schedule_missed"
	EventDatasourceUnavailable        OrderTrigger = "datasource_unavailable"
	EventFinalStageSucceeded          OrderTrigger = "final_stage_succeeded"
	EventNonfinalStageSucceeded       OrderTrigger = "nonfinal_stage_succeeded"
	EventNonfinalSucceededInvalidNext OrderTrigger = "nonfinal_stage_succeeded_with_invalid_next_node"
	EventDmlFailedRolledBack          OrderTrigger = "dml_failed_rolled_back"
	EventDdlPartialFailed             OrderTrigger = "ddl_partial_failed"
	EventCancelledWithoutPartial      OrderTrigger = "cancelled_without_partial"
	EventCancelledAfterPartial        OrderTrigger = "cancelled_after_partial"
	EventResultBecameUnknown          OrderTrigger = "result_became_unknown"
	EventFrozenActorSetBecameEmpty    OrderTrigger = "required_frozen_actor_set_became_empty"
)

// orderEdges is the complete machine edge table; keyed by source+trigger
// (transcribed item-by-item from state-machines.json change_order.transitions).
var orderEdges = map[OrderState]map[OrderTrigger]OrderState{
	StateSubmitted: {
		EventFirstStageActivated:       StateStageApproval,
		ActionWithdraw:                 StateWithdrawn,
		ActionVoid:                     StateVoided,
		EventFrozenActorSetBecameEmpty: StateInvalid,
	},
	StateStageApproval: {
		ActionApproveFinalStep:         StateStageExecution,
		ActionReject:                   StateRejected,
		ActionWithdraw:                 StateWithdrawn,
		ActionVoid:                     StateVoided,
		EventFrozenActorSetBecameEmpty: StateInvalid,
	},
	StateStageExecution: {
		ActionExecute:                  StateRunning,
		ActionSchedule:                 StateScheduled,
		ActionWithdraw:                 StateWithdrawn,
		ActionVoid:                     StateVoided,
		EventFrozenActorSetBecameEmpty: StateInvalid,
	},
	StateScheduled: {
		EventScheduleDue:           StateRunning,
		EventScheduleMissed:        StateMissedSchedule,
		EventDatasourceUnavailable: StateBlockedDSUnavailable,
		// Any frozen executor cancels an unexpired schedule: returns to execution_pending (to reschedule or execute immediately);
		// mutually exclusive with due-time claim via schedule row CAS, first committer wins (owner ruling 2026-10-30).
		ActionCancelSchedule:           StateStageExecution,
		ActionWithdraw:                 StateWithdrawn,
		ActionVoid:                     StateVoided,
		EventFrozenActorSetBecameEmpty: StateInvalid,
	},
	StateRunning: {
		EventFinalStageSucceeded:          StateCompleted,
		EventNonfinalStageSucceeded:       StateStageApproval,
		EventNonfinalSucceededInvalidNext: StateInvalid,
		EventDmlFailedRolledBack:          StateFailed,
		EventDdlPartialFailed:             StatePartialFailed,
		EventCancelledWithoutPartial:      StateCancelled,
		EventCancelledAfterPartial:        StatePartialCancelled,
		EventResultBecameUnknown:          StateResultUnknown,
		ActionWithdrawPartial:             StateWithdrawnPartial,
	},
	StateResultUnknown: {
		ActionVerifySucceededFinal:    StateCompleted,
		ActionVerifySucceededNonFinal: StateStageApproval,
		ActionVerifyNonFinalInvalid:   StateInvalid,
		ActionVerifyFailed:            StateFailed,
		ActionVerifyPartial:           StatePartialFailed,
		ActionVerifyStillUnknown:      StateResultUnknown,
		ActionWithdrawPartial:         StateWithdrawnPartial,
		ActionVoid:                    StateVoided,
	},
	StateBlockedDSUnavailable: {
		ActionWithdraw:                 StateWithdrawn,
		ActionVoid:                     StateVoided,
		EventFrozenActorSetBecameEmpty: StateInvalid,
	},
	StateInvalid: {
		ActionVoid: StateVoided,
	},
}

// ErrIllegalTransition undeclared (state, trigger) combination (1010).
var ErrIllegalTransition = errors.New("changeorder: illegal state transition")

// ErrTerminalState any trigger on a terminal state (1010).
var ErrTerminalState = errors.New("changeorder: terminal state")

// OrderTransition returns the target state for trigger on s.
func OrderTransition(s OrderState, trigger OrderTrigger) (OrderState, error) {
	if OrderTerminal(s) {
		return s, ErrTerminalState
	}
	edges, ok := orderEdges[s]
	if !ok {
		return s, ErrIllegalTransition
	}
	next, ok := edges[trigger]
	if !ok {
		return s, ErrIllegalTransition
	}
	return next, nil
}

// OrderTriggerVocabulary returns the complete machine trigger vocabulary
// (used by illegal-transition tests expanding all state x vocabulary pairs).
func OrderTriggerVocabulary() []OrderTrigger {
	seen := map[OrderTrigger]bool{}
	var out []OrderTrigger
	for _, edges := range orderEdges {
		for t := range edges {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// OrderAllStates returns all states (non-terminal states first for convenient test iteration).
func OrderAllStates() []OrderState {
	return []OrderState{
		StateSubmitted, StateStageApproval, StateStageExecution, StateScheduled,
		StateRunning, StateResultUnknown, StateBlockedDSUnavailable, StateInvalid,
		StateCompleted, StateRejected, StateWithdrawn, StateWithdrawnPartial,
		StateVoided, StateFailed, StatePartialFailed, StateCancelled,
		StatePartialCancelled, StateMissedSchedule,
	}
}

// WithdrawSources is the set of valid source states for withdraw
// (the no_execution_send_boundary_crossed precondition of withdraw_or_void_change_order
// is evaluated from execution facts at the command layer).
func WithdrawSources() []OrderState {
	return []OrderState{StateSubmitted, StateStageApproval, StateStageExecution, StateScheduled, StateBlockedDSUnavailable}
}

// VoidSources is the set of valid source states for void.
func VoidSources() []OrderState {
	return []OrderState{StateSubmitted, StateStageApproval, StateStageExecution, StateScheduled, StateBlockedDSUnavailable, StateResultUnknown, StateInvalid}
}

// ---- Stage State (order_stages.state; serial progression is backed by
// the order_one_live_stage partial unique index) ----

// StageState is the controlled enum for order_stages.state.
type StageState string

const (
	StagePending       StageState = "pending"
	StageApproval      StageState = "approval_active"
	StageExecution     StageState = "execution_pending"
	StageScheduled     StageState = "scheduled"
	StageRunning       StageState = "running"
	StageSucceeded     StageState = "succeeded"
	StageFailed        StageState = "failed"
	StagePartialFailed StageState = "partial_failed"
	StageCancelled     StageState = "cancelled"
	StagePartialCancel StageState = "partial_cancelled"
	StageResultUnknown StageState = "result_unknown"
	StageSkipped       StageState = "skipped"
)

// ValidStageState reports whether s is a contract-defined stage state.
func ValidStageState(s StageState) bool {
	switch s {
	case StagePending, StageApproval, StageExecution, StageScheduled, StageRunning,
		StageSucceeded, StageFailed, StagePartialFailed, StageCancelled,
		StagePartialCancel, StageResultUnknown, StageSkipped:
		return true
	}
	return false
}

// StageLive reports whether the stage is in a live state
// (predicate of the order_one_live_stage partial unique index).
func StageLive(s StageState) bool {
	switch s {
	case StageApproval, StageExecution, StageScheduled, StageRunning, StageResultUnknown:
		return true
	}
	return false
}

// ---- Approval Step State (order_approval_steps.state) ----

// StepState is the controlled enum for order_approval_steps.state.
type StepState string

const (
	StepPending  StepState = "pending"
	StepActive   StepState = "active"
	StepApproved StepState = "approved"
	StepRejected StepState = "rejected"
	StepSkipped  StepState = "skipped"
	StepInvalid  StepState = "invalid"
)

// ValidStepState reports whether s is a contract-defined step state.
func ValidStepState(s StepState) bool {
	switch s {
	case StepPending, StepActive, StepApproved, StepRejected, StepSkipped, StepInvalid:
		return true
	}
	return false
}
