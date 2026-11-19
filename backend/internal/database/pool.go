// pool.go Purpose connection pools and cursor registry.
// review/query pools enforce read-only sessions; execution pools are writable. Session variables
// may only come from sessionMutationAllowlist. Cursors bind to a dedicated connection (native target DB
// streaming semantics); losing the owner connection immediately yields ErrCursorExpired without replaying SQL.
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// AdapterOptions configures Adapter construction (injected by the composition root).
type AdapterOptions struct {
	// CapabilityResolver is injected by the service layer (datasource.ResolveCapabilities);
	// when nil, TestConnection returns target Unsupported (fail closed, never falsely succeeding).
	CapabilityResolver func(engine, mode, version string) (string, map[string]bool, error)
	// Fingerprint computes the identity fingerprint; when nil, uses an unkeyed canonical sha256 digest.
	Fingerprint func(identity ServerIdentity) string
	// SensitiveColumns is the lowercase set of sensitive column names (v1 vocabulary policy; refined in B11).
	SensitiveColumns map[string]struct{}
	// StatementTimeoutMS is applied to each pool (when > 0).
	StatementTimeoutMS int
	// LockTimeoutMS applies only to review/execution pools.
	LockTimeoutMS int
	// MaxOpenConns is the maximum open connections per pool; 0 uses the default.
	MaxOpenConns int
	// SchemaContext is the stage schema locked when acquiring a writable connection (resolved from frozen mapping).
	SchemaContext string
}

// queryer abstracts a read-only query execution point.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execer abstracts a session statement execution point.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var _ queryer = (*sql.Conn)(nil)
var _ queryer = (*sql.Tx)(nil)

// PurposePool is a single-purpose target database connection pool.
type PurposePool struct {
	engine    string // "mysql" | "postgresql"
	purpose   Purpose
	db        *sql.DB
	reg       *mysqlTLSRegistration
	opts      AdapterOptions
	closeHook func() // SEC-0027: budget release hook executed once on the first Close

	mu           sync.Mutex
	cursors      map[string]*liveCursor
	execCancels  map[string]context.CancelFunc
	execBackends map[string]int64 // Server backend identities of active execution connections
	closed       bool
}

// liveCursor is a live reference to a target database cursor (dedicated connection owner).
type liveCursor struct {
	handle      string
	purpose     Purpose
	pageSize    int
	pageOrdinal int
	conn        *sql.Conn
	tx          *sql.Tx // PG read-only transaction; nil on MySQL
	rows        *sql.Rows
	columns     []QueryColumn
	cancel      context.CancelFunc
	exhausted   bool
}

// maxCellBytes per-cell byte limit; oversized values are replaced with a placeholder (safe equivalent of legacy BLOB behavior).
const maxCellBytes = 1 << 20

// cellPlaceholder placeholder text for oversized or non-UTF8 binary cells.
const cellPlaceholder = "BLOB_FIELD_NOT_DISPLAYABLE"

