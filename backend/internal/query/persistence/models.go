// models.go defines Query domain GORM models (gorm-model-registry: query/repository/postgres;
// core schema frozen by 000003_query_audit_jobs, masking rules added by 000012).
package persistence

import "time"

// FlowMaskingRuleModel maps to flow_masking_rules. SensitiveColumns stores a
// normalized JSON array; validated by the application layer and re-validated on read to fail closed on corruption.
type FlowMaskingRuleModel struct {
	FlowID           string    `gorm:"column:flow_id;primaryKey"`
	DatasourceID     string    `gorm:"column:datasource_id;primaryKey"`
	SensitiveColumns []byte    `gorm:"column:sensitive_columns;type:jsonb"`
	Version          int64     `gorm:"column:version"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (FlowMaskingRuleModel) TableName() string { return "flow_masking_rules" }

// QueryAccessRequestModel maps to query_access_requests.
type QueryAccessRequestModel struct {
	ID                     string     `gorm:"column:id;primaryKey"`
	RequesterUserID        string     `gorm:"column:requester_user_id_snapshot"`
	RequesterUsername      string     `gorm:"column:requester_username_snapshot"`
	RequesterDisplayName   string     `gorm:"column:requester_display_name_snapshot"`
	FlowID                 string     `gorm:"column:flow_id_snapshot"`
	FlowName               string     `gorm:"column:flow_name_snapshot"`
	FlowSnapshotCiphertext []byte     `gorm:"column:flow_snapshot_ciphertext"`
	FlowSnapshotNonce      []byte     `gorm:"column:flow_snapshot_nonce"`
	FlowSnapshotKeyID      string     `gorm:"column:flow_snapshot_key_id"`
	FlowSnapshotAlgorithm  string     `gorm:"column:flow_snapshot_algorithm"`
	SnapshotHash           string     `gorm:"column:snapshot_hash"`
	State                  string     `gorm:"column:state"`
	Reason                 string     `gorm:"column:reason"`
	RequestedUntil         time.Time  `gorm:"column:requested_until"`
	Version                int64      `gorm:"column:version"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
	TerminalAt             *time.Time `gorm:"column:terminal_at"`
}

func (QueryAccessRequestModel) TableName() string { return "query_access_requests" }

// QueryAccessRequestDatasourceModel maps to query_access_request_datasources.
type QueryAccessRequestDatasourceModel struct {
	RequestID              string `gorm:"column:request_id"`
	DatasourceID           string `gorm:"column:datasource_id"`
	DatasourceNameSnapshot string `gorm:"column:datasource_name_snapshot"`
	DatasourceIdentityFP   string `gorm:"column:datasource_identity_fingerprint"`
	CanQuery               bool   `gorm:"column:can_query"`
	CanExport              bool   `gorm:"column:can_export"`
}

func (QueryAccessRequestDatasourceModel) TableName() string {
	return "query_access_request_datasources"
}

// QueryAccessApprovalStepModel maps to query_access_approval_steps.
type QueryAccessApprovalStepModel struct {
	ID        string     `gorm:"column:id;primaryKey"`
	RequestID string     `gorm:"column:request_id"`
	Position  int        `gorm:"column:position"`
	State     string     `gorm:"column:state"`
	DecidedAt *time.Time `gorm:"column:decided_at"`
}

func (QueryAccessApprovalStepModel) TableName() string {
	return "query_access_approval_steps"
}

// QueryAccessApprovalActorModel maps to query_access_approval_actors.
type QueryAccessApprovalActorModel struct {
	StepID              string `gorm:"column:step_id;primaryKey"`
	ReviewerUserID      string `gorm:"column:reviewer_user_id_snapshot;primaryKey"`
	UsernameSnapshot    string `gorm:"column:username_snapshot"`
	DisplayNameSnapshot string `gorm:"column:display_name_snapshot"`
}

func (QueryAccessApprovalActorModel) TableName() string {
	return "query_access_approval_actors"
}

// QueryAccessDecisionModel maps to query_access_decisions.
type QueryAccessDecisionModel struct {
	ID               string    `gorm:"column:id;primaryKey"`
	RequestID        string    `gorm:"column:request_id"`
	StepID           string    `gorm:"column:step_id"`
	ReviewerUserID   string    `gorm:"column:reviewer_user_id_snapshot"`
	ReviewerUsername string    `gorm:"column:reviewer_username_snapshot"`
	Decision         string    `gorm:"column:decision"`
	Comment          string    `gorm:"column:comment"`
	OccurredAt       time.Time `gorm:"column:occurred_at"`
}

func (QueryAccessDecisionModel) TableName() string { return "query_access_decisions" }

