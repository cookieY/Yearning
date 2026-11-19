// exec.go Cursor queries and change execution.
// DML: one datasource stage = one complete transaction (E001); failure/cancellation rolls back only the current stage.
// DDL: serial execution without a transaction wrapper; partial failure preserves already-succeeded results.
// Any failure after crossing the commit send boundary = result_unknown, never replayed.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- QueryRunner ----

func (a *baseAdapter) OpenSelectCursor(ctx context.Context, pool *PurposePool, opts OpenCursorOptions) (*SelectCursor, error) {
	if !ReadOnlyPurpose(pool.purpose) {
		return nil, ErrReadOnlyViolation
	}
	if err := validateCursorStatement(opts.StatementSQL, pool.engine == "postgresql"); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(opts.SchemaName); err != nil {
		return nil, err
	}
	pageSize := opts.PageSize
	if pageSize <= 0 || pageSize > 5000 {
		pageSize = 100
	}
	// streamCtx is the owner context of the result stream / read-only transaction on the connection,
	// which must be decoupled from the caller's request context (F-Q-002): live cursors survive across
	// requests per contract (idle TTL + 6h absolute TTL, owner-affinity pagination); binding the HTTP
	// request context would cancel the stream the instant the response returns — causing all subsequent
	// page fetches under a real HTTP server to fail with "cursor stream failed" (4011); in-process
	// httptest clients do not cancel request contexts, so integration tests could not expose this.
	// Statement duration is still enforced server-side by the pool session's statement_timeout/max_execution_time;
	// stream termination is governed uniformly by cancel (dropCursor) and pool Close.
	streamCtx, cancel := context.WithCancel(context.Background())
	// Propagate caller ctx cancellation while OpenSelectCursor is still in flight;
	// disarm before returning a live cursor so post-open HTTP request completion does not cancel streamCtx (F-Q-002).
	stopOpenCancel := context.AfterFunc(ctx, cancel)
	defer func() {
		_ = stopOpenCancel()
	}()
	// Connection establishment / session setup / statement dispatch complete within the request lifecycle (this ctx).
	conn, err := pool.db.Conn(ctx)
	if err != nil {
		cancel()
		return nil, errors.New("database: connection unavailable")
	}
	c := &liveCursor{
		handle:   newHandle(),
		purpose:  pool.purpose,
		pageSize: pageSize,
		conn:     conn,
		cancel:   cancel,
	}
	fail := func(err error) (*SelectCursor, error) {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	if err := pool.applySessionDefaults(ctx, conn); err != nil {
		return fail(err)
	}
	var rows *sql.Rows
	if pool.engine == "postgresql" {
		// Bind the read-only transaction to streamCtx: binding to the request context would roll back
		// the transaction and terminate the cursor as soon as the response returns (root cause of F-Q-002).
		tx, err := conn.BeginTx(streamCtx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return fail(errors.New("database: read-only transaction begin failed"))
		}
		if err := setTxSearchPath(ctx, tx, opts.SchemaName); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if err := drainPriorSelects(ctx, pool, tx, opts.PriorStatements); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		rows, err = tx.QueryContext(streamCtx, opts.StatementSQL)
		if err != nil {
			_ = tx.Rollback()
			// SEC-0030: external rejection surface is the stable classification name (BUG-011 diagnosability
			// is carried by ClassifyError's structured FailureName); raw driver text remains only on
			// SanitizedError.Raw's errors chain and never appears in any string representation.
			cls := ClassifyError(pool.engine, err)
			return fail(&SanitizedError{FailureName: "cursor_rejected_" + cls.FailureName, Raw: err})
		}
		c.tx = tx
	} else {
		if err := pool.setSessionReadOnly(ctx, conn); err != nil {
			return fail(err)
		}
		if err := setConnSchema(ctx, conn, opts.SchemaName); err != nil {
			return fail(err)
		}
		if err := drainPriorSelects(ctx, pool, conn, opts.PriorStatements); err != nil {
			return fail(err)
		}
		rows, err = conn.QueryContext(streamCtx, opts.StatementSQL)
		if err != nil {
			cls := ClassifyError(pool.engine, err)
			return fail(&SanitizedError{FailureName: "cursor_rejected_" + cls.FailureName, Raw: err})
		}
	}
	c.rows = rows
	names, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		if c.tx != nil {
			_ = c.tx.Rollback()
		}
		return fail(errors.New("database: cursor columns unreadable"))
	}
	for i, name := range names {
		c.columns = append(c.columns, QueryColumn{Name: name, Ordinal: i + 1})
	}
	pool.registerCursor(c)
	return &SelectCursor{handle: c.handle}, nil
}

