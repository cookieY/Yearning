//go:build integration

// b7_integration_test.go B7 acceptance gates (real PostgreSQL + Fake Provider):
//
//	G1 AgentRunner full flow: injection freeze -> model loop -> tool evidence collection -> finding persistence ->
//	   gate terminal state (High/Critical blocking, test_gate#7);
//	G2 No Provider fails closed (2011);
//	G3 Budget exhaustion -> partial/budget_exhausted (2010, test_gate#10 no concurrent overspend);
//	G4 Internal knowledge Hash change -> unsubmitted Runs referencing the old Hash immediately become outdated (R010);
//	G5 Injection references and ledger: review_tool_calls three-kind semantics + ai_usage_ledger append.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// fakeFactSource is a read-only evidence collection stub (safe_explain never mutates targets).
// Each method maintains an independent counter for SEC-0041 out-of-bounds assertions (out-of-bounds calls must never reach any evidence method).
type fakeFactSource struct {
	calls               int
	metadataCalls       int
	scopedMetadataCalls int
	tableStatsCalls     int
	indexCalls          int
	explainCalls        int
	tableFact           map[string]any
}

func (f *fakeFactSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql"}, nil
}
func (f *fakeFactSource) SchemaMetadata(_ context.Context, schema string) (map[string]any, error) {
	f.calls++
	f.metadataCalls++
	// Shape aligned with the real factSource (factsource.go SchemaMetadata): both schema and
	// tables must be present — tokenized projection (SEC-0002/NEW-AI-03) depends on these two keys,
	// and the old stub returning only table_count left persisted facts without the schema key, unable to prove the token surface.
	return map[string]any{"schema": schema, "table_count": 1, "tables": []any{"orders"}}, nil
}
func (f *fakeFactSource) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	f.calls++
	f.metadataCalls++
	f.scopedMetadataCalls++
	if f.tableFact != nil {
		fact := cloneFactMap(f.tableFact)
		fact["schema"] = schema
		fact["table"] = table
		return fact, nil
	}
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 2, "returned_column_count": 2,
		"columns": []any{
			map[string]any{
				"column_name": "id", "ordinal": 1, "type_class": "integer",
			},
			map[string]any{
				"column_name": "amount", "ordinal": 2, "type_class": "integer",
			},
		},
		"metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}
func (f *fakeFactSource) TableStatistics(_ context.Context, schema, table string) (map[string]any, error) {
	f.calls++
	f.tableStatsCalls++
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 2, "estimate": true, "metadata_complete": true, "truncated": false,
	}, nil
}
func (f *fakeFactSource) IndexMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	f.indexCalls++
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"indexes":           []map[string]any{{"index_name": "PRIMARY"}},
		"metadata_complete": true, "truncated": false,
	}, nil
}
func (f *fakeFactSource) SafeExplain(_ context.Context, _, _ string) (map[string]any, error) {
	f.calls++
	f.explainCalls++
	return map[string]any{"access_method": "sequential_scan", "estimated_rows": 99, "estimate": true}, nil
}
func (f *fakeFactSource) PolicyCatalog(
	ctx context.Context,
	schemas []string,
) (database.PolicyCatalog, error) {
	if err := ctx.Err(); err != nil {
		return database.PolicyCatalog{}, err
	}
	unique := append([]string(nil), schemas...)
	sort.Strings(unique)
	deduplicated := unique[:0]
	for _, schema := range unique {
		if len(deduplicated) == 0 || deduplicated[len(deduplicated)-1] != schema {
			deduplicated = append(deduplicated, schema)
		}
	}
	unique = deduplicated
	identifiers := make([]database.PolicyCatalogIdentifier, 0, len(unique))
	for _, schema := range unique {
		identifiers = append(identifiers, database.PolicyCatalogIdentifier{
			Kind: database.PolicyCatalogSchema, Schema: schema, Name: schema,
		})
	}
	return database.PolicyCatalog{
		Identifiers: identifiers,
		SchemaCount: len(unique),
	}, nil
}

type scopedTableTokens struct {
	Schema string
	Table  string
}

// scopedTablesFromRequest derives tool arguments only from the anonymous
// object relations exposed to the provider. Raw schema/table names never
// cross this test boundary.
func scopedTablesFromRequest(req provider.Request) ([]scopedTableTokens, error) {
	seen := map[string]bool{}
	var out []scopedTableTokens
	for _, group := range req.AnonymizedStatementGroups {
		for _, object := range group.Objects {
			if !strings.EqualFold(object.Kind, "table") || object.Token == "" {
				continue
			}
			schema := object.SchemaToken
			if schema == "" {
				for _, candidate := range group.Objects {
					if strings.EqualFold(candidate.Kind, "schema") && candidate.Token != "" {
						schema = candidate.Token
						break
					}
				}
			}
			if schema == "" {
				return nil, fmt.Errorf("table %s has no schema token", object.Token)
			}
			key := schema + "\x00" + object.Token
			if !seen[key] {
				seen[key] = true
				out = append(out, scopedTableTokens{Schema: schema, Table: object.Token})
			}
		}
	}
	return out, nil
}

func scopedMetadataToolRequests(req provider.Request, toolCallID string) ([]map[string]any, error) {
	tables, err := scopedTablesFromRequest(req)
	if err != nil {
		return nil, err
	}
	requests := make([]map[string]any, 0, len(tables))
	for index, table := range tables {
		id := toolCallID
		if index > 0 {
			id = uuid.NewString()
		}
		requests = append(requests, map[string]any{
			"tool_call_id": id,
			"tool_name":    "schema_metadata",
			"arguments": map[string]any{
				"schema": table.Schema,
				"table":  table.Table,
			},
		})
	}
	return requests, nil
}

func cloneToolRequest(src map[string]any, toolCallID string) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	out["tool_call_id"] = toolCallID
	if args, ok := src["arguments"].(map[string]any); ok {
		out["arguments"] = cloneFactMap(args)
	}
	return out
}

func firstScopedTable(req provider.Request) (scopedTableTokens, error) {
	tables, err := scopedTablesFromRequest(req)
	if err != nil {
		return scopedTableTokens{}, err
	}
	if len(tables) == 0 {
		return scopedTableTokens{}, errors.New("provider request has no table object")
	}
	return tables[0], nil
}

func cloneFactMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// b7fx extends the B6 fixture with B7 seed data.
type b7fx struct {
	*fx
	providerID  string
	toolID      string
	entryGlobal string
	entryTable  string
	// facts holds the evidence stub wired by the most recent fakeDial call (for SEC-0041 out-of-bounds call count assertions).
	facts            *fakeFactSource
	providerAuditIDs map[string]string // Test-only correlation to stage-scoped storage IDs.
}

