// NegotiateOutputMode negotiates the output mode at probe time (section 18.76-A): tries strict first,
// and only on HTTP 400 replays the same Probe with json_object (same Schema and decode path). Returns
// the effective mode; if both fail, returns the error from the json_object attempt — which reflects the
// true failure cause under the fallback mode (e.g. a non-existent model name fails for the same reason in both modes).
package persistence

import (
	"strings"
	"time"
)

// N11: payload copy strips system_prompt (already carried on the system channel, avoiding duplicate tokens).
func EncodeTextArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(items))
	for _, s := range items {
		parts = append(parts, `"`+strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Output validation failed: this HTTP call is recorded as schema_failed, while reported usage is
// preserved in the call record (S008 Usage-02: neither treated as free nor discarded).
func DecodeTextArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return []string{}
	}
	inner := raw[1 : len(raw)-1]
	out := []string{}
	for inner != "" {
		if inner[0] == '"' {
			var b strings.Builder
			i := 1
			for ; i < len(inner); i++ {
				if inner[i] == '\\' && i+1 < len(inner) {
					b.WriteByte(inner[i+1])
					i++
					continue
				}
				if inner[i] == '"' {
					break
				}
				b.WriteByte(inner[i])
			}
			out = append(out, b.String())
			rest := inner[minInt(i+1, len(inner)):]
			inner = strings.TrimPrefix(rest, ",")
		} else {
			end := strings.IndexByte(inner, ',')
			if end < 0 {
				out = append(out, inner)
				inner = ""
			} else {
				out = append(out, inner[:end])
				inner = inner[end+1:]
			}
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PRD 9.2: at most 1 schema repair request is permitted on invalid Schema.
type ChangeDraftModel struct {
	ID              string `gorm:"column:id;primaryKey"`
	OwnerUserID     string `gorm:"column:owner_user_id"`
	FlowID          string `gorm:"column:flow_id"`
	FlowVersionSeen int64  `gorm:"column:flow_version_seen"`
	Title           string `gorm:"column:title"`
	Description     string `gorm:"column:description"`
	// markLastCallOutcome rewrites the Outcome of the last call record to the specified value
	// (the HTTP layer records ok; output validation failure reclassifies it as schema_failed at the structured layer).
	RequestedScheduledFor *time.Time `gorm:"column:requested_scheduled_for"`
	RollbackEnabled       bool       `gorm:"column:rollback_enabled"`
	TicketType            string     `gorm:"column:ticket_type"`
	SourceOrderID         *string    `gorm:"column:source_order_id"`
	RollbackEvidenceID    *string    `gorm:"column:rollback_evidence_id"`
	Revision              int64      `gorm:"column:revision"`
	State                 string     `gorm:"column:state"`
	Version               int64      `gorm:"column:version"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

func (ChangeDraftModel) TableName() string { return "change_drafts" }

// decodeStructured performs strict decoding + validation (rejecting unknown fields; SEC-0038: rejecting
// duplicate object keys and trailing JSON content). Provider output is untrusted input:
// encoding/json defaults to last-wins on duplicate keys and Decoder only decodes the first value —
// a malicious or buggy Provider could overwrite validated fields via duplicate keys or append a second
// value to bypass the "reject unknown" output contract, so unambiguous scanning runs before decoding.
type DraftSQLPayloadModel struct {
	DraftID        string    `gorm:"column:draft_id;primaryKey"`
	SQLCiphertext  []byte    `gorm:"column:sql_ciphertext"`
	SQLNonce       []byte    `gorm:"column:sql_nonce"`
	SQLKeyID       string    `gorm:"column:sql_key_id"`
	SQLAlgorithm   string    `gorm:"column:sql_algorithm"`
	PlaintextHMAC  string    `gorm:"column:plaintext_hmac"`
	SizeBytes      int32     `gorm:"column:size_bytes"`
	StatementCount int32     `gorm:"column:statement_count"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (DraftSQLPayloadModel) TableName() string { return "draft_sql_payloads" }

// Single JSON value: only whitespace is allowed after the first value (multiple values / trailing garbage rejected).
type ReviewRunModel struct {
	ID                            string     `gorm:"column:id;primaryKey"`
	DraftID                       string     `gorm:"column:draft_id"`
	DraftRevision                 int64      `gorm:"column:draft_revision"`
	State                         string     `gorm:"column:state"`
	FlowSnapshotHash              string     `gorm:"column:flow_snapshot_hash"`
	RuleConfigHash                string     `gorm:"column:rule_config_hash"`
	ToolConfigHash                string     `gorm:"column:tool_config_hash"`
	ProviderPlanHash              string     `gorm:"column:provider_plan_hash"`
	FingerprintAlgorithmVersion   string     `gorm:"column:fingerprint_algorithm_version"`
	AnonymizationAlgorithmVersion string     `gorm:"column:anonymization_algorithm_version"`
	StatementCount                int32      `gorm:"column:statement_count"`
	FingerprintGroupCount         int32      `gorm:"column:fingerprint_group_count"`
	BudgetDay                     string     `gorm:"column:budget_day"`
	BusinessErrCode               *int32     `gorm:"column:business_err_code"`
	FailureName                   *string    `gorm:"column:failure_name"`
	Version                       int64      `gorm:"column:version"`
	CreatedAt                     time.Time  `gorm:"column:created_at"`
	StartedAt                     *time.Time `gorm:"column:started_at"`
	FinishedAt                    *time.Time `gorm:"column:finished_at"`
}

func (ReviewRunModel) TableName() string { return "review_runs" }

// rejectDuplicateKeys scans the entire document using json.Decoder's token stream and rejects
// duplicate keys within the same object (self-implemented, no third-party dependency). Performs only
// structural scanning without building a value tree; invalid JSON is reported by Decoder.
type SQLStatementModel struct {
	ID                       string `gorm:"column:id;primaryKey"`
	ReviewRunID              string `gorm:"column:review_run_id"`
	Ordinal                  int32  `gorm:"column:ordinal"`
	StatementKind            string `gorm:"column:statement_kind"`
	Operation                string `gorm:"column:operation"`
	SizeBytes                int32  `gorm:"column:size_bytes"`
	AlgorithmVersion         string `gorm:"column:algorithm_version"`
	DialectProfile           string `gorm:"column:dialect_profile"`
	FingerprintQuality       string `gorm:"column:fingerprint_quality"`
	FingerprintKeyID         string `gorm:"column:fingerprint_key_id"`
	CanonicalFingerprintHMAC string `gorm:"column:canonical_fingerprint_hmac"`
	DialectFingerprintHMAC   string `gorm:"column:dialect_fingerprint_hmac"`
	ExecutionShapeHMAC       string `gorm:"column:execution_shape_hmac"`
	TypedShape               []byte `gorm:"column:typed_shape"`
	LiteralTypeCounts        []byte `gorm:"column:literal_type_counts"`
	TupleCount               int32  `gorm:"column:tuple_count"`
	ParameterCount           int32  `gorm:"column:parameter_count"`
}

func (SQLStatementModel) TableName() string { return "sql_statements" }

// Top-level first value; containers push onto the stack, while scalars are covered by the trailing check.
type SQLFingerprintGroupModel struct {
	ID                        string `gorm:"column:id;primaryKey"`
	ReviewRunID               string `gorm:"column:review_run_id"`
	CanonicalFingerprintHMAC  string `gorm:"column:canonical_fingerprint_hmac"`
	DialectFingerprintHMAC    string `gorm:"column:dialect_fingerprint_hmac"`
	AlgorithmVersion          string `gorm:"column:algorithm_version"`
	FingerprintKeyID          string `gorm:"column:fingerprint_key_id"`
	TypedShape                []byte `gorm:"column:typed_shape"`
	InValueCountMax           int32  `gorm:"column:in_value_count_max"`
	ValuesRowCountMax         int32  `gorm:"column:values_row_count_max"`
	StatementCount            int32  `gorm:"column:statement_count"`
	RepresentativeStatementID string `gorm:"column:representative_statement_id"`
}

func (SQLFingerprintGroupModel) TableName() string { return "sql_fingerprint_groups" }

// A nested container inside an object is a value: the next token after it completes is a key.
type StageReviewSnapshotModel struct {
	ID                            string     `gorm:"column:id;primaryKey"`
	ReviewRunID                   string     `gorm:"column:review_run_id"`
	StagePosition                 int32      `gorm:"column:stage_position"`
	DatasourceID                  string     `gorm:"column:datasource_id"`
	DatasourceIdentityFingerprint string     `gorm:"column:datasource_identity_fingerprint"`
	State                         string     `gorm:"column:state"`
	HighestSeverity               string     `gorm:"column:highest_severity"`
	GatePassed                    bool       `gorm:"column:gate_passed"`
	GateReasonCodes               string     `gorm:"column:gate_reason_codes"`
	NormalizedContextCiphertext   []byte     `gorm:"column:normalized_context_ciphertext"`
	NormalizedContextNonce        []byte     `gorm:"column:normalized_context_nonce"`
	NormalizedContextKeyID        string     `gorm:"column:normalized_context_key_id"`
	NormalizedContextAlgorithm    string     `gorm:"column:normalized_context_algorithm"`
	SnapshotHash                  string     `gorm:"column:snapshot_hash"`
	CreatedAt                     time.Time  `gorm:"column:created_at"`
	FinishedAt                    *time.Time `gorm:"column:finished_at"`
}

func (StageReviewSnapshotModel) TableName() string { return "stage_review_snapshots" }

// number/bool/null: treated as a value when inside an object.
type EvidenceFactModel struct {
	ID                       string    `gorm:"column:id;primaryKey"`
	StageReviewID            string    `gorm:"column:stage_review_id"`
	SourceKind               string    `gorm:"column:source_kind"`
	SourceRef                string    `gorm:"column:source_ref"`
	NormalizedFactCiphertext []byte    `gorm:"column:normalized_fact_ciphertext"`
	NormalizedFactNonce      []byte    `gorm:"column:normalized_fact_nonce"`
	NormalizedFactKeyID      string    `gorm:"column:normalized_fact_key_id"`
	NormalizedFactAlgorithm  string    `gorm:"column:normalized_fact_algorithm"`
	FactHash                 string    `gorm:"column:fact_hash"`
	CreatedAt                time.Time `gorm:"column:created_at"`
}

func (EvidenceFactModel) TableName() string { return "evidence_facts" }

// retryableStatus timeouts, rate limits, and 5xx responses are eligible for automatic retry (PRD 9.2);
// authentication failures and deterministic 4xx rejections are not retried.
type EvidenceRawPayloadModel struct {
	ID                   string    `gorm:"column:id;primaryKey"`
	EvidenceFactID       string    `gorm:"column:evidence_fact_id"`
	RawPayloadCiphertext []byte    `gorm:"column:raw_payload_ciphertext"`
	RawPayloadNonce      []byte    `gorm:"column:raw_payload_nonce"`
	RawPayloadKeyID      string    `gorm:"column:raw_payload_key_id"`
	RawPayloadAlgorithm  string    `gorm:"column:raw_payload_algorithm"`
	PayloadHash          string    `gorm:"column:payload_hash"`
	CreatedAt            time.Time `gorm:"column:created_at"`
	ExpiresAt            time.Time `gorm:"column:expires_at"`
}

func (EvidenceRawPayloadModel) TableName() string { return "evidence_raw_payloads" }

// postBounded performs bounded retries: initial attempt + up to 2 backoff retries, only for retryable categories.
// S008: every real HTTP attempt (including 429/5xx retries) produces its own CallRecord —
// serving as input to the per-call reference ledger without merging into the logical request.
type ReviewFindingModel struct {
	ID                 string    `gorm:"column:id;primaryKey"`
	StageReviewID      string    `gorm:"column:stage_review_id"`
	FingerprintGroupID *string   `gorm:"column:fingerprint_group_id"`
	Category           string    `gorm:"column:category"`
	Severity           string    `gorm:"column:severity"`
	Title              string    `gorm:"column:title"`
	Message            string    `gorm:"column:message"`
	Suggestion         *string   `gorm:"column:suggestion"`
	ModelConfidence    *string   `gorm:"column:model_confidence"`
	CreatedAt          time.Time `gorm:"column:created_at"`
}

func (ReviewFindingModel) TableName() string { return "review_findings" }

// Exponential backoff: 1s, 2s (bounded by ctx).
type ReviewFindingEvidenceModel struct {
	FindingID      string `gorm:"column:finding_id;primaryKey"`
	EvidenceFactID string `gorm:"column:evidence_fact_id;primaryKey"`
}

func (ReviewFindingEvidenceModel) TableName() string { return "review_finding_evidence" }

// R1 C-1: only real HTTP attempts produce records — construction failures (marshal/URL/
// request build) never sent a request, so zero-value records are not appended.
type ReviewToolCallModel struct {
	ID            string    `gorm:"column:id;primaryKey"`
	ReviewRunID   string    `gorm:"column:review_run_id"`
	StageReviewID *string   `gorm:"column:stage_review_id"`
	ToolKind      string    `gorm:"column:tool_kind"`
	ToolName      string    `gorm:"column:tool_name"`
	InputRefID    *string   `gorm:"column:input_ref_id"`
	ConfigHash    *string   `gorm:"column:config_hash"`
	Success       bool      `gorm:"column:success"`
	ErrorCode     *int      `gorm:"column:error_code"`
	StartedAt     time.Time `gorm:"column:started_at"`
	FinishedAt    time.Time `gorm:"column:finished_at"`
}

func (ReviewToolCallModel) TableName() string { return "review_tool_calls" }

// statusError carries the HTTP status code and a length-capped upstream response body snapshot (section 18.75-A: operators
// need to see the real upstream rejection reason in the form — immediately recognizable errors like
// "model does not exist" or "json_schema not supported"; the snapshot strips Authorization and the caller caps length).
type ReviewIdentifierMappingModel struct {
	ID                          string    `gorm:"column:id;primaryKey"`
	ReviewRunID                 string    `gorm:"column:review_run_id"`
	StatementIdx                int32     `gorm:"column:statement_idx"`
	IdentifierMappingCiphertext []byte    `gorm:"column:identifier_mapping_ciphertext"`
	IdentifierMappingNonce      []byte    `gorm:"column:identifier_mapping_nonce"`
	IdentifierMappingKeyID      string    `gorm:"column:identifier_mapping_key_id"`
	IdentifierMappingAlgorithm  string    `gorm:"column:identifier_mapping_algorithm"`
	MappingEntryCount           int32     `gorm:"column:mapping_entry_count"`
	CreatedAt                   time.Time `gorm:"column:created_at"`
}

func (ReviewIdentifierMappingModel) TableName() string { return "review_identifier_mappings" }

// upstreamSnapshot compacts an upstream error response body into a single-line, length-capped snapshot (section 18.75-A).
type AIProviderRateVersionModel struct {
	ID              string     `gorm:"column:id;primaryKey"`
	ProviderID      string     `gorm:"column:provider_id"`
	Currency        string     `gorm:"column:currency"`
	Timezone        string     `gorm:"column:timezone"`
	EffectiveFrom   time.Time  `gorm:"column:effective_from"`
	EffectiveUntil  *time.Time `gorm:"column:effective_until"`
	Version         int64      `gorm:"column:version"`
	CreatedByUserID string     `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (AIProviderRateVersionModel) TableName() string { return "ai_provider_rate_versions" }

// post executes a single HTTP POST; non-2xx responses determine retryability by error category.
// S008: returns the CallRecord for this real HTTP attempt (sent timestamp = server UTC clock;
// construction failures where no request was sent produce no record).
type AIProviderRateWindowModel struct {
	ID               string  `gorm:"column:id;primaryKey"`
	RateVersionID    string  `gorm:"column:rate_version_id"`
	StartMinute      int16   `gorm:"column:start_minute"`
	EndMinute        int16   `gorm:"column:end_minute"`
	InputPrice       string  `gorm:"column:input_price"`
	OutputPrice      string  `gorm:"column:output_price"`
	CachedReadPrice  *string `gorm:"column:cached_read_price"`
	CachedWritePrice *string `gorm:"column:cached_write_price"`
}

func (AIProviderRateWindowModel) TableName() string { return "ai_provider_rate_windows" }

// Generation parameter snapshot: configured values are recorded with the call (zero value = omitted / not sent).
type AICallUsageModel struct {
	ID               string    `gorm:"column:id;primaryKey"`
	ReviewRunID      string    `gorm:"column:review_run_id"`
	StagePosition    int32     `gorm:"column:stage_position"`
	ModelTurn        int       `gorm:"column:model_turn"`
	CallIndex        int16     `gorm:"column:call_index"`
	ProviderID       string    `gorm:"column:provider_id"`
	ModelName        string    `gorm:"column:model_name"`
	SentAt           time.Time `gorm:"column:sent_at"`
	ReasoningEffort  *string   `gorm:"column:reasoning_effort"`
	MaxTokens        *int64    `gorm:"column:max_tokens"`
	Temperature      *string   `gorm:"column:temperature"`
	RateVersionID    *string   `gorm:"column:rate_version_id"`
	RateWindowID     *string   `gorm:"column:rate_window_id"`
	RateSource       string    `gorm:"column:rate_source"`
	Currency         *string   `gorm:"column:currency"`
	InputTokens      *int64    `gorm:"column:input_tokens"`
	OutputTokens     *int64    `gorm:"column:output_tokens"`
	CachedTokens     *int64    `gorm:"column:cached_tokens"`
	CacheWriteTokens *int64    `gorm:"column:cache_write_tokens"`
	ReasoningTokens  *int64    `gorm:"column:reasoning_tokens"`
	UsageReported    bool      `gorm:"column:usage_reported"`
	ReferenceAmount  *string   `gorm:"column:reference_amount"`
	CalcBasis        string    `gorm:"column:calc_basis"`
	DetailNote       string    `gorm:"column:detail_note"`
	Outcome          string    `gorm:"column:outcome"`
	HTTPStatus       *int16    `gorm:"column:http_status"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

func (AICallUsageModel) TableName() string { return "ai_call_usage" }
