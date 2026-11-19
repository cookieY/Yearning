// record.go record_stage_execution_result command: atomically persists statement facts
// and Attempt terminal state observed by the Runner, and advances the stage and work order state
// machines (E001/E002).
// result_unknown is never automatically replayed; succeeded activates the next stage or completes the work order.
package application

import (
	"context"
	"time"

	"gorm.io/gorm"

	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
)

// stmtFact holds the terminal observed facts for a single statement.
type stmtFact struct {
	Ordinal      int32
	State        domain.StatementState
	AffectedRows *int64
	ErrCode      *int
	FailureName  *string
	Detail       []byte // Plaintext error detail (encrypted in server memory before persistence)
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// oscFact holds the terminal observed facts for a gh-ost run.
type oscFact struct {
	Phase       string
	ProgressBps int
	RowsCopied  int64
	Residual    string
}

// resultFacts holds the collection of terminal facts for an Attempt.
type resultFacts struct {
	Terminal    domain.AttemptState
	Boundary    domain.SendBoundary
	FailureName string
	ReasonCode  int
	Statements  []stmtFact
	Osc         *oscFact
	// SkipSuffix marks unreached trailing statements as skipped (for deterministic DDL failure / confirmed cancellation).
	SkipSuffix bool
}

// applyResult idempotently persists the terminal state and advances the work order (serializable; fence-checked).
// Terminal state writes use an uncancellable context: caller ctx cancellation (Pod shutdown / timeout)
// must not prevent persisting the audit trail of an already-observed terminal state (auditable terminal
// state; the cancellation path never produces a second send).
func (s *Services) applyResult(ctx context.Context, attemptID string, expectFence int64,
	facts resultFacts, actor Actor) error {
	durableCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	ctx = durableCtx
	return s.serializable(ctx, func(tx *gorm.DB) error {
		at, err := s.loadAttemptForUpdate(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if domain.AttemptState(at.State).Terminal() {
			return nil // Idempotent: terminal state already persisted (protects against task replay / race double-writes)
		}
		if at.FenceToken != expectFence || derefStr(at.LeaseOwner) != s.instanceOwner() {
			return domain.ErrConcurrent() // Stale worker fence invalidated; abort write
		}
		order, err := s.lockOrder(ctx, tx, at.OrderID)
		if err != nil {
			return err
		}
		stage, err := s.loadOrderStage(ctx, tx, at.OrderID)
		if err != nil {
			return err
		}
		if stage.StageID != at.StageID {
			return domain.ErrIllegal()
		}
		now := nowUTC()
		// Send boundary is monotonically non-regressing (ADR-0003): terminal write takes the greater of current and fact.
		boundary := domain.SendBoundary(at.SendBoundary).Advance(facts.Boundary)
		// Attempt terminal state (failed family requires code+name; succeeded must be NULL).
		attemptSets := map[string]any{
			"state": string(facts.Terminal), "send_boundary": string(boundary),
			"finished_at": now, "version": at.Version + 1,
			"lease_owner": nil, "lease_expires_at": nil,
		}
		if facts.Terminal != domain.AttemptSucceeded {
			code := facts.ReasonCode
			name := facts.FailureName
			attemptSets["business_err_code"] = &code
			attemptSets["failure_name"] = &name
		}
		if facts.Osc != nil {
			attemptSets["osc_phase"] = facts.Osc.Phase
			attemptSets["osc_progress_basis_points"] = facts.Osc.ProgressBps
			attemptSets["osc_rows_copied"] = facts.Osc.RowsCopied
			attemptSets["osc_residual_state"] = facts.Osc.Residual
		}
		if err := s.updateAttemptVersioned(ctx, tx, at, at.Version, attemptSets); err != nil {
			return err
		}
		// Statement facts (idempotently updated by ordinal; error details encrypted before persistence).
		if err := s.writeStatementFacts(ctx, tx, at, facts); err != nil {
			return err
		}
		if facts.SkipSuffix {
			if err := tx.WithContext(ctx).Exec(`
				update execution_statements set state = 'skipped'
				where attempt_id = ? and state = 'not_started'`, at.ID).Error; err != nil {
				return err
			}
		}
		// A search_path drift or unreadable snapshot is a review-invalidating
		// execution failure. It must close the stage even though it is
		// provably zero-send; ordinary pre-send connection failures keep the
		// stage running for the E004 same-order retry path.
		pathRejected := facts.Terminal == domain.AttemptFailed &&
			facts.Boundary == domain.BoundaryNotStarted &&
			(isPostgresSearchPathFailure(facts.FailureName) || isDeterministicZeroSendRejection(facts.FailureName))
		if !(facts.Terminal == domain.AttemptFailed &&
			facts.Boundary == domain.BoundaryNotStarted && !pathRejected) {
			stageState := stageStateOf(facts.Terminal)
			if err := s.touchStageLocked(ctx, tx, stage.StageID, stageState); err != nil {
				return err
			}
		}
		// Advance work order.
		return s.advanceOrder(ctx, tx, at, order, stage, facts, actor, now)
	})
}

// writeStatementFacts persists statement facts (including encrypted error envelopes).
func (s *Services) writeStatementFacts(ctx context.Context, tx *gorm.DB,
	at *persistence.ExecutionAttemptModel, facts resultFacts) error {
	for _, f := range facts.Statements {
		sets := map[string]any{
			"state": string(f.State), "started_at": f.StartedAt, "finished_at": f.FinishedAt,
		}
		if f.AffectedRows != nil {
			sets["affected_row_count"] = *f.AffectedRows
		} else {
			sets["affected_row_count"] = nil
		}
		if f.ErrCode != nil && f.FailureName != nil {
			sets["business_err_code"] = *f.ErrCode
			sets["failure_name"] = *f.FailureName
			if len(f.Detail) > 0 {
				ct, nonce, keyID, err := s.sealErrorDetail(at.ID, f.Ordinal, f.Detail)
				if err != nil {
					return err
				}
				sets["error_detail_ciphertext"] = ct
				sets["error_nonce"] = nonce
				sets["error_key_id"] = keyID
				sets["error_algorithm"] = "aes-256-gcm"
			}
		} else {
			sets["business_err_code"] = nil
			sets["failure_name"] = nil
		}
		res := tx.WithContext(ctx).Exec(
			`update execution_statements set `+setClause(sets)+` where attempt_id = ? and ordinal = ?`,
			append(setValues(sets), at.ID, f.Ordinal)...)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return domain.ErrConcurrent()
		}
	}
	return nil
}

// touchStageLocked updates stage state under lock (advances version; populates terminal_at for
// terminal stages — required by order_stage_time_consistency).
func (s *Services) touchStageLocked(ctx context.Context, tx *gorm.DB, stageID, state string) error {
	terminal := false
	switch state {
	case "succeeded", "failed", "partial_failed", "cancelled", "partial_cancelled":
		terminal = true
	}
	var res *gorm.DB
	if terminal {
		res = tx.WithContext(ctx).Exec(
			`update order_stages set state = ?, terminal_at = now(), version = version + 1 where id = ?`,
			state, stageID)
	} else {
		res = tx.WithContext(ctx).Exec(
			`update order_stages set state = ?, version = version + 1 where id = ?`, state, stageID)
	}
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	return nil
}

// stageStateOf maps Attempt terminal state -> stage terminal state.
func stageStateOf(t domain.AttemptState) string {
	switch t {
	case domain.AttemptSucceeded:
		return string(orderdomain.StageSucceeded)
	case domain.AttemptResultUnknown:
		return string(orderdomain.StageResultUnknown)
	default:
		return string(t) // failed/partial_failed/cancelled/partial_cancelled share the same names
	}
}

// advanceOrder advances the work order state machine according to the Attempt terminal state and emits domain events.
// When the work order has already been concurrently advanced (withdrawn/voided, etc.), only Attempt and stage facts are persisted without forcing a transition.
func (s *Services) advanceOrder(ctx context.Context, tx *gorm.DB, at *persistence.ExecutionAttemptModel,
	order *orderRow, stage *targetSnapshotRow, facts resultFacts, actor Actor, now time.Time) error {
	var trigger orderdomain.OrderTrigger
	var internalEvent, reason string
	oscFailure := domain.ExecutionKind(at.ExecutionKind).IsOscKind()
	switch facts.Terminal {
	case domain.AttemptSucceeded:
		last, lerr := s.isLastStage(ctx, tx, order.ID, stage.Position)
		if lerr != nil {
			return lerr
		}
		if last {
			trigger = orderdomain.EventFinalStageSucceeded
		} else {
			trigger = orderdomain.EventNonfinalStageSucceeded
		}
		internalEvent, reason = EventExecSucceeded, "execution_succeeded"
	case domain.AttemptFailed:
		trigger = orderdomain.EventDmlFailedRolledBack
		if oscFailure {
			internalEvent, reason = EventExecFailed, "osc_failed"
		} else if isPostgresSearchPathFailure(facts.FailureName) {
			internalEvent = EventExecFailed
			reason = postgresSearchPathFailureReason(facts.FailureName)
		} else {
			internalEvent, reason = EventExecFailed, "dml_failed_rolled_back"
		}
	case domain.AttemptPartialFailed:
		trigger = orderdomain.EventDdlPartialFailed
		internalEvent, reason = EventExecPartial, "ddl_partial_failed"
		if isPostgresSearchPathFailure(facts.FailureName) {
			reason = postgresSearchPathFailureReason(facts.FailureName)
		}
	case domain.AttemptCancelled:
		trigger = orderdomain.EventCancelledWithoutPartial
		// S007: emit event in the same transaction at the cancellation completion adjudication point
		// (RCP-20261031 overturns the former notification-side ruling of "zero events on cancellation completion";
		// the cancellation request itself still emits zero events and zero timeline entries).
		internalEvent, reason = EventExecCancelled, "cancelled_without_partial"
	case domain.AttemptPartialCancel:
		trigger = orderdomain.EventCancelledAfterPartial
		internalEvent, reason = EventExecPartialCancelled, "cancelled_after_partial"
	case domain.AttemptResultUnknown:
		trigger = orderdomain.EventResultBecameUnknown
		internalEvent, reason = EventExecUnknown, "result_became_unknown"
	default:
		return domain.ErrIllegal()
	}
	if facts.Terminal == domain.AttemptFailed &&
		facts.Boundary == domain.BoundaryNotStarted &&
		!isPostgresSearchPathFailure(facts.FailureName) &&
		!isDeterministicZeroSendRejection(facts.FailureName) {
		// Failure with provably zero sends (connection timeout / worker loss): order stays in running
		// so a frozen executor can retry on the same order (E004; the order state machine has no
		// running -> execution_pending edge, so retrying only creates a new Attempt).
		return nil
	}
	if orderdomain.OrderState(order.State) != orderdomain.StateRunning {
		// Work order is no longer running (already withdrawn/voided/unknown): persist facts only without rewriting history.
		return nil
	}
	next, terr := orderdomain.OrderTransition(orderdomain.StateRunning, trigger)
	if terr != nil {
		return domain.ErrIllegal()
	}
	nextVersion := order.Version + 1
	sets := map[string]any{"state": string(next), "version": nextVersion}
	if orderdomain.OrderTerminal(next) {
		sets["terminal_at"] = now
	}
	if err := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version, sets); err != nil {
		return err
	}
	if err := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); err != nil {
		return err
	}
	// Final stage succeeded -> completed; non-final stage succeeded -> activate next stage approval.
	if facts.Terminal == domain.AttemptSucceeded && next == orderdomain.StateStageApproval {
		if err := s.activateNextStage(ctx, tx, order.ID, stage.Position, nextVersion, actor); err != nil {
			return err
		}
	}
	if internalEvent != "" {
		if eerr := s.emitOrderEvent(ctx, tx, internalEvent, order.ID, auditActor(actor), StateChangedPayload{
			AggregateID: order.ID, From: strPtr(order.State), To: string(next),
			ReasonCode: reason, StageID: strPtr(stage.StageID), AggregateVersion: nextVersion,
		}); eerr != nil {
			return eerr
		}
	}
	return nil
}

