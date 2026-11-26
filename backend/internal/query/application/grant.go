// grant.go implements revoke_query_grant and related commands for the Grant lifecycle.
// Revokers are frozen query flow approvers or builtin admins (Q004; admins can only terminate access
// without gaining query or reveal privileges); revocation reason is required and visible to the grantee;
// relinquishment by the owner is irreversible.
package application

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
)

// GrantView conforms to the OpenAPI QueryGrant schema.
type GrantView struct {
	ID                string  `json:"id"`
	RequesterUserID   string  `json:"requester_user_id"`
	HolderName        *string `json:"holder_name,omitempty"`
	State             string  `json:"state"`
	RevokedReason     *string `json:"revoked_reason"`
	RevokedByUsername *string `json:"revoked_by_username"`
	RevokedAt         *string `json:"revoked_at"`
	ExpiresAt         *string `json:"expires_at"`
	Version           int64   `json:"version"`
	CreatedAt         string  `json:"created_at"`
	CanRevoke         bool    `json:"can_revoke"`
}

func grantView(g *persistence.QueryGrantModel) *GrantView {
	var exp *string
	if !g.ExpiresAt.IsZero() {
		v := g.ExpiresAt.UTC().Format(time.RFC3339Nano)
		exp = &v
	}
	var revokedAt *string
	if g.RevokedAt != nil {
		v := g.RevokedAt.UTC().Format(time.RFC3339Nano)
		revokedAt = &v
	}
	return &GrantView{
		ID: g.ID, RequesterUserID: g.RequesterUserID, State: g.State,
		RevokedReason: g.RevokeReason, RevokedByUsername: g.RevokedByUsername,
		RevokedAt: revokedAt, ExpiresAt: exp,
		Version: g.Version, CreatedAt: g.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// GetGrant loads a Grant (visible to the grantee or frozen approvers).
func (s *Services) GetGrant(ctx context.Context, actor Actor, grantID string) (*GrantView, error) {
	g, err := s.loadGrant(ctx, grantID)
	if err != nil {
		return nil, err
	}
	ok, err := s.canReadGrant(ctx, actor, g)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	view := grantView(g)
	canRevokeByID, err := s.canRevokeMap(ctx, s.DB, actor, []persistence.QueryGrantModel{*g})
	if err != nil {
		return nil, err
	}
	view.CanRevoke = canRevokeByID[g.ID]
	return view, nil
}

func (s *Services) loadGrant(ctx context.Context, grantID string) (*persistence.QueryGrantModel, error) {
	var g persistence.QueryGrantModel
	if err := s.DB.WithContext(ctx).Where("id = ?", grantID).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	return &g, nil
}

// canReadGrant checks whether the caller is the grantee or a frozen approver of the originating access request
// (builtin admins do not receive a query-domain read surface -- authorization-policy restricts actors to owner/reviewer with default deny).
func (s *Services) canReadGrant(ctx context.Context, actor Actor, g *persistence.QueryGrantModel) (bool, error) {
	if g.RequesterUserID == actor.UserID {
		return true, nil
	}
	var exists bool
	err := s.DB.WithContext(ctx).Raw(`
		select exists(
			select 1 from query_access_approval_steps st
			join query_access_approval_actors a on a.step_id = st.id
			where st.request_id = ? and a.reviewer_user_id_snapshot = ?)`,
		g.QueryAccessRequestID, actor.UserID).Scan(&exists).Error
	return exists, err
}

// ListGrants lists Grants (relationship-filtered to grantee or frozen approvers, matching GetGrant
// semantics with no global admin read surface).
func (s *Services) ListGrants(ctx context.Context, actor Actor, limit int, after string) ([]GrantView, *string, error) {
	q := s.DB.WithContext(ctx).Model(&persistence.QueryGrantModel{}).
		Where(`requester_user_id_snapshot = ? or exists (
			select 1 from query_access_approval_steps st
			join query_access_approval_actors a on a.step_id = st.id
			where st.request_id = query_grants.query_access_request_id
			  and a.reviewer_user_id_snapshot = ?)`, actor.UserID, actor.UserID)
	if after != "" {
		q = q.Where("(created_at, id) < ((select created_at from query_grants where id = ?), ?::uuid)", after, after)
	}
	var rows []persistence.QueryGrantModel
	// limit+1 lookahead (F12 #005: read-side page envelope requires an accurate next_cursor).
	if err := q.Order("created_at desc, id desc").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	canRevokeByID, err := s.canRevokeMap(ctx, s.DB, actor, rows)
	if err != nil {
		return nil, nil, err
	}
	out := make([]GrantView, 0, len(rows))
	for i := range rows {
		view := grantView(&rows[i])
		view.CanRevoke = canRevokeByID[rows[i].ID]
		// UX-005: populate grantee display name from the frozen username snapshot (preserving historical identity evidence even after user deletion).
		if rows[i].RequesterUsername != "" {
			holder := rows[i].RequesterUsername
			view.HolderName = &holder
		}
		out = append(out, *view)
	}
	if !hasMore || len(rows) == 0 {
		return out, nil, nil
	}
	return out, &rows[len(rows)-1].ID, nil
}

// canRevokeMap computes authoritative grant revocation eligibility (active grant AND builtin admin or
// frozen approver of the originating access request) in a single query.
func (s *Services) canRevokeMap(ctx context.Context, db *gorm.DB, actor Actor, rows []persistence.QueryGrantModel) (map[string]bool, error) {
	out := make(map[string]bool, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	if actor.IsBuiltinAdmin {
		for i := range rows {
			if rows[i].State == string(domain.GrantActive) {
				out[rows[i].ID] = true
			}
		}
		return out, nil
	}
	if actor.UserID == "" {
		return out, nil
	}
	reqIDs := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		if rows[i].State != string(domain.GrantActive) || rows[i].QueryAccessRequestID == "" {
			continue
		}
		if _, ok := seen[rows[i].QueryAccessRequestID]; !ok {
			seen[rows[i].QueryAccessRequestID] = struct{}{}
			reqIDs = append(reqIDs, rows[i].QueryAccessRequestID)
		}
	}
	if len(reqIDs) == 0 {
		return out, nil
	}
	var eligibleReqs []string
	if err := db.WithContext(ctx).Raw(`
		select distinct st.request_id::text
		from query_access_approval_steps st
		join query_access_approval_actors a on a.step_id = st.id
		where st.request_id in ? and a.reviewer_user_id_snapshot = ?`,
		reqIDs, actor.UserID).Scan(&eligibleReqs).Error; err != nil {
		return nil, err
	}
	reqSet := make(map[string]struct{}, len(eligibleReqs))
	for _, rid := range eligibleReqs {
		reqSet[rid] = struct{}{}
	}
	for i := range rows {
		if rows[i].State == string(domain.GrantActive) {
			if _, ok := reqSet[rows[i].QueryAccessRequestID]; ok {
				out[rows[i].ID] = true
			}
		}
	}
	return out, nil
}

// RevokeInput holds parameters for revoking a Grant.
type RevokeInput struct {
	Actor          Actor
	GrantID        string
	Reason         string
	ExpectVersion  int64
	IdempotencyKey string
}

// Revoke revokes a Grant (callable by a frozen approver or builtin admin; reason is required; cascades
// revoked state to Sessions and detaches running queries so any subsequent in-flight results are discarded).
func (s *Services) Revoke(ctx context.Context, in RevokeInput) (*GrantView, bool, error) {
	route := "POST:/query-grants/{grant_id}/revocations"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.GrantID, in.Reason)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.GrantID, in.IdempotencyKey, reqHash, func() (*GrantView, error) {
		var out *GrantView
		var affectedSessions []string
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, sessions, err := s.revokeOnce(ctx, tx, in)
			if err != nil {
				return err
			}
			out = v
			affectedSessions = sessions
			return nil
		}); txErr != nil {
			return nil, txErr
		}
		// After commit, detach live cursors across all Sessions of this Grant (H4: do not force-stop running queries;
		// in-flight results are discarded by postFetchGuard, and new requests immediately fail with 4004).
		s.detachSessionsCursors(ctx, affectedSessions)
		return out, nil
	})
}

