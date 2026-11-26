//go:build integration

// b8_integration_test.go B8 acceptance gate tests (real PostgreSQL):
//
//	G1 Rule Set composition: rules is always {}, composition maintenance, stable Hash, reference deletion guard;
//	G2 Skill blocking: built-in skill bound -> model High finding -> blocked (model path);
//	G3 Stage independence: multiple stages evaluate gate_passed independently, separating clean vs risky stages;
//	G4 Flow/rule replacement invalidation: terminal Runs become outdated + ready drafts converge,
//	   while active Runs converge lazily via frozen Hash;
//	G5 SQL Hash drift -> outdated; broken coverage -> 2007 partial (fail closed);
//	G6 Determinism and non-bypassability: identical inputs yield identical conclusions, and admin is equally blocked;
//	G7 Built-in skill governance: immutable definition, state toggle allowed, deletion forbidden,
//	   and persisted Hash matches Go definition exactly.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// seedSecondStage appends a second stage to the flow (new datasource, same rule set).
func (f *fx) seedSecondStage(t *testing.T) {
	t.Helper()
	dsID := uuid.NewString()
	if err := f.db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-ds2', 'mysql', 'mysql', 'native', 'db2.example.com', 3306, 'shop2', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ef", 32), f.userID).Error; err != nil {
		t.Fatalf("seed ds2: %v", err)
	}
	stageID := uuid.NewString()
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: stage2 scope = default database 'shop2'.
	if err := f.db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 2, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageID, f.flowID, dsID, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatalf("seed stage2: %v", err)
	}
	stepID := uuid.NewString()
	if err := f.db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position)
		values (?, ?, ?, 1)`, stepID, f.flowID, stageID).Error; err != nil {
		t.Fatalf("seed step2: %v", err)
	}
	if err := f.db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`,
		stepID, f.userID).Error; err != nil {
		t.Fatalf("seed actor2: %v", err)
	}
	if err := f.db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`,
		stageID, f.userID).Error; err != nil {
		t.Fatalf("seed exec actor2: %v", err)
	}
}

// builtinDmlRequireWhere mirrors the definition of builtin.dml-require-where from migration 000009
// (test fixture; the database row is the single source of truth in production).
func builtinDmlRequireWhere() (string, PromptToolDefinition) {
	return "builtin.dml-require-where", PromptToolDefinition{
		KnowledgeText: "Rule: UPDATE or DELETE statements must include a WHERE clause. Criterion: emit this finding when where=false (statistics.unbounded_mutation=true) in the group's representative_shape and op is update or delete; do not emit when facts are missing.",
		MaxSeverity:   "high", Version: 1,
	}
}

// seedBuiltinRow inserts a built-in skill row mirroring the migration (with real config_hash) and returns its ID.
func (b *b7fx) seedBuiltinRow(t *testing.T, name string) string {
	t.Helper()
	var def PromptToolDefinition
	switch name {
	case "builtin.dml-require-where":
		_, def = builtinDmlRequireWhere()
	default:
		t.Fatalf("test builtin %s not mirrored", name)
	}
	var id string
	if err := b.db.Raw(`select id::text from prompt_tools where name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	if id != "" {
		return id
	}
	hash, err := promptToolConfigHash(name, "all", nil, def)
	if err != nil {
		t.Fatal(err)
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	id = uuid.NewString()
	if err := b.db.Exec(`
		insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, finding_key, is_builtin, created_by_user_id, created_at, updated_at)
		values (?, ?, 'enabled', 'all', '{}'::jsonb, ?, ?, 1, ?, true, '00000000-0000-0000-0000-000000000000', now(), now())`,
		id, name, defJSON, hash, name).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// bindRuleSetTool binds a tool to the fixture rule set.
func (b *b7fx) bindRuleSetTool(t *testing.T, toolID string) {
	t.Helper()
	if err := b.db.Exec(`
		insert into rule_set_prompt_tools (rule_set_id, prompt_tool_id)
		select id, ? from rule_sets limit 1`, toolID).Error; err != nil {
		t.Fatal(err)
	}
}

// runAgentSQL drives an ephemeral precheck (RCP-20261101): StartEphemeralRun -> wait for terminal ->
// materialize. The legacy DB task chain (StartRun+drain) is retired; DB row assertions work as usual after materialization.
func (b *b7fx) runAgentSQL(t *testing.T, deps AgentDeps, sql, idem string) string {
	t.Helper()
	return b.runAgentSQLActor(t, b.owner, deps, sql, idem)
}

// runAgentSQLActor is identical to runAgentSQL but initiated by a specified actor (e.g. verifying admin is equally blocked).
func (b *b7fx) runAgentSQLActor(t *testing.T, actor Actor, deps AgentDeps, sql, idem string) string {
	t.Helper()
	draft, err := b.svc.CreateDraft(context.Background(), actor, CreateDraftInput{
		FlowID: b.flowID, Title: "b8"})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	draft, err = b.svc.ReplaceSQL(context.Background(), actor, draft.ID, []byte(sql), draft.Version)
	if err != nil {
		t.Fatalf("replace sql: %v", err)
	}
	er, runID := b.startEphemeralTerminal(t, actor, deps, EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sql), IdempotencyKey: idem,
	})
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	return runID
}

