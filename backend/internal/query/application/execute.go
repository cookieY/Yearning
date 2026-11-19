// execute.go implements the execute_query_select command and cursor page fetching (Q005-Q007):
// single-SELECT safety check -> masking analysis -> dedicated connection cursor -> first page -> encrypted cursor row;
// subsequent pages are read from the in-process live cursor via Owner affinity (no cross-Pod RPC and no query replay).
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// QueryPoolFactory opens a query-purpose connection pool (used by tests to inject faults or proxies;
// returns (adapter, pool)).
type QueryPoolFactory func(ctx context.Context, ds *datasource.DatasourceModel, timeoutMS int) (database.Adapter, *database.PurposePool, error)

// MaskingSource supplies the sensitive column vocabulary. Production Services read flow_masking_rules by default;
// tests may override this explicitly. Read errors must be returned and must never degrade to an empty vocabulary.
type MaskingSource interface {
	Vocabulary(ctx context.Context, db *gorm.DB, flowID, datasourceID string) ([]string, error)
}

// MaskedPlaceholder is the irreversible masking placeholder (does not preserve length or format).
const MaskedPlaceholder = "***"

// absoluteCursorLifetime is the maximum absolute lifetime of a cursor (fixed at 6 hours by contract; 000003 CHECK).
const absoluteCursorLifetime = 6 * time.Hour

// liveCursor represents an in-process live cursor (Pod-affine; loss results in CURSOR_EXPIRED without replay;
// mu serializes concurrent page fetches for the same token since a single cursor/connection cannot be read concurrently).
type liveCursor struct {
	mu              sync.Mutex
	adapter         database.Adapter
	pool            *database.PurposePool
	cursor          *database.SelectCursor
	rowID           string
	executionID     string
	sessionID       string
	dsID            string
	tokenHash       string
	columnNames     []string
	colTypes        []string
	maskFlags       []bool
	defaultPageSize int
	rowsRead        int
	maxDisplayRows  int
	expiresAt       time.Time
	absolute        time.Time
}

// ExecuteSelectInput holds parameters for executing a SELECT query.
type ExecuteSelectInput struct {
	Actor        Actor
	SessionID    string
	DatasourceID string
	SchemaName   string
	SQL          string
	TimeoutMS    int
	PageSize     int
}

// QueryResultPageView conforms to the OpenAPI QueryResultPage schema.
type QueryResultPageView struct {
	ExecutionID string         `json:"execution_id"`
	Columns     []resultColumn `json:"columns"`
	Rows        [][]any        `json:"rows"`
	Page        pageInfo       `json:"page"`
	ElapsedMS   int            `json:"elapsed_ms,omitempty"`
}

type resultColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type pageInfo struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// sealedCursorState is the AEAD-sealed cursor state bound to page continuations.
type sealedCursorState struct {
	Handle       string `json:"handle"`
	MaskFlags    []bool `json:"mask_flags"`
	ExecutionID  string `json:"execution_id"`
	SessionID    string `json:"session_id"`
	DatasourceID string `json:"datasource_id"`
}

