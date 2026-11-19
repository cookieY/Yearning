// access.go implements decide_query_access and related commands for the query access request lifecycle.
// Approval grants "datasource access eligibility" rather than approving individual SQL statements (Q001);
// any single approval advances a step and any single rejection rejects the entire request;
// final approval issues a Grant and a frozen capability snapshot (Q003).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
)

// AccessRequestView conforms to the OpenAPI QueryAccessRequest schema.
type AccessRequestView struct {
	ID              string   `json:"id"`
	RequesterUserID string   `json:"requester_user_id"`
	ApplicantName   *string  `json:"applicant_name,omitempty"`
	State           string   `json:"state"`
	DatasourceIDs   []string `json:"datasource_ids"`
	DatasourceNames []string `json:"datasource_names,omitempty"`
	GrantID         *string  `json:"grant_id"`
	Version         int64    `json:"version"`
	CreatedAt       string   `json:"created_at"`
	Reason          *string  `json:"reason"`
	RequestedUntil  *string  `json:"requested_until"`
	TerminalAt      *string  `json:"terminal_at"`
}

// CreateAccessRequestInput holds parameters for creating a query access request.
type CreateAccessRequestInput struct {
	Actor          Actor
	FlowID         string
	DatasourceIDs  []string
	RequestedUntil time.Time
	Reason         string
	IdempotencyKey string
}

// flowSnapshot is the AEAD-sealed frozen flow snapshot (approval path and datasource capabilities).
type flowSnapshot struct {
	FlowID      string               `json:"flow_id"`
	FlowName    string               `json:"flow_name"`
	Steps       []snapshotStep       `json:"steps"`
	Datasources []snapshotDatasource `json:"datasources"`
}

type snapshotStep struct {
	Position int                 `json:"position"`
	Actors   []snapshotStepActor `json:"actors"`
}

type snapshotStepActor struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type snapshotDatasource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IdentityFP string `json:"identity_fingerprint"`
	CanQuery   bool   `json:"can_query"`
	CanExport  bool   `json:"can_export"`
}

