// osc_topology_test.go verifies in-process gh-ost SSRF & replication topology preflight in execution/application:
//
//	(a) forbidden target IP -> OSC_TARGET_ADDRESS_FORBIDDEN (BoundaryNotStarted, 0 TCP connects)
//	(b) unregistered replica primary -> OSC_MASTER_NOT_REGISTERED + audit osc.master_not_registered (0 rogue master connects)
//	(c) forbidden replica primary IP (e.g. 169.254.169.254) -> OSC_MASTER_ADDRESS_FORBIDDEN
//	(d) stopped/broken replication -> OSC_REPLICATION_NOT_RUNNING
//	(e) replica status privilege/query error -> OSC_TOPOLOGY_CHECK_FAILED with privilege_error audit flag
//	(f) registered replica primary -> passes with pinned AssumeMasterHostname
//	(g) standalone primary -> passes with pinned target IP
package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/osc"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

type oscAuditRecord struct {
	vals []string
}

type oscTopoFx struct {
	targetHost      string
	targetPort      int
	registeredHosts []string
	auditInserts    []oscAuditRecord
}

func (f *oscTopoFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *oscTopoFx) Driver() driver.Driver                        { return oscTopoDriver{} }

type oscTopoDriver struct{}

func (oscTopoDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unsupported") }
func (f *oscTopoFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported")
}
func (f *oscTopoFx) Close() error              { return nil }
func (f *oscTopoFx) Begin() (driver.Tx, error) { return nil, errors.New("unsupported") }

func (f *oscTopoFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	lq := strings.ToLower(query)
	switch {
	case strings.Contains(lq, "from order_stages"):
		cols := []string{
			"stage_id", "order_id", "position", "datasource_id", "stage_review_snapshot_id",
			"review_snapshot_hash", "datasource_identity_fingerprint", "state", "version",
			"target_snapshot_ciphertext", "target_snapshot_nonce", "target_snapshot_key_id", "target_snapshot_algorithm",
		}
		row := []driver.Value{
			"stage-1", "order-1", int64(1), "ds-target", "snap-1",
			"hash-1", "fp-1", "executing", int64(1),
			[]byte("{}"), []byte("123456789012"), "k1", "AES-256-GCM",
		}
		return &oscTopoRows{cols: cols, rows: [][]driver.Value{row}}, nil

	case strings.Contains(lq, "select host from datasources"):
		rows := make([][]driver.Value, 0, len(f.registeredHosts))
		for _, h := range f.registeredHosts {
			rows = append(rows, []driver.Value{h})
		}
		return &oscTopoRows{cols: []string{"host"}, rows: rows}, nil

	case strings.Contains(lq, "from \"datasources\""):
		now := time.Now().UTC()
		cols := []string{
			"id", "name", "engine", "compatibility_mode", "deployment_kind",
			"host", "port", "database_name", "version_constraint", "enabled",
			"test_purpose", "identity_fingerprint", "metadata", "version",
			"created_by_user_id", "created_at", "updated_at",
		}
		row := []driver.Value{
			"ds-target", "mysql-target", "mysql", "mysql", "native",
			f.targetHost, int64(f.targetPort), "demo", "8.0", true,
			"execution", "fp-1", []byte("{}"), int64(1),
			"admin-1", now, now,
		}
		return &oscTopoRows{cols: cols, rows: [][]driver.Value{row}}, nil

	default:
		return nil, fmt.Errorf("oscTopoFx unexpected query: %s", query)
	}
}

func (f *oscTopoFx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lq := strings.ToLower(query)
	if strings.Contains(lq, "insert into") && strings.Contains(lq, "audit_events") {
		rec := oscAuditRecord{}
		for _, a := range args {
			switch v := a.Value.(type) {
			case string:
				rec.vals = append(rec.vals, v)
			case []byte:
				rec.vals = append(rec.vals, string(v))
			default:
				if v != nil {
					rec.vals = append(rec.vals, fmt.Sprint(v))
				}
			}
		}
		f.auditInserts = append(f.auditInserts, rec)
		return contestResult{rows: 1}, nil
	}
	return nil, fmt.Errorf("oscTopoFx unexpected exec: %s", query)
}

type oscTopoRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *oscTopoRows) Columns() []string { return r.cols }
func (r *oscTopoRows) Close() error      { return nil }
func (r *oscTopoRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.idx]
	if len(row) != len(dest) {
		return errors.New("oscTopoRows: width mismatch")
	}
	copy(dest, row)
	r.idx++
	return nil
}

func newOscTopoSvc(t *testing.T, fx *oscTopoFx) *Services {
	t.Helper()
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn: sqlDB, WithoutReturning: true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	rec, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatalf("auditlog recorder: %v", err)
	}
	return &Services{
		DB:       db,
		Recorder: rec,
		instance: "osc-topo-test",
	}
}

func (f *oscTopoFx) hasAuditContaining(needle string) bool {
	for _, rec := range f.auditInserts {
		for _, s := range rec.vals {
			if strings.Contains(s, needle) {
				return true
			}
		}
	}
	return false
}

