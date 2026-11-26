package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PostgresSearchPathVersion              = 1
	maxPostgresSearchPathSchemas           = 64
	maxPostgresSearchPathSettingsBytes     = 4096
	maxPostgresSearchPathIdentityNameBytes = 1024
	postgresSearchPathProbeTimeout         = 5 * time.Second
	postgresRelationResolutionTimeout      = 5 * time.Second
)

const postgresSearchPathProbeSQL = `
	SELECT CURRENT_USER::pg_catalog.text,
	       pg_catalog.current_setting('search_path')::pg_catalog.text,
	       pg_catalog.array_to_json(pg_catalog.current_schemas(false))::pg_catalog.text,
	       pg_catalog.array_to_json(pg_catalog.current_schemas(true))::pg_catalog.text,
	       pg_catalog.current_schema()::pg_catalog.text`

var (
	ErrPostgresSearchPathInvalid     = errors.New("database: invalid postgresql search path snapshot")
	ErrPostgresSearchPathUnavailable = errors.New("database: postgresql search path unavailable")
	ErrPostgresRelationUnresolved    = errors.New("database: postgresql relation resolution incomplete")
	// ErrStatementNotSent marks failures proven to occur before the target
	// statement was handed to the driver. Callers may preserve not_started
	// only when this sentinel is present; ordinary driver errors stay unknown.
	ErrStatementNotSent = errors.New("database: statement not sent")
)

// StatementNotSentError carries a stable, sanitized reason without exposing
// driver text. It is returned only for precondition failures that occur before
// statementSQL is sent.
type StatementNotSentError struct {
	FailureName string
}

func (e *StatementNotSentError) Error() string {
	if e == nil || e.FailureName == "" {
		return ErrStatementNotSent.Error()
	}
	return ErrStatementNotSent.Error() + ": " + e.FailureName
}

func (e *StatementNotSentError) Unwrap() error { return ErrStatementNotSent }

// PostgresSearchPath is a trusted, encrypted-stage-only snapshot of the
// execution role's effective PostgreSQL path. Configured preserves the text
// returned by current_setting for diagnosis, while Explicit is the expanded
// value safe to apply with SET and Effective is the ordered lookup path,
// including implicit pg_catalog/pg_temp entries.
type PostgresSearchPath struct {
	SchemaVersion  int      `json:"version"`
	CurrentUser    string   `json:"current_user"`
	Configured     string   `json:"configured"`
	Explicit       []string `json:"explicit"`
	Effective      []string `json:"effective"`
	CreationSchema string   `json:"creation_schema"`
}

// Validate rejects snapshots that cannot be safely compared or replayed.
func (p PostgresSearchPath) Validate() error {
	invalid := func(reason string) error {
		return fmt.Errorf("%w: %s", ErrPostgresSearchPathInvalid, reason)
	}
	if p.SchemaVersion != PostgresSearchPathVersion {
		return invalid("version")
	}
	if !validSearchPathIdentityName(p.CurrentUser) {
		return invalid("current user")
	}
	if len(p.Configured) > maxPostgresSearchPathSettingsBytes || !utf8.ValidString(p.Configured) {
		return invalid("configured")
	}
	if err := validatePostgresSearchPathMembers(p.Explicit, true); err != nil {
		return fmt.Errorf("%w: explicit: %v", ErrPostgresSearchPathInvalid, err)
	}
	if err := validatePostgresSearchPathMembers(p.Effective, true); err != nil {
		return fmt.Errorf("%w: effective: %v", ErrPostgresSearchPathInvalid, err)
	}
	if err := ValidateIdentifier(p.CreationSchema); err != nil {
		return fmt.Errorf("%w: creation schema", ErrPostgresSearchPathInvalid)
	}
	if p.CreationSchema != p.Explicit[0] {
		return invalid("creation schema is not first explicit member")
	}
	expectedEffective := p.Explicit
	if !slices.Contains(p.Explicit, "pg_catalog") {
		expectedEffective = append([]string{"pg_catalog"}, p.Explicit...)
	}
	if !stringSlicesEqual(p.Effective, expectedEffective) {
		return invalid("effective path does not match PostgreSQL expansion")
	}
	return nil
}

