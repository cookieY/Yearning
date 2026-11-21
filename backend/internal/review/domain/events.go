// events.go constructs Review domain event names and payloads (api/events/domain-event.schema.json)
// following repository-event-routing.json: change_draft.revised / change_draft.precheck_completed ->
// io.yearning.v4.change_draft.state_changed; review_run.completed -> review.completed | review.blocked.
package domain

// Internal domain event names recorded in audit/outbox streams.
const (
	EventDraftRevised          = "change_draft.revised"
	EventDraftPrecheckComplete = "change_draft.precheck_completed"
	EventReviewRunCompleted    = "review_run.completed"
)

// External CloudEvent type identifiers emitted to SSE/notification subscribers.
const (
	ExternalDraftStateChanged = "io.yearning.v4.change_draft.state_changed"
	ExternalReviewCompleted   = "io.yearning.v4.review.completed"
	ExternalReviewBlocked     = "io.yearning.v4.review.blocked"
)

// EventSubject builds a canonical CloudEvent subject string (`<kind>/<id>`).
func EventSubject(kind string, id string) string { return kind + "/" + id }

// DraftStateChangedData is the payload emitted when a change_draft transitions state.
type DraftStateChangedData struct {
	AggregateID      string  `json:"aggregate_id"`
	From             *string `json:"from"`
	To               string  `json:"to"`
	ReasonCode       string  `json:"reason_code"`
	AggregateVersion int64   `json:"aggregate_version"`
}

// StageResultData summarizes a single stage outcome within a Review Run event.
type StageResultData struct {
	StagePosition   int    `json:"stage_position"`
	State           string `json:"state"`
	HighestSeverity string `json:"highest_severity"`
	FindingCount    int    `json:"finding_count"`
	EvidenceCount   int    `json:"evidence_count"`
	SnapshotHash    string `json:"snapshot_hash"`
}

// ReviewEventData is the payload for io.yearning.v4.review.completed and review.blocked events.
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

// GatePayload embeds the deterministic submission gate result inside ReviewEventData.
type GatePayload struct {
	Passed      bool     `json:"passed"`
	ReasonCodes []string `json:"reason_codes"`
}

// Reason codes explaining why a draft transitioned or a review completed in a non-ready state.
const (
	ReasonDraftRevised        = "revised"
	ReasonPrecheckPassed      = "precheck_passed"
	ReasonPrecheckBlocked     = "precheck_blocked"
	ReasonPrecheckPartial     = "precheck_partial"
	ReasonPrecheckFailed      = "precheck_failed"
	ReasonReviewInputsChanged = "review_inputs_changed"
)