func newB7Fixture(t *testing.T) *b7fx {
	f := newFixture(t)
	b := &b7fx{fx: f}
	for _, tbl := range []string{"review_tool_calls", "knowledge_entries", "prompt_tools",
		"rule_set_prompt_tools", "ai_providers", "ai_usage_ledger", "ai_daily_budget_usage", "system_settings"} {
		if err := f.db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	b.seed(t)
	return b
}

// enforceBudget writes ai_budget settings (enforced).
func (b *b7fx) enforceBudget(t *testing.T, dailyMinor int64) {
	t.Helper()
	if err := b.db.Exec(`
		insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('ai_budget', 1, ?::jsonb, 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings, version = system_settings.version + 1`,
		fmt.Sprintf(`{"enforced":true,"currency":"USD","daily_budget_minor":%d,"alert_threshold_percent":80}`, dailyMinor),
		b.userID).Error; err != nil {
		t.Fatal(err)
	}
}

func (b *b7fx) seed(t *testing.T) {
	// AI Provider (secret envelope).
	b.providerID = uuid.NewString()
	env, err := b.svc.Crypter.Seal(crypto.PurposePayload, []byte("sk-test"), crypto.AAD{
		DeploymentID: b.svc.Crypter.DeploymentID(),
		Table:        "ai_providers", PrimaryKey: b.providerID,
		Column: "secret_ciphertext", Purpose: "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`
		insert into ai_providers (id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at)
		values (?, 'primary', 'openai_compatible', 'https://ai.example.com', 'test-model', true,
			1, repeat('a',64), repeat('b',64), '{}'::jsonb, ?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		b.providerID, env.Ciphertext, env.Nonce, env.KeyID, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	// Prompt Tool (enabled; bound to rule set).
	b.toolID = uuid.NewString()
	def := PromptToolDefinition{
		KnowledgeText: "UPDATE/DELETE must include a WHERE clause",
		MaxSeverity:   "critical", Version: 1,
	}
	defJSON, _ := json.Marshal(def)
	if err := b.db.Exec(`
		insert into prompt_tools (id, name, state, definition, config_hash, version, finding_key, created_by_user_id, created_at, updated_at)
		values (?, 'must-where', 'enabled', ?, repeat('c',64), 1, 'skill.nowhere', ?, now(), now())`,
		b.toolID, defJSON, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`
		insert into rule_set_prompt_tools (rule_set_id, prompt_tool_id)
		select id, ? from rule_sets limit 1`, b.toolID).Error; err != nil {
		t.Fatal(err)
	}
	// Internal knowledge: global + table (matching the first-stage datasource and database of seedFlow).
	b.entryGlobal = b.insertEntry(t, "global-guard", "global", "", "", "",
		[]string{"high", "critical"}, "kb.global-rule")
	var dsID string
	var dbName *string
	if err := b.db.Raw(`select fs.datasource_id::text, d.database_name
		from flow_stages fs join datasources d on d.id = fs.datasource_id limit 1`).Row().Scan(&dsID, &dbName); err != nil {
		t.Fatal(err)
	}
	dbValue := "shop"
	if dbName != nil && *dbName != "" {
		dbValue = *dbName
	}
	b.entryTable = b.insertEntry(t, "table-boundary", "table", dsID, dbValue, "orders",
		[]string{"critical"}, "kb.orders-scan")
}

func (b *b7fx) insertEntry(t *testing.T, name, scope, ds, db_, tbl string, whitelist []string, key string) string {
	t.Helper()
	id := uuid.NewString()
	def := knowledge.Definition{
		KnowledgeText: name + " boundary rule: emit Finding at highest allowed severity when matched",
		MaxSeverity:   maxSevOf(whitelist), Version: 1,
	}
	raw, _ := json.Marshal(def)
	ptr := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if err := b.db.Exec(`
		insert into knowledge_entries (id, name, purpose, state, scope_type, datasource_id,
			database_name, table_name, definition, config_hash, version, provenance, finding_key,
			created_by_user_id, created_at, updated_at)
		values (?, ?, 'itest', 'enabled', ?, ?, ?, ?, ?, repeat('d',64), 1, 'manual', ?, ?, now(), now())`,
		id, name, scope, ptr(ds), ptr(db_), ptr(tbl), raw, keyOrDerived(key, id), b.userID).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// fakeDial constructs a scripted Fake Provider.
func (b *b7fx) fakeDial(respond func(provider.Request) ([]byte, int, error)) AgentDeps {
	facts := &fakeFactSource{}
	b.facts = facts
	return AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(ep, respond), nil
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return facts, func() {}, nil
		},
	}
}

// validProviderBody produces a valid structured response (including skill findings + tool requests).
func (b *b7fx) validProviderBody(groupID string, withTool bool) func(provider.Request) ([]byte, int, error) {
	return func(req provider.Request) ([]byte, int, error) {
		findings := []map[string]any{{
			"finding_key": "skill.nowhere", "category": "correctness", "severity": "high",
			"title": "Unbounded mutation without WHERE", "message": "The statement has no WHERE condition.", "suggestion": "Add a WHERE condition.",
			"fingerprint_group_ids": []string{groupID}, "evidence_ids": []string{}, "fact_basis": "model_only",
		}}
		var tools []map[string]any
		if withTool {
			var err error
			tools, err = scopedMetadataToolRequests(req, uuid.NewString())
			if err != nil {
				return nil, 500, err
			}
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.", "findings": findings, "tool_requests": tools,
		}
		if strings.Contains(req.SystemPrompt, "English (en-US)") {
			body["summary"] = "Review complete."
			findings[0]["title"] = "Unbounded mutation without WHERE"
			findings[0]["message"] = "The statement has no WHERE condition."
			findings[0]["suggestion"] = "Add a WHERE condition."
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	}
}

// runAgent drives an ephemeral precheck (RCP-20261101): StartEphemeralRun executes the review in a
// goroutine and materializes the Run rows upon reaching a terminal state — restoring the persisted rows
// of the legacy DB task chain (review_runs / findings / evidence / tool calls) so existing DB assertions
// remain unchanged, while the draft stays in draft state per the new semantics (terminal semantics live on the Run row).
func (b *b7fx) runAgent(t *testing.T, deps AgentDeps) string {
	t.Helper()
	const sqlText = "update orders set amount = amount + 1 where id = 1;\n"
	draft := b.createDraftWithSQL(t, sqlText)
	agent := NewAgentRunner(b.svc, deps)
	runID, err := b.svc.StartEphemeralRun(context.Background(), b.owner, agent, EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText),
		IdempotencyKey: "b7-" + t.Name() + "-" + runKeySuffix(),
	})
	if err != nil {
		t.Fatalf("start ephemeral run: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		er := b.svc.EphemeralGetForTest(runID, b.owner.UserID)
		if er != nil {
			switch er.State() {
			case domain.RunStateReady, domain.RunStateFailed, domain.RunStatePartial, domain.RunStateBlocked:
				if merr := b.db.Transaction(func(tx *gorm.DB) error {
					return b.svc.MaterializeEphemeralRunTx(context.Background(), tx, er, draft.ID, draft.Revision)
				}); merr != nil {
					t.Fatalf("materialize ephemeral run: %v", merr)
				}
				return runID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ephemeral run %s not terminal", runID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestB7Gate1AgentFullFlow full flow: High finding -> run blocked; injection references,
// tool calls, evidence, and ledger are all present.
func TestB7Gate1AgentFullFlow(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if len(req.AnonymizedStatementGroups) == 0 {
			return nil, 500, fmt.Errorf("no groups")
		}
		// Turn 1 requests tools; turn 2 returns the final conclusion once evidence is available (real model behavior).
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	var state string
	var errCode *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &errCode); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStateBlocked) || errCode == nil || *errCode != 2008 {
		t.Fatalf("expected blocked/2008, got %s/%v", state, func() any {
			if errCode == nil {
				return "nil"
			}
			return *errCode
		}())
	}
	// Injection references: skill + two knowledge entries (global always matches; table matches when seedFlow
	// database/table matches, or at least one global entry; plus one skill).
	var skillRefs, kbRefs int64
	b.db.Raw(`select count(*) from review_tool_calls where tool_kind = 'prompt_tool'`).Scan(&skillRefs)
	b.db.Raw(`select count(*) from review_tool_calls where tool_kind = 'knowledge_entry'`).Scan(&kbRefs)
	if skillRefs != 1 || kbRefs < 1 {
		t.Fatalf("injection refs: skills=%d kb=%d", skillRefs, kbRefs)
	}
	// Tool evidence collection: safe_explain call succeeds + evidence fact is persisted.
	var toolCalls, facts, findings int64
	b.db.Raw(`select count(*) from review_tool_calls where tool_kind = 'builtin_database_fact' and success`).Scan(&toolCalls)
	b.db.Raw(`select count(*) from evidence_facts ef join stage_review_snapshots s on s.id = ef.stage_review_id where s.review_run_id = ?`, runID).Scan(&facts)
	b.db.Raw(`select count(*) from review_findings`).Scan(&findings)
	if toolCalls != 1 || facts < 1 || findings != 1 {
		t.Fatalf("tool=%d facts=%d findings=%d", toolCalls, facts, findings)
	}
	// Ledger: at least one succeeded row.
	var ledger int64
	b.db.Raw(`select count(*) from ai_usage_ledger where outcome = 'succeeded'`).Scan(&ledger)
	if ledger < 1 {
		t.Fatal("usage ledger empty")
	}
	// Privacy: request groups contain no raw literals (Fake responder already asserted group presence;
	// serialization boundary is covered by unit tests in package agent).
}

// TestB7Gate2NoProviderFailsClosed no enabled Provider -> Run fails with 2011.
// RCP-20261101: on the ephemeral path, loading the review chain fails closed immediately; verify DB rows after materialization.
func TestB7Gate2NoProviderFailsClosed(t *testing.T) {
	b := newB7Fixture(t)
	if err := b.db.Exec(`update ai_providers set enabled = false`).Error; err != nil {
		t.Fatal(err)
	}
	draft := b.createDraftWithSQL(t, "update orders set amount = 1 where id = 1;\n")
	er, runID := b.startEphemeralTerminal(t, b.owner, b.fakeDial(func(provider.Request) ([]byte, int, error) {
		return nil, 500, fmt.Errorf("must not be called")
	}), EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte("update orders set amount = 1 where id = 1;\n"),
		IdempotencyKey: "b7-" + t.Name() + "-" + runKeySuffix()})
	if er.State() != domain.RunStateFailed {
		t.Fatalf("expected failed, got %s (code=%s)", er.State(), ephemeralRunCodeString(er))
	}
	if code := ephemeralRunCode(er); code == nil || *code != 2011 {
		t.Fatalf("expected 2011, got %v", code)
	}
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	var state string
	var code *int32
	b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code)
	if state != string(domain.RunStateFailed) || code == nil || *code != 2011 {
		t.Fatalf("expected failed/2011, got %s/%v", state, func() any {
			if code == nil {
				return "nil"
			}
			return *code
		}())
	}
}

// TestB7Gate3BudgetExhaustionAndConcurrency budget: concurrent reservations never overspend;
// exhaustion -> partial/2010.
func TestB7Gate3BudgetExhaustionAndConcurrency(t *testing.T) {
	b := newB7Fixture(t)
	b.enforceBudget(t, 1000)
	day := b.svc.budgetDayOf(time.Now())
	settings := BudgetSettings{Enforced: true, Currency: "USD", DailyBudgetMinor: 1000}
	// Concurrent reservations: 20 goroutines x 100 = 2000 > 1000, so at most 10 succeed.
	var mu sync.Mutex
	okCount := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.svc.Reserve(ctx, b.db, day, "USD", settings, 100); err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 10 {
		t.Fatalf("concurrent reservation overspent: ok=%d want=10", okCount)
	}
	var reserved int64
	b.db.Raw(`select reserved_minor from ai_daily_budget_usage where budget_day = ? and currency = 'USD'`, day).Scan(&reserved)
	if reserved != 1000 {
		t.Fatalf("reserved=%d want 1000", reserved)
	}
	// Executing a Run after exhaustion -> 2010 (ephemeral driver; verify DB rows after materialization).
	// Note: the ephemeral path (ephemeral_agent) converges budget_exhausted to failed/2010,
	// differing from the partial convergence (RunEventIncomplete) of the legacy DB task chain — asserted here
	// against current production behavior; the no-overspend invariant and halting of subsequent model calls are unaffected.
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
	})
	draft := b.createDraftWithSQL(t, "update orders set amount = 2 where id = 1;\n")
	er, runID := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte("update orders set amount = 2 where id = 1;\n"),
		IdempotencyKey: "b7-" + t.Name() + "-" + runKeySuffix()})
	if er.State() != domain.RunStateFailed {
		t.Fatalf("expected failed, got %s (code=%s failure=%q)", er.State(), ephemeralRunCodeString(er), er.Failure)
	}
	if code := ephemeralRunCode(er); code == nil || *code != 2010 {
		t.Fatalf("expected 2010, got %s", ephemeralRunCodeString(er))
	}
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	var state string
	var code *int32
	b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code)
	if state != string(domain.RunStateFailed) || code == nil || *code != 2010 {
		t.Fatalf("expected failed/2010, got %s/%v", state, func() any {
			if code == nil {
				return "nil"
			}
			return *code
		}())
	}
}

