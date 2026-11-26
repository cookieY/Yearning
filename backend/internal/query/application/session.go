// session.go implements open_query_session / close_query_session commands and lazy session lifecycle management.
// A Session freezes the set of datasources accessible at creation time (Q002/Q003); subsequent flow edits
// do not alter active Sessions, while datasource state changes are resolved lazily via capability rows.
package application

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
)

// SessionCapabilityView conforms to the OpenAPI QuerySessionCapability schema.
type SessionCapabilityView struct {
	DatasourceID   string `json:"datasource_id"`
	DatasourceName string `json:"datasource_name"`
	State          string `json:"state"`
	CanQuery       bool   `json:"can_query"`
	CanExport      bool   `json:"can_export"`
}

// SessionExecutionHistoryView conforms to the OpenAPI QueryExecutionHistoryItem schema.
type SessionExecutionHistoryView struct {
	ID           string `json:"id"`
	DatasourceID string `json:"datasource_id"`
	SQL          string `json:"sql"`
	State        string `json:"state"`
	ElapsedMS    *int   `json:"elapsed_ms"`
	CreatedAt    string `json:"created_at"`
}

// SessionView conforms to the OpenAPI QuerySession schema.
type SessionView struct {
	ID           string                        `json:"id"`
	UserID       string                        `json:"user_id"`
	State        string                        `json:"state"`
	GrantID      *string                       `json:"grant_id"`
	Capabilities []SessionCapabilityView       `json:"capabilities"`
	Executions   []SessionExecutionHistoryView `json:"executions,omitempty"`
	CreatedAt    string                        `json:"created_at"`
}

// CreateSessionInput holds parameters for creating a query Session.
type CreateSessionInput struct {
	Actor          Actor
	FlowID         string
	GrantID        string // Required when approval mode is enabled
	DatasourceIDs  []string
	IdempotencyKey string
}

// CreateSession creates a query Session with a frozen snapshot of datasource capabilities.
func (s *Services) CreateSession(ctx context.Context, in CreateSessionInput) (*SessionView, bool, error) {
	route := "POST:/query-sessions"
	// OpenAPI does not define an IdempotencyKey parameter for createQuerySession: an empty key skips
	// the idempotency table (each request creates a new Session, and multiple Sessions may legally share a Grant).
	if in.IdempotencyKey == "" {
		if in.GrantID == "" && !s.loadQuerySettings(ctx, s.DB).ApprovalEnabled {
			s.ensureDatasourcesIdentity(ctx, in.Actor.UserID, in.FlowID, in.DatasourceIDs)
		}
		var out *SessionView
		txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, err := s.createSessionOnce(ctx, tx, in)
			if err != nil {
				return err
			}
			out = v
			return nil
		})
		if txErr != nil {
			return nil, false, txErr
		}
		return out, false, nil
	}
	reqHash := txutil.RequestHash(in.Actor.UserID, in.FlowID, in.GrantID, dsKey(in.DatasourceIDs))
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.FlowID, in.IdempotencyKey, reqHash, func() (*SessionView, error) {
		if in.GrantID == "" && !s.loadQuerySettings(ctx, s.DB).ApprovalEnabled {
			s.ensureDatasourcesIdentity(ctx, in.Actor.UserID, in.FlowID, in.DatasourceIDs)
		}
		var out *SessionView
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, err := s.createSessionOnce(ctx, tx, in)
			if err != nil {
				return err
			}
			out = v
			return nil
		}); txErr != nil {
			return nil, txErr
		}
		return out, nil
	})
}

