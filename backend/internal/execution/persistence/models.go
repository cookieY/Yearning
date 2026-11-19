// models.go Execution domain GORM models (gorm-model-registry: execution/repository/postgres;
// columns match db/migrations/000002 execution_* tables column-for-column).
package persistence

import "time"

// ExecutionAttemptModel maps to execution_attempts (mutable_aggregate;
// send_boundary and fence_token are the authoritative ADR-0003 send boundary and fencing tokens).
type ExecutionAttemptModel struct {
	ID                     string     `gorm:"column:id;primaryKey"`
	OrderID                string     `gorm:"column:order_id"`
	StageID                string     `gorm:"column:stage_id"`
	InitiatedByUserID      string     `gorm:"column:initiated_by_user_id_snapshot"`
	InitiatedByUsername    string     `gorm:"column:initiated_by_username_snapshot"`
	ExecutionKind          string     `gorm:"column:execution_kind"`
	State                  string     `gorm:"column:state"`
	SendBoundary           string     `gorm:"column:send_boundary"`
	FenceToken             int64      `gorm:"column:fence_token"`
	LeaseOwner             *string    `gorm:"column:lease_owner"`
	LeaseExpiresAt         *time.Time `gorm:"column:lease_expires_at"`
	StartedAt              *time.Time `gorm:"column:started_at"`
	FinishedAt             *time.Time `gorm:"column:finished_at"`
	BusinessErrCode        *int       `gorm:"column:business_err_code"`
	FailureName            *string    `gorm:"column:failure_name"`
	OSCToolVersion         *string    `gorm:"column:osc_tool_version"`
	OSCBinarySHA256        *string    `gorm:"column:osc_binary_sha256"`
	OSCPlanHash            *string    `gorm:"column:osc_plan_hash"`
	OSCPlanCiphertext      []byte     `gorm:"column:osc_plan_ciphertext"`
	OSCPlanNonce           []byte     `gorm:"column:osc_plan_nonce"`
	OSCPlanKeyID           *string    `gorm:"column:osc_plan_key_id"`
	OSCPlanAlgorithm       *string    `gorm:"column:osc_plan_algorithm"`
	OSCPhase               *string    `gorm:"column:osc_phase"`
	OSCProgressBasisPoints *int       `gorm:"column:osc_progress_basis_points"`
	OSCRowsCopied          *int64     `gorm:"column:osc_rows_copied"`
	OSCResidualState       *string    `gorm:"column:osc_residual_state"`
	OSCLastHeartbeatAt     *time.Time `gorm:"column:osc_last_heartbeat_at"`
	Version                int64      `gorm:"column:version"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
}

func (ExecutionAttemptModel) TableName() string { return "execution_attempts" }

// ExecutionStatementModel maps to execution_statements (mutable_aggregate;
// statement ledger stores only execution facts, never raw SQL — raw SQL is always derived from order_sql_payloads).
type ExecutionStatementModel struct {
	ID                    string     `gorm:"column:id;primaryKey"`
	AttemptID             string     `gorm:"column:attempt_id"`
	Ordinal               int32      `gorm:"column:ordinal"`
	StatementKind         string     `gorm:"column:statement_kind"`
	State                 string     `gorm:"column:state"`
	AffectedRowCount      *int64     `gorm:"column:affected_row_count"`
	StartedAt             *time.Time `gorm:"column:started_at"`
	FinishedAt            *time.Time `gorm:"column:finished_at"`
	BusinessErrCode       *int       `gorm:"column:business_err_code"`
	FailureName           *string    `gorm:"column:failure_name"`
	ErrorDetailCiphertext []byte     `gorm:"column:error_detail_ciphertext"`
	ErrorNonce            []byte     `gorm:"column:error_nonce"`
	ErrorKeyID            *string    `gorm:"column:error_key_id"`
	ErrorAlgorithm        *string    `gorm:"column:error_algorithm"`
}

func (ExecutionStatementModel) TableName() string { return "execution_statements" }

// ExecutionScheduleModel maps to execution_schedules (mutable_aggregate;
// E007 delayed execution belongs to B11; this package reads this table to verify no_live_schedule_or_attempt).
type ExecutionScheduleModel struct {
	ID                  string    `gorm:"column:id;primaryKey"`
	OrderID             string    `gorm:"column:order_id"`
	StageID             string    `gorm:"column:stage_id"`
	ScheduledByUserID   string    `gorm:"column:scheduled_by_user_id_snapshot"`
	ScheduledByUsername string    `gorm:"column:scheduled_by_username_snapshot"`
	ScheduledFor        time.Time `gorm:"column:scheduled_for"`
	State               string    `gorm:"column:state"`
	Version             int64     `gorm:"column:version"`
	CreatedAt           time.Time `gorm:"column:created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at"`
}

func (ExecutionScheduleModel) TableName() string { return "execution_schedules" }

// ExecutionVerificationModel maps to execution_verifications (append_only;
// manual verification facts never rewrite original Attempt history).
type ExecutionVerificationModel struct {
	ID                       string    `gorm:"column:id;primaryKey"`
	AttemptID                string    `gorm:"column:attempt_id"`
	VerifiedByUserID         string    `gorm:"column:verified_by_user_id_snapshot"`
	VerifiedByUsername       string    `gorm:"column:verified_by_username_snapshot"`
	Result                   string    `gorm:"column:result"`
	ReasonCiphertext         []byte    `gorm:"column:reason_ciphertext"`
	ReasonNonce              []byte    `gorm:"column:reason_nonce"`
	ReasonKeyID              string    `gorm:"column:reason_key_id"`
	ReasonAlgorithm          string    `gorm:"column:reason_algorithm"`
	EvidenceBundleCiphertext []byte    `gorm:"column:evidence_bundle_ciphertext"`
	EvidenceNonce            []byte    `gorm:"column:evidence_nonce"`
	EvidenceKeyID            string    `gorm:"column:evidence_key_id"`
	EvidenceAlgorithm        string    `gorm:"column:evidence_algorithm"`
	OccurredAt               time.Time `gorm:"column:occurred_at"`
}

func (ExecutionVerificationModel) TableName() string { return "execution_verifications" }

// OrderRollbackEvidenceModel maps to order_rollback_evidences (mutable_aggregate).
// Stores AEAD envelopes and status for DML/DDL rollback evidence captured by protected execution attempts, containing no plaintext business rows or rollback SQL.
type OrderRollbackEvidenceModel struct {
	ID                        string    `gorm:"column:id;primaryKey"`
	OrderID                   string    `gorm:"column:order_id"`
	StageID                   string    `gorm:"column:stage_id"`
	AttemptID                 string    `gorm:"column:attempt_id"`
	DatasourceID              string    `gorm:"column:datasource_id"`
	DatabaseEngine            string    `gorm:"column:database_engine"`
	DatabaseVersion           string    `gorm:"column:database_version"`
	SchemaName                string    `gorm:"column:schema_name"`
	OperationType             string    `gorm:"column:operation_type"`
	RollbackStatus            string    `gorm:"column:rollback_status"`
	UnsupportedReason         *string   `gorm:"column:unsupported_reason"`
	ConflictReason            *string   `gorm:"column:conflict_reason"`
	AffectedRows              int64     `gorm:"column:affected_rows"`
	RollbackPayloadCiphertext []byte    `gorm:"column:rollback_payload_ciphertext"`
	RollbackPayloadNonce      []byte    `gorm:"column:rollback_payload_nonce"`
	RollbackPayloadKeyID      string    `gorm:"column:rollback_payload_key_id"`
	RollbackPayloadAlgorithm  string    `gorm:"column:rollback_payload_algorithm"`
	RollbackSQLHMAC           string    `gorm:"column:rollback_sql_hmac"`
	CreatedAt                 time.Time `gorm:"column:created_at"`
	UpdatedAt                 time.Time `gorm:"column:updated_at"`
}

func (OrderRollbackEvidenceModel) TableName() string { return "order_rollback_evidences" }