// CreateAccessRequest creates a query access request with a frozen flow snapshot and approval steps.
func (s *Services) CreateAccessRequest(ctx context.Context, in CreateAccessRequestInput) (*AccessRequestView, bool, error) {
	route := "POST:/query-access-requests"
	reqHash := idemRequestHash(in.Actor.UserID, in.FlowID, dsKey(in.DatasourceIDs), in.RequestedUntil.UTC().Format(time.RFC3339Nano), in.Reason)
	claimed, replay, cerr := s.idemClaim(ctx, in.Actor.UserID, route, in.FlowID, in.IdempotencyKey, reqHash)
	if cerr != nil {
		return nil, false, cerr
	}
	if !claimed {
		var v AccessRequestView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	// Bootstrap missing identity_fingerprint outside the serializable transaction for enabled, granted flow datasources.
	s.ensureDatasourcesIdentity(ctx, in.Actor.UserID, in.FlowID, in.DatasourceIDs)
	var out *AccessRequestView
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.createRequestOnce(ctx, tx, in)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if txErr != nil {
		// Transaction rolled back completely; always release the in_progress idempotency claim so retries are not wedged.
		_ = s.idemRelease(ctx, in.Actor.UserID, route, in.FlowID, in.IdempotencyKey)
		return nil, false, txErr
	}
	raw, merr := json.Marshal(out)
	if merr != nil {
		return nil, false, merr
	}
	if cerr := s.idemComplete(ctx, in.Actor.UserID, route, in.FlowID, in.IdempotencyKey, raw); cerr != nil {
		return nil, false, cerr
	}
	return out, false, nil
}

func dsKey(ids []string) string {
	out := ""
	for _, id := range ids {
		out += id + ","
	}
	return out
}

func (s *Services) createRequestOnce(ctx context.Context, tx *gorm.DB, in CreateAccessRequestInput) (*AccessRequestView, error) {
	settings := s.loadQuerySettings(ctx, tx)
	if !settings.ApprovalEnabled {
		return nil, domain.ErrIllegal()
	}
	if in.Reason == "" || len(in.Reason) > 4096 {
		return nil, domain.ErrValidation()
	}
	if len(in.DatasourceIDs) == 0 {
		return nil, domain.ErrValidation()
	}
	now := s.now()
	if !in.RequestedUntil.After(now) {
		return nil, domain.ErrValidation()
	}
	maxUntil := now.Add(time.Duration(settings.MaximumApprovedDurationMinutes) * time.Minute)
	if in.RequestedUntil.After(maxUntil) {
		return nil, domain.ErrValidation()
	}
	// Flow validation: must be query_access type, enabled, and granted to the user via an enabled permission group.
	flow, err := s.loadQueryFlowForUser(ctx, tx, in.Actor.UserID, in.FlowID)
	if err != nil {
		return nil, err
	}
	// Requested datasources must be a subset of the flow's capabilities and present in the flow.
	caps, err := s.loadFlowCapabilities(ctx, tx, in.FlowID)
	if err != nil {
		return nil, err
	}
	requested := map[string]bool{}
	for _, id := range in.DatasourceIDs {
		requested[id] = true
	}
	snapDS := make([]snapshotDatasource, 0, len(in.DatasourceIDs))
	unavailable := false
	for _, c := range caps {
		if !requested[c.DatasourceID] {
			continue
		}
		if !c.Enabled || !isValidIdentityFingerprint(c.IdentityFP) {
			unavailable = true
		}
		snapDS = append(snapDS, snapshotDatasource{
			ID: c.DatasourceID, Name: c.Name, IdentityFP: c.IdentityFP,
			CanQuery: c.CanQuery, CanExport: c.CanExport,
		})
		delete(requested, c.DatasourceID)
	}
	if len(requested) > 0 {
		return nil, domain.ErrValidation() // Datasource outside flow capabilities
	}
	if unavailable {
		return nil, datasourceUnavailable()
	}
	// Approval path snapshot (flow-level steps; stage_id is NULL).
	type stepRow struct {
		ID       string `gorm:"column:id"`
		Position int    `gorm:"column:position"`
	}
	var steps []stepRow
	if err := tx.WithContext(ctx).Raw(`
		select fs.id, fs.position
		from flow_approval_steps fs
		where fs.flow_id = ? and fs.stage_id is null
		order by fs.position`, in.FlowID).Scan(&steps).Error; err != nil {
		return nil, err
	}
	if len(steps) == 0 || len(steps) > 10 {
		return nil, domain.ErrValidation()
	}
	snap := flowSnapshot{FlowID: in.FlowID, FlowName: flow.Name, Datasources: snapDS}
	for _, st := range steps {
		type actorRow struct {
			UserID      string `gorm:"column:user_id"`
			Username    string `gorm:"column:username"`
			DisplayName string `gorm:"column:display_name"`
		}
		var actors []actorRow
		if err := tx.WithContext(ctx).Raw(`
			select a.user_id, u.username, u.display_name
			from flow_approval_step_actors a join users u on u.id = a.user_id
			where a.step_id = ? order by u.username`, st.ID).Scan(&actors).Error; err != nil {
			return nil, err
		}
		if len(actors) == 0 {
			return nil, domain.ErrValidation()
		}
		ss := snapshotStep{Position: st.Position}
		for _, a := range actors {
			ss.Actors = append(ss.Actors, snapshotStepActor{
				UserID: a.UserID, Username: a.Username, DisplayName: a.DisplayName,
			})
		}
		snap.Steps = append(snap.Steps, ss)
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	// BE-B13-LEFTOVER-CLOSURE (D005): AAD sealing binds to the row primary key -- consistent with the
	// repository-wide sealPayload convention (execID, draftID, and factID all bind row primary keys);
	// earlier versions used the state constant "pending" as the PK (a previously documented AAD deviation closed here).
	reqID := newID()
	env, err := s.sealPayload("query_access_requests", reqID, "flow_snapshot", snapJSON)
	if err != nil {
		return nil, err
	}
	row := &persistence.QueryAccessRequestModel{
		ID: reqID, RequesterUserID: in.Actor.UserID, RequesterUsername: in.Actor.Username,
		RequesterDisplayName: actorDisplay(in.Actor), FlowID: in.FlowID, FlowName: flow.Name,
		FlowSnapshotCiphertext: env.Ciphertext, FlowSnapshotNonce: env.Nonce,
		FlowSnapshotKeyID: env.KeyID, FlowSnapshotAlgorithm: cryptoEnvelopeAlgorithm(),
		SnapshotHash: sha256Hex(snapJSON), State: string(domain.RequestAccessPending),
		Reason: in.Reason, RequestedUntil: in.RequestedUntil.UTC(),
		Version: 1, CreatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	for _, d := range snapDS {
		rd := &persistence.QueryAccessRequestDatasourceModel{
			RequestID: reqID, DatasourceID: d.ID, DatasourceNameSnapshot: d.Name,
			DatasourceIdentityFP: d.IdentityFP, CanQuery: d.CanQuery, CanExport: d.CanExport,
		}
		if err := tx.WithContext(ctx).Create(rd).Error; err != nil {
			return nil, err
		}
	}
	for i, st := range snap.Steps {
		stepID := newID()
		stepState := string(domain.StepPending)
		if i == 0 {
			stepState = string(domain.StepActive)
		}
		if err := tx.WithContext(ctx).Create(&persistence.QueryAccessApprovalStepModel{
			ID: stepID, RequestID: reqID, Position: st.Position, State: stepState,
		}).Error; err != nil {
			return nil, err
		}
		for _, a := range st.Actors {
			if err := tx.WithContext(ctx).Create(&persistence.QueryAccessApprovalActorModel{
				StepID: stepID, ReviewerUserID: a.UserID,
				UsernameSnapshot: a.Username, DisplayNameSnapshot: a.DisplayName,
			}).Error; err != nil {
				return nil, err
			}
		}
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_access.requested",
		"query_access_requests", reqID, "create", "succeeded")); err != nil {
		return nil, err
	}
	_ = s.Recorder.NotifyRealtime(ctx, tx, "query_access.requested",
		"query-access-requests/"+reqID, string(domain.RequestAccessPending), auditActor(in.Actor))
	reason := in.Reason
	reqUntil := in.RequestedUntil.UTC().Format(time.RFC3339Nano)
	return &AccessRequestView{
		ID: reqID, RequesterUserID: in.Actor.UserID, State: string(domain.RequestAccessPending),
		DatasourceIDs: in.DatasourceIDs, Version: 1, CreatedAt: now.Format(time.RFC3339Nano),
		Reason: &reason, RequestedUntil: &reqUntil,
	}, nil
}

func actorDisplay(a Actor) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Username
}

func cryptoEnvelopeAlgorithm() string { return "aes-256-gcm" }

// flowRow is a projection of a flows row.
type flowRow struct {
	ID     string `gorm:"column:id"`
	Name   string `gorm:"column:name"`
	Status string `gorm:"column:status"`
}

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

// flowCapability is a projection of a flow query capability.
type flowCapability struct {
	DatasourceID string `gorm:"column:datasource_id"`
	Name         string `gorm:"column:name"`
	IdentityFP   string `gorm:"column:identity_fingerprint"`
	Enabled      bool   `gorm:"column:enabled"`
	CanQuery     bool   `gorm:"column:can_query"`
	CanExport    bool   `gorm:"column:can_export"`
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
	return s.requestView(ctx, &row)
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
		ID: row.ID, RequesterUserID: row.RequesterUserID, State: row.State,
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

// ListAccessRequests lists query access requests (relationship-filtered to requester or frozen approvers, with optional state filter).
func (s *Services) ListAccessRequests(ctx context.Context, actor Actor, limit int, after string, state string) ([]AccessRequestView, *string, bool, error) {
	rows, err := s.listRequestsFor(ctx, actor, limit+1, after, state)
	if err != nil {
		return nil, nil, false, err
	}
	out := make([]AccessRequestView, 0, len(rows))
	for i := range rows {
		v, verr := s.requestView(ctx, &rows[i])
		if verr != nil {
			return nil, nil, false, verr
		}
		out = append(out, *v)
	}
	// limit+1 lookahead per caller convention: here len(out) == len(rows) (up to limit+1),
	// and hasMore is determined against limit to populate next_cursor (F12 #005: page envelope).
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	if !hasMore || len(out) == 0 {
		return out, nil, false, nil
	}
	last := out[len(out)-1].ID
	return out, &last, true, nil
}

func (s *Services) listRequestsFor(ctx context.Context, actor Actor, limit int, after string, state string) ([]persistence.QueryAccessRequestModel, error) {
	// Relationship filter: requester or frozen approver (admins do not get a global read surface in the query domain; default deny).
	q := s.DB.WithContext(ctx).Model(&persistence.QueryAccessRequestModel{}).
		Where(`requester_user_id_snapshot = ? or exists (
			select 1 from query_access_approval_actors a
			join query_access_approval_steps st on st.id = a.step_id
			where st.request_id = query_access_requests.id
			  and a.reviewer_user_id_snapshot = ?)`, actor.UserID, actor.UserID)
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

// DecideInput holds parameters for an approval decision.
type DecideInput struct {
	Actor          Actor
	RequestID      string
	Decision       string // approve|reject
	Comment        string
	ExpectVersion  int64
	IdempotencyKey string
}

// Decide processes an approval decision (by a frozen approver of the currently active step; any approval advances, any rejection rejects the whole request).
func (s *Services) Decide(ctx context.Context, in DecideInput) (*AccessRequestView, bool, error) {
	route := "POST:/query-access-requests/{request_id}/decisions"
	reqHash := idemRequestHash(in.Actor.UserID, in.RequestID, in.Decision, in.Comment)
	claimed, replay, cerr := s.idemClaim(ctx, in.Actor.UserID, route, in.RequestID, in.IdempotencyKey, reqHash)
	if cerr != nil {
		return nil, false, cerr
	}
	if !claimed {
		var v AccessRequestView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	var out *AccessRequestView
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.decideOnce(ctx, tx, in)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if txErr != nil {
		_ = s.idemRelease(ctx, in.Actor.UserID, route, in.RequestID, in.IdempotencyKey)
		return nil, false, txErr
	}
	raw, merr := json.Marshal(out)
	if merr != nil {
		return nil, false, merr
	}
	if cerr := s.idemComplete(ctx, in.Actor.UserID, route, in.RequestID, in.IdempotencyKey, raw); cerr != nil {
		return nil, false, cerr
	}
	return out, false, nil
}

func (s *Services) decideOnce(ctx context.Context, tx *gorm.DB, in DecideInput) (*AccessRequestView, error) {
	if in.Decision != "approve" && in.Decision != "reject" {
		return nil, domain.ErrValidation()
	}
	if len(in.Comment) > 4096 {
		return nil, domain.ErrValidation()
	}
	settings := s.loadQuerySettings(ctx, tx)
	if !settings.ApprovalEnabled {
		return nil, domain.ErrIllegal()
	}
	var req persistence.QueryAccessRequestModel
	if err := tx.WithContext(ctx).Clauses(lockUpdate()).
		Where("id = ?", in.RequestID).First(&req).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if in.ExpectVersion != req.Version {
		return nil, domain.ErrConcurrent()
	}
	if req.State != string(domain.RequestAccessPending) {
		return nil, domain.ErrIllegal()
	}
	// Active step (at most one guaranteed by the partial unique index).
	var step persistence.QueryAccessApprovalStepModel
	if err := tx.WithContext(ctx).Clauses(lockUpdate()).
		Where("request_id = ? and state = ?", in.RequestID, string(domain.StepActive)).
		First(&step).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrIllegal()
		}
		return nil, err
	}
	// Verify frozen approver eligibility.
	var isActor bool
	if err := tx.WithContext(ctx).Raw(
		`select exists(select 1 from query_access_approval_actors
		 where step_id = ? and reviewer_user_id_snapshot = ?)`,
		step.ID, in.Actor.UserID).Scan(&isActor).Error; err != nil {
		return nil, err
	}
	if !isActor {
		return nil, domain.ErrNotFound() // Non-related actors receive 403 (without leaking resource existence)
	}
	now := s.now()
	if err := tx.WithContext(ctx).Create(&persistence.QueryAccessDecisionModel{
		ID: newID(), RequestID: req.ID, StepID: step.ID,
		ReviewerUserID: in.Actor.UserID, ReviewerUsername: in.Actor.Username,
		Decision: in.Decision, Comment: in.Comment, OccurredAt: now,
	}).Error; err != nil {
		return nil, err
	}
	var grantID *string
	if in.Decision == "reject" {
		if _, terr := domain.RequestTransition(domain.RequestAccessPending, domain.RequestReject); terr != nil {
			return nil, domain.ErrIllegal()
		}
		res := tx.WithContext(ctx).Exec(`
			update query_access_approval_steps set state = ?, decided_at = ? where id = ?`,
			string(domain.StepRejected), now, step.ID)
		if res.Error != nil || res.RowsAffected != 1 {
			return nil, domain.ErrConcurrent()
		}
		res = tx.WithContext(ctx).Exec(`
			update query_access_requests set state = ?, version = version + 1, terminal_at = ?
			where id = ? and version = ?`,
			string(domain.RequestRejected), now, req.ID, req.Version)
		if res.Error != nil || res.RowsAffected != 1 {
			return nil, domain.ErrConcurrent()
		}
		// Reject the entire request: mark all subsequent pending steps as invalid (matching withdraw semantics).
		if err := tx.WithContext(ctx).Exec(`
			update query_access_approval_steps set state = ?
			where request_id = ? and state = 'pending'`,
			string(domain.StepInvalid), req.ID).Error; err != nil {
			return nil, err
		}
		if err := s.emitQueryEvent(ctx, tx, EventAccessRejected, "query-access-requests/"+req.ID, in.Actor,
			map[string]any{"request_id": req.ID, "from": "access_pending", "to": "access_rejected",
				"reason_code": "reviewer_rejected"}); err != nil {
			return nil, err
		}
	} else {
		// Approved: advance to the next step or finalize approval.
		var maxPos int
		if err := tx.WithContext(ctx).Raw(
			`select max(position) from query_access_approval_steps where request_id = ?`, req.ID).
			Scan(&maxPos).Error; err != nil {
			return nil, err
		}
		res := tx.WithContext(ctx).Exec(`
			update query_access_approval_steps set state = ?, decided_at = ? where id = ?`,
			string(domain.StepApproved), now, step.ID)
		if res.Error != nil || res.RowsAffected != 1 {
			return nil, domain.ErrConcurrent()
		}
		if step.Position < maxPos {
			res = tx.WithContext(ctx).Exec(`
				update query_access_approval_steps set state = ?
				where request_id = ? and position = ? and state = ?`,
				string(domain.StepActive), req.ID, step.Position+1, string(domain.StepPending))
			if res.Error != nil || res.RowsAffected != 1 {
				return nil, domain.ErrConcurrent()
			}
		} else {
			// Final approval: issue Grant and frozen capability snapshot (Q003).
			if _, terr := domain.RequestTransition(domain.RequestAccessPending, domain.RequestApproveFinal); terr != nil {
				return nil, domain.ErrIllegal()
			}
			res = tx.WithContext(ctx).Exec(`
				update query_access_requests set state = ?, version = version + 1, terminal_at = ?
				where id = ? and version = ?`,
				string(domain.RequestGrantActive), now, req.ID, req.Version)
			if res.Error != nil || res.RowsAffected != 1 {
				return nil, domain.ErrConcurrent()
			}
			gid, gerr := s.createGrant(ctx, tx, &req, now)
			if gerr != nil {
				return nil, gerr
			}
			grantID = &gid
			if err := s.emitQueryEvent(ctx, tx, EventGrantActivated, "query-grants/"+gid, in.Actor,
				map[string]any{"grant_id": gid, "request_id": req.ID,
					"expires_at": req.RequestedUntil.UTC().Format(time.RFC3339Nano)}); err != nil {
				return nil, err
			}
		}
		if err := s.emitQueryEvent(ctx, tx, EventAccessApproved, "query-access-requests/"+req.ID, in.Actor,
			map[string]any{"request_id": req.ID, "step_position": step.Position,
				"decision": "approve"}); err != nil {
			return nil, err
		}
	}
	if err := s.Recorder.Audit(ctx, tx, auditInput(in.Actor, "query_access.decided",
		"query_access_requests", req.ID, in.Decision, "succeeded")); err != nil {
		return nil, err
	}
	var final persistence.QueryAccessRequestModel
	if err := tx.WithContext(ctx).Where("id = ?", req.ID).First(&final).Error; err != nil {
		return nil, err
	}
	view, verr := s.requestViewLocked(ctx, tx, &final, grantID)
	if verr != nil {
		return nil, verr
	}
	return view, nil
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
		ID: row.ID, RequesterUserID: row.RequesterUserID, State: row.State,
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

// WithdrawInput holds parameters for withdrawing an access request.
type WithdrawInput struct {
	Actor          Actor
	RequestID      string
	Reason         string
	ExpectVersion  int64
	IdempotencyKey string
}

// Withdraw allows the requester to withdraw a pending access request.
func (s *Services) Withdraw(ctx context.Context, in WithdrawInput) (*AccessRequestView, bool, error) {
	route := "POST:/query-access-requests/{request_id}/withdrawal"
	reqHash := idemRequestHash(in.Actor.UserID, in.RequestID, in.Reason)
	claimed, replay, cerr := s.idemClaim(ctx, in.Actor.UserID, route, in.RequestID, in.IdempotencyKey, reqHash)
	if cerr != nil {
		return nil, false, cerr
	}
	if !claimed {
		var v AccessRequestView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	var out *AccessRequestView
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.withdrawOnce(ctx, tx, in)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if txErr != nil {
		_ = s.idemRelease(ctx, in.Actor.UserID, route, in.RequestID, in.IdempotencyKey)
		return nil, false, txErr
	}
	raw, merr := json.Marshal(out)
	if merr != nil {
		return nil, false, merr
	}
	if cerr := s.idemComplete(ctx, in.Actor.UserID, route, in.RequestID, in.IdempotencyKey, raw); cerr != nil {
		return nil, false, cerr
	}
	return out, false, nil
}

func (s *Services) withdrawOnce(ctx context.Context, tx *gorm.DB, in WithdrawInput) (*AccessRequestView, error) {
	var req persistence.QueryAccessRequestModel
	if err := tx.WithContext(ctx).Clauses(lockUpdate()).
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
		"query-access-requests/"+req.ID, string(domain.RequestWithdrawn), auditActor(in.Actor))
	var final persistence.QueryAccessRequestModel
	if err := tx.WithContext(ctx).Where("id = ?", req.ID).First(&final).Error; err != nil {
		return nil, err
	}
	return s.requestViewLocked(ctx, tx, &final, nil)
}
