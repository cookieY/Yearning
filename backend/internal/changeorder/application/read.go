// read.go Work order read surface: view construction, listing, frozen Review findings, and SQL Reveal (zero Provider calls, R003).
package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"gorm.io/gorm"
	"strings"
	"time"
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
	RequestedScheduledFor *string `json:"requested_scheduled_for,omitempty"`
	// Active schedule (owner ruling 2026-10-30): carried only when state=scheduled.
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

// buildOrderViews assembles work order views in constant query count over a slice of ChangeOrderModel rows
// (eliminating per-order N+1 queries on stages, steps, actors, SQL payload presence, rollback metadata,
// DDL snapshot checks, OSC settings, active schedules, and latest execution attempts).
func (s *Services) buildOrderViews(ctx context.Context, db *gorm.DB, orders []persistence.ChangeOrderModel) ([]*OrderView, error) {
	if len(orders) == 0 {
		return []*OrderView{}, nil
	}

	orderIDs := make([]string, 0, len(orders))
	sourceIDSet := make(map[string]struct{})
	var sourceOrderIDs []string
	var scheduledOrderIDs []string
	for i := range orders {
		o := &orders[i]
		orderIDs = append(orderIDs, o.ID)
		if o.SourceOrderID != nil && *o.SourceOrderID != "" {
			if _, exists := sourceIDSet[*o.SourceOrderID]; !exists {
				sourceIDSet[*o.SourceOrderID] = struct{}{}
				sourceOrderIDs = append(sourceOrderIDs, *o.SourceOrderID)
			}
		}
		if o.State == "scheduled" {
			scheduledOrderIDs = append(scheduledOrderIDs, o.ID)
		}
	}

	// 1. Batched order_stages
	var stageRows []persistence.OrderStageModel
	if err := db.WithContext(ctx).Where("order_id in ?", orderIDs).
		Order("order_id").Order("position").Find(&stageRows).Error; err != nil {
		return nil, err
	}
	stagesByOrder := make(map[string][]persistence.OrderStageModel, len(orders))
	stageIDs := make([]string, 0, len(stageRows))
	snapIDSet := make(map[string]struct{})
	var snapIDs []string
	for _, st := range stageRows {
		stagesByOrder[st.OrderID] = append(stagesByOrder[st.OrderID], st)
		stageIDs = append(stageIDs, st.ID)
		if st.StageReviewSnapshotID != "" {
			if _, exists := snapIDSet[st.StageReviewSnapshotID]; !exists {
				snapIDSet[st.StageReviewSnapshotID] = struct{}{}
				snapIDs = append(snapIDs, st.StageReviewSnapshotID)
			}
		}
	}

	// 2-4. Batched steps, approval actors, and execution actors (each helper guards len(stageIDs) == 0).
	steps, err := s.loadOrderSteps(ctx, db, stageIDs)
	if err != nil {
		return nil, err
	}
	stepsByStage := make(map[string][]persistence.OrderApprovalStepModel, len(stageIDs))
	for _, step := range steps {
		stepsByStage[step.StageID] = append(stepsByStage[step.StageID], step)
	}
	actorsByStep, err := s.loadApprovalActorsByStep(ctx, db, stageIDs)
	if err != nil {
		return nil, err
	}
	execByStage, err := s.loadExecutionActorsByStage(ctx, db, stageIDs)
	if err != nil {
		return nil, err
	}

	// 5. Batched order_sql_payloads presence.
	hasSQLSet := make(map[string]bool, len(orderIDs))
	var sqlOrderIDs []string
	if err := db.WithContext(ctx).Raw(
		`select order_id::text as order_id from order_sql_payloads where order_id in ?`, orderIDs,
	).Scan(&sqlOrderIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range sqlOrderIDs {
		hasSQLSet[id] = true
	}

	// 6. Batched rollback metadata (source display numbers, latest evidence, child rollback orders).
	srcDisplayByID := make(map[string]string, len(sourceOrderIDs))
	if len(sourceOrderIDs) > 0 {
		var srcRows []struct {
			ID            string `gorm:"column:id"`
			DisplayNumber string `gorm:"column:display_number"`
		}
		if err := db.WithContext(ctx).Raw(
			`select id::text as id, display_number from change_orders where id in ?`, sourceOrderIDs,
		).Scan(&srcRows).Error; err != nil {
			return nil, err
		}
		for _, r := range srcRows {
			if r.DisplayNumber != "" {
				srcDisplayByID[r.ID] = r.DisplayNumber
			}
		}
	}

	type rollbackEvidenceSummary struct {
		OrderID           string  `gorm:"column:order_id"`
		ID                string  `gorm:"column:id"`
		RollbackStatus    string  `gorm:"column:rollback_status"`
		UnsupportedReason *string `gorm:"column:unsupported_reason"`
		ConflictReason    *string `gorm:"column:conflict_reason"`
	}
	var evRows []rollbackEvidenceSummary
	if err := db.WithContext(ctx).Raw(`
		select distinct on (order_id)
		       order_id::text as order_id, id::text as id, rollback_status, unsupported_reason, conflict_reason
		from order_rollback_evidences
		where order_id in ?
		order by order_id, created_at desc`, orderIDs).Scan(&evRows).Error; err != nil {
		return nil, err
	}
	evByOrder := make(map[string]rollbackEvidenceSummary, len(evRows))
	for _, ev := range evRows {
		evByOrder[ev.OrderID] = ev
	}

	var childRows []struct {
		ID            string    `gorm:"column:id"`
		SourceOrderID string    `gorm:"column:source_order_id"`
		DisplayNumber string    `gorm:"column:display_number"`
		State         string    `gorm:"column:state"`
		SubmittedAt   time.Time `gorm:"column:submitted_at"`
	}
	if err := db.WithContext(ctx).Raw(`
		select id::text as id, source_order_id::text as source_order_id, display_number, state, submitted_at
		from change_orders
		where source_order_id in ?
		order by submitted_at desc, id desc`, orderIDs).Scan(&childRows).Error; err != nil {
		return nil, err
	}
	childrenBySource := make(map[string][]RollbackOrderRef)
	for _, c := range childRows {
		childrenBySource[c.SourceOrderID] = append(childrenBySource[c.SourceOrderID], RollbackOrderRef{
			ID:            c.ID,
			DisplayNumber: c.DisplayNumber,
			State:         c.State,
			SubmittedAt:   c.SubmittedAt.UTC().Format("2006-01-02T15:04:05.999999Z"),
		})
	}

	// 7. Batched DDL snapshot detection using EXISTS over sql_statements.
	ddlSnapSet := make(map[string]bool)
	if len(snapIDs) > 0 {
		var ddlSnapIDs []string
		if err := db.WithContext(ctx).Raw(`
			select snap.id::text as id
			from stage_review_snapshots snap
			where snap.id in ?
			  and exists (
			      select 1 from sql_statements s
			      where s.review_run_id = snap.review_run_id
			        and s.statement_kind = 'ddl'
			  )`, snapIDs).Scan(&ddlSnapIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range ddlSnapIDs {
			ddlSnapSet[id] = true
		}
	}

	// 8. Single OSC policy check for the page if any order has a MySQL stage with DDL.
	hasAnyMySQLDDL := false
	for _, st := range stageRows {
		if strings.EqualFold(st.DatasourceEngineSnapshot, "mysql") && st.StageReviewSnapshotID != "" && ddlSnapSet[st.StageReviewSnapshotID] {
			hasAnyMySQLDDL = true
			break
		}
	}
	oscGhOstEnabled := false
	if hasAnyMySQLDDL {
		var oscEnabled int64
		if err := db.WithContext(ctx).Raw(`
			select count(*) from system_settings
			where namespace = 'execution' and settings::jsonb -> 'osc' ->> 'tool' = 'gh-ost'`).Scan(&oscEnabled).Error; err != nil {
			return nil, err
		}
		oscGhOstEnabled = oscEnabled > 0
	}

	// 9. Active schedules (only for orders currently in scheduled state).
	schedByOrder := make(map[string]*ActiveScheduleView, len(scheduledOrderIDs))
	if len(scheduledOrderIDs) > 0 {
		var schedRows []struct {
			OrderID      string    `gorm:"column:order_id"`
			ID           string    `gorm:"column:id"`
			StageID      string    `gorm:"column:stage_id"`
			ScheduledFor time.Time `gorm:"column:scheduled_for"`
		}
		if err := db.WithContext(ctx).Raw(`
			select distinct on (order_id)
			       order_id::text as order_id, id::text as id, stage_id::text as stage_id, scheduled_for
			from execution_schedules
			where order_id in ? and state = 'scheduled'
			order by order_id, created_at asc`, scheduledOrderIDs).Scan(&schedRows).Error; err != nil {
			return nil, err
		}
		for _, sr := range schedRows {
			if sr.ID != "" {
				schedByOrder[sr.OrderID] = &ActiveScheduleView{
					ID:           sr.ID,
					StageID:      sr.StageID,
					ScheduledFor: sr.ScheduledFor.UTC().Format("2006-01-02T15:04:05.999999Z"),
					State:        "scheduled",
				}
			}
		}
	}

	// 10. Latest execution attempt per order.
	var attemptRows []struct {
		OrderID string `gorm:"column:order_id"`
		ID      string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).Raw(`
		select distinct on (order_id) order_id::text as order_id, id::text as id
		from execution_attempts
		where order_id in ?
		order by order_id, created_at desc`, orderIDs).Scan(&attemptRows).Error; err != nil {
		return nil, err
	}
	latestAttemptByOrder := make(map[string]string, len(attemptRows))
	for _, ar := range attemptRows {
		if ar.ID != "" {
			latestAttemptByOrder[ar.OrderID] = ar.ID
		}
	}

	out := make([]*OrderView, 0, len(orders))
	for i := range orders {
		o := &orders[i]
		view := &OrderView{
			ID: o.ID, DisplayNumber: o.DisplayNumber, SubmitterUserID: o.SubmitterUserIDSnapshot,
			SubmitterUsername: o.SubmitterUsernameSnapshot, SubmitterDisplayName: o.SubmitterDisplayNameSnapshot,
			Title: o.Title, State: o.State, CurrentStagePosition: o.CurrentStagePosition,
			HasSQL: hasSQLSet[o.ID], SQLHash: o.SQLHMAC, SnapshotHash: o.SnapshotHash,
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
		if o.SourceOrderID != nil && *o.SourceOrderID != "" {
			if srcDisplay, ok := srcDisplayByID[*o.SourceOrderID]; ok && srcDisplay != "" {
				d := srcDisplay
				view.SourceOrderDisplayNumber = &d
			}
		}
		if ev, ok := evByOrder[o.ID]; ok && ev.ID != "" {
			if view.RollbackEvidenceID == nil || *view.RollbackEvidenceID == "" {
				id := ev.ID
				view.RollbackEvidenceID = &id
			}
			view.RollbackUnsupportedReason = ev.UnsupportedReason
			view.RollbackConflictReason = ev.ConflictReason
		}
		if children := childrenBySource[o.ID]; len(children) > 0 {
			view.RollbackOrders = children
		}

		orderStages := stagesByOrder[o.ID]
		var hasDDL, hasMySQLDDL bool
		for _, st := range orderStages {
			if st.StageReviewSnapshotID != "" && ddlSnapSet[st.StageReviewSnapshotID] {
				hasDDL = true
				if strings.EqualFold(st.DatasourceEngineSnapshot, "mysql") {
					hasMySQLDDL = true
				}
			}
		}
		view.HasDDL = hasDDL
		view.OscChoiceAvailable = oscGhOstEnabled && hasMySQLDDL

		if o.RequestedScheduledFor != nil {
			rsf := o.RequestedScheduledFor.UTC().Format("2006-01-02T15:04:05.999999Z")
			view.RequestedScheduledFor = &rsf
		}
		if o.TerminalAt != nil {
			t := o.TerminalAt.UTC().Format("2006-01-02T15:04:05.999999Z")
			view.TerminalAt = &t
		}
		if o.State == "scheduled" {
			view.ActiveSchedule = schedByOrder[o.ID]
		}
		if latestAttemptID := latestAttemptByOrder[o.ID]; latestAttemptID != "" {
			aid := latestAttemptID
			view.LatestAttemptID = &aid
		}

		for _, st := range orderStages {
			sv := StageView{
				ID: st.ID, Position: st.Position, DatasourceName: st.DatasourceNameSnapshot,
				State: st.State,
			}
			for _, step := range stepsByStage[st.ID] {
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
		out = append(out, view)
	}
	return out, nil
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
	var hasDDL bool
	if err := db.WithContext(ctx).Raw(`
		select exists (
			select 1 from stage_review_snapshots snap
			where snap.id in ?
			  and exists (
			      select 1 from sql_statements s
			      where s.review_run_id = snap.review_run_id
			        and s.statement_kind = 'ddl'
			  )
		)`, snapIDs).Scan(&hasDDL).Error; err != nil {
		return false
	}
	return hasDDL
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
	var hasDDL bool
	if err := db.WithContext(ctx).Raw(`
		select exists (
			select 1 from stage_review_snapshots snap
			where snap.id = ?
			  and exists (
			      select 1 from sql_statements s
			      where s.review_run_id = snap.review_run_id
			        and s.statement_kind = 'ddl'
			  )
		)`, mysqlSnapID).Scan(&hasDDL).Error; err != nil {
		return false
	}
	return hasDDL
}
