// execute_cursor.go holds execute domain helpers and operations.
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"gorm.io/gorm"
	"strings"
	"time"
)

// sealedCursorState is the AEAD-sealed cursor state bound to page continuations.
type sealedCursorState struct {
	Handle       string `json:"handle"`
	MaskFlags    []bool `json:"mask_flags"`
	ExecutionID  string `json:"execution_id"`
	SessionID    string `json:"session_id"`
	DatasourceID string `json:"datasource_id"`
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
// connection cursor contract) under the query pool budget. When the budget is exhausted, it first sweeps expired
// cursors and, if still full, evicts the least-recently-used idle cursor before rejecting with errQueryPoolBusy.
func (s *Services) openQueryPool(ctx context.Context, ds *datasource.DatasourceModel, timeoutMS int) (database.Adapter, *database.PurposePool, error) {
	return s.openPoolWithSlot(ctx, ds, timeoutMS, func() (func(), error) {
		if release, err := acquireQueryPoolSlot(); err == nil {
			return release, nil
		}
		s.SweepCursors(ctx)
		if release, err := acquireQueryPoolSlot(); err == nil {
			return release, nil
		}
		s.evictLRUIdleCursor(ctx)
		return acquireQueryPoolSlot()
	})
}

// openMetadataPool opens a short-lived query-purpose connection pool for metadata browsing using the
// isolated metadataPoolSlots budget so 32 open user cursors never block schema/table/column lookups.
func (s *Services) openMetadataPool(ctx context.Context, ds *datasource.DatasourceModel, timeoutMS int) (database.Adapter, *database.PurposePool, error) {
	return s.openPoolWithSlot(ctx, ds, timeoutMS, acquireMetadataPoolSlot)
}

// openPoolWithSlot acquires a pool budget slot via acquire, resolves credentials/TLS (or invokes PoolFactory),
// opens the PurposePool, and binds slot release to the pool's first Close.
func (s *Services) openPoolWithSlot(
	ctx context.Context,
	ds *datasource.DatasourceModel,
	timeoutMS int,
	acquire func() (func(), error),
) (database.Adapter, *database.PurposePool, error) {
	release, aerr := acquire()
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
	cred, err := datasource.ResolveCredential(ctx, s.DB, s.Crypter, ds.ID, datasource.CredentialResolveOptions{
		Purposes: []string{"query", "review"},
	})
	if err != nil {
		return fail(datasourceUnavailable())
	}
	tlsPurposes := []string{cred.Purpose, "review"}
	if cred.Purpose == "review" {
		tlsPurposes = []string{"review", "query"}
	}
	tlsMat, err := datasource.ResolveTLSMaterial(ctx, s.DB, s.Crypter, ds.ID, tlsPurposes...)
	if err != nil {
		return fail(datasourceUnavailable())
	}
	ep := database.Endpoint{
		Host: ds.Host, Port: ds.Port,
		DatabaseName: ptr.Deref(ds.DatabaseName), CompatibilityMod: ds.CompatibilityMode,
	}
	opts := database.AdapterOptions{StatementTimeoutMS: timeoutMS}
	var adapter database.Adapter
	if ds.CompatibilityMode == "mysql" {
		adapter = database.NewMySQLAdapter(ep, opts)
	} else {
		adapter = database.NewPostgreSQLAdapter(ep, opts)
	}
	pool, err := adapter.OpenPurposePool(ctx, database.PurposeQuery,
		database.Credential{Username: cred.Username, Password: cred.Password}, tlsMat)
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
	c, err := datasource.OpenCredentialEnvelope(s.Crypter, cred)
	if err != nil {
		return nil, datasourceUnavailable()
	}
	return &queryCredentialPayload{Username: c.Username, Password: c.Password}, nil
}

type queryCredentialPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// openQueryTLS decrypts TLS material and splits the PEM bundle via datasource.OpenTLSEnvelope.
func (s *Services) openQueryTLS(tls *datasource.DatasourceTLSMaterialModel) (*database.TLSMaterial, error) {
	return datasource.OpenTLSEnvelope(s.Crypter, tls)
}

// insertExecution inserts an execution row in running state (with AEAD SQL payload + plaintext HMAC + fingerprint HMAC).
func (s *Services) insertExecution(ctx context.Context, execID string, session *persistence.QuerySessionModel,
	in ExecuteSelectInput, dsID string, postgres bool) error {
	env, err := s.Crypter.SealColumn(crypto.PurposePayload,
		crypto.ColumnRef{Table: "query_executions", PrimaryKey: execID, Column: "query_payload"}, []byte(in.SQL))
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
	env, err := s.Crypter.SealColumn(crypto.PurposePayload,
		crypto.ColumnRef{Table: "query_cursors", PrimaryKey: hashToken(token), Column: "cursor_state"}, stateJSON)
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
	if e.pool != nil {
		_ = e.pool.Close()
	}
	s.registryDrop(e.tokenHash)
}

func (s *Services) expireCursorRow(ctx context.Context, cursorID string) {
	if s.DB == nil || cursorID == "" {
		return
	}
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
	return hashutil.SHA256Hex([]byte("yearning-query-cursor:" + token))
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
	if e.lastAccessed.IsZero() {
		e.lastAccessed = s.now()
	}
	s.registry[e.tokenHash] = e
}

func (s *Services) registryGet(tokenHash string) *liveCursor {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	return s.registry[tokenHash]
}

func (s *Services) registryTouch(tokenHash string, at time.Time) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if e := s.registry[tokenHash]; e != nil {
		e.lastAccessed = at
	}
}

