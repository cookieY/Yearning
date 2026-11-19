//go:build integration

// b9_helpers_test.go B9 change order domain HTTP test fixture: extends B6 fixture with
// reviewer/executor identities, enabled Provider (envelope key), configurable stage-actor flows,
// and counting-Dial AgentRunner driver (draft -> ready).
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// b9Fixture change order domain fixture (embeds pointer to b6Fixture since it contains sync.Mutex).
type b9Fixture struct {
	*b6Fixture
	reviewer1   testClient
	reviewer2   testClient
	executor    testClient
	reviewer1ID string
	reviewer2ID string
	executorID  string
	providerID  string
	dialCount   atomic.Int64
	lastFlowID  string
	lastRuleSet string
	lastDsID    string
}

// b9FlowStages builds multi-stage flow payload.
func b9FlowStages(name, ruleSetID string, stages []map[string]any) map[string]any {
	return map[string]any{
		"name": name, "flow_type": "change_review", "enabled": true,
		"rule_set_id": ruleSetID, "stages": stages,
	}
}

func b9Stage(pos int, dsID string, steps [][]string, exec []string) map[string]any {
	stepPayload := make([]map[string]any, 0, len(steps))
	for i, actors := range steps {
		as := make([]map[string]any, 0, len(actors))
		for _, uid := range actors {
			as = append(as, map[string]any{"user_id": uid})
		}
		stepPayload = append(stepPayload, map[string]any{
			"position": i + 1, "actors": as,
		})
	}
	execPayload := make([]map[string]any, 0, len(exec))
	for _, uid := range exec {
		execPayload = append(execPayload, map[string]any{"user_id": uid})
	}
	return map[string]any{
		"position": pos, "datasource_id": dsID,
		"approval_steps": stepPayload, "execution_actors": execPayload,
	}
}

