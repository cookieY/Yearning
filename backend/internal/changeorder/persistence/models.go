// Package persistence provides GORM persistence models for the ChangeOrder domain.
// Model names match gorm-model-registry.json (domain=changeorder); column tags
// correspond column-by-column to db/migrations/000002_change_review.up.sql and
// postgresql-column-catalog; no embedded gorm.Model, no json tags, and no automatic
// Association saving.
// Raw SQL and snapshot ciphertexts only store AEAD envelopes; models do not implement
// any plaintext output interface.
package persistence

import "time"

// ChangeOrderModel maps to change_orders (mutable_aggregate; optimistic locking version).
type ChangeOrderModel struct {
	ID                           string  `gorm:"column:id;primaryKey"`
	DisplayNumber                string  `gorm:"column:display_number"`
	SubmitterUserIDSnapshot      string  `gorm:"column:submitter_user_id_snapshot"`
	SubmitterUsernameSnapshot    string  `gorm:"column:submitter_username_snapshot"`
	SubmitterDisplayNameSnapshot string  `gorm:"column:submitter_display_name_snapshot"`
	Title                        string  `gorm:"column:title"`
	Description                  string  `gorm:"column:description"`
	State                        string  `gorm:"column:state"`
	CurrentStagePosition         *int32  `gorm:"column:current_stage_position"`
	SQLHMAC                      string  `gorm:"column:sql_hmac"`
	SnapshotHash                 string  `gorm:"column:snapshot_hash"`
	ManuallyVerified             bool    `gorm:"column:manually_verified"`
	LegacySnapshotCompleteness   *string `gorm:"column:legacy_snapshot_completeness"`
	// Submitter's expected execution time (§18.56 Option B): frozen from draft on submission;
	// advisory value for display and executor prefill; does not participate in snapshot_hash
	// and does not constitute execution authorization.
	RequestedScheduledFor *time.Time `gorm:"column:requested_scheduled_for"`
	RollbackEnabled       bool       `gorm:"column:rollback_enabled"`
	TicketType            string     `gorm:"column:ticket_type"`
	SourceOrderID         *string    `gorm:"column:source_order_id"`
	RollbackEvidenceID    *string    `gorm:"column:rollback_evidence_id"`
	RollbackStatus        string     `gorm:"column:rollback_status"`
	Version               int64      `gorm:"column:version"`
	SubmittedAt           time.Time  `gorm:"column:submitted_at"`
	TerminalAt            *time.Time `gorm:"column:terminal_at"`
}

func (ChangeOrderModel) TableName() string { return "change_orders" }

// OrderFlowSnapshotModel maps to order_flow_snapshots (insert_only;
// complete flow structure frozen at submission, immutable per W004).
type OrderFlowSnapshotModel struct {
	OrderID                string    `gorm:"column:order_id;primaryKey"`
	FlowSnapshotCiphertext []byte    `gorm:"column:flow_snapshot_ciphertext"`
	FlowSnapshotNonce      []byte    `gorm:"column:flow_snapshot_nonce"`
	FlowSnapshotKeyID      string    `gorm:"column:flow_snapshot_key_id"`
	FlowSnapshotAlgorithm  string    `gorm:"column:flow_snapshot_algorithm"`
	SnapshotHash           string    `gorm:"column:snapshot_hash"`
	CreatedAt              time.Time `gorm:"column:created_at"`
}

func (OrderFlowSnapshotModel) TableName() string { return "order_flow_snapshots" }

