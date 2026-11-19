// read.go Work order read surface: view construction, listing (relation-filtered), frozen Review findings,
// SQL plaintext Reveal (no-store + watermark + audit), and copy events.
// Zero Provider calls on the read path (R003 approval reuse; acceptance gate).
package application

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// OrderView maps to the OpenAPI ChangeOrder schema (allowlisted fields).
type OrderView struct {
	ID              string `json:"id"`
	DisplayNumber   string `json:"display_number"`
	SubmitterUserID string `json:"submitter_user_id"`
	// Submitter snapshot name (§18.58 approval queue display): frozen on submission, immutable through terminal states.
	SubmitterUsername    string `json:"submitter_username,omitempty"`
	SubmitterDisplayName string `json:"submitter_display_name,omitempty"`
	Title                string `json:"title,omitempty"`
	State                string `json:"state"`
	CurrentStagePosition *int32 `json:"current_stage_position"`
	// Submitter's expected execution time (§18.56 Option B): RFC3339 snapshot; nil = unset.
	// Used for display and executor schedule prefill; does not constitute execution authorization.
	RequestedScheduledFor *string `json:"requested_scheduled_for,omitempty"`
	// Active schedule (owner ruling 2026-10-30): carried only when state=scheduled; drives
	// the scheduled-state execution card (planned time + cancel entry). Persisted in the same DB
	// as the schedule row, remaining unchanged across crash restarts.
	ActiveSchedule            *ActiveScheduleView `json:"active_schedule,omitempty"`
	LatestAttemptID           *string             `json:"latest_attempt_id,omitempty"`
	Stages                    []StageView         `json:"stages"`
	HasSQL                    bool                `json:"has_sql"`
	SQLHash                   string              `json:"sql_hash,omitempty"`
	SnapshotHash              string              `json:"snapshot_hash,omitempty"`
	ManuallyVerified          bool                `json:"manually_verified,omitempty"`
	HasDDL                    bool                `json:"has_ddl,omitempty"`
	OscChoiceAvailable        bool                `json:"osc_choice_available,omitempty"`
	RollbackEnabled           bool                `json:"rollback_enabled"`
	TicketType                string              `json:"ticket_type"`
	SourceOrderID             *string             `json:"source_order_id,omitempty"`
	SourceOrderDisplayNumber  *string             `json:"source_order_display_number,omitempty"`
	RollbackEvidenceID        *string             `json:"rollback_evidence_id,omitempty"`
	RollbackStatus            string              `json:"rollback_status"`
	RollbackUnsupportedReason *string             `json:"rollback_unsupported_reason,omitempty"`
	RollbackConflictReason    *string             `json:"rollback_conflict_reason,omitempty"`
	RollbackOrders            []RollbackOrderRef  `json:"rollback_orders,omitempty"`
	Version                   int64               `json:"version"`
	SubmittedAt               string              `json:"submitted_at"`
	TerminalAt                *string             `json:"terminal_at"`
}

// RollbackOrderRef summary of an associated rollback work order.
type RollbackOrderRef struct {
	ID            string `json:"id"`
	DisplayNumber string `json:"display_number"`
	State         string `json:"state"`
	SubmittedAt   string `json:"submitted_at"`
}

// ActiveScheduleView maps to ChangeOrder.active_schedule (trimmed ExecutionSchedule
// shape: only the three fields required by the display surface).
type ActiveScheduleView struct {
	ID           string `json:"id"`
	StageID      string `json:"stage_id"`
	ScheduledFor string `json:"scheduled_for"`
	State        string `json:"state"`
}

// StageView maps to the OrderStage schema.
type StageView struct {
	ID              string      `json:"id"`
	Position        int32       `json:"position"`
	DatasourceName  string      `json:"datasource_name"`
	State           string      `json:"state"`
	ApprovalSteps   []StepView  `json:"approval_steps"`
	ExecutionActors []ActorView `json:"execution_actors"`
}

// StepView approval step (free-form object in schema; fields read directly from frozen tables).
type StepView struct {
	ID        string      `json:"id"`
	Position  int32       `json:"position"`
	State     string      `json:"state"`
	DecidedAt *string     `json:"decided_at,omitempty"`
	Actors    []ActorView `json:"actors"`
}