// newB9Fixture constructs fixture: B6 base + reviewers/executor + Provider + single-stage dual-approver flow.
func newB9Fixture(t *testing.T) *b9Fixture {
	t.Helper()
	base := newB6Fixture(t)
	fx := &b9Fixture{b6Fixture: base}
	fx.reviewer1 = testClient{t: t, engine: base.engine, cookies: map[string]string{}}
	fx.reviewer2 = testClient{t: t, engine: base.engine, cookies: map[string]string{}}
	fx.executor = testClient{t: t, engine: base.engine, cookies: map[string]string{}}
	fx.reviewer1ID = fx.adminCreateUserB6(t, "rev1", "rev1-pass-123")
	fx.reviewer2ID = fx.adminCreateUserB6(t, "rev2", "rev2-pass-123")
	fx.executorID = fx.adminCreateUserB6(t, "exec1", "exec1-pass-123")
	if _, body, _ := fx.reviewer1.login("rev1", "rev1-pass-123"); errCode(body) != 0 {
		t.Fatal("rev1 login failed")
	}
	if _, body, _ := fx.reviewer2.login("rev2", "rev2-pass-123"); errCode(body) != 0 {
		t.Fatal("rev2 login failed")
	}
	if _, body, _ := fx.executor.login("exec1", "exec1-pass-123"); errCode(body) != 0 {
		t.Fatal("exec1 login failed")
	}
	// Enable Provider (AEAD envelope; test Dial does not touch network).
	fx.providerID = uuid.NewString()
	env, err := fx.svc.Crypter.Seal(crypto.PurposePayload, []byte("sk-b9-test"), crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(),
		Table:        "ai_providers", PrimaryKey: fx.providerID,
		Column: "secret_ciphertext", Purpose: "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`
		insert into ai_providers (id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at)
		values (?, 'b9-primary', 'openai_compatible', 'https://b9.example.com', 'b9-model', true,
			1, repeat('a',64), repeat('b',64), '{}'::jsonb, ?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		fx.providerID, env.Ciphertext, env.Nonce, env.KeyID, fx.ownerID).Error; err != nil {
		t.Fatal(err)
	}
	// Standard flow: single stage, single step with two approvers (any-one), executor=exec1.
	// Rule set binds High skill: after S18.66 finding_key is a system-derived row attribute;
	// fixture pins key to skill.b9-high echoed by b9HighDial (max_severity=critical downward closure,
	// so High falls within authorization and blocks).
	tool, err := fx.svc.CreatePromptTool(context.Background(), fx.ownerID, "b9-high-skill", "enabled",
		application.PromptToolDefinition{
			KnowledgeText: "\u6d4b\u8bd5\u7528High\u6280\u80fd",
			MaxSeverity:   "critical", Version: 1,
		})
	if err != nil {
		t.Fatalf("create prompt tool: %v", err)
	}
	if err := fx.db.Exec(`update prompt_tools set finding_key = 'skill.b9-high' where id = ?`, tool.ID).Error; err != nil {
		t.Fatalf("pin fixture finding key: %v", err)
	}
	rsView, err := fx.svc.CreateRuleSet(context.Background(), fx.ownerID, "b9-rules", true,
		[]byte("{}"), []string{tool.ID})
	if err != nil {
		t.Fatalf("create rule set: %v", err)
	}
	fx.lastDsID = fx.b6CreateDatasource(t, "b9-ds")
	fx.lastRuleSet = rsView.ID
	fx.lastFlowID = fx.b9CreateFlow(t, "b9-flow", []map[string]any{
		b9Stage(1, fx.lastDsID, [][]string{{fx.reviewer1ID, fx.reviewer2ID}}, []string{fx.executorID}),
	})
	fx.b9CreateGrant(t, fx.ownerID, fx.lastFlowID)
	return fx
}

// installB9DatasourceIdentityProbe keeps the HTTP fixture's intentionally
// unreachable datasource deterministic without weakening the production probe.
// The application seam accepts an unexported target type, so this external test
// package installs its function through the field's reflection type.
func installB9DatasourceIdentityProbe(t *testing.T, svc *application.Services) {
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
			identity.InstanceMarker = "mysql:b9-" + id
			identity.LowerCaseTableNames = "0"
		case "postgresql":
			identity.ServerVersion = "17.2"
			identity.InstanceMarker = "pg:b9-" + id
			identity.SystemIdentifier = "b9-" + id
			identity.DatabaseOID = 42
		}
		return []reflect.Value{
			reflect.ValueOf(identity),
			reflect.Zero(field.Type().Out(1)),
		}
	})
	field.Set(fn)
}

// b9CreateGrant grants user submission access to flow under a unique permission group name.
func (fx *b9Fixture) b9CreateGrant(t *testing.T, userID, flowID string) {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/permission-groups", map[string]any{
		"name": "b9-grant-" + uuid.NewString()[:8], "enabled": true,
		"member_user_ids":  []string{userID},
		"granted_flow_ids": []string{flowID},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create grant: %d %s", code, raw)
	}
}

// b9CreateFlow creates a flow with custom stage structure.
func (fx *b9Fixture) b9CreateFlow(t *testing.T, name string, stages []map[string]any) string {
	t.Helper()
	payload := b9FlowStages(name, fx.lastRuleSet, stages)
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/flows", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create b9 flow %s: %d %s", name, code, raw)
	}
	return body["data"].(map[string]any)["id"].(string)
}

// b9CleanDial no-finding responder (counted; used for G2 read-surface zero-Provider assertions).
func (fx *b9Fixture) b9CleanDial() func(req provider.Request) ([]byte, int, error) {
	return func(req provider.Request) ([]byte, int, error) {
		fx.dialCount.Add(1)
		if tools := b9MetadataToolRequests(req); len(tools) > 0 {
			body := map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "\u8bfb\u53d6\u6570\u636e\u5e93\u5143\u6570\u636e", "findings": []map[string]any{}, "tool_requests": tools,
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, 500, err
			}
			return raw, 200, nil
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "\u5ba1\u6838\u5b8c\u6210\uff0c\u672a\u53d1\u73b0\u98ce\u9669\u3002", "findings": []map[string]any{}, "tool_requests": []map[string]any{},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 500, err
		}
		return raw, 200, nil
	}
}

