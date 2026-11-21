// Package persistence provides GORM v2 table models for the Review bounded context matching
// gorm-model-registry.json and postgresql-column-catalog.json without AutoMigrate or JSON tags.
package persistence

import (
	"strings"
	"time"
)

// EncodeTextArray encodes a Go string slice into a PostgreSQL text[] literal.
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

// DecodeTextArray decodes a PostgreSQL text[] literal into a Go string slice.
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
			rest := inner[min(i+1, len(inner)):]
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

// ChangeDraftModel corresponds to change_drafts (mutable_aggregate).
type ChangeDraftModel struct {
	ID                    string     `gorm:"column:id;primaryKey"`
	OwnerUserID           string     `gorm:"column:owner_user_id"`
	FlowID                string     `gorm:"column:flow_id"`
	FlowVersionSeen       int64      `gorm:"column:flow_version_seen"`
	Title                 string     `gorm:"column:title"`
	Description           string     `gorm:"column:description"`
	RequestedScheduledFor *time.Time `gorm:"column:requested_scheduled_for"` // Advisory requested schedule time copied onto submitted orders.
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

// DraftSQLPayloadModel corresponds to draft_sql_payloads (AEAD-sealed raw draft SQL payload).
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

// ReviewRunModel corresponds to review_runs (mutable until reaching a terminal review state).
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

// SQLStatementModel corresponds to sql_statements (per-statement structural metadata without raw SQL).
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

// SQLFingerprintGroupModel corresponds to sql_fingerprint_groups (aggregated statement groups per Run).
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

// StageReviewSnapshotModel corresponds to stage_review_snapshots (immutable per-stage review snapshot).
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

// EvidenceFactModel corresponds to evidence_facts (insert-only normalized tool fact records).
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

// EvidenceRawPayloadModel corresponds to evidence_raw_payloads (insert-only encrypted raw target payloads with 7d TTL).
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

// ReviewFindingModel corresponds to review_findings (insert-only review findings).
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

// ReviewFindingEvidenceModel corresponds to review_finding_evidence (finding-to-evidence join table).
type ReviewFindingEvidenceModel struct {
	FindingID      string `gorm:"column:finding_id;primaryKey"`
	EvidenceFactID string `gorm:"column:evidence_fact_id;primaryKey"`
}

func (ReviewFindingEvidenceModel) TableName() string { return "review_finding_evidence" }

// ReviewToolCallModel corresponds to review_tool_calls (insert-only tool invocation and injection audit rows).
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

// ReviewIdentifierMappingModel declares the contract schema anchor for review_identifier_mappings
// (managed via raw SQL in review/application/identifiermapping.go without AutoMigrate).
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

// AIProviderRateVersionModel declares the contract schema anchor for ai_provider_rate_versions
// (append-only pricing versions managed via raw SQL in review/application/pricing.go).
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

// AIProviderRateWindowModel declares the contract schema anchor for ai_provider_rate_windows
// (same-day [start,end) pricing windows managed via raw SQL in review/application/pricing.go).
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

// AICallUsageModel declares the contract schema anchor for ai_call_usage
// (append-only per-HTTP-call usage ledger managed via raw SQL in review/application/pricing.go).
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
