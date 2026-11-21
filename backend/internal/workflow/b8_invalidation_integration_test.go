//go:build integration

// b8_invalidation_integration_test.go verifies B8 flow-replacement invalidation hook wiring
// against real PostgreSQL: workflow.ReplaceFlow must invoke application.InvalidateRunsForFlow
// so that uncommitted terminal Runs referencing the flow immediately transition to outdated.
// This test lives in the workflow package because workflow -> application is the production dependency
// direction; placing it in application tests would create an import cycle.
package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// acquireB8Lock shares the same cross-process file lock as application/httpapi integration tests
// to prevent parallel package tests from colliding on the shared database.
func acquireB8Lock(t *testing.T) {
	path := os.TempDir() + "/yearningx-itest-shared.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// installB8DatasourceIdentityProbe installs the same deterministic test seam
// used by the HTTP B9 fixture. Production keeps the live probe path whenever
// this field is nil.
func installB8DatasourceIdentityProbe(t *testing.T, svc *reviewapp.Services) {
	t.Helper()
	field := reflect.ValueOf(svc).Elem().FieldByName("DatasourceIdentityProbe")
	if !field.IsValid() || !field.CanSet() {
		t.Fatal("application datasource identity probe seam unavailable")
	}
	fn := reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
		row := args[3]
		id := row.FieldByName("ID").String()
		mode := row.FieldByName("CompatibilityMode").String()
		host := row.FieldByName("Host").String()
		port := int(row.FieldByName("Port").Int())
		identity := database.ServerIdentity{
			EngineFamily:         mode,
			ConfiguredEndpointID: fmt.Sprintf("%s:%d", host, port),
		}
		if databaseName := row.FieldByName("DatabaseName"); !databaseName.IsNil() {
			identity.DatabaseName = databaseName.Elem().String()
		}
		switch mode {
		case "mysql":
			identity.ServerVersion = "8.0.36"
			identity.InstanceMarker = "mysql:b8-" + id
			identity.LowerCaseTableNames = "0"
		case "postgresql":
			identity.ServerVersion = "17.2"
			identity.InstanceMarker = "pg:b8-" + id
			identity.SystemIdentifier = "b8-" + id
			identity.DatabaseOID = 42
		}
		return []reflect.Value{
			reflect.ValueOf(identity),
			reflect.Zero(field.Type().Out(1)),
		}
	})
	field.Set(fn)
}

