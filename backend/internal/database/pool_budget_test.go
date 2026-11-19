// pool_budget_test.go Unit tests for SEC-0027 single-page byte budget and pool Close hook.
// FetchCursorPage budget behavior exercises the real read loop via an in-memory fake driver
// (covering truncated continuation, single-row rejection, cursor termination, and cross-page row order continuity);
// does not require a real target database.
package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// ---- Pure Page Budget Decisions ----

func TestPageBudgetAdmit(t *testing.T) {
	// Empty page + small row: admit into page and continue.
	appendRow, stop, err := pageBudgetAdmit(0, 100)
	if err != nil || !appendRow || stop {
		t.Fatalf("small row should fit: append=%v stop=%v err=%v", appendRow, stop, err)
	}
	// Exact budget boundary: admit into page and continue.
	appendRow, stop, err = pageBudgetAdmit(maxPageBytes-perCellOverheadBytes-10, 10+perCellOverheadBytes)
	if err != nil || !appendRow || stop {
		t.Fatalf("boundary row should fit: append=%v stop=%v err=%v", appendRow, stop, err)
	}
	// Non-empty page exceeding budget: admit row (preserving row order continuity) and stop fetching without error.
	appendRow, stop, err = pageBudgetAdmit(maxPageBytes, 10)
	if err != nil || !appendRow || !stop {
		t.Fatalf("overflow row should append+stop: append=%v stop=%v err=%v", appendRow, stop, err)
	}
	// Single row exceeds budget on an empty page: reject row, stop, and return structured budget error.
	appendRow, stop, err = pageBudgetAdmit(0, maxPageBytes+1)
	if !errors.Is(err, ErrPageBudgetExceeded) || appendRow || !stop {
		t.Fatalf("single oversized row should reject: append=%v stop=%v err=%v", appendRow, stop, err)
	}
}

func TestEstimateRowBytes(t *testing.T) {
	row := []QueryValue{
		{Kind: "string", Text: strings.Repeat("a", 100)},
		{Kind: "null", Text: ""},
		{Kind: "int", Text: "12345"},
	}
	want := (100 + perCellOverheadBytes) + perCellOverheadBytes + (5 + perCellOverheadBytes)
	if got := estimateRowBytes(row); got != want {
		t.Fatalf("estimateRowBytes=%d want %d", got, want)
	}
}

// ---- PurposePool Close Hook (SEC-0027 Budget Release Carrier) ----

func TestPurposePoolCloseHookFiresOnce(t *testing.T) {
	p := &PurposePool{db: newBudgetFakeDB(func() driver.Rows { return &budgetFakeRows{} })}
	calls := 0
	p.SetCloseHook(func() { calls++ })
	if err := p.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close (idempotent): %v", err)
	}
	if calls != 1 {
		t.Fatalf("close hook fired %d times, want 1", calls)
	}
}

// ---- FetchCursorPage End-to-End Page Budget (Fake Driver Exercising Real Read Loop) ----

// budgetFakeRows in-memory result set.
type budgetFakeRows struct {
	cols []string
	vals [][]driver.Value
	i    int
}

func (r *budgetFakeRows) Columns() []string { return r.cols }
func (r *budgetFakeRows) Close() error      { return nil }
func (r *budgetFakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.i])
	r.i++
	return nil
}

type budgetFakeConn struct{ rows driver.Rows }

func (c *budgetFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unused")
}
func (c *budgetFakeConn) Close() error              { return nil }
func (c *budgetFakeConn) Begin() (driver.Tx, error) { return nil, errors.New("begin unused") }
func (c *budgetFakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	// Session defaults / read-only session / USE schema are all absorbed here.
	return driver.RowsAffected(0), nil
}
func (c *budgetFakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.rows, nil
}

type budgetFakeConnector struct{ rows func() driver.Rows }

func (c *budgetFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &budgetFakeConn{rows: c.rows()}, nil
}
func (c *budgetFakeConnector) Driver() driver.Driver { return budgetFakeDriver{} }

type budgetFakeDriver struct{}

func (budgetFakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("open unused") }

func newBudgetFakeDB(rows func() driver.Rows) *sql.DB {
	return sql.OpenDB(&budgetFakeConnector{rows: rows})
}

func newBudgetFakePool(rows func() driver.Rows) *PurposePool {
	return &PurposePool{
		engine: "mysql", purpose: PurposeQuery, db: newBudgetFakeDB(rows),
		cursors: map[string]*liveCursor{}, execCancels: map[string]context.CancelFunc{},
		execBackends: map[string]int64{},
	}
}

