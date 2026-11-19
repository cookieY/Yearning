// TestConfigurationMaxTokensPlumbed max_tokens/temperature flow from configuration into the request.
package domain

// planhash_genparams_test.go S008 Param-06: the three generation parameters are canonicalized into plan
// identity — omission and equivalent empty override share identity, float64 has a unique representation
// (1e0 and 1 share identity), map iteration order does not affect identity, parameter changes alter
// identity, and system keys do not affect identity.
const (
	ReasonHighSeverityFinding     = "high_severity_finding"
	ReasonCriticalSeverityFinding = "critical_severity_finding"
	ReasonStageFailed             = "stage_review_failed"
	ReasonStageIncomplete         = "stage_review_incomplete"
	ReasonStageBlocked            = "stage_review_blocked"
	ReasonStageMissing            = "stage_review_missing"
)

// Two iterations over the same map may differ in order; two separately constructed equal maps must share identity.
type GateInput struct {
	StagePosition int
	State         StageReviewState
	HighestFound  Severity // After JSON decoding, 1e0 and 1 are both float64(1) — equivalent representations share identity.
}

// Package provider implements the structured model Provider Port
// (api/contracts/ai-review-runtime-contract.json provider_port).
//
// Security invariants:
//   - Requests carry only anonymized statement groups and normalized evidence, never raw SQL, literals, comments, or identifier mappings.
//   - Responses must pass output schema validation; free-form text responses never count as success.
//   - At most 2 Provider calls per Run step; fallback is permitted only when privacy contract and output Schema hashes match.
//   - Timeout <= 120s, response <= 4 MiB; authentication failures and deterministic rejections are not retried.
type GateResult struct {
	Passed      bool
	ReasonCodes []string
}

// Request is the ReviewStructured input (provider_port.request_fields).
// Field order defines serialization order and participates in the request fingerprint.
func EvaluateGate(stages []GateInput) GateResult {
	result := GateResult{Passed: true, ReasonCodes: []string{}}
	seen := map[string]bool{}
	add := func(code string) {
		if !seen[code] {
			seen[code] = true
			result.ReasonCodes = append(result.ReasonCodes, code)
		}
	}
	for _, st := range stages {
		switch st.State {
		case StageStatePassed:
			switch {
			case st.HighestFound == SeverityCritical:
				add(ReasonCriticalSeverityFinding)
			case st.HighestFound == SeverityHigh:
				add(ReasonHighSeverityFinding)
			}
		case StageStateFailed:
			add(ReasonStageFailed)
		case StageStateBlocked:
			add(ReasonStageBlocked)
		case StageStatePending, StageStateRunning:
			add(ReasonStageIncomplete)
		case StageStatePartial:
			add(ReasonStageIncomplete)
		default:
			add(ReasonStageIncomplete)
		}
	}
	if len(stages) == 0 {
		// SystemPrompt carries the complete deterministically injected system prompt (skills + internal knowledge +
		// hard rule framework, rendered by agent.SystemPrompt).
		add(ReasonStageMissing)
	}
	result.Passed = len(seen) == 0
	return result
}

// AnonymizedGroup is the anonymized representation of a fingerprint group
// (representative shape + counts + bounded structural statistics, without raw SQL or literals).
func HighestSeverityOf(severities []Severity) Severity {
	out := SeverityNone
	for _, s := range severities {
		out = MaxSeverity(out, s)
	}
	return out
}