// ExecuteSelect executes a SELECT query and returns the first page (Query Safety Check is inlined
// with no separate check endpoint; results are never persisted, while cursor metadata is stored encrypted).
func (s *Services) ExecuteSelect(ctx context.Context, in ExecuteSelectInput) (*QueryResultPageView, error) {
	// 1. Session usability check (including lazy terminal state advancement).
	session, err := s.ensureSessionUsable(ctx, in.SessionID)
	if err != nil {
		return nil, err
	}
	if session.UserIDSnapshot != in.Actor.UserID {
		return nil, domain.ErrNotFound()
	}
	// 2. Capability row: frozen scope + lazy state resolution.
	cap, err := s.refreshCapability(ctx, in.SessionID, in.DatasourceID)
	if err != nil {
		return nil, err
	}
	if !cap.CanQuery {
		return nil, domain.ErrDsNotGranted()
	}
	switch domain.CapabilityState(cap.State) {
	case domain.CapIdentity:
		return nil, domain.ErrDsNotGranted()
	case domain.CapUnavailable:
		return nil, datasourceUnavailable()
	}
	// 3. Timeout (1..300000 ms; default 30s; Q005 imposes no total row, byte, or query count limits).
	settings := s.loadQuerySettings(ctx, s.DB)
	timeoutMS := in.TimeoutMS
	if timeoutMS == 0 {
		timeoutMS = settings.DefaultTimeoutMS
	}
	if timeoutMS < 1 || timeoutMS > 300000 {
		return nil, domain.ErrTimeoutRange()
	}
	maxResultRows := settings.MaxResultRows
	if maxResultRows <= 0 {
		maxResultRows = 1000
	}
	pageSize := in.PageSize
	if pageSize == 0 {
		pageSize = 500
	}
	if pageSize < 1 || pageSize > 5000 {
		return nil, domain.ErrValidation()
	}
	effectivePageSize := pageSize
	if effectivePageSize > maxResultRows {
		effectivePageSize = maxResultRows
	}
	if in.SchemaName == "" || len(in.SchemaName) > 128 {
		return nil, domain.ErrValidation()
	}
	// 4. Query Safety Check (semicolon-separated SELECT batch, F-Q-001: allows sequential execution
	// of multiple statements with strict per-statement classification, returning the result set of the final statement;
	// dialect follows the datasource compatibility mode).
	dsRow, err := s.loadDatasourceRow(ctx, in.DatasourceID)
	if err != nil {
		return nil, err
	}
	postgres := dsRow.CompatibilityMode == "postgresql"
	stmts, verr := domain.CheckSelectBatch(in.SQL, postgres)
	if verr != nil {
		return nil, verr
	}
	// 5. Masking analysis (vocabulary -> masking plan; computed against the final statement that produces the result set).
	vocab, err := s.maskingVocabulary(ctx, session.FlowIDSnapshot, in.DatasourceID)
	if err != nil {
		return nil, err
	}
	plan, merr := domain.AnalyzeMasking(stmts[len(stmts)-1], postgres, vocab)
	if merr != nil {
		return nil, merr
	}
	// 6. Persist execution row first (in running state; SQL payload sealed with AEAD + dual HMACs).
	started := s.now()
	execID := newID()
	if err := s.insertExecution(ctx, execID, session, in, dsRow.ID, postgres); err != nil {
		return nil, err
	}
	var failureDetail string
	finish := func(state string, code int, name string) {
		s.finishWithEvent(ctx, execID, in.Actor, state, code, name, failureDetail)
	}
	// 7. Open dedicated pool + cursor and fetch the first page.
	adapter, pool, err := s.openQueryPool(ctx, dsRow, timeoutMS)
	if err != nil {
		if errors.Is(err, errQueryPoolBusy) {
			// SEC-0027: global pool budget backpressure -- return structured "system busy" (1012 family)
			// rather than masquerading as datasource unavailable.
			finish("failed", int(domain.CodeSystemCapacity), "SYSTEM_CAPACITY_EXHAUSTED")
			return nil, domain.ErrSystem()
		}
		finish("failed", 3010, "DATASOURCE_UNAVAILABLE")
		return nil, datasourceUnavailable()
	}
	cur, err := adapter.OpenSelectCursor(ctx, pool, database.OpenCursorOptions{
		StatementSQL: stmts[len(stmts)-1], PriorStatements: stmts[:len(stmts)-1],
		SchemaName: in.SchemaName, PageSize: effectivePageSize,
	})
	if err != nil {
		_ = pool.Close()
		if isAdapterRejection(err) {
			finish("failed", 4007, "QUERY_ONLY_SINGLE_SELECT")
			return nil, domain.ErrOnlySingleSelect()
		}
		if isTimeout(ctx, err) {
			finish("timed_out", 4009, "QUERY_TIMEOUT")
			return nil, domain.ErrQueryTimeout()
		}
		failureDetail = adapterDetail(err)
		finish("failed", 4011, "QUERY_STATEMENT_REJECTED")
		return nil, statementRejectedWith(adapterDetail(err))
	}
	page, err := adapter.FetchCursorPage(ctx, pool, cur, 0)
	if err != nil {
		s.dropLive(adapter, pool, cur)
		if errors.Is(err, database.ErrPageBudgetExceeded) {
			// SEC-0027: single row exceeds page byte budget -- cursor is already terminated; conclude with a
			// structured capacity error (1012 family) without returning the oversized row to the frontend.
			failureDetail = "single_row_page_budget_exceeded"
			finish("failed", int(domain.CodeSystemCapacity), "SYSTEM_CAPACITY_EXHAUSTED")
			return nil, domain.ErrSystem()
		}
		if isTimeout(ctx, err) {
			finish("timed_out", 4009, "QUERY_TIMEOUT")
			return nil, domain.ErrQueryTimeout()
		}
		failureDetail = adapterDetail(err)
		finish("failed", 4011, "QUERY_STATEMENT_REJECTED")
		return nil, statementRejectedWith(adapterDetail(err))
	}
	// 8. Masking flags (first-page column names -> boolean mask set, reused across subsequent pages).
	columnNames := make([]string, 0, len(page.Columns))
	for _, c := range page.Columns {
		columnNames = append(columnNames, c.Name)
	}
	maskFlags := plan.ColumnMask(columnNames)
	// 9. Post-fetch guard: if a revocation, close, or expiration raced with execution, discard the result without returning it to the client.
	if guard := s.postFetchGuard(ctx, session); guard != nil {
		s.dropLive(adapter, pool, cur)
		finish("cancelled", int(guard.Code), guard.Name)
		return nil, guard
	}
	elapsed := int(time.Since(started).Milliseconds())
	if len(page.Rows) >= maxResultRows && !(in.PageSize == 1 && maxResultRows == 1) {
		page.Rows = page.Rows[:maxResultRows]
		page.HasMore = false
		page.NextCursor = ""
	}
	rows := maskRows(page.Rows, maskFlags)
	// Column types: inferred from the first row of the first page (defaults to "string" for empty results or nil values).
	colTypes := inferColumnTypes(columnNames, page.Rows)
	var nextToken string
	var rowID string
	if page.HasMore {
		token, tokErr := newCursorToken()
		if tokErr != nil {
			s.dropLive(adapter, pool, cur)
			s.finishWithEvent(ctx, execID, in.Actor, "failed", 1012, "SYSTEM_CAPACITY_EXHAUSTED")
			return nil, domain.ErrSystem()
		}
		nextToken = token
	}
	// 10. Persist terminal execution state + cursor row + event + audit in a single atomic transaction
	// (N1: cursor row is persisted in the same transaction -- rollback on failure leaves no orphan row,
	// discards the fetched first page, and returns the error to the client).
	txErr := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if page.HasMore {
			state := sealedCursorState{
				Handle: cur.Handle(), MaskFlags: maskFlags, ExecutionID: execID,
				SessionID: session.ID, DatasourceID: in.DatasourceID,
			}
			var insErr error
			rowID, insErr = s.insertCursorRowTx(ctx, tx, nextToken, &state, s.now())
			if insErr != nil {
				return insErr
			}
		}
		if ferr := s.finishExecutionTx(ctx, tx, execID, "succeeded", 0, ""); ferr != nil {
			return ferr
		}
		if eerr := s.emitQueryEvent(ctx, tx, EventExecCompleted, "query-executions/"+execID, in.Actor,
			map[string]any{"execution_id": execID, "rows_first_page": len(rows)}); eerr != nil {
			return eerr
		}
		return s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query.execution_completed",
			"query_executions", execID, "execute", "succeeded"))
	})
	if txErr != nil {
		s.dropLive(adapter, pool, cur)
		// Reconciliation fallback: record failure if the terminal state transaction fails (without returning side effects).
		s.finishWithEvent(ctx, execID, in.Actor, "failed", 1012, "SYSTEM_CAPACITY_EXHAUSTED")
		return nil, domain.ErrSystem()
	}
	if page.HasMore {
		s.registryAdd(&liveCursor{
			adapter: adapter, pool: pool, cursor: cur, rowID: rowID,
			executionID: execID, sessionID: session.ID, dsID: in.DatasourceID,
			tokenHash: hashToken(nextToken), columnNames: columnNames,
			colTypes: colTypes, maskFlags: maskFlags,
			defaultPageSize: effectivePageSize,
			rowsRead:        len(rows),
			maxDisplayRows:  maxResultRows,
			expiresAt:       s.now().Add(s.cursorIdle), absolute: s.now().Add(absoluteCursorLifetime),
		})
	} else {
		s.dropLive(adapter, pool, cur)
	}
	view := &QueryResultPageView{
		ExecutionID: execID, Rows: rows,
		Page: pageInfo{HasMore: page.HasMore}, ElapsedMS: elapsed,
	}
	for i, n := range columnNames {
		t := "string"
		if i < len(colTypes) {
			t = colTypes[i]
		}
		view.Columns = append(view.Columns, resultColumn{Name: n, Type: t})
	}
	if nextToken != "" {
		view.Page.NextCursor = &nextToken
	}
	return view, nil
}

