package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
)

type cancelStubState struct {
	mu                    sync.Mutex
	attemptID             string
	orderID               string
	stageID               string
	sourceOrderID         string
	rollbackEvID          string
	actorID               string
	actorName             string
	attemptState          string
	orderState            string
	orderTicketType       string
	failAttemptWrite      bool
	failAttemptErr        error
	attemptUpdateAttempts int
	committedCount        int
	execs                 []string
}

var (
	cancelStubMu       sync.Mutex
	cancelStubRegistry = map[string]*cancelStubState{}
	cancelStubOnce     sync.Once
)

func openCancelStubServices(t *testing.T, st *cancelStubState) *Services {
	t.Helper()
	cancelStubOnce.Do(func() {
		sql.Register("exec_cancel_unit_stub", cancelStubDriver{})
	})
	dsn := "cancel-" + uuid.NewString()
	cancelStubMu.Lock()
	cancelStubRegistry[dsn] = st
	cancelStubMu.Unlock()
	t.Cleanup(func() {
		cancelStubMu.Lock()
		delete(cancelStubRegistry, dsn)
		cancelStubMu.Unlock()
	})

	sqlDB, err := sql.Open("exec_cancel_unit_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	gdb, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}

	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"cancel-unit","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"id-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"cur-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(1), makeKey(2), makeKey(3), makeKey(4))
	if err := os.WriteFile(keyPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(gdb, crypter, rec)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type cancelStubDriver struct{}

func (cancelStubDriver) Open(dsn string) (driver.Conn, error) {
	cancelStubMu.Lock()
	st := cancelStubRegistry[dsn]
	cancelStubMu.Unlock()
	if st == nil {
		return nil, errors.New("unknown cancel stub dsn")
	}
	return &cancelStubConn{state: st}, nil
}

type cancelStubConn struct{ state *cancelStubState }
type cancelStubTx struct{ state *cancelStubState }

func (c *cancelStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *cancelStubConn) Close() error              { return nil }
func (c *cancelStubConn) Begin() (driver.Tx, error) { return &cancelStubTx{state: c.state}, nil }
func (c *cancelStubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &cancelStubTx{state: c.state}, nil
}

func (tx *cancelStubTx) Commit() error {
	tx.state.mu.Lock()
	tx.state.committedCount++
	tx.state.mu.Unlock()
	return nil
}

func (tx *cancelStubTx) Rollback() error { return nil }

func (c *cancelStubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	isAttemptUpdate := strings.Contains(query, `UPDATE "execution_attempts"`)
	c.state.mu.Lock()
	c.state.execs = append(c.state.execs, query)
	if isAttemptUpdate {
		c.state.attemptUpdateAttempts++
	}
	failWrite := c.state.failAttemptWrite
	failErr := c.state.failAttemptErr
	c.state.mu.Unlock()
	if isAttemptUpdate && failErr != nil {
		return nil, failErr
	}
	if isAttemptUpdate && failWrite {
		return nil, errors.New("simulated execution_attempts write failure")
	}
	return driver.RowsAffected(1), nil
}

func (c *cancelStubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	st := c.state
	attState := st.attemptState
	ordState := st.orderState
	ticketType := st.orderTicketType
	st.mu.Unlock()

	now := time.Now().UTC()
	switch {
	case strings.Contains(query, `FROM "execution_attempts"`):
		return &cancelStubRows{
			cols: []string{
				"id", "order_id", "stage_id", "initiated_by_user_id_snapshot",
				"initiated_by_username_snapshot", "execution_kind", "state",
				"send_boundary", "fence_token", "version", "created_at",
			},
			rows: [][]driver.Value{{
				st.attemptID, st.orderID, st.stageID, st.actorID,
				st.actorName, string(domain.KindDML), attState,
				string(domain.BoundaryNotStarted), int64(1), int64(1), now,
			}},
		}, nil
	case strings.Contains(query, "from order_execution_actors"):
		return &cancelStubRows{
			cols: []string{"count"},
			rows: [][]driver.Value{{int64(1)}},
		}, nil
	case strings.Contains(query, "from change_orders"):
		return &cancelStubRows{
			cols: []string{
				"id", "state", "current_stage_position", "sql_hmac",
				"manually_verified", "version", "terminal_at",
				"ticket_type", "source_order_id", "rollback_evidence_id",
			},
			rows: [][]driver.Value{{
				st.orderID, ordState, int64(1), "abc",
				false, int64(1), nil,
				ticketType, st.sourceOrderID, st.rollbackEvID,
			}},
		}, nil
	case strings.Contains(query, "from order_stages"):
		return &cancelStubRows{
			cols: []string{
				"stage_id", "order_id", "position", "datasource_id",
				"stage_review_snapshot_id", "review_snapshot_hash",
				"datasource_identity_fingerprint", "state", "version",
			},
			rows: [][]driver.Value{{
				st.stageID, st.orderID, int64(1), uuid.NewString(),
				uuid.NewString(), "hash", "fp", "running", int64(1),
			}},
		}, nil
	default:
		return &cancelStubRows{cols: []string{}}, nil
	}
}

type cancelStubRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *cancelStubRows) Columns() []string { return r.cols }
func (r *cancelStubRows) Close() error      { return nil }
func (r *cancelStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func TestCancelInvokesRunnerOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	st := &cancelStubState{
		attemptID:        uuid.NewString(),
		orderID:          uuid.NewString(),
		stageID:          uuid.NewString(),
		actorID:          uuid.NewString(),
		actorName:        "executor",
		attemptState:     string(domain.AttemptRunning),
		orderState:       string(orderdomain.StateRunning),
		orderTicketType:  "standard",
		failAttemptWrite: true,
	}
	svc := openCancelStubServices(t, st)
	svc.cancelDelayOverride = 10 * time.Second

	called := 0
	var committedWhenCalled int
	runnerCancel := func(id string) bool {
		called++
		st.mu.Lock()
		committedWhenCalled = st.committedCount
		st.mu.Unlock()
		return id == st.attemptID
	}

	// Failure inside serializable transaction must NOT invoke runnerCancel.
	_, _, err := svc.Cancel(ctx, CancelInput{
		AttemptID:      st.attemptID,
		OrderID:        st.orderID,
		Actor:          Actor{UserID: st.actorID, Username: st.actorName},
		ExpectVersion:  1,
		IdempotencyKey: uuid.NewString(),
		Reason:         "abort on failure",
	}, runnerCancel)
	if err == nil {
		t.Fatal("expected Cancel to return error when transaction fails")
	}
	if called != 0 {
		t.Fatalf("runnerCancel must not be invoked when transaction rolls back, called=%d", called)
	}

	// Succeeding transaction invokes runnerCancel strictly after Commit().
	st.mu.Lock()
	st.failAttemptWrite = false
	beforeCommits := st.committedCount
	st.mu.Unlock()

	_, _, err = svc.Cancel(ctx, CancelInput{
		AttemptID:      st.attemptID,
		OrderID:        st.orderID,
		Actor:          Actor{UserID: st.actorID, Username: st.actorName},
		ExpectVersion:  1,
		IdempotencyKey: uuid.NewString(),
		Reason:         "operator cancel",
	}, runnerCancel)
	if err != nil {
		t.Fatalf("Cancel commit path: %v", err)
	}
	if called != 1 {
		t.Fatalf("runnerCancel must be called once after commit, got %d", called)
	}
	if committedWhenCalled <= beforeCommits {
		t.Fatalf("expected runnerCancel after transaction commit (before=%d whenCalled=%d)",
			beforeCommits, committedWhenCalled)
	}
}

func TestSweepCancelOneAndPostgresPreflightSyncRollback(t *testing.T) {
	ctx := context.Background()
	st := &cancelStubState{
		attemptID:       uuid.NewString(),
		orderID:         uuid.NewString(),
		stageID:         uuid.NewString(),
		sourceOrderID:   uuid.NewString(),
		rollbackEvID:    uuid.NewString(),
		actorID:         uuid.NewString(),
		actorName:       "executor",
		attemptState:    string(domain.AttemptCancelling),
		orderState:      string(orderdomain.StateRunning),
		orderTicketType: "rollback",
	}
	svc := openCancelStubServices(t, st)

	// 1. sweepCancelOne on cancelling not_started rollback attempt must sync source rollback status.
	if err := svc.sweepCancelOne(ctx, st.attemptID); err != nil {
		t.Fatalf("sweepCancelOne: %v", err)
	}
	assertRollbackSyncSQL := func(phase string, execs []string) {
		t.Helper()
		var syncedOrder, syncedEvidence bool
		for _, q := range execs {
			if strings.Contains(q, "update change_orders") && strings.Contains(q, "set rollback_status =") {
				syncedOrder = true
			}
			if strings.Contains(q, "update order_rollback_evidences") && strings.Contains(q, "set rollback_status =") {
				syncedEvidence = true
			}
		}
		if !syncedOrder || !syncedEvidence {
			t.Fatalf("%s must sync both change_orders.rollback_status and order_rollback_evidences.rollback_status (order=%v evidence=%v)",
				phase, syncedOrder, syncedEvidence)
		}
	}
	assertRollbackSyncSQL("sweepCancelOne", st.execs)

	// 2. failOrderAfterPostgresSearchPathPreflightTx on rollback order must also sync source rollback status.
	st.mu.Lock()
	st.execs = nil
	st.orderState = string(orderdomain.StateStageExecution)
	st.mu.Unlock()

	err := svc.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return svc.failOrderAfterPostgresSearchPathPreflightTx(ctx, tx, BeginInput{
			OrderID: st.orderID,
			Actor:   Actor{UserID: st.actorID, Username: st.actorName},
		}, "EXECUTION_PREFLIGHT_FAILED:POSTGRES_SEARCH_PATH_UNBOUND")
	})
	if err != nil {
		t.Fatalf("failOrderAfterPostgresSearchPathPreflightTx: %v", err)
	}
	assertRollbackSyncSQL("failOrderAfterPostgresSearchPathPreflightTx", st.execs)
}

