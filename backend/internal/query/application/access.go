// access.go implements decide_query_access and related commands for the query access request lifecycle.
// Approval grants "datasource access eligibility" rather than approving individual SQL statements (Q001);
// any single approval advances a step and any single rejection rejects the entire request;
// final approval issues a Grant and a frozen capability snapshot (Q003).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
	"gorm.io/gorm"
	"time"
)

// AccessRequestView conforms to the OpenAPI QueryAccessRequest schema.
type AccessRequestView struct {
	ID              string   `json:"id"`
	FlowID          string   `json:"flow_id,omitempty"`
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
	CanDecide       bool     `json:"can_decide"`
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

func validateRequestedUntil(now, requestedUntil time.Time, maxDurationMinutes int) error {
	if !requestedUntil.After(now) {
		return domain.ErrValidation().WithDetail("requested_until_in_past")
	}
	maxUntil := now.Add(time.Duration(maxDurationMinutes) * time.Minute)
	if requestedUntil.After(maxUntil) {
		return domain.ErrValidation().WithDetail(fmt.Sprintf("requested_until_exceeds_max_minutes:%d", maxDurationMinutes))
	}
	return nil
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
	if err := validateRequestedUntil(now, in.RequestedUntil, settings.MaximumApprovedDurationMinutes); err != nil {
		return nil, err
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
	env, err := s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "query_access_requests",
		PrimaryKey: reqID,
		Column:     "flow_snapshot",
	}, snapJSON)
	if err != nil {
		return nil, err
	}
	row := &persistence.QueryAccessRequestModel{
		ID: reqID, RequesterUserID: in.Actor.UserID, RequesterUsername: in.Actor.Username,
		RequesterDisplayName: actorDisplay(in.Actor), FlowID: in.FlowID, FlowName: flow.Name,
		FlowSnapshotCiphertext: env.Ciphertext, FlowSnapshotNonce: env.Nonce,
		FlowSnapshotKeyID: env.KeyID, FlowSnapshotAlgorithm: cryptoEnvelopeAlgorithm(),
		SnapshotHash: hashutil.SHA256Hex(snapJSON), State: string(domain.RequestAccessPending),
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
		"query-access-requests/"+reqID, string(domain.RequestAccessPending), auditlog.UserActor(in.Actor.UserID, in.Actor.Username))
	reason := in.Reason
	reqUntil := in.RequestedUntil.UTC().Format(time.RFC3339Nano)
	canDecide := false
	if len(snap.Steps) > 0 {
		for _, a := range snap.Steps[0].Actors {
			if a.UserID == in.Actor.UserID {
				canDecide = true
				break
			}
		}
	}
	return &AccessRequestView{
		ID: reqID, FlowID: in.FlowID, RequesterUserID: in.Actor.UserID, State: string(domain.RequestAccessPending),
		DatasourceIDs: in.DatasourceIDs, Version: 1, CreatedAt: now.Format(time.RFC3339Nano),
		Reason: &reason, RequestedUntil: &reqUntil, CanDecide: canDecide,
	}, nil
}

// flowRow is a projection of a flows row.
type flowRow struct {
	ID     string `gorm:"column:id"`
	Name   string `gorm:"column:name"`
	Status string `gorm:"column:status"`
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

// DecideInput holds parameters for an approval decision.
type DecideInput struct {
	Actor          Actor
	RequestID      string
	Decision       string // approve|reject
	Comment        string
	ExpectVersion  int64
	IdempotencyKey string
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
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
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
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
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
	canDecideByID, cerr := s.canDecideMap(ctx, tx, in.Actor.UserID, []persistence.QueryAccessRequestModel{final})
	if cerr != nil {
		return nil, cerr
	}
	view.CanDecide = canDecideByID[final.ID]
	return view, nil
}

// WithdrawInput holds parameters for withdrawing an access request.
type WithdrawInput struct {
	Actor          Actor
	RequestID      string
	Reason         string
	ExpectVersion  int64
	IdempotencyKey string
}
