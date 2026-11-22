// worker.go implements the in-process task executor: Claim -> Heartbeat -> Handler -> Complete/Fail.
// All Handler steps are idempotent (B6 FakeRunner has no external send boundary);
// lease-expired tasks after process interruption can be safely reclaimed (worker interruption recovers safe steps only).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

const defaultMaxIdlePoll = 15 * time.Second

var wakeRegistry = struct {
	mu        sync.RWMutex
	listeners map[string]map[*Worker]struct{}
}{
	listeners: map[string]map[*Worker]struct{}{},
}

// WakeKind sends a non-blocking wakeup signal to every active Worker registered for kind.
func WakeKind(kind string) {
	wakeRegistry.mu.RLock()
	set := wakeRegistry.listeners[kind]
	var targets []*Worker
	if len(set) > 0 {
		targets = make([]*Worker, 0, len(set))
		for w := range set {
			targets = append(targets, w)
		}
	}
	wakeRegistry.mu.RUnlock()
	for _, w := range targets {
		w.Wake()
	}
}

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
	maxIdlePoll  time.Duration
	wakeCh       chan struct{}
	handlers     map[string]Handler
}

// NewWorker constructs a Worker; owner must be unique within a deployment (instance ID).
func NewWorker(db *gorm.DB, owner string, kinds []string, lease, poll time.Duration) *Worker {
	maxIdle := defaultMaxIdlePoll
	if maxIdle < poll {
		maxIdle = poll
	}
	return &Worker{
		db: db, kinds: kinds, owner: owner,
		lease: lease, pollInterval: poll, maxIdlePoll: maxIdle,
		wakeCh:   make(chan struct{}, 1),
		handlers: map[string]Handler{},
	}
}

// WithMaxIdlePoll sets the upper bound for idle exponential backoff (clamped >= pollInterval).
func (w *Worker) WithMaxIdlePoll(d time.Duration) *Worker {
	if d < w.pollInterval {
		d = w.pollInterval
	}
	w.maxIdlePoll = d
	return w
}

// Wake sends a non-blocking wakeup signal so an idle Worker polls immediately without waiting for its backoff timer.
func (w *Worker) Wake() {
	if w == nil || w.wakeCh == nil {
		return
	}
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

// Close removes this Worker from the WakeKind registry. Run calls Close automatically on exit.
func (w *Worker) Close() {
	if w == nil {
		return
	}
	wakeRegistry.mu.Lock()
	defer wakeRegistry.mu.Unlock()
	for kind := range w.handlers {
		if set := wakeRegistry.listeners[kind]; set != nil {
			delete(set, w)
			if len(set) == 0 {
				delete(wakeRegistry.listeners, kind)
			}
		}
	}
}

// Register registers a handler for a task kind; duplicate registration panics (startup configuration error).
func (w *Worker) Register(kind string, h Handler) {
	if _, dup := w.handlers[kind]; dup {
		panic("tasks: duplicate handler for kind " + kind)
	}
	w.handlers[kind] = h
	wakeRegistry.mu.Lock()
	set := wakeRegistry.listeners[kind]
	if set == nil {
		set = map[*Worker]struct{}{}
		wakeRegistry.listeners[kind] = set
	}
	set[w] = struct{}{}
	wakeRegistry.mu.Unlock()
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

// nextIdleInterval doubles cur up to max without overflowing.
func nextIdleInterval(cur, max time.Duration) time.Duration {
	if cur <= 0 {
		return max
	}
	next := cur * 2
	if next <= 0 || next > max {
		return max
	}
	return next
}

// Run polls continuously until ctx is cancelled, applying exponential idle backoff up to maxIdlePoll
// with instant Wake/WakeKind wakeups and retrying with backoff on poll errors (database unavailable).
func (w *Worker) Run(ctx context.Context) {
	w.runLoop(ctx, w.RunOnce)
}

func (w *Worker) runLoop(ctx context.Context, step func(context.Context) error) {
	defer w.Close()
	errBackoff := time.Second
	basePoll := w.pollInterval
	if basePoll <= 0 {
		basePoll = time.Millisecond
	}
	maxIdle := w.maxIdlePoll
	if maxIdle < basePoll {
		maxIdle = basePoll
	}
	idleWait := basePoll
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := step(ctx)
		switch {
		case errors.Is(err, ErrNoTask):
			errBackoff = time.Second
			timer := time.NewTimer(idleWait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-w.wakeCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				idleWait = basePoll
			case <-timer.C:
				idleWait = nextIdleInterval(idleWait, maxIdle)
			}
		case err != nil:
			timer := time.NewTimer(errBackoff)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-w.wakeCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				idleWait = basePoll
			case <-timer.C:
			}
			if errBackoff < 30*time.Second {
				errBackoff *= 2
				if errBackoff > 30*time.Second {
					errBackoff = 30 * time.Second
				}
			}
		default:
			errBackoff = time.Second
			idleWait = basePoll
		}
	}
}