// inferColumnTypes infers column types from the first row values (L6: no longer hardcodes "string").
func inferColumnTypes(names []string, rows [][]database.QueryValue) []string {
	out := make([]string, len(names))
	for i := range out {
		out[i] = "string"
	}
	if len(rows) == 0 {
		return out
	}
	for j := 0; j < len(names) && j < len(rows[0]); j++ {
		switch rows[0][j].Kind {
		case "int", "float":
			out[j] = "number"
		case "bool":
			out[j] = "boolean"
		case "null":
			out[j] = "string"
		}
	}
	return out
}

// finishWithEvent persists the failed terminal state, domain event, and audit log (M4: failure path emits
// query.execution_failed; all three writes run in a single best-effort transaction).
func (s *Services) finishWithEvent(ctx context.Context, execID string, actor Actor,
	state string, code int, name string, detail ...string) {
	_ = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if ferr := s.finishExecutionTx(ctx, tx, execID, state, code, name); ferr != nil {
			return ferr
		}
		if state != "succeeded" {
			meta := map[string]any{"execution_id": execID, "failure_name": name}
			// BUG-010: include truncated, credential-free diagnostic text in failure event metadata.
			if len(detail) > 0 && detail[0] != "" {
				meta["detail"] = detail[0]
			}
			if eerr := s.emitQueryEvent(ctx, tx, EventExecFailed, "query-executions/"+execID, actor,
				meta); eerr != nil {
				return eerr
			}
		}
		return s.Recorder.Audit(ctx, tx, auditInput(actor, "query.execution_failed",
			"query_executions", execID, "execute", "failed"))
	})
}

// postFetchGuard checks whether the Session or Grant was revoked, closed, or expired before the page arrived.
func (s *Services) postFetchGuard(ctx context.Context, session *persistence.QuerySessionModel) *domain.Error {
	if _, err := s.ensureSessionUsable(ctx, session.ID); err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return de
		}
		return domain.ErrSystem()
	}
	return nil
}

// FetchPageInput holds parameters for fetching the next cursor page.
type FetchPageInput struct {
	Actor       Actor
	ExecutionID string
	CursorToken string
	Purpose     string // display|export
	// PageSize overrides the page size for continuation fetches (RCP-20260901-QUERY-PAGE-SIZE-PARAM):
	// 0 keeps the cursor's current page size; 1..5000 overrides it and persists for subsequent reads.
	PageSize int
}