// QueryGrantModel maps to query_grants.
type QueryGrantModel struct {
	ID                   string     `gorm:"column:id;primaryKey"`
	QueryAccessRequestID string     `gorm:"column:query_access_request_id"`
	RequesterUserID      string     `gorm:"column:requester_user_id_snapshot"`
	RequesterUsername    string     `gorm:"column:requester_username_snapshot"`
	State                string     `gorm:"column:state"`
	ExpiresAt            time.Time  `gorm:"column:expires_at"`
	RevokedByUserID      *string    `gorm:"column:revoked_by_user_id_snapshot"`
	RevokedByUsername    *string    `gorm:"column:revoked_by_username_snapshot"`
	RevokeReason         *string    `gorm:"column:revoke_reason"`
	RevokedAt            *time.Time `gorm:"column:revoked_at"`
	TerminalAt           *time.Time `gorm:"column:terminal_at"`
	TerminalReason       *string    `gorm:"column:terminal_reason"`
	Version              int64      `gorm:"column:version"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
}

func (QueryGrantModel) TableName() string { return "query_grants" }

// QueryGrantCapabilityModel maps to query_grant_capabilities.
type QueryGrantCapabilityModel struct {
	GrantID                string `gorm:"column:grant_id"`
	DatasourceID           string `gorm:"column:datasource_id"`
	DatasourceNameSnapshot string `gorm:"column:datasource_name_snapshot"`
	DatasourceIdentityFP   string `gorm:"column:datasource_identity_fingerprint"`
	CanQuery               bool   `gorm:"column:can_query"`
	CanExport              bool   `gorm:"column:can_export"`
}

func (QueryGrantCapabilityModel) TableName() string { return "query_grant_capabilities" }

// QuerySessionModel maps to query_sessions.
type QuerySessionModel struct {
	ID               string     `gorm:"column:id;primaryKey"`
	UserIDSnapshot   string     `gorm:"column:user_id_snapshot"`
	UsernameSnapshot string     `gorm:"column:username_snapshot"`
	FlowIDSnapshot   string     `gorm:"column:flow_id_snapshot"`
	FlowNameSnapshot string     `gorm:"column:flow_name_snapshot"`
	GrantID          *string    `gorm:"column:grant_id"`
	ApprovalRequired bool       `gorm:"column:approval_required_snapshot"`
	State            string     `gorm:"column:state"`
	Version          int64      `gorm:"column:version"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	LastSeenAt       time.Time  `gorm:"column:last_seen_at"`
	ClosedAt         *time.Time `gorm:"column:closed_at"`
	CloseReason      *string    `gorm:"column:close_reason"`
}

func (QuerySessionModel) TableName() string { return "query_sessions" }

// QuerySessionCapabilityModel maps to query_session_capabilities.
type QuerySessionCapabilityModel struct {
	SessionID              string `gorm:"column:session_id"`
	DatasourceID           string `gorm:"column:datasource_id"`
	DatasourceNameSnapshot string `gorm:"column:datasource_name_snapshot"`
	DatasourceIdentityFP   string `gorm:"column:datasource_identity_fingerprint"`
	State                  string `gorm:"column:state"`
	CanQuery               bool   `gorm:"column:can_query"`
	CanExport              bool   `gorm:"column:can_export"`
}

func (QuerySessionCapabilityModel) TableName() string { return "query_session_capabilities" }

// QueryExecutionModel maps to query_executions.
type QueryExecutionModel struct {
	ID                     string     `gorm:"column:id;primaryKey"`
	SessionID              string     `gorm:"column:session_id"`
	UserIDSnapshot         string     `gorm:"column:user_id_snapshot"`
	DatasourceID           string     `gorm:"column:datasource_id"`
	QueryPayloadCiphertext []byte     `gorm:"column:query_payload_ciphertext"`
	QueryPayloadNonce      []byte     `gorm:"column:query_payload_nonce"`
	QueryPayloadKeyID      string     `gorm:"column:query_payload_key_id"`
	QueryPayloadAlgorithm  string     `gorm:"column:query_payload_algorithm"`
	SQLHMAC                string     `gorm:"column:sql_hmac"`
	FingerprintHMAC        string     `gorm:"column:fingerprint_hmac"`
	State                  string     `gorm:"column:state"`
	TimeoutMS              int        `gorm:"column:timeout_ms"`
	ElapsedMS              *int       `gorm:"column:elapsed_ms"`
	StreamedRowCount       int64      `gorm:"column:streamed_row_count"`
	BusinessErrCode        *int       `gorm:"column:business_err_code"`
	FailureName            *string    `gorm:"column:failure_name"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
	FinishedAt             *time.Time `gorm:"column:finished_at"`
}

func (QueryExecutionModel) TableName() string { return "query_executions" }

// QueryCursorModel maps to query_cursors.
type QueryCursorModel struct {
	ID                    string     `gorm:"column:id;primaryKey"`
	ExecutionID           string     `gorm:"column:execution_id"`
	CursorTokenHash       string     `gorm:"column:cursor_token_hash"`
	OwnerInstanceID       string     `gorm:"column:owner_instance_id"`
	FenceToken            int64      `gorm:"column:fence_token"`
	Purpose               string     `gorm:"column:purpose"`
	CursorStateCiphertext []byte     `gorm:"column:cursor_state_ciphertext"`
	CursorStateNonce      []byte     `gorm:"column:cursor_state_nonce"`
	CursorStateKeyID      string     `gorm:"column:cursor_state_key_id"`
	CursorStateAlgorithm  string     `gorm:"column:cursor_state_algorithm"`
	NextPageNumber        int64      `gorm:"column:next_page_number"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	ExpiresAt             time.Time  `gorm:"column:expires_at"`
	AbsoluteExpiresAt     time.Time  `gorm:"column:absolute_expires_at"`
	ConsumedAt            *time.Time `gorm:"column:consumed_at"`
}

func (QueryCursorModel) TableName() string { return "query_cursors" }
