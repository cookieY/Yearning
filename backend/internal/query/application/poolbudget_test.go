// poolbudget_test.go unit tests for the SEC-0027 global pool semaphore: exhaustion -> rejection,
// release -> recovery, and idempotent release. Because queryPoolSlots is package-level shared state,
// tests clean up after themselves (restoring all slots upon completion) independent of execution order.
package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
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

// drainMetadataPoolSlots drains all metadata pool slots to a clean baseline.
func drainMetadataPoolSlots() {
	for {
		select {
		case <-metadataPoolSlots:
		default:
			return
		}
	}
}

type fakePoolBudgetAdapter struct {
	database.Adapter
	cancels atomic.Int32
}

func (f *fakePoolBudgetAdapter) CancelQuery(_ context.Context, _ *database.PurposePool, _ *database.SelectCursor) error {
	f.cancels.Add(1)
	return nil
}

func (f *fakePoolBudgetAdapter) CloseCursor(_ context.Context, _ *database.PurposePool, _ *database.SelectCursor) error {
	return nil
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

func TestMetadataPoolSlotIsolationAndRecovery(t *testing.T) {
	drainQueryPoolSlots()
	drainMetadataPoolSlots()
	t.Cleanup(func() {
		drainQueryPoolSlots()
		drainMetadataPoolSlots()
	})

	// Exhaust all 32 query pool slots first.
	var queryReleases []func()
	for i := 0; i < maxConcurrentQueryPools; i++ {
		rel, err := acquireQueryPoolSlot()
		if err != nil {
			t.Fatalf("query slot %d should succeed: %v", i, err)
		}
		queryReleases = append(queryReleases, rel)
	}
	defer func() {
		for _, r := range queryReleases {
			r()
		}
	}()

	// Even with all 32 query slots held, all 8 metadata slots must be acquirable.
	svc := &Services{
		PoolFactory: func(_ context.Context, _ *datasource.DatasourceModel, _ int) (database.Adapter, *database.PurposePool, error) {
			return &fakePoolBudgetAdapter{}, &database.PurposePool{}, nil
		},
	}
	ds := &datasource.DatasourceModel{ID: "ds-meta"}
	var pools []*database.PurposePool
	for i := 0; i < maxConcurrentMetadataPools; i++ {
		_, pool, err := svc.openMetadataPool(context.Background(), ds, 5000)
		if err != nil {
			t.Fatalf("metadata pool %d should succeed despite query budget exhaustion: %v", i, err)
		}
		pools = append(pools, pool)
	}

	// 9th metadata acquisition must reject with errQueryPoolBusy.
	if _, _, err := svc.openMetadataPool(context.Background(), ds, 5000); !errors.Is(err, errQueryPoolBusy) {
		t.Fatalf("9th metadata pool should fail with errQueryPoolBusy, got %v", err)
	}

	// Closing one pool releases its metadata slot idempotently.
	_ = pools[0].Close()
	_ = pools[0].Close()
	_, extraPool, err := svc.openMetadataPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatalf("metadata slot should recover after pool Close: %v", err)
	}
	_ = extraPool.Close()
	for _, p := range pools[1:] {
		_ = p.Close()
	}
	if n := len(metadataPoolSlots); n != 0 {
		t.Fatalf("all metadata slots must be released, %d held", n)
	}
}

func TestOpenQueryPoolEvictsLRUIdleCursor(t *testing.T) {
	drainQueryPoolSlots()
	t.Cleanup(drainQueryPoolSlots)

	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	adapter := &fakePoolBudgetAdapter{}
	svc := &Services{
		cursorIdle:            10 * time.Minute,
		cursorEvictMinIdle:    30 * time.Second,
		cursorEvictMinIdleSet: true,
		nowFunc:               func() time.Time { return now },
		PoolFactory: func(_ context.Context, _ *datasource.DatasourceModel, _ int) (database.Adapter, *database.PurposePool, error) {
			return adapter, &database.PurposePool{}, nil
		},
	}
	ds := &datasource.DatasourceModel{ID: "ds-1"}

	// Fill 29 query slots directly and 3 via live cursors opened through openQueryPool.
	var directReleases []func()
	for i := 0; i < maxConcurrentQueryPools-3; i++ {
		rel, err := acquireQueryPoolSlot()
		if err != nil {
			t.Fatalf("direct slot %d: %v", i, err)
		}
		directReleases = append(directReleases, rel)
	}
	defer func() {
		for _, r := range directReleases {
			r()
		}
	}()

	openCursor := func(hash string, lastAcc time.Time) *liveCursor {
		adp, pool, err := svc.openQueryPool(context.Background(), ds, 5000)
		if err != nil {
			t.Fatalf("openQueryPool %s: %v", hash, err)
		}
		cur := &liveCursor{
			adapter:      adp,
			pool:         pool,
			tokenHash:    hash,
			expiresAt:    now.Add(10 * time.Minute),
			absolute:     now.Add(6 * time.Hour),
			lastAccessed: lastAcc,
		}
		svc.registryAdd(cur)
		return cur
	}

	_ = openCursor("tok-oldest", now.Add(-90*time.Second))
	newerIdle := openCursor("tok-newer-idle", now.Add(-45*time.Second))
	recent := openCursor("tok-recent", now.Add(-10*time.Second))
	defer func() {
		_ = newerIdle.pool.Close()
		_ = recent.pool.Close()
	}()

	// Budget is full; opening a new pool should evict only tok-oldest (>= 30s idle, oldest).
	_, newPool, err := svc.openQueryPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatalf("expected openQueryPool to succeed via LRU eviction, got %v", err)
	}
	defer func() { _ = newPool.Close() }()

	if svc.registryGet("tok-oldest") != nil {
		t.Fatal("expected oldest idle cursor tok-oldest to be evicted")
	}
	if svc.registryGet("tok-newer-idle") == nil {
		t.Fatal("expected tok-newer-idle not to be evicted when older candidate existed")
	}
	if svc.registryGet("tok-recent") == nil {
		t.Fatal("expected recent cursor tok-recent not to be evicted")
	}
	if adapter.cancels.Load() != 1 {
		t.Fatalf("expected exactly 1 CancelQuery on eviction, got %d", adapter.cancels.Load())
	}
}

