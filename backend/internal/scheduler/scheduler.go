// Package scheduler implements the persistent scheduler (E007; ADR-0001: all scheduling state lives in
// PostgreSQL, multi-replica execution is deduplicated via row locks/CAS, with no in-memory source of truth).
// Tick body: due schedule adjudication (claim-once / no catch-up on missed window / datasource block) +
// query domain lazy lifecycle advancement (Grant expiration, cursor cleanup) + execution domain periodic reconciliation
// (SEC-0022: cancel-timeout sweep and global expired Attempt adjudication, idempotent and batch-bounded).
package scheduler

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
)

// Scheduler is the periodic scheduler (one per process; recovers from persistent state on service restart).
type Scheduler struct {
	db       *gorm.DB
	exec     *execapp.Services
	query    *queryapp.Services
	interval time.Duration
	// onError logs periodic reconciliation errors (defaults to stdlib log; injectable for tests).
	onError func(task string, err error)
}

// New constructs a Scheduler; interval sets the tick period (5s in production; injectable for tests).
func New(db *gorm.DB, exec *execapp.Services, query *queryapp.Services, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Scheduler{
		db: db, exec: exec, query: query, interval: interval,
		onError: func(task string, err error) { log.Printf("scheduler: %s: %v", task, err) },
	}
}

// SetErrorHandler injects an error reporting function (for test observation).
func (s *Scheduler) SetErrorHandler(fn func(task string, err error)) { s.onError = fn }

// Tick executes a single scheduling pass (shared by tests and the main loop). Errors from due schedules or
// Grant advancement are returned to the caller; reconciliation errors are logged without aborting (idempotent, retried next tick).
func (s *Scheduler) Tick(ctx context.Context) error {
	if s.exec != nil {
		if _, err := s.exec.ProcessDueSchedules(ctx); err != nil {
			return err
		}
		// SEC-0022: periodic reconciliation -- sweep timed-out cancelling Attempts and adjudicate
		// globally expired active Attempts (previously neither had a periodic production caller, leaving
		// tickets unconverged after Pod restarts or worker crashes).
		if err := s.exec.SweepCancelTimeouts(ctx); err != nil {
			s.reportError("sweep cancel timeouts", err)
		}
		if err := s.exec.ReconcileExpiredAttempts(ctx); err != nil {
			s.reportError("reconcile expired attempts", err)
		}
	}
	if s.query != nil {
		if err := s.query.ExpireGrants(ctx); err != nil {
			return err
		}
		s.query.SweepCursors(ctx)
	}
	return nil
}

// reportError records periodic reconciliation errors via the injected handler.
func (s *Scheduler) reportError(task string, err error) {
	if s.onError != nil {
		s.onError(task, err)
	}
}

// Run polls continuously until ctx is cancelled (backing off and retrying when the database is unavailable).
func (s *Scheduler) Run(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := s.Tick(ctx)
		switch {
		case err == nil:
			backoff = time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.interval):
			}
		default:
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}
