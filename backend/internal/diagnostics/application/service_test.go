package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewprovider "github.com/cookieY/YearningX/backend/internal/review/provider"
)

func acquireDiagnosticsTestLock(tb testing.TB) {
	tb.Helper()
	for _, name := range []string{"/yearningx-itest-shared.lock", "/yearningx-b13-assistant-test.lock"} {
		path := os.TempDir() + name
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			tb.Fatalf("open lock %s: %v", name, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			tb.Fatalf("flock %s: %v", name, err)
		}
		file := f
		tb.Cleanup(func() {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
		})
	}
}

func ensureMigration000027(tb testing.TB, db *gorm.DB) {
	tb.Helper()
	var hasIDCol bool
	_ = db.Raw(`select exists(select 1 from information_schema.columns where table_schema = 'public' and table_name = 'datasource_diagnostics_configs' and column_name = 'id')`).Scan(&hasIDCol).Error
	if hasIDCol {
		return
	}
	for _, tbl := range []string{
		"sql_diagnostics_order_links",
		"sql_diagnostics_ai_reports",
		"sql_diagnostics_bucket_metrics",
		"sql_diagnostics_statements",
		"sql_diagnostics_snapshots",
		"diagnostics_instance_leases",
		"datasource_diagnostics_configs",
	} {
		_ = db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE").Error
	}
	migPath := filepath.Join("..", "..", "..", "..", "db", "migrations", "000027_sql_diagnostics.up.sql")
	sqlBytes, err := os.ReadFile(migPath)
	if err != nil {
		tb.Fatalf("read 000027 migration: %v", err)
	}
	sqlText := string(sqlBytes)
	var hasRoleCol bool
	_ = db.Raw(`select exists(select 1 from information_schema.columns where table_schema = 'public' and table_name = 'users' and column_name = 'role')`).Scan(&hasRoleCol).Error
	if hasRoleCol {
		idx := strings.Index(sqlText, "create table datasource_diagnostics_configs")
		if idx > 0 {
			sqlText = "begin;\n" + sqlText[idx:]
		}
	}
	if err := db.Exec(sqlText).Error; err != nil {
		tb.Fatalf("apply 000027 migration: %v", err)
	}
}

func openDiagnosticsTestDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	acquireDiagnosticsTestLock(tb)
	host := envOr("YEARNING_B13_TEST_DB_HOST", "127.0.0.1")
	port := envOr("YEARNING_B13_TEST_DB_PORT", "54332")
	user := envOr("YEARNING_B13_TEST_DB_USER", "yearning")
	pass := envOr("YEARNING_B13_TEST_DB_PASSWORD", os.Getenv("YEARNING_TEST_DATABASE_PASSWORD"))
	if pass == "" {
		tb.Skip("PostgreSQL test credentials not configured (set YEARNING_B13_TEST_DB_PASSWORD or YEARNING_TEST_DATABASE_PASSWORD)")
	}
	dbname := envOr("YEARNING_B13_TEST_DB_NAME", "yearning_b13_test")
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, dbname)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		tb.Skipf("PostgreSQL test database not reachable (%v)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		tb.Fatalf("db.DB(): %v", err)
	}
	tb.Cleanup(func() { _ = sqlDB.Close() })
	ensureMigration000027(tb, db)

	for _, tbl := range []string{
		"sql_diagnostics_order_links",
		"sql_diagnostics_ai_reports",
		"sql_diagnostics_bucket_metrics",
		"sql_diagnostics_statements",
		"sql_diagnostics_snapshots",
		"diagnostics_instance_leases",
		"datasource_diagnostics_configs",
		"order_stages",
		"change_orders",
		"permission_group_flow_grants",
		"permission_group_members",
		"permission_groups",
		"flow_stages",
		"flows",
		"rule_sets",
		"datasource_credentials",
		"datasources",
		"users",
	} {
		_ = db.Exec("TRUNCATE TABLE " + tbl + " CASCADE").Error
	}
	return db
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func makeKey32(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func newTestCrypter(tb testing.TB) *crypto.Crypter {
	tb.Helper()
	dir := tb.TempDir()
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b13-diagnostics-test","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"state-1","purpose":"oidc_state","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"}
	]}`, makeKey32(11), makeKey32(21), makeKey32(31), makeKey32(41), makeKey32(51))
	path := dir + "/keyring.json"
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		tb.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		tb.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		tb.Fatal(err)
	}
	return crypter
}

type fakeCollectorBackend struct {
	probeState    database.StatsProbeState
	probeErr      error
	snapshotRows  []database.NativeStatementStatRow
	snapshotErr   error
	templatesByFP map[string]string
	columns       []database.MetadataColumn
	indexes       []database.MetadataIndex
}

func (f *fakeCollectorBackend) Probe(_ context.Context, ds DatasourceRow) (*database.StatsProbeResult, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	st := f.probeState
	if st == "" {
		st = database.StatsProbeReady
	}
	return &database.StatsProbeResult{
		State:        st,
		EngineFamily: ds.CompatibilityMode,
	}, nil
}

func (f *fakeCollectorBackend) ReadSnapshot(_ context.Context, _ DatasourceRow, _ int) ([]database.NativeStatementStatRow, error) {
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	return f.snapshotRows, nil
}

func (f *fakeCollectorBackend) ReadTemplate(_ context.Context, _ DatasourceRow, fp, _ string) (string, error) {
	if f.templatesByFP != nil {
		if t, ok := f.templatesByFP[fp]; ok && t != "" {
			return t, nil
		}
	}
	return "", database.ErrTemplateUnavailable
}

func (f *fakeCollectorBackend) ListColumns(_ context.Context, _ DatasourceRow, schema, table string) ([]database.MetadataColumn, error) {
	if len(f.columns) > 0 {
		return f.columns, nil
	}
	return []database.MetadataColumn{
		{SchemaName: schema, TableName: table, ColumnName: "id", Ordinal: 1, DatabaseType: "bigint", Nullable: false},
		{SchemaName: schema, TableName: table, ColumnName: "customer_code", Ordinal: 2, DatabaseType: "varchar(64)", Nullable: false},
		{SchemaName: schema, TableName: table, ColumnName: "created_at", Ordinal: 3, DatabaseType: "timestamptz", Nullable: false},
	}, nil
}

func (f *fakeCollectorBackend) ListIndexes(_ context.Context, _ DatasourceRow, schema, table string) ([]database.MetadataIndex, error) {
	if len(f.indexes) > 0 {
		return f.indexes, nil
	}
	return []database.MetadataIndex{
		{SchemaName: schema, TableName: table, IndexName: "PRIMARY", Primary: true, ColumnNames: []string{"id"}},
	}, nil
}

func seedUserAndDatasource(tb testing.TB, db *gorm.DB, withReadOnlyCred bool) (userID string, dsID string, flowID string) {
	tb.Helper()
	userID = uuid.NewString()
	dsID = uuid.NewString()
	flowID = uuid.NewString()
	ruleSetID := uuid.NewString()
	groupID := uuid.NewString()
	uname := "diag_admin_" + userID[:8]

	if err := db.Exec(`
		insert into users (id, username, display_name, password_hash, is_builtin_admin, role, version, created_at, updated_at)
		values (?, ?, 'Diag Admin', 'argon2id$test', false, 'admin', 1, now(), now())`, userID, uname).Error; err != nil {
		tb.Fatalf("seed user: %v", err)
	}
	if err := db.Exec(`
		insert into datasources (
			id, name, engine, compatibility_mode, deployment_kind, host, port, database_name, version_constraint,
			identity_fingerprint, enabled, test_purpose, created_by_user_id, version, created_at, updated_at
		) values (?, ?, 'mysql', 'mysql', 'native', '10.0.0.10', 3306, 'trade_db', '8.0',
			repeat('1', 64), true, 'query', ?, 1, now(), now())`, dsID, "prod-mysql-"+dsID[:8], userID).Error; err != nil {
		tb.Fatalf("seed datasource: %v", err)
	}
	if withReadOnlyCred {
		if err := db.Exec(`
			insert into datasource_credentials (
				id, datasource_id, purpose, credential_ciphertext, credential_nonce,
				credential_key_id, credential_algorithm, enabled, created_at, updated_at
			) values (?, ?, 'query', decode('0102','hex'), decode('0102030405060708090a0b0c','hex'),
				'pay-1', 'aes-256-gcm', true, now(), now())`, uuid.NewString(), dsID).Error; err != nil {
			tb.Fatalf("seed credential: %v", err)
		}
	}
	if err := db.Exec(`
		insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, '{}'::jsonb, repeat('a', 64), 1, ?, now(), now())`, ruleSetID, "rules-"+ruleSetID[:8], userID).Error; err != nil {
		tb.Fatalf("seed rule_set: %v", err)
	}
	if err := db.Exec(`
		insert into flows (id, name, flow_type, status, rule_set_id, created_by_user_id, version, created_at, updated_at)
		values (?, ?, 'change_review', 'enabled', ?, ?, 1, now(), now())`, flowID, "flow-"+flowID[:8], ruleSetID, userID).Error; err != nil {
		tb.Fatalf("seed flow: %v", err)
	}
	if err := db.Exec(`
		insert into flow_stages (id, flow_id, position, datasource_id, schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 1, ?, '\x01', '\x02', 'pay-1', 'aes-256-gcm')`, uuid.NewString(), flowID, dsID).Error; err != nil {
		tb.Fatalf("seed flow_stage: %v", err)
	}
	if err := db.Exec(`
		insert into permission_groups (id, name, enabled, created_by_user_id, version, created_at, updated_at)
		values (?, ?, true, ?, 1, now(), now())`, groupID, "dba-group-"+groupID[:8], userID).Error; err != nil {
		tb.Fatalf("seed permission_group: %v", err)
	}
	if err := db.Exec(`
		insert into permission_group_members (permission_group_id, user_id)
		values (?, ?)`, groupID, userID).Error; err != nil {
		tb.Fatalf("seed group_member: %v", err)
	}
	if err := db.Exec(`
		insert into permission_group_flow_grants (permission_group_id, flow_id)
		values (?, ?)`, groupID, flowID).Error; err != nil {
		tb.Fatalf("seed group_flow: %v", err)
	}
	return userID, dsID, flowID
}

// TestConfigLifecycleCircuitBreakerAndRateLimit verifies configuration enable/disable, read-only credential validation,
// 30s manual snapshot rate limiting, 2x frequency degradation after 2 consecutive failures (degraded),
// and automatic circuit breaker pause after 5 consecutive failures (paused_on_failure -> 7007).
func TestConfigLifecycleCircuitBreakerAndRateLimit(t *testing.T) {
	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)

	// 1. Enabling diagnostics without a read-only credential must be rejected with 7005 DIAGNOSTICS_INSUFFICIENT_DB_PRIVILEGES.
	userID, dsNoRO, _ := seedUserAndDatasource(t, db, false)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}
	fake := &fakeCollectorBackend{probeState: database.StatsProbeReady}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	cfg0, err := svc.GetConfig(context.Background(), dsNoRO)
	if err != nil {
		t.Fatal(err)
	}
	if cfg0.Enabled || cfg0.State != "disabled" {
		t.Fatalf("expected default disabled config, got %+v", cfg0)
	}
	if _, err := svc.UpdateConfig(context.Background(), actor, dsNoRO, cfg0.Version, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	}); err == nil {
		t.Fatal("expected 7005 error when enabling diagnostics without read-only credential")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeDiagnosticsInsufficientDBPrivilege {
			t.Fatalf("expected 7005 DIAGNOSTICS_INSUFFICIENT_DB_PRIVILEGES, got %v", err)
		}
	}

	// 2. Enabling succeeds once a read-only credential is configured; test 30s manual snapshot rate limiting (7006).
	_, dsID, _ := seedUserAndDatasource(t, db, true)
	cfg1, err := svc.GetConfig(context.Background(), dsID)
	if err != nil {
		t.Fatal(err)
	}
	cfgEnabled, err := svc.UpdateConfig(context.Background(), actor, dsID, cfg1.Version, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	})
	if err != nil {
		t.Fatalf("UpdateConfig enable failed: %v", err)
	}
	if !cfgEnabled.Enabled || cfgEnabled.State != "active" || cfgEnabled.LastProbeState != "ready" {
		t.Fatalf("unexpected enabled config: %+v", cfgEnabled)
	}

	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "digest_orders_select",
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        100,
			TotalExecTimeMS:                   500.0,
			RowsExaminedOrSharedBlksReadTotal: 10000,
			RowsSentOrRowsReturnedTotal:       100,
			LockTimeMSOrTempBlksWrittenTotal:  12.5,
		},
	}
	if _, err := svc.TriggerManualSnapshot(context.Background(), actor, dsID); err != nil {
		t.Fatalf("first manual snapshot failed: %v", err)
	}
	if _, err := svc.TriggerManualSnapshot(context.Background(), actor, dsID); err == nil {
		t.Fatal("expected second manual snapshot within 30s to be rate-limited (7006)")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeDiagnosticsSnapshotRateLimited {
			t.Fatalf("expected 7006 DIAGNOSTICS_SNAPSHOT_RATE_LIMITED, got %v", err)
		}
	}

	// 3. 2 consecutive timeouts/failures -> degraded (effective_interval_seconds = 120); 5 consecutive -> paused_on_failure (7007).
	fake.snapshotErr = errors.New("statement timeout after 2000ms")
	baseTime := time.Now().UTC().Add(1 * time.Minute)
	for i := 1; i <= 5; i++ {
		_, snapErr := svc.CollectScheduledSnapshot(context.Background(), dsID, baseTime.Add(time.Duration(i)*time.Minute))
		if snapErr == nil {
			t.Fatalf("iteration %d expected failure", i)
		}
		curCfg, _ := svc.GetConfig(context.Background(), dsID)
		if i == 2 {
			if curCfg.State != "degraded" || curCfg.EffectiveIntervalSeconds != 120 {
				t.Fatalf("after 2 failures expected degraded & 120s, got state=%s effective=%d", curCfg.State, curCfg.EffectiveIntervalSeconds)
			}
		}
		if i == 5 {
			if curCfg.State != "paused_on_failure" || curCfg.EffectiveIntervalSeconds != 0 {
				t.Fatalf("after 5 failures expected paused_on_failure, got state=%s effective=%d", curCfg.State, curCfg.EffectiveIntervalSeconds)
			}
		}
	}

	// Subsequent collection attempts after circuit breaker trips must return 7007 DIAGNOSTICS_CIRCUIT_PAUSED.
	if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, baseTime.Add(10*time.Minute)); err == nil {
		t.Fatal("expected 7007 when paused_on_failure")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeDiagnosticsCircuitPaused {
			t.Fatalf("expected 7007 DIAGNOSTICS_CIRCUIT_PAUSED, got %v", err)
		}
	}
}

// TestMonotonicDeltaResetSixChartsAndOrderComparison verifies:
// 1. Initial snapshot cumulative_only, subsequent monotonic delta window_delta, and counter reset reset_or_gap (no negative deltas).
// 2. Overview 6 chart groups (load trend, Top 10, scatter, heatmap, regression ranking D4 minimum sample guard, and new high-load unobserved notice).
// 3. On-demand fetching and encryption of SQL templates in statement detail, 3 detail chart groups, and before/after order comparable window comparison.
func TestMonotonicDeltaResetSixChartsAndOrderComparison(t *testing.T) {
	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, _ := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	fake := &fakeCollectorBackend{
		probeState: database.StatsProbeReady,
		templatesByFP: map[string]string{
			"fp_hot_query": "SELECT `id`, `customer_code`, `created_at` FROM `orders` WHERE `customer_code` = ? ORDER BY `created_at` DESC LIMIT ?",
			"fp_new_query": "SELECT COUNT(*) FROM `orders` WHERE `created_at` >= ?",
		},
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	cfg0, _ := svc.GetConfig(context.Background(), dsID)
	_, err = svc.UpdateConfig(context.Background(), actor, dsID, cfg0.Version, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	})
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

	// Comparison window (t0..t0+4m): collect 4 times (producing 1 cumulative + 3 valid delta buckets, total delta_calls = 30, avg = 10ms).
	for i := 0; i < 4; i++ {
		fake.snapshotRows = []database.NativeStatementStatRow{
			{
				EngineFingerprint:                 "fp_hot_query",
				DatabaseOrSchema:                  "trade_db",
				CallsTotal:                        int64(100 + i*10),
				TotalExecTimeMS:                   float64(1000 + i*100),
				RowsExaminedOrSharedBlksReadTotal: int64(10000 + i*1000),
				RowsSentOrRowsReturnedTotal:       int64(100 + i*10),
				LockTimeMSOrTempBlksWrittenTotal:  float64(5 + i),
			},
		}
		if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("baseline snapshot %d failed: %v", i, err)
		}
	}

	// Current window (t0+4m..t0+7m): collect 3 times (producing 3 valid delta buckets; fp_hot_query avg latency regresses to 50ms, and new high-load fp_new_query appears).
	for i := 1; i <= 3; i++ {
		fake.snapshotRows = []database.NativeStatementStatRow{
			{
				EngineFingerprint:                 "fp_hot_query",
				DatabaseOrSchema:                  "trade_db",
				CallsTotal:                        int64(130 + i*10),
				TotalExecTimeMS:                   float64(1300 + i*500), // 10 calls taking 500ms each -> avg=50ms
				RowsExaminedOrSharedBlksReadTotal: int64(13000 + i*5000),
				RowsSentOrRowsReturnedTotal:       int64(130 + i*10),
				LockTimeMSOrTempBlksWrittenTotal:  float64(8 + i*2),
			},
			{
				EngineFingerprint:                 "fp_new_query",
				DatabaseOrSchema:                  "trade_db",
				CallsTotal:                        int64(i * 15),
				TotalExecTimeMS:                   float64(i * 300),
				RowsExaminedOrSharedBlksReadTotal: int64(i * 3000),
				RowsSentOrRowsReturnedTotal:       int64(i * 15),
				LockTimeMSOrTempBlksWrittenTotal:  float64(i),
			},
		}
		if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(time.Duration(3+i)*time.Minute)); err != nil {
			t.Fatalf("current window snapshot %d failed: %v", i, err)
		}
	}

	cmpFrom := t0
	cmpTo := t0.Add(3*time.Minute + 30*time.Second)
	curFrom := cmpTo
	curTo := t0.Add(6*time.Minute + 30*time.Second)

	overview, err := svc.GetOverview(context.Background(), dsID, OverviewQuery{
		From:        curFrom,
		To:          curTo,
		CompareFrom: &cmpFrom,
		CompareTo:   &cmpTo,
		SortBy:      "total_exec_time_ms",
		Limit:       50,
	})
	if err != nil {
		t.Fatalf("GetOverview failed: %v", err)
	}
	if overview.CoverageMode != "window_delta" {
		t.Fatalf("expected coverage_mode=window_delta, got %s", overview.CoverageMode)
	}
	if len(overview.LoadTrend) != 3 || len(overview.TopStatements) != 2 || len(overview.ScatterPoints) != 2 || len(overview.Heatmap) != 2 {
		t.Fatalf("unexpected chart counts: trend=%d top=%d scatter=%d heatmap=%d",
			len(overview.LoadTrend), len(overview.TopStatements), len(overview.ScatterPoints), len(overview.Heatmap))
	}
	if len(overview.Regressions) == 0 {
		t.Fatal("expected fp_hot_query in regressions")
	}
	reg := overview.Regressions[0]
	if reg.EngineFingerprint != "fp_hot_query" || reg.InsufficientSamples || reg.RegressionRatio == nil || *reg.RegressionRatio < 3.9 {
		t.Fatalf("expected valid regression for fp_hot_query (10ms -> 50ms), got %+v", reg)
	}
	if len(overview.NewHighLoad) != 1 || overview.NewHighLoad[0].EngineFingerprint != "fp_new_query" {
		t.Fatalf("expected fp_new_query in new_high_load, got %+v", overview.NewHighLoad)
	}
	if !strings.Contains(overview.NewHighLoad[0].UnobservedNote, "Top-N") {
		t.Fatalf("expected explicit unobserved_note on new_high_load item, got %q", overview.NewHighLoad[0].UnobservedNote)
	}

	// Verify routine snapshots do not fetch SQL templates (template_status == not_fetched).
	var hotStmtID string
	for _, st := range overview.Statements {
		if st.EngineFingerprint == "fp_hot_query" {
			hotStmtID = st.StatementID
			if st.TemplateStatus != "not_fetched" {
				t.Fatalf("routine snapshot must not fetch SQL template, got status=%s", st.TemplateStatus)
			}
		}
	}

	// Opening statement detail fetches the template on demand and persists it encrypted.
	detail, err := svc.GetStatementDetail(context.Background(), dsID, hotStmtID, t0, curTo)
	if err != nil {
		t.Fatalf("GetStatementDetail failed: %v", err)
	}
	if detail.TemplateStatus != "available" || detail.TemplateSQL == nil || !strings.Contains(*detail.TemplateSQL, "customer_code") {
		t.Fatalf("expected on-demand template SQL, got status=%s sql=%v", detail.TemplateStatus, detail.TemplateSQL)
	}
	if detail.YearningFingerprint == nil || *detail.YearningFingerprint == "" {
		t.Fatal("expected computed YearningFingerprint after template fetch")
	}

	// Simulate counter reset (CallsTotal drops from 160 to 5): must mark reset_detected = true and emit no negative deltas.
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_hot_query",
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        5,
			TotalExecTimeMS:                   25.0,
			RowsExaminedOrSharedBlksReadTotal: 50,
			RowsSentOrRowsReturnedTotal:       5,
			LockTimeMSOrTempBlksWrittenTotal:  0.5,
		},
	}
	resetSnap, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(7*time.Minute))
	if err != nil {
		t.Fatalf("reset snapshot failed: %v", err)
	}
	if !resetSnap.ResetDetected {
		t.Fatal("expected ResetDetected=true when counter decreased")
	}
	var resetBucket persistence.SQLDiagnosticsBucketMetricModel
	if err := db.Where("snapshot_id = ? and statement_id = ?", resetSnap.SnapshotID, hotStmtID).First(&resetBucket).Error; err != nil {
		t.Fatal(err)
	}
	if !resetBucket.ResetDetected || resetBucket.DeltaCalls < 0 || resetBucket.DeltaExecTimeMS < 0 || resetBucket.CoverageMode != "reset_or_gap" {
		t.Fatalf("expected non-negative reset bucket with reset_or_gap, got %+v", resetBucket)
	}
}

// TestDesensitizedAIDiagnosisAntiHallucinationAndOrderSubmission verifies:
// 1. In-memory per-turn desensitization preserves ? / ... / $1 placeholders, keeps real schema/table/column/index names out of the model summary, and zeroes memory via ZeroAndRelease after the request.
// 2. The anti-hallucination guard without an execution plan rejects wording that asserts a confirmed execution plan.
// 3. The 4-section report and candidate SQL are persisted with AES-256-GCM + HMAC and can be advanced to a change order via PrecheckOptimizationDraft + SubmitOptimizationOrder.
func TestDesensitizedAIDiagnosisAntiHallucinationAndOrderSubmission(t *testing.T) {
	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, flowID := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	fake := &fakeCollectorBackend{
		probeState: database.StatsProbeReady,
		templatesByFP: map[string]string{
			"fp_secret_table": "SELECT `id`, `customer_code` FROM `secret_payroll_orders` WHERE `customer_code` = ? AND `id` IN (...) ORDER BY `created_at` DESC",
		},
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	// 1. Verify single-turn desensitization preserves ? and ... and does not leak real table/column names.
	priv := privacy.NewTurnPrivacySession()
	dsRow, _ := svc.loadDatasourceRow(context.Background(), dsID)
	des, err := svc.DesensitizeStatementTemplate(
		context.Background(),
		priv,
		dsRow,
		"trade_db",
		fake.templatesByFP["fp_secret_table"],
	)
	if err != nil {
		t.Fatalf("DesensitizeStatementTemplate failed: %v", err)
	}
	if strings.Contains(des.AnonymizedSummary, "secret_payroll_orders") ||
		strings.Contains(des.AnonymizedSummary, "customer_code") ||
		strings.Contains(des.AnonymizedSummary, "trade_db") {
		t.Fatalf("outbound AI summary leaked real identifiers: %s", des.AnonymizedSummary)
	}
	if !strings.Contains(des.AnonymizedTemplate, "?") || !strings.Contains(des.AnonymizedTemplate, "...") {
		t.Fatalf("expected ? and ... placeholders preserved in anonymized template, got %s", des.AnonymizedTemplate)
	}
	priv.ZeroAndRelease()
	if !priv.IsReleased() {
		t.Fatal("expected TurnPrivacySession to be released")
	}

	// 2. Verify anti-hallucination gate rejects content asserting a "confirmed execution plan" without EXPLAIN evidence.
	if err := ValidateNoHallucinatedPlanFacts(
		[]domain.DiagnosticAIObservedFactView{{Metric: "Execution Plan", Value: "Execution plan confirmed full table scan", Window: "1h"}},
		nil,
	); err == nil {
		t.Fatal("expected ValidateNoHallucinatedPlanFacts to reject hallucinated plan fact")
	}

	// 3. Execute full AI diagnosis + precheck + change order submission loop.
	cfg0, _ := svc.GetConfig(context.Background(), dsID)
	_, _ = svc.UpdateConfig(context.Background(), actor, dsID, cfg0.Version, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	})
	t0 := time.Now().UTC().Add(-10 * time.Minute)
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_secret_table",
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        50,
			TotalExecTimeMS:                   2500.0,
			RowsExaminedOrSharedBlksReadTotal: 50000,
			RowsSentOrRowsReturnedTotal:       50,
			LockTimeMSOrTempBlksWrittenTotal:  20.0,
		},
	}
	_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, t0)
	fake.snapshotRows[0].CallsTotal = 120
	fake.snapshotRows[0].TotalExecTimeMS = 6000.0
	fake.snapshotRows[0].RowsExaminedOrSharedBlksReadTotal = 120000
	_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(1*time.Minute))

	var stmt persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_secret_table'", dsID).First(&stmt).Error; err != nil {
		t.Fatal(err)
	}

	// Without an active AI provider configured, DiagnoseStatementWithAI must fail with CodeAIProviderFailed (2011)
	// and never persist a fabricated deterministic fallback report.
	if _, err := svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmt.ID, t0.Add(-1*time.Minute), t0.Add(2*time.Minute)); err == nil {
		t.Fatal("expected DiagnoseStatementWithAI to fail when no AI provider is configured")
	} else {
		var diagErr *domain.Error
		if !errors.As(err, &diagErr) || diagErr.Code != domain.CodeAIProviderFailed {
			t.Fatalf("expected AI_PROVIDER_FAILED (2011), got %v", err)
		}
	}

	svc.Review = wireMockDiagLLMProvider(t, db, crypter, userID, "CREATE INDEX idx_secret_payroll_orders_cust ON secret_payroll_orders (customer_code)")

	reportView, err := svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmt.ID, t0.Add(-1*time.Minute), t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("DiagnoseStatementWithAI failed: %v", err)
	}
	if len(reportView.ObservedFacts) == 0 || len(reportView.RootCauseHypotheses) == 0 ||
		len(reportView.OptimizationRecommendations) == 0 || len(reportView.VerificationSteps) == 0 {
		t.Fatalf("expected 4-section structured AI report, got %+v", reportView)
	}
	if reportView.CandidateSQL == nil || !strings.Contains(*reportView.CandidateSQL, "CREATE INDEX") {
		t.Fatalf("expected candidate CREATE INDEX DDL, got %v", reportView.CandidateSQL)
	}

	// Verify AES-256-GCM ciphertext and HMAC of the candidate SQL.
	var storedReport persistence.SQLDiagnosticsAIReportModel
	if err := db.Where("id = ?", reportView.ReportID).First(&storedReport).Error; err != nil {
		t.Fatal(err)
	}
	if !svc.VerifyCandidateSQLHMAC(&storedReport, *reportView.CandidateSQL) {
		t.Fatal("candidate SQL HMAC verification failed")
	}

	// Advance to ephemeral precheck and change order submission.
	fakeRunID := uuid.NewString()
	fakeOrderID := uuid.NewString()
	svc.PrecheckRunner = func(_ context.Context, _ domain.Actor, fID, sqlText, _ string) (string, error) {
		if fID != flowID || !strings.Contains(sqlText, "CREATE INDEX") {
			return "", errors.New("unexpected precheck input")
		}
		return fakeRunID, nil
	}
	svc.OrderSubmitter = func(_ context.Context, _ domain.Actor, fID, schemaName, stmtKind, sqlText, runID, title, _, idemKey string) (string, string, string, error) {
		if fID != flowID || runID != fakeRunID || stmtKind != "ddl" || idemKey == "" {
			return "", "", "", errors.New("unexpected order submission input")
		}
		// Insert a mock change_orders row so ConfirmOrderLink succeeds.
		if err := db.Exec(`
			insert into change_orders (
				id, display_number, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
				title, description, state, sql_hmac, snapshot_hash, manually_verified, rollback_enabled,
				ticket_type, rollback_status, version, submitted_at, terminal_at
			) values (?, 'YR-20261107-000001', ?, 'diag_admin', 'Diag Admin',
				?, 'diag', 'completed', repeat('a', 64), repeat('b', 64), false, false,
				'normal', 'not_requested', 1, now(), now())`,
			fakeOrderID, userID, title).Error; err != nil {
			return "", "", "", err
		}
		return fakeOrderID, "YR-20261107-000001", "stage_approval_active", nil
	}

	precheckRes, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, reportView.ReportID, flowID, nil, "idem-precheck-1")
	if err != nil {
		t.Fatalf("PrecheckOptimizationDraft failed: %v", err)
	}
	if precheckRes["run_id"] != fakeRunID {
		t.Fatalf("expected run_id=%s, got %v", fakeRunID, precheckRes["run_id"])
	}

	submitRes, err := svc.SubmitOptimizationOrder(
		context.Background(),
		actor,
		dsID,
		reportView.ReportID,
		flowID,
		fakeRunID,
		"Create composite index on secret_payroll_orders for slow query optimization",
		"Derived from SQL performance diagnosis report",
		"idem-submit-1",
	)
	if err != nil {
		t.Fatalf("SubmitOptimizationOrder failed: %v", err)
	}
	if submitRes["order_id"] != fakeOrderID || submitRes["order_display_no"] != "YR-20261107-000001" {
		t.Fatalf("unexpected submit response: %+v", submitRes)
	}

	linkView, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, stmt.ID, fakeOrderID, "index_or_schema_change", nil)
	if err != nil {
		t.Fatalf("ConfirmOrderLink failed: %v", err)
	}
	if linkView.ChangeOrderID != fakeOrderID || linkView.RelationKind != "index_or_schema_change" {
		t.Fatalf("unexpected order link view: %+v", linkView)
	}
}

// BenchmarkCollectAndAggregateTop200 verifies the D5 performance budget: single Top-200 fingerprint snapshot delta calculation and overview aggregation << 500ms.
func BenchmarkCollectAndAggregateTop200(b *testing.B) {
	db := openDiagnosticsTestDB(b)
	crypter := newTestCrypter(b)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, _ := seedUserAndDatasource(b, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	rows := make([]database.NativeStatementStatRow, 200)
	for i := 0; i < 200; i++ {
		rows[i] = database.NativeStatementStatRow{
			EngineFingerprint:                 fmt.Sprintf("fp_bench_%03d", i),
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        int64(1000 + i*10),
			TotalExecTimeMS:                   float64(5000 + i*25),
			RowsExaminedOrSharedBlksReadTotal: int64(50000 + i*100),
			RowsSentOrRowsReturnedTotal:       int64(1000 + i*10),
			LockTimeMSOrTempBlksWrittenTotal:  float64(i),
		}
	}
	fake := &fakeCollectorBackend{
		probeState:   database.StatsProbeReady,
		snapshotRows: rows,
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	svc.Collector = fake

	cfg0, _ := svc.GetConfig(context.Background(), dsID)
	_, _ = svc.UpdateConfig(context.Background(), actor, dsID, cfg0.Version, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	})

	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range fake.snapshotRows {
			fake.snapshotRows[j].CallsTotal += 5
			fake.snapshotRows[j].TotalExecTimeMS += 15.0
		}
		_, err := svc.CollectScheduledSnapshot(context.Background(), dsID, base.Add(time.Duration(i)*time.Minute))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func wireMockDiagLLMProvider(
	t *testing.T,
	db *gorm.DB,
	crypter *crypto.Crypter,
	userID string,
	candidateSQL string,
) *reviewapp.Services {
	t.Helper()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rec := map[string]any{
			"kind":                  "index_ddl",
			"title":                 "Add composite index on filter predicates",
			"rationale":             "Lower per-call rows examined",
			"applicable_conditions": []string{"Confirm column selectivity"},
			"candidate_sql":         candidateSQL,
			"risk_notes":            []string{"Validate DDL lock impact via precheck"},
		}
		payload := map[string]any{
			"observed_facts": []map[string]any{
				{"metric": "Execution Calls & Total Time", "value": "calls=120, total_exec_time_ms=6000.000", "window": "window"},
			},
			"root_cause_hypotheses": []map[string]any{
				{
					"hypothesis":     "Filter predicate lacks a composite index (inference).",
					"confidence":     "medium",
					"evidence_basis": "calls=120, rows_examined=120000",
				},
			},
			"optimization_recommendations": []map[string]any{rec},
			"verification_steps":           []string{"Run AI Precheck and monitor after execution."},
		}
		content, _ := json.Marshal(payload)
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": string(content)}},
			},
		})
		_, _ = w.Write(resp)
	}))
	t.Cleanup(llmServer.Close)

	provID := uuid.NewString()
	provEnv, err := crypter.Seal(crypto.PurposePayload, []byte("sk-diag-test"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "ai_providers",
		PrimaryKey:   provID,
		Column:       "secret_ciphertext",
		Purpose:      "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Exec(`delete from ai_providers`).Error
	if err := db.Exec(`
		insert into ai_providers (
			id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at
		) values (?, ?, 'openai_compatible', ?, 'gpt-4o-mini', true,
			1, ?, ?, '{"temperature":0.1}'::jsonb,
			?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		provID, "diag-mock-"+provID[:8], llmServer.URL,
		reviewprovider.PrivacyContractHash, reviewprovider.OutputSchemaHash,
		provEnv.Ciphertext, provEnv.Nonce, provEnv.KeyID, userID).Error; err != nil {
		t.Fatalf("seed mock ai_provider failed: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from ai_providers where id = ?`, provID).Error
	})
	return &reviewapp.Services{DB: db, Crypter: crypter}
}

