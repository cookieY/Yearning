// schedule.go schedule_stage_execution command and due-time Scheduler adjudication (E007):
// a frozen executor creating a schedule = one explicit execution authorization for that planned time;
// at due time, the Scheduler automatically claims and executes it once (without requiring a second click);
// missed or blocked schedules are never backfilled.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
)

// Schedule window constants (frozen values from settings-namespaces execution.fixed_constants).
const (
	MinimumScheduleLeadMinutes = 5
	MaximumScheduleDays        = 30
)

// ScheduleClaimGrace grace window for due-time claims: a healthy Scheduler (5s tick) should
// claim within seconds; remaining unclaimed past the grace window is treated as missed due to
// downtime (missed, never backfilled). 60s covers GC pauses and brief DB jitter; tests inject
// via SetScheduleClaimGraceForTest.
const ScheduleClaimGrace = 60 * time.Second

// SetScheduleClaimGraceForTest injects the claim grace window for testing; called only in test builds.
var scheduleClaimGrace = ScheduleClaimGrace

func SetScheduleClaimGraceForTest(d time.Duration) { scheduleClaimGrace = d }

// ScheduleView conforms to OpenAPI ExecutionSchedule.
type ScheduleView struct {
	ID           string `json:"id"`
	StageID      string `json:"stage_id"`
	ScheduledFor string `json:"scheduled_for"`
	State        string `json:"state"`
}

// CreateScheduleInput holds parameters for creating a schedule.
type CreateScheduleInput struct {
	OrderID        string
	Actor          Actor
	ScheduledFor   time.Time
	ExpectVersion  int64
	IdempotencyKey string
}

// CreateSchedule creates a delayed execution schedule as a frozen executor (schedule_stage_execution:
// order -> scheduled; authorizes a single system execution at due time).
func (s *Services) CreateSchedule(ctx context.Context, in CreateScheduleInput) (*ScheduleView, bool, error) {
	route := "POST:/change-orders/{order_id}/execution-schedules"
	reqHash := idemRequestHash(in.Actor.UserID, in.OrderID, in.ScheduledFor.UTC().Format(time.RFC3339Nano))
	claimed, replay, cerr := s.idemClaim(ctx, in.Actor.UserID, route, in.OrderID, in.IdempotencyKey, reqHash)
	if cerr != nil {
		return nil, false, cerr
	}
	if !claimed {
		var v ScheduleView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	var out *ScheduleView
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.createScheduleOnce(ctx, tx, in)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if txErr != nil {
		var de *domain.Error
		if errors.As(txErr, &de) {
			_ = s.idemRelease(ctx, in.Actor.UserID, route, in.OrderID, in.IdempotencyKey)
		}
		return nil, false, txErr
	}
	raw, merr := json.Marshal(out)
	if merr != nil {
		return nil, false, merr
	}
	if cerr := s.idemComplete(ctx, in.Actor.UserID, route, in.OrderID, in.IdempotencyKey, raw); cerr != nil {
		return nil, false, cerr
	}
	return out, false, nil
}

func (s *Services) createScheduleOnce(ctx context.Context, tx *gorm.DB, in CreateScheduleInput) (*ScheduleView, error) {
	order, err := s.lockOrder(ctx, tx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if in.ExpectVersion != order.Version {
		return nil, domain.ErrConcurrent()
	}
	stage, err := s.loadOrderStage(ctx, tx, order.ID)
	if err != nil {
		return nil, err
	}
	if !s.isFrozenExecutor(ctx, tx, stage.StageID, in.Actor.UserID) {
		return nil, domain.ErrNotFrozen()
	}
	if orderdomain.OrderState(order.State) != orderdomain.StateStageExecution {
		return nil, domain.ErrIllegal()
	}
	// No live schedule or Attempt (backed against concurrency by partial unique indexes).
	live, sent, lerr := s.stageAttemptFacts(ctx, tx, stage.StageID)
	if lerr != nil {
		return nil, lerr
	}
	if live > 0 || sent > 0 {
		return nil, domain.ErrAlreadyStarted()
	}
	var liveSched int64
	if err := tx.WithContext(ctx).Raw(
		`select count(*) from execution_schedules where stage_id = ? and state = 'scheduled'`,
		stage.StageID).Scan(&liveSched).Error; err != nil {
		return nil, err
	}
	if liveSched > 0 {
		return nil, domain.ErrAlreadyStarted()
	}
	// Schedule window (settings fixed_constants): >= now + 5m and <= now + 30d.
	now := nowUTC()
	if in.ScheduledFor.Before(now.Add(MinimumScheduleLeadMinutes*time.Minute)) ||
		in.ScheduledFor.After(now.Add(MaximumScheduleDays*24*time.Hour)) {
		return nil, domain.ErrScheduleRange()
	}
	// Global execution window: scheduled time must fall within an allowed window (PRD07 §4).
	if werr := s.checkExecutionWindowAt(ctx, tx, in.ScheduledFor); werr != nil {
		return nil, werr
	}
	// Order -> scheduled; insert schedule row (atomic within the same transaction).
	next, terr := orderdomain.OrderTransition(orderdomain.StateStageExecution, orderdomain.ActionSchedule)
	if terr != nil {
		return nil, domain.ErrIllegal()
	}
	if uerr := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version,
		map[string]any{"state": string(next), "version": order.Version + 1}); uerr != nil {
		return nil, uerr
	}
	schedID := newID()
	row := &persistence.ExecutionScheduleModel{
		ID: schedID, OrderID: order.ID, StageID: stage.StageID,
		ScheduledByUserID: in.Actor.UserID, ScheduledByUsername: in.Actor.Username,
		ScheduledFor: in.ScheduledFor.UTC(), State: "scheduled",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrAlreadyStarted()
		}
		return nil, err
	}
	if eerr := s.emitOrderEvent(ctx, tx, "execution.scheduled", order.ID, auditActor(in.Actor), StateChangedPayload{
		AggregateID: order.ID, From: strPtr(order.State), To: string(next),
		ReasonCode: "executor_scheduled", StageID: strPtr(stage.StageID),
		AggregateVersion: order.Version + 1,
	}); eerr != nil {
		return nil, eerr
	}
	if err := s.Recorder.Audit(ctx, tx, auditInputFor(in.Actor, "execution.schedule_created",
		"execution_schedules", schedID, "schedule", "succeeded")); err != nil {
		return nil, err
	}
	return &ScheduleView{
		ID: schedID, StageID: stage.StageID,
		ScheduledFor: row.ScheduledFor.UTC().Format(time.RFC3339Nano), State: row.State,
	}, nil
}