// queryRunner abstracts QueryContext across *sql.Tx and *sql.Conn (for draining prior statements).
type queryRunner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// drainPriorSelects executes batch prior statements (F-Q-001 multi-SELECT) sequentially on the
// same read-only connection/transaction and drains/discards their results — iterating without
// materializing (bounded memory), with statement duration enforced server-side by the pool session's
// statement_timeout/max_execution_time. Each statement undergoes shallow validation (forming the same
// dual guarantee as the final statement); failures map to the same sanitized classification names as the main statement (SEC-0030).
func drainPriorSelects(ctx context.Context, pool *PurposePool, q queryRunner, stmts []string) error {
	postgres := pool.engine == "postgresql"
	for _, s := range stmts {
		if err := validateCursorStatement(s, postgres); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, s)
		if err != nil {
			cls := ClassifyError(pool.engine, err)
			return &SanitizedError{FailureName: "cursor_rejected_" + cls.FailureName, Raw: err}
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			cls := ClassifyError(pool.engine, err)
			return &SanitizedError{FailureName: "cursor_rejected_" + cls.FailureName, Raw: err}
		}
		_ = rows.Close()
	}
	return nil
}

func (a *baseAdapter) FetchCursorPage(ctx context.Context, pool *PurposePool, cursor *SelectCursor, pageSize int) (*QueryPage, error) {
	if cursor == nil {
		return nil, ErrCursorExpired
	}
	c, err := pool.lookupCursor(cursor.handle)
	if err != nil {
		return nil, err
	}
	if c.exhausted {
		return nil, ErrCursorExpired
	}
	if pageSize > 0 && pageSize != c.pageSize {
		// Continuation page size override: applies to this fetch and subsequent fetches on this cursor (already bounded to 1..5000 at the application layer).
		c.pageSize = pageSize
	}
	page := &QueryPage{
		Columns:     c.columns,
		PageOrdinal: c.pageOrdinal + 1,
		Purpose:     c.purpose,
	}
	scan := make([]any, len(c.columns))
	vals := make([]any, len(c.columns))
	for i := range scan {
		scan[i] = &vals[i]
	}
	// SEC-0027: single-page byte budget (infrastructure-level exhaustion defense, not a product row limit) —
	// accumulate estimated bytes row by row and stop fetching once the budget is reached; pagination
	// continues normally with HasMore/NextCursor (truncated pages have the same shape as tail pages,
	// without failing the request); if a single row exceeds the budget on an empty page, terminate the
	// cursor and return ErrPageBudgetExceeded (mapped to 1012-family structured error at the application layer).
	budgetUsed := 0
	for i := 0; i < c.pageSize; i++ {
		if !c.rows.Next() {
			if err := c.rows.Err(); err != nil {
				pool.dropCursor(c.handle)
				return nil, errors.New("database: cursor stream failed")
			}
			c.exhausted = true
			break
		}
		if err := c.rows.Scan(scan...); err != nil {
			pool.dropCursor(c.handle)
			return nil, errors.New("database: cursor row unreadable")
		}
		row := make([]QueryValue, len(vals))
		for j, v := range vals {
			row[j] = normalizeValue(v)
		}
		appendRow, stop, berr := pageBudgetAdmit(budgetUsed, estimateRowBytes(row))
		if berr != nil {
			pool.dropCursor(c.handle)
			return nil, berr
		}
		if appendRow {
			page.Rows = append(page.Rows, row)
			budgetUsed += estimateRowBytes(row)
		}
		if stop {
			break
		}
	}
	c.pageOrdinal = page.PageOrdinal
	if c.exhausted {
		page.HasMore = false
		page.NextCursor = ""
		pool.dropCursor(c.handle)
		return page, nil
	}
	// Do not prefetch the next row: a full page conservatively returns HasMore=true (the next page may be empty and then terminate).
	page.HasMore = true
	page.NextCursor = c.handle
	return page, nil
}

