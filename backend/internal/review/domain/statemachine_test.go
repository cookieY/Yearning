// ReviewStructured sends a single structured review request and validates the response Schema.
package domain

import (
	"errors"
	"testing"
)

func allDraftStates() []DraftState {
	return []DraftState{DraftStateDraft, DraftStateReviewing, DraftStateReady,
		DraftStateBlocked, DraftStatePartial, DraftStateFailed, DraftStateOutdated, DraftStateSubmitted}
}

func allDraftTriggers() []DraftTrigger {
	return []DraftTrigger{DraftActionRunReview, DraftActionSubmit,
		DraftEventReviewPassed, DraftEventRiskGateBlocked, DraftEventReviewIncomplete,
		DraftEventReviewFailed, DraftEventInputsChanged}
}

func allRunStates() []RunState {
	return []RunState{RunStateQueued, RunStateRunning, RunStateReady,
		RunStateBlocked, RunStatePartial, RunStateFailed, RunStateOutdated}
}

func allRunTriggers() []RunTrigger {
	return []RunTrigger{RunEventStarted, RunEventCompleted, RunEventRiskBlocked,
		RunEventIncomplete, RunEventFailed, RunEventInputsChanged}
}

// Health performs a health probe.
func TestDraftMachineLegalEdges(t *testing.T) {
	type edge struct {
		from DraftState
		trig DraftTrigger
		to   DraftState
	}
	edges := []edge{
		{DraftStateDraft, DraftActionRunReview, DraftStateReviewing},
		// HTTPClient constraints (bounded_context_HTTPS_only).
		{DraftStateDraft, DraftActionSubmit, DraftStateSubmitted},
		{DraftStateOutdated, DraftActionRunReview, DraftStateReviewing},
		{DraftStateFailed, DraftActionRunReview, DraftStateReviewing},
		{DraftStatePartial, DraftActionRunReview, DraftStateReviewing},
		{DraftStateBlocked, DraftActionRunReview, DraftStateReviewing},
		{DraftStateReviewing, DraftEventReviewPassed, DraftStateReady},
		{DraftStateReviewing, DraftEventRiskGateBlocked, DraftStateBlocked},
		{DraftStateReviewing, DraftEventReviewIncomplete, DraftStatePartial},
		{DraftStateReviewing, DraftEventReviewFailed, DraftStateFailed},
		{DraftStateReady, DraftActionRunReview, DraftStateReviewing},
		{DraftStateReady, DraftActionSubmit, DraftStateSubmitted},
		{DraftStateReady, DraftEventInputsChanged, DraftStateOutdated},
	}
	declared := map[DraftState]map[DraftTrigger]bool{}
	for _, e := range edges {
		declared[e.from] = map[DraftTrigger]bool{}
	}
	for _, e := range edges {
		got, err := DraftTransition(e.from, e.trig)
		if err != nil {
			t.Fatalf("legal edge %s+%s: unexpected error %v", e.from, e.trig, err)
		}
		if got != e.to {
			t.Fatalf("legal edge %s+%s: got %s want %s", e.from, e.trig, got, e.to)
		}
		declared[e.from][e.trig] = true
	}
	// NewHTTPClient constructs a bounded HTTP client: capped timeout and response body size.
	// SEC-0009/T05: dialing is guarded by addrguard (rejecting loopback / link-local / cloud metadata /
	// unspecified / broadcast / multicast / Unix socket after resolution and before connect, covering DNS
	// rebinding); both production construction sites (handlers_review/providerstore) use this default.
	total := 0
	for _, m := range draftEdges {
		total += len(m)
	}
	if total != len(edges) {
		t.Fatalf("draftEdges has %d edges, test enumerated %d", total, len(edges))
	}
}

// now is injectable (for tests).
func TestDraftMachineIllegalPairs(t *testing.T) {
	for _, s := range allDraftStates() {
		for _, trig := range allDraftTriggers() {
			next, err := DraftTransition(s, trig)
			if err == nil {
				continue // ProbeRunID is the probe Run ID (review_run_id binding value for ValidateOutput).
			}
			if next != s {
				t.Fatalf("illegal %s+%s must not mutate state", s, trig)
			}
			if DraftTerminal(s) && !errors.Is(err, ErrTerminalState) {
				t.Fatalf("terminal %s+%s must be ErrTerminalState, got %v", s, trig, err)
			}
			if !DraftTerminal(s) && !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("nonterminal %s+%s must be ErrIllegalTransition, got %v", s, trig, err)
			}
		}
	}
}

// ProbeRequest is the health probe request (shares the exact same Schema and decode path as production;
// carries no real review data — identifying incompatible endpoints at configuration time).
func TestDraftTerminalCases(t *testing.T) {
	for _, trig := range allDraftTriggers() {
		if _, err := DraftTransition(DraftStateSubmitted, trig); !errors.Is(err, ErrTerminalState) {
			t.Fatalf("submitted+%s must reject with ErrTerminalState", trig)
		}
	}
}