func TestDiagnoseStatementWithAI_GlobalBudgetEnforcementAndLedger(t *testing.T) {
	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, _ := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	fake := &fakeCollectorBackend{
		probeState: database.StatsProbeReady,
		templatesByFP: map[string]string{
			"fp_budget_stmt": "SELECT `id` FROM `budget_orders` WHERE `status` = ?",
		},
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	cfg0, _ := svc.GetConfig(context.Background(), dsID)
	_, _ = svc.UpdateConfig(context.Background(), actor, dsID, cfg0.Version, domain.UpdateDiagnosticsConfigInput{
		Enabled: true, IntervalSeconds: 60, TopN: 200,
	})
	t0 := time.Now().UTC().Add(-5 * time.Minute)
	fake.snapshotRows = []database.NativeStatementStatRow{
		{EngineFingerprint: "fp_budget_stmt", DatabaseOrSchema: "trade_db", CallsTotal: 10, TotalExecTimeMS: 100},
	}
	_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, t0)
	var stmt persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_budget_stmt'", dsID).First(&stmt).Error; err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var returnErr atomic.Bool
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if returnErr.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"observed_facts": []map[string]any{{"metric": "Calls", "value": "10", "window": "window"}},
			"root_cause_hypotheses": []map[string]any{
				{"hypothesis": "Status filter may scan rows.", "confidence": "medium", "evidence_basis": "calls=10"},
			},
			"optimization_recommendations": []map[string]any{
				{
					"kind":                  "index_ddl",
					"title":                 "Review index on status column",
					"rationale":             "Check whether filtering by status needs an index after inspecting selectivity.",
					"applicable_conditions": []string{"status filter selectivity is high"},
					"candidate_sql":         nil,
					"risk_notes":            []string{"Index maintenance adds write overhead"},
				},
			},
			"verification_steps": []string{"Verify index selectivity."},
		}
		content, _ := json.Marshal(payload)
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
			"usage":   map[string]any{"prompt_tokens": 150, "completion_tokens": 50},
		})
		_, _ = w.Write(resp)
	}))
	defer llmServer.Close()

	provID := uuid.NewString()
	provEnv, err := crypter.Seal(crypto.PurposePayload, []byte("sk-diag-budget"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "ai_providers", PrimaryKey: provID, Column: "secret_ciphertext", Purpose: "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Exec(`delete from ai_providers`).Error
	if err := db.Exec(`
		insert into ai_providers (
			id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at
		) values (?, ?, 'openai_compatible', ?, 'gpt-4o-mini', true,
			1, ?, ?, '{"temperature":0.1}'::jsonb,
			?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		provID, "diag-budget-"+provID[:8], llmServer.URL,
		reviewprovider.PrivacyContractHash, reviewprovider.OutputSchemaHash,
		provEnv.Ciphertext, provEnv.Nonce, provEnv.KeyID, userID).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from ai_providers where id = ?`, provID).Error
		_ = db.Exec(`delete from system_settings where namespace = 'ai_budget'`).Error
		_ = db.Exec(`delete from ai_daily_budget_usage`).Error
		_ = db.Exec(`delete from ai_usage_ledger`).Error
	})

	svc.Review = &reviewapp.Services{DB: db, Crypter: crypter}
	day := svc.Review.BudgetDay(time.Now())
	if err := db.Exec(`
		insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('ai_budget', 1, '{"enforced":true,"currency":"USD","daily_budget_minor":10,"alert_threshold_percent":80,"input_price_per_million_minor":10000,"output_price_per_million_minor":20000}'::jsonb, 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings`, userID).Error; err != nil {
		t.Fatalf("seed ai_budget settings: %v", err)
	}
	_ = db.Exec(`delete from ai_daily_budget_usage`).Error
	_ = db.Exec(`delete from ai_usage_ledger`).Error

	// 1. Exhausted daily budget -> fail closed 2010 before calling LLM.
	if err := db.Exec(`
		insert into ai_daily_budget_usage (budget_day, currency, amount_minor, reserved_minor, version, updated_at)
		values (?, 'USD', 10, 0, 1, now())`, day).Error; err != nil {
		t.Fatalf("seed exhausted budget: %v", err)
	}
	_, err = svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmt.ID, time.Time{}, time.Time{})
	var diagErr *domain.Error
	if !errors.As(err, &diagErr) || diagErr.Code != domain.CodeAIBudgetExhausted {
		t.Fatalf("expected 2010 AI_DAILY_BUDGET_EXHAUSTED, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 HTTP calls when daily budget exhausted, got %d", calls.Load())
	}

	// 2. Available budget + LLM 500 -> 2011 and reservation is released (reserved_minor == 0).
	_ = db.Exec(`update ai_daily_budget_usage set amount_minor = 0, reserved_minor = 0 where budget_day = ? and currency = 'USD'`, day).Error
	returnErr.Store(true)
	_, err = svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmt.ID, time.Time{}, time.Time{})
	if !errors.As(err, &diagErr) || diagErr.Code != domain.CodeAIProviderFailed {
		t.Fatalf("expected 2011 AI_PROVIDER_FAILED on HTTP 500, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 HTTP call on LLM failure, got %d", calls.Load())
	}
	var usage struct {
		AmountMinor   int64
		ReservedMinor int64
	}
	if err := db.Raw(`select amount_minor, reserved_minor from ai_daily_budget_usage where budget_day = ? and currency = 'USD'`, day).Scan(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.ReservedMinor != 0 || usage.AmountMinor != 0 {
		t.Fatalf("expected released budget reservation (0/0), got amount=%d reserved=%d", usage.AmountMinor, usage.ReservedMinor)
	}

	// 3. Available budget + LLM 200 -> ledger row recorded and cost settled cleanly.
	returnErr.Store(false)
	rep, err := svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmt.ID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DiagnoseStatementWithAI with budget failed: %v", err)
	}
	var ledger struct {
		ProviderID   string
		ModelName    string
		InputTokens  int64 `gorm:"column:input_token_count"`
		OutputTokens int64 `gorm:"column:output_token_count"`
		AmountMinor  int64
		Outcome      string
	}
	if err := db.Raw(`select provider_id::text as provider_id, model_name, input_token_count, output_token_count, amount_minor, outcome
		from ai_usage_ledger where review_run_id = ?`, rep.ReportID).Scan(&ledger).Error; err != nil {
		t.Fatalf("query ai_usage_ledger: %v", err)
	}
	if ledger.Outcome != "succeeded" || ledger.InputTokens != 150 || ledger.OutputTokens != 50 ||
		ledger.ProviderID != provID || ledger.ModelName != "gpt-4o-mini" || ledger.AmountMinor <= 0 {
		t.Fatalf("unexpected ledger row: %+v", ledger)
	}
	if err := db.Raw(`select amount_minor, reserved_minor from ai_daily_budget_usage where budget_day = ? and currency = 'USD'`, day).Scan(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.ReservedMinor != 0 || usage.AmountMinor != ledger.AmountMinor {
		t.Fatalf("unexpected settled budget usage: amount=%d reserved=%d ledger=%d", usage.AmountMinor, usage.ReservedMinor, ledger.AmountMinor)
	}
}