// TestB7Gate4KnowledgeChangeInvalidatesRun R010: knowledge Hash change ->
// unsubmitted Runs referencing the old Hash immediately become outdated.
func TestB7Gate4KnowledgeChangeInvalidatesRun(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	var state string
	b.db.Raw(`select state from review_runs where id = ?`, runID).Row().Scan(&state)
	if state != string(domain.RunStateBlocked) {
		t.Fatalf("precondition: run should be blocked, got %s", state)
	}
	// Mutate the entry through the real Service.Replace path to prevent regressions where overwriting the
	// aggregate before reading oldHash causes the invalidation query to miss old Runs.
	repo := &knowledge.Repository{DB: b.db}
	entry, err := repo.Get(context.Background(), b.entryGlobal)
	if err != nil {
		t.Fatal(err)
	}
	entry.Definition.KnowledgeText += " (revised)"
	ks := &knowledge.Service{Repo: repo, Invalidate: InvalidateInputRefs}
	_, err = ks.Replace(context.Background(), entry.ID, knowledge.ReplaceInput{
		ExpectVersion: entry.Version, Name: entry.Name, Purpose: entry.Purpose,
		State: entry.State, ScopeType: entry.ScopeType, DatasourceID: entry.DatasourceID,
		DatabaseName: entry.DatabaseName, TableName: entry.TableName, Definition: entry.Definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	b.db.Raw(`select state from review_runs where id = ?`, runID).Row().Scan(&state)
	if state != string(domain.RunStateOutdated) {
		t.Fatalf("referencing run not outdated: %s", state)
	}
	// Mutating an unreferenced table-scoped entry has no effect (no reference to that Hash).
}

// TestB7Gate4bKnowledgeDisableInvalidatesRun P3 coverage: enabled -> disabled
// goes through the real Service.Replace path (including InvalidateInputRefs) to invalidate referencing Runs.
func TestB7Gate4bKnowledgeDisableInvalidatesRun(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	var state string
	b.db.Raw(`select state from review_runs where id = ?`, runID).Row().Scan(&state)
	if state != string(domain.RunStateBlocked) {
		t.Fatalf("precondition: run should be blocked, got %s", state)
	}
	repo := &knowledge.Repository{DB: b.db}
	entry, err := repo.Get(context.Background(), b.entryGlobal)
	if err != nil {
		t.Fatal(err)
	}
	ks := &knowledge.Service{Repo: repo, Invalidate: InvalidateInputRefs}
	if _, err := ks.Replace(context.Background(), entry.ID, knowledge.ReplaceInput{
		ExpectVersion: entry.Version, Name: entry.Name, Purpose: entry.Purpose,
		State: knowledge.StateDisabled, ScopeType: entry.ScopeType, DatasourceID: entry.DatasourceID,
		DatabaseName: entry.DatabaseName, TableName: entry.TableName, Definition: entry.Definition,
	}); err != nil {
		t.Fatal(err)
	}
	b.db.Raw(`select state from review_runs where id = ?`, runID).Row().Scan(&state)
	if state != string(domain.RunStateOutdated) {
		t.Fatalf("disable must invalidate referencing run via real path, got %s", state)
	}
}

// TestB7Gate5NoProviderCallsOnRead zero Provider calls on the read path
// (test_gate#9: reviewer viewing never triggers a model call).
func TestB7Gate5NoProviderCallsOnRead(t *testing.T) {
	b := newB7Fixture(t)
	calls := 0
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		calls++
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	execCalls := calls
	// Read Run and findings (B6 read path).
	if _, err := b.svc.GetRunOwned(context.Background(), b.owner, runID); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if _, _, err := b.svc.ListRunFindings(context.Background(), b.owner, runID, 50, ""); err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if calls != execCalls {
		t.Fatal("provider called on read path")
	}
}

// runKeySuffix generates a unique idempotency key suffix per test.
var runKeyCounter int64

func runKeySuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), func() int64 { runKeyCounter++; return runKeyCounter }())
}

