// exec_cursor_stream_ctx_test.go F-Q-002 regression: the owner context of a cursor result stream /
// read-only transaction must be decoupled from the request context that opened it. A real HTTP server
// cancels the request context the instant the handler returns — if the stream is bound to it, all
// cross-request page fetches fail with "cursor stream failed" (causing 4011 during browser XLSX export).
// The fake driver reproduces pgx semantics: once the context captured by QueryContext is cancelled, subsequent Next calls fail.
package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
)

// ---- Fake driver: context-bound stream ----

type ctxStreamConnector struct {
	rowsFn func() *ctxStreamRows
	log    *[]string
}

func (c *ctxStreamConnector) Connect(context.Context) (driver.Conn, error) {
	return &ctxStreamConn{rowsFn: c.rowsFn, log: c.log}, nil
}
func (c *ctxStreamConnector) Driver() driver.Driver { return ctxStreamDriver{} }

type ctxStreamDriver struct{}

func (ctxStreamDriver) Open(string) (driver.Conn, error) { return nil, errors.New("open unused") }

type ctxStreamConn struct {
	rowsFn func() *ctxStreamRows
	log    *[]string // Recorded query order (for asserting prior statements)
}

func (c *ctxStreamConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unused")
}
func (c *ctxStreamConn) Close() error              { return nil }
func (c *ctxStreamConn) Begin() (driver.Tx, error) { return noopTx{}, nil }
func (c *ctxStreamConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return noopTx{}, nil
}
func (c *ctxStreamConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (c *ctxStreamConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	// Capture query context: stream dies once cancelled (pgx / go-sql-driver semantics).
	// Each query generates a fresh result set (draining prior statements does not exhaust data for subsequent statements).
	*c.log = append(*c.log, q)
	rows := c.rowsFn()
	rows.queryCtx = ctx
	return rows, nil
}

type noopTx struct{}

func (noopTx) Commit() error   { return nil }
func (noopTx) Rollback() error { return nil }

type ctxStreamRows struct {
	cols     []string
	data     [][]driver.Value
	pos      int
	queryCtx context.Context
}

func (r *ctxStreamRows) Columns() []string { return r.cols }
func (r *ctxStreamRows) Close() error      { return nil }
func (r *ctxStreamRows) Next(dest []driver.Value) error {
	if r.queryCtx != nil && r.queryCtx.Err() != nil {
		return errors.New("fake driver: context canceled")
	}
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

// TestCursorStreamSurvivesRequestCancel Cursor pagination must remain readable after the request
// context is cancelled (F-Q-002 root-cause anchor).
func TestCursorStreamSurvivesRequestCancel(t *testing.T) {
	cols := []string{"id"}
	data := [][]driver.Value{{int64(1)}, {int64(2)}, {int64(3)}}
	var qlog []string
	var streamed *ctxStreamRows // For assertion: result set instance of the streaming query
	pool := &PurposePool{
		engine: "postgresql", purpose: PurposeQuery,
		db: sql.OpenDB(&ctxStreamConnector{log: &qlog, rowsFn: func() *ctxStreamRows {
			r := &ctxStreamRows{cols: cols, data: data}
			streamed = r
			return r
		}}),
		cursors:     map[string]*liveCursor{},
		execCancels: map[string]context.CancelFunc{},
	}
	adapter := NewPostgreSQLAdapter(Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{})

	reqCtx, cancelReq := context.WithCancel(context.Background())
	cur, err := adapter.OpenSelectCursor(reqCtx, pool, OpenCursorOptions{
		StatementSQL: "select id from t", SchemaName: "demo", PageSize: 1,
	})
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	page1, err := adapter.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil || len(page1.Rows) != 1 || !page1.HasMore {
		t.Fatalf("first page: err=%v rows=%d hasMore=%v", err, len(page1.Rows), page1.HasMore)
	}
	// Simulate HTTP response completion: request context is cancelled.
	cancelReq()
	page2, err := adapter.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil {
		t.Fatalf("continuation after request cancel failed (F-Q-002): %v", err)
	}
	if len(page2.Rows) != 1 || !page2.HasMore {
		t.Fatalf("second page: rows=%d hasMore=%v", len(page2.Rows), page2.HasMore)
	}
	page3, err := adapter.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil || len(page3.Rows) != 1 {
		t.Fatalf("third page: err=%v rows=%d", err, len(page3.Rows))
	}
	// Full page conservatively sets HasMore=true (without prefetching the next row) — terminates after an empty tail page.
	if !page3.HasMore {
		t.Fatal("third page should conservatively report has_more")
	}
	page4, err := adapter.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil || len(page4.Rows) != 0 || page4.HasMore {
		t.Fatalf("terminal page: err=%v rows=%d hasMore=%v", err, len(page4.Rows), page4.HasMore)
	}
	// Pool Close terminates the stream: subsequent reads end with cursor expired (governance path does not regress).
	if err := pool.Close(); err != nil {
		t.Fatalf("pool close: %v", err)
	}
	if _, err := adapter.FetchCursorPage(context.Background(), pool, cur, 0); err == nil {
		t.Fatal("fetch after pool close should fail")
	}
	_ = streamed
}

// TestCursorBatchPriorStatementsOrder F-Q-001: batch prior statements execute on the same
// connection before the final (cursor-opening) statement, and each undergoes shallow validation —
// prior statements smuggling non-SELECT operations are rejected when opening the cursor.
func TestCursorBatchPriorStatementsOrder(t *testing.T) {
	var qlog2 []string
	pool := &PurposePool{
		engine: "postgresql", purpose: PurposeQuery,
		db: sql.OpenDB(&ctxStreamConnector{log: &qlog2, rowsFn: func() *ctxStreamRows {
			return &ctxStreamRows{cols: []string{"id"}, data: [][]driver.Value{{int64(9)}}}
		}}),
		cursors:     map[string]*liveCursor{},
		execCancels: map[string]context.CancelFunc{},
	}
	adapter := NewPostgreSQLAdapter(Endpoint{CompatibilityMod: "postgresql"}, AdapterOptions{})

	cur, err := adapter.OpenSelectCursor(context.Background(), pool, OpenCursorOptions{
		PriorStatements: []string{"select id from a", "select id from b"},
		StatementSQL:    "select id from final", SchemaName: "demo", PageSize: 100,
	})
	if err != nil {
		t.Fatalf("open batch cursor: %v", err)
	}
	page, err := adapter.FetchCursorPage(context.Background(), pool, cur, 0)
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("first page: err=%v rows=%d", err, len(page.Rows))
	}
	want := []string{"select id from a", "select id from b", "select id from final"}
	if len(qlog2) != len(want) {
		t.Fatalf("query order log = %v want %v", qlog2, want)
	}
	for i := range want {
		if qlog2[i] != want[i] {
			t.Fatalf("query order log = %v want %v", qlog2, want)
		}
	}

	// Impure prior statement (shallow validation defense): opening the entire batch is rejected.
	pool2 := &PurposePool{
		engine: "postgresql", purpose: PurposeQuery,
		db: sql.OpenDB(&ctxStreamConnector{log: &qlog2, rowsFn: func() *ctxStreamRows {
			return &ctxStreamRows{cols: []string{"id"}, data: [][]driver.Value{{int64(9)}}}
		}}),
		cursors:     map[string]*liveCursor{},
		execCancels: map[string]context.CancelFunc{},
	}
	if _, err := adapter.OpenSelectCursor(context.Background(), pool2, OpenCursorOptions{
		PriorStatements: []string{"delete from t"},
		StatementSQL:    "select 1", SchemaName: "demo", PageSize: 100,
	}); err == nil {
		t.Fatal("non-SELECT prior statement should be rejected at open")
	}
}