// backendIdentityOf reads the server-side backend identity of a connection
// (PG pg_backend_pid; MySQL connection_id; returns 0 on failure = not target-cancellable).
func backendIdentityOf(ctx context.Context, engine string, conn *sql.Conn) int64 {
	q := "select pg_backend_pid()"
	if engine == "mysql" {
		q = "select connection_id()"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var pid int64
	if err := conn.QueryRowContext(queryCtx, q).Scan(&pid); err != nil {
		return 0
	}
	return pid
}

// trackExecutionBackend registers the server-side identity of an execution connection (for targeted cancellation addressing).
func (p *PurposePool) trackExecutionBackend(id string, backend int64) {
	p.mu.Lock()
	if p.execBackends == nil {
		p.execBackends = map[string]int64{}
	}
	if backend > 0 {
		p.execBackends[id] = backend
	}
	p.mu.Unlock()
}

// untrackExecutionBackend releases the registration.
func (p *PurposePool) untrackExecutionBackend(id string) {
	p.mu.Lock()
	delete(p.execBackends, id)
	p.mu.Unlock()
}

// cancelStatementBackends performs targeted cancellation of all in-flight execution statements (contract cancel semantics:
// PG sends cancel via an independent connection (pg_cancel_backend); MySQL uses KILL QUERY;
// does not kill the connection — transaction rollback can still be confirmed on the original connection).
func (p *PurposePool) cancelStatementBackends(ctx context.Context) {
	p.mu.Lock()
	backends := make(map[string]int64, len(p.execBackends))
	for k, v := range p.execBackends {
		backends[k] = v
	}
	p.mu.Unlock()
	if len(backends) == 0 {
		return
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := p.db.Conn(cancelCtx)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	for _, backend := range backends {
		var q string
		if p.engine == "mysql" {
			q = "KILL QUERY " + strconv.FormatInt(backend, 10)
		} else {
			q = "select pg_cancel_backend(" + strconv.FormatInt(backend, 10) + ")"
		}
		cctx, ccancel := context.WithTimeout(cancelCtx, 2*time.Second)
		_, _ = conn.ExecContext(cctx, q)
		ccancel()
	}
}

// openPool opens a pool for the given engine.
func openPool(ctx context.Context, engine string, ep Endpoint, purpose Purpose, cred Credential, mat *TLSMaterial, opts AdapterOptions) (*PurposePool, error) {
	if !ValidPurpose(purpose) {
		return nil, fmt.Errorf("database: invalid purpose %q", purpose)
	}
	var db *sql.DB
	var reg *mysqlTLSRegistration
	switch engine {
	case "mysql":
		cfg, mysqlReg, err := BuildMySQLConfig(ep, cred, mat, ConnectTimeout)
		if err != nil {
			return nil, err
		}
		reg = mysqlReg
		// SEC-0009: connect directly from Config via Connector (never FormatDSN + re-parse —
		// a DSN string cannot carry the guarded DialFunc, so serialization would silently drop the address guard).
		conn, cerr := gomysql.NewConnector(cfg)
		if cerr != nil {
			reg.close()
			return nil, errors.New("database: mysql pool open failed")
		}
		db = sql.OpenDB(conn)
	case "postgresql":
		ccfg, err := BuildPostgresConfig(ctx, ep, cred, mat, ConnectTimeout)
		if err != nil {
			return nil, err
		}
		db = sql.OpenDB(stdlib.GetConnector(*ccfg))
	default:
		return nil, fmt.Errorf("%w: engine %q", ErrUnsupportedTarget, engine)
	}
	maxOpen := opts.MaxOpenConns
	if maxOpen <= 0 {
		switch purpose {
		case PurposeReview:
			maxOpen = 4
		case PurposeQuery:
			maxOpen = 8
		case PurposeExecution:
			maxOpen = 4
		}
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxLifetime(30 * time.Minute)
	p := &PurposePool{
		engine: engine, purpose: purpose, db: db, reg: reg, opts: opts,
		cursors: map[string]*liveCursor{}, execCancels: map[string]context.CancelFunc{},
		execBackends: map[string]int64{},
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		reg.close()
		return nil, errors.New("database: pool unreachable")
	}
	return p, nil
}

// Close closes the pool and all its cursors.
func (p *PurposePool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	// SEC-0027: the budget release hook runs on the first Close and at most once. By convention
	// the hook releases a package-level semaphore slot (non-reentrant, no callback into this pool),
	// so running it under the lock has no deadlock surface.
	if p.closeHook != nil {
		hook := p.closeHook
		p.closeHook = nil
		hook()
	}
	for _, c := range p.cursors {
		c.cancel()
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if c.rows != nil {
			_ = c.rows.Close()
		}
		if c.tx != nil {
			_ = c.tx.Rollback()
		}
	}
	p.cursors = map[string]*liveCursor{}
	var err error
	if p.db != nil {
		err = p.db.Close()
	}
	if p.reg != nil {
		p.reg.close()
	}
	return err
}

// Purpose returns the pool's purpose.
func (p *PurposePool) Purpose() Purpose { return p.purpose }

// Engine returns the pool's dialect.
func (p *PurposePool) Engine() string { return p.engine }

// SetCloseHook registers a hook executed once on the pool's first Close (SEC-0027 pool budget release).
// Must be set after OpenPurposePool returns and before the pool is put into use; Close is idempotent,
// and the hook executes at most once without re-triggering. The hook must not call back into methods on this pool.
func (p *PurposePool) SetCloseHook(fn func()) {
	p.mu.Lock()
	p.closeHook = fn
	p.mu.Unlock()
}

// ---- SEC-0027 Single-Page Query Byte Budget (Infrastructure-Level Exhaustion Defense) ----
//
// The budget only constrains total materialized row bytes per FetchCursorPage call to prevent
// wide rows x large page sizes from exhausting control-plane memory; it is not a product row limit —
// the product contract still imposes no total row or query count limit (Q005), and budget overrun
// finishes via page truncation + normal pagination or a structured capacity error.
// Because the settings contract query namespace forbidden_fields explicitly excludes configuration
// items like maximum_bytes, the budget is a package-level constant and cannot be tuned via settings.

// maxPageBytes single-page byte budget (sum of cell text lengths + fixed overhead).
// 4MiB: shrinks the legacy upper bound of page_size 5000 x 1MiB cells (hundreds of MB / GB,
// SEC-0027 attack vector) to a safe magnitude under a 2GiB control-plane memory envelope.
const maxPageBytes = 4 << 20

// perCellOverheadBytes estimated serialization overhead per cell (JSON quotes/escaping and
// QueryValue wrapper), providing a non-zero byte cost floor for pages with many small rows.
const perCellOverheadBytes = 32

// ErrPageBudgetExceeded a single row exceeds the page budget: the row cannot be returned within
// budget, so the cursor terminates and the entire page fails with a structured capacity error (mapped to 1012 family at the application layer).
var ErrPageBudgetExceeded = errors.New("database: single row exceeds page byte budget")

// estimateRowBytes estimates the serialized byte size of a row (cell text lengths + fixed overhead).
// Estimation rationale: value text dominates serialized volume, while quotes/escaping are accounted for in perCell overhead.
func estimateRowBytes(row []QueryValue) int {
	n := 0
	for _, v := range row {
		n += len(v.Text) + perCellOverheadBytes
	}
	return n
}

// pageBudgetAdmit decides whether to admit a single row into the page (pure decision for the page budget loop, easy to unit-test):
//   - fits: used + rowBytes <= maxPageBytes -> admit row into page and continue fetching;
//   - overflow: page budget reached but page already has rows — still admit the row (it has already
//     been read from the cursor stream, and discarding it would break cross-page row order continuity),
//     then stop fetching and continue pagination as usual with HasMore/NextCursor (appears to the user
//     as a page with fewer rows, shaped like a tail page without failing the request);
//   - single row exceeds budget on an empty page: return ErrPageBudgetExceeded and terminate the cursor.
//
// The page upper bound is therefore maxPageBytes + one row (where a single row is bounded by maxCellBytes x column count).
func pageBudgetAdmit(used, rowBytes int) (appendRow bool, stop bool, err error) {
	if used+rowBytes <= maxPageBytes {
		return true, false, nil
	}
	if used == 0 {
		return false, true, ErrPageBudgetExceeded
	}
	return true, true, nil
}

// applySessionDefaults applies allowlisted session defaults (called after acquiring a dedicated connection).
// Variable names map by dialect: MySQL lacks statement_timeout/lock_timeout (equivalent to
// max_execution_time[ms] / innodb_lock_wait_timeout[s]); PG uses the contract variable names.
func (p *PurposePool) applySessionDefaults(ctx context.Context, ex execer) error {
	allow := sessionMutationAllowlist[p.purpose]
	set := func(name, value string) error {
		if !allow[name] {
			return fmt.Errorf("database: session var %q not allowlisted", name)
		}
		if !safeSessionValue(value) {
			return fmt.Errorf("database: session var %q value rejected", name)
		}
		_, err := ex.ExecContext(ctx, fmt.Sprintf("SET %s = %s", name, value))
		return err
	}
	if p.opts.StatementTimeoutMS > 0 {
		if p.engine == "mysql" {
			if err := set("max_execution_time", fmt.Sprintf("%d", p.opts.StatementTimeoutMS)); err != nil && !isMySQLUnknownSystemVariable(err) {
				return err
			}
		} else if err := set("statement_timeout", fmt.Sprintf("%d", p.opts.StatementTimeoutMS)); err != nil {
			return err
		}
	}
	if p.opts.LockTimeoutMS > 0 && (p.purpose == PurposeReview || p.purpose == PurposeExecution) {
		if p.engine == "mysql" {
			// innodb_lock_wait_timeout is in seconds; round up with a minimum of 1.
			secs := (p.opts.LockTimeoutMS + 999) / 1000
			if secs < 1 {
				secs = 1
			}
			if err := set("innodb_lock_wait_timeout", fmt.Sprintf("%d", secs)); err != nil && !isMySQLUnknownSystemVariable(err) {
				return err
			}
		} else if err := set("lock_timeout", fmt.Sprintf("%dms", p.opts.LockTimeoutMS)); err != nil {
			return err
		}
	}
	return nil
}

// safeSessionValue validates session variable values against the allowlist (numeric / ms units).
func safeSessionValue(v string) bool {
	if v == "" || len(v) > 20 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r == 'm' || r == 's' || r == 'M' || r == 'S':
		default:
			return false
		}
	}
	return true
}

// setSessionReadOnly enforces a read-only MySQL session (applied after acquiring each dedicated connection).
func (p *PurposePool) setSessionReadOnly(ctx context.Context, ex execer) error {
	if p.engine != "mysql" || !ReadOnlyPurpose(p.purpose) {
		return nil
	}
	if _, err := ex.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
		return errors.New("database: read-only session enforce failed")
	}
	return nil
}

