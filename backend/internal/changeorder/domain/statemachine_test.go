// statemachine_test.go Full-coverage pure unit tests for the change_order state machine
// (state-machine-test-policy): 100% transcription of all 42 expanded contract edges; 100%
// enumeration of illegal (state, trigger) pairs; terminal states reject all triggers; graph
// invariants (invalid is non-terminal and its sole user action is void).
package domain

import "testing"

// TestOrderMachineLegalEdges expands all 30 declared edges from state-machines.json.
func TestOrderMachineLegalEdges(t *testing.T) {
	type edge struct {
		from OrderState
		trig OrderTrigger
		to   OrderState
	}
	edges := []edge{
		{StateSubmitted, EventFirstStageActivated, StateStageApproval},
		{StateStageApproval, ActionApproveFinalStep, StateStageExecution},
		{StateStageApproval, ActionReject, StateRejected},
		{StateStageExecution, ActionExecute, StateRunning},
		{StateStageExecution, ActionSchedule, StateScheduled},
		{StateScheduled, EventScheduleDue, StateRunning},
		{StateScheduled, EventScheduleMissed, StateMissedSchedule},
		{StateScheduled, EventDatasourceUnavailable, StateBlockedDSUnavailable},
		{StateScheduled, ActionCancelSchedule, StateStageExecution},
		{StateRunning, EventFinalStageSucceeded, StateCompleted},
		{StateRunning, EventNonfinalStageSucceeded, StateStageApproval},
		{StateRunning, EventNonfinalSucceededInvalidNext, StateInvalid},
		{StateRunning, EventDmlFailedRolledBack, StateFailed},
		{StateRunning, EventDdlPartialFailed, StatePartialFailed},
		{StateRunning, EventCancelledWithoutPartial, StateCancelled},
		{StateRunning, EventCancelledAfterPartial, StatePartialCancelled},
		{StateRunning, EventResultBecameUnknown, StateResultUnknown},
		{StateResultUnknown, ActionVerifySucceededFinal, StateCompleted},
		{StateResultUnknown, ActionVerifySucceededNonFinal, StateStageApproval},
		{StateResultUnknown, ActionVerifyNonFinalInvalid, StateInvalid},
		{StateResultUnknown, ActionVerifyFailed, StateFailed},
		{StateResultUnknown, ActionVerifyPartial, StatePartialFailed},
		{StateResultUnknown, ActionVerifyStillUnknown, StateResultUnknown},
		{StateSubmitted, ActionWithdraw, StateWithdrawn},
		{StateStageApproval, ActionWithdraw, StateWithdrawn},
		{StateStageExecution, ActionWithdraw, StateWithdrawn},
		{StateScheduled, ActionWithdraw, StateWithdrawn},
		{StateBlockedDSUnavailable, ActionWithdraw, StateWithdrawn},
		{StateRunning, ActionWithdrawPartial, StateWithdrawnPartial},
		{StateResultUnknown, ActionWithdrawPartial, StateWithdrawnPartial},
		{StateSubmitted, EventFrozenActorSetBecameEmpty, StateInvalid},
		{StateStageApproval, EventFrozenActorSetBecameEmpty, StateInvalid},
		{StateStageExecution, EventFrozenActorSetBecameEmpty, StateInvalid},
		{StateScheduled, EventFrozenActorSetBecameEmpty, StateInvalid},
		{StateBlockedDSUnavailable, EventFrozenActorSetBecameEmpty, StateInvalid},
		{StateSubmitted, ActionVoid, StateVoided},
		{StateStageApproval, ActionVoid, StateVoided},
		{StateStageExecution, ActionVoid, StateVoided},
		{StateScheduled, ActionVoid, StateVoided},
		{StateBlockedDSUnavailable, ActionVoid, StateVoided},
		{StateResultUnknown, ActionVoid, StateVoided},
		{StateInvalid, ActionVoid, StateVoided},
	}
	for _, e := range edges {
		got, err := OrderTransition(e.from, e.trig)
		if err != nil {
			t.Fatalf("legal edge %s+%s: unexpected error %v", e.from, e.trig, err)
		}
		if got != e.to {
			t.Fatalf("legal edge %s+%s: got %s want %s", e.from, e.trig, got, e.to)
		}
	}
	// Coverage: total edge table count matches enumeration (no untested declared edges).
	total := 0
	for _, m := range orderEdges {
		total += len(m)
	}
	if total != len(edges) {
		t.Fatalf("orderEdges has %d edges, test enumerated %d", total, len(edges))
	}
	// Contract expanded edge count anchor (state-machines.json change_order).
	if total != 42 {
		t.Fatalf("contract change_order expands to 42 edges, got %d", total)
	}
}

