// gate.go implements pure SubmissionGate evaluation: deterministically applies policy independently
// of AI narrative conclusions. High/critical findings or incomplete stage reviews always block
// submission (fail closed; model confidence cannot override the gate).
package domain

// Machine reason codes surfaced when the submission gate blocks draft promotion.
const (
	ReasonHighSeverityFinding     = "high_severity_finding"
	ReasonCriticalSeverityFinding = "critical_severity_finding"
	ReasonStageFailed             = "stage_review_failed"
	ReasonStageIncomplete         = "stage_review_incomplete"
	ReasonStageBlocked            = "stage_review_blocked"
	ReasonStageMissing            = "stage_review_missing"
)

// GateInput represents the terminal outcome and highest finding severity for one stage.
type GateInput struct {
	StagePosition int
	State         StageReviewState
	HighestFound  Severity // Normalized severity rank used to detect high/critical blockers.
}

// GateResult holds the deterministic submission gate verdict and deduplicated blocking reasons.
type GateResult struct {
	Passed      bool
	ReasonCodes []string
}

// EvaluateGate computes the submission gate verdict across all frozen stage review outcomes.
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
		// An empty stage list fails closed rather than implicitly allowing submission.
		add(ReasonStageMissing)
	}
	result.Passed = len(seen) == 0
	return result
}

// HighestSeverityOf returns the maximum severity across a slice of finding severities.
func HighestSeverityOf(severities []Severity) Severity {
	out := SeverityNone
	for _, s := range severities {
		out = MaxSeverity(out, s)
	}
	return out
}