func (a *baseAdapter) CancelQuery(ctx context.Context, pool *PurposePool, cursor *SelectCursor) error {
	if cursor == nil {
		return ErrCursorExpired
	}
	c, err := pool.lookupCursor(cursor.handle)
	if err != nil {
		return err
	}
	// Cancel governance context: PG sends a Cancel request, MySQL aborts on connection close;
	// cancellation acknowledgment does not imply the outcome after commit send is known (contract cancel semantics).
	if c.cancel != nil {
		c.cancel()
	}
	pool.dropCursor(c.handle)
	return nil
}

func (a *baseAdapter) CloseCursor(ctx context.Context, pool *PurposePool, cursor *SelectCursor) error {
	if cursor == nil {
		return nil
	}
	if _, err := pool.lookupCursor(cursor.handle); err != nil {
		return nil // Already terminated is treated as idempotent success
	}
	pool.dropCursor(cursor.handle)
	return nil
}

// ---- ChangeExecutor ----

// dmlTx is the datasource stage transaction handle.
type dmlTx struct {
	pool       *PurposePool
	conn       *sql.Conn
	tx         *sql.Tx
	open       bool
	statements int
	execID     string // Execution registration ID (for targeted cancellation addressing)
	// execCtx is the governance context (derived from the Begin caller ctx); statement execution must use it
	// so caller cancellation propagates via the parent chain (statement-level targeted cancellation uses cancelStatementBackends).
	execCtx  context.Context
	release  func() // Cancels the execution governance context; released when the transaction terminates
	finished bool   // Resources already terminated (prevents duplicate cleanup / false result_unknown reporting)
}

// finish closes transaction resources; releasing the governance context must happen after transaction
// termination (cancelling early would asynchronously roll back the transaction, causing ErrTxDone on subsequent statements). Idempotent.
func (t *dmlTx) finish() {
	if t.finished {
		return
	}
	t.finished = true
	t.open = false
	if t.execID != "" {
		t.pool.untrackExecutionBackend(t.execID)
	}
	_ = t.conn.Close()
	if t.release != nil {
		t.release()
		t.release = nil
	}
}

// sealUnknown terminates the transaction after result_unknown: makes a best-effort rollback and immediately
// releases the dedicated connection and governance cancel registration (caller may abandon Rollback, so connections must not leak).
func (t *dmlTx) sealUnknown() {
	_ = t.tx.Rollback()
	t.finish()
}

// Commit implements the DMLTransaction interface; identical semantics to ChangeExecutor.CommitDMLTransaction.
func (t *dmlTx) Commit(ctx context.Context) (*StatementResult, error) { return commitDML(ctx, t) }

// Rollback implements the DMLTransaction interface.
func (t *dmlTx) Rollback(ctx context.Context) (*StatementResult, error) { return rollbackDML(ctx, t) }

func (a *baseAdapter) BeginDMLTransaction(ctx context.Context, pool *PurposePool) (DMLTransaction, error) {
	if ReadOnlyPurpose(pool.purpose) {
		return nil, ErrReadOnlyViolation
	}
	execCtx, release, execID := pool.registerExecution(ctx)
	conn, err := pool.acquireWriteConn(execCtx)
	if err != nil {
		release()
		return nil, err
	}
	tx, err := conn.BeginTx(execCtx, nil)
	if err != nil {
		_ = conn.Close()
		release()
		return nil, errors.New("database: transaction begin failed")
	}
	// Register server backend identity: targeted cancellation kills only the statement, keeping the connection and transaction rollback-capable.
	pool.trackExecutionBackend(execID, backendIdentityOf(execCtx, pool.engine, conn))
	return &dmlTx{pool: pool, conn: conn, tx: tx, open: true, execCtx: execCtx,
		release: release, execID: execID}, nil
}

