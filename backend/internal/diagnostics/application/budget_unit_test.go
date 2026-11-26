package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

type diagBudgetStubState struct {
	mu               sync.Mutex
	failBudgetLoad   bool
	failBudgetUpsert bool
	txRollbacks      int
	releases         []int64
}

type diagBudgetDriver struct{}

var (
	diagBudgetMu   sync.Mutex
	diagBudgetMap  = map[string]*diagBudgetStubState{}
	diagBudgetOnce sync.Once
)

func (diagBudgetDriver) Open(dsn string) (driver.Conn, error) {
	diagBudgetMu.Lock()
	st := diagBudgetMap[dsn]
	diagBudgetMu.Unlock()
	return &diagBudgetConn{state: st}, nil
}

type diagBudgetConn struct{ state *diagBudgetStubState }
type diagBudgetTx struct{ state *diagBudgetStubState }

func (c *diagBudgetConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *diagBudgetConn) Close() error                        { return nil }
func (c *diagBudgetConn) Begin() (driver.Tx, error)           { return diagBudgetTx{state: c.state}, nil }
func (c *diagBudgetConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return diagBudgetTx{state: c.state}, nil
}
func (t diagBudgetTx) Commit() error { return nil }
func (t diagBudgetTx) Rollback() error {
	t.state.mu.Lock()
	t.state.txRollbacks++
	t.state.mu.Unlock()
	return nil
}

func (c *diagBudgetConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	failUpsert := c.state.failBudgetUpsert
	c.state.mu.Unlock()
	if strings.Contains(query, "update ai_daily_budget_usage set reserved_minor = greatest") && len(args) > 0 {
		if amt, ok := args[0].Value.(int64); ok {
			c.state.mu.Lock()
			c.state.releases = append(c.state.releases, amt)
			c.state.mu.Unlock()
		}
	}
	if failUpsert && strings.Contains(query, "insert into ai_daily_budget_usage") {
		return nil, errors.New("simulated daily budget upsert failure")
	}
	return driver.RowsAffected(1), nil
}

func (c *diagBudgetConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	failLoad := c.state.failBudgetLoad
	c.state.mu.Unlock()
	if failLoad && strings.Contains(query, "namespace = 'ai_budget'") {
		return nil, errors.New("simulated ai_budget query error")
	}
	return &diagBudgetStubRows{}, nil
}

type diagBudgetStubRows struct{}

func (*diagBudgetStubRows) Columns() []string         { return []string{} }
func (*diagBudgetStubRows) Close() error              { return nil }
func (*diagBudgetStubRows) Next([]driver.Value) error { return io.EOF }

func TestDiagnosticsBudgetFailClosedOnLoadErrorAndTransactionalSettle(t *testing.T) {
	diagBudgetOnce.Do(func() {
		sql.Register("diagnostics_budget_stub", diagBudgetDriver{})
	})
	st := &diagBudgetStubState{failBudgetLoad: true, failBudgetUpsert: true}
	dsn := "diag-budget-" + uuid.NewString()
	diagBudgetMu.Lock()
	diagBudgetMap[dsn] = st
	diagBudgetMu.Unlock()
	t.Cleanup(func() {
		diagBudgetMu.Lock()
		delete(diagBudgetMap, dsn)
		diagBudgetMu.Unlock()
	})

	sqlDB, err := sql.Open("diagnostics_budget_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB, PreferSimpleProtocol: true, WithoutReturning: true,
	}), &gorm.Config{DisableAutomaticPing: true, Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}

	// 1. D14 fail-closed: nil Review or nil DB must return ErrBudgetServiceUnavailable.
	svcNilReview := &Services{DB: gdb}
	if _, err := svcNilReview.acquireDailyBudgetLease(context.Background(), gdb, 1024); !errors.Is(err, reviewapp.ErrBudgetServiceUnavailable) {
		t.Fatalf("expected ErrBudgetServiceUnavailable when Review is nil, got %v", err)
	}
	svcNilDB := &Services{Review: &reviewapp.Services{}}
	if _, err := svcNilDB.acquireDailyBudgetLease(context.Background(), nil, 1024); !errors.Is(err, reviewapp.ErrBudgetServiceUnavailable) {
		t.Fatalf("expected ErrBudgetServiceUnavailable when DB is nil, got %v", err)
	}

	// 2. LoadBudgetSettings DB error must fail closed.
	reviewSvc := &reviewapp.Services{DB: gdb}
	svc := &Services{DB: gdb, Review: reviewSvc}
	if _, err := svc.acquireDailyBudgetLease(context.Background(), gdb, 1024); err == nil {
		t.Fatal("expected acquireDailyBudgetLease to fail closed on LoadBudgetSettings DB error")
	}

	// 3. Partial Settle failure rolls back tx and releases full reservedMinor outside tx.
	settings := reviewapp.BudgetSettings{
		Enforced: true, Currency: "USD", DailyBudgetMinor: 10000,
		InputPricePerMillionMinor: 10000, OutputPricePerMillionMinor: 20000,
	}
	leaseFail := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 500, settings)
	if err := leaseFail.Settle(context.Background(), uuid.NewString(), "", "gpt-4o-mini", "diagnostics", 200, 100, "succeeded"); err == nil {
		t.Fatal("expected BudgetLease.Settle error when ai_daily_budget_usage upsert fails")
	}
	st.mu.Lock()
	rollbacks := st.txRollbacks
	releases := append([]int64(nil), st.releases...)
	st.mu.Unlock()
	if rollbacks != 1 {
		t.Fatalf("expected 1 transaction rollback on partial Settle failure, got %d", rollbacks)
	}
	if len(releases) != 1 || releases[0] != 500 {
		t.Fatalf("expected outer release of 500 after rolled-back Settle tx, got %v", releases)
	}

	// 4. Successful Settle under budget settles actual (100) and releases ONLY excess (400), never full 500.
	st.mu.Lock()
	st.failBudgetUpsert = false
	st.releases = nil
	st.mu.Unlock()
	underSettings := reviewapp.BudgetSettings{
		Enforced: true, Currency: "USD", DailyBudgetMinor: 10000,
		InputPricePerMillionMinor: 100, OutputPricePerMillionMinor: 0,
	}
	leaseUnder := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 500, underSettings)
	if err := leaseUnder.Settle(context.Background(), uuid.NewString(), "", "gpt-4o-mini", "diagnostics", 1_000_000, 0, "succeeded"); err != nil {
		t.Fatalf("under-budget Settle failed: %v", err)
	}
	st.mu.Lock()
	underReleases := append([]int64(nil), st.releases...)
	st.mu.Unlock()
	if len(underReleases) != 1 || underReleases[0] != 400 {
		t.Fatalf("expected partial excess release [400], got %v", underReleases)
	}

	// 5. Abort Release releases full reservedMinor once and is idempotent.
	st.mu.Lock()
	st.releases = nil
	st.mu.Unlock()
	leaseAbort := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 500, underSettings)
	if err := leaseAbort.Release(context.Background()); err != nil {
		t.Fatalf("abort Release failed: %v", err)
	}
	if err := leaseAbort.Release(context.Background()); err != nil {
		t.Fatalf("second abort Release failed: %v", err)
	}
	st.mu.Lock()
	abortReleases := append([]int64(nil), st.releases...)
	st.mu.Unlock()
	if len(abortReleases) != 1 || abortReleases[0] != 500 {
		t.Fatalf("expected single full abort release [500], got %v", abortReleases)
	}
}
