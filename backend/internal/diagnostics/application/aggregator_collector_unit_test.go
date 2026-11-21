package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
)

func i64Ptr(v int64) *int64     { return &v }
func f64Ptr(v float64) *float64 { return &v }

func TestMergeSnapshotStatRows_MergesDuplicatesAndOptionalMetrics(t *testing.T) {
	in := []database.NativeStatementStatRow{
		{
			EngineFingerprint: "",
			CallsTotal:        99,
		},
		{
			EngineFingerprint:                 "fp-1",
			DatabaseOrSchema:                  "",
			CallsTotal:                        10,
			TotalExecTimeMS:                   25.5,
			RowsExaminedOrSharedBlksReadTotal: 100,
			RowsSentOrRowsReturnedTotal:       20,
			LockTimeMSOrTempBlksWrittenTotal:  1.5,
			PlansTotal:                        i64Ptr(4),
			TotalPlanTimeMS:                   f64Ptr(2.25),
			TempBlksReadTotal:                 nil,
			SharedBlksHitTotal:                i64Ptr(50),
		},
		{
			EngineFingerprint:                 "fp-1",
			DatabaseOrSchema:                  "public",
			CallsTotal:                        5,
			TotalExecTimeMS:                   14.5,
			RowsExaminedOrSharedBlksReadTotal: 40,
			RowsSentOrRowsReturnedTotal:       8,
			LockTimeMSOrTempBlksWrittenTotal:  0.5,
			PlansTotal:                        i64Ptr(3),
			TotalPlanTimeMS:                   f64Ptr(1.75),
			TempBlksReadTotal:                 i64Ptr(9),
			SharedBlksHitTotal:                nil,
		},
		{
			EngineFingerprint: "fp-2",
			DatabaseOrSchema:  "app",
			CallsTotal:        2,
		},
	}

	got := mergeSnapshotStatRows(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 merged rows (ignoring empty fp), got %d", len(got))
	}
	m := got[0]
	if m.EngineFingerprint != "fp-1" || m.DatabaseOrSchema != "public" ||
		m.CallsTotal != 15 || m.TotalExecTimeMS != 40.0 ||
		m.RowsExaminedOrSharedBlksReadTotal != 140 || m.RowsSentOrRowsReturnedTotal != 28 ||
		m.LockTimeMSOrTempBlksWrittenTotal != 2.0 {
		t.Fatalf("unexpected merged primary totals: %+v", m)
	}
	if m.PlansTotal == nil || *m.PlansTotal != 7 ||
		m.TotalPlanTimeMS == nil || *m.TotalPlanTimeMS != 4.0 ||
		m.TempBlksReadTotal == nil || *m.TempBlksReadTotal != 9 ||
		m.SharedBlksHitTotal == nil || *m.SharedBlksHitTotal != 50 {
		t.Fatalf("unexpected merged optional totals: plans=%v planMS=%v tempRead=%v sharedHit=%v",
			m.PlansTotal, m.TotalPlanTimeMS, m.TempBlksReadTotal, m.SharedBlksHitTotal)
	}
}

func TestComputeBucketDelta_NormalGapAndReset(t *testing.T) {
	prev := persistence.SQLDiagnosticsStatementModel{
		LatestCallsTotal:                        100,
		LatestTotalExecTimeMS:                   500,
		LatestRowsExaminedOrSharedBlksReadTotal: 1000,
		LatestRowsSentOrRowsReturnedTotal:       200,
		LatestLockTimeMSOrTempBlksWrittenTotal:  10,
		LatestPlansTotal:                        i64Ptr(50),
		LatestTotalPlanTimeMS:                   f64Ptr(25),
	}

	// Normal monotonic window delta.
	d1 := computeBucketDelta(database.NativeStatementStatRow{
		CallsTotal:                        120,
		TotalExecTimeMS:                   700,
		RowsExaminedOrSharedBlksReadTotal: 1300,
		RowsSentOrRowsReturnedTotal:       250,
		LockTimeMSOrTempBlksWrittenTotal:  14,
		PlansTotal:                        i64Ptr(55),
		TotalPlanTimeMS:                   f64Ptr(30),
	}, prev, false)
	if d1.reset || d1.coverageMode != "window_delta" || d1.calls != 20 || d1.execMS != 200 || d1.avgMS != 10 ||
		d1.plans == nil || *d1.plans != 5 || d1.planMS == nil || *d1.planMS != 5 {
		t.Fatalf("unexpected monotonic delta: %+v", d1)
	}

	// Gap detected marks coverageMode = reset_or_gap without setting reset=true.
	dGap := computeBucketDelta(database.NativeStatementStatRow{CallsTotal: 110, TotalExecTimeMS: 550}, prev, true)
	if dGap.reset || dGap.coverageMode != "reset_or_gap" || dGap.calls != 10 {
		t.Fatalf("unexpected gap delta: %+v", dGap)
	}

	// Counter reset clamps deltas non-negative from current cumulative counters and clears optional deltas.
	dReset := computeBucketDelta(database.NativeStatementStatRow{
		CallsTotal:      8,
		TotalExecTimeMS: 32,
	}, prev, false)
	if !dReset.reset || dReset.coverageMode != "reset_or_gap" || dReset.calls != 8 || dReset.execMS != 32 || dReset.avgMS != 4 || dReset.plans != nil {
		t.Fatalf("unexpected reset delta: %+v", dReset)
	}
}

