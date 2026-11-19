package auditlog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/realtime"
)

func TestRecorderEmitAndNotifyRealtime(t *testing.T) {
	drv := newStubAuditDriver()
	drv.queryRows["select submitter_user_id_snapshot from change_orders"] = []string{"submitter-1"}
	drv.queryRows["from order_approval_actors"] = []string{"reviewer-1", "reviewer-2"}
	drv.queryRows["from order_execution_actors"] = []string{"executor-1"}
	drv.queryRows["select requester_user_id_snapshot from query_grants"] = []string{"requester-1"}
	drv.queryRows["from query_access_approval_actors"] = []string{"q-reviewer-1"}

	sqlDB := sql.OpenDB(drv)
	defer sqlDB.Close()
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

	rec, err := NewRecorder(gdb)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	hub := realtime.NewHub()
	rec.SetRealtimeHub(hub)
	if rec.RealtimeHub() != hub {
		t.Fatal("RealtimeHub getter mismatch")
	}

	submitterCh, unsubSubmitter := hub.Subscribe("submitter-1", "user", false)
	defer unsubSubmitter()
	reviewerCh, unsubReviewer := hub.Subscribe("reviewer-1", "user", false)
	defer unsubReviewer()
	executorCh, unsubExecutor := hub.Subscribe("executor-1", "user", false)
	defer unsubExecutor()
	outsiderCh, unsubOutsider := hub.Subscribe("outsider-9", "user", false)
	defer unsubOutsider()
	adminCh, unsubAdmin := hub.Subscribe("admin-1", "admin", true)
	defer unsubAdmin()

	// 1. Emit on change-orders/{id} resolves submitter, reviewers, executors, and actor;
	// admin_is_not_business_override must prevent NotifyAdmins=true.
	err = rec.Emit(context.Background(), nil, "change_order.approval_decided", "change-orders/order-uuid-1",
		Actor{Kind: "user", UserID: "actor-1", Username: "reviewer"},
		map[string]any{"to": "stage_execution_pending"})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	for name, ch := range map[string]<-chan realtime.Delivery{
		"submitter": submitterCh,
		"reviewer":  reviewerCh,
		"executor":  executorCh,
	} {
		select {
		case d := <-ch:
			if d.Envelope.Type != "io.yearning.v4.approval.decided" ||
				d.Envelope.Subject != "change-orders/order-uuid-1" ||
				d.Envelope.Aggregate != "order-uuid-1" ||
				d.Envelope.State != "stage_execution_pending" {
				t.Fatalf("%s unexpected envelope: %+v", name, d.Envelope)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive emitted event", name)
		}
	}
	for name, ch := range map[string]<-chan realtime.Delivery{
		"outsider": outsiderCh,
		"admin":    adminCh,
	} {
		select {
		case d := <-ch:
			t.Fatalf("%s must not receive change_order event: %+v", name, d)
		case <-time.After(30 * time.Millisecond):
		}
	}

	// Verify pg_notify was executed with compacted BroadcastMessage containing expected routing metadata.
	notifyPayloads := drv.notifyPayloads()
	if len(notifyPayloads) != 1 {
		t.Fatalf("pg_notify count = %d, want 1", len(notifyPayloads))
	}
	var notified realtime.BroadcastMessage
	if err := json.Unmarshal([]byte(notifyPayloads[0]), &notified); err != nil {
		t.Fatalf("unmarshal pg_notify payload: %v", err)
	}
	if notified.NotifyAdmins {
		t.Fatal("NotifyAdmins must be false for change-orders")
	}

	// 2. NotifyRealtime broadcasts directly without touching domain_events/outbox_events.
	requesterCh, unsubReq := hub.Subscribe("requester-1", "user", false)
	defer unsubReq()
	qReviewerCh, unsubQRev := hub.Subscribe("q-reviewer-1", "user", false)
	defer unsubQRev()

	beforeInserts := drv.insertCount()
	if err := rec.NotifyRealtime(context.Background(), nil, "query_grant.relinquished", "query-grants/grant-uuid-1", "relinquished",
		Actor{Kind: "user", UserID: "requester-1", Username: "req"}); err != nil {
		t.Fatalf("NotifyRealtime failed: %v", err)
	}
	if drv.insertCount() != beforeInserts {
		t.Fatal("NotifyRealtime must not insert into domain_events or outbox_events")
	}
	for name, ch := range map[string]<-chan realtime.Delivery{
		"requester":  requesterCh,
		"q_reviewer": qReviewerCh,
	} {
		select {
		case d := <-ch:
			if d.Envelope.Type != "io.yearning.v4.query_grant.revoked" || d.Envelope.Aggregate != "grant-uuid-1" || d.Envelope.State != "relinquished" {
				t.Fatalf("%s unexpected envelope: %+v", name, d.Envelope)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive NotifyRealtime event", name)
		}
	}
	select {
	case d := <-adminCh:
		t.Fatalf("admin must not receive query_grant event: %+v", d)
	case <-time.After(30 * time.Millisecond):
	}

	// 3. When PGListenerActive is true, NotifyRealtime uses pg_notify only and skips direct hub.Publish
	// (preventing pre-commit delivery before the PG listener receives the committed notification).
	hub.SetPGListenerActive(true)
	if err := rec.NotifyRealtime(context.Background(), nil, "query_grant.relinquished", "query-grants/grant-uuid-2", "relinquished",
		Actor{Kind: "user", UserID: "requester-1", Username: "req"}); err != nil {
		t.Fatalf("NotifyRealtime with PGListenerActive failed: %v", err)
	}
	select {
	case d := <-requesterCh:
		t.Fatalf("direct hub.Publish should be skipped while PGListenerActive=true, got %+v", d)
	case <-time.After(30 * time.Millisecond):
	}
	if len(drv.notifyPayloads()) != 3 {
		t.Fatalf("expected pg_notify to still fire while PGListenerActive=true, count=%d", len(drv.notifyPayloads()))
	}
}

func TestResolveHelpers(t *testing.T) {
	if got := aggregateFromSubject("query-sessions/abc-123"); got != "abc-123" {
		t.Fatalf("aggregateFromSubject = %q", got)
	}
	if got := aggregateFromSubject("flow:flow-1"); got != "flow-1" {
		t.Fatalf("aggregateFromSubject colon = %q", got)
	}
	if got := extractEventState(map[string]any{"state": "active", "to": "ignored"}); got != "active" {
		t.Fatalf("extractEventState state = %q", got)
	}
	if got := extractEventState(map[string]any{"to": "completed"}); got != "completed" {
		t.Fatalf("extractEventState to = %q", got)
	}
	if got := extractEventState(nil); got != "" {
		t.Fatalf("extractEventState nil = %q", got)
	}
}

type stubAuditDriver struct {
	mu        sync.Mutex
	queryRows map[string][]string
	notifies  []string
	inserts   int
}

func newStubAuditDriver() *stubAuditDriver {
	return &stubAuditDriver{queryRows: make(map[string][]string)}
}

func (d *stubAuditDriver) Connect(context.Context) (driver.Conn, error) {
	return &stubAuditConn{parent: d}, nil
}
func (d *stubAuditDriver) Driver() driver.Driver            { return d }
func (d *stubAuditDriver) Open(string) (driver.Conn, error) { return &stubAuditConn{parent: d}, nil }

func (d *stubAuditDriver) notifyPayloads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.notifies))
	copy(out, d.notifies)
	return out
}