// withReadQueryer acquires a read-only query channel:
// MySQL = dedicated connection + read-only session; PG = dedicated connection + READ ONLY transaction.
// Metadata and cursors use this channel; Explain on MySQL DML uses the mysqlReadOnly=false variant
// (MySQL rejects EXPLAIN DML outright inside a read-only session with error 1792; EXPLAIN itself
// does not execute the statement, and safety is guaranteed by the fixed EXPLAIN prefix + absence of ANALYZE, asserted separately by behavioral gates).
func (p *PurposePool) withReadQueryer(ctx context.Context, mysqlReadOnly bool, fn func(q queryer) error) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return errors.New("database: connection unavailable")
	}
	defer func() { _ = conn.Close() }()
	if err := p.applySessionDefaults(ctx, conn); err != nil {
		return err
	}
	if mysqlReadOnly {
		if err := p.setSessionReadOnly(ctx, conn); err != nil {
			return err
		}
	}
	if p.engine == "postgresql" {
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return errors.New("database: read-only transaction begin failed")
		}
		defer func() { _ = tx.Rollback() }()
		return fn(tx)
	}
	return fn(conn)
}

// withSchemaQueryer restricts the current schema context on a read-only channel before invoking the callback.
// MySQL uses USE <quoted>; PG uses SET LOCAL search_path = <quoted>;
// identifiers pass ValidateIdentifier + engine quoting, which is the sole interpolation path.
func (p *PurposePool) withSchemaQueryer(ctx context.Context, schema string, fn func(q queryer) error) error {
	if err := ValidateIdentifier(schema); err != nil {
		return err
	}
	var useStmt string
	if p.engine == "mysql" {
		quoted, err := quoteMySQLOpen(schema)
		if err != nil {
			return err
		}
		useStmt = "USE " + quoted
	}
	return p.withReadQueryer(ctx, true, func(q queryer) error {
		if p.engine == "postgresql" {
			tx, ok := q.(*sql.Tx)
			if !ok {
				return errors.New("database: postgresql read channel must be transactional")
			}
			quoted, err := quotePostgresOpen(schema)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = "+quoted); err != nil {
				return errors.New("database: schema context apply failed")
			}
		} else if _, err := q.(execer).ExecContext(ctx, useStmt); err != nil {
			return errors.New("database: schema context apply failed")
		}
		return fn(q)
	})
}