// OrderSQLPayloadModel maps to order_sql_payloads (insert_only;
// work order SQL envelope + plaintext HMAC, Revision is immutable after submission).
type OrderSQLPayloadModel struct {
	OrderID        string    `gorm:"column:order_id;primaryKey"`
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

// OrderStageModel maps to order_stages (mutable_aggregate;
// stage_review_snapshot_id directly binds the accepted stage review snapshot).
type OrderStageModel struct {
	ID                            string     `gorm:"column:id;primaryKey"`
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
	Version                       int64      `gorm:"column:version"`
	ActivatedAt                   *time.Time `gorm:"column:activated_at"`
	TerminalAt                    *time.Time `gorm:"column:terminal_at"`
}

func (OrderStageModel) TableName() string { return "order_stages" }

// OrderApprovalStepModel maps to order_approval_steps
// (mutable_aggregate; serial approval step, unique per order_one_active_approval_step).
type OrderApprovalStepModel struct {
	ID        string     `gorm:"column:id;primaryKey"`
	StageID   string     `gorm:"column:stage_id"`
	Position  int32      `gorm:"column:position"`
	State     string     `gorm:"column:state"`
	DecidedAt *time.Time `gorm:"column:decided_at"`
}

func (OrderApprovalStepModel) TableName() string { return "order_approval_steps" }

// OrderApprovalActorModel maps to order_approval_actors
// (insert_only; frozen candidate approver snapshot, evaluation surface for W008 deletion impact).
type OrderApprovalActorModel struct {
	StepID                 string `gorm:"column:step_id;primaryKey"`
	ReviewerUserIDSnapshot string `gorm:"column:reviewer_user_id_snapshot;primaryKey"`
	UsernameSnapshot       string `gorm:"column:username_snapshot"`
	DisplayNameSnapshot    string `gorm:"column:display_name_snapshot"`
}

func (OrderApprovalActorModel) TableName() string { return "order_approval_actors" }

// ApprovalDecisionModel maps to approval_decisions
// (append_only; approval_step_single_effective_decision guarantees
// exactly one effective decision per step — first peer to decide wins, W003).
type ApprovalDecisionModel struct {
	ID                     string    `gorm:"column:id;primaryKey"`
	OrderID                string    `gorm:"column:order_id"`
	StageID                string    `gorm:"column:stage_id"`
	StepID                 string    `gorm:"column:step_id"`
	ReviewerUserIDSnapshot string    `gorm:"column:reviewer_user_id_snapshot"`
	ReviewerUsername       string    `gorm:"column:reviewer_username_snapshot"`
	Decision               string    `gorm:"column:decision"`
	Comment                string    `gorm:"column:comment"`
	OccurredAt             time.Time `gorm:"column:occurred_at"`
}

func (ApprovalDecisionModel) TableName() string { return "approval_decisions" }

// OrderExecutionActorModel maps to order_execution_actors
// (insert_only; frozen candidate executor snapshot; approval never auto-executes, W006).
type OrderExecutionActorModel struct {
	StageID                string `gorm:"column:stage_id;primaryKey"`
	ExecutorUserIDSnapshot string `gorm:"column:executor_user_id_snapshot;primaryKey"`
	UsernameSnapshot       string `gorm:"column:username_snapshot"`
	DisplayNameSnapshot    string `gorm:"column:display_name_snapshot"`
	// F12 #006: contract execution_actors references the full User shape; snapshot columns are frozen on submission.
	IsBuiltinAdminSnapshot bool      `gorm:"column:is_builtin_admin_snapshot"`
	UserVersionSnapshot    int64     `gorm:"column:user_version_snapshot"`
	UserCreatedAtSnapshot  time.Time `gorm:"column:user_created_at_snapshot"`
	UserUpdatedAtSnapshot  time.Time `gorm:"column:user_updated_at_snapshot"`
}

func (OrderExecutionActorModel) TableName() string { return "order_execution_actors" }

// OrderCommentModel maps to order_comments (append_only; permanently retained per S004).
type OrderCommentModel struct {
	ID                     string    `gorm:"column:id;primaryKey"`
	OrderID                string    `gorm:"column:order_id"`
	AuthorUserIDSnapshot   string    `gorm:"column:author_user_id_snapshot"`
	AuthorUsernameSnapshot string    `gorm:"column:author_username_snapshot"`
	Content                string    `gorm:"column:content"`
	OccurredAt             time.Time `gorm:"column:occurred_at"`
}

func (OrderCommentModel) TableName() string { return "order_comments" }
