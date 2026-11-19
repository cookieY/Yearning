// types.go defines the domain enums, view structs, and quota constants for the Database AI Assistant.
package domain

import "time"

// Session retention and quota constants (PRD §5.7 A3).
const (
	DefaultSessionRetentionDays = 30
	DefaultSessionRetention     = 30 * 24 * time.Hour
	MaxActiveSessionsPerUser    = 50
	MaxMessagesPerSession       = 100
)

// Capability enumerates the three supported assistant capabilities plus out_of_scope.
type Capability string

const (
	CapabilityPlatformHelp Capability = "platform_help"
	CapabilitySQLGenerate  Capability = "sql_generate"
	CapabilitySQLOptimize  Capability = "sql_optimize"
	CapabilityOutOfScope   Capability = "out_of_scope"
)

func ValidCapability(c string) bool {
	switch Capability(c) {
	case CapabilityPlatformHelp, CapabilitySQLGenerate, CapabilitySQLOptimize, CapabilityOutOfScope:
		return true
	}
	return false
}

// SessionState enumerates session lifecycle states.
type SessionState string

const (
	SessionStateActive  SessionState = "active"
	SessionStateDeleted SessionState = "deleted"
	SessionStateExpired SessionState = "expired"
)

// MessageRole enumerates message roles.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
)

// MessageState enumerates message lifecycle states.
type MessageState string

const (
	MessageStreaming MessageState = "streaming"
	MessageCompleted MessageState = "completed"
	MessageCancelled MessageState = "cancelled"
	MessageFailed    MessageState = "failed"
)

// DraftRiskCategory classifies the risk tier of a SQL draft.
type DraftRiskCategory string

const (
	RiskReadOnly DraftRiskCategory = "read_only"
	RiskDML      DraftRiskCategory = "dml"
	RiskDDL      DraftRiskCategory = "ddl"
)

// DraftSubmissionState enumerates the precheck and order submission states of a SQL draft.
type DraftSubmissionState string

const (
	SubmissionDraft           DraftSubmissionState = "draft"
	SubmissionPrecheckRunning DraftSubmissionState = "precheck_running"
	SubmissionPrecheckReady   DraftSubmissionState = "precheck_ready"
	SubmissionPrecheckBlocked DraftSubmissionState = "precheck_blocked"
	SubmissionOrderSubmitted  DraftSubmissionState = "order_submitted"
	SubmissionStale           DraftSubmissionState = "stale"
)

// StatementKind enumerates the allowed SQL statement kinds in v4.0.0 (PRD §5.1 A2).
type StatementKind string

const (
	StmtSelect      StatementKind = "select"
	StmtInsert      StatementKind = "insert"
	StmtUpdate      StatementKind = "update"
	StmtDelete      StatementKind = "delete"
	StmtCreateTable StatementKind = "create_table"
	StmtAlterTable  StatementKind = "alter_table"
	StmtCreateIndex StatementKind = "create_index"
	StmtDropIndex   StatementKind = "drop_index"
)

// RiskCategoryOf returns the risk category corresponding to a StatementKind.
func RiskCategoryOf(kind StatementKind) (DraftRiskCategory, bool) {
	switch kind {
	case StmtSelect:
		return RiskReadOnly, true
	case StmtInsert, StmtUpdate, StmtDelete:
		return RiskDML, true
	case StmtCreateTable, StmtAlterTable, StmtCreateIndex, StmtDropIndex:
		return RiskDDL, true
	default:
		return "", false
	}
}

// Actor captures the authenticated user identity snapshot.
type Actor struct {
	UserID         string
	Username       string
	IsBuiltinAdmin bool
}

// SessionView corresponds to OpenAPI AssistantSession.
type SessionView struct {
	ID                  string  `json:"id"`
	UserID              string  `json:"user_id"`
	Title               string  `json:"title"`
	State               string  `json:"state"`
	ActiveDatasourceID  *string `json:"active_datasource_id"`
	ActiveFlowID        *string `json:"active_flow_id"`
	ActiveSchemaName    *string `json:"active_schema_name"`
	SelectedDraftID     *string `json:"selected_draft_id"`
	CompactedThroughSeq int64   `json:"compacted_through_seq"`
	ExpiresAt           string  `json:"expires_at"`
	Version             int64   `json:"version"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

// CitationView corresponds to OpenAPI AssistantCitation.
type CitationView struct {
	CitationID string  `json:"citation_id"`
	Title      string  `json:"title"`
	SourceRef  string  `json:"source_ref"`
	RoutePath  *string `json:"route_path"`
}

// SQLDraftView corresponds to OpenAPI AssistantSqlDraft.
type SQLDraftView struct {
	ID                      string   `json:"id"`
	SessionID               string   `json:"session_id"`
	MessageID               string   `json:"message_id"`
	Revision                int64    `json:"revision"`
	DatasourceID            string   `json:"datasource_id"`
	DatasourceName          *string  `json:"datasource_name,omitempty"`
	FlowID                  *string  `json:"flow_id"`
	Engine                  string   `json:"engine"`
	SchemaName              string   `json:"schema_name"`
	StatementKind           string   `json:"statement_kind"`
	RiskCategory            string   `json:"risk_category"`
	SQLText                 string   `json:"sql_text"`
	SQLHMAC                 string   `json:"sql_hmac"`
	Assumptions             []string `json:"assumptions"`
	NextSteps               []string `json:"next_steps"`
	SemanticWarnings        []string `json:"semantic_warnings"`
	ReferencedTables        []string `json:"referenced_tables"`
	SubmissionState         string   `json:"submission_state"`
	EphemeralRunID          *string  `json:"ephemeral_run_id"`
	SubmittedOrderID        *string  `json:"submitted_order_id"`
	SubmittedOrderDisplayNo *string  `json:"submitted_order_display_no"`
	Version                 int64    `json:"version"`
	CreatedAt               string   `json:"created_at"`
	UpdatedAt               string   `json:"updated_at"`
}

// MessageView corresponds to OpenAPI AssistantMessage.
type MessageView struct {
	ID          string         `json:"id"`
	SessionID   string         `json:"session_id"`
	Sequence    int64          `json:"sequence"`
	Role        string         `json:"role"`
	Capability  string         `json:"capability"`
	State       string         `json:"state"`
	Content     string         `json:"content"`
	Citations   []CitationView `json:"citations"`
	SQLDraft    *SQLDraftView  `json:"sql_draft"`
	FailureCode *int           `json:"failure_code"`
	CreatedAt   string         `json:"created_at"`
}

// SessionDetailView corresponds to OpenAPI AssistantSessionDetail.
type SessionDetailView struct {
	Session  SessionView    `json:"session"`
	Messages []MessageView  `json:"messages"`
	Drafts   []SQLDraftView `json:"drafts"`
}

// SendMessageResult corresponds to OpenAPI SendAssistantMessageResponse.
type SendMessageResult struct {
	Session          SessionView   `json:"session"`
	UserMessage      MessageView   `json:"user_message"`
	AssistantMessage MessageView   `json:"assistant_message"`
	SQLDraft         *SQLDraftView `json:"sql_draft"`
	ContextCompacted bool          `json:"context_compacted"`
}

// PrecheckDraftResult corresponds to OpenAPI PrecheckAssistantSqlDraftResponse.
type PrecheckDraftResult struct {
	Draft          SQLDraftView `json:"draft"`
	EphemeralRunID string       `json:"ephemeral_run_id"`
	ResultRef      string       `json:"result_ref"`
}