// acquireWriteConn acquires a writable dedicated connection (for execution pools only);
// locks the stage schema after acquisition when SchemaContext is configured.
func (p *PurposePool) acquireWriteConn(ctx context.Context) (*sql.Conn, error) {
	if ReadOnlyPurpose(p.purpose) {
		return nil, ErrReadOnlyViolation
	}
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, errors.New("database: connection unavailable")
	}
	if err := p.applySessionDefaults(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if p.engine == "mysql" {
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ WRITE"); err != nil {
			_ = conn.Close()
			return nil, errors.New("database: write session enforce failed")
		}
	}
	if p.opts.SchemaContext != "" {
		if err := applySchemaContext(ctx, p.engine, conn, p.opts.SchemaContext); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// registerCursor registers a live cursor.
func (p *PurposePool) registerCursor(c *liveCursor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cursors[c.handle] = c
}

// lookupCursor looks up a live cursor.
func (p *PurposePool) lookupCursor(handle string) (*liveCursor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrCursorExpired
	}
	c, ok := p.cursors[handle]
	if !ok || c == nil {
		return nil, ErrCursorExpired
	}
	return c, nil
}

// dropCursor removes and terminates a cursor.
// Order: cancel the governance context and close the connection first (immediately aborting in-flight
// statements), then clean up rows/transaction — rows.Close() waits for the result set to drain and
// would block for tens of seconds on in-flight SLEEP-style statements if called first.
func (p *PurposePool) dropCursor(handle string) {
	p.mu.Lock()
	c, ok := p.cursors[handle]
	if ok {
		delete(p.cursors, handle)
	}
	p.mu.Unlock()
	if ok && c != nil {
		if c.cancel != nil {
			c.cancel()
		}
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if c.rows != nil {
			_ = c.rows.Close()
		}
		if c.tx != nil {
			_ = c.tx.Rollback()
		}
	}
}

