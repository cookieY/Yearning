// Package rollback implements Yearning's unified MySQL and PostgreSQL SQL rollback engine
// (DML transactional snapshot/RETURNING capture, DDL schema snapshot and inverse DDL generation,
// AEAD evidence bundle encryption/decryption, and pre-rollback conflict verification).
// Does not rely on MySQL Binlog, PostgreSQL WAL, business table triggers, or external agents.
package rollback

import (
	"github.com/cookieY/YearningX/backend/internal/database"
)

// Ticket type enum (change_drafts.ticket_type / change_orders.ticket_type).
const (
	TicketTypeNormal   = "normal"
	TicketTypeRollback = "rollback"
)

// Rollback status enum (change_orders.rollback_status / order_rollback_evidences.rollback_status).
const (
	StatusNotRequested       = "not_requested"
	StatusPending            = "pending"
	StatusAvailable          = "available"
	StatusUnsupported        = "unsupported"
	StatusEvidenceIncomplete = "evidence_incomplete"
	StatusConflicted         = "conflicted"
	StatusRollbackInProgress = "rollback_in_progress"
	StatusRolledBack         = "rolled_back"
	StatusRollbackFailed     = "rollback_failed"
)

// Operation category enum (order_rollback_evidences.operation_type).
const (
	OperationTypeDML = "dml"
	OperationTypeDDL = "ddl"
)

// Capture strategy identifier.
const (
	StrategyMySQLDMLTxSnapshot    = "mysql_dml_tx_snapshot"
	StrategyPG18DMLReturning      = "pg18_dml_returning_old_new"
	StrategyPGDMLTxReturning      = "pg_dml_tx_returning"
	StrategyMySQLDDLSchemaDiff    = "mysql_ddl_schema_diff"
	StrategyPostgresDDLSchemaDiff = "pg_ddl_schema_diff"
)

// Default capture safety limits (preventing oversized DML from exhausting memory when rollback is enabled; fails closed and rolls back the original transaction when exceeded).
const (
	DefaultMaxCapturedRowsPerStatement = 100000
	DefaultMaxCapturedRowsPerOrder     = 100000
)

// StatementKind defines the supported fine-grained reversible SQL operation kinds.
type StatementKind string

const (
	KindInsert         StatementKind = "INSERT"
	KindUpdate         StatementKind = "UPDATE"
	KindDelete         StatementKind = "DELETE"
	KindCreateTable    StatementKind = "CREATE_TABLE"
	KindDropTable      StatementKind = "DROP_TABLE"
	KindAddColumn      StatementKind = "ADD_COLUMN"
	KindDropColumn     StatementKind = "DROP_COLUMN"
	KindRenameColumn   StatementKind = "RENAME_COLUMN"
	KindRenameTable    StatementKind = "RENAME_TABLE"
	KindAddIndex       StatementKind = "ADD_INDEX"
	KindDropIndex      StatementKind = "DROP_INDEX"
	KindModifyColumn   StatementKind = "MODIFY_COLUMN"
	KindAddConstraint  StatementKind = "ADD_CONSTRAINT"
	KindDropConstraint StatementKind = "DROP_CONSTRAINT"
)

// StatementEvidence holds the rollback forensic facts and inverse SQL for a single executed statement.
type StatementEvidence struct {
	Ordinal            int                             `json:"ordinal"`
	OriginalSQL        string                          `json:"original_sql"`
	Kind               StatementKind                   `json:"kind"`
	SchemaName         string                          `json:"schema_name"`
	TableName          string                          `json:"table_name"`
	PostTableName      string                          `json:"post_table_name,omitempty"`
	ColumnName         string                          `json:"column_name,omitempty"`
	PostColumnName     string                          `json:"post_column_name,omitempty"`
	IndexName          string                          `json:"index_name,omitempty"`
	ConstraintName     string                          `json:"constraint_name,omitempty"`
	IdentityKeyColumns []string                        `json:"identity_key_columns,omitempty"`
	BeforeRows         []database.RollbackRow          `json:"before_rows,omitempty"`
	AfterRows          []database.RollbackRow          `json:"after_rows,omitempty"`
	SchemaBefore       *database.TableRollbackMetadata `json:"schema_before,omitempty"`
	SchemaAfter        *database.TableRollbackMetadata `json:"schema_after,omitempty"`
	InverseStatements  []string                        `json:"inverse_statements"`
}

// EvidenceBundle is the complete evidence payload stored encrypted in order_rollback_evidences.rollback_payload_*.
type EvidenceBundle struct {
	Engine             string              `json:"engine"`
	ServerVersion      string              `json:"server_version"`
	ServerVersionNum   int                 `json:"server_version_num"`
	SchemaName         string              `json:"schema_name"`
	OperationType      string              `json:"operation_type"`
	Strategy           string              `json:"strategy"`
	OriginalStatements []string            `json:"original_statements"`
	RollbackStatements []string            `json:"rollback_statements"`
	RollbackSQL        string              `json:"rollback_sql"`
	AffectedRows       int64               `json:"affected_rows"`
	Statements         []StatementEvidence `json:"statements"`
}

// CapabilityError indicates that a SQL statement lacks deterministic reversibility when rollback protection is enabled (Fail-Closed execution rejection).
type CapabilityError struct {
	Reason  string
	Message string
}

func (e *CapabilityError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "rollback capability unsupported: " + e.Reason
}

// ConflictError indicates that the current state of the target database conflicts with the original post-execution evidence before executing a rollback work order.
type ConflictError struct {
	Reason  string
	Message string
}

func (e *ConflictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "rollback conflict detected: " + e.Reason
}