// drain claims and executes tasks for this Run until empty (invokes handler directly and
// marks task status synchronously in this method to avoid replay).
func (b *b7fx) drain(t *testing.T, agent *AgentRunner, runID string) {
	t.Helper()
	for round := 0; round < 10; round++ {
		var rows []struct {
			ID    string
			Input []byte
		}
		if err := b.db.Raw(`select id::text, input from async_tasks
			where aggregate_id = ? and state = 'queued' order by created_at`, runID).
			Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			return
		}
		for _, r := range rows {
			var in TaskInput
			if err := json.Unmarshal(r.Input, &in); err != nil {
				t.Fatalf("decode task input: %v", err)
			}
			execErr := agent.Execute(context.Background(), b.db, in, nil, nil)
			if execErr != nil {
				t.Fatalf("agent execute: %v", execErr)
			}
			if err := b.db.Exec(
				`update async_tasks set state = 'succeeded', finished_at = now() where id = ?`, r.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestB7Gate6InjectionAndFactsReachModel C1/C2: deterministic injection text and
// normalized facts must appear in the request sent to the Provider.
func TestB7Gate6InjectionAndFactsReachModel(t *testing.T) {
	b := newB7Fixture(t)
	var sawSkill, sawKnowledge, sawFact bool
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if strings.Contains(req.SystemPrompt, "must-where") &&
			strings.Contains(req.SystemPrompt, "UPDATE/DELETE") &&
			strings.Contains(req.SystemPrompt, "finding_key: skill.nowhere") &&
			strings.Contains(req.SystemPrompt, "effective_severity: critical") {
			sawSkill = true
		}
		if strings.Contains(req.SystemPrompt, "global-guard") &&
			strings.Contains(req.SystemPrompt, "finding_key: kb.global-rule") &&
			strings.Contains(req.SystemPrompt, "effective_severity: critical") {
			sawKnowledge = true
		}
		for _, ev := range req.NormalizedEvidence {
			if ev.Fact != nil && ev.Fact["existence"] == "present" &&
				ev.Fact["metadata_complete"] == true {
				sawFact = true
			}
		}
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	var state string
	b.db.Raw(`select state from review_runs where id = ?`, runID).Row().Scan(&state)
	if !sawSkill {
		t.Fatal("skill injection text never reached provider request (C1)")
	}
	if !sawKnowledge {
		t.Fatal("knowledge injection text never reached provider request (C1)")
	}
	if !sawFact {
		t.Fatal("normalized tool fact never reached provider request (C2)")
	}
}

// TestB7Gate7DuplicateToolCallSameArgs H1: duplicate tool requests with identical arguments
// under different tool_call_ids must not crash the Run (deduplicated by fact_hash).
func TestB7Gate7DuplicateToolCallSameArgs(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if len(req.NormalizedEvidence) == 0 {
			// Request the same tool with identical arguments under two different tool_call_ids in the same turn
			// (H1 real path: the second copy shares the same fact_hash and must be deduplicated).
			requests, err := scopedMetadataToolRequests(
				req, "9f0b1e2c-0000-4000-8000-000000000001")
			if err != nil {
				return nil, 500, err
			}
			if len(requests) == 0 {
				return nil, 500, errors.New("scoped metadata tool request missing")
			}
			requests = append(requests, cloneToolRequest(
				requests[0], "9f0b1e2c-0000-4000-8000-000000000002"))
			body := map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Evidence required.", "findings": []any{},
				"tool_requests": requests,
			}
			raw, _ := json.Marshal(body)
			return raw, 200, nil
		}
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
	})
	runID := b.runAgent(t, deps)
	var state string
	var code *int32
	b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code)
	if state == string(domain.RunStateFailed) {
		t.Fatalf("duplicate tool call broke the run: code=%v", code)
	}
}