func (s *Services) registryTouchIf(tokenHash string, expected *liveCursor, at time.Time) bool {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	e := s.registry[tokenHash]
	if e == nil || e != expected {
		return false
	}
	e.lastAccessed = at
	return true
}

func (s *Services) registrySetExpires(tokenHash string, expiresAt time.Time) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if e := s.registry[tokenHash]; e != nil {
		e.expiresAt = expiresAt
	}
}

func (s *Services) registryDrop(tokenHash string) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	delete(s.registry, tokenHash)
}

// dropLive closes both the cursor and its pool (used when the first page already exhausts the cursor).
func (s *Services) dropLive(adapter database.Adapter, pool *database.PurposePool, cur *database.SelectCursor) {
	if adapter != nil {
		_ = adapter.CloseCursor(context.Background(), pool, cur)
	}
	if pool != nil {
		_ = pool.Close()
	}
}

func mapCursorFetchError(ctx context.Context, err error) error {
	if errors.Is(err, database.ErrCursorExpired) {
		return domain.ErrCursorExpired()
	}
	if errors.Is(err, database.ErrPageBudgetExceeded) {
		// SEC-0027: single row exceeds page byte budget -- cursor is terminated (terminateEntry
		// closes the pool and releases the slot), returning a structured capacity error (1012 family).
		return domain.ErrSystem()
	}
	if isTimeout(ctx, err) {
		return domain.ErrQueryTimeout()
	}
	return statementRejected()
}

// terminateEntry terminates a live cursor entry (cancels context, closes pool, and unregisters; mutually exclusive
// with in-flight fetches -- N4: cancellation happens outside the fetch boundary and never interleaves with cursor reads).
func (s *Services) terminateEntry(e *liveCursor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.adapter != nil {
		_ = e.adapter.CancelQuery(context.Background(), e.pool, e.cursor)
	}
	if e.pool != nil {
		_ = e.pool.Close()
	}
	s.registryDrop(e.tokenHash)
}

// evictLRUIdleCursor selects and terminates the single least-recently-used idle cursor whose
// idle duration meets cursorEvictMinIdle and whose per-cursor mutex is not currently held.
// Lock order safety: TryLock + immediate Unlock while holding regMu, then release regMu BEFORE
// calling terminateEntry(victim) so e.mu -> regMu inside registryDrop can never deadlock.
func (s *Services) evictLRUIdleCursor(ctx context.Context) bool {
	now := s.now()
	minIdle := s.effectiveCursorEvictMinIdle()
	s.regMu.Lock()
	var victim *liveCursor
	for _, e := range s.registry {
		if now.Sub(e.lastAccessed) < minIdle {
			continue
		}
		if !e.mu.TryLock() {
			continue
		}
		e.mu.Unlock()
		if victim == nil || e.lastAccessed.Before(victim.lastAccessed) {
			victim = e
		}
	}
	if victim != nil {
		delete(s.registry, victim.tokenHash)
	}
	s.regMu.Unlock()
	if victim == nil {
		return false
	}
	s.terminateEntry(victim)
	if s.DB != nil && victim.rowID != "" {
		s.expireCursorRow(ctx, victim.rowID)
	}
	return true
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
	if s.DB != nil {
		_ = s.DB.WithContext(ctx).Exec(`
			update query_cursors set consumed_at = now()
			where consumed_at is null and owner_instance_id = ? and expires_at < now()`,
			s.instance)
	}
}

// InstanceID returns this process's instance identifier (for tests and diagnostics).
func (s *Services) InstanceID() string { return s.instance }