func (s *Services) revokeOnce(ctx context.Context, tx *gorm.DB, in RevokeInput) (*GrantView, []string, error) {
	if in.Reason == "" || len(in.Reason) > 4096 {
		return nil, nil, domain.ErrValidation()
	}
	var g persistence.QueryGrantModel
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
		Where("id = ?", in.GrantID).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, domain.ErrNotFound()
		}
		return nil, nil, err
	}
	if in.ExpectVersion != g.Version {
		return nil, nil, domain.ErrConcurrent()
	}
	// Revocation eligibility: builtin admin or frozen approver of the Grant's originating access request (Q004).
	if !in.Actor.IsBuiltinAdmin {
		var isReviewer bool
		if err := tx.WithContext(ctx).Raw(`
			select exists(
				select 1 from query_access_approval_steps st
				join query_access_approval_actors a on a.step_id = st.id
				where st.request_id = ? and a.reviewer_user_id_snapshot = ?)`,
			g.QueryAccessRequestID, in.Actor.UserID).Scan(&isReviewer).Error; err != nil {
			return nil, nil, err
		}
		if !isReviewer {
			return nil, nil, domain.ErrNotFound()
		}
	}
	if _, terr := domain.GrantTransition(domain.GrantState(g.State), domain.GrantRevoke); terr != nil {
		return nil, nil, domain.ErrIllegal()
	}
	now := s.now()
	res := tx.WithContext(ctx).Exec(`
		update query_grants set state = ?, version = version + 1,
		    revoked_by_user_id_snapshot = ?, revoked_by_username_snapshot = ?,
		    revoke_reason = ?, revoked_at = ?, terminal_at = ?, terminal_reason = 'revoked'
		where id = ? and version = ?`,
		string(domain.GrantRevoked), in.Actor.UserID, in.Actor.Username,
		in.Reason, now, now, g.ID, g.Version)
	if res.Error != nil || res.RowsAffected != 1 {
		return nil, nil, domain.ErrConcurrent()
	}
	// Cascade: transition active Sessions under the Grant to revoked (revocation visibility is aggregated on the read surface).
	var sessions []string
	if err := tx.WithContext(ctx).Raw(`
		update query_sessions set state = ?, version = version + 1,
		    closed_at = ?, close_reason = 'grant_revoked'
		where grant_id = ? and state = 'active'
		returning id::text`, string(domain.SessionRevoked), now, g.ID).Scan(&sessions).Error; err != nil {
		return nil, nil, err
	}
	if err := s.emitQueryEvent(ctx, tx, EventGrantRevoked, "query-grants/"+g.ID, in.Actor,
		map[string]any{"grant_id": g.ID, "from": "active", "to": "revoked",
			"reason_code": "revoked_by_frozen_reviewer_or_admin",
			"revoked_by":  in.Actor.Username}); err != nil {
		return nil, nil, err
	}
	for _, sid := range sessions {
		if err := s.emitQueryEvent(ctx, tx, EventSessionRevReq, "query-sessions/"+sid, in.Actor,
			map[string]any{"session_id": sid, "grant_id": g.ID,
				"reason": "grant_revoked"}); err != nil {
			return nil, nil, err
		}
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_grant.revoked",
		"query_grants", g.ID, "revoke", "succeeded")); err != nil {
		return nil, nil, err
	}
	var final persistence.QueryGrantModel
	if err := tx.WithContext(ctx).Where("id = ?", g.ID).First(&final).Error; err != nil {
		return nil, nil, err
	}
	return grantView(&final), sessions, nil
}