// builtinDial outputs a High finding per the built-in skill (deterministic responder).
func builtinDial(key string) func(provider.Request) ([]byte, int, error) {
	return func(req provider.Request) ([]byte, int, error) {
		if len(req.AnonymizedStatementGroups) == 0 {
			return nil, 500, errors.New("no groups")
		}
		gid := req.AnonymizedStatementGroups[0].GroupID
		if len(req.NormalizedEvidence) == 0 {
			requests, err := scopedMetadataToolRequests(req, uuid.NewString())
			if err != nil {
				return nil, 500, err
			}
			if len(requests) > 0 {
				body := map[string]any{
					"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
					"summary": "Continue review after reading table schema.", "findings": []any{},
					"tool_requests": requests,
				}
				raw, err := json.Marshal(body)
				if err != nil {
					return nil, 500, err
				}
				return raw, 200, nil
			}
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.", "findings": []map[string]any{{
				"finding_key": key, "category": "correctness", "severity": "high",
				"title": "Built-in skill violation", "message": "Statement group violates built-in skill rule.", "suggestion": "Fix according to recommendation and re-run precheck.",
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

// cleanDial is a zero-finding responder.
func cleanDial(req provider.Request) ([]byte, int, error) {
	body := map[string]any{
		"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
		"summary": "Review passed.", "findings": []map[string]any{}, "tool_requests": []map[string]any{},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 500, err
	}
	return raw, 200, nil
}

// cleanEvidenceDial returns a schema_metadata tool request for referenced objects on turn 1 and a
// zero-finding conclusion on turn 2: model driver for "benign SQL -> ready" on the ephemeral path
// (a zero-tool table review would converge to partial/2007, so evidence must be collected first).
func cleanEvidenceDial(req provider.Request) ([]byte, int, error) {
	if len(req.NormalizedEvidence) == 0 {
		requests, err := scopedMetadataToolRequests(req, uuid.NewString())
		if err != nil {
			return nil, 500, err
		}
		if len(requests) > 0 {
			raw, err := json.Marshal(map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Continue review after reading table schema.", "findings": []any{}, "tool_requests": requests,
			})
			if err != nil {
				return nil, 500, err
			}
			return raw, 200, nil
		}
	}
	return cleanDial(req)
}

func TestB8Gate1RuleSetComposition(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Non-empty rules object is always rejected (fail closed).
	for _, bad := range []string{`{"forbid_drop": {"enabled": true, "severity": "high"}}`, `[]`, `null`, ``} {
		if _, err := f.svc.CreateRuleSet(ctx, f.userID, "b8-bad", true, json.RawMessage(bad), nil); err == nil {
			t.Fatalf("rules %q must be rejected", bad)
		}
	}

	// Composition: create a user skill and bind it.
	tool, err := f.svc.CreatePromptTool(ctx, f.userID, "b8-tool", "enabled", PromptToolDefinition{
		KnowledgeText: "Index changes must evaluate table lock window", MaxSeverity: "medium", Version: 1,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}
	view, err := f.svc.CreateRuleSet(ctx, f.userID, "b8-g1", true, []byte(`{}`), []string{tool.ID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Version != 1 || len(view.PromptToolIDs) != 1 || string(view.Rules) != "{}" {
		t.Fatalf("view mismatch: %+v", view)
	}
	got, err := f.svc.GetRuleSet(ctx, view.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PromptToolIDs[0] != tool.ID {
		t.Fatalf("get mismatch: %+v", got)
	}

	// Replacement: renaming (composition unchanged) -> stable Hash; toggling enabled -> Hash changes + version increments.
	same, err := f.svc.ReplaceRuleSet(ctx, view.ID, view.Version, "b8-g1-renamed", true, []byte(`{}`), []string{tool.ID})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if same.ConfigHash != view.ConfigHash {
		t.Fatalf("metadata-only rename must keep hash: %s vs %s", same.ConfigHash, view.ConfigHash)
	}
	changed, err := f.svc.ReplaceRuleSet(ctx, same.ID, same.Version, "b8-g1-renamed", false, []byte(`{}`), []string{tool.ID})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if changed.ConfigHash == view.ConfigHash || changed.Version != same.Version+1 {
		t.Fatalf("enabled flip must change hash and bump version: %+v", changed)
	}

	// Reference deletion guard: attached to a flow -> ErrRuleSetReferenced; deletable after detaching.
	if err := f.db.Exec(`update flows set rule_set_id = ? where id = ?`, changed.ID, f.flowID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteRuleSet(ctx, changed.ID, changed.Version); !errors.Is(err, ErrRuleSetReferenced) {
		t.Fatalf("want referenced, got %v", err)
	}
	other, err := f.svc.CreateRuleSet(ctx, f.userID, "b8-g1-other", true, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`update flows set rule_set_id = ? where id = ?`, other.ID, f.flowID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteRuleSet(ctx, changed.ID, changed.Version); err != nil {
		t.Fatalf("delete after unreference: %v", err)
	}
}

// TestB8ParameterRenderingRealPath the real injection chain (BuildRequest ->
// SystemPrompt) must render parameter placeholders (critical regression: RenderSkillInjection
// previously omitted the RenderParameters call).
func TestB8ParameterRenderingRealPath(t *testing.T) {
	inj := agent.SkillInjection{
		ToolID: "t1", Name: "builtin.max-affect-rows", Engine: "all",
		Parameters:    map[string]any{"max_affect_rows": int64(100000)},
		KnowledgeText: "Rule: emit finding when estimated affected rows exceed {{max_affect_rows}}.",
		ConfigHash:    "h",
	}
	rendered := agent.RenderSkillInjection(inj)
	if !strings.Contains(rendered, "100000") || strings.Contains(rendered, "{{") {
		t.Fatalf("real-path rendering missing parameter: %s", rendered)
	}
	// Missing parameters fall back to "not configured" without leaving raw placeholders.
	inj.Parameters = nil
	rendered = agent.RenderSkillInjection(inj)
	if strings.Contains(rendered, "{{") || !strings.Contains(rendered, "not configured") {
		t.Fatalf("missing parameter fallback broken: %s", rendered)
	}
}

func TestB8Gate2SkillBlockingViaModel(t *testing.T) {
	b := newB7Fixture(t)
	// Bind the built-in skill (real definition + Hash) to the fixture rule set.
	builtinID := b.seedBuiltinRow(t, "builtin.dml-require-where")
	b.bindRuleSetTool(t, builtinID)

	injected := false
	wrap := func(req provider.Request) ([]byte, int, error) {
		if strings.Contains(req.SystemPrompt, "builtin.dml-require-where") &&
			strings.Contains(req.SystemPrompt, "unbounded_mutation") &&
			strings.Contains(req.SystemPrompt, "finding_key: builtin.dml-require-where") &&
			strings.Contains(req.SystemPrompt, "effective_severity: critical") {
			injected = true // Deterministic injection reaches the model context
		}
		return builtinDial("builtin.dml-require-where")(req)
	}
	runID := b.runAgentSQL(t, b.fakeDial(wrap), "update orders set amount = 0;\n", "b8-g2-skill")
	if !injected {
		t.Fatal("builtin skill knowledge not injected into provider context")
	}
	var state string
	var biz *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &biz); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStateBlocked) || biz == nil || *biz != 2008 {
		t.Fatalf("want blocked/2008, got %s/%v", state, biz)
	}
	var stageState string
	var stageGate bool
	if err := b.db.Raw(`select state, gate_passed from stage_review_snapshots where review_run_id = ?`,
		runID).Row().Scan(&stageState, &stageGate); err != nil {
		t.Fatal(err)
	}
	if stageState != "blocked" || stageGate {
		t.Fatalf("stage %s gate=%v", stageState, stageGate)
	}
	// Once an injected mandatory rule is actually violated, even a raw High returned by the model must
	// be normalized to Critical.
	if n := count(t, b.db, `select count(*) from review_findings f
		join stage_review_snapshots s on s.id = f.stage_review_id
		where s.review_run_id = ? and f.severity = 'critical'`, runID); n != 1 {
		t.Fatalf("skill finding want 1, got %d", n)
	}
}

func TestB8DisabledRuleSetDoesNotInjectSkills(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	var ruleSetID string
	if err := b.db.Raw(`
		select rs.id::text
		from rule_sets rs
		join flows f on f.rule_set_id = rs.id
		where f.id = ?`, b.flowID).Scan(&ruleSetID).Error; err != nil {
		t.Fatal(err)
	}
	view, err := b.svc.GetRuleSet(ctx, ruleSetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.ReplaceRuleSet(ctx, view.ID, view.Version, view.Name, false,
		view.Rules, view.PromptToolIDs); err != nil {
		t.Fatalf("disable rule set: %v", err)
	}
	_, toolHash, err := b.svc.ruleConfigHashes(ctx, b.db, &ruleSetID)
	if err != nil {
		t.Fatal(err)
	}
	var knowledgeHashes []string
	if err := b.db.Raw(`select 'knowledge_entry:' || id::text || ':' || config_hash
		from knowledge_entries where state = 'enabled' and scope_type <> 'global' order by id`).Scan(&knowledgeHashes).Error; err != nil {
		t.Fatal(err)
	}
	if len(knowledgeHashes) == 0 || toolHash != domain.EffectiveToolConfigHash(knowledgeHashes) {
		t.Fatalf("disabled rule set must omit tools while preserving enabled knowledge policy identity, got %s", toolHash)
	}

	injected := false
	wrap := func(req provider.Request) ([]byte, int, error) {
		if strings.Contains(req.SystemPrompt, "must-where") {
			injected = true
		}
		return cleanDial(req)
	}
	_ = b.runAgentSQL(t, b.fakeDial(wrap), "select 1;\n", "b8-disabled-rule-set")
	if injected {
		t.Fatal("disabled rule set must not inject its prompt tools")
	}
}

func TestB8Gate3PerStageIndependence(t *testing.T) {
	b := newB7Fixture(t)
	b.fx.seedSecondStage(t)

	// Clean response: all stages pass individually -> run ready.
	clean := b.runAgentSQL(t, b.fakeDial(cleanDial), "select 1;\n", "b8-g3-clean")
	run1, err := b.svc.GetRunOwned(context.Background(), b.owner, clean)
	if err != nil {
		t.Fatal(err)
	}
	if run1.State != string(domain.RunStateReady) || !run1.Gate.Passed {
		t.Fatalf("clean run want ready/pass, got %s %+v", run1.State, run1.Gate)
	}
	stages1 := stageGateRows(t, b.db, clean)
	if len(stages1) != 2 || !stages1[1] || !stages1[2] {
		t.Fatalf("per-stage gate want both passed: %+v", stages1)
	}

	// Violating response: all stages block individually -> run blocked (using the skill.nowhere allowlisted key
	// already bound in b7fx; after hardening, unknown finding_keys are capped at Medium and no longer block).
	risky := b.runAgentSQL(t, b.fakeDial(builtinDial("skill.nowhere")), "update orders set amount = 0;\n", "b8-g3-risky")
	run2, _ := b.svc.GetRunOwned(context.Background(), b.owner, risky)
	if run2.State != string(domain.RunStateBlocked) || run2.Gate.Passed {
		t.Fatalf("risky run want blocked/fail, got %s %+v", run2.State, run2.Gate)
	}
	stages2 := stageGateRows(t, b.db, risky)
	if stages2[1] || stages2[2] {
		t.Fatalf("per-stage gate want both blocked: %+v", stages2)
	}
}

// stageGateRows maps stage position -> gate_passed.
func stageGateRows(t *testing.T, db *gorm.DB, runID string) map[int]bool {
	t.Helper()
	var rows []struct {
		Position int
		Gate     bool
	}
	if err := db.Raw(`select stage_position as position, gate_passed as gate
		from stage_review_snapshots where review_run_id = ? order by stage_position`, runID).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[int]bool{}
	for _, r := range rows {
		out[r.Position] = r.Gate
	}
	return out
}

// TestB8WrapperStateToggleOnBuiltin the B7 compatibility wrapper (defaulting omitted engine/params to
// persisted row values) supports pure state toggling on mysql built-in rows and parameterized rows,
// keeping config_hash identical to recomputation from persisted row data (regression pinned from closure verification).
func TestB8WrapperStateToggleOnBuiltin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	def := PromptToolDefinition{
		KnowledgeText: "Rule: auto-increment columns must be declared UNSIGNED. {{max_loops}} is a placeholder.",
		MaxSeverity:   "medium", Version: 1,
	}
	params := map[string]any{"max_loops": int64(7)}
	hash, err := promptToolConfigHash("builtin.test-unsigned", "mysql", params, def)
	if err != nil {
		t.Fatal(err)
	}
	defJSON, _ := json.Marshal(def)
	paramsJSON, _ := json.Marshal(params)
	if err := f.db.Exec(`
		insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, finding_key, is_builtin, created_by_user_id, created_at, updated_at)
		values (?, 'builtin.test-unsigned', 'enabled', 'mysql', ?::jsonb, ?, ?, 1, 'builtin.test-unsigned', true, '00000000-0000-0000-0000-000000000000', now(), now())`,
		uuid.NewString(), string(paramsJSON), string(defJSON), hash).Error; err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.db.Raw(`select id::text from prompt_tools where name = 'builtin.test-unsigned'`).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	// B7 wrapper: pure state toggle (omitted engine/params reuse persisted row values).
	view, err := f.svc.GetPromptTool(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	off, err := f.svc.ReplacePromptTool(ctx, id, view.Version, view.Name, "disabled", view.Definition)
	if err != nil {
		t.Fatalf("wrapper state toggle on mysql builtin must pass: %v", err)
	}
	// config_hash must equal the hash recomputed from persisted row data (engine/params/def).
	reread, err := f.svc.GetPromptTool(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	recomputed, err := promptToolConfigHash(reread.Name, reread.Engine, reread.Parameters, reread.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if reread.ConfigHash != recomputed {
		t.Fatalf("config_hash drift after wrapper toggle: row=%s recomputed=%s", reread.ConfigHash, recomputed)
	}
	if reread.Parameters["max_loops"] != int64(7) {
		t.Fatalf("params must be preserved: %+v", reread.Parameters)
	}
	if off.State != "disabled" || off.Version != view.Version+1 {
		t.Fatalf("toggle view mismatch: %+v", off)
	}
}

// TestB8RuleSetInvalidationScopedToRuleSet toggling enabled on rule set A must not falsely invalidate
// unsubmitted Runs of rule set B (which shares the same hash when enabled is identical) (false-invalidation bug caught during closure verification).
// RCP-20261101: Runs participate in invalidation after ephemeral precheck materialization; draft-state assertions retired with the state machine.
func TestB8RuleSetInvalidationScopedToRuleSet(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	// Draft + ready Run (fixture rule set A).
	sqlA := "update orders set amount = amount + 1 where id = 1;\n"
	draftA := b.createDraftWithSQL(t, sqlA)
	erA, runA := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
		FlowID: b.flowID, DraftID: draftA.ID, SQL: []byte(sqlA), IdempotencyKey: "b8-scope"})
	if erA.State() != domain.RunStateReady {
		t.Fatalf("precondition ready, got %s", erA.State())
	}
	b.materializeEphemeral(t, erA, draftA.ID, draftA.Revision)
	// Rule set B (same enabled=true -> same config_hash), new flow bound to B + new draft.
	rsB, err := b.svc.CreateRuleSet(ctx, b.userID, "b8-scope-b", true, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	dsB := uuid.NewString()
	if err := b.db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-ds-b', 'mysql', 'mysql', 'native', 'dbb.example.com', 3306, 'shopB', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsB, strings.Repeat("cd", 32), b.userID).Error; err != nil {
		t.Fatal(err)
	}
	flowB := uuid.NewString()
	if err := b.db.Exec(`insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-flow-b', 'change_review', 'enabled', ?, 1, ?, now(), now())`, flowB, rsB.ID, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	stageB := uuid.NewString()
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: flowB scope = default database 'shopB'.
	if err := b.db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 1, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageB, flowB, dsB, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatal(err)
	}
	stepB := uuid.NewString()
	if err := b.db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position) values (?, ?, ?, 1)`,
		stepB, flowB, stageB).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepB, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`, stageB, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	groupB := uuid.NewString()
	if err := b.db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'b8-group-b', true, 1, ?, now(), now())`, groupB, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, groupB, b.owner.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, groupB, flowB).Error; err != nil {
		t.Fatal(err)
	}
	sqlB := "update orders set amount = amount + 2 where id = 2;\n"
	draftB, err := b.svc.CreateDraft(ctx, b.owner, CreateDraftInput{FlowID: flowB, Title: "b8-b"})
	if err != nil {
		t.Fatal(err)
	}
	draftB, err = b.svc.ReplaceSQL(ctx, b.owner, draftB.ID, []byte(sqlB), draftB.Version)
	if err != nil {
		t.Fatal(err)
	}
	erB, runB := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
		FlowID: flowB, DraftID: draftB.ID, SQL: []byte(sqlB), IdempotencyKey: "b8-scope-b"})
	if erB.State() != domain.RunStateReady {
		t.Fatalf("precondition B ready, got %s", erB.State())
	}
	b.materializeEphemeral(t, erB, draftB.ID, draftB.Revision)
	// Toggle enabled on A: B's Run must remain unaffected.
	var ruleSetA string
	if err := b.db.Raw(`select rs.id::text from rule_sets rs join flows f on f.rule_set_id = rs.id where f.id = ?`, b.flowID).Row().Scan(&ruleSetA); err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.ReplaceRuleSet(ctx, ruleSetA, 1, "b6-rules", false, []byte(`{}`), nil); err != nil {
		t.Fatalf("replace A: %v", err)
	}
	runAfter, _ := b.svc.GetRunOwned(ctx, b.owner, runA)
	if runAfter.State != string(domain.RunStateOutdated) {
		t.Fatalf("A run want outdated, got %s", runAfter.State)
	}
	runBAfter, _ := b.svc.GetRunOwned(ctx, b.owner, runB)
	if runBAfter.State != string(domain.RunStateReady) {
		t.Fatalf("B run must stay ready (scoped invalidation), got %s", runBAfter.State)
	}
}

// TestB8UnknownFindingKeyCappedToMedium governance tiering: a High finding with an unbound finding_key
// is downgraded to a Medium Agent Observation — visible without blocking submission.
func TestB8UnknownFindingKeyCappedToMedium(t *testing.T) {
	b := newB7Fixture(t)
	// Bind no skill/knowledge containing that key: dial outputs an ungoverned key.
	runID := b.runAgentSQL(t, b.fakeDial(builtinDial("unregistered.key")), "update orders set amount = 0;\n", "b8-unknown-key")
	var state string
	var biz *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &biz); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStateReady) || biz != nil {
		t.Fatalf("unknown-key high must not block, got %s/%v", state, biz)
	}
	var sev string
	if err := b.db.Raw(`select f.severity from review_findings f
		join stage_review_snapshots s on s.id = f.stage_review_id
		where s.review_run_id = ? limit 1`, runID).Row().Scan(&sev); err != nil {
		t.Fatal(err)
	}
	if sev != "medium" {
		t.Fatalf("unknown-key high must land as medium observation, got %s", sev)
	}
}

// TestB8Gate4InvalidationOnRuleAndFlowChange rule/flow replacement invalidates materialized Runs.
// RCP-20261101: Runs participate in invalidation after ephemeral precheck materialization; draft-state
// assertions retired with the state machine; legacy "lazy convergence of active Runs" (re-verifying
// frozen Hashes when the review task starts) retired with the DB task chain — on the ephemeral path,
// policy freezing and review complete within the same in-memory boundary with no mid-flight drift window.
func TestB8Gate4InvalidationOnRuleAndFlowChange(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	startReady := func(key string) string {
		t.Helper()
		sql := "update orders set amount = amount + 1 where id = 1;\n"
		draft := b.createDraftWithSQL(t, sql)
		er, runID := b.startEphemeralTerminal(t, b.owner, b.fakeDial(cleanEvidenceDial), EphemeralStartInput{
			FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sql), IdempotencyKey: key})
		if er.State() != domain.RunStateReady {
			t.Fatalf("precondition ready, got %s (code=%v)", er.State(), ephemeralRunCode(er))
		}
		b.materializeEphemeral(t, er, draft.ID, draft.Revision)
		return runID
	}

	// Terminal ready Run.
	runID := startReady("b8-g4")
	runBefore, _ := b.svc.GetRunOwned(ctx, b.owner, runID)
	if runBefore.State != string(domain.RunStateReady) {
		t.Fatalf("precondition ready, got %s", runBefore.State)
	}
	var ruleSetID string
	if err := b.db.Raw(`select rs.id::text from rule_sets rs
		join flows fl on fl.rule_set_id = rs.id where fl.id = ?`, b.flowID).Row().Scan(&ruleSetID); err != nil {
		t.Fatal(err)
	}
	// Rule set enabled toggle (config_hash changes) -> old-Hash Run becomes outdated.
	if _, err := b.svc.ReplaceRuleSet(ctx, ruleSetID, 1, "b6-rules", false, []byte(`{}`), nil); err != nil {
		t.Fatalf("replace rule: %v", err)
	}
	runAfter, _ := b.svc.GetRunOwned(ctx, b.owner, runID)
	if runAfter.State != string(domain.RunStateOutdated) {
		t.Fatalf("run want outdated after rule replace, got %s", runAfter.State)
	}
	if runAfter.FailureCode != nil {
		t.Fatalf("outdated run must not carry failure fields: %+v", runAfter.FailureCode)
	}

	// Flow template replacement invalidation (target function; ReplaceFlow wiring is verified in package
	// workflow integration tests — importing workflow in application tests would create a cycle).
	runID2 := startReady("b8-g4-flow")
	err := b.db.Transaction(func(tx *gorm.DB) error {
		return InvalidateRunsForFlow(ctx, tx, b.flowID)
	})
	if err != nil {
		t.Fatalf("invalidate for flow: %v", err)
	}
	run2After, _ := b.svc.GetRunOwned(ctx, b.owner, runID2)
	if run2After.State != string(domain.RunStateOutdated) {
		t.Fatalf("run2 want outdated after flow change, got %s", run2After.State)
	}
}

// TestB8Gate5HashTamperAndCoverageFailClosed removed under RCP-20261101.
// Legacy premise: the DB task chain left a tamper window between the grouping and review tasks — when
// the review task started, it re-read draft_sql_payloads.plaintext_hmac to re-verify (tamper -> outdated)
// and rechecked the sql_fingerprint_groups row count (deleted group -> 2007 partial). Ephemeral precheck
// converges grouping and review into the same in-memory boundary (er.SQLHMAC/er.Groups are frozen in
// StartEphemeralRun, and DB rows appear only upon materialization), eliminating the mid-flight tamper
// vector; submit-side "review A submit B" HMAC comparison is covered by httpapi dml_commit_gate
// integration tests, and the coverage guard (2007) is covered by TestB7ZeroToolCompleteIsPartialForTableReview and the R012 suite.

func TestB8Gate6DeterminismAndNoBypass(t *testing.T) {
	b := newB7Fixture(t)

	// Two independent prechecks on the same SQL (with the same deterministic responder): conclusions are identical.
	outcomes := make([][]string, 0, 2)
	var severities [][]string
	for i := 0; i < 2; i++ {
		runID := b.runAgentSQL(t, b.fakeDial(builtinDial("skill.nowhere")),
			"update orders set amount = 0;\n", "b8-g6-"+strings.Repeat(string(rune('a'+i)), 2))
		run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State != string(domain.RunStateBlocked) {
			t.Fatalf("run %d want blocked, got %s", i, run.State)
		}
		codes := append([]string(nil), run.Gate.ReasonCodes...)
		sortStrings(codes)
		outcomes = append(outcomes, codes)
		var sevs []string
		_ = b.db.Raw(`select f.severity from review_findings f
			join stage_review_snapshots s on s.id = f.stage_review_id
			where s.review_run_id = ? order by f.severity`, runID).Scan(&sevs).Error
		severities = append(severities, sevs)
	}
	if strings.Join(outcomes[0], ",") != strings.Join(outcomes[1], ",") {
		t.Fatalf("gate reason codes diverged: %v vs %v", outcomes[0], outcomes[1])
	}
	if strings.Join(severities[0], ",") != strings.Join(severities[1], ",") {
		t.Fatalf("finding severities diverged: %v vs %v", severities[0], severities[1])
	}

	// Admin is equally blocked: initiated by an authorized admin, producing the exact same conclusion.
	admin := Actor{UserID: uuid.NewString(), Username: "boss" + uuid.NewString()[:6]}
	if err := b.db.Exec(`insert into users (id, username, display_name, is_builtin_admin, created_at, updated_at)
		values (?, ?, 'Boss', false, now(), now())`, admin.UserID, admin.Username).Error; err != nil {
		t.Fatal(err)
	}
	var groupID string
	if err := b.db.Raw(`select permission_group_id::text from permission_group_flow_grants where flow_id = ? limit 1`,
		b.flowID).Row().Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`,
		groupID, admin.UserID).Error; err != nil {
		t.Fatal(err)
	}
	adminRunID := b.runAgentSQLActor(t, admin, b.fakeDial(builtinDial("skill.nowhere")), "update orders set amount = 0;\n", "b8-g6-admin")
	adminRun, _ := b.svc.GetRunOwned(context.Background(), admin, adminRunID)
	if adminRun.State != string(domain.RunStateBlocked) || adminRun.Gate.Passed {
		t.Fatalf("admin run must hit same block boundary: %s %+v", adminRun.State, adminRun.Gate)
	}
}

func TestB8Gate7BuiltinSkillGovernance(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Built-in row: carries tunable parameters (threshold class), with the DB as the sole persistence carrier.
	def := PromptToolDefinition{
		KnowledgeText: "Rule: estimated affected rows must not exceed {{max_affect_rows}}. Criterion: call safe_explain to obtain EstimatedRows; emit this finding when EstimatedRows exceeds {{max_affect_rows}}; do not emit when unavailable and mark unknown.",
		MaxSeverity:   "high", Version: 1,
	}
	b := &b7fx{fx: f}
	hash, err := promptToolConfigHash("builtin.max-affect-rows", "all", map[string]any{"max_affect_rows": int64(1000)}, def)
	if err != nil {
		t.Fatal(err)
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var builtinID string
	if err := f.db.Exec(`
		insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, finding_key, is_builtin, created_by_user_id, created_at, updated_at)
		values (?, 'builtin.max-affect-rows', 'enabled', 'all', ?::jsonb, ?, ?, 1, 'builtin.max-affect-rows', true, '00000000-0000-0000-0000-000000000000', now(), now())`,
		uuid.NewString(), `{"max_affect_rows":1000}`, defJSON, hash).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw(`select id::text from prompt_tools where name = 'builtin.max-affect-rows'`).Scan(&builtinID).Error; err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.GetPromptTool(ctx, builtinID)
	if err != nil {
		t.Fatalf("get builtin: %v", err)
	}
	if !view.IsBuiltin || view.State != "enabled" || view.Parameters["max_affect_rows"] != int64(1000) {
		t.Fatalf("builtin view mismatch: %+v", view)
	}

	// Definition modifications are rejected (any change to knowledge text / template / allowlist).
	mutated := view.Definition
	mutated.KnowledgeText = "tampered"
	if _, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, view.Version, view.Name, "enabled", "all", view.Parameters, mutated, ""); err == nil {
		t.Fatal("builtin definition change must be rejected")
	}
	// Renaming or changing engine is rejected.
	if _, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, view.Version, "renamed", "enabled", "all", view.Parameters, view.Definition, ""); err == nil {
		t.Fatal("builtin rename must be rejected")
	}
	if _, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, view.Version, view.Name, "enabled", "mysql", view.Parameters, view.Definition, ""); err == nil {
		t.Fatal("builtin engine change must be rejected")
	}
	// Unknown parameter keys or type drift are rejected.
	if _, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, view.Version, view.Name, "enabled", "all", map[string]any{"unknown_key": int64(1)}, view.Definition, ""); err == nil {
		t.Fatal("unknown parameter key must be rejected")
	}
	if _, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, view.Version, view.Name, "enabled", "all", map[string]any{"max_affect_rows": "not-a-number"}, view.Definition, ""); err == nil {
		t.Fatal("parameter type drift must be rejected")
	}

	// Parameters are tunable (owner ruling example: changing default 1000 to 100000) — Hash changes +
	// version increments, and the modification is persisted in the DB row.
	tuned, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, view.Version, view.Name, "enabled", "all",
		map[string]any{"max_affect_rows": int64(100000)}, view.Definition, "")
	if err != nil {
		t.Fatalf("parameter tuning must pass: %v", err)
	}
	if tuned.Parameters["max_affect_rows"] != int64(100000) || tuned.ConfigHash == view.ConfigHash || tuned.Version != view.Version+1 {
		t.Fatalf("tuned view mismatch: %+v", tuned)
	}
	reread, err := f.svc.GetPromptTool(ctx, builtinID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Parameters["max_affect_rows"] != int64(100000) {
		t.Fatalf("tuning not persisted: %+v", reread.Parameters)
	}
	// Rendering: injection text replaces placeholders with current parameter values.
	rendered := renderPromptToolInjection(view.Name, reread.Parameters, reread.Definition)
	if !strings.Contains(rendered, "100000") || strings.Contains(rendered, "{{") {
		t.Fatalf("parameter not rendered: %s", rendered)
	}

	// State toggling is permitted; deletion is rejected.
	disabled, err := f.svc.ReplacePromptToolForEngine(ctx, builtinID, reread.Version, view.Name, "disabled", "all", reread.Parameters, view.Definition, "")
	if err != nil {
		t.Fatalf("state-only replace must pass: %v", err)
	}
	if disabled.State != "disabled" || disabled.Version != reread.Version+1 {
		t.Fatalf("state transition view mismatch: %+v", disabled)
	}
	if err := f.svc.DeletePromptTool(ctx, builtinID, disabled.Version); err == nil {
		t.Fatal("builtin delete must be rejected")
	}

	// User-defined custom skills are unaffected (can be deleted normally).
	tool, err := f.svc.CreatePromptTool(ctx, f.userID, "b8-user-tool", "draft", PromptToolDefinition{
		KnowledgeText: "Standard user skill", MaxSeverity: "low", Version: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeletePromptTool(ctx, tool.ID, tool.Version); err != nil {
		t.Fatalf("user tool delete: %v", err)
	}
	_ = b
}

// TestB8MigrationSkillPackageIntegrity integrity of migration 000009 seed data:
// config_hash of every built-in row matches Go-side recomputation from persisted row data (pinning
// equivalence between Python hashing in migration SQL and Go Marshal), and every definition passes the enablement gate.
// Uses an isolated evidence DB (not truncated by test fixtures).
func TestB8MigrationSkillPackageIntegrity(t *testing.T) {
	dsn := testenv.RequireB8EvidenceDSN(t)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	var rows []struct {
		Name       string
		Engine     string
		Parameters []byte
		Definition []byte
		ConfigHash string
		State      string
	}
	if err := db.Raw(`select name, coalesce(engine,'all') as engine, parameters, definition, config_hash, state
		from prompt_tools where is_builtin order by name`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 48 {
		t.Fatalf("builtin package want 48 skills, got %d", len(rows))
	}
	mysqlCount := 0
	for _, r := range rows {
		def := PromptToolDefinition{}
		if err := json.Unmarshal(r.Definition, &def); err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		params := map[string]any{}
		if len(r.Parameters) > 0 {
			_ = json.Unmarshal(r.Parameters, &params)
		}
		hash, err := promptToolConfigHash(r.Name, r.Engine, params, def)
		if err != nil {
			t.Fatal(err)
		}
		if hash != r.ConfigHash {
			t.Fatalf("builtin %s hash drift: db=%s go=%s", r.Name, r.ConfigHash, hash)
		}
		if r.Engine == "mysql" {
			mysqlCount++
		}
		if r.State == "enabled" {
			if err := RunPromptToolEval(r.Name, params, def); err != nil {
				t.Fatalf("%s eval: %v", r.Name, err)
			}
		}
	}
	if mysqlCount != 8 {
		t.Fatalf("mysql-engine builtins want 8, got %d", mysqlCount)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