func (a *baseAdapter) ExecuteDMLStatement(ctx context.Context, tx DMLTransaction, stmt DMLStatement) (*StatementResult, error) {
	t, ok := tx.(*dmlTx)
	if !ok || !t.open {
		return nil, ErrTransactionRequired
	}
	if stmt.SQL == "" {
		return nil, ErrStatementRequired
	}
	started := time.Now().UTC()
	res, err := t.tx.ExecContext(t.execCtx, stmt.SQL)
	if err == nil {
		affected := int64(0)
		if res != nil {
			if n, rerr := res.RowsAffected(); rerr == nil {
				affected = n
			}
		}
		t.statements++
		return &StatementResult{
			StartedAt: started, FinishedAt: time.Now().UTC(),
			AffectedRows: affected, EngineCodeClass: ClassSuccess,
			Outcome: OutcomeSuccess, SendBoundary: SendBoundarySent,
		}, nil
	}
	result := statementFailure(t.pool.engine, started, err)
	if result.Outcome == OutcomeResultUnknown {
		// Ambiguous failure: terminate and release the dedicated connection and governance cancel registration (caller may abandon Rollback).
		t.sealUnknown()
	}
	return result, nil
}

func (a *baseAdapter) CommitDMLTransaction(ctx context.Context, tx DMLTransaction) (*StatementResult, error) {
	t, ok := tx.(*dmlTx)
	if !ok || !t.open {
		return nil, ErrTransactionRequired
	}
	return commitDML(ctx, t)
}

func (a *baseAdapter) RollbackDMLTransaction(ctx context.Context, tx DMLTransaction) (*StatementResult, error) {
	t, ok := tx.(*dmlTx)
	if !ok {
		return nil, ErrTransactionRequired
	}
	return rollbackDML(ctx, t)
}

func commitDML(ctx context.Context, t *dmlTx) (*StatementResult, error) {
	if t.finished {
		return nil, ErrTransactionRequired
	}
	started := time.Now().UTC()
	err := t.tx.Commit()
	t.finish()
	if err == nil {
		return &StatementResult{
			StartedAt: started, FinishedAt: time.Now().UTC(),
			EngineCodeClass: ClassSuccess, Outcome: OutcomeSuccess,
			SendBoundary: SendBoundarySent,
		}, nil
	}
	cls := ClassifyError(t.pool.engine, err)
	return &StatementResult{
		StartedAt: started, FinishedAt: time.Now().UTC(),
		EngineCodeClass: cls.Class, SanitizedFailureName: cls.FailureName,
		// Any failure after commit send is treated as result_unknown (classic ambiguous commit) and is never automatically retried.
		Outcome: OutcomeResultUnknown, SendBoundary: SendBoundarySent,
	}, nil
}

func rollbackDML(ctx context.Context, t *dmlTx) (*StatementResult, error) {
	if t.finished {
		// Resources were already released on the sealUnknown/termination path; treat idempotently as cancelled cleanup.
		return &StatementResult{
			StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
			EngineCodeClass: ClassSuccess, Outcome: OutcomeCancelled,
			SendBoundary: SendBoundaryNotStarted,
		}, nil
	}
	started := time.Now().UTC()
	err := t.tx.Rollback()
	t.finish()
	if err == nil {
		return &StatementResult{
			StartedAt: started, FinishedAt: time.Now().UTC(),
			EngineCodeClass: ClassSuccess, Outcome: OutcomeCancelled,
			SendBoundary: SendBoundaryNotStarted,
		}, nil
	}
	cls := ClassifyError(t.pool.engine, err)
	return &StatementResult{
		StartedAt: started, FinishedAt: time.Now().UTC(),
		EngineCodeClass: cls.Class, SanitizedFailureName: cls.FailureName,
		Outcome: OutcomeResultUnknown, SendBoundary: SendBoundaryNotStarted,
	}, nil
}

