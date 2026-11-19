// scheduler_test.go unit tests for Scheduler loop semantics: exit on ctx cancellation, idle tick,
// and non-fatal error backoff.
package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestSchedulerTickNoDeps(t *testing.T) {
	s := New(nil, nil, nil, 0)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("empty tick: %v", err)
	}
}

func TestSchedulerRunExitsOnCancel(t *testing.T) {
	s := New(nil, nil, nil, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler Run did not exit on cancel")
	}
}

func TestSchedulerIntervalDefault(t *testing.T) {
	if New(nil, nil, nil, -1).interval != 5*time.Second {
		t.Fatal("default interval must be 5s")
	}
}

// TestSchedulerDefaultErrorHandler SEC-0022: default error handler is non-nil
// (periodic reconciliation errors must be logged rather than swallowed); SetErrorHandler allows test injection.
func TestSchedulerDefaultErrorHandler(t *testing.T) {
	s := New(nil, nil, nil, 0)
	if s.onError == nil {
		t.Fatal("default error handler must not be nil")
	}
	seen := make([]string, 0, 2)
	s.SetErrorHandler(func(task string, err error) { seen = append(seen, task) })
	s.reportError("task-a", nil)
	s.reportError("task-b", nil)
	if len(seen) != 2 || seen[0] != "task-a" || seen[1] != "task-b" {
		t.Fatalf("injected handler observations = %v", seen)
	}
}
