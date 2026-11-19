package legacy

import "time"

// Explicit GORM models for target tables: TableName() + column tags map every column explicitly,
// without relying on GORM naming inference, embedding gorm.Model, or permitting any form of auto-migration.
// Nullable columns use pointers; zero-valued columns are populated by database defaults.

type UserModel struct {
	ID             string    `gorm:"column:id"`
	Username       string    `gorm:"column:username"`
	PasswordHash   *string   `gorm:"column:password_hash"`
	DisplayName    string    `gorm:"column:display_name"`
	Email          *string   `gorm:"column:email"`
	IsBuiltinAdmin bool      `gorm:"column:is_builtin_admin"`
	Role           string    `gorm:"column:role"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (UserModel) TableName() string { return "users" }

type IdentityProviderModel struct {
	ID                string    `gorm:"column:id"`
	ProviderKey       string    `gorm:"column:provider_key"`
	ProviderKind      string    `gorm:"column:provider_kind"`
	DisplayName       string    `gorm:"column:display_name"`
	Enabled           bool      `gorm:"column:enabled"`
	Configuration     string    `gorm:"column:configuration"`
	ConfigurationHash string    `gorm:"column:configuration_hash"`
	SecretCiphertext  []byte    `gorm:"column:secret_ciphertext"`
	SecretNonce       []byte    `gorm:"column:secret_nonce"`
	SecretKeyID       string    `gorm:"column:secret_key_id"`
	SecretAlgorithm   string    `gorm:"column:secret_algorithm"`
	CreatedByUserID   string    `gorm:"column:created_by_user_id"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (IdentityProviderModel) TableName() string { return "identity_providers" }

type NotificationChannelModel struct {
	ID               string    `gorm:"column:id"`
	Kind             string    `gorm:"column:kind"`
	Name             string    `gorm:"column:name"`
	Enabled          bool      `gorm:"column:enabled"`
	Configuration    string    `gorm:"column:configuration"`
	SecretCiphertext []byte    `gorm:"column:secret_ciphertext"`
	SecretNonce      []byte    `gorm:"column:secret_nonce"`
	SecretKeyID      string    `gorm:"column:secret_key_id"`
	SecretAlgorithm  string    `gorm:"column:secret_algorithm"`
	CreatedByUserID  string    `gorm:"column:created_by_user_id"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (NotificationChannelModel) TableName() string { return "notification_channels" }

type SystemSettingModel struct {
	Namespace       string    `gorm:"column:namespace"`
	SchemaVersion   int       `gorm:"column:schema_version"`
	Settings        string    `gorm:"column:settings"`
	UpdatedByUserID string    `gorm:"column:updated_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (SystemSettingModel) TableName() string { return "system_settings" }

type AIProviderModel struct {
	ID                  string    `gorm:"column:id"`
	Name                string    `gorm:"column:name"`
	ProviderKind        string    `gorm:"column:provider_kind"`
	BaseURL             string    `gorm:"column:base_url"`
	ModelName           string    `gorm:"column:model_name"`
	Enabled             bool      `gorm:"column:enabled"`
	SelectionPriority   int16     `gorm:"column:selection_priority"`
	PrivacyContractHash string    `gorm:"column:privacy_contract_hash"`
	OutputSchemaHash    string    `gorm:"column:output_schema_hash"`
	Configuration       string    `gorm:"column:configuration"`
	SecretCiphertext    []byte    `gorm:"column:secret_ciphertext"`
	SecretNonce         []byte    `gorm:"column:secret_nonce"`
	SecretKeyID         string    `gorm:"column:secret_key_id"`
	SecretAlgorithm     string    `gorm:"column:secret_algorithm"`
	CreatedByUserID     string    `gorm:"column:created_by_user_id"`
	CreatedAt           time.Time `gorm:"column:created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at"`
}

func (AIProviderModel) TableName() string { return "ai_providers" }

type AnnouncementRevisionModel struct {
	ID                        string    `gorm:"column:id"`
	RevisionNumber            int64     `gorm:"column:revision_number"`
	Title                     string    `gorm:"column:title"`
	MarkdownSource            string    `gorm:"column:markdown_source"`
	SanitizedHTML             string    `gorm:"column:sanitized_html"`
	ContentSha256             string    `gorm:"column:content_sha256"`
	SanitizerPolicyVersion    string    `gorm:"column:sanitizer_policy_version"`
	CreatedByUserIDSnapshot   string    `gorm:"column:created_by_user_id_snapshot"`
	CreatedByUsernameSnapshot string    `gorm:"column:created_by_username_snapshot"`
	CreatedAt                 time.Time `gorm:"column:created_at"`
}

func (AnnouncementRevisionModel) TableName() string { return "announcement_revisions" }

type DatasourceModel struct {
	ID                string    `gorm:"column:id"`
	Name              string    `gorm:"column:name"`
	Engine            string    `gorm:"column:engine"`
	CompatibilityMode string    `gorm:"column:compatibility_mode"`
	DeploymentKind    string    `gorm:"column:deployment_kind"`
	Host              string    `gorm:"column:host"`
	Port              int       `gorm:"column:port"`
	DatabaseName      *string   `gorm:"column:database_name"`
	VersionConstraint string    `gorm:"column:version_constraint"`
	Enabled           bool      `gorm:"column:enabled"`
	Metadata          string    `gorm:"column:metadata"`
	CreatedByUserID   string    `gorm:"column:created_by_user_id"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (DatasourceModel) TableName() string { return "datasources" }

type DatasourceCredentialModel struct {
	ID                   string    `gorm:"column:id"`
	DatasourceID         string    `gorm:"column:datasource_id"`
	Purpose              string    `gorm:"column:purpose"`
	CredentialCiphertext []byte    `gorm:"column:credential_ciphertext"`
	CredentialNonce      []byte    `gorm:"column:credential_nonce"`
	CredentialKeyID      string    `gorm:"column:credential_key_id"`
	CredentialAlgorithm  string    `gorm:"column:credential_algorithm"`
	Enabled              bool      `gorm:"column:enabled"`
	CreatedAt            time.Time `gorm:"column:created_at"`
	UpdatedAt            time.Time `gorm:"column:updated_at"`
}

func (DatasourceCredentialModel) TableName() string { return "datasource_credentials" }

type DatasourceTLSMaterialModel struct {
	ID                 string    `gorm:"column:id"`
	DatasourceID       string    `gorm:"column:datasource_id"`
	Purpose            string    `gorm:"column:purpose"`
	VerifyServer       bool      `gorm:"column:verify_server"`
	ServerName         *string   `gorm:"column:server_name"`
	MaterialCiphertext []byte    `gorm:"column:material_ciphertext"`
	MaterialNonce      []byte    `gorm:"column:material_nonce"`
	MaterialKeyID      string    `gorm:"column:material_key_id"`
	MaterialAlgorithm  string    `gorm:"column:material_algorithm"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

func (DatasourceTLSMaterialModel) TableName() string { return "datasource_tls_materials" }

type PermissionGroupModel struct {
	ID              string    `gorm:"column:id"`
	Name            string    `gorm:"column:name"`
	Enabled         bool      `gorm:"column:enabled"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (PermissionGroupModel) TableName() string { return "permission_groups" }

type PermissionGroupMemberModel struct {
	PermissionGroupID string `gorm:"column:permission_group_id"`
	UserID            string `gorm:"column:user_id"`
}

func (PermissionGroupMemberModel) TableName() string { return "permission_group_members" }

type ChangeOrderModel struct {
	ID                           string     `gorm:"column:id"`
	DisplayNumber                string     `gorm:"column:display_number"`
	SubmitterUserIDSnapshot      string     `gorm:"column:submitter_user_id_snapshot"`
	SubmitterUsernameSnapshot    string     `gorm:"column:submitter_username_snapshot"`
	SubmitterDisplayNameSnapshot string     `gorm:"column:submitter_display_name_snapshot"`
	Title                        string     `gorm:"column:title"`
	Description                  string     `gorm:"column:description"`
	State                        string     `gorm:"column:state"`
	CurrentStagePosition         *int       `gorm:"column:current_stage_position"`
	SQLHMAC                      string     `gorm:"column:sql_hmac"`
	SnapshotHash                 string     `gorm:"column:snapshot_hash"`
	ManuallyVerified             bool       `gorm:"column:manually_verified"`
	LegacySnapshotCompleteness   *string    `gorm:"column:legacy_snapshot_completeness"`
	SubmittedAt                  time.Time  `gorm:"column:submitted_at"`
	TerminalAt                   *time.Time `gorm:"column:terminal_at"`
}

func (ChangeOrderModel) TableName() string { return "change_orders" }

type OrderSQLPayloadModel struct {
	OrderID        string    `gorm:"column:order_id"`
	SQLCiphertext  []byte    `gorm:"column:sql_ciphertext"`
	SQLNonce       []byte    `gorm:"column:sql_nonce"`
	SQLKeyID       string    `gorm:"column:sql_key_id"`
	SQLAlgorithm   string    `gorm:"column:sql_algorithm"`
	PlaintextHMAC  string    `gorm:"column:plaintext_hmac"`
	SizeBytes      int32     `gorm:"column:size_bytes"`
	StatementCount int32     `gorm:"column:statement_count"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (OrderSQLPayloadModel) TableName() string { return "order_sql_payloads" }

type OrderFlowSnapshotModel struct {
	OrderID                string    `gorm:"column:order_id"`
	FlowSnapshotCiphertext []byte    `gorm:"column:flow_snapshot_ciphertext"`
	FlowSnapshotNonce      []byte    `gorm:"column:flow_snapshot_nonce"`
	FlowSnapshotKeyID      string    `gorm:"column:flow_snapshot_key_id"`
	FlowSnapshotAlgorithm  string    `gorm:"column:flow_snapshot_algorithm"`
	SnapshotHash           string    `gorm:"column:snapshot_hash"`
	CreatedAt              time.Time `gorm:"column:created_at"`
}

func (OrderFlowSnapshotModel) TableName() string { return "order_flow_snapshots" }

type ExecutionAttemptModel struct {
	ID                          string     `gorm:"column:id"`
	OrderID                     string     `gorm:"column:order_id"`
	StageID                     string     `gorm:"column:stage_id"`
	InitiatedByUserIDSnapshot   string     `gorm:"column:initiated_by_user_id_snapshot"`
	InitiatedByUsernameSnapshot string     `gorm:"column:initiated_by_username_snapshot"`
	ExecutionKind               string     `gorm:"column:execution_kind"`
	State                       string     `gorm:"column:state"`
	SendBoundary                string     `gorm:"column:send_boundary"`
	FenceToken                  int64      `gorm:"column:fence_token"`
	BusinessErrCode             *int       `gorm:"column:business_err_code"`
	FailureName                 *string    `gorm:"column:failure_name"`
	StartedAt                   *time.Time `gorm:"column:started_at"`
	FinishedAt                  *time.Time `gorm:"column:finished_at"`
	CreatedAt                   time.Time  `gorm:"column:created_at"`
}

func (ExecutionAttemptModel) TableName() string { return "execution_attempts" }

type ExecutionStatementModel struct {
	ID                    string     `gorm:"column:id"`
	AttemptID             string     `gorm:"column:attempt_id"`
	Ordinal               int32      `gorm:"column:ordinal"`
	StatementKind         string     `gorm:"column:statement_kind"`
	State                 string     `gorm:"column:state"`
	AffectedRowCount      *int64     `gorm:"column:affected_row_count"`
	FinishedAt            *time.Time `gorm:"column:finished_at"`
	BusinessErrCode       *int       `gorm:"column:business_err_code"`
	FailureName           *string    `gorm:"column:failure_name"`
	ErrorDetailCiphertext []byte     `gorm:"column:error_detail_ciphertext"`
	ErrorNonce            []byte     `gorm:"column:error_nonce"`
	ErrorKeyID            *string    `gorm:"column:error_key_id"`
	ErrorAlgorithm        *string    `gorm:"column:error_algorithm"`
}

func (ExecutionStatementModel) TableName() string { return "execution_statements" }

type ExecutionScheduleModel struct {
	ID                          string    `gorm:"column:id"`
	OrderID                     string    `gorm:"column:order_id"`
	StageID                     string    `gorm:"column:stage_id"`
	ScheduledByUserIDSnapshot   string    `gorm:"column:scheduled_by_user_id_snapshot"`
	ScheduledByUsernameSnapshot string    `gorm:"column:scheduled_by_username_snapshot"`
	ScheduledFor                time.Time `gorm:"column:scheduled_for"`
	State                       string    `gorm:"column:state"`
	CreatedAt                   time.Time `gorm:"column:created_at"`
	UpdatedAt                   time.Time `gorm:"column:updated_at"`
}

func (ExecutionScheduleModel) TableName() string { return "execution_schedules" }

type OrderCommentModel struct {
	ID                     string    `gorm:"column:id"`
	OrderID                string    `gorm:"column:order_id"`
	AuthorUserIDSnapshot   string    `gorm:"column:author_user_id_snapshot"`
	AuthorUsernameSnapshot string    `gorm:"column:author_username_snapshot"`
	Content                string    `gorm:"column:content"`
	OccurredAt             time.Time `gorm:"column:occurred_at"`
}

func (OrderCommentModel) TableName() string { return "order_comments" }

type ApprovalDecisionModel struct {
	ID                       string    `gorm:"column:id"`
	OrderID                  string    `gorm:"column:order_id"`
	StageID                  string    `gorm:"column:stage_id"`
	StepID                   string    `gorm:"column:step_id"`
	ReviewerUserIDSnapshot   string    `gorm:"column:reviewer_user_id_snapshot"`
	ReviewerUsernameSnapshot string    `gorm:"column:reviewer_username_snapshot"`
	Decision                 string    `gorm:"column:decision"`
	Comment                  string    `gorm:"column:comment"`
	OccurredAt               time.Time `gorm:"column:occurred_at"`
}

func (ApprovalDecisionModel) TableName() string { return "approval_decisions" }

type DomainEventModel struct {
	ID            string    `gorm:"column:id"`
	EventType     string    `gorm:"column:event_type"`
	Subject       string    `gorm:"column:subject"`
	Sequence      int64     `gorm:"column:sequence"`
	CorrelationID string    `gorm:"column:correlation_id"`
	Actor         string    `gorm:"column:actor"`
	EventData     string    `gorm:"column:event_data"`
	OccurredAt    time.Time `gorm:"column:occurred_at"`
}

func (DomainEventModel) TableName() string { return "domain_events" }

type QueryAccessRequestModel struct {
	ID                           string     `gorm:"column:id"`
	RequesterUserIDSnapshot      string     `gorm:"column:requester_user_id_snapshot"`
	RequesterUsernameSnapshot    string     `gorm:"column:requester_username_snapshot"`
	RequesterDisplayNameSnapshot string     `gorm:"column:requester_display_name_snapshot"`
	FlowIDSnapshot               string     `gorm:"column:flow_id_snapshot"`
	FlowNameSnapshot             string     `gorm:"column:flow_name_snapshot"`
	FlowSnapshotCiphertext       []byte     `gorm:"column:flow_snapshot_ciphertext"`
	FlowSnapshotNonce            []byte     `gorm:"column:flow_snapshot_nonce"`
	FlowSnapshotKeyID            string     `gorm:"column:flow_snapshot_key_id"`
	FlowSnapshotAlgorithm        string     `gorm:"column:flow_snapshot_algorithm"`
	SnapshotHash                 string     `gorm:"column:snapshot_hash"`
	State                        string     `gorm:"column:state"`
	Reason                       string     `gorm:"column:reason"`
	RequestedUntil               time.Time  `gorm:"column:requested_until"`
	CreatedAt                    time.Time  `gorm:"column:created_at"`
	TerminalAt                   *time.Time `gorm:"column:terminal_at"`
}

func (QueryAccessRequestModel) TableName() string { return "query_access_requests" }

type QueryAccessRequestDatasourceModel struct {
	RequestID    string `gorm:"column:request_id"`
	DatasourceID string `gorm:"column:datasource_id"`
	CanQuery     bool   `gorm:"column:can_query"`
	CanExport    bool   `gorm:"column:can_export"`
}

func (QueryAccessRequestDatasourceModel) TableName() string {
	return "query_access_request_datasources"
}

type QueryGrantModel struct {
	ID                        string     `gorm:"column:id"`
	QueryAccessRequestID      string     `gorm:"column:query_access_request_id"`
	RequesterUserIDSnapshot   string     `gorm:"column:requester_user_id_snapshot"`
	RequesterUsernameSnapshot string     `gorm:"column:requester_username_snapshot"`
	State                     string     `gorm:"column:state"`
	ExpiresAt                 time.Time  `gorm:"column:expires_at"`
	CreatedAt                 time.Time  `gorm:"column:created_at"`
	TerminalAt                *time.Time `gorm:"column:terminal_at"`
	TerminalReason            *string    `gorm:"column:terminal_reason"`
}

func (QueryGrantModel) TableName() string { return "query_grants" }

type QueryExecutionModel struct {
	ID                     string     `gorm:"column:id"`
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
	TimeoutMs              int32      `gorm:"column:timeout_ms"`
	ElapsedMs              *int32     `gorm:"column:elapsed_ms"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
	FinishedAt             *time.Time `gorm:"column:finished_at"`
}

func (QueryExecutionModel) TableName() string { return "query_executions" }

type RuleSetModel struct {
	ID              string    `gorm:"column:id"`
	Name            string    `gorm:"column:name"`
	Enabled         bool      `gorm:"column:enabled"`
	Rules           string    `gorm:"column:rules"`
	ConfigHash      string    `gorm:"column:config_hash"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (RuleSetModel) TableName() string { return "rule_sets" }

type FlowModel struct {
	ID              string    `gorm:"column:id"`
	Name            string    `gorm:"column:name"`
	FlowType        string    `gorm:"column:flow_type"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (FlowModel) TableName() string { return "flows" }

type FlowStageModel struct {
	ID                      string `gorm:"column:id"`
	FlowID                  string `gorm:"column:flow_id"`
	Position                int32  `gorm:"column:position"`
	DatasourceID            string `gorm:"column:datasource_id"`
	SchemaMappingCiphertext []byte `gorm:"column:schema_mapping_ciphertext"`
	SchemaMappingNonce      []byte `gorm:"column:schema_mapping_nonce"`
	SchemaMappingKeyID      string `gorm:"column:schema_mapping_key_id"`
	SchemaMappingAlgorithm  string `gorm:"column:schema_mapping_algorithm"`
}

func (FlowStageModel) TableName() string { return "flow_stages" }

type ChangeDraftModel struct {
	ID              string    `gorm:"column:id"`
	OwnerUserID     string    `gorm:"column:owner_user_id"`
	FlowID          string    `gorm:"column:flow_id"`
	FlowVersionSeen int64     `gorm:"column:flow_version_seen"`
	Title           string    `gorm:"column:title"`
	Description     string    `gorm:"column:description"`
	Revision        int64     `gorm:"column:revision"`
	State           string    `gorm:"column:state"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (ChangeDraftModel) TableName() string { return "change_drafts" }

type DraftSQLPayloadModel struct {
	DraftID        string    `gorm:"column:draft_id"`
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

type ReviewRunModel struct {
	ID                            string     `gorm:"column:id"`
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
	CreatedAt                     time.Time  `gorm:"column:created_at"`
	StartedAt                     *time.Time `gorm:"column:started_at"`
	FinishedAt                    *time.Time `gorm:"column:finished_at"`
}

func (ReviewRunModel) TableName() string { return "review_runs" }

type StageReviewSnapshotModel struct {
	ID                            string     `gorm:"column:id"`
	ReviewRunID                   string     `gorm:"column:review_run_id"`
	StagePosition                 int32      `gorm:"column:stage_position"`
	DatasourceID                  string     `gorm:"column:datasource_id"`
	DatasourceIdentityFingerprint string     `gorm:"column:datasource_identity_fingerprint"`
	State                         string     `gorm:"column:state"`
	HighestSeverity               string     `gorm:"column:highest_severity"`
	GatePassed                    bool       `gorm:"column:gate_passed"`
	NormalizedContextCiphertext   []byte     `gorm:"column:normalized_context_ciphertext"`
	NormalizedContextNonce        []byte     `gorm:"column:normalized_context_nonce"`
	NormalizedContextKeyID        string     `gorm:"column:normalized_context_key_id"`
	NormalizedContextAlgorithm    string     `gorm:"column:normalized_context_algorithm"`
	SnapshotHash                  string     `gorm:"column:snapshot_hash"`
	CreatedAt                     time.Time  `gorm:"column:created_at"`
	FinishedAt                    *time.Time `gorm:"column:finished_at"`
}

func (StageReviewSnapshotModel) TableName() string { return "stage_review_snapshots" }

type OrderStageModel struct {
	ID                            string     `gorm:"column:id"`
	OrderID                       string     `gorm:"column:order_id"`
	Position                      int32      `gorm:"column:position"`
	DatasourceID                  string     `gorm:"column:datasource_id"`
	DatasourceNameSnapshot        string     `gorm:"column:datasource_name_snapshot"`
	DatasourceEngineSnapshot      string     `gorm:"column:datasource_engine_snapshot"`
	DatasourceIdentityFingerprint string     `gorm:"column:datasource_identity_fingerprint"`
	StageReviewSnapshotID         string     `gorm:"column:stage_review_snapshot_id"`
	ReviewSnapshotHash            string     `gorm:"column:review_snapshot_hash"`
	ReviewGatePassed              bool       `gorm:"column:review_gate_passed"`
	TargetSnapshotCiphertext      []byte     `gorm:"column:target_snapshot_ciphertext"`
	TargetSnapshotNonce           []byte     `gorm:"column:target_snapshot_nonce"`
	TargetSnapshotKeyID           string     `gorm:"column:target_snapshot_key_id"`
	TargetSnapshotAlgorithm       string     `gorm:"column:target_snapshot_algorithm"`
	State                         string     `gorm:"column:state"`
	ActivatedAt                   *time.Time `gorm:"column:activated_at"`
	TerminalAt                    *time.Time `gorm:"column:terminal_at"`
}

func (OrderStageModel) TableName() string { return "order_stages" }

type QuerySessionModel struct {
	ID                       string     `gorm:"column:id"`
	UserIDSnapshot           string     `gorm:"column:user_id_snapshot"`
	UsernameSnapshot         string     `gorm:"column:username_snapshot"`
	FlowIDSnapshot           string     `gorm:"column:flow_id_snapshot"`
	FlowNameSnapshot         string     `gorm:"column:flow_name_snapshot"`
	GrantID                  string     `gorm:"column:grant_id"`
	ApprovalRequiredSnapshot bool       `gorm:"column:approval_required_snapshot"`
	State                    string     `gorm:"column:state"`
	CreatedAt                time.Time  `gorm:"column:created_at"`
	LastSeenAt               time.Time  `gorm:"column:last_seen_at"`
	ClosedAt                 *time.Time `gorm:"column:closed_at"`
	CloseReason              *string    `gorm:"column:close_reason"`
}

func (QuerySessionModel) TableName() string { return "query_sessions" }

// jsonb columns are represented as pre-serialized strings on the models;
// all legacy keys entering jsonb are validated against a whitelist, and unknown keys are quarantined per contract.