func TestOpenQueryPoolSkipsLockedAndRecentCursors(t *testing.T) {
	drainQueryPoolSlots()
	t.Cleanup(drainQueryPoolSlots)

	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	adapter := &fakePoolBudgetAdapter{}
	svc := &Services{
		cursorIdle:            10 * time.Minute,
		cursorEvictMinIdle:    30 * time.Second,
		cursorEvictMinIdleSet: true,
		nowFunc:               func() time.Time { return now },
		PoolFactory: func(_ context.Context, _ *datasource.DatasourceModel, _ int) (database.Adapter, *database.PurposePool, error) {
			return adapter, &database.PurposePool{}, nil
		},
	}
	ds := &datasource.DatasourceModel{ID: "ds-1"}

	var directReleases []func()
	for i := 0; i < maxConcurrentQueryPools-2; i++ {
		rel, err := acquireQueryPoolSlot()
		if err != nil {
			t.Fatalf("direct slot %d: %v", i, err)
		}
		directReleases = append(directReleases, rel)
	}
	defer func() {
		for _, r := range directReleases {
			r()
		}
	}()

	_, poolLocked, err := svc.openQueryPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = poolLocked.Close() }()
	lockedCur := &liveCursor{
		adapter:      adapter,
		pool:         poolLocked,
		tokenHash:    "tok-locked",
		expiresAt:    now.Add(10 * time.Minute),
		absolute:     now.Add(6 * time.Hour),
		lastAccessed: now.Add(-2 * time.Minute),
	}
	svc.registryAdd(lockedCur)

	_, poolRecent, err := svc.openQueryPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = poolRecent.Close() }()
	recentCur := &liveCursor{
		adapter:      adapter,
		pool:         poolRecent,
		tokenHash:    "tok-recent",
		expiresAt:    now.Add(10 * time.Minute),
		absolute:     now.Add(6 * time.Hour),
		lastAccessed: now.Add(-5 * time.Second),
	}
	svc.registryAdd(recentCur)

	// Simulate an active in-flight FetchCursorPage holding lockedCur.mu.
	lockedCur.mu.Lock()
	_, _, err = svc.openQueryPool(context.Background(), ds, 5000)
	lockedCur.mu.Unlock()

	if !errors.Is(err, errQueryPoolBusy) {
		t.Fatalf("expected errQueryPoolBusy when only candidates are locked or recent, got %v", err)
	}
	if svc.registryGet("tok-locked") == nil || svc.registryGet("tok-recent") == nil {
		t.Fatal("locked and recent cursors must remain registered")
	}
	if adapter.cancels.Load() != 0 {
		t.Fatalf("expected 0 CancelQuery calls, got %d", adapter.cancels.Load())
	}
}

