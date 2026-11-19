//go:build integration

// b10_helpers_test.go B10 execution domain integration fixture: real PG/MySQL target databases,
// execution datasources, order pipeline to execution_pending, and TCP proxy fault injection
// (execution-failure-injection harness: test build only).
package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// b10Fixture layers execution service (with reachable fault hooks) and target DB coordinates on top of b9Fixture.
type b10Fixture struct {
	*b9Fixture
	exec   *execapp.Services
	runner *execapp.Runner
	pgPW   string // Target PG (separate DB in same instance) password
}

func newB10Fixture(t *testing.T) *b10Fixture {
	t.Helper()
	base := newB9Fixture(t)
	svc, err := execapp.NewServices(base.db, base.svc.Crypter, base.svc.Recorder)
	if err != nil {
		t.Fatal(err)
	}
	return &b10Fixture{b9Fixture: base, exec: svc, runner: execapp.NewRunner(svc)}
}

// b10ExecDatasource creates a real target DB datasource (engine=postgresql|mysql).
func (fx *b10Fixture) b10ExecDatasource(t *testing.T, name, engine, host string, port int,
	dbname, user, password, versionConstraint string) string {
	t.Helper()
	payload := map[string]any{
		"name": name, "engine": engine, "compatibility_mode": engine,
		"deployment_kind": "native", "host": host, "port": port,
		"database_name": dbname, "version_constraint": versionConstraint, "enabled": true,
		"credentials": []map[string]any{{
			"purpose": "execution", "username": user,
			"password": map[string]any{"value": password},
		}, {
			// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: when freezing search_path binding during PG stage review,
			// baseline/callable resolution uses review-purpose credentials, so fixture datasources must
			// also carry review credentials (legacy mapping bypass is no longer reached).
			"purpose": "review", "username": user,
			"password": map[string]any{"value": password},
		}},
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create exec datasource: %d %s", code, raw)
	}
	id := body["data"].(map[string]any)["id"].(string)
	if err := fx.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		strings.Repeat("cd", 32), id).Error; err != nil {
		t.Fatal(err)
	}
	if engine == "postgresql" {
		// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: PG stage object scope = execution identity
		// search_path (no more stage mapping bypass). Pin the fixture role database-level
		// search_path to the target schema so the review-frozen path covers exec_target.
		db := openTargetPG(t, host, strconv.Itoa(port), dbname, user, password)
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(fmt.Sprintf(
			`ALTER ROLE %s IN DATABASE %s SET search_path TO %s, public`,
			user, dbname, "exec_target")); err != nil {
			t.Fatalf("pin exec role search_path: %v", err)
		}
		// RCP-20261101 ephemeral precheck: search_path freeze chain requires measured identity
		// for this datasource (fixture default fake identity would cause binding probe to fail-closed -> coverage failure).
		installRealIdentityProbeForDS(t, fx, id, host, port, dbname, user, password)
	}
	return id
}

// b10ExecFlow creates an execution stage flow and grants owner submission access (schema mapping physical =
// target physical schema).
func (fx *b10Fixture) b10ExecFlow(t *testing.T, name, physical string, dsIDs ...string) string {
	t.Helper()
	stages := make([]map[string]any, 0, len(dsIDs))
	for i, dsID := range dsIDs {
		stages = append(stages, fx.b10Stage(i+1, dsID, physical))
	}
	flowID := fx.b9CreateFlow(t, name, stages)
	fx.b9CreateGrant(t, fx.ownerID, flowID)
	return flowID
}

// b10Stage builds stage payload (single step with two approvers + executor=exec1; RCP-20261031:
// stages no longer carry schema mappings; object scope is determined by datasource default DB / frozen search_path).
func (fx *b10Fixture) b10Stage(pos int, dsID, physical string) map[string]any {
	stepPayload := make([]map[string]any, 0, 1)
	as := []map[string]any{
		{"user_id": fx.reviewer1ID}, {"user_id": fx.reviewer2ID},
	}
	stepPayload = append(stepPayload, map[string]any{"position": 1, "actors": as})
	return map[string]any{
		"position": pos, "datasource_id": dsID,
		"approval_steps":   stepPayload,
		"execution_actors": []map[string]any{{"user_id": fx.executorID}},
	}
}

