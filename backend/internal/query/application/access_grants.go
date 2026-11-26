// access_grants.go holds access domain helpers and operations.
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
	"gorm.io/gorm"
	"time"
)

// CreateAccessRequest creates a query access request with a frozen flow snapshot and approval steps.
func (s *Services) CreateAccessRequest(ctx context.Context, in CreateAccessRequestInput) (*AccessRequestView, bool, error) {
	route := "POST:/query-access-requests"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.FlowID, dsKey(in.DatasourceIDs), in.RequestedUntil.UTC().Format(time.RFC3339Nano), in.Reason)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.FlowID, in.IdempotencyKey, reqHash, func() (*AccessRequestView, error) {
		// Bootstrap missing identity_fingerprint outside the serializable transaction for enabled, granted flow datasources.
		s.ensureDatasourcesIdentity(ctx, in.Actor.UserID, in.FlowID, in.DatasourceIDs)
		var out *AccessRequestView
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, err := s.createRequestOnce(ctx, tx, in)
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

func dsKey(ids []string) string {
	out := ""
	for _, id := range ids {
		out += id + ","
	}
	return out
}

func actorDisplay(a Actor) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Username
}

func cryptoEnvelopeAlgorithm() string { return "aes-256-gcm" }

// loadQueryFlowForUser verifies that the query flow is available to the user via an enabled permission group.
func (s *Services) loadQueryFlowForUser(ctx context.Context, tx *gorm.DB, userID, flowID string) (*flowRow, error) {
	var f flowRow
	err := tx.WithContext(ctx).Raw(`
		select f.id, f.name, f.status from flows f
		where f.id = ? and f.flow_type = 'query_access'`, flowID).Scan(&f).Error
	if err != nil {
		return nil, err
	}
	if f.ID == "" {
		return nil, domain.ErrNotFound()
	}
	if f.Status != "enabled" {
		return nil, domain.ErrNotFound()
	}
	var granted bool
	if err := tx.WithContext(ctx).Raw(`
		select exists(
			select 1 from permission_group_flow_grants g
			join permission_groups pg on pg.id = g.permission_group_id
			join permission_group_members m on m.permission_group_id = pg.id
			where g.flow_id = ? and m.user_id = ? and pg.enabled)`,
		flowID, userID).Scan(&granted).Error; err != nil {
		return nil, err
	}
	if !granted {
		return nil, domain.ErrNotFound()
	}
	return &f, nil
}

