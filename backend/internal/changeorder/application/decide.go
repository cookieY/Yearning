// decide.go Approval decision (decide_change_approval command, serializable).
// Approvers only read the frozen Review snapshot without re-running models (R003: approval
// reuses the accepted submission review); first peer at the same step to decide wins
// (single effective decision), and any rejection immediately rejects the entire order (W003);
// approving the final step only enters execution_pending and never auto-executes (W006).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
)

// DecideInput parameters for an approval decision.
type DecideInput struct {
	OrderID        string
	ExpectVersion  int64
	Decision       string // approve | reject
	Comment        string
	IdempotencyKey string
}

const idemRouteDecide = "POST /change-orders/{order_id}/approval-decisions"

// Decide records an approval decision and advances the state machine.
func (s *Services) Decide(ctx context.Context, actor Actor, meta RequestMeta, in DecideInput) (view *OrderView, replayed bool, err error) {
	if in.Decision != "approve" && in.Decision != "reject" {
		return nil, false, domain.ErrValidation()
	}
	// UX-003 ruling: rejection requires a reason (the reason enters the approval timeline visible to the submitter).
	if in.Decision == "reject" && strings.TrimSpace(in.Comment) == "" {
		return nil, false, domain.ErrValidation()
	}
	reqHash := idemRequestHash(actor.UserID, in.OrderID, in.Decision, in.Comment, fmt.Sprint(in.ExpectVersion))
	claimed, replay, err := s.idemClaim(ctx, actor.UserID, idemRouteDecide, in.OrderID, in.IdempotencyKey, reqHash)
	if err != nil {
		return nil, false, err
	}
	if claimed == nil {
		var v OrderView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	view, err = s.decideTx(ctx, actor, in)
	if err != nil {
		_ = s.idemRelease(ctx, actor.UserID, idemRouteDecide, in.OrderID, in.IdempotencyKey)
		return nil, false, err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, false, err
	}
	if err := s.idemComplete(ctx, actor.UserID, idemRouteDecide, in.OrderID, in.IdempotencyKey, raw); err != nil {
		return nil, false, err
	}
	return view, false, nil
}

func (s *Services) decideTx(ctx context.Context, actor Actor, in DecideInput) (*OrderView, error) {
	var view *OrderView
	err := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.decideOnce(ctx, tx, actor, in)
		if err != nil {
			return err
		}
		view = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// activeStepRow represents the currently active approval step and its stage.
type activeStepRow struct {
	StepID      string
	StageID     string
	Position    int32
	StepState   string
	StageState  string
	MaxPosition int32
}

// decideOnce executes the command body.
func (s *Services) decideOnce(ctx context.Context, tx *gorm.DB, actor Actor, in DecideInput) (*OrderView, error) {
	// 1. Work order row lock + version.
	order, err := s.loadOrderForUpdate(ctx, tx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if order.Version != in.ExpectVersion {
		return nil, domain.ErrConcurrent()
	}
	// 2. Frozen Actor health materialization (W008): converge to invalid first if the frozen set was emptied by user deletion.
	if err := s.materializeInvalidIfNeeded(ctx, tx, order); err != nil {
		return nil, err
	}
	if order.State != string(domain.StateStageApproval) {
		// Non-(stage_approval_active, user decision) combinations are rejected by the state machine.
		return nil, domain.ErrIllegal()
	}
	// 3. Current active step (order_one_active_approval_step guarantees at most one).
	var step activeStepRow
	if err := tx.WithContext(ctx).Raw(`
		select st.id::text as step_id, st.stage_id::text as stage_id, st.position,
		       st.state as step_state, sg.state as stage_state,
		       (select max(position) from order_approval_steps where stage_id = st.stage_id) as max_position
		from order_approval_steps st
		join order_stages sg on sg.id = st.stage_id
		where sg.order_id = ? and sg.state = 'approval_active' and st.state = 'active'`,
		in.OrderID).Scan(&step).Error; err != nil {
		return nil, err
	}
	if step.StepID == "" {
		return nil, domain.ErrIllegal()
	}
	// 4. Actor must be a frozen approver of this step (3001; exact UUID match, fixing legacy
	// substring false-match defect).
	var actorCount int64
	if err := tx.WithContext(ctx).Raw(
		`select count(*) from order_approval_actors where step_id = ? and reviewer_user_id_snapshot = ?`,
		step.StepID, actor.UserID).Scan(&actorCount).Error; err != nil {
		return nil, err
	}
	if actorCount == 0 {
		return nil, domain.ErrNotFrozenActor()
	}
	// 5. Single effective decision (3002; backed by approval_step_single_effective_decision).
	var decided int64
	if err := tx.WithContext(ctx).Raw(
		`select count(*) from approval_decisions where step_id = ?`, step.StepID).
		Scan(&decided).Error; err != nil {
		return nil, err
	}
	if decided > 0 {
		return nil, domain.ErrApprovalDecided()
	}
	// 6. Append decision (append_exactly_one).
	now := nowUTC()
	if err := tx.WithContext(ctx).Create(&persistence.ApprovalDecisionModel{
		ID: newID(), OrderID: in.OrderID, StageID: step.StageID, StepID: step.StepID,
		ReviewerUserIDSnapshot: actor.UserID, ReviewerUsername: actor.Username,
		Decision: in.Decision, Comment: truncate(in.Comment, 4096), OccurredAt: now,
	}).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrApprovalDecided()
		}
		return nil, err
	}

	auditActor := auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username}
	nextOrderVersion := order.Version + 1

	if in.Decision == "approve" {
		return s.decideApprove(ctx, tx, actor, order, step, now, nextOrderVersion, auditActor)
	}
	return s.decideReject(ctx, tx, actor, order, step, now, nextOrderVersion, auditActor)
}