// isLastStage reports whether position is the final stage of the work order.
func (s *Services) isLastStage(ctx context.Context, tx *gorm.DB, orderID string, position int32) (bool, error) {
	var maxPos *int32
	if err := tx.WithContext(ctx).Raw(
		`select max(position) from order_stages where order_id = ?`, orderID).Scan(&maxPos).Error; err != nil {
		return false, err
	}
	return maxPos != nil && *maxPos == position, nil
}

// activateNextStage activates approval for the next stage (stage -> approval_active + first step active).
// If the next stage's frozen approver set is empty -> work order becomes invalid (deterministic invalidation with the same semantics as W008).
// S007: activating the first step emits step_activated in the same transaction (event actor = frozen executor who completed the previous stage).
func (s *Services) activateNextStage(ctx context.Context, tx *gorm.DB, orderID string,
	afterPosition int32, orderVersion int64, actor Actor) error {
	var next struct {
		StageID string `gorm:"column:stage_id"`
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as stage_id from order_stages
		where order_id = ? and position = ?`, orderID, afterPosition+1).Scan(&next).Error; err != nil {
		return err
	}
	if next.StageID == "" {
		return domain.ErrIllegal()
	}
	// Frozen approver existence (validated at submission time; defensively re-checked here, empty set -> invalid).
	var actors int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from order_approval_actors a
		join order_approval_steps st on st.id = a.step_id
		where st.stage_id = ?`, next.StageID).Scan(&actors).Error; err != nil {
		return err
	}
	if actors == 0 {
		if _, terr := orderdomain.OrderTransition(orderdomain.StateStageApproval,
			orderdomain.EventNonfinalSucceededInvalidNext); terr != nil {
			return domain.ErrIllegal()
		}
		now := nowUTC()
		if err := s.updateOrderVersionedRaw(ctx, tx, orderID, orderVersion, map[string]any{
			"state": string(orderdomain.StateInvalid), "terminal_at": now,
		}); err != nil {
			return err
		}
		return nil
	}
	// pending -> approval_active requires populating activated_at (order_stage_time_consistency).
	res := tx.WithContext(ctx).Exec(`
		update order_stages set state = ?, activated_at = coalesce(activated_at, now()), version = version + 1
		where id = ?`, string(orderdomain.StageApproval), next.StageID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	// Activate first step (pending step with the smallest position).
	stepRes := tx.WithContext(ctx).Exec(`
		update order_approval_steps set state = 'active'
		where stage_id = ? and position = (select min(position) from order_approval_steps where stage_id = ?)`,
		next.StageID, next.StageID)
	if stepRes.Error != nil {
		return stepRes.Error
	}
	if stepRes.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	// S007: cross-stage first-step activation emits step_activated in the same transaction
	// (notification to-dos resolve frozen approvers of the new step from this event on the consumer side).
	var activated struct {
		StepID string `gorm:"column:step_id"`
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as step_id from order_approval_steps
		where stage_id = ? and state = 'active' order by position limit 1`,
		next.StageID).Scan(&activated).Error; err != nil {
		return err
	}
	if activated.StepID == "" {
		return domain.ErrConcurrent()
	}
	return s.emitOrderEvent(ctx, tx, EventStepActivated, orderID, auditActor(actor), StepActivatedPayload{
		AggregateID:      orderID,
		StageID:          next.StageID,
		StepID:           activated.StepID,
		AggregateVersion: orderVersion,
	})
}

// markResultUnknown sets the Attempt to result_unknown and advances the work order
// (covering all ambiguous boundaries: lease loss / unconfirmed cancellation after 30s / lost commit ACK, etc.).
func (s *Services) markResultUnknown(ctx context.Context, tx *gorm.DB,
	at *persistence.ExecutionAttemptModel, failureName string, actor Actor,
	order *orderRow, stage *targetSnapshotRow) error {
	now := nowUTC()
	code := int(domain.CodeResultUnknown)
	sets := map[string]any{
		"state": string(domain.AttemptResultUnknown), "finished_at": now,
		"business_err_code": &code, "failure_name": &failureName,
		"lease_owner": nil, "lease_expires_at": nil, "version": at.Version + 1,
	}
	if err := s.updateAttemptVersioned(ctx, tx, at, at.Version, sets); err != nil {
		return err
	}
	if err := s.touchStageLocked(ctx, tx, at.StageID, string(orderdomain.StageResultUnknown)); err != nil {
		return err
	}
	if orderdomain.OrderState(order.State) == orderdomain.StateRunning {
		next, terr := orderdomain.OrderTransition(orderdomain.StateRunning, orderdomain.EventResultBecameUnknown)
		if terr != nil {
			return domain.ErrIllegal()
		}
		nextVersion := order.Version + 1
		if err := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version, map[string]any{
			"state": string(next), "version": nextVersion,
		}); err != nil {
			return err
		}
		if err := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); err != nil {
			return err
		}
		return s.emitOrderEvent(ctx, tx, EventExecUnknown, order.ID, auditActor(actor), StateChangedPayload{
			AggregateID: order.ID, From: strPtr(order.State), To: string(next),
			ReasonCode: "result_became_unknown", StageID: strPtr(at.StageID),
			AggregateVersion: nextVersion,
		})
	}
	return nil
}