func (d *stubAuditDriver) insertCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inserts
}

type stubAuditConn struct {
	parent *stubAuditDriver
}

func (c *stubAuditConn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (c *stubAuditConn) Close() error                        { return nil }
func (c *stubAuditConn) Begin() (driver.Tx, error)           { return stubTx{}, nil }

func (c *stubAuditConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.parent.mu.Lock()
	defer c.parent.mu.Unlock()
	low := strings.ToLower(query)
	if strings.Contains(low, "pg_notify") && len(args) >= 2 {
		if s, ok := args[1].Value.(string); ok {
			c.parent.notifies = append(c.parent.notifies, s)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(low), "insert into") {
		c.parent.inserts++
	}
	return driver.RowsAffected(1), nil
}

func (c *stubAuditConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	low := strings.ToLower(query)
	if strings.Contains(low, "max(sequence)") {
		return &stubSingleValRows{cols: []string{"max"}, vals: [][]driver.Value{{nil}}}, nil
	}
	c.parent.mu.Lock()
	defer c.parent.mu.Unlock()
	for pattern, vals := range c.parent.queryRows {
		if strings.Contains(low, pattern) {
			rows := make([][]driver.Value, len(vals))
			for i, v := range vals {
				rows[i] = []driver.Value{v}
			}
			return &stubSingleValRows{cols: []string{"id"}, vals: rows}, nil
		}
	}
	return &stubSingleValRows{cols: []string{"id"}}, nil
}

type stubTx struct{}

func (stubTx) Commit() error   { return nil }
func (stubTx) Rollback() error { return nil }

type stubSingleValRows struct {
	cols []string
	vals [][]driver.Value
	idx  int
}

func (r *stubSingleValRows) Columns() []string { return r.cols }
func (r *stubSingleValRows) Close() error      { return nil }
func (r *stubSingleValRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.idx])
	r.idx++
	return nil
}