func (s *Services) createSessionOnce(ctx context.Context, tx *gorm.DB, in CreateSessionInput) (*SessionView, error) {
	settings := s.loadQuerySettings(ctx, tx)
	now := s.now()
	var (
		caps      []flowCapability
		grantID   *string
		flowName  string
		approvalR bool
	)
	if settings.ApprovalEnabled {
		// Grant path: freeze the subset of capabilities authorized by a valid Grant.
		if in.GrantID == "" {
			return nil, domain.ErrGrantRequired()
		}
		var g persistence.QueryGrantModel
		if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
			Where("id = ?", in.GrantID).First(&g).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, domain.ErrGrantRequired()
			}
			return nil, err
		}
		if g.RequesterUserID != in.Actor.UserID {
			return nil, domain.ErrNotFound()
		}
		if g.State == string(domain.GrantRevoked) {
			return nil, domain.ErrGrantRevoked()
		}
		if g.State != string(domain.GrantActive) {
			return nil, domain.ErrGrantExpired()
		}
		if !g.ExpiresAt.After(now) {
			return nil, domain.ErrGrantExpired()
		}
		var gc []persistence.QueryGrantCapabilityModel
		if err := tx.WithContext(ctx).Where("grant_id = ?", g.ID).Find(&gc).Error; err != nil {
			return nil, err
		}
		requested := map[string]bool{}
		for _, id := range in.DatasourceIDs {
			requested[id] = true
		}
		for _, c := range gc {
			if len(requested) > 0 && !requested[c.DatasourceID] {
				continue
			}
			caps = append(caps, flowCapability{
				DatasourceID: c.DatasourceID, Name: c.DatasourceNameSnapshot,
				IdentityFP: c.DatasourceIdentityFP, CanQuery: c.CanQuery, CanExport: c.CanExport,
			})
		}
		gid := g.ID
		grantID = &gid
		approvalR = true
		if err := tx.WithContext(ctx).Raw(
			`select flow_name_snapshot from query_access_requests where id = ?`,
			g.QueryAccessRequestID).Scan(&flowName).Error; err != nil {
			return nil, err
		}
	} else {
		// Approval-free path: if the user is authorized for the query flow, the flow's capabilities define
		// the frozen scope (Q002, with no maximum expiration time).
		flow, err := s.loadQueryFlowForUser(ctx, tx, in.Actor.UserID, in.FlowID)
		if err != nil {
			return nil, err
		}
		flowName = flow.Name
		fc, err := s.loadFlowCapabilities(ctx, tx, in.FlowID)
		if err != nil {
			return nil, err
		}
		requested := map[string]bool{}
		for _, id := range in.DatasourceIDs {
			requested[id] = true
		}
		hasRequested := len(requested) > 0
		unavailable := false
		for _, c := range fc {
			if hasRequested && !requested[c.DatasourceID] {
				continue
			}
			if !c.Enabled || !isValidIdentityFingerprint(c.IdentityFP) {
				unavailable = true
			}
			caps = append(caps, c)
			delete(requested, c.DatasourceID)
		}
		if hasRequested && len(requested) > 0 {
			return nil, domain.ErrDsNotGranted()
		}
		if len(caps) > 0 && unavailable {
			return nil, datasourceUnavailable()
		}
	}
	if len(caps) == 0 {
		return nil, domain.ErrDsNotGranted()
	}
	sid := newID()
	row := &persistence.QuerySessionModel{
		ID: sid, UserIDSnapshot: in.Actor.UserID, UsernameSnapshot: in.Actor.Username,
		FlowIDSnapshot: in.FlowID, FlowNameSnapshot: flowName, GrantID: grantID,
		ApprovalRequired: approvalR, State: string(domain.SessionActive),
		Version: 1, CreatedAt: now, LastSeenAt: now,
	}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	for _, c := range caps {
		state, serr := s.resolveCapabilityState(ctx, tx, c)
		if serr != nil {
			return nil, serr
		}
		sc := &persistence.QuerySessionCapabilityModel{
			SessionID: sid, DatasourceID: c.DatasourceID,
			DatasourceNameSnapshot: c.Name, DatasourceIdentityFP: c.IdentityFP,
			State: state, CanQuery: c.CanQuery, CanExport: c.CanExport,
		}
		if err := tx.WithContext(ctx).Create(sc).Error; err != nil {
			return nil, err
		}
	}
	if err := s.emitQueryEvent(ctx, tx, EventSessionOpened, "query-sessions/"+sid, in.Actor,
		map[string]any{"session_id": sid, "datasource_count": len(caps),
			"approval_required": approvalR}); err != nil {
		return nil, err
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_session.opened",
		"query_sessions", sid, "open", "succeeded")); err != nil {
		return nil, err
	}
	return s.sessionViewLocked(ctx, tx, row), nil
}

// resolveCapabilityState maps the datasource's current state onto a frozen capability row
// (disabled -> datasource_unavailable; identity drift -> terminal identity_changed).
func (s *Services) resolveCapabilityState(ctx context.Context, tx *gorm.DB, c flowCapability) (string, error) {
	type dsRow struct {
		Enabled    bool    `gorm:"column:enabled"`
		IdentityFP *string `gorm:"column:identity_fingerprint"`
	}
	var ds dsRow
	if err := tx.WithContext(ctx).Raw(
		`select enabled, identity_fingerprint from datasources where id = ?`, c.DatasourceID).
		Scan(&ds).Error; err != nil {
		return "", err
	}
	if ds.IdentityFP == nil || *ds.IdentityFP == "" || *ds.IdentityFP != c.IdentityFP {
		return string(domain.CapIdentity), nil
	}
	if !ds.Enabled {
		return string(domain.CapUnavailable), nil
	}
	return string(domain.CapActive), nil
}

