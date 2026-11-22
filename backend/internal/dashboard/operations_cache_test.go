package dashboard

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var (
	opsStubOnce     sync.Once
	opsStubMu       sync.Mutex
	opsStubRegistry = map[string]*opsStubState{}
)

type opsStubState struct {
	mu           sync.Mutex
	queries      []string
	txStarts     atomic.Int32
	stmtGroupHit atomic.Int32
	gate         chan struct{}
	entered      chan struct{}
}

type opsStubDriver struct{}

func (opsStubDriver) Open(dsn string) (driver.Conn, error) {
	opsStubMu.Lock()
	st := opsStubRegistry[dsn]
	opsStubMu.Unlock()
	if st == nil {
		return nil, errors.New("unknown ops stub dsn")
	}
	return &opsStubConn{st: st}, nil
}

type opsStubConn struct{ st *opsStubState }

func (*opsStubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (*opsStubConn) Close() error                        { return nil }
func (c *opsStubConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *opsStubConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.st.txStarts.Add(1)
	return &opsStubTx{}, nil
}

type opsStubTx struct{}

func (*opsStubTx) Commit() error   { return nil }
func (*opsStubTx) Rollback() error { return nil }

func (c *opsStubConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.st.mu.Lock()
	c.st.queries = append(c.st.queries, query)
	c.st.mu.Unlock()

	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "system_settings"):
		return &opsStubRows{cols: []string{"tz"}, data: [][]driver.Value{{"UTC"}}}, nil
	case strings.Contains(lower, "from query_executions"):
		return &opsStubRows{cols: []string{"count"}, data: [][]driver.Value{{int64(25)}}}, nil
	case strings.Contains(lower, "from users"):
		return &opsStubRows{cols: []string{"count"}, data: [][]driver.Value{{int64(10)}}}, nil
	case strings.Contains(lower, "from datasources"):
		return &opsStubRows{cols: []string{"count"}, data: [][]driver.Value{{int64(4)}}}, nil
	case strings.Contains(lower, "from sql_statements"):
		c.st.stmtGroupHit.Add(1)
		if c.st.entered != nil {
			select {
			case c.st.entered <- struct{}{}:
			default:
			}
		}
		if c.st.gate != nil {
			select {
			case <-c.st.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &opsStubRows{
			cols: []string{"statement_kind", "n"},
			data: [][]driver.Value{{"ddl", int64(4)}, {"dml", int64(12)}},
		}, nil
	case strings.Contains(lower, "percentile_cont"):
		return &opsStubRows{cols: []string{"p50", "p95"}, data: [][]driver.Value{{int64(120), int64(450)}}}, nil
	case strings.Contains(lower, "to_char"):
		return &opsStubRows{
			cols: []string{"day", "n"},
			data: [][]driver.Value{{"2026-11-20", float64(6)}, {"2026-11-21", float64(9)}},
		}, nil
	case strings.Contains(lower, "from change_orders") && strings.Contains(lower, "group by state"):
		return &opsStubRows{
			cols: []string{"state", "n"},
			data: [][]driver.Value{
				{"completed", int64(7)},
				{"failed", int64(2)},
				{"partial_failed", int64(1)},
				{"submitted", int64(5)},
			},
		}, nil
	default:
		return &opsStubRows{cols: []string{}}, nil
	}
}

type opsStubRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (r *opsStubRows) Columns() []string { return r.cols }
func (r *opsStubRows) Close() error      { return nil }
func (r *opsStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func newOpsStubDB(t *testing.T, st *opsStubState) *gorm.DB {
	t.Helper()
	opsStubOnce.Do(func() {
		sql.Register("dashboard_ops_stub", opsStubDriver{})
	})
	dsn := fmt.Sprintf("ops-stub-%d", time.Now().UnixNano())
	opsStubMu.Lock()
	opsStubRegistry[dsn] = st
	opsStubMu.Unlock()
	t.Cleanup(func() {
		opsStubMu.Lock()
		delete(opsStubRegistry, dsn)
		opsStubMu.Unlock()
	})
	sqlDB, err := sql.Open("dashboard_ops_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	gdb, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return gdb
}

func TestCloneOperationsViewIndependent(t *testing.T) {
	if cloneOperationsView(nil) != nil {
		t.Fatal("expected nil clone for nil input")
	}
	orig := &OperationsView{
		Meta: SnapshotMeta{
			WindowStart:           "2026-10-22",
			WindowEnd:             "2026-11-21",
			SystemTimezone:        "UTC",
			RefreshedAt:           "2026-11-21T00:00:00Z",
			Completeness:          "complete",
			UnavailableMetricKeys: []string{"m1"},
		},
		ChangeOrderTotal: 10,
		OrderTrend:       []TrendPoint{{Day: "2026-11-21", Value: 10}},
		StateDistribution: StateDistributionFromRows(map[string]int64{
			"completed": 8,
			"failed":    2,
		}),
	}
	cp := cloneOperationsView(orig)
	cp.Meta.UnavailableMetricKeys[0] = "mutated"
	cp.OrderTrend[0].Value = 999
	cp.StateDistribution["completed"] = 999
	cp.StateDistribution["submitted"] = 777

	if orig.Meta.UnavailableMetricKeys[0] != "m1" {
		t.Fatalf("UnavailableMetricKeys aliased: %v", orig.Meta.UnavailableMetricKeys)
	}
	if orig.OrderTrend[0].Value != 10 {
		t.Fatalf("OrderTrend aliased: %+v", orig.OrderTrend)
	}
	if orig.StateDistribution["completed"] != 8 || orig.StateDistribution["submitted"] != 0 {
		t.Fatalf("StateDistribution aliased: %+v", orig.StateDistribution)
	}
}

func TestOperationsDashboardCacheTTLKeyClampingAndQueries(t *testing.T) {
	st := &opsStubState{}
	gdb := newOpsStubDB(t, st)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	svc := &Service{
		DB: gdb,
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
	}

	ctx := context.Background()
	// Window clamping happens BEFORE cache lookup: 0, 30, and 999 all map to key 30.
	v1, err := svc.OperationsDashboard(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v1.ChangeOrderTotal != 15 || v1.CompletedTotal != 7 || v1.FailedTotal != 2 || v1.PartialFailedTotal != 1 {
		t.Fatalf("unexpected derived order totals: %+v", v1)
	}
	if v1.DdlStatementTotal != 4 || v1.DmlStatementTotal != 12 {
		t.Fatalf("unexpected statement totals: ddl=%d dml=%d", v1.DdlStatementTotal, v1.DmlStatementTotal)
	}
	var sum int64
	for _, n := range v1.StateDistribution {
		sum += n
	}
	if sum != v1.ChangeOrderTotal {
		t.Fatalf("state distribution sum %d != change_order_total %d", sum, v1.ChangeOrderTotal)
	}

	// Verify redundant change_orders count queries and duplicate sql_statements scans were eliminated.
	st.mu.Lock()
	for _, q := range st.queries {
		lq := strings.ToLower(q)
		if strings.Contains(lq, "from change_orders") && !strings.Contains(lq, "group by") {
			t.Fatalf("unexpected ungrouped change_orders query: %s", q)
		}
	}
	st.mu.Unlock()
	if st.stmtGroupHit.Load() != 1 {
		t.Fatalf("expected 1 grouped sql_statements query, got %d", st.stmtGroupHit.Load())
	}

	// Mutating caller's copy must not affect cached view.
	v1.StateDistribution["completed"] = 999
	v1.OrderTrend[0].Value = 999

	v2, err := svc.OperationsDashboard(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	v3, err := svc.OperationsDashboard(ctx, 999)
	if err != nil {
		t.Fatal(err)
	}
	if v2.StateDistribution["completed"] != 7 || v2.OrderTrend[0].Value != 6 {
		t.Fatalf("cache was mutated via returned view: %+v", v2)
	}
	if v3.ChangeOrderTotal != 15 || st.txStarts.Load() != 1 {
		t.Fatalf("expected clamped windows (0/30/999) to share cache key, txStarts=%d", st.txStarts.Load())
	}

	// Distinct window key 7 misses cache.
	if _, err := svc.OperationsDashboard(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if st.txStarts.Load() != 2 {
		t.Fatalf("expected distinct window 7 to query DB, txStarts=%d", st.txStarts.Load())
	}

	// Advancing clock by 4s stays within 5s default TTL.
	clockMu.Lock()
	now = now.Add(4 * time.Second)
	clockMu.Unlock()
	if _, err := svc.OperationsDashboard(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if st.txStarts.Load() != 2 {
		t.Fatalf("expected cache hit within 5s TTL, txStarts=%d", st.txStarts.Load())
	}

	// Advancing clock past 5s TTL expires window 30 entry.
	clockMu.Lock()
	now = now.Add(2 * time.Second)
	clockMu.Unlock()
	if _, err := svc.OperationsDashboard(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if st.txStarts.Load() != 3 {
		t.Fatalf("expected cache expiry after 6s, txStarts=%d", st.txStarts.Load())
	}

	// Disabling cache via SetOperationsCacheTTLForTest(0) bypasses cache on every call.
	svc.SetOperationsCacheTTLForTest(0)
	if _, err := svc.OperationsDashboard(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.OperationsDashboard(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if st.txStarts.Load() != 5 {
		t.Fatalf("expected TTL=0 to bypass cache on every call, txStarts=%d", st.txStarts.Load())
	}
}

func TestOperationsDashboardSingleflightCoalescesAndIsolatesClones(t *testing.T) {
	st := &opsStubState{
		gate:    make(chan struct{}),
		entered: make(chan struct{}, 1),
	}
	gdb := newOpsStubDB(t, st)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	svc := &Service{
		DB:  gdb,
		Now: func() time.Time { return now },
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]*OperationsView, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v, err := svc.OperationsDashboard(context.Background(), 14)
			results[idx] = v
			errs[idx] = err
			if v != nil {
				v.StateDistribution["completed"] = int64(1000 + idx)
			}
		}(i)
	}

	select {
	case <-st.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for singleflight leader")
	}
	time.Sleep(25 * time.Millisecond)
	close(st.gate)
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil || results[i] == nil {
			t.Fatalf("worker %d failed: err=%v view=%+v", i, errs[i], results[i])
		}
		if results[i].CompletedTotal != 7 {
			t.Fatalf("worker %d got unexpected CompletedTotal=%d", i, results[i].CompletedTotal)
		}
	}
	if st.txStarts.Load() != 1 {
		t.Fatalf("expected singleflight to coalesce 8 callers into 1 DB tx, got %d", st.txStarts.Load())
	}

	// Verify none of the worker mutations leaked into cached entry.
	fresh, err := svc.OperationsDashboard(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.StateDistribution["completed"] != 7 {
		t.Fatalf("cached StateDistribution was mutated by caller: %d", fresh.StateDistribution["completed"])
	}
}