// ActorView frozen Actor snapshot. Approval step items are open objects (contract inline object),
// whereas executor items reference the full User shape — is_builtin_admin/version/created_at/updated_at
// are all snapshot columns (F12 #006) carried uniformly for both Actor types.
type ActorView struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	DisplayName    string `json:"display_name"`
	IsBuiltinAdmin bool   `json:"is_builtin_admin"`
	Version        int64  `json:"version"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// relationFilterSQL is the work order relation predicate (change-order-relevant-actor:
// submitter / frozen approver / frozen executor), ?1=orderID, ?2=actor.
const relationFilterSQL = `(
	o.submitter_user_id_snapshot = @actor
	or exists (select 1 from order_approval_actors aa
	           join order_approval_steps st on st.id = aa.step_id
	           join order_stages sg on sg.id = st.stage_id
	           where sg.order_id = o.id and aa.reviewer_user_id_snapshot = @actor)
	or exists (select 1 from order_execution_actors ea
	           join order_stages sg2 on sg2.id = ea.stage_id
	           where sg2.order_id = o.id and ea.executor_user_id_snapshot = @actor))`

// CanRead verifies the actor's relation to the work order (returns false on failure without leaking existence).
func (s *Services) CanRead(ctx context.Context, db *gorm.DB, orderID, userID string) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Raw(
		`select count(*) from change_orders o where o.id = @order and `+relationFilterSQL,
		map[string]any{"order": orderID, "actor": userID}).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// buildOrderView assembles the work order view (stages / steps / Actors / SQL existence).
func (s *Services) buildOrderView(ctx context.Context, db *gorm.DB, orderID string) (*OrderView, error) {
	var o persistence.ChangeOrderModel
	if err := db.WithContext(ctx).Where("id = ?", orderID).First(&o).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	stages, err := s.loadOrderStages(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	stageIDs := make([]string, 0, len(stages))
	for _, st := range stages {
		stageIDs = append(stageIDs, st.ID)
	}
	steps, err := s.loadOrderSteps(ctx, db, stageIDs)
	if err != nil {
		return nil, err
	}
	actorsByStep, err := s.loadApprovalActorsByStep(ctx, db, stageIDs)
	if err != nil {
		return nil, err
	}
	execByStage, err := s.loadExecutionActorsByStage(ctx, db, stageIDs)
	if err != nil {
		return nil, err
	}
	var hasSQL int64
	if err := db.WithContext(ctx).Raw(
		`select count(*) from order_sql_payloads where order_id = ?`, orderID).Scan(&hasSQL).Error; err != nil {
		return nil, err
	}
	view := &OrderView{
		ID: o.ID, DisplayNumber: o.DisplayNumber, SubmitterUserID: o.SubmitterUserIDSnapshot,
		SubmitterUsername: o.SubmitterUsernameSnapshot, SubmitterDisplayName: o.SubmitterDisplayNameSnapshot,
		Title: o.Title, State: o.State, CurrentStagePosition: o.CurrentStagePosition,
		HasSQL: hasSQL > 0, SQLHash: o.SQLHMAC, SnapshotHash: o.SnapshotHash,
		ManuallyVerified:   o.ManuallyVerified,
		RollbackEnabled:    o.RollbackEnabled,
		TicketType:         o.TicketType,
		SourceOrderID:      o.SourceOrderID,
		RollbackEvidenceID: o.RollbackEvidenceID,
		RollbackStatus:     o.RollbackStatus,
		Version:            o.Version,
		SubmittedAt:        o.SubmittedAt.UTC().Format("2006-01-02T15:04:05.999999Z"),
	}
	if view.TicketType == "" {
		view.TicketType = "normal"
	}
	if view.RollbackStatus == "" {
		if view.RollbackEnabled && view.TicketType == "normal" {
			view.RollbackStatus = "pending"
		} else {
			view.RollbackStatus = "not_requested"
		}
	}
	if err := s.populateOrderRollbackView(ctx, db, &o, view); err != nil {
		return nil, err
	}
	view.HasDDL = s.hasDDLStatements(ctx, db, stages)
	view.OscChoiceAvailable = s.isOscChoiceAvailable(ctx, db, stages)
	if o.RequestedScheduledFor != nil {
		rsf := o.RequestedScheduledFor.UTC().Format("2006-01-02T15:04:05.999999Z")
		view.RequestedScheduledFor = &rsf
	}
	if o.TerminalAt != nil {
		t := o.TerminalAt.UTC().Format("2006-01-02T15:04:05.999999Z")
		view.TerminalAt = &t
	}
	// Active schedules exist only when state=scheduled (create / cancel / due / missed transition
	// in lockstep within the same transaction; see execution/application/schedule.go); reads the
	// current live row from execution_schedules directly — naturally absent when order state != scheduled.
	if o.State == "scheduled" {
		var sched struct {
			ID           string    `gorm:"column:id"`
			StageID      string    `gorm:"column:stage_id"`
			ScheduledFor time.Time `gorm:"column:scheduled_for"`
		}
		if err := db.WithContext(ctx).Raw(`
			select id::text as id, stage_id::text as stage_id, scheduled_for
			from execution_schedules
			where order_id = ? and state = 'scheduled'
			order by created_at limit 1`, orderID).Scan(&sched).Error; err != nil {
			return nil, err
		}
		if sched.ID != "" {
			view.ActiveSchedule = &ActiveScheduleView{
				ID: sched.ID, StageID: sched.StageID,
				ScheduledFor: sched.ScheduledFor.UTC().Format("2006-01-02T15:04:05.999999Z"),
				State:        "scheduled",
			}
		}
	}
	var latestAttemptID string
	if err := db.WithContext(ctx).Raw(`
		select id::text as id
		from execution_attempts
		where order_id = ?
		order by created_at desc
		limit 1`, orderID).Scan(&latestAttemptID).Error; err != nil {
		return nil, err
	}
	if latestAttemptID != "" {
		view.LatestAttemptID = &latestAttemptID
	}
	for _, st := range stages {
		sv := StageView{
			ID: st.ID, Position: st.Position, DatasourceName: st.DatasourceNameSnapshot,
			State: st.State,
		}
		for _, step := range steps {
			if step.StageID != st.ID {
				continue
			}
			pv := StepView{ID: step.ID, Position: step.Position, State: step.State}
			if step.DecidedAt != nil {
				d := step.DecidedAt.UTC().Format("2006-01-02T15:04:05.999999Z")
				pv.DecidedAt = &d
			}
			for _, a := range actorsByStep[step.ID] {
				pv.Actors = append(pv.Actors, ActorView{
					ID: a.ReviewerUserIDSnapshot, Username: a.UsernameSnapshot,
					DisplayName: a.DisplayNameSnapshot,
					// Approval step items are open objects in the contract; these fields are only for shape
					// compatibility, as the snapshot table does not store user row version facts.
					Version:   1,
					CreatedAt: "1970-01-01T00:00:00Z",
					UpdatedAt: "1970-01-01T00:00:00Z",
				})
			}
			if pv.Actors == nil {
				pv.Actors = []ActorView{}
			}
			sv.ApprovalSteps = append(sv.ApprovalSteps, pv)
		}
		if sv.ApprovalSteps == nil {
			sv.ApprovalSteps = []StepView{}
		}
		for _, a := range execByStage[st.ID] {
			sv.ExecutionActors = append(sv.ExecutionActors, ActorView{
				ID: a.ExecutorUserIDSnapshot, Username: a.UsernameSnapshot,
				DisplayName:    a.DisplayNameSnapshot,
				IsBuiltinAdmin: a.IsBuiltinAdminSnapshot, Version: a.UserVersionSnapshot,
				CreatedAt: a.UserCreatedAtSnapshot.UTC().Format("2006-01-02T15:04:05.999999Z"),
				UpdatedAt: a.UserUpdatedAtSnapshot.UTC().Format("2006-01-02T15:04:05.999999Z"),
			})
		}
		if sv.ExecutionActors == nil {
			sv.ExecutionActors = []ActorView{}
		}
		view.Stages = append(view.Stages, sv)
	}
	if view.Stages == nil {
		view.Stages = []StageView{}
	}
	return view, nil
}

func (s *Services) loadApprovalActorsByStep(ctx context.Context, db *gorm.DB, stageIDs []string) (map[string][]persistence.OrderApprovalActorModel, error) {
	out := map[string][]persistence.OrderApprovalActorModel{}
	if len(stageIDs) == 0 {
		return out, nil
	}
	var rows []persistence.OrderApprovalActorModel
	if err := db.WithContext(ctx).Raw(`
		select aa.step_id::text as step_id, aa.reviewer_user_id_snapshot::text as reviewer_user_id_snapshot,
		       aa.username_snapshot, aa.display_name_snapshot
		from order_approval_actors aa
		join order_approval_steps st on st.id = aa.step_id
		where st.stage_id in ?
		order by aa.reviewer_user_id_snapshot`, stageIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.StepID] = append(out[r.StepID], r)
	}
	return out, nil
}

func (s *Services) loadExecutionActorsByStage(ctx context.Context, db *gorm.DB, stageIDs []string) (map[string][]persistence.OrderExecutionActorModel, error) {
	out := map[string][]persistence.OrderExecutionActorModel{}
	if len(stageIDs) == 0 {
		return out, nil
	}
	var rows []persistence.OrderExecutionActorModel
	if err := db.WithContext(ctx).Raw(`
		select stage_id::text as stage_id, executor_user_id_snapshot::text as executor_user_id_snapshot,
		       username_snapshot, display_name_snapshot,
		       is_builtin_admin_snapshot, user_version_snapshot,
		       user_created_at_snapshot, user_updated_at_snapshot
		from order_execution_actors where stage_id in ?
		order by executor_user_id_snapshot`, stageIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.StageID] = append(out[r.StageID], r)
	}
	return out, nil
}

// ListOrders performs relation filtering + cursor pagination (limit+1 prefetch; returns next page cursor).
func (s *Services) ListOrders(ctx context.Context, actor Actor, limit int, after string) ([]*OrderView, *string, error) {
	args := map[string]any{"actor": actor.UserID}
	where := relationFilterSQL
	if after != "" {
		if ts, id, ok := splitCursor(after); ok {
			where += ` and (o.submitted_at, o.id) < (@ts::timestamptz, @id::uuid)`
			args["ts"] = ts
			args["id"] = id
		}
	}
	var rows []struct {
		ID          string
		SubmittedAt time.Time
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select o.id::text as id, o.submitted_at from change_orders o where `+where+`
		order by o.submitted_at desc, o.id desc limit `+strconv.Itoa(limit+1), args).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]*OrderView, 0, len(rows))
	for _, r := range rows {
		v, err := s.buildOrderView(ctx, s.DB, r.ID)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, v)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		cur := last.SubmittedAt.UTC().Format("2006-01-02T15:04:05.999999Z") + "|" + last.ID
		next = &cur
	}
	return out, next, nil
}