// FetchPage reads the next page from a cursor (Owner Pod affinity; validates Session, Grant, capability,
// and purpose on every page; export requires can_export and records an audit event).
func (s *Services) FetchPage(ctx context.Context, in FetchPageInput) (*QueryResultPageView, error) {
	if in.Purpose != "display" && in.Purpose != "export" {
		return nil, domain.ErrValidation()
	}
	if in.PageSize < 0 || in.PageSize > 5000 {
		return nil, domain.ErrValidation()
	}
	tokenHash := hashToken(in.CursorToken)
	var row persistence.QueryCursorModel
	if err := s.DB.WithContext(ctx).Where("cursor_token_hash = ?", tokenHash).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrCursorInvalid()
		}
		return nil, err
	}
	now := s.now()
	if row.ConsumedAt != nil || !row.ExpiresAt.After(now) || !row.AbsoluteExpiresAt.After(now) {
		s.expireCursorRow(ctx, row.ID)
		s.registryDrop(tokenHash)
		return nil, domain.ErrCursorExpired()
	}
	if row.OwnerInstanceID != s.instance {
		// Non-Owner Pod: never replay the query (contract owner_loss); the affinity cookie is repaired
		// at the L7 routing layer since this Pod holds no live cursor to continue.
		return nil, domain.ErrCursorExpired()
	}
	live := s.registryGet(tokenHash)
	if live == nil {
		s.expireCursorRow(ctx, row.ID)
		return nil, domain.ErrCursorExpired()
	}
	if live.executionID != in.ExecutionID || live.executionID != row.ExecutionID {
		return nil, domain.ErrCursorInvalid()
	}
	var exec persistence.QueryExecutionModel
	if err := s.DB.WithContext(ctx).Where("id = ?", in.ExecutionID).First(&exec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if exec.SessionID != live.sessionID {
		return nil, domain.ErrCursorInvalid()
	}
	session, err := s.ensureSessionUsable(ctx, live.sessionID)
	if err != nil {
		s.terminateEntry(live)
		return nil, err
	}
	if session.UserIDSnapshot != in.Actor.UserID {
		return nil, domain.ErrNotFound()
	}
	cap, err := s.refreshCapability(ctx, live.sessionID, live.dsID)
	if err != nil {
		return nil, err
	}
	if in.Purpose == "export" {
		if !cap.CanExport {
			return nil, domain.ErrExportNotGranted()
		}
		if err := s.Recorder.Audit(ctx, nil, auditInput(in.Actor, "query.export_page_read",
			"query_executions", in.ExecutionID, "export_read", "succeeded")); err != nil {
			return nil, err
		}
	}
	// Renew idle expiration (capped by the absolute cursor lifetime).
	newIdle := now.Add(s.cursorIdle)
	if newIdle.After(row.AbsoluteExpiresAt) {
		newIdle = row.AbsoluteExpiresAt
	}
	if err := s.DB.WithContext(ctx).Exec(
		`update query_cursors set expires_at = ? where id = ?`, newIdle, row.ID).Error; err != nil {
		return nil, err
	}
	live.expiresAt = newIdle
	// M6: serialize concurrent page fetches for the same token via per-entry mutex (a cursor/connection cannot be read concurrently).
	live.mu.Lock()
	fetchSize := in.PageSize
	if in.Purpose == "display" && live.maxDisplayRows > 0 {
		remaining := live.maxDisplayRows - live.rowsRead
		if remaining <= 0 {
			live.mu.Unlock()
			s.terminateEntry(live)
			s.expireCursorRow(ctx, row.ID)
			view := &QueryResultPageView{
				ExecutionID: in.ExecutionID,
				Rows:        [][]any{},
				Page:        pageInfo{HasMore: false},
			}
			for i, n := range live.columnNames {
				t := "string"
				if i < len(live.colTypes) {
					t = live.colTypes[i]
				}
				view.Columns = append(view.Columns, resultColumn{Name: n, Type: t})
			}
			return view, nil
		}
		effective := fetchSize
		if effective == 0 {
			effective = live.defaultPageSize
		}
		if effective > remaining {
			fetchSize = remaining
		}
	}
	page, err := live.adapter.FetchCursorPage(ctx, live.pool, live.cursor, fetchSize)
	if err == nil {
		if in.PageSize > 0 {
			live.defaultPageSize = in.PageSize
		}
		if in.Purpose == "display" {
			live.rowsRead += len(page.Rows)
			if live.maxDisplayRows > 0 && live.rowsRead >= live.maxDisplayRows {
				page.HasMore = false
				page.NextCursor = ""
			}
		}
	}
	live.mu.Unlock()
	if err != nil {
		s.terminateEntry(live)
		s.expireCursorRow(ctx, row.ID)
		if errors.Is(err, database.ErrPageBudgetExceeded) {
			// SEC-0027: single row exceeds page byte budget -- cursor is terminated (terminateEntry
			// closes the pool and releases the slot), returning a structured capacity error (1012 family).
			return nil, domain.ErrSystem()
		}
		if isTimeout(ctx, err) {
			return nil, domain.ErrQueryTimeout()
		}
		return nil, statementRejected()
	}
	// M5: post-fetch guard (symmetric with ExecuteSelect): if revocation, relinquish, close, or expiration
	// occurred during the fetch window, discard the page without returning it to the frontend.
	if guard := s.postFetchGuard(ctx, session); guard != nil {
		s.detachEntry(live)
		s.expireCursorRow(ctx, row.ID)
		return nil, guard
	}
	rows := maskRows(page.Rows, live.maskFlags)
	streamed := int64(len(rows))
	if page.HasMore {
		if err := s.DB.WithContext(ctx).Exec(`
			update query_cursors set next_page_number = next_page_number + 1 where id = ?`, row.ID).Error; err != nil {
			return nil, err
		}
	} else {
		s.terminateEntry(live)
		s.expireCursorRow(ctx, row.ID)
	}
	if err := s.DB.WithContext(ctx).Exec(`
		update query_executions set streamed_row_count = streamed_row_count + ? where id = ?`,
		streamed, in.ExecutionID).Error; err != nil {
		return nil, err
	}
	view := &QueryResultPageView{
		ExecutionID: in.ExecutionID,
		Rows:        rows,
		Page:        pageInfo{HasMore: page.HasMore},
	}
	for i, n := range live.columnNames {
		t := "string"
		if i < len(live.colTypes) {
			t = live.colTypes[i]
		}
		view.Columns = append(view.Columns, resultColumn{Name: n, Type: t})
	}
	if page.NextCursor != "" {
		// Keep using the live cursor handle as the continuation token (do not issue a new token).
		t := in.CursorToken
		view.Page.NextCursor = &t
	}
	return view, nil
}