func TestPreflightOscTopologyAndPinMatrix(t *testing.T) {
	t.Cleanup(addrguard.ResetSelfGuardForTest())
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	t.Cleanup(addrguard.SetAllowLoopbackDatasourceForTest(false))

	in := TaskInput{AttemptID: "attempt-1", OrderID: "order-1", StageID: "stage-1"}
	at := &persistence.ExecutionAttemptModel{ID: "attempt-1"}
	actor := Actor{UserID: "user-1", Username: "executor"}

	t.Run("(a) forbidden target IP fails with OSC_TARGET_ADDRESS_FORBIDDEN and zero connects", func(t *testing.T) {
		t.Cleanup(addrguard.SetAllowLoopbackDatasourceForTest(false))
		decoy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = decoy.Close() }()
		var accepts atomic.Int32
		go func() {
			for {
				c, aerr := decoy.Accept()
				if aerr != nil {
					return
				}
				accepts.Add(1)
				_ = c.Close()
			}
		}()
		_, pStr, _ := net.SplitHostPort(decoy.Addr().String())
		port, _ := strconv.Atoi(pStr)

		fx := &oscTopoFx{targetHost: "127.0.0.1", targetPort: port}
		svc := newOscTopoSvc(t, fx)
		_, _, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil {
			t.Fatalf("unexpected err: %v", perr)
		}
		if failFacts == nil || failFacts.FailureName != failureOSCTargetAddressForbidden || failFacts.Boundary != domain.BoundaryNotStarted {
			t.Fatalf("got failFacts=%+v; want OSC_TARGET_ADDRESS_FORBIDDEN + BoundaryNotStarted", failFacts)
		}
		if !fx.hasAuditContaining("osc.target_address_forbidden") {
			t.Fatalf("missing audit osc.target_address_forbidden in %+v", fx.auditInserts)
		}
		if accepts.Load() != 0 {
			t.Fatalf("decoy listener accepted %d connections; want 0", accepts.Load())
		}
	})

	t.Run("(b) unregistered replica primary fails with OSC_MASTER_NOT_REGISTERED and zero rogue connects", func(t *testing.T) {
		t.Cleanup(addrguard.SetAllowLoopbackDatasourceForTest(true))
		rogueMaster, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rogueMaster.Close() }()
		var rogueAccepts atomic.Int32
		go func() {
			for {
				c, aerr := rogueMaster.Accept()
				if aerr != nil {
					return
				}
				rogueAccepts.Add(1)
				_ = c.Close()
			}
		}()
		_, mPortStr, _ := net.SplitHostPort(rogueMaster.Addr().String())
		mPort, _ := strconv.Atoi(mPortStr)

		fx := &oscTopoFx{targetHost: "replica.db.internal", targetPort: 3306, registeredHosts: []string{"10.0.0.2"}}
		svc := newOscTopoSvc(t, fx)
		svc.OscResolver = func(_ context.Context, host string) ([]net.IP, error) {
			if host == "replica.db.internal" {
				return []net.IP{net.ParseIP("10.0.0.5")}, nil
			}
			return nil, errors.New("unexpected lookup")
		}
		svc.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return &database.MySQLReplicaStatus{
				IsReplica: true, ReplicationRunning: true, MasterHost: "127.0.0.1", MasterPort: mPort,
			}, nil
		}

		_, _, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil {
			t.Fatalf("unexpected err: %v", perr)
		}
		if failFacts == nil || failFacts.FailureName != failureOSCMasterNotRegistered || failFacts.Boundary != domain.BoundaryNotStarted {
			t.Fatalf("got failFacts=%+v; want OSC_MASTER_NOT_REGISTERED", failFacts)
		}
		if !fx.hasAuditContaining("osc.master_not_registered") {
			t.Fatalf("missing audit osc.master_not_registered")
		}
		if rogueAccepts.Load() != 0 {
			t.Fatalf("rogue master listener accepted %d connections; want 0", rogueAccepts.Load())
		}
	})

	t.Run("(c) forbidden master IP fails with OSC_MASTER_ADDRESS_FORBIDDEN", func(t *testing.T) {
		t.Cleanup(addrguard.SetAllowLoopbackDatasourceForTest(false))
		fx := &oscTopoFx{targetHost: "10.0.0.5", targetPort: 3306, registeredHosts: []string{"169.254.169.254"}}
		svc := newOscTopoSvc(t, fx)
		svc.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return &database.MySQLReplicaStatus{
				IsReplica: true, ReplicationRunning: true, MasterHost: "169.254.169.254", MasterPort: 3306,
			}, nil
		}
		_, _, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil {
			t.Fatalf("unexpected err: %v", perr)
		}
		if failFacts == nil || failFacts.FailureName != failureOSCMasterAddressForbidden {
			t.Fatalf("got %+v; want OSC_MASTER_ADDRESS_FORBIDDEN", failFacts)
		}
		if !fx.hasAuditContaining("osc.master_address_forbidden") {
			t.Fatalf("missing audit osc.master_address_forbidden")
		}
	})

	t.Run("(d) stopped replication fails with OSC_REPLICATION_NOT_RUNNING", func(t *testing.T) {
		fx := &oscTopoFx{targetHost: "10.0.0.5", targetPort: 3306}
		svc := newOscTopoSvc(t, fx)
		svc.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return &database.MySQLReplicaStatus{
				IsReplica: true, ReplicationRunning: false, MasterHost: "10.0.0.2", MasterPort: 3306,
			}, nil
		}
		_, _, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil {
			t.Fatalf("unexpected err: %v", perr)
		}
		if failFacts == nil || failFacts.FailureName != failureOSCReplicationNotRunning {
			t.Fatalf("got %+v; want OSC_REPLICATION_NOT_RUNNING", failFacts)
		}
	})

	t.Run("(e) privilege error fails with OSC_TOPOLOGY_CHECK_FAILED and privilege_error audit field", func(t *testing.T) {
		fx := &oscTopoFx{targetHost: "10.0.0.5", targetPort: 3306}
		svc := newOscTopoSvc(t, fx)
		svc.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return nil, database.ErrReplicaStatusPrivilege
		}
		_, _, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil {
			t.Fatalf("unexpected err: %v", perr)
		}
		if failFacts == nil || failFacts.FailureName != failureOSCTopologyCheckFailed {
			t.Fatalf("got %+v; want OSC_TOPOLOGY_CHECK_FAILED", failFacts)
		}
		if !fx.hasAuditContaining(`"privilege_error":true`) {
			t.Fatalf("expected privilege_error:true in audit inserts, got %+v", fx.auditInserts)
		}
	})

	t.Run("(f) registered replica primary passes and pins AssumeMasterHostname (including bracketed IPv6)", func(t *testing.T) {
		fx := &oscTopoFx{targetHost: "replica.db.internal", targetPort: 3306, registeredHosts: []string{"primary.db.internal"}}
		svc := newOscTopoSvc(t, fx)
		svc.OscResolver = func(_ context.Context, host string) ([]net.IP, error) {
			switch host {
			case "replica.db.internal":
				return []net.IP{net.ParseIP("10.0.0.5")}, nil
			case "primary.db.internal":
				return []net.IP{net.ParseIP("10.0.0.2")}, nil
			default:
				return nil, errors.New("unknown host")
			}
		}
		svc.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return &database.MySQLReplicaStatus{
				IsReplica: true, ReplicationRunning: true, MasterHost: "10.0.0.2", MasterPort: 3306,
			}, nil
		}
		pinned, assume, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil || failFacts != nil {
			t.Fatalf("got failFacts=%+v err=%v; want success", failFacts, perr)
		}
		if pinned != "10.0.0.5" || assume != "10.0.0.2:3306" {
			t.Fatalf("got pinned=%q assume=%q; want 10.0.0.5 / 10.0.0.2:3306", pinned, assume)
		}

		// Bracketed IPv6 registered datasource "[fd00::2]" also matches unbracketed Master_Host "fd00::2".
		fx6 := &oscTopoFx{targetHost: "fd00::5", targetPort: 3306, registeredHosts: []string{"[fd00::2]"}}
		svc6 := newOscTopoSvc(t, fx6)
		svc6.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return &database.MySQLReplicaStatus{
				IsReplica: true, ReplicationRunning: true, MasterHost: "fd00::2", MasterPort: 3306,
			}, nil
		}
		pinned6, assume6, failFacts6, perr6 := svc6.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr6 != nil || failFacts6 != nil {
			t.Fatalf("bracketed v6 registered primary: got failFacts=%+v err=%v; want success", failFacts6, perr6)
		}
		if pinned6 != "fd00::5" || assume6 != "[fd00::2]:3306" {
			t.Fatalf("got pinned6=%q assume6=%q; want fd00::5 / [fd00::2]:3306", pinned6, assume6)
		}
	})

	t.Run("(g) standalone primary passes with pinned self IP", func(t *testing.T) {
		fx := &oscTopoFx{targetHost: "primary.db.internal", targetPort: 3307}
		svc := newOscTopoSvc(t, fx)
		svc.OscResolver = func(_ context.Context, host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("10.0.0.9")}, nil
		}
		svc.OscReplicaStatusProbe = func(context.Context, *stageTarget) (*database.MySQLReplicaStatus, error) {
			return &database.MySQLReplicaStatus{IsReplica: false}, nil
		}
		pinned, assume, failFacts, perr := svc.preflightOscTopologyAndPin(context.Background(), in, at, osc.ToolGhOst, actor)
		if perr != nil || failFacts != nil {
			t.Fatalf("got failFacts=%+v err=%v; want success", failFacts, perr)
		}
		if pinned != "10.0.0.9" || assume != "10.0.0.9:3307" {
			t.Fatalf("got pinned=%q assume=%q; want 10.0.0.9 / 10.0.0.9:3307", pinned, assume)
		}
	})
}
