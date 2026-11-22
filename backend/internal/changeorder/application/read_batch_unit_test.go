package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
)

var (
	coReadStubOnce     sync.Once
	coReadStubMu       sync.Mutex
	coReadStubRegistry = map[string]*coReadStubState{}
)

type coReadStubState struct {
	mu      sync.Mutex
	queries []string
	args    [][]driver.NamedValue
	empty   bool
	single  bool
}

type coReadStubDriver struct{}

func (coReadStubDriver) Open(dsn string) (driver.Conn, error) {
	coReadStubMu.Lock()
	st := coReadStubRegistry[dsn]
	coReadStubMu.Unlock()
	if st == nil {
		return nil, errors.New("unknown co read stub dsn")
	}
	return &coReadStubConn{st: st}, nil
}

type coReadStubConn struct{ st *coReadStubState }

func (*coReadStubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (*coReadStubConn) Close() error                        { return nil }
func (*coReadStubConn) Begin() (driver.Tx, error)           { return &coReadStubTx{}, nil }

type coReadStubTx struct{}

func (*coReadStubTx) Commit() error   { return nil }
func (*coReadStubTx) Rollback() error { return nil }

func (c *coReadStubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.st.mu.Lock()
	c.st.queries = append(c.st.queries, query)
	c.st.args = append(c.st.args, append([]driver.NamedValue(nil), args...))
	c.st.mu.Unlock()

	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Minute)
	t2 := t0.Add(2 * time.Minute)
	schedAt := t0.Add(24 * time.Hour)
	srcID := "ord-0"

	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "select o.* from change_orders o"):
		if c.st.empty {
			return &coReadStubRows{cols: changeOrderCols()}, nil
		}
		return &coReadStubRows{
			cols: changeOrderCols(),
			data: [][]driver.Value{
				{"ord-1", "YR-20261121-000001", "u-1", "alice", "Alice", "DDL order", "desc", "completed", int64(1), "h1", "sh1", false, nil, nil, true, "normal", nil, nil, "available", int64(2), t2, t2},
				{"ord-2", "YR-20261121-000002", "u-1", "alice", "Alice", "Scheduled RB", "desc", "scheduled", int64(1), "h2", "sh2", false, nil, &schedAt, false, "rollback", srcID, "ev-src", "not_requested", int64(1), t1, nil},
				{"ord-3", "YR-20261121-000003", "u-2", "bob", "Bob", "PG DML", "desc", "submitted", int64(1), "h3", "sh3", false, nil, nil, true, "normal", nil, nil, "pending", int64(1), t0, nil},
			},
		}, nil
	case strings.Contains(lower, `from "change_orders"`):
		if c.st.empty {
			return &coReadStubRows{cols: changeOrderCols()}, nil
		}
		return &coReadStubRows{
			cols: changeOrderCols(),
			data: [][]driver.Value{
				{"ord-1", "YR-20261121-000001", "u-1", "alice", "Alice", "DDL order", "desc", "completed", int64(1), "h1", "sh1", false, nil, nil, true, "normal", nil, nil, "available", int64(2), t2, t2},
			},
		}, nil
	case strings.Contains(lower, `from "order_stages"`):
		rows := [][]driver.Value{
			{"stg-1", "ord-1", int64(1), "ds-mysql", "prod-mysql", "mysql", "fp1", "snap-1", "rsh1", true, []byte("c"), []byte("n"), "k", "aes-256-gcm", "succeeded", int64(1), t2, t2},
			{"stg-2", "ord-2", int64(1), "ds-mysql", "prod-mysql", "mysql", "fp1", "snap-2", "rsh2", true, []byte("c"), []byte("n"), "k", "aes-256-gcm", "scheduled", int64(1), t1, nil},
		}
		if c.st.single {
			rows = rows[:1]
		}
		return &coReadStubRows{
			cols: []string{
				"id", "order_id", "position", "datasource_id", "datasource_name_snapshot",
				"datasource_engine_snapshot", "datasource_identity_fingerprint", "stage_review_snapshot_id",
				"review_snapshot_hash", "review_gate_passed", "target_snapshot_ciphertext",
				"target_snapshot_nonce", "target_snapshot_key_id", "target_snapshot_algorithm",
				"state", "version", "activated_at", "terminal_at",
			},
			data: rows,
		}, nil
	case strings.Contains(lower, `from "order_approval_steps"`):
		return &coReadStubRows{
			cols: []string{"id", "stage_id", "position", "state", "decided_at"},
			data: [][]driver.Value{
				{"step-1", "stg-1", int64(1), "approved", t2},
			},
		}, nil
	case strings.Contains(lower, "from order_approval_actors"):
		return &coReadStubRows{
			cols: []string{"step_id", "reviewer_user_id_snapshot", "username_snapshot", "display_name_snapshot"},
			data: [][]driver.Value{
				{"step-1", "u-rev", "carol", "Carol"},
			},
		}, nil
	case strings.Contains(lower, "from order_execution_actors"):
		return &coReadStubRows{
			cols: []string{
				"stage_id", "executor_user_id_snapshot", "username_snapshot", "display_name_snapshot",
				"is_builtin_admin_snapshot", "user_version_snapshot", "user_created_at_snapshot", "user_updated_at_snapshot",
			},
			data: [][]driver.Value{
				{"stg-1", "u-exec", "dave", "Dave", false, int64(1), t0, t0},
			},
		}, nil
	case strings.Contains(lower, "from order_sql_payloads"):
		return &coReadStubRows{
			cols: []string{"order_id"},
			data: [][]driver.Value{{"ord-1"}, {"ord-2"}},
		}, nil
	case strings.Contains(lower, "select id::text as id, display_number from change_orders"):
		return &coReadStubRows{
			cols: []string{"id", "display_number"},
			data: [][]driver.Value{{srcID, "YR-20261120-000099"}},
		}, nil
	case strings.Contains(lower, "from order_rollback_evidences"):
		return &coReadStubRows{
			cols: []string{"order_id", "id", "rollback_status", "unsupported_reason", "conflict_reason"},
			data: [][]driver.Value{{"ord-1", "ev-1", "available", nil, nil}},
		}, nil
	case strings.Contains(lower, "where source_order_id in"):
		return &coReadStubRows{
			cols: []string{"id", "source_order_id", "display_number", "state", "submitted_at"},
			data: [][]driver.Value{{"ord-rb-child", "ord-1", "YR-20261121-000010", "completed", t2}},
		}, nil
	case strings.Contains(lower, "select exists") && strings.Contains(lower, "stage_review_snapshots"):
		return &coReadStubRows{
			cols: []string{"exists"},
			data: [][]driver.Value{{true}},
		}, nil
	case strings.Contains(lower, "from stage_review_snapshots"):
		return &coReadStubRows{
			cols: []string{"id"},
			data: [][]driver.Value{{"snap-1"}},
		}, nil
	case strings.Contains(lower, "from system_settings"):
		return &coReadStubRows{
			cols: []string{"count"},
			data: [][]driver.Value{{int64(1)}},
		}, nil
	case strings.Contains(lower, "from execution_schedules"):
		return &coReadStubRows{
			cols: []string{"order_id", "id", "stage_id", "scheduled_for"},
			data: [][]driver.Value{{"ord-2", "sched-2", "stg-2", schedAt}},
		}, nil
	case strings.Contains(lower, "from execution_attempts"):
		return &coReadStubRows{
			cols: []string{"order_id", "id"},
			data: [][]driver.Value{{"ord-1", "att-1"}},
		}, nil
	default:
		return &coReadStubRows{cols: []string{}}, nil
	}
}