func (s *Services) sessionViewLocked(ctx context.Context, tx *gorm.DB, row *persistence.QuerySessionModel) *SessionView {
	var caps []persistence.QuerySessionCapabilityModel
	_ = tx.WithContext(ctx).Where("session_id = ?", row.ID).Order("datasource_name_snapshot").Find(&caps)
	view := &SessionView{
		ID: row.ID, UserID: row.UserIDSnapshot, State: row.State,
		GrantID:      row.GrantID,
		CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339Nano),
		Capabilities: []SessionCapabilityView{},
	}
	for _, c := range caps {
		view.Capabilities = append(view.Capabilities, SessionCapabilityView{
			DatasourceID: c.DatasourceID, DatasourceName: c.DatasourceNameSnapshot,
			State: c.State, CanQuery: c.CanQuery, CanExport: c.CanExport,
		})
	}
	return view
}

// GetSession loads a Session (restricted to its owner).
func (s *Services) GetSession(ctx context.Context, actor Actor, sessionID string) (*SessionView, error) {
	var row persistence.QuerySessionModel
	if err := s.DB.WithContext(ctx).Where("id = ?", sessionID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if row.UserIDSnapshot != actor.UserID {
		return nil, domain.ErrNotFound()
	}
	view := s.sessionViewLocked(ctx, s.DB, &row)
	var execRows []persistence.QueryExecutionModel
	_ = s.DB.WithContext(ctx).
		Where("session_id = ?", row.ID).
		Order("created_at desc, id desc").
		Limit(50).
		Find(&execRows)
	for _, ex := range execRows {
		plain, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
			Table:      "query_executions",
			PrimaryKey: ex.ID,
			Column:     "query_payload",
		}, crypto.Envelope{
			Ciphertext: ex.QueryPayloadCiphertext,
			Nonce:      ex.QueryPayloadNonce,
			KeyID:      ex.QueryPayloadKeyID,
			Algorithm:  ex.QueryPayloadAlgorithm,
		})
		if err != nil {
			continue
		}
		view.Executions = append(view.Executions, SessionExecutionHistoryView{
			ID:           ex.ID,
			DatasourceID: ex.DatasourceID,
			SQL:          string(plain),
			State:        ex.State,
			ElapsedMS:    ex.ElapsedMS,
			CreatedAt:    ex.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return view, nil
}

// ListSessions lists Sessions owned by the current user.
func (s *Services) ListSessions(ctx context.Context, actor Actor, limit int, after string) ([]SessionView, *string, error) {
	q := s.DB.WithContext(ctx).Model(&persistence.QuerySessionModel{}).
		Where("user_id_snapshot = ?", actor.UserID)
	if after != "" {
		q = q.Where("(created_at, id) < ((select created_at from query_sessions where id = ? and user_id_snapshot = ?), ?::uuid)", after, actor.UserID, after)
	}
	var rows []persistence.QuerySessionModel
	// limit+1 lookahead (F12 #005: read-side page envelope requires an accurate next_cursor).
	if err := q.Order("created_at desc, id desc").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]SessionView, 0, len(rows))
	for i := range rows {
		out = append(out, *s.sessionViewLocked(ctx, s.DB, &rows[i]))
	}
	if !hasMore || len(rows) == 0 {
		return out, nil, nil
	}
	return out, &rows[len(rows)-1].ID, nil
}

// CloseSessionInput holds parameters for closing a Session.
type CloseSessionInput struct {
	Actor          Actor
	SessionID      string
	Reason         string
	IdempotencyKey string
}

// CloseSession allows the owner to close a Session: immediately rejects new queries, expires open cursors,
// and requests cancellation of running SELECTs while recording their final outcome.
func (s *Services) CloseSession(ctx context.Context, in CloseSessionInput) (*SessionView, bool, error) {
	route := "POST:/query-sessions/{session_id}/closure"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.SessionID, in.Reason)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.SessionID, in.IdempotencyKey, reqHash, func() (*SessionView, error) {
		var out *SessionView
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, err := s.closeSessionOnce(ctx, tx, in)
			if err != nil {
				return err
			}
			out = v
			return nil
		}); txErr != nil {
			return nil, txErr
		}
		s.terminateSessionsCursors(ctx, []string{in.SessionID})
		return out, nil
	})
}