// b10OrderToExecutionPending runs draft -> ephemeral precheck -> submit -> final approval;
// returns (orderID, version@execution_pending).
func (fx *b10Fixture) b10OrderToExecutionPending(t *testing.T, flowID, sql string) (string, int64) {
	t.Helper()
	draftID, _, runID := fx.b10FrozenApprovedDraft(t, flowID, sql)
	code, body, raw := fx.b9Submit(t, flowID, draftID, runID, sql)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submit: %d %s", code, raw)
	}
	orderID := body["data"].(map[string]any)["id"].(string)
	// Approve to final step (rev1 suffices: single-step any-one).
	var ov struct {
		ID      string
		Version int64
	}
	if err := fx.db.Raw(`select id::text, version from change_orders where id = ?`, orderID).
		Scan(&ov).Error; err != nil {
		t.Fatal(err)
	}
	code, body, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions", map[string]any{
			"decision": "approve", "comment": "go",
		}, map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, ov.Version),
			"Idempotency-Key": "b10-approve-" + orderID[:8],
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("approve: %d %s", code, raw)
	}
	var state string
	var fresh int64
	if err := fx.db.Raw(`select state, version from change_orders where id = ?`, orderID).
		Row().Scan(&state, &fresh); err != nil {
		t.Fatal(err)
	}
	if state != "stage_execution_pending" {
		t.Fatalf("order state=%s want stage_execution_pending", state)
	}
	return orderID, fresh
}

// installRealIdentityProbeForDS installs a real identity probe for the specified datasource (wraps
// the probe: returns measured identity on match, falls back to original probe otherwise). Ephemeral
// precheck PG search_path freeze chain requires measured identity; default deterministic fake identity fails closed.
func installRealIdentityProbeForDS(
	t *testing.T, fx *b10Fixture, datasourceID, host string, port int, dbname, user, pass string,
) {
	t.Helper()
	adapter := database.NewPostgreSQLAdapter(database.Endpoint{
		Host: host, Port: port, DatabaseName: dbname, CompatibilityMod: "postgresql",
	}, database.AdapterOptions{})
	identity, err := adapter.ReadServerIdentity(context.Background(),
		database.Credential{Username: user, Password: pass}, nil)
	_ = adapter.Close()
	if err != nil {
		t.Fatalf("read real PostgreSQL identity: %v", err)
	}
	field := reflect.ValueOf(fx.svc).Elem().FieldByName("DatasourceIdentityProbe")
	if !field.IsValid() || !field.CanSet() {
		t.Fatal("application datasource identity probe seam unavailable")
	}
	previous := reflect.ValueOf(fx.svc).Elem().FieldByName("DatasourceIdentityProbe")
	replacement := reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
		row := args[3]
		if row.FieldByName("ID").String() != datasourceID {
			return previous.Call(args)
		}
		actual := *identity
		if databaseName := row.FieldByName("DatabaseName"); !databaseName.IsNil() {
			actual.DatabaseName = databaseName.Elem().String()
		}
		return []reflect.Value{reflect.ValueOf(actual), reflect.Zero(field.Type().Out(1))}
	})
	field.Set(replacement)
}

// execTargetFacts execution domain precheck fact source: real target DB existence awareness. b9FakeFacts
// answers "present" for any table -- CREATE new table cases would be blocked by validity check as
// "object already exists" (fail-closed is right, the fake catalog lied); this wrapper answers by
// querying pg_class and returns real columns when present (column mismatch also fails validity).
type execTargetFacts struct {
	b9FakeFacts
	db *sql.DB
}