func TestOpenQueryPoolSweepsExpiredBeforeEvictingLRU(t *testing.T) {
	drainQueryPoolSlots()
	t.Cleanup(drainQueryPoolSlots)

	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	adapter := &fakePoolBudgetAdapter{}
	svc := &Services{
		cursorIdle:            10 * time.Minute,
		cursorEvictMinIdle:    30 * time.Second,
		cursorEvictMinIdleSet: true,
		nowFunc:               func() time.Time { return now },
		PoolFactory: func(_ context.Context, _ *datasource.DatasourceModel, _ int) (database.Adapter, *database.PurposePool, error) {
			return adapter, &database.PurposePool{}, nil
		},
	}
	ds := &datasource.DatasourceModel{ID: "ds-1"}

	var directReleases []func()
	for i := 0; i < maxConcurrentQueryPools-2; i++ {
		rel, err := acquireQueryPoolSlot()
		if err != nil {
			t.Fatalf("direct slot %d: %v", i, err)
		}
		directReleases = append(directReleases, rel)
	}
	defer func() {
		for _, r := range directReleases {
			r()
		}
	}()

	_, poolExp, err := svc.openQueryPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatal(err)
	}
	svc.registryAdd(&liveCursor{
		adapter:      adapter,
		pool:         poolExp,
		tokenHash:    "tok-expired",
		expiresAt:    now.Add(-1 * time.Second),
		absolute:     now.Add(6 * time.Hour),
		lastAccessed: now.Add(-1 * time.Second),
	})

	_, poolIdle, err := svc.openQueryPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = poolIdle.Close() }()
	svc.registryAdd(&liveCursor{
		adapter:      adapter,
		pool:         poolIdle,
		tokenHash:    "tok-unexpired-idle",
		expiresAt:    now.Add(10 * time.Minute),
		absolute:     now.Add(6 * time.Hour),
		lastAccessed: now.Add(-120 * time.Second),
	})

	_, newPool, err := svc.openQueryPool(context.Background(), ds, 5000)
	if err != nil {
		t.Fatalf("expected SweepCursors to reclaim expired slot: %v", err)
	}
	defer func() { _ = newPool.Close() }()

	if svc.registryGet("tok-expired") != nil {
		t.Fatal("expected expired cursor to be swept")
	}
	if svc.registryGet("tok-unexpired-idle") == nil {
		t.Fatal("expected unexpired idle cursor to survive when expired cursor freed a slot")
	}
}

func TestCursorRegistryTouchAndSetExpiresRaceSafe(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	svc := &Services{
		nowFunc: func() time.Time { return now },
	}
	svc.SetCursorEvictMinIdleForTest(30 * time.Second)
	cur := &liveCursor{
		tokenHash: "tok-race",
		expiresAt: now.Add(10 * time.Minute),
		absolute:  now.Add(6 * time.Hour),
	}
	svc.registryAdd(cur)
	if cur.lastAccessed != now {
		t.Fatalf("expected registryAdd to initialize lastAccessed=%v, got %v", now, cur.lastAccessed)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			tAt := now.Add(time.Duration(step) * time.Second)
			svc.registryTouch("tok-race", tAt)
			svc.registrySetExpires("tok-race", tAt.Add(10*time.Minute))
			_ = svc.evictLRUIdleCursor(context.Background())
		}(i)
	}
	wg.Wait()
}

func TestMapCursorFetchError_ExpiredBudgetTimeoutAndFallback(t *testing.T) {
	ctx := context.Background()
	timedOutCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()

	cases := []struct {
		name     string
		ctx      context.Context
		err      error
		wantCode int32
	}{
		{name: "direct_cursor_expired", ctx: ctx, err: database.ErrCursorExpired, wantCode: domain.CodeCursorExpired},
		{name: "wrapped_cursor_expired", ctx: ctx, err: fmt.Errorf("closed: %w", database.ErrCursorExpired), wantCode: domain.CodeCursorExpired},
		{name: "page_budget_exceeded", ctx: ctx, err: database.ErrPageBudgetExceeded, wantCode: domain.CodeSystemCapacity},
		{name: "ctx_deadline_exceeded", ctx: timedOutCtx, err: errors.New("canceled"), wantCode: domain.CodeQueryTimeout},
		{name: "driver_timeout_err", ctx: ctx, err: errors.New("statement timeout exceeded"), wantCode: domain.CodeQueryTimeout},
		{name: "generic_statement_error", ctx: ctx, err: errors.New("syntax error"), wantCode: 4011},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapCursorFetchError(tc.ctx, tc.err)
			var de *domain.Error
			if !errors.As(mapped, &de) || de.Code != tc.wantCode {
				t.Fatalf("mapCursorFetchError(%v) = %v, want code %d", tc.err, mapped, tc.wantCode)
			}
		})
	}
}

func TestRegistryTouchIfDetectsEvictedCursor(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	svc := &Services{nowFunc: func() time.Time { return base }}
	cur := &liveCursor{
		tokenHash: "tok-live",
		expiresAt: base.Add(10 * time.Minute),
		absolute:  base.Add(6 * time.Hour),
	}
	svc.registryAdd(cur)

	updated := base.Add(45 * time.Second)
	if !svc.registryTouchIf("tok-live", cur, updated) {
		t.Fatal("expected registryTouchIf to succeed for live cursor")
	}
	if cur.lastAccessed != updated {
		t.Fatalf("lastAccessed = %v, want %v", cur.lastAccessed, updated)
	}

	other := &liveCursor{tokenHash: "tok-live"}
	if svc.registryTouchIf("tok-live", other, updated.Add(time.Second)) {
		t.Fatal("expected registryTouchIf to reject mismatched liveCursor pointer")
	}

	svc.registryDrop("tok-live")
	if svc.registryTouchIf("tok-live", cur, updated.Add(2*time.Second)) {
		t.Fatal("expected registryTouchIf to return false after eviction")
	}
}