// GetOrder reads a single work order (relation check + invalid materialization to reflect Actor deletion facts).
func (s *Services) GetOrder(ctx context.Context, actor Actor, orderID string) (*OrderView, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	// W008: materialize invalid on read (writes only when the frozen Actor set has been emptied).
	err = s.serializable(ctx, func(tx *gorm.DB) error {
		order, err := s.loadOrderForUpdate(ctx, tx, orderID)
		if err != nil {
			return err
		}
		return s.materializeInvalidIfNeeded(ctx, tx, order)
	})
	if err != nil {
		return nil, err
	}
	return s.buildOrderView(ctx, s.DB, orderID)
}

// FindingView maps to the ReviewFinding schema (read directly from frozen snapshots on the order side).
type FindingView struct {
	ID                 string   `json:"id"`
	StagePosition      int32    `json:"stage_position"`
	FingerprintGroupID *string  `json:"fingerprint_group_id"`
	Category           string   `json:"category"`
	Severity           string   `json:"severity"`
	Title              string   `json:"title"`
	Message            string   `json:"message"`
	Suggestion         *string  `json:"suggestion"`
	ModelConfidence    *float64 `json:"model_confidence"`
	EvidenceIDs        []string `json:"evidence_ids"`
}

// ListOrderFindings reads findings from frozen stage snapshots (R003 reuse; zero Provider calls).
func (s *Services) ListOrderFindings(ctx context.Context, actor Actor, orderID string, limit int, after string, meta ...RequestMeta) ([]*FindingView, *string, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, domain.ErrNotFound()
	}
	q := `
		select f.id::text as id, snap.review_run_id::text as run_id, sg.position as stage_position,
		       f.fingerprint_group_id::text as fingerprint_group_id, f.category, f.severity,
		       f.title, f.message, f.suggestion, f.model_confidence, f.created_at
		from review_findings f
		join stage_review_snapshots snap on snap.id = f.stage_review_id
		join order_stages sg on sg.stage_review_snapshot_id = snap.id
		where sg.order_id = ?`
	args := []any{orderID}
	if after != "" {
		if created, id, okc := splitCursor(after); okc {
			q += ` and (f.created_at, f.id) < (?::timestamptz, ?::uuid)`
			args = append(args, created, id)
		}
	}
	q += ` order by f.created_at desc, f.id desc limit ?`
	args = append(args, limit+1)
	var rows []struct {
		RunID              string
		ID                 string
		StagePosition      int32
		FingerprintGroupID *string
		Category           string
		Severity           string
		Title              string
		Message            string
		Suggestion         *string
		ModelConfidence    *float64
		CreatedAt          time.Time
	}
	if err := s.DB.WithContext(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	evMap := map[string][]string{}
	if len(ids) > 0 {
		var evRows []struct {
			FindingID  string
			EvidenceID string
		}
		if err := s.DB.WithContext(ctx).Raw(`
			select finding_id::text as finding_id, evidence_fact_id::text as evidence_id
			from review_finding_evidence where finding_id in ? order by evidence_fact_id`, ids).Scan(&evRows).Error; err != nil {
			return nil, nil, err
		}
		for _, r := range evRows {
			evMap[r.FindingID] = append(evMap[r.FindingID], r.EvidenceID)
		}
	}
	restorer := newOrderIdentifierDisplay(s, actor, meta)
	out := make([]*FindingView, 0, len(rows))
	for _, r := range rows {
		ev := evMap[r.ID]
		if ev == nil {
			ev = []string{}
		}
		restored, err := restorer.restore(ctx, r.RunID, &FindingView{
			ID: r.ID, StagePosition: r.StagePosition, FingerprintGroupID: r.FingerprintGroupID,
			Category: r.Category, Severity: r.Severity, Title: r.Title, Message: r.Message,
			Suggestion: r.Suggestion, ModelConfidence: r.ModelConfidence,
			EvidenceIDs: ev,
		})
		if err != nil {
			return nil, nil, err
		}
		out = append(out, restored)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		cur := last.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z") + "|" + last.ID
		next = &cur
	}
	if err := restorer.audit(ctx); err != nil {
		return nil, nil, err
	}
	return out, next, nil
}

