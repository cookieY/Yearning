// cancel.go cancelExecutionAttempt: a frozen executor requests cancellation (no one may bypass
// this via admin privileges). Cancellation is a request: if running in this process -> triggered
// immediately (DML rolls back the current transaction; DDL stops dispatching remaining statements);
// confirmed outcome is persisted by the Runner; unconfirmed after 30s -> result_unknown manual
// verification path. Non-running states idempotently return the current view.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

// CancelTimeout upper bound for waiting on target driver cancellation confirmation (PRD §11: 30s).
const CancelTimeout = 30 * time.Second

// sweepBatchLimit maximum batch size per reconciliation sweep (bounded batch; SEC-0022 prevents long transactions / full-table lock scans).
const sweepBatchLimit = 100

// cancelWatchdogDelay test-tunable cancel watchdog delay (production = CancelTimeout;
// timer starts at the cancellation request rather than Attempt start).
func (s *Services) cancelWatchdogDelay() time.Duration {
	if s.cancelDelayOverride > 0 {
		return s.cancelDelayOverride
	}
	return CancelTimeout
}

// CancelInput holds parameters for a cancellation request.
type CancelInput struct {
	AttemptID      string
	OrderID        string
	Actor          Actor
	ExpectVersion  int64
	IdempotencyKey string
	// Reason is the cancellation reason (OpenAPI ReasonRequest.required; recorded in audit events).
	Reason string
}

// Cancel requests cancellation of an Attempt (idempotent). Returns the Attempt view.
func (s *Services) Cancel(ctx context.Context, in CancelInput, runnerCancel func(attemptID string) bool) (view *AttemptView, replayed bool, err error) {
	route := "POST:/execution-attempts/{attempt_id}/cancellation"
	reqHash := txutil.RequestHash(in.Actor.UserID, in.AttemptID)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.AttemptID, in.IdempotencyKey, reqHash, func() (*AttemptView, error) {
		var out *AttemptView
		var triggerCancel bool
		txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			triggerCancel = false
			v, startedCancel, cerr := s.cancelOnce(ctx, tx, in)
			if cerr != nil {
				return cerr
			}
			out = v
			triggerCancel = startedCancel
			return nil
		})
		if txErr != nil {
			return nil, txErr
		}
		// Trigger in-process driver/context cancellation and start watchdog only after transaction commit
		// so rolled-back 40001 retries never abort live DB execution or leak duplicate watchdogs.
		if triggerCancel {
			if runnerCancel != nil {
				runnerCancel(in.AttemptID)
			}
			go s.cancelWatchdog(context.WithoutCancel(ctx), in.AttemptID, s.cancelWatchdogDelay())
		}
		return out, nil
	})
}

// cancelOnce marks the Attempt as cancelling and returns true when a new cancellation was initiated;
// terminal state is persisted by the Runner or timeout sweep.
func (s *Services) cancelOnce(ctx context.Context, tx *gorm.DB, in CancelInput) (*AttemptView, bool, error) {
	at, err := s.loadAttemptForUpdate(ctx, tx, in.AttemptID)
	if err != nil {
		return nil, false, err
	}
	if at.OrderID != in.OrderID {
		return nil, false, domain.ErrNotFound()
	}
	if !s.isFrozenExecutor(ctx, tx, at.StageID, in.Actor.UserID) {
		return nil, false, domain.ErrNotFrozen()
	}
	if domain.AttemptState(at.State).Terminal() {
		// Idempotent: already in a terminal state, return view directly (cancelled/unknown/failed cannot be cancelled again).
		v, berr := s.buildAttemptView(ctx, tx, at.ID)
		return v, false, berr
	}
	if in.ExpectVersion != at.Version {
		return nil, false, domain.ErrConcurrent()
	}
	if at.State == string(domain.AttemptCancelling) {
		// Already cancelling: return idempotently (waiting for Runner terminal state).
		v, berr := s.buildAttemptView(ctx, tx, at.ID)
		return v, false, berr
	}
	if at.State != string(domain.AttemptRunning) {
		return nil, false, domain.ErrIllegal()
	}
	// Set cancelling (both Runner inter-statement checks and watchCancel trigger driver cancellation;
	// the in-process handle is triggered by Cancel right after commit for immediate effect).
	if err := s.updateAttemptVersioned(ctx, tx, at, at.Version, map[string]any{
		"state": string(domain.AttemptCancelling), "version": at.Version + 1,
	}); err != nil {
		return nil, false, err
	}
	// Record cancellation reason in audit log (audit surface outside domain_events; excluded from domain event payload).
	if rerr := s.Recorder.Audit(ctx, tx, auditlog.AuditInput{
		EventType: "execution.cancel_requested", ActorKind: "user",
		ActorID: ptr.To(in.Actor.UserID), ActorUsername: ptr.To(in.Actor.Username),
		ResourceType: "execution_attempt", ResourceID: ptr.To(at.ID),
		Action: "cancel", Outcome: "succeeded",
		Metadata: map[string]any{"reason": in.Reason},
	}); rerr != nil {
		return nil, false, rerr
	}
	v, berr := s.buildAttemptView(ctx, tx, at.ID)
	if berr != nil {
		return nil, false, berr
	}
	return v, true, nil
}

