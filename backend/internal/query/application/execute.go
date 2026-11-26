// execute.go implements the execute_query_select command and cursor page fetching (Q005-Q007):
// single-SELECT safety check -> masking analysis -> dedicated connection cursor -> first page -> encrypted cursor row;
// subsequent pages are read from the in-process live cursor via Owner affinity (no cross-Pod RPC and no query replay).
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
	"gorm.io/gorm"
	"sync"
	"time"
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
	lastAccessed    time.Time
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
	// Refresh LRU activity immediately after ownership/auth checks pass so concurrent idle eviction
	// cannot target this active continuation while capability/audit/DB renewal work runs.
	if !s.registryTouchIf(tokenHash, live, s.now()) {
		s.expireCursorRow(ctx, row.ID)
		return nil, domain.ErrCursorExpired()
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
	s.registrySetExpires(tokenHash, newIdle)
	// M6: serialize concurrent page fetches for the same token via per-entry mutex (a cursor/connection cannot be read concurrently).
	live.mu.Lock()
	if !s.registryTouchIf(tokenHash, live, s.now()) {
		live.mu.Unlock()
		s.expireCursorRow(ctx, row.ID)
		return nil, domain.ErrCursorExpired()
	}
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
		return nil, mapCursorFetchError(ctx, err)
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
	if page.HasMore {
		s.registryTouch(tokenHash, s.now())
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