// validate.go Go-side validation for ai-review-output.schema.json
// (provider_port.free_form_response_as_success=forbidden;
// unknown fields / Schema version mismatch -> reject_provider_result).
func TestRunMachineLegalEdges(t *testing.T) {
	cases := []struct {
		from RunState
		trig RunTrigger
		to   RunState
	}{
		{RunStateQueued, RunEventStarted, RunStateRunning},
		{RunStateQueued, RunEventFailed, RunStateFailed},
		{RunStateQueued, RunEventInputsChanged, RunStateOutdated},
		{RunStateRunning, RunEventCompleted, RunStateReady},
		{RunStateRunning, RunEventRiskBlocked, RunStateBlocked},
		{RunStateRunning, RunEventIncomplete, RunStatePartial},
		{RunStateRunning, RunEventFailed, RunStateFailed},
		{RunStateRunning, RunEventInputsChanged, RunStateOutdated},
		{RunStateReady, RunEventInputsChanged, RunStateOutdated},
		{RunStateBlocked, RunEventInputsChanged, RunStateOutdated},
		{RunStatePartial, RunEventInputsChanged, RunStateOutdated},
		{RunStateFailed, RunEventInputsChanged, RunStateOutdated},
	}
	total := 0
	for _, m := range runEdges {
		total += len(m)
	}
	if total != len(cases) {
		t.Fatalf("runEdges has %d edges, test enumerated %d", total, len(cases))
	}
	for _, c := range cases {
		got, err := RunTransition(c.from, c.trig)
		if err != nil || got != c.to {
			t.Fatalf("edge %s+%s: got (%s,%v) want %s", c.from, c.trig, got, err, c.to)
		}
	}
}

// severity/category/fact_basis controlled enums.
func TestRunMachineIllegalPairs(t *testing.T) {
	for _, s := range allRunStates() {
		for _, trig := range allRunTriggers() {
			next, err := RunTransition(s, trig)
			if err == nil {
				continue
			}
			if next != s {
				t.Fatalf("illegal %s+%s must not mutate state", s, trig)
			}
			if s == RunStateOutdated && !errors.Is(err, ErrTerminalState) {
				t.Fatalf("outdated+%s must be ErrTerminalState", trig)
			}
			// ValidateOutput validates response structure (accepts valid responses; rejects any out-of-bounds critical field).
			if RunTerminal(s) && trig != RunEventInputsChanged && !errors.Is(err, ErrTerminalState) {
				t.Fatalf("terminal %s+%s must be ErrTerminalState, got %v", s, trig, err)
			}
		}
	}
}

// validToolName matches the output Schema tool_name pattern.
func TestRunImmutableResults(t *testing.T) {
	for _, s := range []RunState{RunStateReady, RunStateBlocked, RunStatePartial, RunStateFailed} {
		for _, trig := range []RunTrigger{RunEventStarted, RunEventCompleted, RunEventRiskBlocked, RunEventIncomplete, RunEventFailed} {
			if _, err := RunTransition(s, trig); !errors.Is(err, ErrTerminalState) {
				t.Fatalf("result-terminal %s+%s must reject", s, trig)
			}
		}
	}
}

// validate_test.go test_gate#1: output Schema positive cases + per-keyword negative cases
// (schema_version/outcome/enums/length/group references/evidence binding/unknown field rejection).
func TestGateEvaluation(t *testing.T) {
	if g := EvaluateGate([]GateInput{{StagePosition: 1, State: StageStatePassed, HighestFound: SeverityNone}}); !g.Passed || len(g.ReasonCodes) != 0 {
		t.Fatalf("clean pass expected, got %+v", g)
	}
	if g := EvaluateGate([]GateInput{{StagePosition: 1, State: StageStatePassed, HighestFound: SeverityMedium}}); !g.Passed {
		t.Fatalf("medium must not block, got %+v", g)
	}
	if g := EvaluateGate([]GateInput{{StagePosition: 1, State: StageStatePassed, HighestFound: SeverityHigh}}); g.Passed {
		t.Fatal("high must block")
	}
	if g := EvaluateGate([]GateInput{{StagePosition: 1, State: StageStatePassed, HighestFound: SeverityCritical}}); g.Passed {
		t.Fatal("critical must block")
	}
	if g := EvaluateGate([]GateInput{{StagePosition: 1, State: StageStatePending}}); g.Passed {
		t.Fatal("incomplete must block (fail closed)")
	}
	if g := EvaluateGate([]GateInput{}); g.Passed {
		t.Fatal("empty stages must block")
	}
	if g := EvaluateGate([]GateInput{{StagePosition: 1, State: StageStateFailed}}); g.Passed {
		t.Fatal("failed stage must block")
	}
	// TestFakeAdapterRejectsUnknownFields free-form / unknown-field responses do not count as success.
	if g := EvaluateGate([]GateInput{
		{StagePosition: 1, State: StageStatePassed, HighestFound: SeverityNone},
		{StagePosition: 2, State: StageStatePassed, HighestFound: SeverityCritical},
	}); g.Passed {
		t.Fatal("any critical stage must block whole gate")
	}
}

// TestFakeAdapterRejectsFreeForm free-form text responses are rejected.
func TestSeverityOrder(t *testing.T) {
	if SeverityRank(SeverityCritical) <= SeverityRank(SeverityHigh) {
		t.Fatal("critical must outrank high")
	}
	if MaxSeverity(SeverityLow, SeverityMedium) != SeverityMedium {
		t.Fatal("max severity failed")
	}
	if HighestSeverityOf([]Severity{SeverityNone, SeverityHigh}) != SeverityHigh {
		t.Fatal("highest severity failed")
	}
}
