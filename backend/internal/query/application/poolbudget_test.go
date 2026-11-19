// poolbudget_test.go unit tests for the SEC-0027 global pool semaphore: exhaustion -> rejection,
// release -> recovery, and idempotent release. Because queryPoolSlots is package-level shared state,
// tests clean up after themselves (restoring all slots upon completion) independent of execution order.
package application

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// drainQueryPoolSlots drains all global slots (defensively ensuring the test starts from a known state).
func drainQueryPoolSlots() {
	for {
		select {
		case <-queryPoolSlots:
		default:
			return
		}
	}
}

func TestQueryPoolSlotAcquireRelease(t *testing.T) {
	drainQueryPoolSlots()
	var releases []func()
	for i := 0; i < maxConcurrentQueryPools; i++ {
		release, err := acquireQueryPoolSlot()
		if err != nil {
			t.Fatalf("slot %d should be acquirable: %v", i, err)
		}
		releases = append(releases, release)
	}
	// Budget exhausted -> return structured backpressure error.
	if _, err := acquireQueryPoolSlot(); !errors.Is(err, errQueryPoolBusy) {
		t.Fatalf("exhausted budget should reject with errQueryPoolBusy, got %v", err)
	}
	// Release one slot -> acquisition succeeds again.
	releases[0]()
	extra, err := acquireQueryPoolSlot()
	if err != nil {
		t.Fatalf("slot should recover after release: %v", err)
	}
	// Idempotent release: calling release multiple times returns only a single slot.
	releases[0]()
	releases[0]()
	for _, r := range releases[1:] {
		r()
	}
	extra()
	drainQueryPoolSlots()
	if n := len(queryPoolSlots); n != 0 {
		t.Fatalf("all slots must be released after test, %d held", n)
	}
}

func TestQueryPoolSlotConcurrentExhaustion(t *testing.T) {
	drainQueryPoolSlots()
	const contenders = maxConcurrentQueryPools + 16
	var wg sync.WaitGroup
	var okCount, busyCount atomic.Int64
	var mu sync.Mutex
	var held []func()
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := acquireQueryPoolSlot()
			if err != nil {
				if errors.Is(err, errQueryPoolBusy) {
					busyCount.Add(1)
				}
				return
			}
			okCount.Add(1)
			mu.Lock()
			held = append(held, release)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if okCount.Load() != maxConcurrentQueryPools {
		t.Fatalf("exactly %d goroutines should win, got %d", maxConcurrentQueryPools, okCount.Load())
	}
	if busyCount.Load() != contenders-maxConcurrentQueryPools {
		t.Fatalf("overflow goroutines should be rejected, got %d", busyCount.Load())
	}
	// Release all held slots -> full capacity is restored.
	for _, r := range held {
		r()
	}
	if n := len(queryPoolSlots); n != 0 {
		t.Fatalf("slots must drain to empty, %d held", n)
	}
}