// ---- Internal helpers ----

func datasourceUnavailable() *domain.Error {
	return &domain.Error{Code: 3010, Name: "DATASOURCE_UNAVAILABLE"}
}

// statementRejected (§18.73): rejected query-domain statements (e.g. read-only violations) use the dedicated
// query code 4011 -- previously reused 2003, which collided with review-domain SQL_PARSE_FAILED.
func statementRejected() *domain.Error {
	return &domain.Error{Code: 4011, Name: "QUERY_STATEMENT_REJECTED"}
}

// adapterDetail extracts the stable failure classification name from an adapter error (SEC-0030):
// raw driver text (host/port/user/table names/SQL fragments) is never returned to query users -- only
// the sanitized FailureName from database.SanitizedError may leave the boundary; unclassified errors omit detail.
func adapterDetail(err error) string {
	var se *database.SanitizedError
	if errors.As(err, &se) {
		return se.Error()
	}
	return ""
}

func statementRejectedWith(detail string) *domain.Error {
	e := statementRejected()
	if detail != "" {
		e.WithDetail(detail)
	}
	return e
}

func isAdapterRejection(err error) bool {
	return errors.Is(err, database.ErrReadOnlyViolation) ||
		errors.Is(err, database.ErrIdentifierInvalid) ||
		errors.Is(err, database.ErrStatementRequired)
}

func isTimeout(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "timeout")
}

// loadDatasourceRow loads a datasource row (read-only access to datasource/**).
func (s *Services) loadDatasourceRow(ctx context.Context, dsID string) (*datasource.DatasourceModel, error) {
	var ds datasource.DatasourceModel
	if err := s.DB.WithContext(ctx).Where("id = ?", dsID).First(&ds).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrDsNotGranted()
		}
		return nil, err
	}
	return &ds, nil
}

// refreshCapability lazily refreshes a capability row's state (handling datasource disable/reenable and identity drift).
func (s *Services) refreshCapability(ctx context.Context, sessionID, dsID string) (*persistence.QuerySessionCapabilityModel, error) {
	var capRow persistence.QuerySessionCapabilityModel
	if err := s.DB.WithContext(ctx).Where(
		"session_id = ? and datasource_id = ?", sessionID, dsID).First(&capRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrDsNotGranted()
		}
		return nil, err
	}
	fc := flowCapability{
		DatasourceID: capRow.DatasourceID, Name: capRow.DatasourceNameSnapshot,
		IdentityFP: capRow.DatasourceIdentityFP, CanQuery: capRow.CanQuery, CanExport: capRow.CanExport,
	}
	state, err := s.resolveCapabilityState(ctx, s.DB, fc)
	if err != nil {
		return nil, err
	}
	if state != capRow.State {
		next, terr := domain.CapabilityTransition(domain.CapabilityState(capRow.State), toCapabilityTrigger(state))
		if terr == nil {
			_ = s.DB.WithContext(ctx).Exec(
				`update query_session_capabilities set state = ? where session_id = ? and datasource_id = ?`,
				string(next), sessionID, dsID)
			capRow.State = string(next)
		}
	}
	return &capRow, nil
}

func toCapabilityTrigger(target string) domain.CapabilityTrigger {
	switch domain.CapabilityState(target) {
	case domain.CapUnavailable:
		return domain.CapDatasourceUnavailable
	case domain.CapActive:
		return domain.CapSameIdentityRecovered
	default:
		return domain.CapDifferentIdentity
	}
}

