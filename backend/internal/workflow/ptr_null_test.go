package workflow

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

func TestAuditGroupEmptyRequestIDBindsSQLNull(t *testing.T) {
	drv := &stubWorkflowAuditDriver{}
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
	svc.auditGroup(
		context.Background(),
		AdminActor{UserID: "u-1", Username: "admin"},
		"permission_group.updated",
		"replace_permission_group",
		"grp-1",
		RequestMeta{RequestID: ""},
	)

	// Non-empty RequestID must bind the string value into audit_events.request_id.
	svc.auditGroup(
		context.Background(),
		AdminActor{UserID: "u-1", Username: "admin"},
		"permission_group.updated",
		"replace_permission_group",
		"grp-1",
		RequestMeta{RequestID: "req-wf-1"},
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
	if val, ok := inserts[1]["request_id"]; !ok || val != "req-wf-1" {
		t.Fatalf("expected populated RequestID to bind %q, got %#v", "req-wf-1", val)
	}
}

type stubWorkflowAuditDriver struct {
	mu      sync.Mutex
	inserts []map[string]any
}

func (d *stubWorkflowAuditDriver) Connect(context.Context) (driver.Conn, error) {
	return &stubWorkflowAuditConn{parent: d}, nil
}
func (d *stubWorkflowAuditDriver) Driver() driver.Driver { return d }
func (d *stubWorkflowAuditDriver) Open(string) (driver.Conn, error) {
	return &stubWorkflowAuditConn{parent: d}, nil
}

func (d *stubWorkflowAuditDriver) snapshot() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]map[string]any, len(d.inserts))
	copy(out, d.inserts)
	return out
}

type stubWorkflowAuditConn struct{ parent *stubWorkflowAuditDriver }

func (c *stubWorkflowAuditConn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (c *stubWorkflowAuditConn) Close() error                        { return nil }
func (c *stubWorkflowAuditConn) Begin() (driver.Tx, error)           { return stubWorkflowAuditTx{}, nil }

func (c *stubWorkflowAuditConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if cols := parseWorkflowInsertColumns(query, "audit_events"); len(cols) > 0 {
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

func (c *stubWorkflowAuditConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return stubWorkflowEmptyRows{}, nil
}

type stubWorkflowAuditTx struct{}

func (stubWorkflowAuditTx) Commit() error   { return nil }
func (stubWorkflowAuditTx) Rollback() error { return nil }

type stubWorkflowEmptyRows struct{}

func (stubWorkflowEmptyRows) Columns() []string         { return nil }
func (stubWorkflowEmptyRows) Close() error              { return nil }
func (stubWorkflowEmptyRows) Next([]driver.Value) error { return io.EOF }

func parseWorkflowInsertColumns(query, table string) []string {
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