func newBudgetFakeAdapter() Adapter {
	return NewMySQLAdapter(Endpoint{CompatibilityMod: "mysql"}, AdapterOptions{})
}

// TestFetchCursorPageBudgetTruncatesAndContinues Page truncation upon reaching budget: returns
// the portion up to the budget (including the overflow row to preserve cross-page row order continuity),
// continues pagination normally with HasMore/NextCursor, and terminates/cleans up the cursor on the final page.
func TestFetchCursorPageBudgetTruncatesAndContinues(t *testing.T) {
	cell := strings.Repeat("x", 200000)
	row := []driver.Value{cell, cell}
	// Estimated 2*(200000+32)=400064B per row; 10 rows = 4000640 <= 4MiB, 11th row overflows.
	vals := make([][]driver.Value, 0, 12)
	for i := 0; i < 12; i++ {
		vals = append(vals, row)
	}
	pool := newBudgetFakePool(func() driver.Rows {
		return &budgetFakeRows{cols: []string{"a", "b"}, vals: vals}
	})
	defer func() { _ = pool.Close() }()
	a := newBudgetFakeAdapter()
	cur, err := a.OpenSelectCursor(context.Background(), pool, OpenCursorOptions{
		StatementSQL: "select a, b from t", SchemaName: "main", PageSize: 100,
	})
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	page, err := a.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Rows) != 11 {
		t.Fatalf("truncated page rows=%d, want 11 (10 fit + 1 overflow row)", len(page.Rows))
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("truncated page must continue pagination: has_more=%v next=%q", page.HasMore, page.NextCursor)
	}
	page2, err := a.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page2.Rows) != 1 {
		t.Fatalf("final page rows=%d, want 1 (contiguous row order: 11+1=12)", len(page2.Rows))
	}
	if page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("final page must terminate: has_more=%v next=%q", page2.HasMore, page2.NextCursor)
	}
	// Cursor is terminated: fetching again returns ErrCursorExpired.
	if _, err := a.FetchCursorPage(context.Background(), pool, cur, 0); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("third fetch should be ErrCursorExpired, got %v", err)
	}
}

// TestFetchCursorPageSingleRowOverBudget Single row exceeds budget: rejects the entire page,
// returns ErrPageBudgetExceeded, and terminates the cursor.
func TestFetchCursorPageSingleRowOverBudget(t *testing.T) {
	cell := strings.Repeat("y", 900000)
	// Single row 5*(900000+32)=4500160 > 4MiB.
	pool := newBudgetFakePool(func() driver.Rows {
		return &budgetFakeRows{cols: []string{"a", "b", "c", "d", "e"},
			vals: [][]driver.Value{{cell, cell, cell, cell, cell}}}
	})
	defer func() { _ = pool.Close() }()
	a := newBudgetFakeAdapter()
	cur, err := a.OpenSelectCursor(context.Background(), pool, OpenCursorOptions{
		StatementSQL: "select a, b, c, d, e from t", SchemaName: "main", PageSize: 100,
	})
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	if _, err := a.FetchCursorPage(context.Background(), pool, cur, 0); !errors.Is(err, ErrPageBudgetExceeded) {
		t.Fatalf("want ErrPageBudgetExceeded, got %v", err)
	}
	if _, err := a.FetchCursorPage(context.Background(), pool, cur, 0); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("cursor must be dropped after budget rejection, got %v", err)
	}
}

// TestFetchCursorPageNoBudgetBreachNormalPath Small-row pages are unaffected by the budget:
// read full pages normally and terminate naturally (existing pagination semantics unchanged).
func TestFetchCursorPageNoBudgetBreachNormalPath(t *testing.T) {
	vals := make([][]driver.Value, 0, 3)
	for i := 0; i < 3; i++ {
		vals = append(vals, []driver.Value{"tiny"})
	}
	pool := newBudgetFakePool(func() driver.Rows {
		return &budgetFakeRows{cols: []string{"a"}, vals: vals}
	})
	defer func() { _ = pool.Close() }()
	a := newBudgetFakeAdapter()
	cur, err := a.OpenSelectCursor(context.Background(), pool, OpenCursorOptions{
		StatementSQL: "select a from t", SchemaName: "main", PageSize: 100,
	})
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	page, err := a.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Rows) != 3 || page.HasMore || page.NextCursor != "" {
		t.Fatalf("normal page mismatch: rows=%d has_more=%v next=%q",
			len(page.Rows), page.HasMore, page.NextCursor)
	}
}