// CancelScheduleInput holds parameters for cancelling a schedule.
type CancelScheduleInput struct {
	OrderID        string
	Actor          Actor
	ExpectVersion  int64
	IdempotencyKey string
}

// CancelSchedule allows any frozen executor to cancel an unexpired schedule (cancel_execution_schedule:
// order scheduled -> stage_execution_pending; schedule row CAS-finalized as cancelled).
// Mutually exclusive with due-time claim: both lock the order row first and then CAS the schedule row,
// so the first committer wins — if cancellation commits first, claim reads non-scheduled and skips;
// if claim commits first, the order state / CAS version check here rejects. Both cancellation outcome
// and schedule row persist in a single transaction, surviving process crash restarts without loss or
// drift (the scheduler reads from the table on every tick with no in-memory state).
func (s *Services) CancelSchedule(ctx context.Context, in CancelScheduleInput) (*ScheduleView, bool, error) {
	route := "POST:/change-orders/{order_id}/execution-schedules/cancellation"
	reqHash := idemRequestHash(in.Actor.UserID, in.OrderID, "cancel")
	claimed, replay, cerr := s.idemClaim(ctx, in.Actor.UserID, route, in.OrderID, in.IdempotencyKey, reqHash)
	if cerr != nil {
		return nil, false, cerr
	}
	if !claimed {
		var v ScheduleView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	var out *ScheduleView
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.cancelScheduleOnce(ctx, tx, in)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if txErr != nil {
		var de *domain.Error
		if errors.As(txErr, &de) {
			_ = s.idemRelease(ctx, in.Actor.UserID, route, in.OrderID, in.IdempotencyKey)
		}
		return nil, false, txErr
	}
	raw, merr := json.Marshal(out)
	if merr != nil {
		return nil, false, merr
	}
	if cerr := s.idemComplete(ctx, in.Actor.UserID, route, in.OrderID, in.IdempotencyKey, raw); cerr != nil {
		return nil, false, cerr
	}
	return out, false, nil
}

func (s *Services) cancelScheduleOnce(ctx context.Context, tx *gorm.DB, in CancelScheduleInput) (*ScheduleView, error) {
	order, err := s.lockOrder(ctx, tx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if in.ExpectVersion != order.Version {
		return nil, domain.ErrConcurrent()
	}
	stage, err := s.loadOrderStage(ctx, tx, order.ID)
	if err != nil {
		return nil, err
	}
	// Authorization = any frozen executor of the stage (owner ruling 2026-10-30; not restricted to
	// the schedule creator — scheduling is a role authorization rather than private ownership, matching due-time execution authorization).
	if !s.isFrozenExecutor(ctx, tx, stage.StageID, in.Actor.UserID) {
		return nil, domain.ErrNotFrozen()
	}
	if orderdomain.OrderState(order.State) != orderdomain.StateScheduled {
		return nil, domain.ErrIllegal()
	}
	// Schedule row lock + CAS: row missing or no longer scheduled = due-time claim already won or was already handled.
	var sched persistence.ExecutionScheduleModel
	if err := tx.WithContext(ctx).Clauses(forUpdate()).
		Where("stage_id = ? and state = 'scheduled'", stage.StageID).
		First(&sched).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrConcurrent()
		}
		return nil, err
	}
	next, terr := orderdomain.OrderTransition(orderdomain.StateScheduled, orderdomain.ActionCancelSchedule)
	if terr != nil {
		return nil, domain.ErrIllegal()
	}
	if uerr := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version,
		map[string]any{"state": string(next), "version": order.Version + 1}); uerr != nil {
		return nil, uerr
	}
	res := tx.WithContext(ctx).Exec(`
		update execution_schedules set state = 'cancelled', version = version + 1, updated_at = ?
		where id = ? and state = 'scheduled'`, nowUTC(), sched.ID)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, domain.ErrConcurrent()
	}
	if eerr := s.emitOrderEvent(ctx, tx, "execution.schedule_cancelled", order.ID, auditActor(in.Actor), StateChangedPayload{
		AggregateID: order.ID, From: strPtr(order.State), To: string(next),
		ReasonCode: "executor_cancelled_schedule", StageID: strPtr(stage.StageID),
		AggregateVersion: order.Version + 1,
	}); eerr != nil {
		return nil, eerr
	}
	if err := s.Recorder.Audit(ctx, tx, auditInputFor(in.Actor, "execution.schedule_cancelled",
		"execution_schedules", sched.ID, "cancel", "succeeded")); err != nil {
		return nil, err
	}
	return &ScheduleView{
		ID: sched.ID, StageID: stage.StageID,
		ScheduledFor: sched.ScheduledFor.UTC().Format(time.RFC3339Nano),
		State:        "cancelled",
	}, nil
}