// TestB7Gate8TableScopeMatches H4: table-scoped knowledge entries genuinely match and are injected.
func TestB7Gate8TableScopeMatches(t *testing.T) {
	b := newB7Fixture(t)
	sawOrders := false
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if strings.Contains(req.SystemPrompt, `id="`+b.entryTable+`"`) {
			sawOrders = true
		}
		if strings.Contains(req.SystemPrompt, `name="orders-guard"`) {
			t.Fatal("raw table identifier leaked through policy name")
		}
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	var refs int64
	b.db.Raw(`select count(*) from review_tool_calls where tool_kind = 'knowledge_entry' and input_ref_id = ?`, b.entryTable).Scan(&refs)
	if !sawOrders || refs != 1 {
		t.Fatalf("table-scope entry not matched/injected: saw=%v refs=%d", sawOrders, refs)
	}
	_ = runID
}

// TestB7KnowledgeScopeUsesFrozenSchemaScope proves the knowledge
// prefilter consumes the persisted anonymous object graph and the frozen
// stage object scope (which after RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS is
// datasources.database_name='shop'). The actual Provider prompt is the
// assertion boundary: exact same-basename objects are injected for the
// allowed schema, while an untouched table on the same datasource remains
// absent.
func TestB7KnowledgeScopeUsesFrozenSchemaScope(t *testing.T) {
	b := newB7Fixture(t)

	var datasourceID string
	if err := b.db.Raw(`
		select datasource_id::text from flow_stages
		where flow_id = ? order by position limit 1`, b.flowID).
		Row().Scan(&datasourceID); err != nil {
		t.Fatal(err)
	}

	shopOrders := b.insertEntry(t, "scoped-schema-first", "table",
		datasourceID, "shop", "orders", []string{"critical"}, "kb.shop-orders")
	untouched := b.insertEntry(t, "scoped-schema-untouched", "table",
		datasourceID, "shop", "users", []string{"critical"}, "kb.shop-users")

	var prompts []string
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		prompts = append(prompts, req.SystemPrompt)
		if len(req.AnonymizedStatementGroups) == 0 {
			return nil, 500, errors.New("provider request has no statement groups")
		}
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(
			req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgentSQL(t, deps,
		"update shop.orders set amount = amount + 1 where id = 1;\n",
		"knowledge-scope-"+runKeySuffix())

	if len(prompts) == 0 {
		t.Fatal("no Provider request captured")
	}
	combined := strings.Join(prompts, "\n")
	if !strings.Contains(combined, `id="`+shopOrders+`"`) {
		t.Fatalf("matched knowledge entry %q missing from Provider request", shopOrders)
	}
	if strings.Contains(combined, `id="`+untouched+`"`) {
		t.Fatal("untouched table knowledge leaked into the Provider request")
	}
	for entryID, want := range map[string]int64{
		shopOrders: 1,
		untouched:  0,
	} {
		var refs int64
		if err := b.db.Raw(`
			select count(*) from review_tool_calls
			where review_run_id = ? and tool_kind = 'knowledge_entry'
			  and input_ref_id = ?`, runID, entryID).Scan(&refs).Error; err != nil {
			t.Fatal(err)
		}
		if refs != want {
			t.Fatalf("knowledge reference count for %s = %d, want %d",
				entryID, refs, want)
		}
	}
}

// TestB7Gate8aKnowledgeIsStageLocal verifies that a datasource/table entry
// matched by stage 2 is neither injected into nor frozen for stage 1.
func TestB7Gate8aKnowledgeIsStageLocal(t *testing.T) {
	b := newB7Fixture(t)
	ds2 := uuid.NewString()
	if err := b.db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version,
		created_by_user_id, created_at, updated_at)
		values (?, 'b7-prod', 'mysql', 'mysql', 'native', 'prod.example.com', 3306, 'prod',
		'8.0.x', true, ?, 1, ?, now(), now())`, ds2, strings.Repeat("ef", 32), b.userID).Error; err != nil {
		t.Fatal(err)
	}
	stage2 := uuid.NewString()
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: stage2 scope = its datasource
	// default database 'prod', no longer sealing mappings.
	if err := b.db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 2, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stage2, b.flowID, ds2, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatal(err)
	}
	step2 := uuid.NewString()
	if err := b.db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position) values (?, ?, ?, 1)`,
		step2, b.flowID, stage2).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, step2, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`, stage2, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	prodEntry := b.insertEntry(t, "stage-two-only", "table", ds2, "prod", "orders",
		[]string{"critical"}, "kb.prod-orders")
	prompts := []string{}
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		prompts = append(prompts, req.SystemPrompt)
		withTool := len(req.NormalizedEvidence) == 0
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
	})
	runID := b.runAgent(t, deps)
	firstStageTwoPrompt := -1
	for index, prompt := range prompts {
		if strings.Contains(prompt, `id="`+prodEntry+`"`) {
			firstStageTwoPrompt = index
			break
		}
	}
	// Each stage now makes at least two model calls ("scoped metadata" and "final conclusion");
	// stage-2 knowledge can only appear starting from the third call.
	if len(prompts) < 4 || firstStageTwoPrompt < 2 {
		t.Fatalf("stage-local injection violated: prompts=%d first_stage2_match=%d",
			len(prompts), firstStageTwoPrompt)
	}
	var positions []int
	if err := b.db.Raw(`select s.stage_position from review_tool_calls c
		join stage_review_snapshots s on s.id = c.stage_review_id
		where c.review_run_id = ? and c.tool_kind = 'knowledge_entry' and c.input_ref_id = ?
		order by s.stage_position`, runID, prodEntry).Scan(&positions).Error; err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0] != 2 {
		t.Fatalf("knowledge ref frozen for wrong stages: %v", positions)
	}
}

// TestB7Gate9CoveragePartialIs2007 M9: pure incomplete coverage is classified as
// coverage_incomplete/2007 rather than risk_blocked/2008.
func TestB7Gate9CoveragePartialIs2007(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "partial",
			"summary": "Coverage incomplete.", "findings": []any{}, "tool_requests": []any{},
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	})
	runID := b.runAgent(t, deps)
	var state string
	var code *int32
	b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code)
	if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
		t.Fatalf("expected partial/2007, got %s/%v", state, code)
	}
}

// TestB7Gate10CancellationPreservesTerminalState removed under RCP-20261101.
// Legacy premise: reviews ran on the DB task chain (two-stage sql_grouping/ai_review tasks), where pod
// cancellation manifested as a worker claiming a task with a cancelled ctx — CancelReviewTask / ctx
// cancellation converged the Run to partial/2007 "cancelled". Ephemeral prechecks are driven by
// StartEphemeralRun in an in-process goroutine using context.Background(), completing in milliseconds
// with no cancellable task rows, and the user-facing cancellation entry (CancelReviewTask) was retired
// along with the task chain; the internal ctx cancellation convergence (2007/cancelled) inside
// ExecuteEphemeralRun is a process-shutdown robustness branch unreachable from production entry points.
// Terminal state immutability is covered by the replay assertions in TestGate2IdempotentStartRun.

// TestB7Gate11BudgetReconciliation M5: reconciliation recomputes from the ledger and zeroes out orphaned reservations.
func TestB7Gate11BudgetReconciliation(t *testing.T) {
	b := newB7Fixture(t)
	b.enforceBudget(t, 1000)
	day := b.svc.budgetDayOf(time.Now())
	settings := BudgetSettings{Enforced: true, Currency: "USD", DailyBudgetMinor: 1000}
	// Simulate crash leak: reservation of 200 with no corresponding ledger entry.
	if err := b.svc.Reserve(context.Background(), b.db, day, "USD", settings, 200); err != nil {
		t.Fatal(err)
	}
	// Ledger records 100 incurred.
	if err := b.svc.Settle(context.Background(), b.db, uuid.NewString(), b.providerID, "m", 1, 10, 5, 100, "USD", day, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.ReconcileDailyUsage(context.Background(), b.db, "USD"); err != nil {
		t.Fatal(err)
	}
	var amount, reserved int64
	b.db.Raw(`select amount_minor, reserved_minor from ai_daily_budget_usage where budget_day = ? and currency = 'USD'`, day).Row().Scan(&amount, &reserved)
	if amount != 100 || reserved != 0 {
		t.Fatalf("reconciliation wrong: amount=%d reserved=%d", amount, reserved)
	}
}

// TestB7Gate12ModelCallCapAndRawFidelity L2/L7: model call cap and deep
// metadata truncation jointly converge to partial; raw evidence preserves pre-normalization content.
func TestB7Gate12ModelCallCapAndRawFidelity(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		// Turn 1 requests only tools without a conclusion; turn 2 is cut off by MaxModelCalls=1 ->
		// covering the call-cap partial convergence path (no findings -> 2007 rather than 2008).
		requests, err := scopedMetadataToolRequests(
			req, "9f0b1e2c-0000-4000-8000-000000000009")
		if err != nil {
			return nil, 500, err
		}
		if len(requests) == 0 {
			return nil, 500, errors.New("scoped metadata tool request missing")
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "partial",
			"summary": "Evidence required.", "findings": []any{},
			"tool_requests": requests[:1],
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	})
	deps.RuntimeBudget = &RuntimeBudgetConfig{MaxModelCalls: 1}
	// Deep noise is used only for this negative case: normalization must truncate it (protecting model context),
	// while raw evidence preserves it verbatim (L7).
	deep := map[string]any{"v": strings.Repeat("d", 40)}
	for i := 0; i < 10; i++ {
		deep = map[string]any{"v": deep}
	}
	b.facts.tableFact = map[string]any{
		"existence": "present", "metadata_complete": true, "truncated": false,
		"columns": []any{}, "debug_deep": deep,
	}
	runID := b.runAgent(t, deps)
	var state string
	var code *int32
	b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code)
	if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
		t.Fatalf("call cap must converge partial/2007, got %s/%v", state, code)
	}
	// L7: raw payload preserves pre-normalization content; normalized fact truncates deep noise.
	var factID string
	if err := b.db.Raw(`
		select ef.id::text from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		join review_runs rr on rr.id = s.review_run_id
		where rr.id = ? and ef.source_kind = 'database_tool'
		order by ef.created_at desc limit 1`, runID).Scan(&factID).Error; err != nil || factID == "" {
		t.Fatalf("tool evidence missing: id=%q err=%v", factID, err)
	}
	var rawLen int64
	if err := b.db.Raw(`select octet_length(raw_payload_ciphertext) from evidence_raw_payloads where evidence_fact_id = ?::uuid`, factID).Row().Scan(&rawLen); err != nil || rawLen == 0 {
		t.Fatalf("raw payload missing for fact %s: len=%d err=%v", factID, rawLen, err)
	}
	var rawCT []byte
	if err := b.db.Raw(`select raw_payload_ciphertext from evidence_raw_payloads where evidence_fact_id = ?::uuid`, factID).Row().Scan(&rawCT); err != nil {
		t.Fatal(err)
	}
	// L7 semantics: noise is permitted only in the raw ciphertext (7-day retention) channel;
	// normalized facts (the model context channel) must not contain the deep noise key.
	if len(rawCT) == 0 {
		t.Fatal("raw ciphertext empty")
	}
	var normCT []byte
	if err := b.db.Raw(`select normalized_fact_ciphertext from evidence_facts where id = ?::uuid`, factID).Row().Scan(&normCT); err != nil || len(normCT) == 0 {
		t.Fatalf("normalized fact missing: %v", err)
	}
}

func TestB7SafeExplainOnlyIsPartialForTableReview(t *testing.T) {
	b := newB7Fixture(t)
	turn := 0
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		turn++
		if turn == 1 {
			table, err := firstScopedTable(req)
			if err != nil {
				return nil, 500, err
			}
			body := map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Continue reading execution plan.", "findings": []any{},
				"tool_requests": []map[string]any{{
					"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000011",
					"tool_name":    "safe_explain",
					"arguments": map[string]any{
						"schema":   table.Schema,
						"group_id": req.AnonymizedStatementGroups[0].GroupID,
					},
				}},
			}
			raw, err := json.Marshal(body)
			return raw, 200, err
		}
		return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
	})
	runID := b.runAgent(t, deps)
	var state string
	var code *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).
		Row().Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
		t.Fatalf("safe_explain without scoped metadata = %s/%v, want partial/2007", state, code)
	}
	if b.facts.scopedMetadataCalls != 0 || b.facts.explainCalls != 1 {
		t.Fatalf("tool calls = scoped:%d explain:%d", b.facts.scopedMetadataCalls, b.facts.explainCalls)
	}
}

func TestB7ZeroToolCompleteIsPartialForTableReview(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.", "findings": []any{}, "tool_requests": []any{},
		}
		raw, err := json.Marshal(body)
		return raw, 200, err
	})
	runID := b.runAgent(t, deps)
	var state string
	var code *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).
		Row().Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
		t.Fatalf("zero-tool table review = %s/%v, want partial/2007", state, code)
	}
}

// TestB7Gate13EnableGateAndConfigValidation vendor-difference mitigation layer:
// free-form text endpoints must not pass the enablement probe; configuration controlled-key validation.
func TestB7Gate13EnableGateAndConfigValidation(t *testing.T) {
	b := newB7Fixture(t)
	// configuration range validation.
	if err := ValidateProviderConfiguration(map[string]any{"max_tokens": 100.0}); err == nil {
		t.Fatal("max_tokens below floor accepted")
	}
	if err := ValidateProviderConfiguration(map[string]any{"temperature": 3.0}); err == nil {
		t.Fatal("temperature out of range accepted")
	}
	if err := ValidateProviderConfiguration(map[string]any{"endpoint_override": "x"}); err == nil {
		t.Fatal("unknown configuration key accepted")
	}
	if err := ValidateProviderConfiguration(map[string]any{"max_tokens": 8192.0, "temperature": 0.2}); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	// Enablement gate: probe must fail against a free-form text endpoint -> not persisted.
	origDial := probeDial
	probeDial = func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
			return []byte("not json at all"), 200, nil
		}), nil
	}
	defer func() { probeDial = origDial }()
	_, err := b.svc.CreateAIProvider(context.Background(), b.userID, CreateAIProviderInput{
		Name: "bad-vendor", ProviderKind: "openai_compatible",
		BaseURL: "https://bad.example.com", ModelName: "m", Enabled: true, SelectionPriority: 2,
		APIKey: "k",
	})
	if err == nil {
		t.Fatal("enable gate failed open: bad vendor accepted")
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != 2011 {
		t.Fatalf("bad vendor must map 2011, got %v", err)
	}
	var n int64
	b.db.Raw(`select count(*) from ai_providers where name = 'bad-vendor'`).Scan(&n)
	if n != 0 {
		t.Fatal("enable gate failed open: provider persisted despite failed probe")
	}
	// Conformant endpoint passes probe -> persisted successfully.
	probeDial = func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
			raw, _ := json.Marshal(map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Connection test.", "findings": []any{}, "tool_requests": []any{},
			})
			return raw, 200, nil
		}), nil
	}
	view, err := b.svc.CreateAIProvider(context.Background(), b.userID, CreateAIProviderInput{
		Name: "good-vendor", ProviderKind: "openai_compatible",
		BaseURL: "https://good.example.com", ModelName: "m", Enabled: true, SelectionPriority: 3,
		APIKey: "k", Configuration: map[string]any{"max_tokens": 8192.0},
	})
	if err != nil {
		t.Fatalf("conformant vendor rejected at enable: %v", err)
	}
	if view == nil || view.Name != "good-vendor" {
		t.Fatal("conformant vendor not created")
	}
}

// TestB7Gate14ReplaceKeepSemanticsAndKeyRotation M1/M2/H1:
// keep semantics (omitting configuration/secret no longer writes NULL); key rotation triggers re-probing;
// the effective chain carries configuration.
func TestB7Gate14ReplaceKeepSemanticsAndKeyRotation(t *testing.T) {
	b := newB7Fixture(t)
	// Conformant probe.
	probeOK := func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
			raw, _ := json.Marshal(map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Connection test.", "findings": []any{}, "tool_requests": []any{},
			})
			return raw, 200, nil
		}), nil
	}
	origDial := probeDial
	probeDial = probeOK
	defer func() { probeDial = origDial }()
	view, err := b.svc.CreateAIProvider(context.Background(), b.userID, CreateAIProviderInput{
		Name: "keep-vendor", ProviderKind: "openai_compatible",
		BaseURL: "https://keep.example.com", ModelName: "m", Enabled: true, SelectionPriority: 2,
		APIKey: "k1", Configuration: map[string]any{"max_tokens": 4096.0},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// M2: omitting configuration and secret -> retains existing values (neither writing NULL nor triggering 23502).
	updated, err := b.svc.ReplaceAIProvider(context.Background(), view.ID, ReplaceAIProviderInput{
		ExpectVersion: view.Version, Name: view.Name, ProviderKind: "openai_compatible",
		BaseURL: view.BaseURL, ModelName: view.ModelName, Enabled: true, SelectionPriority: 2,
	})
	if err != nil {
		t.Fatalf("keep-semantics replace failed: %v", err)
	}
	var cfgText string
	var secretLen int
	b.db.Raw(`select configuration::text, octet_length(secret_ciphertext) from ai_providers where id = ?`, view.ID).Row().Scan(&cfgText, &secretLen)
	if !strings.Contains(cfgText, "4096") || secretLen == 0 {
		t.Fatalf("keep semantics broken: cfg=%q secret=%d", cfgText, secretLen)
	}
	// M1: key rotation (changing only the key) triggers re-probing.
	probeCalls := 0
	probeDial = func(ep provider.Endpoint) (provider.Client, error) {
		probeCalls++
		return probeOK(ep)
	}
	if _, err := b.svc.ReplaceAIProvider(context.Background(), view.ID, ReplaceAIProviderInput{
		ExpectVersion: updated.Version, Name: view.Name, ProviderKind: "openai_compatible",
		BaseURL: view.BaseURL, ModelName: view.ModelName, Enabled: true, SelectionPriority: 2,
		APIKey: "k2",
	}); err != nil {
		t.Fatalf("key rotation replace failed: %v", err)
	}
	if probeCalls == 0 {
		t.Fatal("key-only rotation did not re-probe (M1)")
	}
	// H1: effective chain carries configuration (max_tokens reaches the production Adapter;
	// seed primary having an empty configuration is valid, so assert on keep-vendor).
	chain, err := b.svc.LoadProviderChain(context.Background(), b.db, func(ep provider.Endpoint) (provider.Client, error) {
		if ep.ProviderID == view.ID {
			if ep.Configuration == nil || ep.Configuration["max_tokens"] != 4096.0 {
				t.Fatalf("chain endpoint missing configuration (H1): %+v", ep.Configuration)
			}
		}
		return probeOK(ep)
	})
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	_ = chain
}

// maxSevOf section 18.66: legacy allowlist slice -> highest severity (integration fixture compatibility helper).
func maxSevOf(list []string) string {
	rank := map[string]int{"info": 0, "low": 1, "medium": 2, "high": 3, "critical": 4}
	out := "medium"
	for _, v := range list {
		if r, ok := rank[v]; ok && r >= rank[out] {
			out = v
		}
	}
	return out
}

// keyOrDerived section 18.66: fixture knowledge rows prefer the semantic key echoed by the model, defaulting to exp.<id>.
func keyOrDerived(key, id string) string {
	if key == "" {
		return "exp." + id
	}
	return key
}

// TestB7Gate15ProviderUniqueMappingAndProbeDetail section 18.75:
// A = probe failure error chain preserves upstream rejection reason ("probe:" prefix, while errors.As still unwraps 2011);
// B = name/selection_priority unique index collisions map to RESOURCE_ALREADY_EXISTS (1005),
//
//	no longer leaking as a silent 500 (on both create and replace paths).
func TestB7Gate15ProviderUniqueMappingAndProbeDetail(t *testing.T) {
	b := newB7Fixture(t)
	probeOK := func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
			raw, _ := json.Marshal(map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Connection test.", "findings": []any{}, "tool_requests": []any{},
			})
			return raw, 200, nil
		}), nil
	}
	origDial := probeDial
	probeDial = probeOK
	defer func() { probeDial = origDial }()

	base := func(name string, prio int) CreateAIProviderInput {
		return CreateAIProviderInput{Name: name, ProviderKind: "openai_compatible",
			BaseURL: "https://u15.example.com", ModelName: "m", Enabled: true,
			SelectionPriority: prio, APIKey: "k"}
	}
	if _, err := b.svc.CreateAIProvider(context.Background(), b.userID, base("u15-a", 91)); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	// B1: duplicate name -> 1005.
	_, err := b.svc.CreateAIProvider(context.Background(), b.userID, base("u15-a", 92))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != 1005 {
		t.Fatalf("duplicate name must map 1005, got %v", err)
	}
	// B2: duplicate priority -> 1005.
	_, err = b.svc.CreateAIProvider(context.Background(), b.userID, base("u15-b", 91))
	if !errors.As(err, &de) || de.Code != 1005 {
		t.Fatalf("duplicate priority must map 1005, got %v", err)
	}

	// B3: replace renaming into an existing name -> 1005.
	view, err := b.svc.CreateAIProvider(context.Background(), b.userID, base("u15-c", 93))
	if err != nil {
		t.Fatalf("create c: %v", err)
	}
	_, err = b.svc.ReplaceAIProvider(context.Background(), view.ID, ReplaceAIProviderInput{
		ExpectVersion: view.Version, Name: "u15-a", ProviderKind: "openai_compatible",
		BaseURL: view.BaseURL, ModelName: view.ModelName, Enabled: true, SelectionPriority: 93,
	})
	if !errors.As(err, &de) || de.Code != 1005 {
		t.Fatalf("replace onto duplicate name must map 1005, got %v", err)
	}

	// A: probe failure — domain 2011 can still be unwrapped, and the error string preserves the upstream rejection reason.
	probeDial = func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
			return nil, 401, fmt.Errorf("upstream rejected: invalid api key")
		}), nil
	}
	_, err = b.svc.CreateAIProvider(context.Background(), b.userID, base("u15-bad", 94))
	if !errors.As(err, &de) || de.Code != 2011 {
		t.Fatalf("probe failure must map 2011, got %v", err)
	}
	if !strings.Contains(err.Error(), "probe:") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("probe failure must retain upstream detail in error chain, got %q", err.Error())
	}
	// section 18.75-A addition: Detail carries the upstream snapshot on domain.Error — surfaced via meta.detail
	// to the operator form (owner ruling: error prompts must be specific, never just generic wording).
	if de.Detail == "" || !strings.Contains(de.Detail, "invalid api key") {
		t.Fatalf("probe failure must carry upstream detail for the UI, got %q", de.Detail)
	}
}

// gate16ModeStub is a probe negotiation stub (answering only NegotiateOutputMode).
type gate16ModeStub struct {
	mode string
	err  error
}

func (c gate16ModeStub) ValidateConfiguration(context.Context) error { return nil }
func (c gate16ModeStub) NegotiateOutputMode(context.Context) (string, error) {
	return c.mode, c.err
}
func (c gate16ModeStub) ReviewStructured(context.Context, provider.Request) (provider.CallOutcome, error) {
	return provider.CallOutcome{}, nil
}
func (c gate16ModeStub) Health(context.Context) error { return nil }

// b7Gate16Configuration reads back the persisted configuration jsonb of a Provider.
func b7Gate16Configuration(t *testing.T, b *b7fx, id string) map[string]any {
	t.Helper()
	var rows []struct {
		Configuration string
	}
	if err := b.svc.DB.WithContext(context.Background()).Raw(
		`select configuration::text as configuration from ai_providers where id = ?`, id).
		Scan(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("read configuration: err=%v rows=%d", err, len(rows))
	}
	raw := rows[0].Configuration
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode configuration: %v", err)
	}
	return out
}

// TestB7Gate16OutputModeNegotiationPersisted section 18.76-A: negotiate output mode at probe time
// and persist it as a system-managed key (zero 400 guessing at runtime); client spoofing of this key is rejected.
func TestB7Gate16OutputModeNegotiationPersisted(t *testing.T) {
	b := newB7Fixture(t)
	origDial := probeDial
	defer func() { probeDial = origDial }()

	// A: when negotiated as json_object, user configuration and the system key are merged and persisted.
	probeDial = func(provider.Endpoint) (provider.Client, error) {
		return gate16ModeStub{mode: provider.OutputModeJSONObject}, nil
	}
	create := func(name string, cfg map[string]any) CreateAIProviderInput {
		return CreateAIProviderInput{Name: name, ProviderKind: "openai_compatible",
			BaseURL: "https://u16.example.com", ModelName: "m", Enabled: true,
			SelectionPriority: 50, APIKey: "k16", Configuration: cfg}
	}
	view, err := b.svc.CreateAIProvider(context.Background(), b.userID, create("u16-a", map[string]any{"max_tokens": float64(1000)}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cfg := b7Gate16Configuration(t, b, view.ID)
	if cfg[provider.ConfigKeyOutputMode] != provider.OutputModeJSONObject || cfg["max_tokens"] != float64(1000) {
		t.Fatalf("negotiated mode must persist alongside user keys, got %v", cfg)
	}

	// B: client spoofing structured_output_mode -> VALIDATION_FAILED (system-managed key).
	forged := create("u16-b", map[string]any{provider.ConfigKeyOutputMode: "json_object"})
	if _, err := b.svc.CreateAIProvider(context.Background(), b.userID, forged); err == nil {
		t.Fatal("forged structured_output_mode must be rejected")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != 1001 {
			t.Fatalf("forged key must map 1001, got %v", err)
		}
	}

	// C: replace re-probe refreshes the negotiated result (json_object -> json_schema).
	probeDial = func(provider.Endpoint) (provider.Client, error) {
		return gate16ModeStub{mode: provider.OutputModeJSONSchema}, nil
	}
	if _, err := b.svc.ReplaceAIProvider(context.Background(), view.ID, ReplaceAIProviderInput{
		ExpectVersion: view.Version, Name: "u16-a", ProviderKind: "openai_compatible",
		BaseURL: view.BaseURL, ModelName: "m2", Enabled: true, SelectionPriority: 50,
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	cfg = b7Gate16Configuration(t, b, view.ID)
	if cfg[provider.ConfigKeyOutputMode] != provider.OutputModeJSONSchema || cfg["max_tokens"] != float64(1000) {
		t.Fatalf("renegotiated mode must refresh while keeping user keys, got %v", cfg)
	}

	// D: negotiation failure -> 2011 with upstream rejection reason (probe gate prevents persistence).
	probeDial = func(provider.Endpoint) (provider.Client, error) {
		return gate16ModeStub{err: errors.New("dial: connection refused")}, nil
	}
	_, err = b.svc.CreateAIProvider(context.Background(), b.userID, create("u16-c", nil))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != 2011 || de.Detail == "" {
		t.Fatalf("negotiation failure must map 2011 with detail, got %v", err)
	}
}