type diagBudgetStubState struct {
	mu               sync.Mutex
	failBudgetLoad   bool
	failBudgetUpsert bool
	txRollbacks      int
	releases         []int64
}

type diagBudgetDriver struct{}

var (
	diagBudgetMu   sync.Mutex
	diagBudgetMap  = map[string]*diagBudgetStubState{}
	diagBudgetOnce sync.Once
)

func (diagBudgetDriver) Open(dsn string) (driver.Conn, error) {
	diagBudgetMu.Lock()
	st := diagBudgetMap[dsn]
	diagBudgetMu.Unlock()
	return &diagBudgetConn{state: st}, nil
}

type diagBudgetConn struct{ state *diagBudgetStubState }
type diagBudgetTx struct{ state *diagBudgetStubState }

func (c *diagBudgetConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *diagBudgetConn) Close() error                        { return nil }
func (c *diagBudgetConn) Begin() (driver.Tx, error)           { return diagBudgetTx{state: c.state}, nil }
func (c *diagBudgetConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return diagBudgetTx{state: c.state}, nil
}
func (t diagBudgetTx) Commit() error { return nil }
func (t diagBudgetTx) Rollback() error {
	t.state.mu.Lock()
	t.state.txRollbacks++
	t.state.mu.Unlock()
	return nil
}

