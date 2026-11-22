package tasks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNextIdleIntervalAndWithMaxIdlePoll(t *testing.T) {
	if got := nextIdleInterval(0, 15*time.Second); got != 15*time.Second {
		t.Fatalf("nextIdleInterval(0) = %v, want 15s", got)
	}
	if got := nextIdleInterval(time.Second, 15*time.Second); got != 2*time.Second {
		t.Fatalf("nextIdleInterval(1s) = %v, want 2s", got)
	}
	if got := nextIdleInterval(8*time.Second, 15*time.Second); got != 15*time.Second {
		t.Fatalf("nextIdleInterval(8s, 15s) = %v, want 15s", got)
	}

	w := NewWorker(nil, "owner-1", []string{"k1"}, 30*time.Second, 500*time.Millisecond)
	if w.maxIdlePoll != 15*time.Second {
		t.Fatalf("default maxIdlePoll = %v, want 15s", w.maxIdlePoll)
	}
	w.WithMaxIdlePoll(100 * time.Millisecond)
	if w.maxIdlePoll != 500*time.Millisecond {
		t.Fatalf("clamped maxIdlePoll = %v, want 500ms", w.maxIdlePoll)
	}
	w.WithMaxIdlePoll(2 * time.Second)
	if w.maxIdlePoll != 2*time.Second {
		t.Fatalf("updated maxIdlePoll = %v, want 2s", w.maxIdlePoll)
	}
}

func TestWorkerIdleBackoffAndResetOnTask(t *testing.T) {
	w := NewWorker(nil, "owner-backoff", []string{"k_backoff"}, 30*time.Second, 10*time.Millisecond).
		WithMaxIdlePoll(80 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var timestamps []time.Time
	var callNum atomic.Int64

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runLoop(ctx, func(_ context.Context) error {
			n := callNum.Add(1)
			mu.Lock()
			timestamps = append(timestamps, time.Now())
			mu.Unlock()
			// Calls 1..4: idle (backoff 10ms, 20ms, 40ms, 80ms).
			// Call 5: task found -> resets backoff back to 10ms!
			// Call 6: idle (waits 10ms before call 7).
			// Call 7: cancel context.
			switch n {
			case 5:
				return nil
			case 7:
				cancel()
				return ErrNoTask
			default:
				return ErrNoTask
			}
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker runLoop did not finish in time")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(timestamps) < 7 {
		t.Fatalf("expected at least 7 poll calls, got %d", len(timestamps))
	}
	// Verify backoff expanded by call 4 -> call 5 (after 10ms + 20ms + 40ms + 80ms sleeps)
	// and call 6 -> call 7 reset back down near base pollInterval (<= 50ms, well below 80ms cap).
	resetGap := timestamps[6].Sub(timestamps[5])
	grownGap := timestamps[4].Sub(timestamps[3])
	if grownGap < 60*time.Millisecond {
		t.Fatalf("expected grown idle interval >= 60ms before task, got %v", grownGap)
	}
	if resetGap > 50*time.Millisecond {
		t.Fatalf("expected idle interval to reset after task completion (<= 50ms), got %v", resetGap)
	}
}

func TestWorkerWakeAndWakeKindInstant(t *testing.T) {
	w := NewWorker(nil, "owner-wake", []string{"k_wake_test"}, 30*time.Second, 5*time.Second).
		WithMaxIdlePoll(15 * time.Second)
	w.Register("k_wake_test", func(_ context.Context, _ *gorm.DB, _ *ClaimedTask, _ func() error, _ func(int64, int64)) error {
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstPoll := make(chan struct{})
	secondPoll := make(chan struct{})
	var calls atomic.Int64

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runLoop(ctx, func(_ context.Context) error {
			n := calls.Add(1)
			switch n {
			case 1:
				close(firstPoll)
				return ErrNoTask
			case 2:
				close(secondPoll)
				return nil
			default:
				cancel()
				return ErrNoTask
			}
		})
	}()

	select {
	case <-firstPoll:
	case <-time.After(time.Second):
		t.Fatal("first poll did not happen")
	}
	// Allow goroutine to enter select sleep on 5s idleWait.
	time.Sleep(15 * time.Millisecond)

	startWake := time.Now()
	WakeKind("k_wake_test")

	select {
	case <-secondPoll:
		if elapsed := time.Since(startWake); elapsed > 200*time.Millisecond {
			t.Fatalf("WakeKind took too long to wake worker: %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("WakeKind failed to wake sleeping worker promptly")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after cancel")
	}

	// Verify Close/Run exit removed the worker from wakeRegistry without leaking.
	wakeRegistry.mu.RLock()
	_, exists := wakeRegistry.listeners["k_wake_test"]
	wakeRegistry.mu.RUnlock()
	if exists {
		t.Fatal("expected wakeRegistry listener for k_wake_test to be cleaned up on Run exit")
	}
}

func TestWorkerDirectWakeAndTransientErrCancel(t *testing.T) {
	w := NewWorker(nil, "owner-direct", []string{"k_direct"}, 30*time.Second, 5*time.Second)
	w.Register("k_direct", func(_ context.Context, _ *gorm.DB, _ *ClaimedTask, _ func() error, _ func(int64, int64)) error {
		return nil
	})
	// Calling Wake before Run buffers 1 signal without blocking; calling twice must not block.
	w.Wake()
	w.Wake()

	ctx, cancel := context.WithCancel(context.Background())
	errPoll := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runLoop(ctx, func(_ context.Context) error {
			once.Do(func() { close(errPoll) })
			return errors.New("transient db error")
		})
	}()

	select {
	case <-errPoll:
	case <-time.After(time.Second):
		t.Fatal("expected poll error attempt")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("worker did not exit promptly on cancel during error backoff")
	}
}