type concurrentSpyCollector struct {
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	totalCalls  atomic.Int32
	readFn      func(ctx context.Context, fp string) (string, error)
}

func (c *concurrentSpyCollector) Probe(context.Context, DatasourceRow) (*database.StatsProbeResult, error) {
	return &database.StatsProbeResult{}, nil
}

func (c *concurrentSpyCollector) ReadSnapshot(context.Context, DatasourceRow, int) ([]database.NativeStatementStatRow, error) {
	return nil, nil
}

func (c *concurrentSpyCollector) ReadTemplate(ctx context.Context, _ DatasourceRow, fp, _ string) (string, error) {
	cur := c.inFlight.Add(1)
	for {
		prevMax := c.maxInFlight.Load()
		if cur <= prevMax || c.maxInFlight.CompareAndSwap(prevMax, cur) {
			break
		}
	}
	c.totalCalls.Add(1)
	defer c.inFlight.Add(-1)
	if c.readFn != nil {
		return c.readFn(ctx, fp)
	}
	time.Sleep(15 * time.Millisecond)
	return "SELECT * FROM items WHERE fp = '" + fp + "'", nil
}

func (c *concurrentSpyCollector) ListColumns(context.Context, DatasourceRow, string, string) ([]database.MetadataColumn, error) {
	return nil, nil
}

func (c *concurrentSpyCollector) ListIndexes(context.Context, DatasourceRow, string, string) ([]database.MetadataIndex, error) {
	return nil, nil
}

func TestBackfillOverviewTemplatePreviews_BoundedConcurrency(t *testing.T) {
	spy := &concurrentSpyCollector{}
	svc := &Services{Collector: spy}
	ds := &DatasourceRow{ID: "ds-1", CompatibilityMode: "postgresql"}

	rows := make([]domain.DiagnosticsStatementRowView, 30)
	for i := range rows {
		rows[i] = domain.DiagnosticsStatementRowView{
			StatementID:       fmt.Sprintf("stmt-%02d", i),
			EngineFingerprint: fmt.Sprintf("fp-%02d", i),
			DatabaseOrSchema:  "public",
			TemplateStatus:    "not_fetched",
		}
	}
	cached := "CACHED_SQL"
	rows[1].TemplatePreview = &cached
	rows[1].TemplateStatus = "available"
	rows[2].TemplateStatus = "unavailable"

	svc.backfillOverviewTemplatePreviews(context.Background(), ds, rows)

	if spy.totalCalls.Load() != 23 {
		t.Fatalf("expected 23 ReadTemplate calls (cap 25 minus 1 cached minus 1 unavailable), got %d", spy.totalCalls.Load())
	}
	if maxSeen := spy.maxInFlight.Load(); maxSeen != 2 {
		t.Fatalf("expected bounded concurrency == 2 (<= overviewPreviewConcurrency=%d), observed max %d", overviewPreviewConcurrency, maxSeen)
	}
	if rows[1].TemplatePreview == nil || *rows[1].TemplatePreview != cached {
		t.Fatalf("cached preview must remain untouched, got %v", rows[1].TemplatePreview)
	}
	if rows[2].TemplatePreview != nil {
		t.Fatalf("unavailable preview must stay nil, got %v", rows[2].TemplatePreview)
	}
	for i := 0; i < 25; i++ {
		if i == 1 || i == 2 {
			continue
		}
		wantSub := fmt.Sprintf("fp-%02d", i)
		if rows[i].TemplatePreview == nil || *rows[i].TemplatePreview == "" || !strings.Contains(*rows[i].TemplatePreview, wantSub) {
			t.Fatalf("row %d missing backfilled preview for %s: %v", i, wantSub, rows[i].TemplatePreview)
		}
	}
	for i := 25; i < len(rows); i++ {
		if rows[i].TemplatePreview != nil {
			t.Fatalf("row %d beyond preview cap 25 must stay nil", i)
		}
	}
}

