// lifecycle.go Withdraw/void (withdraw_or_void_change_order command) and
// lazy invalid materialization when W008 frozen Actor sets become empty.
// Only the submitter can withdraw/void (non-submitters get 403 at the HTTP layer, admins included);
// frozen snapshots are never rewritten; scheduled executions are cancelled; succeeded stages retain facts without rollback.
// Withdraw triggers linked execution cancellation (SEC-0029/RCP-20261021-WITHDRAW-CANCELS-EXECUTION,
// owner ruling 2026-10-21): active Attempts enter the cancellation channel in the same transaction as order terminalization.
package application

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

// LifecycleInput parameters for withdraw/void.
type LifecycleInput struct {
	OrderID        string
	ExpectVersion  int64
	Reason         string
	IdempotencyKey string
}

// Withdraw withdraws a work order by the submitter (before the current stage starts execution).
func (s *Services) Withdraw(ctx context.Context, actor Actor, meta RequestMeta, in LifecycleInput) (view *OrderView, replayed bool, err error) {
	const route = "POST /change-orders/{order_id}/withdrawal"
	return s.lifecycle(ctx, actor, meta, in, route, domain.ActionWithdraw,
		EventOrderWithdrawn, "withdrawn_by_submitter")
}

// Void voids a work order by the submitter (including terminal paths from invalid and result_unknown).
func (s *Services) Void(ctx context.Context, actor Actor, meta RequestMeta, in LifecycleInput) (view *OrderView, replayed bool, err error) {
	const route = "POST /change-orders/{order_id}/voidance"
	return s.lifecycle(ctx, actor, meta, in, route, domain.ActionVoid,
		EventOrderVoided, "voided_by_submitter")
}

func (s *Services) lifecycle(ctx context.Context, actor Actor, meta RequestMeta, in LifecycleInput,
	route string, action domain.OrderTrigger, eventType string, reason string) (view *OrderView, replayed bool, err error) {
	reqHash := txutil.RequestHash(actor.UserID, in.OrderID, string(action), in.Reason)
	return txutil.RunIdempotent(ctx, s.idemStore(), actor.UserID, route, in.OrderID, in.IdempotencyKey, reqHash, func() (*OrderView, error) {
		return s.lifecycleTx(ctx, actor, meta, in, action, eventType, reason)
	})
}