func TestCancelReleasesIdempotencyOnNonDomainTxError(t *testing.T) {
	st := &cancelStubState{
		attemptID:       uuid.NewString(),
		orderID:         uuid.NewString(),
		stageID:         uuid.NewString(),
		actorID:         uuid.NewString(),
		actorName:       "executor",
		attemptState:    string(domain.AttemptRunning),
		orderState:      string(orderdomain.StateRunning),
		orderTicketType: "standard",
		failAttemptErr:  &pgconn.PgError{Code: "40001"},
	}
	svc := openCancelStubServices(t, st)
	svc.cancelDelayOverride = 10 * time.Second

	_, _, err := svc.Cancel(context.Background(), CancelInput{
		AttemptID:      st.attemptID,
		OrderID:        st.orderID,
		Actor:          Actor{UserID: st.actorID, Username: st.actorName},
		ExpectVersion:  1,
		IdempotencyKey: uuid.NewString(),
		Reason:         "serialization conflict exhaustion",
	}, nil)
	if err == nil {
		t.Fatal("expected error after SQLSTATE 40001 retry exhaustion")
	}
	var pgCheck *pgconn.PgError
	if !errors.As(err, &pgCheck) || pgCheck.Code != "40001" {
		t.Fatalf("expected wrapped *pgconn.PgError with SQLSTATE 40001, got %v", err)
	}
	st.mu.Lock()
	attempts := st.attemptUpdateAttempts
	execs := append([]string(nil), st.execs...)
	st.mu.Unlock()
	if attempts != 3 {
		t.Fatalf("expected txutil.Serializable to retry 3 times on 40001, got %d attempt(s)", attempts)
	}
	released := false
	for _, q := range execs {
		if strings.Contains(q, "delete from idempotency_records") && strings.Contains(q, "state = 'in_progress'") {
			released = true
		}
	}
	if !released {
		t.Fatalf("expected Cancel to release idempotency_records on 40001 retry exhaustion, execs=%v", execs)
	}
}