func (f *execTargetFacts) TableMetadata(ctx context.Context, schema, table string) (map[string]any, error) {
	var present bool
	if err := f.db.QueryRowContext(ctx, `
		select exists (
			select 1 from pg_catalog.pg_class c
			join pg_catalog.pg_namespace n on n.oid = c.relnamespace
			where n.nspname = $1 and c.relname = $2
		)`, schema, table).Scan(&present); err != nil {
		return nil, err
	}
	if !present {
		return map[string]any{
			"schema": schema, "table": table, "existence": "absent",
			"columns": []map[string]any{}, "metadata_complete": true, "truncated": false, "estimate": false,
		}, nil
	}
	rows, err := f.db.QueryContext(ctx, `
		select column_name, data_type from information_schema.columns
		where table_schema = $1 and table_name = $2 order by ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := []map[string]any{}
	ordinal := 0
	for rows.Next() {
		ordinal++
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			return nil, err
		}
		columns = append(columns, map[string]any{
			"column_name": name, "ordinal": ordinal,
			"type_class": pgTypeClass(dataType), "type_status": "observed",
		})
	}
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": len(columns), "returned_column_count": len(columns),
		"columns": columns, "metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}

func (f *execTargetFacts) TableStatistics(ctx context.Context, schema, table string) (map[string]any, error) {
	var present bool
	if err := f.db.QueryRowContext(ctx, `
		select exists (
			select 1 from pg_catalog.pg_class c
			join pg_catalog.pg_namespace n on n.oid = c.relnamespace
			where n.nspname = $1 and c.relname = $2
		)`, schema, table).Scan(&present); err != nil {
		return nil, err
	}
	existence := "absent"
	if present {
		existence = "present"
	}
	return map[string]any{
		"existence": existence, "estimate": true, "metadata_complete": true, "truncated": false,
	}, nil
}

// pgTypeClass coarse-grained type classification (consumed by validity program via type_class).
func pgTypeClass(pgType string) string {
	switch {
	case strings.Contains(pgType, "int"):
		return "integer"
	case strings.Contains(pgType, "char"), strings.Contains(pgType, "text"):
		return "string"
	case strings.Contains(pgType, "time"), strings.Contains(pgType, "date"):
		return "datetime"
	case strings.Contains(pgType, "num"), strings.Contains(pgType, "float"):
		return "decimal"
	case pgType == "boolean":
		return "boolean"
	default:
		return "other"
	}
}

// b10FrozenApprovedDraft builds the historical review fixture required by RCP-20261025-TARGET-HASH-PARITY
// for execution-failure tests. SQL goes through real draft, ephemeral precheck (HTTP two-step flow),
// and ready terminal state; tests later induce execution failures on that SQL. Fact source = execTargetFacts
// (real target DB existence awareness; statements omit schema prefix and resolve to exec_target via role search_path).
func (fx *b10Fixture) b10FrozenApprovedDraft(t *testing.T, flowID, sql string) (string, int64, string) {
	t.Helper()
	target := mustOpenTargetPG(t, fx)
	t.Cleanup(func() { _ = target.Close() })
	facts := &execTargetFacts{db: target}
	fx.setAgentFacts(func(context.Context, string, reviewapp.FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
		return facts, func() {}, nil
	})
	defer fx.setAgentFacts(nil)
	return fx.b9ReadyDraft(t, flowID, sql, fx.b9CleanDial())
}

// b10Begin executor clicks execute (direct application-layer drive; fault hooks reachable).
func (fx *b10Fixture) b10Begin(t *testing.T, orderID string, version int64) string {
	t.Helper()
	view, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-" + orderID[:8] + "-" + fmt.Sprint(time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if view == nil || view.ID == "" {
		t.Fatal("begin view missing id")
	}
	return view.ID
}

// b10Drive drives execution task directly to terminal state.
func (fx *b10Fixture) b10Drive(t *testing.T, attemptID string) error {
	t.Helper()
	return fx.runner.RunAttempt(context.Background(), execapp.TaskInput{
		AttemptID: attemptID,
		OrderID:   fx.b10AttemptOrder(t, attemptID),
		StageID:   fx.b10AttemptStage(t, attemptID),
	})
}

func (fx *b10Fixture) b10AttemptOrder(t *testing.T, attemptID string) string {
	var v string
	if err := fx.db.Raw(`select order_id::text from execution_attempts where id = ?`, attemptID).
		Scan(&v).Error; err != nil || v == "" {
		t.Fatalf("attempt order: %v", err)
	}
	return v
}

func (fx *b10Fixture) b10AttemptStage(t *testing.T, attemptID string) string {
	var v string
	if err := fx.db.Raw(`select stage_id::text from execution_attempts where id = ?`, attemptID).
		Scan(&v).Error; err != nil || v == "" {
		t.Fatalf("attempt stage: %v", err)
	}
	return v
}

// b10OrderState reads change order state.
func (fx *b10Fixture) b10OrderState(t *testing.T, orderID string) string {
	var v string
	if err := fx.db.Raw(`select state from change_orders where id = ?`, orderID).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// b10AttemptRow reads Attempt snapshot.
func (fx *b10Fixture) b10AttemptRow(t *testing.T, attemptID string) (
	state, boundary string, fence int64, version int64) {
	err := fx.db.Raw(
		`select state, send_boundary, fence_token, version from execution_attempts where id = ?`,
		attemptID).Row().Scan(&state, &boundary, &fence, &version)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// stmtRow is the statement ledger projection.
type stmtRow struct {
	Ordinal  int
	State    string
	Affected sql.NullInt64
	Failure  sql.NullString
}

// b10Statements reads statement ledger ordered by ordinal.
func (fx *b10Fixture) b10Statements(t *testing.T, attemptID string) []stmtRow {
	rows, err := fx.db.Raw(`
		select ordinal, state, affected_row_count, failure_name
		from execution_statements where attempt_id = ? order by ordinal`, attemptID).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []stmtRow
	for rows.Next() {
		var r stmtRow
		if err := rows.Scan(&r.Ordinal, &r.State, &r.Affected, &r.Failure); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// b10TargetQueryPG queries target PG database directly (exec_target schema).
func (fx *b10Fixture) b10TargetQueryPG(t *testing.T, query string, args ...any) [][]string {
	t.Helper()
	pw := fx.b10PGPassword(t)
	db := openTargetPG(t, envOrB6("YEARNING_TEST_ADAPTER_PG_HOST", "localhost"), strconv.Itoa(b10PGPort()), "yearning_exec_target", b10PGUser(), pw)
	defer func() { _ = db.Close() }()
	return queryTarget(t, db, query, args...)
}

func b10PGPort() int {
	if raw := envOrB6("YEARNING_TEST_ADAPTER_PG_PORT", ""); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 {
			return p
		}
	}
	return 54330
}

func b10PGUser() string {
	return envOrB6("YEARNING_TEST_ADAPTER_PG_USER", "yearning")
}

func b10MySQLPort() int {
	if raw := envOrB6("YEARNING_TEST_ADAPTER_MYSQL_PORT", ""); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 {
			return p
		}
	}
	return 33306
}

func b10MySQLPassword(t *testing.T) string {
	t.Helper()
	if pw := envOrB6("YEARNING_TEST_ADAPTER_MYSQL_PASSWORD", ""); pw != "" {
		return pw
	}
	pw, err := dockerExecOutput("yearning-b10-mysql", "printenv", "MYSQL_ROOT_PASSWORD")
	if err != nil || pw == "" {
		t.Skip("mysql target container unavailable")
	}
	return pw
}

// b10PGPassword reads target PG password from container (same instance and credentials as metadata DB).
func (fx *b10Fixture) b10PGPassword(t *testing.T) string {
	t.Helper()
	if fx.pgPW != "" {
		return fx.pgPW
	}
	if pw := envOrB6("YEARNING_TEST_ADAPTER_PG_PASSWORD", ""); pw != "" {
		fx.pgPW = pw
		return pw
	}
	pw, err := dockerExecOutput("yearning-b2-pg", "printenv", "POSTGRES_PASSWORD")
	if err != nil || pw == "" {
		t.Skipf("target pg password unavailable: %v", err)
	}
	fx.pgPW = pw
	return pw
}

// b10ExpireLease rewinds Attempt lease to expired (simulating time elapsed after worker death).
func (fx *b10Fixture) b10ExpireLease(t *testing.T, attemptID string) {
	t.Helper()
	if err := fx.db.Exec(`update execution_attempts set lease_expires_at = now() - interval '1 minute'
		where id = ?`, attemptID).Error; err != nil {
		t.Fatal(err)
	}
}

// b10Version reads change order version.
func (fx *b10Fixture) b10Version(t *testing.T, orderID string) int64 {
	var v int64
	if err := fx.db.Raw(`select version from change_orders where id = ?`, orderID).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// b10AttemptVersion reads Attempt version.
func (fx *b10Fixture) b10AttemptVersion(t *testing.T, attemptID string) int64 {
	var v int64
	if err := fx.db.Raw(`select version from execution_attempts where id = ?`, attemptID).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// b10MySQLDatasource creates target MySQL datasource (native 8.0 -> gh-ost eligible).
func (fx *b10Fixture) b10MySQLDatasource(t *testing.T, name string) string {
	t.Helper()
	pw := b10MySQLPassword(t)
	pgDB := mustOpenTargetPG(t, fx)
	defer func() { _ = pgDB.Close() }()
	execTarget(t, pgDB, `create table if not exists exec_target.items (id int primary key, val int)`)
	mysqlDB := openTargetMySQL(t, fmt.Sprintf("root:%s@tcp(127.0.0.1:%d)/exec_target", pw, b10MySQLPort()))
	defer func() { _ = mysqlDB.Close() }()
	execTarget(t, mysqlDB, "DROP TABLE IF EXISTS `exec_target`.`items`")
	execTarget(t, mysqlDB, "CREATE TABLE `exec_target`.`items` (`id` INT PRIMARY KEY, `val` INT NOT NULL DEFAULT 0)")
	execTarget(t, mysqlDB, "INSERT INTO `exec_target`.`items` (`id`, `val`) VALUES (1, 10), (2, 20)")
	return fx.b10ExecDatasource(t, name, "mysql", "127.0.0.1", b10MySQLPort(),
		"exec_target", "root", pw, "8.0.x")
}

// b10OscPolicy writes execution settings osc policy (admin settings API belongs to a later package;
// test writes row directly with identical product read surface).
func (fx *b10Fixture) b10OscPolicy(t *testing.T, tool string) {
	t.Helper()
	if err := fx.db.Exec(`insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('execution', 1, ?::jsonb, 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings, updated_at = now()`,
		`{"restriction_enabled":false,"windows":[],"osc":{"tool":"`+tool+`","defaults":{"chunk_size":250}}}`,
		fx.adminUserID(t)).Error; err != nil {
		t.Fatal(err)
	}
}

// adminUserID reads builtin admin user ID (settings row audit field).
func (fx *b10Fixture) adminUserID(t *testing.T) string {
	var id string
	if err := fx.db.Raw(`select id::text from users where username = 'admin'`).Scan(&id).Error; err != nil || id == "" {
		t.Fatal("admin user missing")
	}
	return id
}

// b10GhOstEnv injects fake gh-ost binary coordinates and enables policy (default values in defaults).
func (fx *b10Fixture) b10GhOstEnv(t *testing.T, modeAlter string) {
	t.Helper()
	bin, sha := buildFakeGhostShared(t)
	fx.exec.OscBinaries = map[string]*execapp.OscBinaryIdentity{
		"gh-ost": {Version: "fake-gh-ost-v1", SHA256: sha, Path: bin},
	}
	fx.exec.Osc = newOscRunnerAdapter()
	fx.b10OscPolicy(t, "gh-ost")
}

// b10PtOscEnv injects fake pt-osc binary coordinates and enables policy.
func (fx *b10Fixture) b10PtOscEnv(t *testing.T, modeAlter string) {
	t.Helper()
	bin, sha := buildFakeGhostShared(t)
	fx.exec.OscBinaries = map[string]*execapp.OscBinaryIdentity{
		"pt-osc": {Version: "fake-pt-osc-v1", SHA256: sha, Path: bin},
	}
	fx.exec.Osc = newOscRunnerAdapter()
	fx.b10OscPolicy(t, "pt-osc")
}

// ---- TCP proxy (fault injection: connection-level RST) ----

// killProxy is a TCP proxy that cuts connections upon matching a byte marker (test build only;
// in-process equivalent of harness contract target_database_proxies=toxiproxy_or_equivalent).
type killProxy struct {
	mu       sync.Mutex
	listener net.Listener
	target   string
	markers  []string
	fired    bool
	closed   bool
}

// startKillProxy starts proxy: during forwarding, once client->server byte stream contains any
// marker, both directions are immediately closed (simulating connection loss after socket write).
func startKillProxy(t *testing.T, target string, markers ...string) *killProxy {
	t.Helper()
	if target == "127.0.0.1:54330" {
		target = "127.0.0.1:" + strconv.Itoa(b10PGPort())
	} else if target == "127.0.0.1:33306" {
		target = "127.0.0.1:" + strconv.Itoa(b10MySQLPort())
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &killProxy{listener: l, target: target, markers: markers}
	go p.acceptLoop()
	t.Cleanup(func() { _ = l.Close() })
	return p
}

func (p *killProxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

func (p *killProxy) acceptLoop() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.serve(conn)
	}
}

func (p *killProxy) serve(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	go p.copyWatch(client, server)
	go p.copyWatch(server, client)
}

// copyWatch copies one way while sniffing for markers; closes both connections upon match.
func (p *killProxy) copyWatch(dst, src net.Conn) {
	defer func() { _ = src.Close() }()
	buf := make([]byte, 16*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if p.shouldKill(chunk) {
				_ = dst.Close()
				_ = src.Close()
				return
			}
			if _, werr := dst.Write(chunk); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *killProxy) shouldKill(chunk []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fired || len(p.markers) == 0 {
		return false
	}
	text := string(chunk)
	for _, m := range p.markers {
		if m != "" && strings.Contains(text, m) {
			p.fired = true
			return true
		}
	}
	return false
}

// killed reports whether the marker has been triggered.
func (p *killProxy) killed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fired
}
