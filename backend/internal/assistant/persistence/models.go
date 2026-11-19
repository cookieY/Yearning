// models.go defines the GORM models for the AI Assistant domain (gorm-model-registry: assistant/persistence;
// schema frozen by 000026_ai_assistant.up.sql).
// JSON tags and GORM association fields are forbidden on persistence models (ADR-0005 / gorm-model-registry).
package persistence

import "time"

// AIAssistantSessionModel maps to ai_assistant_sessions.
type AIAssistantSessionModel struct {
	ID                  string     `gorm:"column:id;primaryKey"`
	UserID              string     `gorm:"column:user_id"`
	TitleCiphertext     []byte     `gorm:"column:title_ciphertext"`
	TitleNonce          []byte     `gorm:"column:title_nonce"`
	TitleKeyID          string     `gorm:"column:title_key_id"`
	TitleAlgorithm      string     `gorm:"column:title_algorithm"`
	State               string     `gorm:"column:state"`
	ActiveDatasourceID  *string    `gorm:"column:active_datasource_id"`
	ActiveFlowID        *string    `gorm:"column:active_flow_id"`
	ActiveSchemaName    *string    `gorm:"column:active_schema_name"`
	SelectedDraftID     *string    `gorm:"column:selected_draft_id"`
	CompactedThroughSeq int64      `gorm:"column:compacted_through_seq"`
	ExpiresAt           time.Time  `gorm:"column:expires_at"`
	DeletedAt           *time.Time `gorm:"column:deleted_at"`
	Version             int64      `gorm:"column:version"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

func (AIAssistantSessionModel) TableName() string { return "ai_assistant_sessions" }

// AIAssistantMessageModel maps to ai_assistant_messages.
type AIAssistantMessageModel struct {
	ID                string    `gorm:"column:id;primaryKey"`
	SessionID         string    `gorm:"column:session_id"`
	UserID            string    `gorm:"column:user_id"`
	Sequence          int64     `gorm:"column:sequence"`
	Role              string    `gorm:"column:role"`
	Capability        string    `gorm:"column:capability"`
	State             string    `gorm:"column:state"`
	ContentCiphertext []byte    `gorm:"column:content_ciphertext"`
	ContentNonce      []byte    `gorm:"column:content_nonce"`
	ContentKeyID      string    `gorm:"column:content_key_id"`
	ContentAlgorithm  string    `gorm:"column:content_algorithm"`
	SQLDraftID        *string   `gorm:"column:sql_draft_id"`
	FailureCode       *int      `gorm:"column:failure_code"`
	Version           int64     `gorm:"column:version"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (AIAssistantMessageModel) TableName() string { return "ai_assistant_messages" }

// AIAssistantSQLDraftModel maps to ai_assistant_sql_drafts.
type AIAssistantSQLDraftModel struct {
	ID                      string    `gorm:"column:id;primaryKey"`
	SessionID               string    `gorm:"column:session_id"`
	MessageID               string    `gorm:"column:message_id"`
	UserID                  string    `gorm:"column:user_id"`
	Revision                int64     `gorm:"column:revision"`
	DatasourceID            string    `gorm:"column:datasource_id"`
	FlowID                  *string   `gorm:"column:flow_id"`
	Engine                  string    `gorm:"column:engine"`
	SchemaName              string    `gorm:"column:schema_name"`
	StatementKind           string    `gorm:"column:statement_kind"`
	RiskCategory            string    `gorm:"column:risk_category"`
	SQLCiphertext           []byte    `gorm:"column:sql_ciphertext"`
	SQLNonce                []byte    `gorm:"column:sql_nonce"`
	SQLKeyID                string    `gorm:"column:sql_key_id"`
	SQLAlgorithm            string    `gorm:"column:sql_algorithm"`
	SQLHMAC                 []byte    `gorm:"column:sql_hmac"`
	SQLHMACKeyID            string    `gorm:"column:sql_hmac_key_id"`
	SubmissionState         string    `gorm:"column:submission_state"`
	EphemeralRunID          *string   `gorm:"column:ephemeral_run_id"`
	SubmittedOrderID        *string   `gorm:"column:submitted_order_id"`
	SubmittedOrderDisplayNo *string   `gorm:"column:submitted_order_display_no"`
	Version                 int64     `gorm:"column:version"`
	CreatedAt               time.Time `gorm:"column:created_at"`
	UpdatedAt               time.Time `gorm:"column:updated_at"`
}

func (AIAssistantSQLDraftModel) TableName() string { return "ai_assistant_sql_drafts" }

// AIAssistantContextSummaryModel maps to ai_assistant_context_summaries.
type AIAssistantContextSummaryModel struct {
	ID                string    `gorm:"column:id;primaryKey"`
	SessionID         string    `gorm:"column:session_id"`
	CoveredFromSeq    int64     `gorm:"column:covered_from_seq"`
	CoveredThroughSeq int64     `gorm:"column:covered_through_seq"`
	SourceVersion     int64     `gorm:"column:source_version"`
	SummaryCiphertext []byte    `gorm:"column:summary_ciphertext"`
	SummaryNonce      []byte    `gorm:"column:summary_nonce"`
	SummaryKeyID      string    `gorm:"column:summary_key_id"`
	SummaryAlgorithm  string    `gorm:"column:summary_algorithm"`
	Invalidated       bool      `gorm:"column:invalidated"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

func (AIAssistantContextSummaryModel) TableName() string { return "ai_assistant_context_summaries" }
