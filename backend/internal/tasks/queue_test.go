package tasks

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type queueStubFx struct {
	pendingCount int64
	queryErr     error
	execErr      error
	queries      []string
	queryArgs    [][]driver.NamedValue
	execQueries  []string
}

func (f *queueStubFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *queueStubFx) Driver() driver.Driver                        { return queueStubDriver{} }

type queueStubDriver struct{}

func (queueStubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused open") }
func (f *queueStubFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused prepare")
}
func (f *queueStubFx) Close() error              { return nil }
func (f *queueStubFx) Begin() (driver.Tx, error) { return nil, errors.New("unused begin") }
func (f *queueStubFx) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	f.execQueries = append(f.execQueries, query)
	if f.execErr != nil {
		return nil, f.execErr
	}
	return driver.RowsAffected(1), nil
}
func (f *queueStubFx) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	f.queries = append(f.queries, query)
	f.queryArgs = append(f.queryArgs, args)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &queueCountRows{val: f.pendingCount}, nil
}

type queueCountRows struct {
	val  int64
	done bool
}

func (r *queueCountRows) Columns() []string { return []string{"count"} }
func (r *queueCountRows) Close() error      { return nil }
func (r *queueCountRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.val
	return nil
}

func openQueueStubDB(t *testing.T, fx *queueStubFx) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return db
}

func TestDefaultPendingMaximumAndOverride(t *testing.T) {
	if DefaultPendingMaximum != 50000 {
		t.Fatalf("DefaultPendingMaximum = %d, want 50000", DefaultPendingMaximum)
	}
	if PendingMaximum() != 50000 {
		t.Fatalf("PendingMaximum = %d, want 50000", PendingMaximum())
	}

	restoreOuter := SetPendingMaximumForTest(12)
	if PendingMaximum() != 12 {
		t.Fatalf("PendingMaximum after outer override = %d, want 12", PendingMaximum())
	}
	restoreInner := SetPendingMaximumForTest(3)
	if PendingMaximum() != 3 {
		t.Fatalf("PendingMaximum after inner override = %d, want 3", PendingMaximum())
	}
	restoreInner()
	if PendingMaximum() != 12 {
		t.Fatalf("PendingMaximum after inner restore = %d, want 12", PendingMaximum())
	}
	restoreOuter()
	if PendingMaximum() != DefaultPendingMaximum {
		t.Fatalf("PendingMaximum after outer restore = %d, want %d", PendingMaximum(), DefaultPendingMaximum)
	}
}

func TestCheckPendingCapacityAndEnqueueGuards(t *testing.T) {
	ctx := context.Background()
	if err := CheckPendingCapacityWithLimit(ctx, nil, 10); err == nil {
		t.Fatal("expected error for nil db")
	}
	if err := Enqueue(ctx, nil, &AsyncTaskModel{}); err == nil {
		t.Fatal("expected error for nil db in Enqueue")
	}

	db := openQueueStubDB(t, &queueStubFx{})
	if err := CheckPendingCapacityWithLimit(ctx, db, 0); err == nil {
		t.Fatal("expected error for non-positive limit")
	}
	if err := CheckPendingCapacityWithLimit(ctx, db, -5); err == nil {
		t.Fatal("expected error for negative limit")
	}
	if err := Enqueue(ctx, db, nil); err == nil {
		t.Fatal("expected error for nil task in Enqueue")
	}
}

func TestCheckPendingCapacityAndEnqueueBehavior(t *testing.T) {
	ctx := context.Background()

	t.Run("bounded query and enqueue succeeds below limit", func(t *testing.T) {
		defer SetPendingMaximumForTest(5)()
		fx := &queueStubFx{pendingCount: 4}
		db := openQueueStubDB(t, fx)
		now := time.Now().UTC()
		task := &AsyncTaskModel{
			ID:           "0198d9cc-0000-7000-8000-000000000001",
			Kind:         KindSQLGrouping,
			State:        "queued",
			Input:        []byte(`{}`),
			ProgressUnit: "statements",
			FenceToken:   1,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := Enqueue(ctx, db, task); err != nil {
			t.Fatalf("Enqueue unexpected error: %v", err)
		}
		if len(fx.queries) != 1 {
			t.Fatalf("expected 1 capacity query, got %d", len(fx.queries))
		}
		q := strings.ToLower(fx.queries[0])
		if !strings.Contains(q, "state in ('queued', 'running')") || !strings.Contains(q, "limit") {
			t.Fatalf("capacity query missing bounded partial-index predicate: %q", fx.queries[0])
		}
		if len(fx.queryArgs[0]) != 1 || fx.queryArgs[0][0].Value != int64(5) {
			t.Fatalf("expected limit arg 5, got %+v", fx.queryArgs[0])
		}
		if len(fx.execQueries) != 1 || !strings.Contains(strings.ToLower(fx.execQueries[0]), `insert into "async_tasks"`) {
			t.Fatalf("expected async_tasks insert, got %v", fx.execQueries)
		}
	})

	t.Run("rejects with ErrQueueFull at limit without inserting", func(t *testing.T) {
		defer SetPendingMaximumForTest(5)()
		fx := &queueStubFx{pendingCount: 5}
		db := openQueueStubDB(t, fx)
		now := time.Now().UTC()
		task := &AsyncTaskModel{
			ID:           "0198d9cc-0000-7000-8000-000000000002",
			Kind:         KindSQLGrouping,
			State:        "queued",
			Input:        []byte(`{}`),
			ProgressUnit: "statements",
			FenceToken:   1,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		err := Enqueue(ctx, db, task)
		if !errors.Is(err, ErrQueueFull) {
			t.Fatalf("Enqueue err = %v, want ErrQueueFull", err)
		}
		if len(fx.execQueries) != 0 {
			t.Fatalf("expected zero inserts when queue full, got %v", fx.execQueries)
		}
	})

	t.Run("propagates database error from capacity check", func(t *testing.T) {
		dbBoom := errors.New("db unavailable")
		fx := &queueStubFx{queryErr: dbBoom}
		db := openQueueStubDB(t, fx)
		err := CheckPendingCapacityWithLimit(ctx, db, 10)
		if !errors.Is(err, dbBoom) {
			t.Fatalf("expected dbBoom, got %v", err)
		}
	})
}
