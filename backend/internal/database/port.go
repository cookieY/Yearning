// Package database implements the unified database Adapter Port (api/contracts/database-adapter-contract.json).
//
// This package targets external user databases (MySQL/PostgreSQL dialects) only and is unrelated to the platform metadata database.
// Security invariants:
//   - Identifiers are never interpolated from user text; they are only resolved from metadata IDs or frozen mappings and quoted by the engine Adapter.
//   - Raw SQL enters only target database driver execution methods, never errors, logs, or structured results.
//   - Explain never carries options such as ANALYZE that execute mutations.
//   - Driver error text is classified and sanitized before leaving this package.
//   - Statements, transaction commits, DDLs, and gh-ost processes are never retried inside this package.
package database

import (
	"context"
	"errors"
	"time"
)

// Purpose represents the three connection purposes defined in contract purpose_pools.
type Purpose string

const (
	PurposeReview    Purpose = "review"
	PurposeQuery     Purpose = "query"
	PurposeExecution Purpose = "execution"
)

// ValidPurpose validates a Purpose value.
func ValidPurpose(p Purpose) bool {
	switch p {
	case PurposeReview, PurposeQuery, PurposeExecution:
		return true
	}
	return false
}

// ReadOnlyPurpose reports whether the Purpose must be read-only.
func ReadOnlyPurpose(p Purpose) bool {
	return p == PurposeReview || p == PurposeQuery
}

// sessionMutationAllowlist is the set of session variable mutations allowed by purpose_pools
// (including MySQL dialect equivalents: max_execution_time/innodb_lock_wait_timeout).
// Package-private: the allowed set cannot be mutated at runtime to expand the mutation surface.
var sessionMutationAllowlist = map[Purpose]map[string]bool{
	PurposeReview:    {"statement_timeout": true, "lock_timeout": true, "application_name": true, "max_execution_time": true, "innodb_lock_wait_timeout": true, "sql_mode": true},
	PurposeQuery:     {"statement_timeout": true, "application_name": true, "max_execution_time": true, "sql_mode": true},
	PurposeExecution: {"statement_timeout": true, "lock_timeout": true, "application_name": true, "max_execution_time": true, "innodb_lock_wait_timeout": true, "sql_mode": true},
}

// IsSessionMutationAllowed reports whether a Purpose is allowed to mutate the specified session variable (read-only).
func IsSessionMutationAllowed(p Purpose, name string) bool {
	allow, ok := sessionMutationAllowlist[p]
	if !ok {
		return false
	}
	return allow[name]
}

// ConnectTimeout is the Adapter-level connection timeout ceiling (common_invariants item 1).
const ConnectTimeout = 10 * time.Second

// Common Adapter errors; error text never carries SQL or raw driver text.
var (
	// ErrUnsupportedTarget the capability matrix cannot resolve the current target (fail closed).
	ErrUnsupportedTarget = errors.New("database: capability target unsupported")
	// ErrIdentifierInvalid the identifier failed allowlist validation; refuses to execute any query.
	ErrIdentifierInvalid = errors.New("database: identifier rejected")
	// ErrMetadataVisibilityUnproven indicates the current account cannot prove metadata catalog coverage
	// over all objects, so an empty result must not be interpreted as object non-existence.
	ErrMetadataVisibilityUnproven = errors.New("database: metadata visibility unproven")
	// ErrUnsupportedRelationType indicates a non-base-table relation exists under the same schema/name;
	// callers must not interpret this as a regular table not existing.
	ErrUnsupportedRelationType = errors.New("database: unsupported relation type")
	// ErrReadOnlyViolation write-semantic operation attempted on a read-only Purpose.
	ErrReadOnlyViolation = errors.New("database: read-only purpose cannot run mutating statement")
	// ErrCursorExpired cursor owner connection was lost or closed (owner_loss semantics).
	ErrCursorExpired = errors.New("database: cursor expired")
	// ErrStatementRequired empty statement.
	ErrStatementRequired = errors.New("database: statement required")
	// ErrTransactionRequired no active transaction.
	ErrTransactionRequired = errors.New("database: transaction required")
	// ErrTransactionOpen a transaction is already open.
	ErrTransactionOpen = errors.New("database: transaction already open")
	// ErrResultUnknown failure occurred after crossing the send boundary; outcome is unknown and replay is forbidden.
	ErrResultUnknown = errors.New("database: result unknown after send boundary")
)

// SendBoundary represents durable_send_boundary_states from the execution-failure-injection contract.
type SendBoundary string

const (
	SendBoundaryNotStarted SendBoundary = "not_started"
	SendBoundarySending    SendBoundary = "sending"
	SendBoundarySent       SendBoundary = "sent"
)

// Outcome represents the execution outcome of a statement.
type Outcome string

const (
	OutcomeSuccess       Outcome = "success"
	OutcomeFailed        Outcome = "failed"
	OutcomeResultUnknown Outcome = "result_unknown"
	OutcomeCancelled     Outcome = "cancelled"
)

