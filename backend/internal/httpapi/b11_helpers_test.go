//go:build integration

// b11_helpers_test.go B11 query/schedule domain HTTP test fixture: extends B10 fixture with
// query flows, real PG target DB (query-purpose credentials), query user, and settings toggles.
// HTTP surface uses B11 routes auto-derived by engine; service surface (masking injection / sweep /
// multi-instance / scheduler tick) uses the fixture query service instance.
package httpapi

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
	"github.com/cookieY/YearningX/backend/internal/scheduler"
)

// b11Fixture query/schedule domain fixture.
type b11Fixture struct {
	*b10Fixture
	q           *queryapp.Services
	requester   testClient
	requesterID string
	qFlowID     string
	qDsID       string
	qSchema     string
	qTargetPG   b11TargetPGConfig
}

// b11TargetPGConfig isolates each fixture by database and login role. Query
// application pools still carry the product application_name, so cleanup only
// targets sessions matching both this fixture's database and dedicated role.
type b11TargetPGConfig struct {
	Host              string
	Port              int
	AdminUser         string
	AdminPassword     string
	FixtureDatabase   string
	FixtureRole       string
	FixturePassword   string
	ApplicationName   string
	VersionConstraint string
}

// newB11Fixture constructs query domain fixture (real PG target DB + query flow + dual approvers).
func newB11Fixture(t *testing.T) *b11Fixture {
	t.Helper()
	base := newB10Fixture(t)
	q, err := queryapp.NewServices(base.db, base.svc.Crypter, base.svc.Recorder)
	if err != nil {
		t.Fatal(err)
	}
	fx := &b11Fixture{b10Fixture: base, q: q}
	fx.qTargetPG = resolveB11TargetPG(t, base)
	fx.requester = testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	fx.requesterID = fx.adminCreateUserB6(t, "quser1", "quser1-pass-123")
	if _, body, _ := fx.requester.login("quser1", "quser1-pass-123"); errCode(body) != 0 {
		t.Fatal("quser1 login failed")
	}
	fx.qSchema = "qtarget"
	fx.b11EnsureQueryTarget(t)
	fx.qDsID = fx.b11QueryDatasource(t)
	fx.qFlowID = fx.b11QueryFlow(t, fx.reviewer1ID, fx.reviewer2ID, fx.qDsID, true, true)
	fx.b9CreateGrant(t, fx.requesterID, fx.qFlowID)
	return fx
}

// resolveB11TargetPG resolves the real target service coordinates via testenv.
func resolveB11TargetPG(t *testing.T, base *b10Fixture) b11TargetPGConfig {
	t.Helper()
	scope := strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	cfg, ok := testenv.ResolveTargetPG(testenv.WithDefaultPort(54330))
	password := cfg.Password
	if !ok || password == "" {
		password = base.b10PGPassword(t)
	}
	return b11TargetPGConfig{
		Host:            cfg.Host,
		Port:            cfg.Port,
		AdminUser:       cfg.User,
		AdminPassword:   password,
		FixtureDatabase: "yearning_b11_target_" + scope,
		FixtureRole:     "yearning_b11_role_" + scope,
		FixturePassword: "b11_" + scope,
		ApplicationName: "yearning_b11_fixture_" + scope,
	}
}

// b11EnsureQueryTarget provisions a unique database and role on the target
// service, then bootstraps its tables. No metadata-cluster database or backend
// administration is involved.
func (fx *b11Fixture) b11EnsureQueryTarget(t *testing.T) {
	t.Helper()
	cfg := fx.qTargetPG
	maintenance := openTargetPGNamed(t, cfg.Host, strconv.Itoa(cfg.Port), "postgres",
		cfg.AdminUser, cfg.AdminPassword, cfg.ApplicationName)
	defer func() { _ = maintenance.Close() }()

	var exists bool
	if err := maintenance.QueryRow(`select exists(select 1 from pg_roles where rolname = $1)`,
		cfg.FixtureRole).Scan(&exists); err != nil {
		t.Fatalf("check fixture role: %v", err)
	}
	if exists {
		t.Fatalf("fixture role %q already exists", cfg.FixtureRole)
	}
	if err := maintenance.QueryRow(`select exists(select 1 from pg_database where datname = $1)`,
		cfg.FixtureDatabase).Scan(&exists); err != nil {
		t.Fatalf("check fixture database: %v", err)
	}
	if exists {
		t.Fatalf("fixture database %q already exists", cfg.FixtureDatabase)
	}

	roleCreated, databaseCreated := false, false
	t.Cleanup(func() {
		if !roleCreated && !databaseCreated {
			return
		}
		db := openTargetPGNamed(t, cfg.Host, strconv.Itoa(cfg.Port), "postgres",
			cfg.AdminUser, cfg.AdminPassword, cfg.ApplicationName)
		defer func() { _ = db.Close() }()
		if databaseCreated {
			if _, err := db.Exec(`select pg_terminate_backend(pid)
				from pg_stat_activity
				where datname = $1 and usename = $2
				  and application_name in ($3, $4)
				  and pid <> pg_backend_pid()`,
				cfg.FixtureDatabase, cfg.FixtureRole,
				cfg.ApplicationName, database.ApplicationName); err != nil {
				t.Logf("fixture cleanup terminate exact sessions: %v", err)
			}
			if _, err := db.Exec(`drop database ` + cfg.FixtureDatabase); err != nil {
				t.Logf("fixture cleanup drop database: %v", err)
				return
			}
		}
		if roleCreated {
			if _, err := db.Exec(`drop role ` + cfg.FixtureRole); err != nil {
				t.Logf("fixture cleanup drop role: %v", err)
			}
		}
	})

	if _, err := maintenance.Exec(`create role ` + cfg.FixtureRole +
		` login password '` + cfg.FixturePassword + `'`); err != nil {
		t.Fatalf("create fixture role: %v", err)
	}
	roleCreated = true
	if _, err := maintenance.Exec(`create database ` + cfg.FixtureDatabase +
		` owner ` + cfg.FixtureRole); err != nil {
		t.Fatalf("create fixture database: %v", err)
	}
	databaseCreated = true

	var versionNumber int
	if err := maintenance.QueryRow(`select current_setting('server_version_num')::int`).
		Scan(&versionNumber); err != nil {
		t.Fatalf("read target server version: %v", err)
	}
	cfg.VersionConstraint = fmt.Sprintf("%d.x", versionNumber/10000)
	fx.qTargetPG = cfg

	tdb := openTargetPGNamed(t, cfg.Host, strconv.Itoa(cfg.Port), cfg.FixtureDatabase,
		cfg.FixtureRole, cfg.FixturePassword, cfg.ApplicationName)
	defer tdb.Close()
	for _, stmt := range []string{
		`create schema if not exists qtarget`,
		`create table if not exists qtarget.accounts (id int primary key, name text, secret text, note text)`,
		`insert into qtarget.accounts (id, name, secret, note) values (1,'alice','s-crumb-1','n1'),(2,'bob','s-crumb-2','n2') on conflict (id) do nothing`,
		`drop table if exists qtarget.pages`,
		`create table qtarget.pages as select g as id, 'row-'||g as label from generate_series(1,200) g`,
	} {
		if _, err := tdb.Exec(stmt); err != nil {
			t.Fatalf("target bootstrap %q: %v", stmt[:30], err)
		}
	}
}