// b9HighDial High-finding responder (constructs blocked Run).
func (fx *b9Fixture) b9HighDial() func(req provider.Request) ([]byte, int, error) {
	return func(req provider.Request) ([]byte, int, error) {
		fx.dialCount.Add(1)
		if tools := b9MetadataToolRequests(req); len(tools) > 0 {
			body := map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "\u8bfb\u53d6\u6570\u636e\u5e93\u5143\u6570\u636e", "findings": []map[string]any{}, "tool_requests": tools,
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, 500, err
			}
			return raw, 200, nil
		}
		gid := ""
		if len(req.AnonymizedStatementGroups) > 0 {
			gid = req.AnonymizedStatementGroups[0].GroupID
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "\u5ba1\u6838\u53d1\u73b0\u9ad8\u98ce\u9669\u95ee\u9898\u3002", "findings": []map[string]any{{
				"finding_key": "skill.b9-high", "category": "correctness", "severity": "high",
				"title": "\u9ad8\u5371\u53d8\u66f4", "message": "\u8bed\u53e5\u8fdd\u53cd\u5185\u7f6e\u6280\u80fd", "suggestion": "\u4fee\u590d\u540e\u91cd\u5ba1",
				"fingerprint_group_ids": []string{gid}, "evidence_ids": []string{}, "fact_basis": "model_only",
			}}, "tool_requests": []map[string]any{},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 500, err
		}
		return raw, 200, nil
	}
}

// b9MetadataToolRequests requests one complete table fact for each referenced
// table only on the first Provider turn. Later turns already carry normalized
// evidence and can provide their business conclusion.
func b9MetadataToolRequests(req provider.Request) []map[string]any {
	if len(req.NormalizedEvidence) > 0 {
		return nil
	}
	type tableTarget struct{ schema, table string }
	seen := map[tableTarget]bool{}
	var out []map[string]any
	for _, group := range req.AnonymizedStatementGroups {
		for _, object := range group.Objects {
			if object.Kind != "table" || object.Token == "" {
				continue
			}
			target := tableTarget{schema: object.SchemaToken, table: object.Token}
			if seen[target] {
				continue
			}
			seen[target] = true
			out = append(out, map[string]any{
				"tool_call_id": uuid.NewString(),
				"tool_name":    "schema_metadata",
				"arguments": map[string]any{
					"schema": target.schema,
					"table":  target.table,
				},
			})
		}
	}
	return out
}

// b9FakeFacts read-only fact port (deterministic facts, no network access).
type b9FakeFacts struct{}

// PolicyCatalog describes the complete virtual fixture namespace, using the
// same requested-schema boundary as production. No capability bypass is used.
func (f *b9FakeFacts) PolicyCatalog(ctx context.Context, schemas []string) (database.PolicyCatalog, error) {
	if err := ctx.Err(); err != nil {
		return database.PolicyCatalog{}, err
	}
	ids := []database.PolicyCatalogIdentifier{}
	for _, schema := range schemas {
		ids = append(ids,
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogSchema, Schema: schema, Name: schema},
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: schema, Name: "orders"},
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: schema, Relation: "orders", Name: "id"},
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: schema, Relation: "orders", Name: "amount"},
			database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogIndex, Schema: schema, Relation: "orders", Name: "PRIMARY"},
		)
	}
	return database.PolicyCatalog{Identifiers: ids, SchemaCount: len(schemas)}, nil
}

func (f *b9FakeFacts) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql"}, nil
}
func (f *b9FakeFacts) SchemaMetadata(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"table_count": 1}, nil
}
func (f *b9FakeFacts) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 2, "returned_column_count": 2,
		"columns": []map[string]any{
			{"column_name": "id", "ordinal": 1, "type_class": "integer", "type_status": "observed"},
			{"column_name": "amount", "ordinal": 2, "type_class": "integer", "type_status": "observed"},
		},
		"metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}