// EngineCodeClass represents the sanitized engine error category.
type EngineCodeClass string

const (
	ClassSuccess             EngineCodeClass = "success"
	ClassSyntaxError         EngineCodeClass = "syntax_error"
	ClassConstraintViolation EngineCodeClass = "constraint_violation"
	ClassInsufficientPriv    EngineCodeClass = "insufficient_privilege"
	ClassConnectionFailure   EngineCodeClass = "connection_failure"
	ClassNotFound            EngineCodeClass = "not_found"
	ClassCancelled           EngineCodeClass = "cancelled"
	ClassTimeout             EngineCodeClass = "timeout"
	ClassUnknown             EngineCodeClass = "unknown"
)

// Endpoint describes a target database endpoint (sourced from a datasource row, containing no plaintext credentials).
type Endpoint struct {
	Host             string
	Port             int
	DatabaseName     string // Optional (may be empty)
	CompatibilityMod string // "mysql" | "postgresql"
}

// TLSMaterial holds decrypted TLS material (used only during connection construction).
type TLSMaterial struct {
	VerifyServer bool
	ServerName   string
	CAPEM        []byte
	CertPEM      []byte
	KeyPEM       []byte
}

// Credential holds decrypted target database credentials (used only during connection construction).
type Credential struct {
	Username string
	Password string
}

// ServerIdentity is the structured result of contract dialects.*.server_identity_inputs.
type ServerIdentity struct {
	EngineFamily         string // mysql | postgresql
	ServerVersion        string
	InstanceMarker       string // Stable instance identifier (invariant across credential rotation)
	LowerCaseTableNames  string // MySQL @@lower_case_table_names
	SystemIdentifier     string // PG system_identifier (empty when unavailable)
	DatabaseOID          uint32 // PG database_oid (0 on MySQL)
	DatabaseName         string
	ConfiguredEndpointID string // host:port
}

// ConnectionTestResult conforms to result_contracts.connection_test.
type ConnectionTestResult struct {
	Engine                    string
	CompatibilityMode         string
	ServerVersion             string
	ServerIdentityFingerprint string
	Capabilities              map[string]bool
	LatencyMS                 int64
	SanitizedFailureName      string // Populated on failure; empty on success
}

// MetadataColumn conforms to result_contracts.metadata_column.
type MetadataColumn struct {
	SchemaName      string
	TableName       string
	ColumnName      string
	Ordinal         int
	DatabaseType    string
	Nullable        bool
	SensitivePolicy string // v1: "none" | "masked"; policy vocabulary is injected by the service layer
	// §18.51 Table structure enrichment (aligned with SHOW FULL FIELDS; all may be empty strings):
	// does not participate in ReadSchemaSignature canonical hashing (the frozen signature only
	// includes column name/ordinal/type/nullability; see adapter.ReadSchemaSignature).
	ColumnKey    string // MySQL: PRI/UNI/MUL; left empty on PG where there is no direct equivalent
	ColumnDeflt  string // Column default value expression, or empty string if no default
	ColumnExtra  string // MySQL extra (e.g. auto_increment); mapped from identity/generated on PG
	ColumnRemark string // Column comment, or empty string if none
}

// MetadataIndex describes a single index.
type MetadataIndex struct {
	SchemaName string
	TableName  string
	IndexName  string
	NonUnique  bool
	Primary    bool
	// ColumnNames contains named key columns. Expression key parts are counted
	// by ExpressionCount because their raw definitions stay outside the
	// Provider-safe metadata boundary.
	ColumnNames     []string
	IncludedColumns []string
	ExpressionCount int
	PrefixLengths   []int
}

// SchemaSignature is the deterministic result of ReadSchemaSignature.
type SchemaSignature struct {
	SchemaName   string
	Signature    string // Hex-encoded SHA-256 of canonicalized metadata
	ElementCount int
}

// ExplainFact conforms to result_contracts.explain_fact.
type ExplainFact struct {
	AccessMethod         string
	EstimatedRows        int64
	EstimatedCost        float64
	IndexNames           []string
	Warnings             []string
	RawEvidenceReference string // sha256:<hash>:<bytes>; raw output never leaves this package
}

// StatementResult conforms to result_contracts.statement_result.
type StatementResult struct {
	StartedAt            time.Time
	FinishedAt           time.Time
	AffectedRows         int64
	EngineCodeClass      EngineCodeClass
	Outcome              Outcome
	SendBoundary         SendBoundary
	SanitizedFailureName string
}

// QueryColumn describes a cursor result column (stable ordinal; duplicate column names are distinguished by ordinal).
type QueryColumn struct {
	Name    string
	Ordinal int
}

// QueryValue is a sanitized cell value.
type QueryValue struct {
	Kind string // "null" | "bool" | "int" | "float" | "string" | "bytes_placeholder"
	Text string
}