func TestBackfillOverviewTemplatePreviews_ErrorHandling(t *testing.T) {
	ds := &DatasourceRow{ID: "ds-err", CompatibilityMode: "postgresql"}
	makeRows := func(n int) []domain.DiagnosticsStatementRowView {
		rows := make([]domain.DiagnosticsStatementRowView, n)
		for i := range rows {
			rows[i] = domain.DiagnosticsStatementRowView{
				StatementID:       fmt.Sprintf("stmt-%02d", i),
				EngineFingerprint: fmt.Sprintf("fp-%02d", i),
				DatabaseOrSchema:  "public",
				TemplateStatus:    "not_fetched",
			}
		}
		return rows
	}

	t.Run("continues on per-statement unavailable error", func(t *testing.T) {
		spy := &concurrentSpyCollector{
			readFn: func(_ context.Context, fp string) (string, error) {
				if fp == "fp-03" {
					return "", database.ErrTemplateUnavailable
				}
				return "SELECT * FROM items WHERE fp = '" + fp + "'", nil
			},
		}
		svc := &Services{Collector: spy}
		rows := makeRows(10)
		svc.backfillOverviewTemplatePreviews(context.Background(), ds, rows)

		if spy.totalCalls.Load() != 10 {
			t.Fatalf("expected all 10 statements attempted on single ErrTemplateUnavailable, got %d", spy.totalCalls.Load())
		}
		if rows[3].TemplatePreview != nil {
			t.Fatalf("expected fp-03 preview nil on ErrTemplateUnavailable, got %v", rows[3].TemplatePreview)
		}
		for i := range rows {
			if i == 3 {
				continue
			}
			if rows[i].TemplatePreview == nil || !strings.Contains(*rows[i].TemplatePreview, rows[i].EngineFingerprint) {
				t.Fatalf("expected row %d to be backfilled despite fp-03 unavailable", i)
			}
		}
	})

	t.Run("aborts early on target-wide connection error", func(t *testing.T) {
		spy := &concurrentSpyCollector{
			readFn: func(context.Context, string) (string, error) {
				time.Sleep(5 * time.Millisecond)
				return "", errors.New("dial tcp: connection refused")
			},
		}
		svc := &Services{Collector: spy}
		rows := makeRows(25)
		svc.backfillOverviewTemplatePreviews(context.Background(), ds, rows)

		if got := spy.totalCalls.Load(); got > overviewPreviewConcurrency {
			t.Fatalf("expected early abort with <= %d calls, got %d", overviewPreviewConcurrency, got)
		}
		for i := range rows {
			if rows[i].TemplatePreview != nil {
				t.Fatalf("expected no previews backfilled on target error, row %d=%v", i, *rows[i].TemplatePreview)
			}
		}
	})
}

func TestAcquireReadPool_SingleflightCoalescesAndIsolatesCancel(t *testing.T) {
	var openCalls atomic.Int32
	started := make(chan struct{})
	releaseDial := make(chan struct{})
	var startOnce sync.Once

	backend := NewLiveStatsCollectorBackend(nil, nil)
	backend.openPoolFn = func(ctx context.Context, _ DatasourceRow) (*sharedReadPoolEntry, error) {
		openCalls.Add(1)
		startOnce.Do(func() { close(started) })
		select {
		case <-releaseDial:
			return &sharedReadPoolEntry{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	ds := DatasourceRow{ID: "ds-sf", CompatibilityMode: "postgresql"}
	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup

	cancelCtx, cancelFirst := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, errs[0] = backend.acquireReadPool(cancelCtx, ds)
	}()

	<-started

	for i := 1; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, errs[idx] = backend.acquireReadPool(context.Background(), ds)
		}(i)
	}

	cancelFirst()
	time.Sleep(15 * time.Millisecond)
	close(releaseDial)
	wg.Wait()

	if !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("expected canceled caller 0 to return context.Canceled, got %v", errs[0])
	}
	for i := 1; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d failed when sibling canceled: %v", i, errs[i])
		}
	}
	if got := openCalls.Load(); got != 1 {
		t.Fatalf("expected singleflight to coalesce 8 callers into 1 dial, got %d", got)
	}

	// Warm cache hit must not trigger another dial.
	if _, _, err := backend.acquireReadPool(context.Background(), ds); err != nil {
		t.Fatalf("warm acquireReadPool failed: %v", err)
	}
	if got := openCalls.Load(); got != 1 {
		t.Fatalf("expected 0 extra dials after warm cache hit, got total %d", got)
	}

	// Close marks backend closed and subsequent acquire fails deterministically.
	backend.Close()
	if _, _, err := backend.acquireReadPool(context.Background(), ds); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after Close(), got %v", err)
	}
}
