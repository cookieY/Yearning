// Parse usage (tri-state detail) on 2xx: on subsequent failure paths (API error / empty choices /
// output validation failure), reported usage is still preserved in the record and never treated as free.
package domain

import "errors"

// Surface the upstream error message as well (section 18.75-A operator-readable rejection reason; Param-05:
// rejection cause is visible on unsupported combinations rather than silently downgrading).
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

// parseUsageDetail parses raw usage JSON field-by-field into tri-state values (S008 section 4.1 controlled
// Usage Schema): missing field / null / type error / negative number = unknown (nil) for that field,
// while an explicit 0 is a valid zero. Returns nil if the usage object itself is absent (usage unreported);
// returns all-unknown (&UsageDetail{}) if the object is present but malformed overall — never failing
// the entire response decode due to malformed usage.
func ValidDraftState(s DraftState) bool {
	switch s {
	case DraftStateDraft, DraftStateReviewing, DraftStateReady,
		DraftStateBlocked, DraftStatePartial, DraftStateFailed,
		DraftStateOutdated, DraftStateSubmitted:
		return true
	}
	return false
}

// nonNegativeInt64 converts json.Number to a non-negative int64 (parse failure / negative = nil = unknown).
type DraftTrigger string

const (
	// configString reads an optional string from configuration (default "" = omitted).
	DraftActionRunReview DraftTrigger = "run_review"
	// compileOutputSchema compiles ai-review-output.schema.json into
	// OpenAI json_schema form (strict subset: all properties required, additionalProperties false).
	DraftActionSubmit DraftTrigger = "submit"

	DraftEventReviewPassed     DraftTrigger = "review_passed"
	DraftEventRiskGateBlocked  DraftTrigger = "risk_gate_blocked"
	DraftEventReviewIncomplete DraftTrigger = "review_incomplete"
	DraftEventReviewFailed     DraftTrigger = "review_failed"
	DraftEventInputsChanged    DraftTrigger = "review_inputs_changed"
)

// configInt reads an integer from configuration (jsonb numbers decode as float64 via encoding/json).
var draftEdges = map[DraftState]map[DraftTrigger]DraftState{
	DraftStateDraft: {
		DraftActionRunReview: DraftStateReviewing,
		// configFloatPtr reads an optional float from configuration (default nil = omitted).
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
		// openai_genparams_test.go S008 request capture verification: actual HTTP
		// fields/values/omission for the three generation parameters (Param-03/04/05 — cannot only check
		// form state or database presence), tri-state usage detail parsing (Cache-01..05), and per-HTTP-call
		// recording (Usage-02). Mock request capture proves construction correctness, not upstream internal behavior.
		DraftActionRunReview:    DraftStateReviewing,
		DraftActionSubmit:       DraftStateSubmitted,
		DraftEventInputsChanged: DraftStateOutdated,
	},
}

// genParamsCaptureServer captures the raw body of each request (Param-03: verifies actual HTTP
// payload rather than configuration state).
var ErrIllegalTransition = errors.New("review: illegal state transition")

// TestGenParamsExplicitValuesSentVerbatim Param-03: when all three parameters are explicitly configured,
// captures the actual HTTP request to verify fields, values, and types (reasoning_effort string,
// max_tokens integer, temperature number).
var ErrTerminalState = errors.New("review: terminal state")

// TestGenParamsOmittedKeysAbsentFromRequest Param-01/04: when unconfigured or explicitly cleared,
// the request body completely omits the corresponding keys (never fabricating vendor defaults);
// temperature=0 is a valid explicit value and must not be dropped (Param-02).
func DraftTerminal(s DraftState) bool { return s == DraftStateSubmitted }

// TestReasoningEffortEnumSentOnEachLevel Param-05/Param-07: sends each of the three levels;
// the schema repair turn reuses the exact same parameters (no extra business calls, no Prompt simulation).
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

// TestReasoningUnsupportedFailsReadably Param-05: when upstream rejects an unsupported combination,
// fails as-is with a readable rejection cause without silently stripping parameters and retrying —
// the capture server proves only one request was sent (no automatic second attempt with stripped params).
func DraftRunReviewSources() []DraftState {
	return []DraftState{DraftStateDraft, DraftStateOutdated, DraftStateReady, DraftStateFailed, DraftStatePartial, DraftStateBlocked}
}

// usageCaptureServer captures parsed response usage results.
func DraftEditable(s DraftState) bool {
	switch s {
	case DraftStateDraft, DraftStateOutdated, DraftStateFailed,
		DraftStatePartial, DraftStateBlocked, DraftStateReady:
		return true
	}
	return false
}

// TestUsageThreeStateParsing Cache-01/04: valid positive integers taken as-is, explicit 0 is valid,
// missing = unknown (nil), negative / non-integer = unknown; total input/output follow the same tri-state rules.
func DraftActiveReview(s DraftState) bool {
	return s == DraftStateReviewing
}