// RelinquishInput holds parameters for relinquishing a Grant.
type RelinquishInput struct {
	Actor          Actor
	GrantID        string
	Reason         string
	ExpectVersion  int64
	IdempotencyKey string
}

// Relinquish allows the grantee to irreversibly relinquish a Grant, terminating all associated Sessions.
func (s *Services) Relinquish(ctx context.Context, in RelinquishInput) (*GrantView, bool, error) {
	route := "POST:/query-grants/{grant_id}/relinquishment"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.GrantID, in.Reason)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.GrantID, in.IdempotencyKey, reqHash, func() (*GrantView, error) {
		var out *GrantView
		var sessions []string
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, ss, err := s.relinquishOnce(ctx, tx, in)
			if err != nil {
				return err
			}
			out = v
			sessions = ss
			return nil
		}); txErr != nil {
			return nil, txErr
		}
		s.detachSessionsCursors(ctx, sessions)
		return out, nil
	})
}

func (s *Services) relinquishOnce(ctx context.Context, tx *gorm.DB, in RelinquishInput) (*GrantView, []string, error) {
	var g persistence.QueryGrantModel
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
		Where("id = ?", in.GrantID).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, domain.ErrNotFound()
		}
		return nil, nil, err
	}
	if g.RequesterUserID != in.Actor.UserID {
		return nil, nil, domain.ErrNotFound()
	}
	if in.ExpectVersion != g.Version {
		return nil, nil, domain.ErrConcurrent()
	}
	if _, terr := domain.GrantTransition(domain.GrantState(g.State), domain.GrantRelinquish); terr != nil {
		return nil, nil, domain.ErrIllegal()
	}
	now := s.now()
	res := tx.WithContext(ctx).Exec(`
		update query_grants set state = ?, version = version + 1,
		    terminal_at = ?, terminal_reason = 'relinquished'
		where id = ? and version = ?`,
		string(domain.GrantRelinquished), now, g.ID, g.Version)
	if res.Error != nil || res.RowsAffected != 1 {
		return nil, nil, domain.ErrConcurrent()
	}
	var sessions []string
	if err := tx.WithContext(ctx).Raw(`
		update query_sessions set state = ?, version = version + 1,
		    closed_at = ?, close_reason = 'grant_relinquished'
		where grant_id = ? and state = 'active'
		returning id::text`, string(domain.SessionClosed), now, g.ID).Scan(&sessions).Error; err != nil {
		return nil, nil, err
	}
	for _, sid := range sessions {
		if err := s.emitQueryEvent(ctx, tx, EventSessionClosed, "query-sessions/"+sid, in.Actor,
			map[string]any{"session_id": sid, "reason": "grant_relinquished"}); err != nil {
			return nil, nil, err
		}
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_grant.relinquished",
		"query_grants", g.ID, "relinquish", "succeeded")); err != nil {
		return nil, nil, err
	}
	_ = s.Recorder.NotifyRealtime(ctx, tx, "query_grant.relinquished",
		"query-grants/"+g.ID, string(domain.GrantRelinquished), auditlog.UserActor(in.Actor.UserID, in.Actor.Username))
	var final persistence.QueryGrantModel
	if err := tx.WithContext(ctx).Where("id = ?", g.ID).First(&final).Error; err != nil {
		return nil, nil, err
	}
	return grantView(&final), sessions, nil
}