// loadFlowCapabilities loads the flow's query capabilities (joining current datasource rows for name, enabled state, and identity fingerprint).
func (s *Services) loadFlowCapabilities(ctx context.Context, tx *gorm.DB, flowID string) ([]flowCapability, error) {
	var out []flowCapability
	err := tx.WithContext(ctx).Raw(`
		select c.datasource_id, d.name,
		       coalesce(d.identity_fingerprint, '') as identity_fingerprint,
		       d.enabled, c.can_query, c.can_export
		from flow_query_capabilities c
		join datasources d on d.id = c.datasource_id
		where c.flow_id = ? order by d.name`, flowID).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetAccessRequest loads a query access request (visible to the requester and frozen approvers).
func (s *Services) GetAccessRequest(ctx context.Context, actor Actor, requestID string) (*AccessRequestView, error) {
	var row persistence.QueryAccessRequestModel
	if err := s.DB.WithContext(ctx).Where("id = ?", requestID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	allowed, err := s.canReadRequest(ctx, actor, &row)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, domain.ErrNotFound()
	}
	view, err := s.requestView(ctx, &row)
	if err != nil {
		return nil, err
	}
	canDecideByID, err := s.canDecideMap(ctx, s.DB, actor.UserID, []persistence.QueryAccessRequestModel{row})
	if err != nil {
		return nil, err
	}
	view.CanDecide = canDecideByID[row.ID]
	return view, nil
}

func (s *Services) canReadRequest(ctx context.Context, actor Actor, row *persistence.QueryAccessRequestModel) (bool, error) {
	if row.RequesterUserID == actor.UserID {
		return true, nil
	}
	var exists bool
	err := s.DB.WithContext(ctx).Raw(`
		select exists(
			select 1 from query_access_approval_steps st
			join query_access_approval_actors a on a.step_id = st.id
			where st.request_id = ? and a.reviewer_user_id_snapshot = ?)`,
		row.ID, actor.UserID).Scan(&exists).Error
	return exists, err
}

func (s *Services) requestView(ctx context.Context, row *persistence.QueryAccessRequestModel) (*AccessRequestView, error) {
	var ids []string
	if err := s.DB.WithContext(ctx).Model(&persistence.QueryAccessRequestDatasourceModel{}).
		Where("request_id = ?", row.ID).Order("datasource_id").
		Pluck("datasource_id", &ids).Error; err != nil {
		return nil, err
	}
	var grantID *string
	if row.State == string(domain.RequestGrantActive) {
		if err := s.DB.WithContext(ctx).Raw(
			`select id::text from query_grants where query_access_request_id = ?`, row.ID).
			Scan(&grantID).Error; err != nil {
			return nil, err
		}
	}
	reason := row.Reason
	reqUntil := row.RequestedUntil.UTC().Format(time.RFC3339Nano)
	var terminalAt *string
	if row.TerminalAt != nil {
		v := row.TerminalAt.UTC().Format(time.RFC3339Nano)
		terminalAt = &v
	}
	view := &AccessRequestView{
		ID: row.ID, FlowID: row.FlowID, RequesterUserID: row.RequesterUserID, State: row.State,
		DatasourceIDs: ids, GrantID: grantID, Version: row.Version,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
		Reason:    &reason, RequestedUntil: &reqUntil, TerminalAt: terminalAt,
	}
	s.fillAccessRequestNames(ctx, view)
	return view, nil
}

// fillAccessRequestNames (UX-005): populates requester display name and datasource names on the view
// (nullable; empty if the user or datasource was deleted, in which case the frontend falls back to raw IDs).
func (s *Services) fillAccessRequestNames(ctx context.Context, view *AccessRequestView) {
	var name string
	if err := s.DB.WithContext(ctx).Raw(
		`select coalesce(nullif(display_name, ''), username) from users where id = ?`,
		view.RequesterUserID).Scan(&name).Error; err == nil && name != "" {
		view.ApplicantName = &name
	}
	names := make([]string, 0, len(view.DatasourceIDs))
	for _, dsID := range view.DatasourceIDs {
		var dsName string
		_ = s.DB.WithContext(ctx).Raw(
			`select coalesce(nullif(name, ''), id::text) from datasources where id = ?`, dsID).Scan(&dsName).Error
		names = append(names, dsName)
	}
	view.DatasourceNames = names
}

// ValidQueryAccessRequestState validates query access request state filter values (RCP-20261021 state filter).
func ValidQueryAccessRequestState(v string) bool {
	switch v {
	case "access_pending", "grant_active", "access_rejected", "withdrawn", "invalid":
		return true
	}
	return false
}

// ValidQueryAccessRequestScope validates the optional relationship scope filter on ListAccessRequests.
func ValidQueryAccessRequestScope(v string) bool {
	switch v {
	case "", "all", "reviewer", "requester":
		return true
	}
	return false
}

// canDecideMap computes authoritative active-step frozen-approver eligibility for a batch of access
// requests in a single query (avoiding per-row N+1 checks).
func (s *Services) canDecideMap(ctx context.Context, db *gorm.DB, userID string, rows []persistence.QueryAccessRequestModel) (map[string]bool, error) {
	out := make(map[string]bool, len(rows))
	if userID == "" || len(rows) == 0 {
		return out, nil
	}
	pendingIDs := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].State == string(domain.RequestAccessPending) {
			pendingIDs = append(pendingIDs, rows[i].ID)
		}
	}
	if len(pendingIDs) == 0 {
		return out, nil
	}
	var eligible []string
	if err := db.WithContext(ctx).Raw(`
		select distinct st.request_id::text
		from query_access_approval_steps st
		join query_access_approval_actors a on a.step_id = st.id
		where st.request_id in ? and st.state = ? and a.reviewer_user_id_snapshot = ?`,
		pendingIDs, string(domain.StepActive), userID).Scan(&eligible).Error; err != nil {
		return nil, err
	}
	for _, id := range eligible {
		out[id] = true
	}
	return out, nil
}