// auditInputFor constructs execution domain audit input parameters.
func auditInputFor(a Actor, eventType, resourceType, resourceID, action, outcome string) auditlog.AuditInput {
	rid, uname := resourceID, a.Username
	return auditlog.AuditInput{
		EventType: eventType, ActorKind: "user", ActorID: &a.UserID,
		ActorUsername: &uname, ResourceType: resourceType,
		ResourceID: &rid, Action: action, Outcome: outcome,
	}
}

// SchedulerTickResult holds adjudication statistics for a single scheduler tick (for testing and observability).
type SchedulerTickResult struct {
	Claimed int // Claimed at due time and started execution
	Missed  int // Missed past grace window (never backfilled)
	Blocked int // Blocked due to datasource unavailability
	Skipped int // Skipped due to contention (already handled by another instance / state mismatch)
}

// dueScheduleRow projection of a schedule awaiting adjudication.
type dueScheduleRow struct {
	ID              string    `gorm:"column:id"`
	OrderID         string    `gorm:"column:order_id"`
	StageID         string    `gorm:"column:stage_id"`
	ScheduledBy     string    `gorm:"column:scheduled_by"`
	ScheduledByName string    `gorm:"column:scheduled_by_name"`
	ScheduledFor    time.Time `gorm:"column:scheduled_for"`
}