// statementFailure unifies statement failure classification:
// deterministic rejection -> failed/cancelled; ambiguous (connection failure / unknown) -> result_unknown
// (caller seals the transaction and releases resources accordingly, forbidding replay).
func statementFailure(engine string, started time.Time, err error) *StatementResult {
	cls := ClassifyError(engine, err)
	result := &StatementResult{
		StartedAt: started, FinishedAt: time.Now().UTC(),
		EngineCodeClass: cls.Class, SanitizedFailureName: cls.FailureName,
		SendBoundary: SendBoundarySent,
	}
	switch cls.Class {
	case ClassSyntaxError, ClassNotFound, ClassConstraintViolation, ClassInsufficientPriv, ClassTimeout:
		result.Outcome = OutcomeFailed
	case ClassCancelled:
		result.Outcome = OutcomeCancelled
	default:
		// Cannot prove bytes did not cross the send boundary: outcome is unknown and replay is forbidden.
		result.Outcome = OutcomeResultUnknown
	}
	return result
}

func (a *baseAdapter) ExecuteDDLStatement(ctx context.Context, pool *PurposePool, schema, statementSQL string) (*StatementResult, error) {
	if ReadOnlyPurpose(pool.purpose) {
		return nil, ErrReadOnlyViolation
	}
	if statementSQL == "" {
		return nil, ErrStatementRequired
	}
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	execCtx, release, execID := pool.registerExecution(ctx)
	defer release()
	conn, err := pool.acquireWriteConn(execCtx)
	if err != nil {
		return nil, err
	}
	pool.trackExecutionBackend(execID, backendIdentityOf(execCtx, pool.engine, conn))
	defer pool.untrackExecutionBackend(execID)
	defer func() { _ = conn.Close() }()
	if err := applySchemaContext(execCtx, pool.engine, conn, schema); err != nil {
		return nil, err
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

// setTxSearchPath restricts search_path within a PG read-only transaction (identifier validated against allowlist + quoted).
func setTxSearchPath(ctx context.Context, tx *sql.Tx, schema string) error {
	quoted, err := quotePostgresOpen(schema)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = "+quoted); err != nil {
		return errors.New("database: schema context apply failed")
	}
	return nil
}

// ApplyTxSchema restricts the default schema within an executing DML transaction (USE / SET search_path;
// identifier validated against allowlist + quoted by engine; sole in-transaction schema path on the B10 execution surface).
func ApplyTxSchema(ctx context.Context, pool *PurposePool, tx DMLTransaction, schema string) error {
	t, ok := tx.(*dmlTx)
	if !ok || !t.open {
		return ErrTransactionRequired
	}
	if err := ValidateIdentifier(schema); err != nil {
		return err
	}
	quoted, err := quoteForEngine(pool.engine, schema)
	if err != nil {
		return err
	}
	stmt := "SET LOCAL search_path = " + quoted
	if pool.engine == "mysql" {
		stmt = "USE " + quoted
	}
	if _, err := t.tx.ExecContext(t.execCtx, stmt); err != nil {
		return errors.New("database: schema context apply failed")
	}
	return nil
}

// postgresSearchPathSQL renders an ordered PostgreSQL path. It is kept with
// the other engine-specific identifier interpolation helpers so every
// search_path assignment has one validated quoting path.
func postgresSearchPathSQL(path []string, local bool) (string, error) {
	if err := validatePostgresSearchPathMembers(path, true); err != nil {
		return "", fmt.Errorf("%w: %v", ErrPostgresSearchPathInvalid, err)
	}
	quoted := make([]string, len(path))
	for i, schema := range path {
		value, err := quotePostgresOpen(schema)
		if err != nil {
			return "", ErrPostgresSearchPathInvalid
		}
		quoted[i] = value
	}
	quotedPath := strings.Join(quoted, ", ")
	if local {
		return "SET LOCAL search_path = " + quotedPath, nil
	}
	return "SET search_path = " + quotedPath, nil
}

// quoteForEngine quotes an identifier for the given engine (reusing the dialect quoter).
func quoteForEngine(engine, ident string) (string, error) {
	if engine == "mysql" {
		return quoteMySQLOpen(ident)
	}
	return quotePostgresOpen(ident)
}

// setConnSchema executes USE schema on a dedicated MySQL connection (identifier validated against allowlist + quoted).
func setConnSchema(ctx context.Context, conn *sql.Conn, schema string) error {
	quoted, err := quoteMySQLOpen(schema)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "USE "+quoted); err != nil {
		return errors.New("database: schema context apply failed")
	}
	return nil
}