// QueryPage conforms to result_contracts.query_page.
type QueryPage struct {
	Columns     []QueryColumn
	Rows        [][]QueryValue
	NextCursor  string
	HasMore     bool
	PageOrdinal int
	Purpose     Purpose
}

// OpenCursorOptions holds parameters for OpenSelectCursor.
type OpenCursorOptions struct {
	StatementSQL string // Frozen final SELECT statement (guaranteed by upstream Query Safety; opens the streaming cursor)
	// PriorStatements batch prior statements (F-Q-001 multi-SELECT): executed sequentially to completion
	// on the same read-only connection/transaction with results discarded; each also undergoes shallow validation as a dual guarantee.
	PriorStatements []string
	SchemaName      string // Frozen schema (used as context after ValidateIdentifier + engine quoting)
	PageSize        int
}

// DMLStatement is a single DML statement within a transaction.
type DMLStatement struct {
	SQL string
}

// Adapter composes all six contract interfaces; each instance binds one engine dialect.
type Adapter interface {
	Connector
	MetadataReader
	PlanReader
	QueryRunner
	ChangeExecutor
	IdentifierQuoter
}

// Connector manages connections and Purpose pools.
type Connector interface {
	// TestConnection performs a one-shot probe and resolves capabilities; returns sanitized_failure_name on failure without raw driver text.
	TestConnection(ctx context.Context, cred Credential, tlsMat *TLSMaterial) (*ConnectionTestResult, error)
	// OpenPurposePool opens a connection pool for the specified purpose; credentials are decrypted and used only during construction.
	OpenPurposePool(ctx context.Context, purpose Purpose, cred Credential, tlsMat *TLSMaterial) (*PurposePool, error)
	// ReadServerIdentity reads the instance identity (without credentials).
	ReadServerIdentity(ctx context.Context, cred Credential, tlsMat *TLSMaterial) (*ServerIdentity, error)
	// Close closes the Connector and all Purpose pools it opened (contract Connector.Close).
	Close() error
}

// MetadataReader accepts only identifiers, never SQL text.
type MetadataReader interface {
	ListSchemas(ctx context.Context, pool *PurposePool) ([]string, error)
	ListTables(ctx context.Context, pool *PurposePool, schema string) ([]string, error)
	ListColumns(ctx context.Context, pool *PurposePool, schema, table string) ([]MetadataColumn, error)
	ListIndexes(ctx context.Context, pool *PurposePool, schema, table string) ([]MetadataIndex, error)
	ReadSchemaSignature(ctx context.Context, pool *PurposePool, schema string) (*SchemaSignature, error)
}

// PlanReader performs non-executing Explain only.
type PlanReader interface {
	ExplainWithoutExecution(ctx context.Context, pool *PurposePool, schema, statementSQL string) (*ExplainFact, error)
}

// SelectCursor is a live reference to a target database cursor (held by the owner connection).
type SelectCursor struct {
	handle string
	// Internal state lives in the pool implementation.
}

// Handle returns the cursor handle (for in-process use only; not usable across Pods).
func (c *SelectCursor) Handle() string { return c.handle }

// QueryRunner executes cursor queries.
type QueryRunner interface {
	OpenSelectCursor(ctx context.Context, pool *PurposePool, opts OpenCursorOptions) (*SelectCursor, error)
	// FetchCursorPage fetches the next page; when pageSize > 0, overrides the cursor's initial
	// page size and persists for subsequent fetches on this cursor (RCP-20260901-QUERY-PAGE-SIZE-PARAM);
	// 0 keeps the current value. Upper-bound validation is enforced by the application layer.
	FetchCursorPage(ctx context.Context, pool *PurposePool, cursor *SelectCursor, pageSize int) (*QueryPage, error)
	CancelQuery(ctx context.Context, pool *PurposePool, cursor *SelectCursor) error
	CloseCursor(ctx context.Context, pool *PurposePool, cursor *SelectCursor) error
}

// DMLTransaction represents a complete DML transaction within a single datasource stage.
type DMLTransaction interface {
	Commit(ctx context.Context) (*StatementResult, error)
	Rollback(ctx context.Context) (*StatementResult, error)
}

// ChangeExecutor executes frozen change statements.
type ChangeExecutor interface {
	BeginDMLTransaction(ctx context.Context, pool *PurposePool) (DMLTransaction, error)
	ExecuteDMLStatement(ctx context.Context, tx DMLTransaction, stmt DMLStatement) (*StatementResult, error)
	CommitDMLTransaction(ctx context.Context, tx DMLTransaction) (*StatementResult, error)
	RollbackDMLTransaction(ctx context.Context, tx DMLTransaction) (*StatementResult, error)
	ExecuteDDLStatement(ctx context.Context, pool *PurposePool, schema, statementSQL string) (*StatementResult, error)
	CancelExecution(ctx context.Context, pool *PurposePool) error
}

// IdentifierQuoter performs safe identifier quoting.
type IdentifierQuoter interface {
	QuoteIdentifier(name string) (string, error)
}