// maskingVocabulary loads the sensitive column vocabulary; any read or parse error is propagated and fails closed.
func (s *Services) maskingVocabulary(ctx context.Context, flowID, dsID string) ([]string, error) {
	if s.MaskingSource == nil {
		return nil, errors.New("query: masking source unavailable")
	}
	return s.MaskingSource.Vocabulary(ctx, s.DB, flowID, dsID)
}

// openQueryPool opens a query-purpose connection pool (one pool per execution: per-request timeout + dedicated
// connection cursor contract; credential envelope AAD matches datasource.Factory field for field --
// replicated read-only since datasource/** is outside B11 allowed_paths; payload is {username,password}).
// SEC-0027: acquires a global pool slot before opening and binds release to the pool's first Close (covering execution,
// pagination, and metadata close paths); budget exhaustion returns errQueryPoolBusy (mapped by callers to
// the 1012 family "system busy" error, distinct from datasource unavailable).
func (s *Services) openQueryPool(ctx context.Context, ds *datasource.DatasourceModel, timeoutMS int) (database.Adapter, *database.PurposePool, error) {
	release, aerr := acquireQueryPoolSlot()
	if aerr != nil {
		return nil, nil, errQueryPoolBusy
	}
	fail := func(err error) (database.Adapter, *database.PurposePool, error) {
		release()
		return nil, nil, err
	}
	if s.PoolFactory != nil {
		adapter, pool, err := s.PoolFactory(ctx, ds, timeoutMS)
		if err != nil {
			return fail(err)
		}
		pool.SetCloseHook(release)
		return adapter, pool, nil
	}
	cred, resolvedPurpose, err := s.loadReadOnlyQueryCredential(ctx, ds.ID)
	if err != nil {
		return fail(datasourceUnavailable())
	}
	plain, err := s.openQueryCredential(cred)
	if err != nil {
		return fail(err)
	}
	var tlsMat *database.TLSMaterial
	var tlsRow datasource.DatasourceTLSMaterialModel
	tlsPurposes := []string{resolvedPurpose}
	if resolvedPurpose == "query" {
		tlsPurposes = append(tlsPurposes, "review")
	} else {
		tlsPurposes = append(tlsPurposes, "query")
	}
	for _, tlsPurpose := range tlsPurposes {
		if err := s.DB.WithContext(ctx).Where(
			"datasource_id = ? and purpose = ?", ds.ID, tlsPurpose).First(&tlsRow).Error; err == nil {
			mat, merr := s.openQueryTLS(&tlsRow)
			if merr != nil {
				return fail(datasourceUnavailable())
			}
			tlsMat = mat
			break
		}
	}
	ep := database.Endpoint{
		Host: ds.Host, Port: ds.Port,
		DatabaseName: derefString(ds.DatabaseName), CompatibilityMod: ds.CompatibilityMode,
	}
	opts := database.AdapterOptions{StatementTimeoutMS: timeoutMS}
	var adapter database.Adapter
	if ds.CompatibilityMode == "mysql" {
		adapter = database.NewMySQLAdapter(ep, opts)
	} else {
		adapter = database.NewPostgreSQLAdapter(ep, opts)
	}
	pool, err := adapter.OpenPurposePool(ctx, database.PurposeQuery,
		database.Credential{Username: plain.Username, Password: plain.Password}, tlsMat)
	if err != nil {
		return fail(datasourceUnavailable())
	}
	// Bind slot lifetime to the pool: released idempotently on first Close with zero leaks.
	pool.SetCloseHook(release)
	return adapter, pool, nil
}

// loadReadOnlyQueryCredential resolves an enabled read-only credential for query execution,
// preferring purpose='query' and falling back to purpose='review' (never execution).
func (s *Services) loadReadOnlyQueryCredential(
	ctx context.Context,
	datasourceID string,
) (*datasource.DatasourceCredentialModel, string, error) {
	for _, candidate := range []string{"query", "review"} {
		var cred datasource.DatasourceCredentialModel
		err := s.DB.WithContext(ctx).Where(
			"datasource_id = ? and purpose = ? and enabled", datasourceID, candidate).First(&cred).Error
		if err == nil {
			return &cred, candidate, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", err
		}
	}
	return nil, "", gorm.ErrRecordNotFound
}

func (s *Services) openQueryCredential(cred *datasource.DatasourceCredentialModel) (*queryCredentialPayload, error) {
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: cred.CredentialCiphertext, Nonce: cred.CredentialNonce,
		KeyID: cred.CredentialKeyID, Algorithm: cred.CredentialAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "datasource_credentials",
		PrimaryKey: cred.ID, Column: "credential", Purpose: "datasource_credential",
	})
	if err != nil {
		return nil, datasourceUnavailable()
	}
	var p queryCredentialPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, datasourceUnavailable()
	}
	return &p, nil
}

type queryCredentialPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// openQueryTLS decrypts TLS material and splits the PEM bundle (present but undecryptable maps to
// datasource unavailable; fails closed and never downgrades to plaintext).
func (s *Services) openQueryTLS(tls *datasource.DatasourceTLSMaterialModel) (*database.TLSMaterial, error) {
	if tls.MaterialCiphertext == nil || tls.MaterialKeyID == nil {
		return nil, nil
	}
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: tls.MaterialCiphertext, Nonce: tls.MaterialNonce,
		KeyID: *tls.MaterialKeyID, Algorithm: derefString(tls.MaterialAlgorithm),
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "datasource_tls_materials",
		PrimaryKey: tls.ID, Column: "material", Purpose: "datasource_tls",
	})
	if err != nil {
		return nil, err
	}
	mat := &database.TLSMaterial{
		VerifyServer: tls.VerifyServer, ServerName: derefString(tls.ServerName),
	}
	var certs [][]byte
	for rest := plain; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			certs = append(certs, pem.EncodeToMemory(block))
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			mat.KeyPEM = pem.EncodeToMemory(block)
		}
	}
	if len(certs) > 0 {
		caCerts := certs
		if mat.KeyPEM != nil {
			mat.CertPEM = certs[len(certs)-1]
			caCerts = certs[:len(certs)-1]
		}
		for _, c := range caCerts {
			mat.CAPEM = append(mat.CAPEM, c...)
		}
	}
	return mat, nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// insertExecution inserts an execution row in running state (with AEAD SQL payload + plaintext HMAC + fingerprint HMAC).
func (s *Services) insertExecution(ctx context.Context, execID string, session *persistence.QuerySessionModel,
	in ExecuteSelectInput, dsID string, postgres bool) error {
	env, err := s.sealPayload("query_executions", execID, "query_payload", []byte(in.SQL))
	if err != nil {
		return err
	}
	sqlHMAC, err := s.hmacSQL([]byte(in.SQL))
	if err != nil {
		return err
	}
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	if postgres {
		profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	}
	// C002 fail-closed: fingerprint engine failure is no longer a skippable bypass -- an empty fingerprint would
	// violate query_executions.fingerprint_hmac NOT NULL/CHECK constraints and surface as a 500 error
	// (F12 #011). Statement-level failures map to the declared QUERY_STATEMENT_REJECTED code,
	// while engine construction failures are treated as system capacity errors.
	var fpHMAC string
	engine, eerr := fingerprint.NewEngine(fingerprint.CryptoSigner{C: s.Crypter}, fingerprint.Options{})
	if eerr != nil {
		return domain.ErrSystem()
	}
	res, aerr := engine.Analyze(profile, []byte(in.SQL), nil)
	if aerr != nil {
		return statementRejected()
	}
	fpHMAC = res.CanonicalFingerprintHMAC
	timeout := in.TimeoutMS
	if timeout == 0 {
		timeout = s.loadQuerySettings(ctx, s.DB).DefaultTimeoutMS
	}
	row := &persistence.QueryExecutionModel{
		ID: execID, SessionID: session.ID, UserIDSnapshot: session.UserIDSnapshot,
		DatasourceID:           dsID,
		QueryPayloadCiphertext: env.Ciphertext, QueryPayloadNonce: env.Nonce,
		QueryPayloadKeyID: env.KeyID, QueryPayloadAlgorithm: env.Algorithm,
		SQLHMAC: sqlHMAC, FingerprintHMAC: fpHMAC,
		State: "running", TimeoutMS: timeout, CreatedAt: s.now(),
	}
	return s.DB.WithContext(ctx).Create(row).Error
}

// insertCursorRowTx inserts the cursor metadata row within the given connection/transaction (AEAD state;
// stores only the token hash; M4: shares the same transaction as the terminal execution state).
func (s *Services) insertCursorRowTx(ctx context.Context, db *gorm.DB, token string, state *sealedCursorState, now time.Time) (string, error) {
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	env, err := s.sealPayload("query_cursors", hashToken(token), "cursor_state", stateJSON)
	if err != nil {
		return "", err
	}
	rowID := newID()
	row := &persistence.QueryCursorModel{
		ID: rowID, ExecutionID: state.ExecutionID,
		CursorTokenHash: hashToken(token), OwnerInstanceID: s.instance,
		FenceToken: 1, Purpose: "display",
		CursorStateCiphertext: env.Ciphertext, CursorStateNonce: env.Nonce,
		CursorStateKeyID: env.KeyID, CursorStateAlgorithm: env.Algorithm,
		NextPageNumber: 2, CreatedAt: now,
		ExpiresAt: now.Add(s.cursorIdle), AbsoluteExpiresAt: now.Add(absoluteCursorLifetime),
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return "", err
	}
	return rowID, nil
}

// finishExecutionTx updates the execution row to its terminal state within a transaction.
func (s *Services) finishExecutionTx(ctx context.Context, tx *gorm.DB, execID, state string, code int, name string) error {
	now := s.now()
	elapsed := 0
	var row persistence.QueryExecutionModel
	if err := tx.WithContext(ctx).Where("id = ?", execID).First(&row).Error; err == nil {
		elapsed = int(now.Sub(row.CreatedAt).Milliseconds())
		if elapsed < 0 {
			elapsed = 0
		}
	}
	var codePtr *int
	var namePtr *string
	if state != "succeeded" {
		c, n := code, name
		codePtr, namePtr = &c, &n
	}
	return tx.WithContext(ctx).Exec(`
		update query_executions set state = ?, elapsed_ms = ?, finished_at = ?,
		    business_err_code = ?, failure_name = ?
		where id = ? and state = 'running'`, state, elapsed, now, codePtr, namePtr, execID).Error
}