// applySchemaContext restricts the schema on a writable connection (sole identifier interpolation path, validated against allowlist + quoted).
func applySchemaContext(ctx context.Context, engine string, conn *sql.Conn, schema string) error {
	if engine == "mysql" {
		quoted, err := quoteMySQLOpen(schema)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "USE "+quoted); err != nil {
			return errors.New("database: schema context apply failed")
		}
		return nil
	}
	quoted, err := quotePostgresOpen(schema)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "SET search_path = "+quoted); err != nil {
		return errors.New("database: schema context apply failed")
	}
	return nil
}

// applyConnRollbackSearchPath sets an ordered PostgreSQL search_path on a borrowed metadata connection
// and returns a cleanup function that resets search_path before returning the connection to sql.DB.
func applyConnRollbackSearchPath(ctx context.Context, conn *sql.Conn, searchPath []string) (func(), error) {
	pathSQL, err := postgresSearchPathSQL(searchPath, false)
	if err != nil {
		return func() {}, err
	}
	if _, err := conn.ExecContext(ctx, pathSQL); err != nil {
		return func() {}, errors.New("database: schema context apply failed")
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rctx, "RESET search_path")
	}, nil
}

// applyConnRollbackSchema restricts the schema on a borrowed metadata connection and returns a cleanup
// function that resets search_path on PostgreSQL before returning the connection to sql.DB.
func applyConnRollbackSchema(ctx context.Context, engine string, conn *sql.Conn, schema string) (func(), error) {
	if err := applySchemaContext(ctx, engine, conn, schema); err != nil {
		return func() {}, err
	}
	if engine == "mysql" {
		return func() {}, nil
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rctx, "RESET search_path")
	}, nil
}

// CancelExecution cancels all in-flight executions on the pool (dual-channel contract cancel semantics):
//  1. Server-side targeted cancellation (PG pg_cancel_backend / MySQL KILL QUERY via an independent
//     connection) — aborts only in-flight statements while keeping the connection and transaction usable so rollback can be confirmed;
//  2. Governance context cancellation — subsequent unsent statements are immediately classified as cancelled
//     (B4 contract required_suite: cancellation), and a cancelled ctx never produces new sends.
//
// Cancellation acknowledgment does not imply the outcome after commit send is known (connection-level ambiguity still maps to result_unknown).
func (a *baseAdapter) CancelExecution(ctx context.Context, pool *PurposePool) error {
	pool.cancelStatementBackends(ctx)
	pool.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(pool.execCancels))
	for _, c := range pool.execCancels {
		cancels = append(cancels, c)
	}
	pool.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

// CancelInflightStatements performs only targeted cancellation of in-flight statements (without cancelling
// the governance context or killing connections): dedicated to the execution service cancellation path —
// in-flight statements return with a Cancelled outcome while transaction rollback can still be confirmed
// on the original connection; the caller stops sending subsequent statements.
func CancelInflightStatements(ctx context.Context, a Adapter, pool *PurposePool) error {
	pool.cancelStatementBackends(ctx)
	return nil
}

// registerExecution registers an execution governance context and addressable ID.
func (p *PurposePool) registerExecution(ctx context.Context) (context.Context, func(), string) {
	execCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	id := "exec_" + newHandle()
	p.execCancels[id] = cancel
	p.mu.Unlock()
	return execCtx, func() {
		cancel()
		p.mu.Lock()
		delete(p.execCancels, id)
		delete(p.execBackends, id)
		p.mu.Unlock()
	}, id
}