// cancelWatchdog adjudicates cancellation timeout (PRD §11: wait 30s for confirmation).
func (s *Services) cancelWatchdog(ctx context.Context, attemptID string, delay time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	_ = s.sweepCancelOne(ctx, attemptID)
}

// SweepCancelTimeouts sweeps cancelling Attempts unconfirmed after 30 seconds:
// boundary=not_started -> cancelled (zero-send fact is certain); otherwise result_unknown
// (PRD: cancellation request unconfirmed after 30s -> manual verification path).
// Pickup condition = expired lease: cancelling rows are no longer renewed by heartbeat
// (run.go heartbeatLoop), and withdraw linkage simultaneously writes a 30s bounded lease when
// setting cancelling — so "Runner is alive but cancellation is ineffective" (missing KILL privilege /
// unresponsive gh-ost / hung execution goroutine while heartbeat goroutine is alive) likewise falls
// into this reconciliation after 30s without relying on process death (review loop closure revision).
// SEC-0022: called periodically by the scheduler (idempotent); bounded batch + per-row independent
// transaction isolation — a single-row failure does not block other rows (errors are aggregated and returned; caller logs without aborting).
func (s *Services) SweepCancelTimeouts(ctx context.Context) error {
	var ids []string
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text from execution_attempts
		where state = 'cancelling'
		  and lease_expires_at is not null and lease_expires_at < now()
		order by lease_expires_at
		limit ?`, sweepBatchLimit).Scan(&ids).Error; err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := s.sweepCancelOne(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("execution: sweep cancel attempt %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// sweepCancelOne adjudicates a single timed-out cancelling Attempt (independent serializable
// transaction; row-level isolation with in-row state re-check to prevent race double-writes).
func (s *Services) sweepCancelOne(ctx context.Context, attemptID string) error {
	return s.serializable(ctx, func(tx *gorm.DB) error {
		at, err := s.loadAttemptForUpdate(ctx, tx, attemptID)
		if err != nil || at.State != string(domain.AttemptCancelling) {
			if err != nil {
				return err
			}
			return nil
		}
		order, err := s.lockOrder(ctx, tx, at.OrderID)
		if err != nil {
			return err
		}
		stage, err := s.loadOrderStage(ctx, tx, at.OrderID)
		if err != nil {
			return err
		}
		actor := Actor{UserID: at.InitiatedByUserID, Username: at.InitiatedByUsername}
		if domain.SendBoundary(at.SendBoundary) == domain.BoundaryNotStarted {
			code := int(domain.CodeIllegalTransition)
			name := "EXECUTION_CANCELLED"
			now := nowUTC()
			if err := s.updateAttemptVersioned(ctx, tx, at, at.Version, map[string]any{
				"state": string(domain.AttemptCancelled), "finished_at": now,
				"business_err_code": &code, "failure_name": &name,
				"lease_owner": nil, "lease_expires_at": nil, "version": at.Version + 1,
			}); err != nil {
				return err
			}
			if err := s.touchStageLocked(ctx, tx, at.StageID, string(orderdomain.StageCancelled)); err != nil {
				return err
			}
			if orderdomain.OrderState(order.State) == orderdomain.StateRunning {
				next, terr := orderdomain.OrderTransition(orderdomain.StateRunning, orderdomain.EventCancelledWithoutPartial)
				if terr != nil {
					return domain.ErrIllegal()
				}
				if err := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version, map[string]any{
					"state": string(next), "version": order.Version + 1, "terminal_at": now,
				}); err != nil {
					return err
				}
				if err := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); err != nil {
					return err
				}
			}
			return nil
		}
		return s.markResultUnknown(ctx, tx, at, "CANCEL_NOT_CONFIRMED", actor, order, stage)
	})
}