// ProcessDueSchedules processes due schedules (Scheduler tick body): claims once at due time,
// transitions to missed terminal state past grace window, or marks blocked on unavailable datasource;
// executes at most once across multiple instances via row lock + CAS.
func (s *Services) ProcessDueSchedules(ctx context.Context) (SchedulerTickResult, error) {
	res := SchedulerTickResult{}
	now := nowUTC()
	var due []dueScheduleRow
	// Candidates = already due (scheduled_for <= now): claim never advances the execution time early
	// (postcondition scheduler_may_begin_once_at_due_time);
	// the grace window applies only to miss adjudication (tolerance window for unclaimed due schedules).
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text, order_id::text, stage_id::text,
		       scheduled_by_user_id_snapshot as scheduled_by,
		       scheduled_by_username_snapshot as scheduled_by_name,
		       scheduled_for
		from execution_schedules
		where state = 'scheduled' and scheduled_for <= ?
		order by scheduled_for limit 50`, now).Scan(&due).Error; err != nil {
		return res, err
	}
	// L8: isolate errors per row — a single bad row must not starve subsequent due schedules.
	var loopErr error
	for _, row := range due {
		deadline := row.ScheduledFor.Add(scheduleClaimGrace)
		switch {
		case now.After(deadline):
			missed, err := s.markScheduleMissed(ctx, row)
			if err != nil {
				res.Skipped++
				if loopErr == nil {
					loopErr = err
				}
				continue
			}
			if missed {
				res.Missed++
			} else {
				res.Skipped++
			}
		default:
			outcome, err := s.claimDueSchedule(ctx, row)
			if err != nil {
				res.Skipped++
				if loopErr == nil {
					loopErr = err
				}
				continue
			}
			switch outcome {
			case dueClaimed:
				res.Claimed++
			case dueBlocked:
				res.Blocked++
			default:
				res.Skipped++
			}
		}
	}
	return res, loopErr
}

type dueOutcome int

const (
	dueClaimed dueOutcome = iota
	dueBlocked
	dueSkipped
)

// claimDueSchedule claims a due schedule: schedule -> running, order scheduled -> running
// (schedule_due), then shares the execution core with immediate click (re-running deterministic
// Preflight; authorizer = the frozen executor who created the schedule, preserving traceability).
// Datasource unavailable -> order blocked_datasource_unavailable + schedule blocked (no automatic
// retry); other Preflight failures -> schedule blocked (order stays scheduled so it can be withdrawn
// and recreated). Exception for PostgreSQL search_path invalidation: the path snapshot is no longer
// trustworthy, so both the order and schedule must converge to a terminal state together.
func (s *Services) claimDueSchedule(ctx context.Context, row dueScheduleRow) (dueOutcome, error) {
	outcome := dueSkipped
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		order, err := s.lockOrder(ctx, tx, row.OrderID)
		if err != nil {
			return err
		}
		// Schedule row lock + CAS (executes at most once across multiple instances).
		var sched persistence.ExecutionScheduleModel
		if err := tx.WithContext(ctx).Clauses(forUpdate()).
			Where("id = ?", row.ID).First(&sched).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound()
			}
			return err
		}
		if sched.State != "scheduled" {
			return domain.ErrConcurrent() // Already handled by another instance or path
		}
		switch orderdomain.OrderState(order.State) {
		case orderdomain.StateScheduled:
			stage, serr := s.loadOrderStage(ctx, tx, order.ID)
			if serr != nil {
				return serr
			}
			// Authorizer = the frozen executor who created the schedule (traceable; eligibility is still re-verified).
			// Ineligible: only the schedule is marked blocked (order stays scheduled so it can be withdrawn
			// and recreated — N5: do not borrow the datasource_unavailable edge and mislead about the order's
			// terminal cause); commit the transaction so it takes effect (M1: returning an error that rolls
			// back after writing would leave the schedule stuck in scheduled forever).
			if !s.isFrozenExecutor(ctx, tx, stage.StageID, row.ScheduledBy) {
				if uerr := tx.WithContext(ctx).Exec(`
					update execution_schedules set state = 'blocked', version = version + 1, updated_at = ?
					where id = ? and state = 'scheduled'`, nowUTC(), row.ID).Error; uerr != nil {
					return uerr
				}
				outcome = dueBlocked
				return nil
			}
			next, terr := orderdomain.OrderTransition(orderdomain.StateScheduled, orderdomain.EventScheduleDue)
			if terr != nil {
				return domain.ErrIllegal()
			}
			if _, aerr := s.beginCore(ctx, tx, order, stage, BeginInput{
				OrderID: order.ID,
				Actor:   Actor{UserID: row.ScheduledBy, Username: row.ScheduledByName},
			}, beginCoreOpts{nextOrderState: string(next), reason: "schedule_due"}); aerr != nil {
				if isPostgresSearchPathFailure(deName(aerr)) {
					if ferr := s.failDueScheduleAfterPostgresSearchPathPreflightTx(
						ctx, tx, row, deName(aerr),
					); ferr != nil {
						return ferr
					}
					outcome = dueBlocked
					return nil
				}
				return aerr
			}
			if uerr := tx.WithContext(ctx).Exec(`
				update execution_schedules set state = 'running', version = version + 1, updated_at = ?
				where id = ? and state = 'scheduled'`, nowUTC(), row.ID).Error; uerr != nil {
				return uerr
			}
			outcome = dueClaimed
			return nil
		case orderdomain.StateWithdrawn, orderdomain.StateVoided, orderdomain.StateMissedSchedule:
			// Order was already terminalized (withdraw / void / miss contention): finalize schedule as cancelled
			// (committed to take effect, L2).
			if uerr := tx.WithContext(ctx).Exec(`
				update execution_schedules set state = 'cancelled', version = version + 1, updated_at = ?
				where id = ? and state = 'scheduled'`, nowUTC(), row.ID).Error; uerr != nil {
				return uerr
			}
			return nil
		default:
			return domain.ErrConcurrent()
		}
	})
	if txErr == nil {
		return outcome, nil
	}
	var de *domain.Error
	if errors.As(txErr, &de) {
		switch de.Code {
		case domain.CodeDatasourceDown:
			// Datasource unavailable at due time: blocked_datasource_unavailable (machine edge with event);
			// never auto-retried (handled separately by a frozen executor after recovery).
			if berr := s.blockDueSchedule(ctx, row); berr != nil {
				return dueSkipped, berr
			}
			return dueBlocked, nil
		case domain.CodeConcurrent, domain.CodeNotFound:
			return dueSkipped, nil
		}
		// Other Preflight failures (schema drift / closed window / OSC unavailable, etc.):
		// stop retrying (PRD07 "stop on Preflight failure") and mark schedule blocked; the order
		// stays scheduled so the submitter can withdraw and re-run precheck.
		if berr := s.blockDueScheduleOnly(ctx, row); berr != nil {
			return dueSkipped, berr
		}
		return dueBlocked, nil
	}
	// Transient DB error: keep scheduled and retry on the next tick.
	if isSerializationFailure(txErr) {
		return dueSkipped, nil
	}
	return dueSkipped, txErr
}

// failDueScheduleAfterPostgresSearchPathPreflightTx records the rejection in
// the same serializable transaction that observed it. The claim may already
// have advanced the order in this transaction, while the schedule update is
// deliberately deferred until after beginCore succeeds.
func (s *Services) failDueScheduleAfterPostgresSearchPathPreflightTx(
	ctx context.Context, tx *gorm.DB, row dueScheduleRow, failureName string,
) error {
	reason := postgresSearchPathFailureReason(failureName)
	order, err := s.lockOrder(ctx, tx, row.OrderID)
	if err != nil {
		return err
	}
	var sched persistence.ExecutionScheduleModel
	if err := tx.WithContext(ctx).Clauses(forUpdate()).
		Where("id = ?", row.ID).First(&sched).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if (sched.State != "scheduled" && sched.State != "running") ||
		orderdomain.OrderState(order.State) != orderdomain.StateScheduled {
		return nil
	}
	running, err := orderdomain.OrderTransition(
		orderdomain.StateScheduled, orderdomain.EventScheduleDue,
	)
	if err != nil {
		return domain.ErrIllegal()
	}
	next, err := orderdomain.OrderTransition(
		running, orderdomain.EventDmlFailedRolledBack,
	)
	if err != nil {
		return domain.ErrIllegal()
	}
	stage, err := s.loadOrderStage(ctx, tx, order.ID)
	if err != nil {
		return err
	}
	live, sent, err := s.stageAttemptFacts(ctx, tx, stage.StageID)
	if err != nil {
		return err
	}
	if live > 0 || sent > 0 {
		// Another execution path owns the outcome. Its own pre-send
		// validation must fail closed before any change statement.
		return nil
	}
	if err := s.touchStageLocked(
		ctx, tx, stage.StageID, string(orderdomain.StageFailed),
	); err != nil {
		return err
	}
	now := nowUTC()
	nextVersion := order.Version + 1
	if err := s.updateOrderVersionedRaw(
		ctx, tx, order.ID, order.Version, map[string]any{
			"state": string(next), "terminal_at": now, "version": nextVersion,
		},
	); err != nil {
		return err
	}
	if err := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); err != nil {
		return err
	}
	res := tx.WithContext(ctx).Exec(`
		update execution_schedules
		set state = 'blocked', version = version + 1, updated_at = ?
		where id = ? and state in ('scheduled', 'running')`, now, row.ID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	return s.emitOrderEvent(
		ctx, tx, EventExecFailed, order.ID, auditlog.Actor{
			UserID: row.ScheduledBy, Username: row.ScheduledByName,
		}, StateChangedPayload{
			AggregateID: order.ID, From: strPtr(order.State),
			To: string(next), ReasonCode: reason,
			StageID: strPtr(stage.StageID), AggregateVersion: nextVersion,
		},
	)
}

// blockSchedule transitions order -> blocked_datasource_unavailable + schedule -> blocked.
// S007: emits execution.blocked_datasource_unavailable in the same transaction at the adjudication
// point (RCP-20261031; event actor = the frozen executor who created the schedule).
func (s *Services) blockSchedule(ctx context.Context, tx *gorm.DB, order *orderRow,
	sched *persistence.ExecutionScheduleModel, actor Actor) error {
	next, terr := orderdomain.OrderTransition(orderdomain.StateScheduled, orderdomain.EventDatasourceUnavailable)
	if terr != nil {
		return domain.ErrIllegal()
	}
	if uerr := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version,
		map[string]any{"state": string(next), "version": order.Version + 1}); uerr != nil {
		return uerr
	}
	if uerr := tx.WithContext(ctx).Exec(`
		update execution_schedules set state = 'blocked', version = version + 1, updated_at = ?
		where id = ? and state in ('scheduled', 'running')`, nowUTC(), sched.ID).Error; uerr != nil {
		return uerr
	}
	var st struct {
		StageID string `gorm:"column:stage_id"`
	}
	stageID := ""
	if order.CurrentStage != nil {
		if err := tx.WithContext(ctx).Raw(`
		select id::text as stage_id from order_stages
		where order_id = ? and position = ?`, order.ID, *order.CurrentStage).
			Scan(&st).Error; err != nil {
			return err
		}
		stageID = st.StageID
	}
	var stageRef *string
	if stageID != "" {
		stageRef = strPtr(stageID)
	}
	return s.emitOrderEvent(ctx, tx, EventBlockedDSUnavailable, order.ID, auditActor(actor), StateChangedPayload{
		AggregateID: order.ID, From: strPtr(string(orderdomain.StateScheduled)), To: string(next),
		ReasonCode: "datasource_unavailable", StageID: stageRef, AggregateVersion: order.Version + 1,
	})
}

// blockDueSchedule independent-transaction variant of blocked adjudication (compensating after the claim transaction rolled back).
func (s *Services) blockDueSchedule(ctx context.Context, row dueScheduleRow) error {
	return s.serializable(ctx, func(tx *gorm.DB) error {
		order, err := s.lockOrder(ctx, tx, row.OrderID)
		if err != nil {
			return err
		}
		if orderdomain.OrderState(order.State) != orderdomain.StateScheduled {
			return nil
		}
		var sched persistence.ExecutionScheduleModel
		if err := tx.WithContext(ctx).Clauses(forUpdate()).
			Where("id = ?", row.ID).First(&sched).Error; err != nil {
			return nil
		}
		if sched.State != "scheduled" && sched.State != "running" {
			return nil
		}
		return s.blockSchedule(ctx, tx, order, &sched, Actor{UserID: row.ScheduledBy, Username: row.ScheduledByName})
	})
}

// blockDueScheduleOnly transitions schedule -> blocked (order stays scheduled; withdraw and
// re-precheck path is handled by the submitter).
func (s *Services) blockDueScheduleOnly(ctx context.Context, row dueScheduleRow) error {
	return s.serializable(ctx, func(tx *gorm.DB) error {
		res := tx.WithContext(ctx).Exec(`
			update execution_schedules set state = 'blocked', version = version + 1, updated_at = ?
			where id = ? and state = 'scheduled'`, nowUTC(), row.ID)
		return res.Error
	})
}

// markScheduleMissed missed past grace window: schedule -> missed, order -> missed_schedule
// (terminal state; automatic catch-up execution after downtime across the planned time is forbidden).
func (s *Services) markScheduleMissed(ctx context.Context, row dueScheduleRow) (bool, error) {
	marked := false
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		// Lock order matches claimDueSchedule/cancelScheduleOnce: order row first, then schedule row
		// (Review P3: previously locked schedule before order, forming an AB-BA deadlock window with
		// the cancellation path that relied on serializable retry to heal — unified lock order eliminates the window).
		order, err := s.lockOrder(ctx, tx, row.OrderID)
		if err != nil {
			return err
		}
		var sched persistence.ExecutionScheduleModel
		if err := tx.WithContext(ctx).Clauses(forUpdate()).
			Where("id = ?", row.ID).First(&sched).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound()
			}
			return err
		}
		if sched.State != "scheduled" {
			return domain.ErrConcurrent()
		}
		if orderdomain.OrderState(order.State) != orderdomain.StateScheduled {
			// Order already left scheduled (withdrawn, etc.): finalize schedule as cancelled (committed to take effect).
			if uerr := tx.WithContext(ctx).Exec(`
				update execution_schedules set state = 'cancelled', version = version + 1, updated_at = ?
				where id = ? and state = 'scheduled'`, nowUTC(), row.ID).Error; uerr != nil {
				return uerr
			}
			return nil
		}
		next, terr := orderdomain.OrderTransition(orderdomain.StateScheduled, orderdomain.EventScheduleMissed)
		if terr != nil {
			return domain.ErrIllegal()
		}
		now := nowUTC()
		if uerr := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version,
			map[string]any{"state": string(next), "terminal_at": now, "version": order.Version + 1}); uerr != nil {
			return uerr
		}
		if rerr := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); rerr != nil {
			return rerr
		}
		if uerr := tx.WithContext(ctx).Exec(`
			update execution_schedules set state = 'missed', version = version + 1, updated_at = ?
			where id = ? and state = 'scheduled'`, now, row.ID).Error; uerr != nil {
			return uerr
		}
		if err := s.Recorder.Audit(ctx, tx, auditInputFor(
			Actor{UserID: row.ScheduledBy, Username: row.ScheduledByName},
			"execution.schedule_missed", "execution_schedules", row.ID, "miss", "failed")); err != nil {
			return err
		}
		// S007: emit execution.missed_schedule in the same transaction at the miss adjudication point
		// (RCP-20261031; actor = the frozen executor who created the schedule).
		stageRef := (*string)(nil)
		if order.CurrentStage != nil {
			var st struct {
				StageID string `gorm:"column:stage_id"`
			}
			if err := tx.WithContext(ctx).Raw(`
				select id::text as stage_id from order_stages
				where order_id = ? and position = ?`, order.ID, *order.CurrentStage).
				Scan(&st).Error; err != nil {
				return err
			}
			if st.StageID != "" {
				stageRef = strPtr(st.StageID)
			}
		}
		if eerr := s.emitOrderEvent(ctx, tx, EventScheduleMissed, order.ID,
			auditActor(Actor{UserID: row.ScheduledBy, Username: row.ScheduledByName}), StateChangedPayload{
				AggregateID: order.ID, From: strPtr(string(orderdomain.StateScheduled)), To: string(next),
				ReasonCode: "schedule_missed", StageID: stageRef, AggregateVersion: order.Version + 1,
			}); eerr != nil {
			return eerr
		}
		marked = true
		return nil
	})
	if txErr != nil {
		var de *domain.Error
		if errors.As(txErr, &de) && (de.Code == domain.CodeConcurrent || de.Code == domain.CodeNotFound) {
			return false, nil
		}
		if isSerializationFailure(txErr) {
			return false, nil
		}
		return false, txErr
	}
	return marked, nil
}