func (s *Services) closeSessionOnce(ctx context.Context, tx *gorm.DB, in CloseSessionInput) (*SessionView, error) {
	var row persistence.QuerySessionModel
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
		Where("id = ?", in.SessionID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if row.UserIDSnapshot != in.Actor.UserID {
		return nil, domain.ErrNotFound()
	}
	if _, terr := domain.SessionTransition(domain.SessionState(row.State), domain.SessionClose); terr != nil {
		// Already in a terminal state: idempotently return the current view.
		if domain.TerminalSession(domain.SessionState(row.State)) {
			return s.sessionViewLocked(ctx, tx, &row), nil
		}
		return nil, domain.ErrIllegal()
	}
	now := s.now()
	reason := in.Reason
	if reason == "" {
		reason = "user_closed"
	}
	res := tx.WithContext(ctx).Exec(`
		update query_sessions set state = ?, version = version + 1,
		    closed_at = ?, close_reason = ?, last_seen_at = ?
		where id = ? and version = ?`,
		string(domain.SessionClosed), now, reason, now, row.ID, row.Version)
	if res.Error != nil || res.RowsAffected != 1 {
		return nil, domain.ErrConcurrent()
	}
	// Immediately expire open cursor metadata rows (in-process live cursors are cleaned up by terminateSessionsCursors).
	if err := tx.WithContext(ctx).Exec(`
		update query_cursors set consumed_at = now()
		where consumed_at is null and execution_id in (
		    select id from query_executions where session_id = ?)`, row.ID).Error; err != nil {
		return nil, err
	}
	// Running queries: request cancellation and finalize with the known outcome (or result_unknown if unconfirmable).
	if err := tx.WithContext(ctx).Exec(`
		update query_executions set state = 'cancelled',
		    business_err_code = 4006, failure_name = 'QUERY_SESSION_CLOSED',
		    finished_at = now(), elapsed_ms = greatest(0, extract(epoch from (now() - created_at)) * 1000)::int
		where session_id = ? and state = 'running'`, row.ID).Error; err != nil {
		return nil, err
	}
	if err := s.emitQueryEvent(ctx, tx, EventSessionClosed, "query-sessions/"+row.ID, in.Actor,
		map[string]any{"session_id": row.ID, "reason": reason}); err != nil {
		return nil, err
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_session.closed",
		"query_sessions", row.ID, "close", "succeeded")); err != nil {
		return nil, err
	}
	var final persistence.QuerySessionModel
	if err := tx.WithContext(ctx).Where("id = ?", row.ID).First(&final).Error; err != nil {
		return nil, err
	}
	return s.sessionViewLocked(ctx, tx, &final), nil
}

// ensureSessionUsable verifies that a Session is usable (including lazy terminal advancement for Grant expiration/revocation,
// user deletion, and approval-toggle changes invalidating approval-free Sessions).
func (s *Services) ensureSessionUsable(ctx context.Context, sessionID string) (*persistence.QuerySessionModel, error) {
	var row persistence.QuerySessionModel
	if err := s.DB.WithContext(ctx).Where("id = ?", sessionID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if domain.TerminalSession(domain.SessionState(row.State)) {
		// Map terminal Session state to the appropriate error code (4004 after revocation, 4005 on expiration, 4006 otherwise).
		switch domain.SessionState(row.State) {
		case domain.SessionRevoked:
			return nil, domain.ErrGrantRevoked()
		case domain.SessionExpired:
			return nil, domain.ErrGrantExpired()
		}
		return nil, domain.ErrSessionClosed()
	}
	if row.GrantID != nil && *row.GrantID != "" {
		var g persistence.QueryGrantModel
		if err := s.DB.WithContext(ctx).Where("id = ?", *row.GrantID).First(&g).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, domain.ErrGrantRevoked()
			}
			return nil, err
		}
		switch domain.GrantState(g.State) {
		case domain.GrantRevoked, domain.GrantRelinquished:
			return nil, domain.ErrGrantRevoked()
		case domain.GrantExpired:
			return nil, domain.ErrGrantExpired()
		}
		if !g.ExpiresAt.After(s.now()) {
			return nil, domain.ErrGrantExpired()
		}
	}
	// User has been deleted (delete_user does not write to query tables; resolved lazily here).
	var userExists bool
	if err := s.DB.WithContext(ctx).Raw(
		`select exists(select 1 from users where id = ?)`, row.UserIDSnapshot).Scan(&userExists).Error; err != nil {
		return nil, err
	}
	if !userExists {
		return nil, domain.ErrSessionClosed()
	}
	// SEC-0007: re-verify permission group membership immediately -- once a member is removed, a group is deleted/disabled,
	// or a flow grant is revoked, existing Sessions lose eligibility immediately (per contract "current permission-group
	// membership", without waiting for the Scheduler's lazy membership_revoked sweep).
	var member bool
	if err := s.DB.WithContext(ctx).Raw(`
		select exists(
			select 1 from permission_group_flow_grants g
			join permission_groups pg on pg.id = g.permission_group_id
			join permission_group_members m on m.permission_group_id = pg.id
			where g.flow_id = ? and m.user_id = ? and pg.enabled)`,
		row.FlowIDSnapshot, row.UserIDSnapshot).Scan(&member).Error; err != nil {
		return nil, err
	}
	if !member {
		return nil, domain.ErrSessionClosed()
	}
	// Approval toggle re-enabled: approval-free direct-access eligibility is revoked immediately (PRD 06 §5).
	if !row.ApprovalRequired {
		settings := s.loadQuerySettings(ctx, s.DB)
		if settings.ApprovalEnabled {
			return nil, domain.ErrGrantRequired()
		}
	}
	return &row, nil
}