// SQLRevealResult plaintext Reveal response.
type SQLRevealResult struct {
	RevealID   string
	SQL        string
	Watermark  string
	ValidUntil string
}

// buildWatermark dynamic watermark: viewer + server timestamp.
func buildWatermark(username string, at time.Time) string {
	return "Yearning SQL Viewer: " + username + " @ " + at.UTC().Format("2006-01-02T15:04:05Z")
}

// RevealOrderSQL decrypts work order SQL plaintext (relation check; no-store set by HTTP layer;
// writes an audit record on every Reveal). When purpose is rollback-order-sql, decrypts the rollback SQL from rollback evidence.
func (s *Services) RevealOrderSQL(ctx context.Context, actor Actor, meta RequestMeta, orderID, purpose string) (*SQLRevealResult, error) {
	if purpose == "rollback-order-sql" {
		return s.RevealOrderRollbackSQL(ctx, actor, meta, orderID, purpose)
	}
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	var payload persistence.OrderSQLPayloadModel
	if err := s.DB.WithContext(ctx).Where("order_id = ?", orderID).First(&payload).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	plaintext, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: payload.SQLCiphertext, Nonce: payload.SQLNonce,
		KeyID: payload.SQLKeyID, Algorithm: payload.SQLAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "order_sql_payloads",
		PrimaryKey: orderID, Column: "sql_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return nil, err
	}
	mac, err := s.Crypter.HMAC("sql_fingerprint", plaintext)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hex.EncodeToString(mac), payload.PlaintextHMAC) {
		return nil, domain.ErrSubmissionGate("sql_hmac_mismatch")
	}
	revealID := newID()
	now := nowUTC()
	res := &SQLRevealResult{
		RevealID:   revealID,
		SQL:        string(plaintext),
		Watermark:  buildWatermark(actor.Username, now),
		ValidUntil: now.Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if err := s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "workorder.sql_revealed", ActorKind: "user",
		ActorID: strPtrOrNil(actor.UserID), ActorUsername: strPtrOrNil(actor.Username),
		ResourceType: "change_order", ResourceID: strPtrOrNil(orderID),
		Action: "reveal", Outcome: "succeeded",
		RequestID: strPtrOrNil(meta.RequestID), SourceIP: strPtrOrNil(meta.SourceIP),
		UserAgent: strPtrOrNil(meta.UserAgent),
		Metadata:  map[string]any{"reveal_id": revealID, "purpose": purpose},
	}); err != nil {
		return nil, err
	}
	return res, nil
}