// TestOrderMachineIllegalPairs All-pairs enumeration: undeclared combinations are uniformly rejected and state remains unchanged.
func TestOrderMachineIllegalPairs(t *testing.T) {
	triggers := OrderTriggerVocabulary()
	for _, s := range OrderAllStates() {
		for _, trig := range triggers {
			next, err := OrderTransition(s, trig)
			if _, legal := orderEdges[s][trig]; legal {
				continue
			}
			if err == nil {
				t.Fatalf("illegal pair %s+%s must fail, got %s", s, trig, next)
			}
			if next != s {
				t.Fatalf("illegal pair %s+%s mutated state to %s", s, trig, next)
			}
		}
	}
}

// TestOrderMachineTerminalStates Terminal states reject all triggers.
func TestOrderMachineTerminalStates(t *testing.T) {
	terminals := []OrderState{StateCompleted, StateRejected, StateWithdrawn,
		StateWithdrawnPartial, StateVoided, StateFailed, StatePartialFailed,
		StateCancelled, StatePartialCancelled, StateMissedSchedule}
	nonTerminal := map[OrderState]bool{StateSubmitted: true, StateStageApproval: true,
		StateStageExecution: true, StateScheduled: true, StateRunning: true,
		StateResultUnknown: true, StateBlockedDSUnavailable: true, StateInvalid: true}
	for _, s := range OrderAllStates() {
		if nonTerminal[s] == OrderTerminal(s) {
			t.Fatalf("terminal classification mismatch for %s", s)
		}
	}
	for _, s := range terminals {
		for _, trig := range OrderTriggerVocabulary() {
			if _, err := OrderTransition(s, trig); err == nil {
				t.Fatalf("terminal %s must reject %s", s, trig)
			}
		}
	}
}

// TestOrderGraphInvariants graph_invariants: invalid is non-terminal and its sole
// user action is void; initial state declaration.
func TestOrderGraphInvariants(t *testing.T) {
	if OrderTerminal(StateInvalid) {
		t.Fatal("invalid must be non-terminal")
	}
	out := orderEdges[StateInvalid]
	if len(out) != 1 {
		t.Fatalf("invalid must have exactly one outgoing edge, got %d", len(out))
	}
	if _, ok := out[ActionVoid]; !ok {
		t.Fatal("invalid's only outgoing edge must be void")
	}
	// Trigger vocabulary covers all declared edges (vocabulary completeness).
	vocab := map[OrderTrigger]bool{}
	for _, t := range OrderTriggerVocabulary() {
		vocab[t] = true
	}
	for s, m := range orderEdges {
		for trig := range m {
			if !vocab[trig] {
				t.Fatalf("trigger %s on %s missing from vocabulary", trig, s)
			}
		}
	}
}

// TestOrderStageAndStepEnums Stage and step enum validity.
func TestOrderStageAndStepEnums(t *testing.T) {
	for _, s := range []StageState{StagePending, StageApproval, StageExecution,
		StageScheduled, StageRunning, StageSucceeded, StageFailed, StagePartialFailed,
		StageCancelled, StagePartialCancel, StageResultUnknown, StageSkipped} {
		if !ValidStageState(s) {
			t.Fatalf("stage state %s must be valid", s)
		}
	}
	if ValidStageState(StageState("nope")) {
		t.Fatal("unknown stage state must be invalid")
	}
	if !StageLive(StageApproval) || StageLive(StageSucceeded) {
		t.Fatal("stage live classification mismatch")
	}
	for _, s := range []StepState{StepPending, StepActive, StepApproved,
		StepRejected, StepSkipped, StepInvalid} {
		if !ValidStepState(s) {
			t.Fatalf("step state %s must be valid", s)
		}
	}
}