func TestB8ReplaceFlowKeepsEphemeralRunDecoupled(t *testing.T) {
	acquireB8Lock(t)
	password := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured: set YEARNING_TEST_DATABASE_PASSWORD")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr("YEARNING_TEST_DATABASE_HOST", "localhost"),
		envOr("YEARNING_TEST_DATABASE_PORT", "54330"),
		envOr("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		envOr("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"))
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	tables := []string{
		"evidence_raw_payloads", "evidence_facts", "review_finding_evidence", "review_findings",
		"stage_review_snapshots", "sql_statements", "sql_fingerprint_groups", "review_runs",
		"change_drafts", "draft_sql_payloads", "async_tasks", "idempotency_records",
		"domain_events", "outbox_events", "audit_events",
		"flow_query_capabilities", "flow_approval_step_actors", "flow_execution_actors", "flow_approval_steps",
		"flow_stages", "flows", "permission_group_flow_grants", "permission_group_members",
		"permission_groups", "datasource_credentials", "datasource_tls_materials",
		"datasources", "rule_sets", "prompt_tools", "users", "ai_providers",
	}
	for _, tbl := range tables {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	// The two purposes must use distinct key material: keyring loading rejects cross-purpose key reuse
	// (AEAD/HMAC domain separation, SEC-0046).
	key := make([]byte, 32)
	fpKey := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
		fpKey[i] = byte(0x80 + i)
	}
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b8-wf-itest","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		base64.StdEncoding.EncodeToString(key),
		base64.StdEncoding.EncodeToString(fpKey))
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/keyring.json", []byte(keyringDoc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(dir + "/keyring.json")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := auditlog.NewRecorder(db)
	wf, err := NewService(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := reviewapp.NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	installB8DatasourceIdentityProbe(t, svc)
	ctx := context.Background()
	adminID := uuid.NewString()
	if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, role, created_at, updated_at)
		values (?, 'admin', 'Admin', true, 'admin', now(), now())`, adminID).Error; err != nil {
		t.Fatal(err)
	}

	ruleView, err := svc.CreateRuleSet(ctx, adminID, "b8-wf-rules", true, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	dsID := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-ds', 'mysql', 'mysql', 'native', 'db.example.com', 3306, 'shop', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ab", 32), adminID).Error; err != nil {
		t.Fatal(err)
	}
	write := &FlowWrite{
		Name: "b8-wf-flow", FlowType: "change_review", Enabled: true, RuleSetID: &ruleView.ID,
		Stages: []StageWrite{{
			Position: 1, DatasourceID: dsID,
			ApprovalSteps:   []ApprovalStepWrite{{Position: 1, Actors: []ActorRef{{UserID: adminID}}}},
			ExecutionActors: []ActorRef{{UserID: adminID}},
		}},
	}
	admin := AdminActor{UserID: adminID, Username: "admin"}
	flowView, err := wf.CreateFlow(ctx, admin, write, RequestMeta{})
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	groupID := uuid.NewString()
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-group', true, 1, ?, now(), now())`, groupID, adminID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`,
		groupID, adminID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`,
		groupID, flowView.ID).Error; err != nil {
		t.Fatal(err)
	}

	owner := reviewapp.Actor{UserID: adminID, Username: "admin"}
	draft, err := svc.CreateDraft(ctx, owner, reviewapp.CreateDraftInput{FlowID: flowView.ID, Title: "b8"})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	// DML form (the review-side fake catalog declares this table; the test focuses on Run decoupling after ReplaceFlow
	// rather than SQL shape -- review fixture behavior for CREATE TABLE is covered separately in httpapi).
	const wfSQL = "update wf_items set id = id where id = 1;"
	draft, err = svc.ReplaceSQL(ctx, owner, draft.ID, []byte(wfSQL), draft.Version)
	if err != nil {
		t.Fatalf("replace sql: %v", err)
	}
	// RCP-20261101: pre-review runs are transient (zero DB persistence); the review driver is the deterministic Agent
	// (fails closed when no Provider exists, so we wire a zero-finding fake dialer to satisfy readiness;
	// an enabled Provider row with an AEAD envelope key must exist per fail-closed semantics).
	providerID := uuid.NewString()
	providerEnv, err := crypter.Seal(crypto.PurposePayload, []byte("sk-b8-wf"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "ai_providers", PrimaryKey: providerID,
		Column: "secret_ciphertext", Purpose: "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		insert into ai_providers (id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-wf-primary', 'openai_compatible', 'https://b8wf.example.com', 'b8-model', true,
			1, repeat('a',64), repeat('b',64), '{}'::jsonb, ?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		providerID, providerEnv.Ciphertext, providerEnv.Nonce, providerEnv.KeyID, adminID).Error; err != nil {
		t.Fatal(err)
	}
	// The grouping stage of transient pre-review freezes targets per stage datasource (real probes to
	// db.example.com would fail) -- install a deterministic identity probe matching the httpapi fixture.
	installWFIdentityProbe(t, svc)
	agent := reviewapp.NewAgentRunner(svc, reviewapp.AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
				// First turn requests schema_metadata for referenced objects (closing context completeness via
				// a tool round-trip -- CREATE TABLE relies on the absent fact); conclude on the next turn once evidence is present.
				if len(req.NormalizedEvidence) == 0 {
					tools := []map[string]any{}
					seen := map[string]bool{}
					for _, group := range req.AnonymizedStatementGroups {
						for _, object := range group.Objects {
							if object.Kind != "table" || object.Token == "" || object.SchemaToken == "" || seen[object.Token] {
								continue
							}
							seen[object.Token] = true
							tools = append(tools, map[string]any{
								"tool_call_id": uuid.NewString(),
								"tool_name":    "schema_metadata",
								"arguments": map[string]any{
									"schema": object.SchemaToken, "table": object.Token,
								},
							})
						}
					}
					if len(tools) > 0 {
						body := map[string]any{
							"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
							"summary": "Read database metadata", "findings": []map[string]any{},
							"tool_requests": tools,
						}
						raw, merr := json.Marshal(body)
						if merr != nil {
							return nil, 500, merr
						}
						return raw, 200, nil
					}
				}
				body := map[string]any{
					"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
					"summary": "Review completed with no risks found.", "findings": []map[string]any{}, "tool_requests": []map[string]any{},
				}
				raw, merr := json.Marshal(body)
				if merr != nil {
					return nil, 500, merr
				}
				return raw, 200, nil
			}), nil
		},
		FactSourceFor: func(context.Context, string, reviewapp.FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return fakeWFTools{}, func() {}, nil
		},
	})
	runID, err := svc.StartEphemeralRun(ctx, owner, agent, reviewapp.EphemeralStartInput{
		FlowID: flowView.ID, DraftID: draft.ID, SQL: []byte(wfSQL), IdempotencyKey: "b8-wf",
	})
	if err != nil {
		t.Fatalf("start ephemeral run: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		er := svc.EphemeralGetForTest(runID, owner.UserID)
		if er != nil {
			switch er.State() {
			case domain.RunStateReady, domain.RunStateFailed, domain.RunStatePartial, domain.RunStateBlocked:
				if er.State() != domain.RunStateReady {
					t.Fatalf("precondition ready, got %s (failure=%s)", er.State(), er.Failure)
				}
			}
			if er.State() == domain.RunStateReady {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("ephemeral run not terminal")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Replace flow template (rename with identical structure) -> succeeds. Per RCP-20261101 semantics:
	// pre-review Runs are transient (zero DB persistence) and decoupled from drafts, so template replacement
	// no longer marks (and cannot mark) Runs/drafts as outdated -- invalidation protection moves to
	// submission-time flow version validation (2001 covered by HTTP gate G4); transient Runs remain readable
	// and users simply re-run pre-review.
	write2 := *write
	write2.Name = "b8-wf-flow-v2"
	if _, err := wf.ReplaceFlow(ctx, admin, flowView.ID, flowView.Version, &write2, RequestMeta{}); err != nil {
		t.Fatalf("replace flow: %v", err)
	}
	if er := svc.EphemeralGetForTest(runID, owner.UserID); er == nil {
		t.Fatal("ephemeral run must stay readable after ReplaceFlow")
	}
	draftAfter, err := svc.GetDraft(ctx, owner, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if draftAfter.State != "draft" {
		t.Fatalf("draft must stay draft (no review states on drafts), got %s", draftAfter.State)
	}
}

// installWFIdentityProbe answers all datasource probes with a deterministic identity (test target
// databases are unreachable; matches httpapi installB9DatasourceIdentityProbe).
func installWFIdentityProbe(t *testing.T, svc *reviewapp.Services) {
	t.Helper()
	field := reflect.ValueOf(svc).Elem().FieldByName("DatasourceIdentityProbe")
	if !field.IsValid() || !field.CanSet() {
		t.Fatal("application datasource identity probe seam unavailable")
	}
	fn := reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
		row := args[3]
		id := row.FieldByName("ID").String()
		mode := row.FieldByName("CompatibilityMode").String()
		host := row.FieldByName("Host").String()
		port := int(row.FieldByName("Port").Int())
		identity := database.ServerIdentity{
			EngineFamily:         mode,
			ConfiguredEndpointID: fmt.Sprintf("%s:%d", host, port),
		}
		if databaseName := row.FieldByName("DatabaseName"); !databaseName.IsNil() {
			identity.DatabaseName = databaseName.Elem().String()
		}
		switch mode {
		case "mysql":
			identity.ServerVersion = "8.0.36"
			identity.InstanceMarker = "mysql:b8wf-" + id
			identity.LowerCaseTableNames = "0"
		case "postgresql":
			identity.ServerVersion = "17.2"
			identity.InstanceMarker = "pg:b8wf-" + id
			identity.SystemIdentifier = "b8wf-" + id
			identity.DatabaseOID = 42
		}
		return []reflect.Value{reflect.ValueOf(identity), reflect.Zero(field.Type().Out(1))}
	})
	field.Set(fn)
}

// fakeWFTools is a minimal tool fact source (answering review metadata requests with a fixed catalog).
type fakeWFTools struct{}

func (fakeWFTools) PolicyCatalog(ctx context.Context, schemas []string) (database.PolicyCatalog, error) {
	// Keep Catalog and TableMetadata consistent: both declare table wf_items with column id as existing.
	ids := []database.PolicyCatalogIdentifier{}
	for _, schema := range schemas {
		ids = append(ids,
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogSchema, Schema: schema, Name: schema},
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: schema, Name: "wf_items"},
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: schema, Relation: "wf_items", Name: "id"},
		)
	}
	return database.PolicyCatalog{Identifiers: ids, SchemaCount: len(schemas)}, nil
}
func (fakeWFTools) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "postgresql"}, nil
}
func (fakeWFTools) SchemaMetadata(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"table_count": 0}, nil
}
func (fakeWFTools) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	if table == "wf_items" {
		return map[string]any{
			"schema": schema, "table": table, "existence": "present",
			"column_count": 1, "returned_column_count": 1,
			"columns": []map[string]any{
				{"column_name": "id", "ordinal": 1, "type_class": "integer", "type_status": "observed"},
			},
			"metadata_complete": true, "truncated": false, "estimate": false,
		}, nil
	}
	return map[string]any{
		"schema": schema, "table": table, "existence": "absent",
		"columns": []map[string]any{}, "metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}
func (fakeWFTools) TableStatistics(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"existence": "absent", "estimate": true, "metadata_complete": true, "truncated": false}, nil
}
func (fakeWFTools) IndexMetadata(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"indexes": []map[string]any{}}, nil
}
func (fakeWFTools) SafeExplain(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"access_method": "sequential_scan", "estimated_rows": 0, "estimate": true}, nil
}