// cursorDangerousFunctions deny-list of side-effecting functions for the pre-execution cursor guard
// (sourced from query/domain.safety.dangerousFunctions — serves as an independent second line of defense
// at the adapter layer without importing the query domain; both sets must be equal, enforced against drift
// by pool_dangerous_functions_parity_test.go).
var cursorDangerousFunctions = map[string]bool{
	// PostgreSQL.
	"dblink": true, "pg_read_file": true, "pg_read_binary_file": true,
	"pg_ls_dir": true, "pg_stat_file": true,
	"pg_ls_logdir": true, "pg_ls_waldir": true, "pg_ls_archive_statusdir": true,
	"lo_import": true, "lo_export": true,
	"pg_terminate_backend": true, "pg_cancel_backend": true,
	"pg_reload_conf": true, "pg_switch_wal": true, "pg_rotate_logfile": true,
	"pg_advisory_lock": true, "pg_advisory_xact_lock": true,
	"pg_advisory_unlock": true, "pg_try_advisory_lock": true, "pg_try_advisory_xact_lock": true,
	"setval": true, "nextval": true, "set_config": true,
	"pg_notify": true, "pg_logical_emit_message": true,
	"pg_replication_origin_advance": true,
	"pg_sleep":                      true, "pg_sleep_for": true, "pg_sleep_until": true,
	// MySQL.
	"load_file": true, "get_lock": true, "release_lock": true,
	"is_free_lock": true, "is_used_lock": true,
	"benchmark": true, "sleep": true, "uuid_short": true,
	"master_pos_wait": true, "source_pos_wait": true,
	"wait_for_executed_gtid_set": true,
	"sys_exec":                   true, "sys_eval": true,
	// Common to both.
	"currval": true,
}

