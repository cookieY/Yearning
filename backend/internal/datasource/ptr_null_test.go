package datasource

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
)

func TestAuditChangeEmptyRequestIDBindsSQLNull(t *testing.T) {
	drv := &stubDatasourceAuditDriver{}
	sqlDB := sql.OpenDB(drv)
	defer func() { _ = sqlDB.Close() }()

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	rec, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	svc := &Service{db: gdb, audit: rec}

	// Empty RequestID must bind nil (SQL NULL) into audit_events.request_id (UUID column), never "".
	svc.auditChange(
		context.Background(),
		ActorRef{UserID: "u-1", Username: "admin"},
		"datasource.updated",
		"replace_datasource",
		"ds-1",
		"succeeded",
		auditMeta{RequestID: ""},
		map[string]any{"engine": "postgres"},
	)

	// Non-empty RequestID must bind the string value into audit_events.request_id.
	svc.auditChange(
		context.Background(),
		ActorRef{UserID: "u-1", Username: "admin"},
		"datasource.updated",
		"replace_datasource",
		"ds-1",
		"succeeded",
		auditMeta{RequestID: "req-123"},
		nil,
	)

	inserts := drv.snapshot()
	if len(inserts) != 2 {
		t.Fatalf("expected 2 audit_events inserts, got %d", len(inserts))
	}
	if val, ok := inserts[0]["request_id"]; !ok {
		t.Fatalf("request_id column missing from insert[0]: %v", inserts[0])
	} else if val != nil {
		t.Fatalf("expected empty RequestID to bind SQL NULL (nil), got %#v", val)
	}
	if val, ok := inserts[1]["request_id"]; !ok || val != "req-123" {
		t.Fatalf("expected populated RequestID to bind %q, got %#v", "req-123", val)
	}
}

type stubDatasourceAuditDriver struct {
	mu      sync.Mutex
	inserts []map[string]any
}

func (d *stubDatasourceAuditDriver) Connect(context.Context) (driver.Conn, error) {
	return &stubDatasourceAuditConn{parent: d}, nil
}
func (d *stubDatasourceAuditDriver) Driver() driver.Driver { return d }
func (d *stubDatasourceAuditDriver) Open(string) (driver.Conn, error) {
	return &stubDatasourceAuditConn{parent: d}, nil
}

func (d *stubDatasourceAuditDriver) snapshot() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]map[string]any, len(d.inserts))
	copy(out, d.inserts)
	return out
}

type stubDatasourceAuditConn struct{ parent *stubDatasourceAuditDriver }

func (c *stubDatasourceAuditConn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (c *stubDatasourceAuditConn) Close() error                        { return nil }
func (c *stubDatasourceAuditConn) Begin() (driver.Tx, error)           { return stubDatasourceAuditTx{}, nil }

func (c *stubDatasourceAuditConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if cols := parseInsertColumns(query, "audit_events"); len(cols) > 0 {
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			if i < len(args) {
				row[col] = args[i].Value
			}
		}
		c.parent.mu.Lock()
		c.parent.inserts = append(c.parent.inserts, row)
		c.parent.mu.Unlock()
	}
	return driver.RowsAffected(1), nil
}

func (c *stubDatasourceAuditConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return stubEmptyRows{}, nil
}

type stubDatasourceAuditTx struct{}

func (stubDatasourceAuditTx) Commit() error   { return nil }
func (stubDatasourceAuditTx) Rollback() error { return nil }

type stubEmptyRows struct{}

func (stubEmptyRows) Columns() []string         { return nil }
func (stubEmptyRows) Close() error              { return nil }
func (stubEmptyRows) Next([]driver.Value) error { return io.EOF }

func parseInsertColumns(query, table string) []string {
	low := strings.ToLower(query)
	if !strings.Contains(low, "insert into") || !strings.Contains(low, table) {
		return nil
	}
	start := strings.Index(low, "(")
	end := strings.Index(low, ")")
	if start < 0 || end <= start {
		return nil
	}
	parts := strings.Split(low[start+1:end], ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		col := strings.Trim(strings.TrimSpace(p), `"`)
		if col != "" {
			cols = append(cols, col)
		}
	}
	return cols
}