func (f *b9FakeFacts) TableStatistics(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{
		"column_count": 2, "existence": "present", "estimate": true,
		"metadata_complete": true, "truncated": false,
	}, nil
}
func (f *b9FakeFacts) IndexMetadata(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"indexes": []map[string]any{{"index_name": "PRIMARY"}}}, nil
}
func (f *b9FakeFacts) SafeExplain(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"access_method": "sequential_scan", "estimated_rows": 9, "estimate": true}, nil
}

// b9ReadyDraft runs draft -> SQL -> ephemeral precheck (POST /review-runs) -> poll until ready.
// Returns (draftID, version, runID); after RCP-20261101 Run no longer advances draft version,
// so version is the draft version after PUT sql (for subsequent If-Match edits).
func (fx *b9Fixture) b9ReadyDraft(t *testing.T, flowID, sql string,
	respond func(req provider.Request) ([]byte, int, error),
	extraCreate ...map[string]any) (string, int64, string) {
	t.Helper()
	// Create draft (extraCreate may carry additional fields such as S18.56 requested schedule).
	create := map[string]any{
		"flow_id": flowID, "title": "b9-order", "description": "b9",
	}
	for _, extra := range extraCreate {
		for k, v := range extra {
			create[k] = v
		}
	}
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", create, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create draft: %d %s", code, raw)
	}
	draft := body["data"].(map[string]any)
	draftID := draft["id"].(string)
	version := int64(draft["version"].(float64))
	// Write SQL.
	code, body, raw = fx.owner.doWithVersion("PUT", "/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": sql}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put sql: %d %s", code, raw)
	}
	version++
	runID := fx.b9StartPrecheck(t, flowID, draftID, sql, respond)
	runData := fx.b9WaitRunTerminal(t, runID)
	if runData["state"] != "ready" {
		t.Fatalf("run want ready got %v (failure=%v)", runData["state"], runData["failure_code"])
	}
	return draftID, version, runID
}

// b9StartPrecheck switches responder and POSTs /review-runs (ephemeral precheck entry point).
func (fx *b9Fixture) b9StartPrecheck(t *testing.T, flowID, draftID, sql string,
	respond func(req provider.Request) ([]byte, int, error)) string {
	t.Helper()
	fx.setAgentDial(respond)
	code, body, raw := fx.owner.doWithHeaders("POST", "/api/v4/review-runs", map[string]any{
		"flow_id": flowID, "draft_id": draftID, "sql": sql,
	}, map[string]string{
		"X-CSRF-Token": fx.owner.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("start ephemeral review: %d %s", code, raw)
	}
	runID := body["data"].(map[string]any)["run_id"].(string)
	if runID == "" {
		t.Fatalf("run_id missing: %s", raw)
	}
	return runID
}

// b9WaitRunTerminal polls ephemeral Run until terminal state (ready/failed/partial/blocked).
func (fx *b9Fixture) b9WaitRunTerminal(t *testing.T, runID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, body, raw := fx.owner.do("GET", "/api/v4/review-runs/"+runID, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("poll run: %d %s", code, raw)
		}
		data := body["data"].(map[string]any)
		switch data["state"] {
		case "ready", "failed", "partial", "blocked":
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s not terminal: %v", runID, data["state"])
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// b9Submit submits ephemeral precheck result (POST /change-orders, RCP-20261101 two-step flow).
// Payload carries current SQL and run_id; idempotency key prevents duplicate orders on double click.
func (fx *b9Fixture) b9Submit(t *testing.T, flowID, draftID, runID, sql string) (int, map[string]any, string) {
	t.Helper()
	return fx.owner.doWithHeaders("POST", "/api/v4/change-orders", map[string]any{
		"flow_id": flowID, "draft_id": draftID, "sql": sql, "run_id": runID,
	}, map[string]string{
		"Idempotency-Key": "b9-submit-" + uuid.NewString()[:8],
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
}