// decideApprove approval: non-final step -> activate next step; final step -> stage execution_pending +
// order stage_execution_pending (explicitly never auto-executes).
func (s *Services) decideApprove(ctx context.Context, tx *gorm.DB, actor Actor,
	order *persistence.ChangeOrderModel, step activeStepRow, now time.Time,
	nextVersion int64, auditActor auditlog.Actor) (*OrderView, error) {
	// Current step -> approved.
	if err := touchStep(ctx, tx, step.StepID, string(domain.StepApproved), now); err != nil {
		return nil, err
	}
	if step.Position < step.MaxPosition {
		// Next step -> active (serial progression within the same stage; 0 rows = position gap, fail closed
		// to prevent the order from getting stuck without an active step — ignored_rows_affected is forbidden).
		res := tx.WithContext(ctx).Exec(`
			update order_approval_steps set state = 'active'
			where stage_id = ? and position = ? and state = 'pending'`,
			step.StageID, step.Position+1)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected != 1 {
			return nil, domain.ErrConcurrent()
		}
		// Order state remains unchanged (still stage_approval_active); version advances to record the decision.
		if err := s.updateOrderVersioned(ctx, tx, order, order.Version, map[string]any{
			"version": nextVersion,
		}); err != nil {
			return nil, err
		}
		// S007: activating the next step in the same stage emits step_activated in the same transaction (RCP-20261031;
		// notification to-dos resolve frozen approvers of the new step from this event on the consumer side).
		var nextStep struct {
			StepID string `gorm:"column:step_id"`
		}
		if err := tx.WithContext(ctx).Raw(`
			select id::text as step_id from order_approval_steps
			where stage_id = ? and position = ?`, step.StageID, step.Position+1).
			Scan(&nextStep).Error; err != nil {
			return nil, err
		}
		if nextStep.StepID == "" {
			return nil, domain.ErrConcurrent()
		}
		if err := s.emitOrderEvent(ctx, tx, EventStepActivated, order.ID, auditActor, StepActivatedPayload{
			AggregateID: order.ID, StageID: step.StageID, StepID: nextStep.StepID,
			AggregateVersion: nextVersion,
		}); err != nil {
			return nil, err
		}
	} else {
		// Final step: stage -> execution_pending (live non-terminal state), order ->
		// stage_execution_pending.
		if err := touchStageState(ctx, tx, step.StageID, string(domain.StageExecution), now); err != nil {
			return nil, err
		}
		nextState, err := domain.OrderTransition(domain.OrderState(order.State), domain.ActionApproveFinalStep)
		if err != nil {
			return nil, domain.ErrIllegal()
		}
		if err := s.updateOrderVersioned(ctx, tx, order, order.Version, map[string]any{
			"state": string(nextState), "version": nextVersion,
		}); err != nil {
			return nil, err
		}
		if err := s.emitOrderEvent(ctx, tx, EventStageReadyForExec, order.ID, auditActor, StateChangedPayload{
			AggregateID: order.ID, From: strPtr(order.State), To: string(nextState),
			ReasonCode: "final_step_approved", StageID: strPtr(step.StageID),
			AggregateVersion: nextVersion,
		}); err != nil {
			return nil, err
		}
	}
	// Approval decision event (approvalDecided def; comment is only written to the decision row/timeline,
	// not into the event payload — SEC-0036).
	if err := s.emitOrderEvent(ctx, tx, EventApprovalDecided, order.ID, auditActor, ApprovalDecidedPayload{
		OrderID: order.ID, StageID: step.StageID, StepID: step.StepID,
		ReviewerUserID: actor.UserID, Decision: "approve",
		AggregateVersion: nextVersion,
	}); err != nil {
		return nil, err
	}
	return s.buildOrderView(ctx, tx, order.ID)
}