func (c *diagBudgetConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	failUpsert := c.state.failBudgetUpsert
	c.state.mu.Unlock()
	if strings.Contains(query, "update ai_daily_budget_usage set reserved_minor = greatest") && len(args) > 0 {
		if amt, ok := args[0].Value.(int64); ok {
			c.state.mu.Lock()
			c.state.releases = append(c.state.releases, amt)
			c.state.mu.Unlock()
		}
	}
	if failUpsert && strings.Contains(query, "insert into ai_daily_budget_usage") {
		return nil, errors.New("simulated daily budget upsert failure")
	}
	return driver.RowsAffected(1), nil
}

func (c *diagBudgetConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	failLoad := c.state.failBudgetLoad
	c.state.mu.Unlock()
	if failLoad && strings.Contains(query, "namespace = 'ai_budget'") {
		return nil, errors.New("simulated ai_budget query error")
	}
	return &diagBudgetStubRows{}, nil
}

type diagBudgetStubRows struct{}

func (*diagBudgetStubRows) Columns() []string         { return []string{} }
func (*diagBudgetStubRows) Close() error              { return nil }
func (*diagBudgetStubRows) Next([]driver.Value) error { return io.EOF }

func TestDiagnosticsBudgetFailClosedOnLoadErrorAndTransactionalSettle(t *testing.T) {
	diagBudgetOnce.Do(func() {
		sql.Register("diagnostics_budget_stub", diagBudgetDriver{})
	})
	st := &diagBudgetStubState{failBudgetLoad: true, failBudgetUpsert: true}
	dsn := "diag-budget-" + uuid.NewString()
	diagBudgetMu.Lock()
	diagBudgetMap[dsn] = st
	diagBudgetMu.Unlock()
	t.Cleanup(func() {
		diagBudgetMu.Lock()
		delete(diagBudgetMap, dsn)
		diagBudgetMu.Unlock()
	})

	sqlDB, err := sql.Open("diagnostics_budget_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB, PreferSimpleProtocol: true, WithoutReturning: true,
	}), &gorm.Config{DisableAutomaticPing: true, Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}

	svc := &Services{Review: &reviewapp.Services{DB: gdb}}
	if _, _, _, _, err := svc.reserveDailyBudget(context.Background(), gdb, 1024); err == nil {
		t.Fatal("expected reserveDailyBudget to fail closed on LoadBudgetSettings DB error")
	}

	settings := reviewapp.BudgetSettings{
		Enforced: true, Currency: "USD", DailyBudgetMinor: 10000,
		InputPricePerMillionMinor: 10000, OutputPricePerMillionMinor: 20000,
	}
	res := &reviewapp.ProviderJSONResult{ModelName: "gpt-4o-mini", InputTokens: 200, OutputTokens: 100}
	if err := svc.settleDailyBudget(context.Background(), gdb, uuid.NewString(), "2026-11-21", "USD", 500, settings, res, "succeeded"); err == nil {
		t.Fatal("expected settleDailyBudget error when ai_daily_budget_usage upsert fails")
	}
	st.mu.Lock()
	rollbacks := st.txRollbacks
	releases := append([]int64(nil), st.releases...)
	st.mu.Unlock()
	if rollbacks != 1 {
		t.Fatalf("expected 1 transaction rollback on partial Settle failure, got %d", rollbacks)
	}
	if len(releases) != 1 || releases[0] != 500 {
		t.Fatalf("expected outer release of 500 after rolled-back Settle tx, got %v", releases)
	}
}