// RenewInput holds parameters for submitting a Grant renewal request.
type RenewInput struct {
	Actor          Actor
	GrantID        string
	RequestedUntil time.Time
	Reason         string
	IdempotencyKey string
}

// Renew submits a renewal request by creating a new access request against the current flow template
// (leaves the existing Grant untouched and valid until its original expiration; datasources default to the flow's current queryable set).
func (s *Services) Renew(ctx context.Context, in RenewInput) (*AccessRequestView, bool, error) {
	g, err := s.loadGrant(ctx, in.GrantID)
	if err != nil {
		return nil, false, err
	}
	if g.RequesterUserID != in.Actor.UserID {
		return nil, false, domain.ErrNotFound()
	}
	// Resolve the flow ID from the originating access request's frozen snapshot (the Grant row itself does not store flow_id).
	var flowID string
	if err := s.DB.WithContext(ctx).Raw(
		`select flow_id_snapshot::text from query_access_requests where id = ?`,
		g.QueryAccessRequestID).Scan(&flowID).Error; err != nil || flowID == "" {
		return nil, false, domain.ErrNotFound()
	}
	caps, cerr := s.loadFlowCapabilities(ctx, s.DB, flowID)
	if cerr != nil {
		return nil, false, cerr
	}
	var ids []string
	for _, c := range caps {
		if c.CanQuery {
			ids = append(ids, c.DatasourceID)
		}
	}
	if len(ids) == 0 {
		return nil, false, domain.ErrValidation()
	}
	return s.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor: in.Actor, FlowID: flowID, DatasourceIDs: ids,
		RequestedUntil: in.RequestedUntil, Reason: in.Reason,
		IdempotencyKey: in.IdempotencyKey,
	})
}
