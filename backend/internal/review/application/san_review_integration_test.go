//go:build integration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

func TestSANReviewAndProviderStore_PGFlow(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	db := testkit.OpenPG(t, testenv.WithDefaultPort(5432))

	tables := []string{
		"review_tool_calls", "evidence_raw_payloads", "evidence_facts", "review_finding_evidence", "review_findings",
		"stage_review_snapshots", "sql_statements", "sql_fingerprint_groups", "review_runs",
		"change_drafts", "draft_sql_payloads", "async_tasks", "idempotency_records",
		"domain_events", "outbox_events", "audit_events",
		"flow_approval_step_actors", "flow_execution_actors", "flow_approval_steps",
		"flow_stages", "flows", "permission_group_flow_grants", "permission_group_members",
		"permission_groups", "datasource_credentials", "datasource_tls_materials",
		"datasources", "knowledge_entries", "rule_set_prompt_tools", "prompt_tools", "rule_sets",
		"ai_providers", "ai_usage_ledger", "ai_daily_budget_usage", "system_settings", "users",
	}
	for _, tbl := range tables {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	crypter := testkit.NewCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	svc, err := NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.DatasourceIdentityProbe = func(_ context.Context, _ *gorm.DB, _ *crypto.Crypter, row datasourceTargetRow) (database.ServerIdentity, error) {
		dbName := ""
		if row.DatabaseName != nil {
			dbName = *row.DatabaseName
		}
		return database.ServerIdentity{
			EngineFamily:         row.CompatibilityMode,
			DatabaseName:         dbName,
			ConfiguredEndpointID: fmt.Sprintf("%s:%d", row.Host, row.Port),
			ServerVersion:        "8.0.36",
			InstanceMarker:       "mysql:b13-" + row.ID,
			LowerCaseTableNames:  "0",
		}, nil
	}

	var mu sync.Mutex
	seenSystems := map[string][]string{}
	runTurns := 0
	mockOpenAISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"san-4b"},{"id":"xmodel-v3.1"}]}`))
			return
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sysContent := ""
		userContent := ""
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				sysContent = m.Content
			case "user":
				userContent = m.Content
			}
		}
		mu.Lock()
		seenSystems[req.Model] = append(seenSystems[req.Model], sysContent)
		mu.Unlock()

		// Handle CallActiveProviderJSON call cleanly.
		if strings.Contains(sysContent, "custom-json-helper") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": `{"ok":"pg-json"}`}}},
			})
			return
		}

		// Parse structured review wire payload.
		trimmed := strings.TrimPrefix(userContent, "Review request JSON payload follows. Respond ONLY with a single json object matching the required JSON Schema.\n")
		var wire struct {
			ReviewRunID               string `json:"review_run_id"`
			AnonymizedStatementGroups []any  `json:"anonymized_statement_groups"`
			NormalizedEvidence        []any  `json:"normalized_evidence"`
		}
		_ = json.Unmarshal([]byte(trimmed), &wire)

		var reply string
		if wire.ReviewRunID == provider.ProbeRunID || len(wire.AnonymizedStatementGroups) == 0 {
			reply = `{"schema_version":1,"review_run_id":"` + wire.ReviewRunID + `","outcome":"complete","summary":"Probe OK","findings":[],"tool_requests":[]}`
		} else {
			mu.Lock()
			runTurns++
			turn := runTurns
			mu.Unlock()
			switch turn {
			case 1:
				// Turn 1: SAN requests raw schema/table tool arguments -> triggers translateSANToolArgsToTokens + copyNormalizedFactForSAN.
				reply = `{"schema_version":1,"review_run_id":"` + wire.ReviewRunID + `","outcome":"complete","summary":"Need table schema","findings":[],"tool_requests":[{"tool_call_id":"550e8400-e29b-41d4-a716-446655440000","tool_name":"schema_metadata","arguments":{"schema":"shop","table":"orders"}}]}`
			case 2:
				// Turn 2: Force single schema retry so schemaPending branch runs.
				reply = `{"invalid_schema":true}`
			default:
				// Final turn after retry completes cleanly.
				reply = `{"schema_version":1,"review_run_id":"` + wire.ReviewRunID + `","outcome":"complete","summary":"No blocking issues on shop.orders.","findings":[],"tool_requests":[]}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer mockOpenAISrv.Close()

	ctx := context.Background()
	actorID := uuid.NewString()
	owner := Actor{UserID: uuid.NewString(), Username: "san_owner"}

	// Invalid system_prompt_profile fails validation at create.
	if _, err := svc.CreateAIProvider(ctx, actorID, CreateAIProviderInput{
		Name: "bad-prof", ProviderKind: "openai_compatible", BaseURL: mockOpenAISrv.URL + "/v1",
		ModelName: "san-4b", Enabled: false, SelectionPriority: 9, APIKey: "sk-1",
		Configuration: map[string]any{"system_prompt_profile": "v3_raw"},
	}); err == nil {
		t.Fatal("expected invalid system_prompt_profile to be rejected")
	}

	// Create enabled SAN provider -> runs probeCandidate and auto-assigns SAN privacy hash.
	sanView, err := svc.CreateAIProvider(ctx, actorID, CreateAIProviderInput{
		Name: "san-primary", ProviderKind: "openai_compatible", BaseURL: mockOpenAISrv.URL + "/v1",
		ModelName: "san-4b", Enabled: true, SelectionPriority: 1, APIKey: "sk-san",
		Configuration: map[string]any{"system_prompt_profile": "san"},
	})
	if err != nil {
		t.Fatalf("CreateAIProvider SAN failed: %v", err)
	}
	if sanView.Configuration == nil || sanView.Configuration.SystemPromptProfile == nil || *sanView.Configuration.SystemPromptProfile != "san" {
		t.Fatalf("expected system_prompt_profile=san in view, got %+v", sanView.Configuration)
	}

	// Verify ListAIProviders, GetAIProvider, TestAIProviderConnection, ListProviderModels.
	listed, _, err := svc.ListAIProviders(ctx, 10, "", "san")
	if err != nil || len(listed) != 1 || listed[0].ID != sanView.ID {
		t.Fatalf("ListAIProviders unexpected result: len=%d err=%v", len(listed), err)
	}
	gotView, err := svc.GetAIProvider(ctx, sanView.ID)
	if err != nil || gotView.ID != sanView.ID {
		t.Fatalf("GetAIProvider failed: %v", err)
	}
	if err := svc.TestAIProviderConnection(ctx, sanView.ID); err != nil {
		t.Fatalf("TestAIProviderConnection SAN failed: %v", err)
	}
	models, err := svc.ListProviderModels(ctx, mockOpenAISrv.URL+"/v1", "", sanView.ID)
	if err != nil || len(models) != 2 {
		t.Fatalf("ListProviderModels with stored key failed: err=%v models=%+v", err, models)
	}

	// ReplaceAIProvider: switch from san -> default -> back to san (verifying config persistence & hash updates).
	if _, err := svc.ReplaceAIProvider(ctx, sanView.ID, ReplaceAIProviderInput{
		ExpectVersion: sanView.Version, Name: sanView.Name, ProviderKind: sanView.ProviderKind,
		BaseURL: sanView.BaseURL, ModelName: "san-4b", Enabled: false, SelectionPriority: 1,
		Configuration: map[string]any{"system_prompt_profile": "invalid"},
	}); err == nil {
		t.Fatal("expected invalid system_prompt_profile in ReplaceAIProvider to fail")
	}
	repDef, err := svc.ReplaceAIProvider(ctx, sanView.ID, ReplaceAIProviderInput{
		ExpectVersion: sanView.Version, Name: sanView.Name, ProviderKind: sanView.ProviderKind,
		BaseURL: sanView.BaseURL, ModelName: "xmodel-v3.1", Enabled: false, SelectionPriority: 1,
		Configuration: map[string]any{"system_prompt_profile": "default"},
	})
	if err != nil || repDef.Configuration == nil || *repDef.Configuration.SystemPromptProfile != "default" {
		t.Fatalf("ReplaceAIProvider to default failed: err=%v view=%+v", err, repDef)
	}
	repSAN, err := svc.ReplaceAIProvider(ctx, sanView.ID, ReplaceAIProviderInput{
		ExpectVersion: repDef.Version, Name: sanView.Name, ProviderKind: sanView.ProviderKind,
		BaseURL: sanView.BaseURL, ModelName: "san-4b", Enabled: true, SelectionPriority: 1,
		Configuration: map[string]any{"system_prompt_profile": "san"},
	})
	if err != nil || repSAN.Configuration == nil || *repSAN.Configuration.SystemPromptProfile != "san" {
		t.Fatalf("ReplaceAIProvider back to san failed: err=%v view=%+v", err, repSAN)
	}

	// Test CallActiveProviderJSON against the PG-persisted SAN chain.
	jsonOut, err := svc.CallActiveProviderJSON(ctx, "custom-json-helper", "hi")
	if err != nil || jsonOut != `{"ok":"pg-json"}` {
		t.Fatalf("CallActiveProviderJSON PG failed: err=%v out=%q", err, jsonOut)
	}

	// Seed datasource + rule set + flow + grant + draft for resolver & AgentRunner test.
	dsID := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b13-ds', 'mysql', 'mysql', 'native', 'db.example.com', 3306, 'shop', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ab", 32), actorID).Error; err != nil {
		t.Fatalf("seed datasource: %v", err)
	}
	ruleView, err := svc.CreateRuleSet(ctx, actorID, "b13-rules-"+uuid.NewString()[:8], true, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("CreateRuleSet: %v", err)
	}
	flowID := uuid.NewString()
	if err := db.Exec(`insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, 'b13-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, ruleView.ID, actorID).Error; err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	stageID := uuid.NewString()
	if err := db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 1, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageID, flowID, dsID, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	stepID := uuid.NewString()
	if err := db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position) values (?, ?, ?, 1)`,
		stepID, flowID, stageID).Error; err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepID, actorID).Error; err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	if err := db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`, stageID, actorID).Error; err != nil {
		t.Fatalf("seed exec actor: %v", err)
	}
	permGroupID := uuid.NewString()
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'b13-group', true, 1, ?, now(), now())`, permGroupID, actorID).Error; err != nil {
		t.Fatalf("seed perm group: %v", err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, permGroupID, owner.UserID).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, permGroupID, flowID).Error; err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	const sampleSQL = "UPDATE orders SET amount = amount + 1 WHERE id = 1;\n"
	draft, err := svc.CreateDraft(ctx, owner, CreateDraftInput{FlowID: flowID, Title: "b13 san pg"})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	draft, err = svc.ReplaceSQL(ctx, owner, draft.ID, []byte(sampleSQL), draft.Version)
	if err != nil {
		t.Fatalf("ReplaceSQL: %v", err)
	}

	// Execute AgentRunner ephemeral run -> exercises SAN tool_requests translation + copyNormalizedFactForSAN + schemaPending retry.
	runner := NewAgentRunner(svc, AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewOpenAIAdapter(ep, nil)
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return sanTestFactSource{}, func() {}, nil
		},
		RuntimeBudget: &RuntimeBudgetConfig{
			PerStageEstimateMinor: 100, MaxModelTurns: 6, MaxModelCalls: 6, MaxToolCalls: 4, MaxDurationSec: 30, PerModelTimeout: 10, PerToolTimeout: 5,
		},
	})
	runID, err := svc.StartEphemeralRun(ctx, owner, runner, EphemeralStartInput{
		FlowID: flowID, DraftID: draft.ID, SQL: []byte(sampleSQL), IdempotencyKey: "san-pg-run-1",
	})
	if err != nil {
		t.Fatalf("StartEphemeralRun: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		er := svc.EphemeralGetForTest(runID, owner.UserID)
		if er != nil && (er.State() == domain.RunStateReady || er.State() == domain.RunStateFailed || er.State() == domain.RunStatePartial || er.State() == domain.RunStateBlocked) {
			if er.State() != domain.RunStateReady {
				t.Fatalf("expected ephemeral SAN run ready, got %s", er.State())
			}
			if merr := db.Transaction(func(tx *gorm.DB) error {
				return svc.MaterializeEphemeralRunTx(ctx, tx, er, draft.ID, draft.Revision)
			}); merr != nil {
				t.Fatalf("MaterializeEphemeralRunTx: %v", merr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for SAN ephemeral run %s", runID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify DB-backed resolveRepresentativeStatementSQL + runProfileForDB with empty profileHint.
	var groupID string
	if err := db.Raw(`select id::text from sql_fingerprint_groups where review_run_id = ? limit 1`, runID).Scan(&groupID).Error; err != nil || groupID == "" {
		t.Fatalf("query materialized group_id: err=%v id=%q", err, groupID)
	}
	if prof := runProfileForDB(ctx, db, runID); prof != "mysql/mysql" {
		t.Fatalf("expected runProfileForDB=mysql/mysql, got %q", prof)
	}
	resolvedSQL, err := resolveRepresentativeStatementSQL(ctx, db, crypter, runID, groupID, "")
	if err != nil || !strings.Contains(resolvedSQL, "orders") {
		t.Fatalf("resolveRepresentativeStatementSQL from PG draft failed: err=%v sql=%q", err, resolvedSQL)
	}
	if _, err := resolveRepresentativeStatementSQL(ctx, db, crypter, runID, uuid.NewString(), ""); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("expected ErrFactSourceUnavailable for unknown group, got %v", err)
	}

	mu.Lock()
	sanPrompts := append([]string{}, seenSystems["san-4b"]...)
	mu.Unlock()
	if len(sanPrompts) < 3 {
		t.Fatalf("expected SAN prompts across probe + tool turn + retry turn, got %d", len(sanPrompts))
	}
	if !strings.Contains(sanPrompts[len(sanPrompts)-1], "flag: retry_schema") {
		t.Fatalf("expected flag: retry_schema on retried SAN system prompt, got:\n%s", sanPrompts[len(sanPrompts)-1])
	}
}