// validateCursorStatement cursor statement defense: single read-only statement, no semicolons,
// starts with SELECT/WITH, no INTO write surface, and no side-effecting function calls (SEC-0008:
// quoted identifier forms like "pg_read_file"(...) and `sleep`(1) are equivalent to bare words and
// matched after token-level unquoting). Lexical analysis runs once and is shared across all four checks.
//
// BUG-011: semicolon and statement-head checks previously used raw-string strings.Contains / HasPrefix,
// mistaking content inside comments and string literals for statement structure — `select 1 -- note; here`,
// `select 'a;b'`, and `/* c */ select 1` were falsely rejected as multi-statement or non-SELECT queries
// with 4011 QUERY_STATEMENT_REJECTED. Moving the checks to the token stream lets the lexer absorb strings
// and comments so checks apply only to actual statement structure without reducing security: real
// multi-statement separators are still top-level TokPunct ";", and real non-SELECT statements still
// do not have select/with as their first token.
func validateCursorStatement(stmt string, postgres bool) error {
	trimmed := strings.TrimSpace(stmt)
	if trimmed == "" {
		return ErrStatementRequired
	}
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	if postgres {
		profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	}
	lex, err := fingerprint.Tokenize([]byte(trimmed), profile.Flags())
	if err != nil {
		// Cannot prove safety (unterminated literal/comment, unclassifiable bytes): fail closed.
		return errors.New("database: cursor statement unparsable")
	}
	if len(lex.Tokens) == 0 {
		return errors.New("database: cursor statement must be a single SELECT")
	}
	for _, t := range lex.Tokens {
		if t.Kind == fingerprint.TokPunct && t.Text == ";" {
			return errors.New("database: cursor statement must be a single statement without semicolons")
		}
	}
	head := ""
	if lex.Tokens[0].Kind == fingerprint.TokWord {
		head = lex.Tokens[0].Text
	}
	if head != "select" && head != "with" {
		return errors.New("database: cursor statement must be a single SELECT")
	}
	if err := scanCursorWriteSurfaces(lex); err != nil {
		return err
	}
	return scanCursorDangerousCalls(lex)
}

// scanCursorWriteSurfaces rejects write targets that a SELECT can carry (NEW-QUERY-01):
// INTO OUTFILE/DUMPFILE/session variables enter the token stream after executable comments are expanded
// and must be intercepted. The cursor statement contract is a pure read-only single SELECT, so a bare
// INTO token can only be a write target (as an alias it is a reserved word that must be quoted, and
// quoted forms do not match bare-word scanning). FOR UPDATE/SHARE and LOCK IN SHARE MODE are intercepted
// by the strong domain classifier, keeping the adapter layer at a shallow validation boundary.
func scanCursorWriteSurfaces(lex fingerprint.LexOutput) error {
	for _, t := range lex.Tokens {
		if t.Kind == fingerprint.TokWord && t.Text == "into" {
			return ErrReadOnlyViolation
		}
	}
	return nil
}