// RecordSQLCopy records a copy event (without storing copied content; binds the reveal audit ID).
// SEC-0035: source reveal ownership check — the referenced reveal must be a Reveal audit record
// issued to the current user for this work order, preventing arbitrary reveal_id cross-linking in the audit chain.
func (s *Services) RecordSQLCopy(ctx context.Context, actor Actor, meta RequestMeta, orderID, revealID string) error {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrNotFound()
	}
	if err := s.verifyRevealOwnership(ctx, actor.UserID, orderID, revealID); err != nil {
		return err
	}
	return s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "workorder.sql_copied", ActorKind: "user",
		ActorID: strPtrOrNil(actor.UserID), ActorUsername: strPtrOrNil(actor.Username),
		ResourceType: "change_order", ResourceID: strPtrOrNil(orderID),
		Action: "copy", Outcome: "succeeded",
		RequestID: strPtrOrNil(meta.RequestID), SourceIP: strPtrOrNil(meta.SourceIP),
		UserAgent: strPtrOrNil(meta.UserAgent),
		Metadata:  map[string]any{"reveal_id": revealID},
	})
}

// verifyRevealOwnership verifies that revealID was issued by a sql_revealed audit record
// for this user and work order (non-existent / foreign user / foreign order -> RESOURCE_NOT_FOUND without leaking existence).
func (s *Services) verifyRevealOwnership(ctx context.Context, userID, orderID, revealID string) error {
	if revealID == "" {
		return domain.ErrNotFound()
	}
	var n int64
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from audit_events
		where event_type = 'workorder.sql_revealed'
		  and actor_kind = 'user' and actor_id = ?
		  and resource_type = 'change_order' and resource_id = ?
		  and metadata->>'reveal_id' = ?`,
		userID, orderID, revealID).Scan(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound()
	}
	return nil
}

// hasDDLStatements reports whether any stage review snapshot of the order contains DDL statements.
func (s *Services) hasDDLStatements(ctx context.Context, db *gorm.DB, stages []persistence.OrderStageModel) bool {
	if len(stages) == 0 {
		return false
	}
	snapIDs := make([]string, 0, len(stages))
	for _, st := range stages {
		if st.StageReviewSnapshotID != "" {
			snapIDs = append(snapIDs, st.StageReviewSnapshotID)
		}
	}
	if len(snapIDs) == 0 {
		return false
	}
	var ddlCount int64
	if err := db.WithContext(ctx).Raw(`
		select count(*) from sql_statements s
		join stage_review_snapshots snap on snap.review_run_id = s.review_run_id
		where snap.id in ? and s.statement_kind = 'ddl'`, snapIDs).Scan(&ddlCount).Error; err != nil || ddlCount == 0 {
		return false
	}
	return true
}

// isOscChoiceAvailable reports whether the order targets a MySQL datasource with DDL statements
// while the global execution OSC policy has gh-ost enabled.
func (s *Services) isOscChoiceAvailable(ctx context.Context, db *gorm.DB, stages []persistence.OrderStageModel) bool {
	if len(stages) == 0 {
		return false
	}
	var mysqlSnapID string
	for _, st := range stages {
		if strings.EqualFold(st.DatasourceEngineSnapshot, "mysql") && st.StageReviewSnapshotID != "" {
			mysqlSnapID = st.StageReviewSnapshotID
			break
		}
	}
	if mysqlSnapID == "" {
		return false
	}
	var oscEnabled int64
	if err := db.WithContext(ctx).Raw(`
		select count(*) from system_settings
		where namespace = 'execution' and settings::jsonb -> 'osc' ->> 'tool' = 'gh-ost'`).Scan(&oscEnabled).Error; err != nil || oscEnabled == 0 {
		return false
	}
	var ddlCount int64
	if err := db.WithContext(ctx).Raw(`
		select count(*) from sql_statements s
		join stage_review_snapshots snap on snap.review_run_id = s.review_run_id
		where snap.id = ? and s.statement_kind = 'ddl'`, mysqlSnapID).Scan(&ddlCount).Error; err != nil || ddlCount == 0 {
		return false
	}
	return true
}