func changeOrderCols() []string {
	return []string{
		"id", "display_number", "submitter_user_id_snapshot", "submitter_username_snapshot",
		"submitter_display_name_snapshot", "title", "description", "state", "current_stage_position",
		"sql_hmac", "snapshot_hash", "manually_verified", "legacy_snapshot_completeness",
		"requested_scheduled_for", "rollback_enabled", "ticket_type", "source_order_id",
		"rollback_evidence_id", "rollback_status", "version", "submitted_at", "terminal_at",
	}
}

type coReadStubRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (r *coReadStubRows) Columns() []string { return r.cols }
func (r *coReadStubRows) Close() error      { return nil }
func (r *coReadStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func newCOReadStubDB(t *testing.T, st *coReadStubState) *gorm.DB {
	t.Helper()
	coReadStubOnce.Do(func() {
		sql.Register("changeorder_read_stub", coReadStubDriver{})
	})
	dsn := fmt.Sprintf("co-read-%d", time.Now().UnixNano())
	coReadStubMu.Lock()
	coReadStubRegistry[dsn] = st
	coReadStubMu.Unlock()
	t.Cleanup(func() {
		coReadStubMu.Lock()
		delete(coReadStubRegistry, dsn)
		coReadStubMu.Unlock()
	})
	sqlDB, err := sql.Open("changeorder_read_stub", dsn)
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

func TestListOrdersBatchedConstantQueriesAndSemantics(t *testing.T) {
	st := &coReadStubState{}
	gdb := newCOReadStubDB(t, st)
	svc := &Services{DB: gdb}

	views, next, err := svc.ListOrders(context.Background(), Actor{UserID: "u-1"}, 10, "")
	if err != nil {
		t.Fatalf("ListOrders failed: %v", err)
	}
	if next != nil {
		t.Fatalf("expected nil next cursor, got %v", *next)
	}
	if len(views) != 3 {
		t.Fatalf("expected 3 views, got %d", len(views))
	}

	// Verify constant batched query count (13 queries total for 3 rich orders, zero N+1 loops)
	// and verify count(*) over sql_statements is never issued.
	st.mu.Lock()
	queries := append([]string(nil), st.queries...)
	st.mu.Unlock()
	if len(queries) != 13 {
		t.Fatalf("expected 13 batched queries for ListOrders page, got %d:\n%s", len(queries), strings.Join(queries, "\n"))
	}
	for _, q := range queries {
		lq := strings.ToLower(q)
		if strings.Contains(lq, "count(*) from sql_statements") {
			t.Fatalf("forbidden count(*) over sql_statements: %s", q)
		}
	}

	// Order 1: DDL MySQL order with child rollback order, evidence, and attempt.
	v1 := views[0]
	if !v1.HasSQL || !v1.HasDDL || !v1.OscChoiceAvailable {
		t.Fatalf("v1 flags mismatch: hasSQL=%v hasDDL=%v osc=%v", v1.HasSQL, v1.HasDDL, v1.OscChoiceAvailable)
	}
	if v1.LatestAttemptID == nil || *v1.LatestAttemptID != "att-1" {
		t.Fatalf("v1 LatestAttemptID mismatch: %v", v1.LatestAttemptID)
	}
	if v1.RollbackEvidenceID == nil || *v1.RollbackEvidenceID != "ev-1" {
		t.Fatalf("v1 RollbackEvidenceID mismatch: %v", v1.RollbackEvidenceID)
	}
	if len(v1.RollbackOrders) != 1 || v1.RollbackOrders[0].ID != "ord-rb-child" {
		t.Fatalf("v1 RollbackOrders mismatch: %+v", v1.RollbackOrders)
	}
	if len(v1.Stages) != 1 || len(v1.Stages[0].ApprovalSteps) != 1 || len(v1.Stages[0].ApprovalSteps[0].Actors) != 1 || len(v1.Stages[0].ExecutionActors) != 1 {
		t.Fatalf("v1 nested stage/step/actors mismatch: %+v", v1.Stages)
	}

	// Order 2: scheduled rollback order with source display number and active schedule.
	v2 := views[1]
	if v2.HasDDL || v2.OscChoiceAvailable {
		t.Fatalf("v2 should not have DDL/OSC: hasDDL=%v osc=%v", v2.HasDDL, v2.OscChoiceAvailable)
	}
	if v2.SourceOrderDisplayNumber == nil || *v2.SourceOrderDisplayNumber != "YR-20261120-000099" {
		t.Fatalf("v2 SourceOrderDisplayNumber mismatch: %v", v2.SourceOrderDisplayNumber)
	}
	if v2.ActiveSchedule == nil || v2.ActiveSchedule.ID != "sched-2" {
		t.Fatalf("v2 ActiveSchedule mismatch: %+v", v2.ActiveSchedule)
	}
	if v2.RollbackOrders != nil {
		t.Fatalf("v2 RollbackOrders should be nil when empty, got %+v", v2.RollbackOrders)
	}
	if len(v2.Stages) != 1 || v2.Stages[0].ApprovalSteps == nil || v2.Stages[0].ExecutionActors == nil {
		t.Fatalf("v2 empty nested slices should be non-nil: %+v", v2.Stages)
	}

	// Order 3: no stages, non-nil empty Stages slice, nil RollbackOrders.
	v3 := views[2]
	if v3.HasSQL || v3.Stages == nil || len(v3.Stages) != 0 || v3.RollbackOrders != nil {
		t.Fatalf("v3 defaults mismatch: %+v", v3)
	}
}

func TestListOrdersEmptyAndSingleBuildOrderView(t *testing.T) {
	emptyState := &coReadStubState{empty: true}
	emptyDB := newCOReadStubDB(t, emptyState)
	emptySvc := &Services{DB: emptyDB}

	views, next, err := emptySvc.ListOrders(context.Background(), Actor{UserID: "u-1"}, 10, "")
	if err != nil || next != nil || len(views) != 0 || views == nil {
		t.Fatalf("expected non-nil empty slice for empty ListOrders, got views=%#v next=%v err=%v", views, next, err)
	}
	if len(emptyState.queries) != 1 {
		t.Fatalf("empty ListOrders should run exactly 1 query, got %d", len(emptyState.queries))
	}

	if _, err := emptySvc.buildOrderView(context.Background(), emptyDB, "missing-id"); err == nil {
		t.Fatal("expected error for missing order in buildOrderView")
	}

	singleState := &coReadStubState{single: true}
	singleDB := newCOReadStubDB(t, singleState)
	singleSvc := &Services{DB: singleDB}
	singleView, err := singleSvc.buildOrderView(context.Background(), singleDB, "ord-1")
	if err != nil {
		t.Fatalf("buildOrderView failed: %v", err)
	}
	if !singleView.HasSQL || !singleView.HasDDL || !singleView.OscChoiceAvailable || singleView.ActiveSchedule != nil {
		t.Fatalf("unexpected single buildOrderView result: %+v", singleView)
	}
	// Since ord-1 has no source_order_id and is not scheduled, those two conditional batch queries are skipped.
	for _, q := range singleState.queries {
		if strings.Contains(strings.ToLower(q), "execution_schedules") {
			t.Fatalf("should skip execution_schedules when no order is scheduled: %s", q)
		}
	}
}

func TestHasDDLAndOscHelpersUseExists(t *testing.T) {
	st := &coReadStubState{}
	db := newCOReadStubDB(t, st)
	svc := &Services{DB: db}

	stages := []persistence.OrderStageModel{
		{DatasourceEngineSnapshot: "mysql", StageReviewSnapshotID: "snap-1"},
	}
	if !svc.hasDDLStatements(context.Background(), db, stages) {
		t.Fatal("expected hasDDLStatements to return true")
	}
	if !svc.isOscChoiceAvailable(context.Background(), db, stages) {
		t.Fatal("expected isOscChoiceAvailable to return true")
	}
	if svc.hasDDLStatements(context.Background(), db, nil) {
		t.Fatal("expected hasDDLStatements to return false for empty stages")
	}
	if svc.isOscChoiceAvailable(context.Background(), db, []persistence.OrderStageModel{{DatasourceEngineSnapshot: "postgresql", StageReviewSnapshotID: "snap-1"}}) {
		t.Fatal("expected isOscChoiceAvailable to return false for non-mysql stage")
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	for _, q := range st.queries {
		lq := strings.ToLower(q)
		if strings.Contains(lq, "count(*) from sql_statements") {
			t.Fatalf("helper used count(*) instead of EXISTS: %s", q)
		}
		if strings.Contains(lq, "sql_statements") && !strings.Contains(lq, "exists") {
			t.Fatalf("expected EXISTS query over sql_statements, got: %s", q)
		}
	}
}

func TestListOrdersFilterSQLAndEscaping(t *testing.T) {
	st := &coReadStubState{}
	gdb := newCOReadStubDB(t, st)
	svc := &Services{DB: gdb}

	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	toExcl := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	views, next, err := svc.ListOrders(context.Background(), Actor{UserID: "u-1"}, 10, "", OrderListFilter{
		State:                "stage_execution_pending",
		Q:                    `100%_\test`,
		Datasource:           "prod-mysql",
		SubmittedFrom:        &from,
		SubmittedToExclusive: &toExcl,
	})
	if err != nil {
		t.Fatalf("filtered ListOrders failed: %v", err)
	}
	if next != nil || len(views) != 3 {
		t.Fatalf("unexpected views/next: len=%d next=%v", len(views), next)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.queries) != 13 {
		t.Fatalf("expected 13 batched queries with filters, got %d", len(st.queries))
	}
	mainSQL := strings.ToLower(st.queries[0])
	for _, want := range []string{
		"o.submitter_user_id_snapshot =",
		"o.state =",
		`o.display_number ilike $5 escape '\'`,
		`coalesce(o.title, '') ilike $6 escape '\'`,
		"exists (select 1 from order_stages sg_ds where sg_ds.order_id = o.id and sg_ds.datasource_name_snapshot =",
		"o.submitted_at >=",
		"o.submitted_at <",
	} {
		if !strings.Contains(mainSQL, want) {
			t.Fatalf("filtered query missing clause %q in SQL:\n%s", want, st.queries[0])
		}
	}

	args := st.args[0]
	var vals []any
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	wantEscaped := `%100\%\_\\test%`
	foundEscaped := false
	foundDS := false
	foundState := false
	for _, v := range vals {
		switch s := v.(type) {
		case string:
			if s == wantEscaped {
				foundEscaped = true
			}
			if s == "prod-mysql" {
				foundDS = true
			}
			if s == "stage_execution_pending" {
				foundState = true
			}
		}
	}
	if !foundState || !foundEscaped || !foundDS {
		t.Fatalf("bound filter args missing expected values (state=%v q=%v ds=%v): %#v", foundState, foundEscaped, foundDS, vals)
	}
}

func TestListOrdersAfterCursorNamedBinding(t *testing.T) {
	st := &coReadStubState{}
	gdb := newCOReadStubDB(t, st)
	svc := &Services{DB: gdb}
	cursor := "2026-11-19T07:57:15.288558Z|3c97ed7e-4cc7-4bc9-b1fa-25ad1936b4d3"
	from := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	_, _, err := svc.ListOrders(context.Background(), Actor{UserID: "user-1"}, 20, cursor, OrderListFilter{
		State:         "stage_execution_pending",
		SubmittedFrom: &from,
	})
	if err != nil {
		t.Fatalf("ListOrders with after cursor failed: %v", err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.queries) == 0 {
		t.Fatal("expected SQL query recorded")
	}
	rawSQL := st.queries[0]
	if strings.Contains(rawSQL, "@") {
		t.Fatalf("expected all named parameters bound without literal @, got SQL:\n%s", rawSQL)
	}
	mainSQL := strings.ToLower(rawSQL)
	for _, want := range []string{
		"o.state = $4",
		"o.submitted_at >= $5",
		"(o.submitted_at, o.id) < (cast($6 as timestamptz), cast($7 as uuid))",
	} {
		if !strings.Contains(mainSQL, want) {
			t.Fatalf("cursor query missing %q in SQL:\n%s", want, rawSQL)
		}
	}
	var vals []any
	for _, a := range st.args[0] {
		vals = append(vals, a.Value)
	}
	foundTs, foundID := false, false
	for _, v := range vals {
		if s, ok := v.(string); ok {
			if s == "2026-11-19T07:57:15.288558Z" {
				foundTs = true
			}
			if s == "3c97ed7e-4cc7-4bc9-b1fa-25ad1936b4d3" {
				foundID = true
			}
		}
	}
	if !foundTs || !foundID {
		t.Fatalf("bound cursor args missing ts=%v id=%v: %#v", foundTs, foundID, vals)
	}
}