func (s *Services) lifecycleTx(ctx context.Context, actor Actor, meta RequestMeta, in LifecycleInput,
	action domain.OrderTrigger, eventType string, reason string) (*OrderView, error) {
	var view *OrderView
	err := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.lifecycleOnce(ctx, tx, actor, meta, in, action, eventType, reason)
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

func (s *Services) lifecycleOnce(ctx context.Context, tx *gorm.DB, actor Actor, meta RequestMeta,
	in LifecycleInput, action domain.OrderTrigger, eventType string, reason string) (*OrderView, error) {
	order, err := s.loadOrderForUpdate(ctx, tx, in.OrderID)
	if err != nil {
		return nil, err
	}
	// Submitter snapshot ownership (W008: only the submitter may withdraw/void).
	if order.SubmitterUserIDSnapshot != actor.UserID {
		return nil, domain.ErrNotFound()
	}
	if order.Version != in.ExpectVersion {
		return nil, domain.ErrConcurrent()
	}
	// W008 materialization: emptied frozen set -> invalid (valid void sources include invalid).
	if err := s.materializeInvalidIfNeeded(ctx, tx, order); err != nil {
		return nil, err
	}
	state := domain.OrderState(order.State)
	// Withdraw on running/result_unknown uses the partial variant (machine declaration).
	trigger := action
	if action == domain.ActionWithdraw &&
		(state == domain.StateRunning || state == domain.StateResultUnknown) {
		trigger = domain.ActionWithdrawPartial
	}
	next, terr := domain.OrderTransition(state, trigger)
	if terr != nil {
		return nil, domain.ErrIllegal()
	}
	// Send boundary precondition (no_execution_send_boundary_crossed): withdraw is forbidden when
	// an Attempt exists that has already sent statements to the database (preserving execution domain
	// semantics for B10; B9 stages have no Attempts, but the check must not be omitted).
	var sent int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from execution_attempts a
		join order_stages st on st.id = a.stage_id
		where st.order_id = ? and a.send_boundary <> 'not_started'`, in.OrderID).
		Scan(&sent).Error; err != nil {
		return nil, err
	}
	if sent > 0 && trigger == domain.ActionWithdraw {
		return nil, domain.ErrIllegal()
	}
	now := nowUTC()
	nextVersion := order.Version + 1
	// Cancel unexpired execution schedules (postcondition: pending_schedule_cancelled).
	if err := tx.WithContext(ctx).Exec(`
		update execution_schedules set state = 'cancelled', version = version + 1, updated_at = ?
		where order_id = ? and state = 'scheduled'`, now, in.OrderID).Error; err != nil {
		return nil, err
	}
	// Withdraw-linked cancellation (SEC-0029): the withdraw command initiates execution cancellation
	// for active Attempts atomically in the same transaction as order terminalization; the Void path
	// has no running source (no such edge in the state machine) and needs no linkage.
	if action == domain.ActionWithdraw {
		if err := s.withdrawCancelLiveAttempts(ctx, tx, in.OrderID, actor, in.Reason); err != nil {
			return nil, err
		}
	}
	// Active stages (approval/execution/scheduled) -> cancelled; terminalized.
	if err := tx.WithContext(ctx).Exec(`
		update order_stages set state = 'cancelled', terminal_at = ?, version = version + 1
		where order_id = ? and state in ('approval_active', 'execution_pending', 'scheduled')`,
		now, in.OrderID).Error; err != nil {
		return nil, err
	}
	// Active approval steps -> skipped.
	if err := tx.WithContext(ctx).Exec(`
		update order_approval_steps set state = 'skipped', decided_at = ?
		where stage_id in (select id from order_stages where order_id = ?) and state = 'active'`,
		now, in.OrderID).Error; err != nil {
		return nil, err
	}
	if err := s.updateOrderVersioned(ctx, tx, order, order.Version, map[string]any{
		"state": string(next), "version": nextVersion, "terminal_at": now,
	}); err != nil {
		return nil, err
	}
	if err := s.restoreSourceOrderRollbackStatusOnAbort(ctx, tx, order, now); err != nil {
		return nil, err
	}
	auditActor := auditlog.UserActor(actor.UserID, actor.Username)
	if err := s.emitOrderEvent(ctx, tx, eventType, order.ID, auditActor, StateChangedPayload{
		AggregateID: order.ID, From: ptr.To(order.State), To: string(next),
		ReasonCode: reason, AggregateVersion: nextVersion,
	}); err != nil {
		return nil, err
	}
	// Terminal command writes an audit record (with submitter reason; no SQL or sensitive content).
	if err := s.Recorder.Audit(ctx, tx, auditlog.AuditInput{
		EventType: "change_order." + string(action), ActorKind: "user",
		ActorID: ptr.NonZero(actor.UserID), ActorUsername: ptr.NonZero(actor.Username),
		ResourceType: "change_order", ResourceID: ptr.NonZero(order.ID),
		Action: string(action), Outcome: "succeeded",
		RequestID: ptr.NonZero(meta.RequestID), SourceIP: ptr.NonZero(meta.SourceIP),
		UserAgent: ptr.NonZero(meta.UserAgent),
		Metadata:  map[string]any{"reason": truncate(in.Reason, 256)},
	}); err != nil {
		return nil, err
	}
	return s.buildOrderView(ctx, tx, order.ID)
}

// withdrawCancelLeaseWindow bounded lease window written when withdraw linkage sets cancelling
// (same 30s window as execution domain CancelTimeout, PRD §11). Runner heartbeat does not renew
// cancelling rows (execution/application run.go heartbeatLoop); once the window expires, the existing
// SweepCancelTimeouts lease condition picks it up for adjudication — covering the case where "Runner
// is alive but cancellation is ineffective" (missing target DB KILL privilege / unresponsive gh-ost /
// hung execution goroutine while heartbeat goroutine is alive) so the Attempt never stays stuck in cancelling forever.
const withdrawCancelLeaseWindow = 30 * time.Second

// withdrawCancelLiveAttempts performs withdraw-linked cancellation (SEC-0029 /
// RCP-20261021-WITHDRAW-CANCELS-EXECUTION, owner ruling 2026-10-21).
// Sets active Attempts (state='running') of the order to cancelling — sharing the same
// coordination point as the existing execution cancellation channel: Runner watchCancel
// (<=500ms polling) and inter-statement attemptCancelling checks immediately drive cancellation
// and let the Runner persist the terminal state; unconfirmed cancellations fall back to the
// Scheduler's existing reconciliation (SweepCancelTimeouts: boundary=not_started -> cancelled,
// crossed boundary -> result_unknown; ReconcileExpiredAttempts ignores cancelling). Setting
// cancelling simultaneously writes lease_expires_at=now()+30s (withdrawCancelLeaseWindow):
// heartbeat does not renew cancelling rows, so lease expiry triggers reconciliation pickup
// without requiring Runner process death (review loop closure revision).
// Does not invoke the execution domain user cancel command: its authorization model is frozen
// executor (W006), which neither the withdrawer nor the system identity may bypass, so system
// coordinated writes implement the same channel semantics.
// Send boundary semantics remain unchanged; already-cancelling or terminal Attempts are not
// re-cancelled (query narrows to state='running', and in-row CAS condition prevents race double-writes).
// Emits execution.cancel_requested audit per Attempt (same audit surface as user cancel command;
// metadata.linkage marks withdraw origin; no SQL or sensitive content).
func (s *Services) withdrawCancelLiveAttempts(ctx context.Context, tx *gorm.DB, orderID string,
	actor Actor, reason string) error {
	var attempts []struct {
		ID    string
		State string
	}
	if err := tx.WithContext(ctx).Raw(`
		select a.id::text as id, a.state as state
		from execution_attempts a
		join order_stages st on st.id = a.stage_id
		where st.order_id = ? and a.state = 'running'
		order by a.id`, orderID).Scan(&attempts).Error; err != nil {
		return err
	}
	for _, at := range attempts {
		// Query already narrows to running; eligibility re-check is kept as a double safeguard
		// against query drift. Attempts already in the cancellation channel or terminal state are not re-cancelled.
		if !withdrawCancelEligible(at.State) {
			continue
		}
		// Set cancelling and write a bounded lease atomically in a single statement: heartbeat
		// stops renewing before lease expiry (state<>'cancelling' condition), and reconciliation adjudicates upon expiry.
		leaseExpiry := nowUTC().Add(withdrawCancelLeaseWindow)
		res := tx.WithContext(ctx).Exec(`
			update execution_attempts set state = 'cancelling', lease_expires_at = ?,
				version = version + 1
			where id = ? and state = 'running'`, leaseExpiry, at.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// Already cancelled/terminal under race: skip without re-issuing cancellation.
			continue
		}
		if aerr := s.Recorder.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "execution.cancel_requested", ActorKind: "user",
			ActorID: ptr.NonZero(actor.UserID), ActorUsername: ptr.NonZero(actor.Username),
			ResourceType: "execution_attempt", ResourceID: ptr.NonZero(at.ID),
			Action: "cancel", Outcome: "succeeded",
			Metadata: withdrawCancelAuditMetadata(reason),
		}); aerr != nil {
			return aerr
		}
	}
	return nil
}

// withdrawCancelEligible reports whether an Attempt is eligible for withdraw-linked cancellation:
// only live running attempts participate; cancelling (already in cancellation channel) and terminal states are not re-cancelled.
func withdrawCancelEligible(state string) bool {
	return state == "running"
}

// withdrawCancelAuditMetadata builds the audit metadata payload for withdraw-linked cancellation
// (linkage marks the origin, reason is the truncated withdraw reason; no SQL or sensitive content).
func withdrawCancelAuditMetadata(reason string) map[string]any {
	return map[string]any{
		"linkage": "change_order.withdrawn",
		"reason":  truncate(reason, 256),
	}
}

// materializeInvalidIfNeeded W008 lazy materialization: when current or subsequent frozen
// approval nodes have no surviving candidate approvers, or an unexecuted stage has an empty
// executor set, the work order converges to invalid and empty-set steps are marked invalid.
// Snapshot rows themselves are never deleted.
// Synchronously updates the in-memory order before returning (for subsequent state checks by the caller).
func (s *Services) materializeInvalidIfNeeded(ctx context.Context, tx *gorm.DB,
	order *persistence.ChangeOrderModel) error {
	if domain.OrderTerminal(domain.OrderState(order.State)) || order.State == string(domain.StateInvalid) {
		return nil
	}
	// Count of surviving Actors for undecided approval steps (pending/active).
	type stepHealth struct {
		StepID    string
		LiveCount int64
	}
	var steps []stepHealth
	if err := tx.WithContext(ctx).Raw(`
		select st.id::text as step_id, count(live.id) as live_count
		from order_approval_steps st
		join order_stages sg on sg.id = st.stage_id
		left join order_approval_actors a on a.step_id = st.id
		left join users live on live.id = a.reviewer_user_id_snapshot
		where sg.order_id = ? and sg.state not in ('succeeded', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'skipped')
		  and st.state in ('pending', 'active')
		group by st.id`, order.ID).Scan(&steps).Error; err != nil {
		return err
	}
	emptySteps := map[string]bool{}
	invalid := false
	for _, sh := range steps {
		if sh.LiveCount == 0 {
			emptySteps[sh.StepID] = true
			invalid = true
		}
	}
	// Count of surviving executors for unexecuted stages.
	var execEmpty int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from order_stages sg
		where sg.order_id = ? and sg.state in ('pending', 'approval_active', 'execution_pending', 'scheduled')
		  and not exists (
		      select 1 from order_execution_actors ea
		      join users u on u.id = ea.executor_user_id_snapshot
		      where ea.stage_id = sg.id)`, order.ID).Scan(&execEmpty).Error; err != nil {
		return err
	}
	if execEmpty > 0 {
		invalid = true
	}
	if !invalid {
		return nil
	}
	// Check transition validity first: do not materialize if the source state is not declared
	// for the event (e.g., running/result_unknown), and never leave half-materialized facts
	// where "the step is marked invalid but the order did not converge".
	next, terr := domain.OrderTransition(domain.OrderState(order.State), domain.EventFrozenActorSetBecameEmpty)
	if terr != nil {
		return nil
	}
	now := nowUTC()
	for stepID := range emptySteps {
		if err := tx.WithContext(ctx).Exec(
			`update order_approval_steps set state = 'invalid', decided_at = ? where id = ? and state in ('pending','active')`,
			now, stepID).Error; err != nil {
			return err
		}
	}
	nextVersion := order.Version + 1
	// Set terminal_at: order_terminal_time in 000002 groups invalid into the non-advancing
	// branch (consistent with identity.active set); at the state-machine level invalid remains
	// non-terminal — its sole exit transition void is unaffected by this column.
	if err := s.updateOrderVersioned(ctx, tx, order, order.Version, map[string]any{
		"state": string(next), "version": nextVersion, "terminal_at": now,
	}); err != nil {
		return err
	}
	// workflow.node_invalidated (routed event; outputs change_order.state_changed discriminated
	// by affected_resource_type).
	if err := s.emitOrderEvent(ctx, tx, EventNodeInvalidated, order.ID,
		auditlog.SystemActor("", ""), NodeInvalidatedPayload{
			AffectedResourceType: "change_order", AggregateID: order.ID,
			From: ptr.To(order.State), To: string(next),
			ReasonCode: "frozen_actor_set_became_empty", AggregateVersion: nextVersion,
		}); err != nil {
		return err
	}
	order.State = string(next)
	order.Version = nextVersion
	return nil
}