// scanCursorDangerousCalls performs token-level scanning for side-effecting function calls
// (bare words and quoted identifiers, case-normalized; lexical failures fail closed in the caller).
func scanCursorDangerousCalls(lex fingerprint.LexOutput) error {
	for i, t := range lex.Tokens {
		var name string
		switch t.Kind {
		case fingerprint.TokWord:
			name = t.Text
		case fingerprint.TokQuotedIdent:
			name = strings.ToLower(t.Text)
		default:
			continue
		}
		if !cursorDangerousFunctions[name] {
			continue
		}
		if i+1 < len(lex.Tokens) && lex.Tokens[i+1].IsPunct("(") {
			return ErrReadOnlyViolation
		}
	}
	return nil
}

// normalizeValue sanitizes and normalizes a cell value.
// []byte (MySQL text protocol) attempts numeric normalization to keep Kind consistent across engines.
func normalizeValue(v any) QueryValue {
	switch t := v.(type) {
	case nil:
		return QueryValue{Kind: "null", Text: ""}
	case bool:
		if t {
			return QueryValue{Kind: "bool", Text: "true"}
		}
		return QueryValue{Kind: "bool", Text: "false"}
	case int64:
		return QueryValue{Kind: "int", Text: fmt.Sprintf("%d", t)}
	case float64:
		return QueryValue{Kind: "float", Text: strconv.FormatFloat(t, 'g', -1, 64)}
	case []byte:
		return normalizeBytes(t)
	case string:
		return normalizeBytes([]byte(t))
	case time.Time:
		return QueryValue{Kind: "string", Text: t.UTC().Format(time.RFC3339Nano)}
	default:
		return normalizeBytes([]byte(fmt.Sprintf("%v", t)))
	}
}

func normalizeBytes(b []byte) QueryValue {
	if len(b) > maxCellBytes {
		return QueryValue{Kind: "bytes_placeholder", Text: cellPlaceholder}
	}
	if len(b) > 0 {
		if n, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			return QueryValue{Kind: "int", Text: strconv.FormatInt(n, 10)}
		}
		if f, err := strconv.ParseFloat(string(b), 64); err == nil {
			return QueryValue{Kind: "float", Text: strconv.FormatFloat(f, 'g', -1, 64)}
		}
	}
	if !utf8.Valid(b) {
		return QueryValue{Kind: "bytes_placeholder", Text: cellPlaceholder}
	}
	return QueryValue{Kind: "string", Text: string(b)}
}

// newHandle generates a random cursor handle.
func newHandle() string {
	return "cur_" + uuid.NewString()
}

// hexSHA256 computes a SHA-256 hex digest.
func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// withExplainQueryer Explain-dedicated read channel: MySQL DML Explain does not run in a read-only
// session (engine limitation 1792); safety is guaranteed by the fixed EXPLAIN prefix and complete
// absence of ANALYZE. PG still uses a READ ONLY transaction.
func (p *PurposePool) withExplainQueryer(ctx context.Context, schema string, fn func(q queryer) error) error {
	if err := ValidateIdentifier(schema); err != nil {
		return err
	}
	isMySQL := p.engine == "mysql"
	return p.withReadQueryer(ctx, false, func(q queryer) error {
		if isMySQL {
			// Pooled connections may retain READ ONLY session state set by previous read paths;
			// explicitly restore READ WRITE so EXPLAIN DML does not non-deterministically fail with 1792.
			if _, err := q.(execer).ExecContext(ctx, "SET SESSION TRANSACTION READ WRITE"); err != nil {
				return errors.New("database: explain session reset failed")
			}
		}
		if p.engine == "postgresql" {
			tx, ok := q.(*sql.Tx)
			if !ok {
				return errors.New("database: postgresql read channel must be transactional")
			}
			quoted, err := quotePostgresOpen(schema)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = "+quoted); err != nil {
				return errors.New("database: schema context apply failed")
			}
		} else {
			quoted, err := quoteMySQLOpen(schema)
			if err != nil {
				return err
			}
			if _, err := q.(execer).ExecContext(ctx, "USE "+quoted); err != nil {
				return errors.New("database: schema context apply failed")
			}
		}
		return fn(q)
	})
}