// Equal compares the execution identity and ordered resolution semantics.
// The configured spelling may differ when Postgres expands "$user" or applies
// a role/database default, so only resolved values participate in drift.
func (p PostgresSearchPath) Equal(other PostgresSearchPath) bool {
	if p.Validate() != nil || other.Validate() != nil {
		return false
	}
	return p.SchemaVersion == other.SchemaVersion &&
		p.CurrentUser == other.CurrentUser &&
		stringSlicesEqual(p.Explicit, other.Explicit) &&
		stringSlicesEqual(p.Effective, other.Effective) &&
		p.CreationSchema == other.CreationSchema
}

// Matches is an intent-revealing alias used by gate callers.
func (p PostgresSearchPath) Matches(other PostgresSearchPath) bool { return p.Equal(other) }

func validSearchPathIdentityName(value string) bool {
	if value == "" || len(value) > maxPostgresSearchPathIdentityNameBytes ||
		!utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, r := range value {
		if r == '\n' || r == '\r' {
			return false
		}
	}
	return true
}

func validatePostgresSearchPathMembers(members []string, rejectTemporary bool) error {
	if len(members) == 0 || len(members) > maxPostgresSearchPathSchemas {
		return errors.New("member count")
	}
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		if err := ValidateIdentifier(member); err != nil {
			return errors.New("member identifier")
		}
		if seen[member] {
			return errors.New("duplicate member")
		}
		seen[member] = true
		if rejectTemporary && postgresTemporarySchema(member) {
			return errors.New("temporary member")
		}
	}
	return nil
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// ProbePostgresSearchPath reads the execution role's actual path with a fixed
// statement inside a read-only transaction. SET LOCAL removes any prior
// search_path mutation on a pooled connection before reading the configured,
// expanded and effective values.
func ProbePostgresSearchPath(ctx context.Context, pool *PurposePool) (PostgresSearchPath, error) {
	if pool == nil || pool.engine != "postgresql" || pool.purpose != PurposeExecution {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	probeCtx, cancel := context.WithTimeout(ctx, postgresSearchPathProbeTimeout)
	defer cancel()
	var snapshot PostgresSearchPath
	err := pool.withReadQueryer(probeCtx, false, func(q queryer) error {
		tx, ok := q.(*sql.Tx)
		if !ok {
			return ErrPostgresSearchPathUnavailable
		}
		// A dedicated connection can carry a session-local path retained by a
		// previous DDL. Reset only inside this read-only transaction so the
		// snapshot reflects the execution role/database defaults.
		if _, err := tx.ExecContext(probeCtx, "SET LOCAL search_path TO DEFAULT"); err != nil {
			return ErrPostgresSearchPathUnavailable
		}
		var err error
		snapshot, err = readPostgresSearchPath(probeCtx, tx)
		return err
	})
	if err != nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	return snapshot, nil
}

func probePostgresQueryerSearchPath(
	ctx context.Context, q queryer,
) (PostgresSearchPath, error) {
	if q == nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	snapshot, err := readPostgresSearchPath(ctx, q)
	if err != nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	return snapshot, nil
}

func readPostgresSearchPath(
	ctx context.Context, q queryer,
) (PostgresSearchPath, error) {
	var (
		snapshot      PostgresSearchPath
		explicitJSON  string
		effectiveJSON string
		creation      sql.NullString
	)
	if err := q.QueryRowContext(ctx, postgresSearchPathProbeSQL).Scan(
		&snapshot.CurrentUser, &snapshot.Configured,
		&explicitJSON, &effectiveJSON, &creation,
	); err != nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	if err := json.Unmarshal([]byte(explicitJSON), &snapshot.Explicit); err != nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	if err := json.Unmarshal([]byte(effectiveJSON), &snapshot.Effective); err != nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	snapshot.Explicit = uniqueStringsPreservingOrder(snapshot.Explicit)
	snapshot.Effective = uniqueStringsPreservingOrder(snapshot.Effective)
	if !creation.Valid {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	snapshot.SchemaVersion = PostgresSearchPathVersion
	snapshot.CreationSchema = creation.String
	if err := snapshot.Validate(); err != nil {
		return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
	}
	for _, schema := range snapshot.Effective {
		if postgresTemporarySchema(schema) {
			// A temporary namespace is session-local and cannot be replayed by
			// a later execution connection.
			return PostgresSearchPath{}, ErrPostgresSearchPathUnavailable
		}
	}
	return snapshot, nil
}

func uniqueStringsPreservingOrder(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func postgresTemporarySchema(schema string) bool {
	return schema == "pg_temp" || strings.HasPrefix(schema, "pg_temp_")
}

// PostgresRelationKind is the closed relation-kind projection used to ensure a
// non-table in an earlier schema cannot be skipped in favor of a later table.
type PostgresRelationKind string

const (
	PostgresRelationTable        PostgresRelationKind = "table"
	PostgresRelationView         PostgresRelationKind = "view"
	PostgresRelationSequence     PostgresRelationKind = "sequence"
	PostgresRelationIndex        PostgresRelationKind = "index"
	PostgresRelationForeignTable PostgresRelationKind = "foreign_table"
	PostgresRelationOther        PostgresRelationKind = "other"
)

func (k PostgresRelationKind) IsBaseTable() bool {
	return k == PostgresRelationTable
}

func postgresRelationKind(relkind string) PostgresRelationKind {
	switch relkind {
	case "r", "p":
		return PostgresRelationTable
	case "v", "m":
		return PostgresRelationView
	case "S":
		return PostgresRelationSequence
	case "i", "I":
		return PostgresRelationIndex
	case "f":
		return PostgresRelationForeignTable
	default:
		return PostgresRelationOther
	}
}

// PostgresRelation is the first relation matching a name in Effective order.
// Schema and Name are trusted catalog identities and must never be sent to a
// Provider or logged.
type PostgresRelation struct {
	Schema string
	Name   string
	Kind   PostgresRelationKind
}

// ResolvePostgresRelation resolves a relation from the frozen effective path
// using the review role's read-only catalog connection. Every searched schema
// must exist and have USAGE; otherwise absence cannot be proven. A found
// non-table is authoritative and prevents later same-name tables from being
// selected.
func ResolvePostgresRelation(
	ctx context.Context,
	pool *PurposePool,
	path PostgresSearchPath,
	relation string,
) (PostgresRelation, bool, error) {
	if pool == nil || pool.engine != "postgresql" || pool.purpose != PurposeReview {
		return PostgresRelation{}, false, ErrPostgresSearchPathUnavailable
	}
	if err := path.Validate(); err != nil {
		return PostgresRelation{}, false, ErrPostgresSearchPathUnavailable
	}
	if err := ValidateIdentifier(relation); err != nil {
		return PostgresRelation{}, false, ErrPostgresRelationUnresolved
	}
	resolveCtx, cancel := context.WithTimeout(ctx, postgresRelationResolutionTimeout)
	defer cancel()
	var (
		resolved PostgresRelation
		found    bool
	)
	err := pool.withReadQueryer(resolveCtx, true, func(q queryer) error {
		for _, schema := range path.Effective {
			if postgresTemporarySchema(schema) {
				return ErrPostgresSearchPathUnavailable
			}
			var (
				schemaExists bool
				usage        bool
				relkind      sql.NullString
			)
			if err := q.QueryRowContext(resolveCtx, `
				SELECT EXISTS (
				         SELECT 1 FROM pg_catalog.pg_namespace
				          WHERE nspname = $1),
				       COALESCE((
				         SELECT pg_catalog.has_schema_privilege(
				                  $3::pg_catalog.name, n.oid, 'USAGE')
				           FROM pg_catalog.pg_namespace n
				          WHERE n.nspname = $1), false),
				       (SELECT c.relkind::pg_catalog.text
				          FROM pg_catalog.pg_class c
				          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				         WHERE n.nspname = $1 AND c.relname = $2
				         LIMIT 1)`, schema, relation, path.CurrentUser).Scan(&schemaExists, &usage, &relkind); err != nil {
				return ErrPostgresSearchPathUnavailable
			}
			if !schemaExists || !usage {
				return ErrPostgresSearchPathUnavailable
			}
			if relkind.Valid && relkind.String != "" {
				resolved = PostgresRelation{
					Schema: schema,
					Name:   relation,
					Kind:   postgresRelationKind(relkind.String),
				}
				found = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return PostgresRelation{}, false, err
	}
	return resolved, found, nil
}

// ApplyTxSearchPath applies a frozen detailed PostgreSQL path inside one DML
// transaction. MySQL retains the existing exactly-one schema behavior.
func ApplyTxSearchPath(ctx context.Context, pool *PurposePool, tx DMLTransaction, path []string) error {
	t, ok := tx.(*dmlTx)
	if !ok || !t.open {
		return ErrTransactionRequired
	}
	if pool == nil || pool.engine != t.pool.engine {
		return ErrPostgresSearchPathInvalid
	}
	var stmt string
	var err error
	if pool.engine == "mysql" {
		if len(path) == 0 {
			return nil
		}
		if len(path) != 1 {
			return ErrPostgresSearchPathInvalid
		}
		quoted, quoteErr := quoteMySQLOpen(path[0])
		if quoteErr != nil {
			return quoteErr
		}
		stmt = "USE " + quoted
	} else if pool.engine == "postgresql" {
		stmt, err = postgresSearchPathSQL(path, true)
		if err != nil {
			return err
		}
	} else {
		return ErrUnsupportedTarget
	}
	if _, err := t.tx.ExecContext(t.execCtx, stmt); err != nil {
		return errors.New("database: schema context apply failed")
	}
	return nil
}

type searchPathDDLExecutor interface {
	executeDDLWithSearchPath(
		ctx context.Context,
		pool *PurposePool,
		searchPath []string,
		statementSQL string,
		beforeSend func(context.Context) error,
	) (*StatementResult, error)
}

type searchPathDDLInspectedExecutor interface {
	executeDDLWithSearchPathInspected(
		ctx context.Context,
		pool *PurposePool,
		searchPath []string,
		statementSQL string,
		beforeSend func(context.Context, *sql.Conn) error,
	) (*StatementResult, error)
}

// ExecuteDDLWithSearchPath executes one DDL on a dedicated connection after
// quoting and applying each frozen path member. The Adapter identity is
// checked through an optional private method so public Adapter interfaces stay
// backward compatible.
func ExecuteDDLWithSearchPath(
	ctx context.Context,
	adapter Adapter,
	pool *PurposePool,
	searchPath []string,
	statementSQL string,
) (*StatementResult, error) {
	return ExecuteDDLWithSearchPathBeforeSend(
		ctx, adapter, pool, searchPath, statementSQL, nil,
	)
}

// ExecuteDDLWithSearchPathBeforeSend is the execution-path variant. beforeSend
// runs after connection/schema initialization and immediately before the DDL
// is handed to the driver, allowing the durable send boundary to advance at the
// narrowest safe point. Initialization failures are wrapped in
// ErrStatementNotSent; once beforeSend succeeds, driver failures retain the
// ordinary result-unknown classification.
func ExecuteDDLWithSearchPathBeforeSend(
	ctx context.Context,
	adapter Adapter,
	pool *PurposePool,
	searchPath []string,
	statementSQL string,
	beforeSend func(context.Context) error,
) (*StatementResult, error) {
	executor, ok := adapter.(searchPathDDLExecutor)
	if !ok || pool == nil {
		return nil, &StatementNotSentError{FailureName: "adapter_unavailable"}
	}
	return executor.executeDDLWithSearchPath(
		ctx, pool, searchPath, statementSQL, beforeSend,
	)
}

// ExecuteDDLWithSearchPathInspectedBeforeSend exposes the exact dedicated
// connection to the pre-send verifier. The statement is sent only if the
// callback returns nil.
func ExecuteDDLWithSearchPathInspectedBeforeSend(
	ctx context.Context,
	adapter Adapter,
	pool *PurposePool,
	searchPath []string,
	statementSQL string,
	beforeSend func(context.Context, *sql.Conn) error,
) (*StatementResult, error) {
	executor, ok := adapter.(searchPathDDLInspectedExecutor)
	if !ok || pool == nil {
		return nil, &StatementNotSentError{FailureName: "adapter_unavailable"}
	}
	return executor.executeDDLWithSearchPathInspected(
		ctx, pool, searchPath, statementSQL, beforeSend,
	)
}

func (a *baseAdapter) executeDDLWithSearchPath(
	ctx context.Context,
	pool *PurposePool,
	searchPath []string,
	statementSQL string,
	beforeSend func(context.Context) error,
) (*StatementResult, error) {
	var inspected func(context.Context, *sql.Conn) error
	if beforeSend != nil {
		inspected = func(ctx context.Context, _ *sql.Conn) error {
			return beforeSend(ctx)
		}
	}
	return a.executeDDLWithSearchPathInspected(
		ctx, pool, searchPath, statementSQL, inspected,
	)
}

func (a *baseAdapter) executeDDLWithSearchPathInspected(
	ctx context.Context,
	pool *PurposePool,
	searchPath []string,
	statementSQL string,
	beforeSend func(context.Context, *sql.Conn) error,
) (*StatementResult, error) {
	if pool == nil || pool.engine != a.engine() {
		return nil, &StatementNotSentError{FailureName: "adapter_unavailable"}
	}
	if ReadOnlyPurpose(pool.purpose) {
		return nil, &StatementNotSentError{FailureName: "read_only_target"}
	}
	if statementSQL == "" {
		return nil, &StatementNotSentError{FailureName: "statement_required"}
	}
	var pathSQL string
	if pool.engine == "mysql" {
		if len(searchPath) > 1 {
			return nil, &StatementNotSentError{FailureName: "schema_context_invalid"}
		}
		if len(searchPath) == 1 {
			quoted, err := quoteMySQLOpen(searchPath[0])
			if err != nil {
				return nil, &StatementNotSentError{FailureName: "schema_context_invalid"}
			}
			pathSQL = "USE " + quoted
		}
	} else if pool.engine == "postgresql" {
		var err error
		pathSQL, err = postgresSearchPathSQL(searchPath, false)
		if err != nil {
			return nil, &StatementNotSentError{FailureName: "schema_context_invalid"}
		}
	} else {
		return nil, &StatementNotSentError{FailureName: "adapter_unavailable"}
	}

	execCtx, release, execID := pool.registerExecution(ctx)
	defer release()
	conn, err := pool.acquireWriteConn(execCtx)
	if err != nil {
		return nil, &StatementNotSentError{FailureName: "connection_open_failed"}
	}
	pool.trackExecutionBackend(execID, backendIdentityOf(execCtx, pool.engine, conn))
	defer pool.untrackExecutionBackend(execID)
	defer func() { _ = conn.Close() }()
	if pathSQL != "" {
		if _, err := conn.ExecContext(execCtx, pathSQL); err != nil {
			return nil, &StatementNotSentError{FailureName: "schema_context_failed"}
		}
	}
	if beforeSend != nil {
		if err := beforeSend(execCtx, conn); err != nil {
			return nil, err
		}
	}
	started := time.Now().UTC()
	res, err := conn.ExecContext(execCtx, statementSQL)
	if err == nil {
		affected := int64(0)
		if res != nil {
			if n, rerr := res.RowsAffected(); rerr == nil {
				affected = n
			}
		}
		return &StatementResult{
			StartedAt: started, FinishedAt: time.Now().UTC(),
			AffectedRows: affected, EngineCodeClass: ClassSuccess,
			Outcome: OutcomeSuccess, SendBoundary: SendBoundarySent,
		}, nil
	}
	return statementFailure(pool.engine, started, err), nil
}