// decideReject rejection: immediately rejects the entire order; active stage becomes cancelled, remaining steps skipped.
// The rejection reason was already persisted to the approval decision row and timeline in decideOnce, and is excluded from domain events (SEC-0036).
func (s *Services) decideReject(ctx context.Context, tx *gorm.DB, actor Actor,
	order *persistence.ChangeOrderModel, step activeStepRow, now time.Time,
	nextVersion int64, auditActor auditlog.Actor) (*OrderView, error) {
	if err := touchStep(ctx, tx, step.StepID, string(domain.StepRejected), now); err != nil {
		return nil, err
	}
	// Remaining pending steps in the same stage -> skipped (including decided_at timestamp).
	if err := tx.WithContext(ctx).Exec(`
		update order_approval_steps set state = 'skipped', decided_at = ?
		where stage_id = ? and state in ('pending', 'active')`, now, step.StageID).Error; err != nil {
		return nil, err
	}
	// Active stage -> cancelled (paired activated + terminal).
	if err := touchStageState(ctx, tx, step.StageID, string(domain.StageCancelled), now); err != nil {
		return nil, err
	}
	nextState, err := domain.OrderTransition(domain.OrderState(order.State), domain.ActionReject)
	if err != nil {
		return nil, domain.ErrIllegal()
	}
	if err := s.updateOrderVersioned(ctx, tx, order, order.Version, map[string]any{
		"state": string(nextState), "version": nextVersion, "terminal_at": now,
	}); err != nil {
		return nil, err
	}
	if err := s.restoreSourceOrderRollbackStatusOnAbort(ctx, tx, order, now); err != nil {
		return nil, err
	}
	// Approval decision event (approvalDecided def; comment is excluded from payload — SEC-0036).
	if err := s.emitOrderEvent(ctx, tx, EventApprovalDecided, order.ID, auditActor, ApprovalDecidedPayload{
		OrderID: order.ID, StageID: step.StageID, StepID: step.StepID,
		ReviewerUserID: actor.UserID, Decision: "reject",
		AggregateVersion: nextVersion,
	}); err != nil {
		return nil, err
	}
	if err := s.emitOrderEvent(ctx, tx, EventOrderRejected, order.ID, auditActor, StateChangedPayload{
		AggregateID: order.ID, From: strPtr(order.State), To: string(nextState),
		ReasonCode: "approval_rejected", StageID: strPtr(step.StageID),
		AggregateVersion: nextVersion,
	}); err != nil {
		return nil, err
	}
	return s.buildOrderView(ctx, tx, order.ID)
}

// touchStep updates the approval step terminal state and decided_at timestamp.
func touchStep(ctx context.Context, tx *gorm.DB, stepID, state string, at time.Time) error {
	res := tx.WithContext(ctx).Exec(
		`update order_approval_steps set state = ?, decided_at = ? where id = ?`, state, at, stepID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("changeorder: approval step update cardinality")
	}
	return nil
}

// touchStageState updates the stage state (preserving activated_at; filling terminal_at for terminal states).
func touchStageState(ctx context.Context, tx *gorm.DB, stageID, state string, at time.Time) error {
	var res *gorm.DB
	switch domain.StageState(state) {
	case domain.StageExecution:
		res = tx.WithContext(ctx).Exec(
			`update order_stages set state = ?, version = version + 1 where id = ?`, state, stageID)
	default:
		res = tx.WithContext(ctx).Exec(
			`update order_stages set state = ?, terminal_at = ?, version = version + 1 where id = ?`, state, at, stageID)
	}
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("changeorder: order stage update cardinality")
	}
	return nil
}

func truncate(s string, n int) string {
	// Accumulate rune by rune up to the byte limit (truncation always lands on a valid UTF-8 boundary,
	// preventing PG text from rejecting invalid byte sequences).
	var b strings.Builder
	for _, r := range s {
		if b.Len()+utf8.RuneLen(r) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func strPtr(s string) *string { return &s }