// detachEntry detaches a live cursor entry (H4 revocation/relinquish semantics: do not forcibly cancel
// an in-flight statement; let it finish naturally so the post-fetch guard discards the result, and close the connection gracefully with the pool).
func (s *Services) detachEntry(e *liveCursor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.pool.Close()
	s.registryDrop(e.tokenHash)
}

func (s *Services) expireCursorRow(ctx context.Context, cursorID string) {
	_ = s.DB.WithContext(ctx).Exec(
		`update query_cursors set consumed_at = now() where id = ? and consumed_at is null`, cursorID)
}

func newCursorToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	return sha256Hex([]byte("yearning-query-cursor:" + token))
}

func maskRows(rows [][]database.QueryValue, flags []bool) [][]any {
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		mr := make([]any, 0, len(r))
		for j, v := range r {
			if j < len(flags) && flags[j] {
				mr = append(mr, MaskedPlaceholder)
				continue
			}
			switch v.Kind {
			case "null":
				mr = append(mr, nil)
			case "bool", "int", "float":
				mr = append(mr, json.Number(v.Text))
			default:
				mr = append(mr, v.Text)
			}
		}
		out = append(out, mr)
	}
	return out
}

// ---- In-process live cursor registry ----

func (s *Services) registryAdd(e *liveCursor) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if s.registry == nil {
		s.registry = map[string]*liveCursor{}
	}
	s.registry[e.tokenHash] = e
}

func (s *Services) registryGet(tokenHash string) *liveCursor {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	return s.registry[tokenHash]
}

func (s *Services) registryDrop(tokenHash string) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	delete(s.registry, tokenHash)
}

// dropLive closes both the cursor and its pool (used when the first page already exhausts the cursor).
func (s *Services) dropLive(adapter database.Adapter, pool *database.PurposePool, cur *database.SelectCursor) {
	_ = adapter.CloseCursor(context.Background(), pool, cur)
	_ = pool.Close()
}

// terminateEntry terminates a live cursor entry (cancels context, closes pool, and unregisters; mutually exclusive
// with in-flight fetches -- N4: cancellation happens outside the fetch boundary and never interleaves with cursor reads).
func (s *Services) terminateEntry(e *liveCursor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.adapter.CancelQuery(context.Background(), e.pool, e.cursor)
	_ = e.pool.Close()
	s.registryDrop(e.tokenHash)
}

// terminateSessionsCursors terminates all live cursors belonging to a set of Sessions (session close
// semantics: request cancellation of running SELECTs and record the final outcome per contract postcondition
// running_select_cancel_requested_and_final_outcome_recorded).
func (s *Services) terminateSessionsCursors(ctx context.Context, sessionIDs []string) {
	s.regMu.Lock()
	var victims []*liveCursor
	for _, sid := range sessionIDs {
		for _, e := range s.registry {
			if e.sessionID == sid {
				victims = append(victims, e)
			}
		}
	}
	s.regMu.Unlock()
	for _, e := range victims {
		s.terminateEntry(e)
		s.expireCursorRow(ctx, e.rowID)
	}
}

// detachSessionsCursors implements revocation/relinquish semantics (H4: running_query_not_force_
// stopped) -- expires cursor rows and detaches live entries without targeted cancellation: in-flight statements
// complete naturally while racing first/continuation pages are discarded by postFetchGuard without reaching the frontend.
func (s *Services) detachSessionsCursors(ctx context.Context, sessionIDs []string) {
	s.regMu.Lock()
	var victims []*liveCursor
	for _, sid := range sessionIDs {
		for _, e := range s.registry {
			if e.sessionID == sid {
				victims = append(victims, e)
			}
		}
	}
	s.regMu.Unlock()
	for _, e := range victims {
		s.detachEntry(e)
		s.expireCursorRow(ctx, e.rowID)
	}
}

// SweepCursors sweeps expired unconsumed cursor rows (in DB) and live entries (in process) owned by this instance.
// Invoked opportunistically during execution, page fetches, and scheduler ticks.
func (s *Services) SweepCursors(ctx context.Context) {
	now := s.now()
	s.regMu.Lock()
	var victims []*liveCursor
	for _, e := range s.registry {
		if !e.expiresAt.After(now) || !e.absolute.After(now) {
			victims = append(victims, e)
		}
	}
	s.regMu.Unlock()
	for _, e := range victims {
		s.terminateEntry(e)
		s.expireCursorRow(ctx, e.rowID)
	}
	_ = s.DB.WithContext(ctx).Exec(`
		update query_cursors set consumed_at = now()
		where consumed_at is null and owner_instance_id = ? and expires_at < now()`,
		s.instance)
}

// InstanceID returns this process's instance identifier (for tests and diagnostics).
func (s *Services) InstanceID() string { return s.instance }
