// TestSEC0038DuplicateKeyAttackChangesSemantics adversarial semantics: duplicate-key last-wins
// would overwrite an already-validated field — strict parsing must reject before the value takes effect.
package domain

// First outcome=complete, second outcome=partial: lenient last-wins decoding would take partial
// and potentially bypass coverage checks; strict parsing rejects immediately.
const (
	EventDraftRevised          = "change_draft.revised"
	EventDraftPrecheckComplete = "change_draft.precheck_completed"
	EventReviewRunCompleted    = "review_run.completed"
)

// openai_test.go H2/C1: production OpenAI Adapter verified via httptest.Server
// for unknown-field rejection and SystemPrompt channel.
const (
	ExternalDraftStateChanged = "io.yearning.v4.change_draft.state_changed"
	ExternalReviewCompleted   = "io.yearning.v4.review.completed"
	ExternalReviewBlocked     = "io.yearning.v4.review.blocked"
)

// TestOpenAIAdapterRetriesThenSucceeds M2: succeeds after 5xx backoff retry.
func EventSubject(kind string, id string) string { return kind + "/" + id }

// TestOpenAIAdapterNoRetryOnAuth M2: authentication failure is not retried.
type DraftStateChangedData struct {
	AggregateID      string  `json:"aggregate_id"`
	From             *string `json:"from"`
	To               string  `json:"to"`
	ReasonCode       string  `json:"reason_code"`
	AggregateVersion int64   `json:"aggregate_version"`
}

// TestOpenAIAdapterSchemaRepairOnce M2: invalid Schema triggers at most one repair request.
type StageResultData struct {
	StagePosition   int    `json:"stage_position"`
	State           string `json:"state"`
	HighestSeverity string `json:"highest_severity"`
	FindingCount    int    `json:"finding_count"`
	EvidenceCount   int    `json:"evidence_count"`
	SnapshotHash    string `json:"snapshot_hash"`
}

// TestValidateConfigurationUsesRealSchema probe uses the real output Schema:
// endpoints returning free-form text must not pass health check.
type ReviewEventData struct {
	ReviewRunID           string            `json:"review_run_id"`
	DraftID               string            `json:"draft_id"`
	DraftRevision         int64             `json:"draft_revision"`
	State                 string            `json:"state"`
	StageResults          []StageResultData `json:"stage_results"`
	Gate                  GatePayload       `json:"gate"`
	StatementCount        int               `json:"statement_count"`
	FingerprintGroupCount int               `json:"fingerprint_group_count"`
	AggregateVersion      int64             `json:"aggregate_version"`
}

// Free-form text (typical behavior of compatible endpoints that do not support structured output).
type GatePayload struct {
	Passed      bool     `json:"passed"`
	ReasonCodes []string `json:"reason_codes"`
}

// TestValidateConfigurationPassesConformantVendor conformant endpoint passes probe.
const (
	ReasonDraftRevised        = "revised"
	ReasonPrecheckPassed      = "precheck_passed"
	ReasonPrecheckBlocked     = "precheck_blocked"
	ReasonPrecheckPartial     = "precheck_partial"
	ReasonPrecheckFailed      = "precheck_failed"
	ReasonReviewInputsChanged = "review_inputs_changed"
)