// b11QueryDatasource creates a real query target datasource (query-purpose credentials).
func (fx *b11Fixture) b11QueryDatasource(t *testing.T) string {
	t.Helper()
	cfg := fx.qTargetPG
	payload := map[string]any{
		"name": fmt.Sprintf("b11-qtarget-%s", fx.requesterID[:8]), "engine": "postgresql",
		"compatibility_mode": "postgresql", "deployment_kind": "native",
		"host": cfg.Host, "port": cfg.Port,
		"database_name": cfg.FixtureDatabase, "version_constraint": cfg.VersionConstraint, "enabled": true,
		"credentials": []map[string]any{{
			"purpose": "query", "username": cfg.FixtureRole,
			"password": map[string]any{"value": cfg.FixturePassword},
		}},
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create query datasource: %d %s", code, raw)
	}
	id := body["data"].(map[string]any)["id"].(string)
	if err := fx.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		strings.Repeat("ef", 32), id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// b11QueryFlow creates a query flow (single step with two reviewers; capabilities = can_query[+can_export]).
func (fx *b11Fixture) b11QueryFlow(t *testing.T, rev1, rev2, dsID string, canQuery, canExport bool) string {
	t.Helper()
	payload := map[string]any{
		"name": fmt.Sprintf("b11-qflow-%s-%s", dsID[:8], uuid.NewString()[:6]), "flow_type": "query_access",
		"enabled": true,
		"approval_steps": []map[string]any{{
			"position": 1,
			"actors":   []map[string]any{{"user_id": rev1}, {"user_id": rev2}},
		}},
		"query_capabilities": []map[string]any{{
			"datasource_id": dsID, "can_query": canQuery, "can_export": canExport,
		}},
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/flows", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create query flow: %d %s", code, raw)
	}
	return body["data"].(map[string]any)["id"].(string)
}

// b11SetApprovalEnabled toggles the query.approval_enabled setting.
func (fx *b11Fixture) b11SetApprovalEnabled(t *testing.T, enabled bool) {
	t.Helper()
	v := "false"
	if enabled {
		v = "true"
	}
	if err := fx.db.Exec(`
		insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('query', 1, jsonb_build_object('approval_enabled', ` + v + `, 'default_timeout_ms', 30000), 1, '00000000-0000-0000-0000-000000000000'::uuid, now(), now())
		on conflict (namespace) do update set settings = system_settings.settings || jsonb_build_object('approval_enabled', ` + v + `)`).Error; err != nil {
		t.Fatal(err)
	}
}

// b11SetVocabulary injects MaskingSource vocabulary for analyzer and failure-path isolation tests.
func (fx *b11Fixture) b11SetVocabulary(words ...string) {
	fx.q.MaskingSource = staticVocabulary(words)
}

type staticVocabulary []string

func (s staticVocabulary) Vocabulary(_ context.Context, _ *gorm.DB, _, _ string) ([]string, error) {
	return s, nil
}

// schedulerTick runs a single scheduler tick (due resolution + lazy query progression).
func (fx *b11Fixture) schedulerTick(t *testing.T) {
	t.Helper()
	s := scheduler.New(fx.db, fx.exec, fx.q, 0)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("scheduler tick: %v", err)
	}
}

// b11QActor returns the query user Actor (for service-layer calls).
func (fx *b11Fixture) b11QActor() queryapp.Actor {
	return queryapp.Actor{UserID: fx.requesterID, Username: "quser1"}
}
