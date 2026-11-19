// worker.go implements the in-process task executor: Claim -> Heartbeat -> Handler -> Complete/Fail.
// All Handler steps are idempotent (B6 FakeRunner has no external send boundary);
// lease-expired tasks after process interruption can be safely reclaimed (worker interruption recovers safe steps only).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Handler executes a claimed task once. It must return promptly when ctx is cancelled;
// completed local idempotent steps may be safely replayed. Returned errors map to task failure.
// fenceGuard is called before terminal business writes: returning ErrFenceLost means this worker
// has lost ownership of the task (zombie) and must abort business writes.
type Handler func(ctx context.Context, db *gorm.DB, task *ClaimedTask,
	fenceGuard func() error, beat func(completed, total int64)) error

// Worker is a single-process task executor.
type Worker struct {
	db           *gorm.DB
	kinds        []string
	owner        string
	lease        time.Duration
	pollInterval time.Duration
	handlers     map[string]Handler
}

// NewWorker constructs a Worker; owner must be unique within a deployment (instance ID).
func NewWorker(db *gorm.DB, owner string, kinds []string, lease, poll time.Duration) *Worker {
	return &Worker{
		db: db, kinds: kinds, owner: owner,
		lease: lease, pollInterval: poll,
		handlers: map[string]Handler{},
	}
}

// Register registers a handler for a task kind; duplicate registration panics (startup configuration error).
func (w *Worker) Register(kind string, h Handler) {
	if _, dup := w.handlers[kind]; dup {
		panic("tasks: duplicate handler for kind " + kind)
	}
	w.handlers[kind] = h
}

// RunOnce claims and executes at most one task; returns ErrNoTask when the queue is empty.
func (w *Worker) RunOnce(ctx context.Context) error {
	task, err := Claim(ctx, w.db, w.kinds, w.owner, w.lease)
	if err != nil {
		return err
	}
	handler, ok := w.handlers[task.Kind]
	if !ok {
		return FailWith(ctx, w.db, task.ID, task.FenceToken, task.LeaseOwner,
			"failed", 2012, "NO_HANDLER")
	}
	// Heartbeat loop: renew lease before expiration; stop as soon as Handler returns.
	hbCtx, stopHB := context.WithCancel(context.WithoutCancel(ctx))
	defer stopHB()
	beat := func(completed, total int64) {
		_ = Heartbeat(hbCtx, w.db, task.ID, task.FenceToken, task.LeaseOwner, w.lease, completed, total)
	}
	go func() {
		ticker := time.NewTicker(w.lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				_ = Extend(hbCtx, w.db, task.ID, task.FenceToken, task.LeaseOwner, w.lease)
			}
		}
	}()
	fenceGuard := func() error {
		return CheckFence(ctx, w.db, task.ID, task.FenceToken, task.LeaseOwner)
	}
	err = runGuarded(ctx, handler, w.db, task, fenceGuard, beat)
	if err == nil {
		stopHB()
		return Complete(ctx, w.db, task.ID, task.FenceToken, task.LeaseOwner, task.Kind+"/"+task.AggregateID)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		stopHB()
		// Client cancellation: keep the task in running state until lease expiration triggers recovery replay,
		// or until explicitly cancelled via Cancel; do not guess a terminal state here.
		return err
	}
	if errors.Is(err, ErrWorkerCrashed) {
		stopHB()
		// Crash semantics: do not write a terminal task state so the task can be safely reclaimed after lease expiration.
		return err
	}
	if isTransientDBError(err) {
		// Transient database errors (40001/40P01/connection errors): per repository-commands
		// defaults.retry semantics, do not write a terminal state -- let the lease expire for reclaim and replay.
		// Replay safety for stage_execution is guarded by send_boundary adjudication (adjudicateReclaim):
		// reclaims past not_started resolve to result_unknown; non-send tasks are safe to replay.
		stopHB()
		return err
	}
	stopHB()
	if markErr := FailBusiness(ctx, w.db, task.ID, task.FenceToken, task.LeaseOwner, err); markErr != nil {
		return markErr
	}
	// Return the original business error (so the caller observes the failure reason after terminal state is persisted).
	return err
}

// isTransientDBError reports whether err is a transient database error safe for replay.
func isTransientDBError(err error) bool {
	if errors.Is(err, ErrFenceLost) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "08000", "08003", "08006", "08001", "08004":
			return true
		}
	}
	return false
}

// ErrWorkerCrashed indicates the handler terminated with a panic (equivalent to process crash semantics).
var ErrWorkerCrashed = errors.New("tasks: worker crashed")

// runGuarded executes the handler, converting any panic into ErrWorkerCrashed without writing a terminal task state.
func runGuarded(ctx context.Context, handler Handler, db *gorm.DB, task *ClaimedTask,
	fenceGuard func() error, beat func(int64, int64)) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrWorkerCrashed, r)
		}
	}()
	return handler(ctx, db, task, fenceGuard, beat)
}

// Run polls continuously until ctx is cancelled, backing off and retrying on poll errors (database unavailable).
func (w *Worker) Run(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := w.RunOnce(ctx)
		switch {
		case errors.Is(err, ErrNoTask):
			backoff = time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.pollInterval):
			}
		case err != nil:
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		default:
			backoff = time.Second
		}
	}
}