func TestB8BatchFlowViewsMatchGetAndCurrentUserFlows(t *testing.T) {
	acquireB8Lock(t)
	password := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured: set YEARNING_TEST_DATABASE_PASSWORD")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr("YEARNING_TEST_DATABASE_HOST", "localhost"),
		envOr("YEARNING_TEST_DATABASE_PORT", "54330"),
		envOr("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		envOr("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"))
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, tbl := range []string{
		"flow_query_capabilities", "flow_approval_step_actors", "flow_execution_actors",
		"flow_approval_steps", "flow_stages", "flows",
		"permission_group_flow_grants", "permission_group_members", "permission_groups",
		"datasource_credentials", "datasource_tls_materials", "datasources",
		"rule_sets", "users", "domain_events", "outbox_events", "audit_events",
	} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	key := make([]byte, 32)
	fpKey := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
		fpKey[i] = byte(0x80 + i)
	}
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b8-wf-batch-itest","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		base64.StdEncoding.EncodeToString(key),
		base64.StdEncoding.EncodeToString(fpKey))
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/keyring.json", []byte(keyringDoc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(dir + "/keyring.json")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := auditlog.NewRecorder(db)
	wf, err := NewService(db, crypter, recorder)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	adminID := uuid.NewString()
	reviewer2ID := uuid.NewString()
	ds1ID := uuid.NewString()
	ds2ID := uuid.NewString()
	rsID := uuid.NewString()
	for _, u := range []struct {
		id, name, role string
	}{
		{adminID, "wf-batch-admin", "admin"},
		{reviewer2ID, "wf-batch-rev", "user"},
	} {
		if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, role, created_at, updated_at)
			values (?, ?, ?, false, ?, now(), now())`, u.id, u.name, u.name, u.role).Error; err != nil {
			t.Fatalf("insert user %s: %v", u.name, err)
		}
	}
	for _, ds := range []struct{ id, name string }{{ds1ID, "batch-ds-alpha"}, {ds2ID, "batch-ds-beta"}} {
		if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
			host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
			values (?, ?, 'mysql', 'mysql', 'native', 'db.example.com', 3306, 'shop', '8.0.x', true, ?, 1, ?, now(), now())`,
			ds.id, ds.name, strings.Repeat("c", 63)+ds.id[:1], adminID).Error; err != nil {
			t.Fatalf("insert ds %s: %v", ds.name, err)
		}
	}
	if err := db.Exec(`insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, 'batch-rules', true, '{}'::jsonb, repeat('a', 64), 1, ?, now(), now())`, rsID, adminID).Error; err != nil {
		t.Fatalf("insert rule_set: %v", err)
	}

	actor := AdminActor{UserID: adminID, Username: "wf-batch-admin"}
	cfView, err := wf.CreateFlow(ctx, actor, &FlowWrite{
		Name: "batch-change-flow", FlowType: "change_review", Enabled: true, RuleSetID: &rsID,
		Stages: []StageWrite{
			{
				Position: 1, DatasourceID: ds1ID,
				ApprovalSteps:   []ApprovalStepWrite{{Position: 1, Actors: []ActorRef{{UserID: reviewer2ID}, {UserID: adminID}}}},
				ExecutionActors: []ActorRef{{UserID: adminID}},
			},
			{
				Position: 2, DatasourceID: ds2ID,
				ApprovalSteps: []ApprovalStepWrite{
					{Position: 1, Actors: []ActorRef{{UserID: reviewer2ID}}},
					{Position: 2, Actors: []ActorRef{{UserID: adminID}}},
				},
				ExecutionActors: []ActorRef{{UserID: reviewer2ID}},
			},
		},
	}, RequestMeta{})
	if err != nil {
		t.Fatalf("CreateFlow change_review: %v", err)
	}
	qfView, err := wf.CreateFlow(ctx, actor, &FlowWrite{
		Name: "batch-query-flow", FlowType: "query_access", Enabled: true,
		ApprovalSteps: []ApprovalStepWrite{
			{Position: 1, Actors: []ActorRef{{UserID: reviewer2ID}, {UserID: adminID}}},
			{Position: 2, Actors: []ActorRef{{UserID: adminID}}},
		},
		QueryCapabilities: []QueryCapabilityWrite{
			{DatasourceID: ds1ID, CanQuery: true, CanExport: false},
			{DatasourceID: ds2ID, CanQuery: true, CanExport: true},
		},
	}, RequestMeta{})
	if err != nil {
		t.Fatalf("CreateFlow query_access: %v", err)
	}

	if _, err := wf.CreateGroup(ctx, actor, &GroupWrite{
		Name: "batch-pg", Enabled: true,
		MemberUserIDs:  []string{adminID},
		GrantedFlowIDs: []string{cfView.ID, qfView.ID},
	}, RequestMeta{}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	wantByID := make(map[string]*FlowView, 2)
	for _, id := range []string{cfView.ID, qfView.ID} {
		g, gerr := wf.Get(ctx, id)
		if gerr != nil {
			t.Fatalf("Get(%s): %v", id, gerr)
		}
		wantByID[id] = g
	}

	listed, next, err := wf.List(ctx, 50, "", "", "", nil)
	if err != nil || next != nil || len(listed) != 2 {
		t.Fatalf("List() = len(%d) next=%v err=%v", len(listed), next, err)
	}
	for i := range listed {
		got := listed[i]
		want := wantByID[got.ID]
		if !reflect.DeepEqual(&got, want) {
			t.Fatalf("List view mismatch for %s:\ngot:  %+v\nwant: %+v", got.ID, got, want)
		}
	}

	for _, ft := range []string{"change_review", "query_access"} {
		cur, _, cerr := wf.CurrentUserFlows(ctx, adminID, ft, 50, "")
		if cerr != nil || len(cur) != 1 {
			t.Fatalf("CurrentUserFlows(%s) = len(%d) err=%v", ft, len(cur), cerr)
		}
		if !reflect.DeepEqual(&cur[0], wantByID[cur[0].ID]) {
			t.Fatalf("CurrentUserFlows(%s) mismatch:\ngot:  %+v\nwant: %+v", ft, cur[0], wantByID[cur[0].ID])
		}
	}
}