// ListAccessRequests lists query access requests (relationship-filtered to requester or frozen approvers, with optional state and scope filters).
func (s *Services) ListAccessRequests(ctx context.Context, actor Actor, limit int, after string, state string, scope string) ([]AccessRequestView, *string, bool, error) {
	if !ValidQueryAccessRequestScope(scope) {
		return nil, nil, false, domain.ErrValidation()
	}
	rows, err := s.listRequestsFor(ctx, actor, limit+1, after, state, scope)
	if err != nil {
		return nil, nil, false, err
	}
	// limit+1 lookahead per caller convention: trim before hydrating views or computing eligibility.
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	canDecideByID, err := s.canDecideMap(ctx, s.DB, actor.UserID, rows)
	if err != nil {
		return nil, nil, false, err
	}
	out := make([]AccessRequestView, 0, len(rows))
	for i := range rows {
		v, verr := s.requestView(ctx, &rows[i])
		if verr != nil {
			return nil, nil, false, verr
		}
		v.CanDecide = canDecideByID[rows[i].ID]
		out = append(out, *v)
	}
	if !hasMore || len(out) == 0 {
		return out, nil, false, nil
	}
	last := out[len(out)-1].ID
	return out, &last, true, nil
}

func (s *Services) listRequestsFor(ctx context.Context, actor Actor, limit int, after string, state string, scope string) ([]persistence.QueryAccessRequestModel, error) {
	// Relationship filter: requester or frozen approver (admins do not get a global read surface in the query domain; default deny).
	q := s.DB.WithContext(ctx).Model(&persistence.QueryAccessRequestModel{})
	switch scope {
	case "reviewer":
		q = q.Where(`exists (
			select 1 from query_access_approval_actors a
			join query_access_approval_steps st on st.id = a.step_id
			where st.request_id = query_access_requests.id
			  and a.reviewer_user_id_snapshot = ?)`, actor.UserID)
	case "requester":
		q = q.Where("requester_user_id_snapshot = ?", actor.UserID)
	default:
		q = q.Where(`requester_user_id_snapshot = ? or exists (
			select 1 from query_access_approval_actors a
			join query_access_approval_steps st on st.id = a.step_id
			where st.request_id = query_access_requests.id
			  and a.reviewer_user_id_snapshot = ?)`, actor.UserID, actor.UserID)
	}
	if state != "" {
		q = q.Where("state = ?", state)
	}
	if after != "" {
		q = q.Where("(created_at, id) < ((select created_at from query_access_requests where id = ?), ?::uuid)", after, after)
	}
	var rows []persistence.QueryAccessRequestModel
	if err := q.Order("created_at desc, id desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Decide processes an approval decision (by a frozen approver of the currently active step; any approval advances, any rejection rejects the whole request).
func (s *Services) Decide(ctx context.Context, in DecideInput) (*AccessRequestView, bool, error) {
	route := "POST:/query-access-requests/{request_id}/decisions"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.RequestID, in.Decision, in.Comment)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.RequestID, in.IdempotencyKey, reqHash, func() (*AccessRequestView, error) {
		var out *AccessRequestView
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, err := s.decideOnce(ctx, tx, in)
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

func (s *Services) requestViewLocked(ctx context.Context, tx *gorm.DB, row *persistence.QueryAccessRequestModel, grantID *string) (*AccessRequestView, error) {
	var ids []string
	if err := tx.WithContext(ctx).Model(&persistence.QueryAccessRequestDatasourceModel{}).
		Where("request_id = ?", row.ID).Order("datasource_id").
		Pluck("datasource_id", &ids).Error; err != nil {
		return nil, err
	}
	if grantID == nil && row.State == string(domain.RequestGrantActive) {
		var gid string
		if err := tx.WithContext(ctx).Raw(
			`select id::text from query_grants where query_access_request_id = ?`, row.ID).
			Scan(&gid).Error; err != nil {
			return nil, err
		}
		if gid != "" {
			grantID = &gid
		}
	}
	reason := row.Reason
	reqUntil := row.RequestedUntil.UTC().Format(time.RFC3339Nano)
	var terminalAt *string
	if row.TerminalAt != nil {
		v := row.TerminalAt.UTC().Format(time.RFC3339Nano)
		terminalAt = &v
	}
	view := &AccessRequestView{
		ID: row.ID, FlowID: row.FlowID, RequesterUserID: row.RequesterUserID, State: row.State,
		DatasourceIDs: ids, GrantID: grantID, Version: row.Version,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
		Reason:    &reason, RequestedUntil: &reqUntil, TerminalAt: terminalAt,
	}
	s.fillAccessRequestNames(ctx, view)
	return view, nil
}

// createGrant creates the Grant and frozen capability snapshot upon final approval (for the approved subset of datasources).
func (s *Services) createGrant(ctx context.Context, tx *gorm.DB, req *persistence.QueryAccessRequestModel, now time.Time) (string, error) {
	gid := newID()
	g := &persistence.QueryGrantModel{
		ID: gid, QueryAccessRequestID: req.ID,
		RequesterUserID: req.RequesterUserID, RequesterUsername: req.RequesterUsername,
		State: string(domain.GrantActive), ExpiresAt: req.RequestedUntil.UTC(),
		Version: 1, CreatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(g).Error; err != nil {
		return "", err
	}
	var ds []persistence.QueryAccessRequestDatasourceModel
	if err := tx.WithContext(ctx).Where("request_id = ?", req.ID).Find(&ds).Error; err != nil {
		return "", err
	}
	for _, d := range ds {
		c := &persistence.QueryGrantCapabilityModel{
			GrantID: gid, DatasourceID: d.DatasourceID,
			DatasourceNameSnapshot: d.DatasourceNameSnapshot,
			DatasourceIdentityFP:   d.DatasourceIdentityFP,
			CanQuery:               d.CanQuery, CanExport: d.CanExport,
		}
		if err := tx.WithContext(ctx).Create(c).Error; err != nil {
			return "", err
		}
	}
	return gid, nil
}

// Withdraw allows the requester to withdraw a pending access request.
func (s *Services) Withdraw(ctx context.Context, in WithdrawInput) (*AccessRequestView, bool, error) {
	route := "POST:/query-access-requests/{request_id}/withdrawal"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.RequestID, in.Reason)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.RequestID, in.IdempotencyKey, reqHash, func() (*AccessRequestView, error) {
		var out *AccessRequestView
		if txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			v, err := s.withdrawOnce(ctx, tx, in)
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

func (s *Services) withdrawOnce(ctx context.Context, tx *gorm.DB, in WithdrawInput) (*AccessRequestView, error) {
	var req persistence.QueryAccessRequestModel
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
		Where("id = ?", in.RequestID).First(&req).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if req.RequesterUserID != in.Actor.UserID {
		return nil, domain.ErrNotFound()
	}
	if in.ExpectVersion != req.Version {
		return nil, domain.ErrConcurrent()
	}
	if _, terr := domain.RequestTransition(domain.RequestState(req.State), domain.RequestWithdraw); terr != nil {
		return nil, domain.ErrIllegal()
	}
	now := s.now()
	res := tx.WithContext(ctx).Exec(`
		update query_access_requests set state = ?, version = version + 1, terminal_at = ?
		where id = ? and version = ?`,
		string(domain.RequestWithdrawn), now, req.ID, req.Version)
	if res.Error != nil || res.RowsAffected != 1 {
		return nil, domain.ErrConcurrent()
	}
	if err := tx.WithContext(ctx).Exec(`
		update query_access_approval_steps set state = ?
		where request_id = ? and state in ('pending', 'active')`,
		string(domain.StepInvalid), req.ID).Error; err != nil {
		return nil, err
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_access.withdrawn",
		"query_access_requests", req.ID, "withdraw", "succeeded")); err != nil {
		return nil, err
	}
	_ = s.Recorder.NotifyRealtime(ctx, tx, "query_access.withdrawn",
		"query-access-requests/"+req.ID, string(domain.RequestWithdrawn), auditlog.UserActor(in.Actor.UserID, in.Actor.Username))
	var final persistence.QueryAccessRequestModel
	if err := tx.WithContext(ctx).Where("id = ?", req.ID).First(&final).Error; err != nil {
		return nil, err
	}
	return s.requestViewLocked(ctx, tx, &final, nil)
}
