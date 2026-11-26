//go:build integration

// sec0041_schema_binding_integration_test.go SEC-0041 end-to-end regression
// (real PostgreSQL + Fake Provider, sharing the same fixture system as b7):
//
//	In-scope stage objects (datasources.database_name) -> tool is allowed and
//	  FactSource is reached;
//	Out-of-scope schema/table -> tool fails with 2012 booked and zero FactSource calls (never touching
//	  the DB or even constructing the evidence port);
//	safe_explain schema argument follows the exact same convention;
//	Flow hash drift (template/datasource mutated after Run launch) -> all stages fail closed;
//	Missing database_name -> remains fail-closed.
//	RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: flow stages no longer configure
//	logical-to-physical Schema mappings, so all scenarios are constructed under the unmapped branch.
package application

import (
	"encoding/json"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

const (
	sec0041SchemaToken = "$schema_token"
	sec0041TableToken  = "$table_token"
	sec0041GroupID     = "$group_id"
)

func resolveSEC0041ToolTokens(req provider.Request, tools []map[string]any) ([]map[string]any, error) {
	tables, err := scopedTablesFromRequest(req)
	if err != nil {
		// Fail-closed scenarios deliberately remove the defensible schema
		// binding. Leave placeholder arguments in place so the runtime rejects
		// them as unknown identifiers and records 2012 instead of failing the
		// Provider before tools are considered.
		tables = nil
	}
	var schemaToken, tableToken, groupID string
	if len(tables) > 0 {
		schemaToken, tableToken = tables[0].Schema, tables[0].Table
	}
	if len(req.AnonymizedStatementGroups) > 0 {
		groupID = req.AnonymizedStatementGroups[0].GroupID
	}
	out := make([]map[string]any, 0, len(tools))
	for _, source := range tools {
		callID, _ := source["tool_call_id"].(string)
		tool := cloneToolRequest(source, callID)
		args, _ := tool["arguments"].(map[string]any)
		for key, value := range args {
			text, _ := value.(string)
			switch text {
			case sec0041SchemaToken:
				if schemaToken != "" {
					args[key] = schemaToken
				}
			case sec0041TableToken:
				if tableToken != "" {
					args[key] = tableToken
				}
			case sec0041GroupID:
				if groupID != "" {
					args[key] = groupID
				}
			}
		}
		out = append(out, tool)
	}
	return out, nil
}

// runSEC0041WithTools requests scripted tools on turn 1 and returns a conclusion on turn 2 (real
// two-turn model behavior), returning the RunID. Tools are issued only on turn 1; tool_call_id
// idempotent deduplication prevents re-execution on duplicate issuance, and turn index guarantees determinism here.
func runSEC0041WithTools(t *testing.T, firstRoundTools []map[string]any) (*b7fx, string) {
	return runSEC0041ScriptedSQL(t, firstRoundTools, nil, nil,
		"update orders set amount = amount + 1 where id = 1;\n")
}

// runSEC0041Scripted is an injectable variant of runSEC0041WithTools: pre runs before StartRun
// (mutations form the frozen content — SEC-0041 empty mapping / missing database_name seeds);
// mutate runs after StartRun freezes flow_snapshot_hash and before draining the task chain
// (mutations constitute flow hash drift — fail-closed scenario).
func runSEC0041Scripted(t *testing.T, firstRoundTools []map[string]any, pre, mutate func(*b7fx)) (*b7fx, string) {
	return runSEC0041ScriptedSQL(t, firstRoundTools, pre, mutate,
		"update orders set amount = amount + 1 where id = 1;\n")
}

func runSEC0041ScriptedSQL(
	t *testing.T, firstRoundTools []map[string]any, pre, mutate func(*b7fx), statementSQL string,
) (*b7fx, string) {
	t.Helper()
	b := newB7Fixture(t)
	b.providerAuditIDs = map[string]string{}
	if pre != nil {
		pre(b)
	}
	turn := 0
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		turn++
		var tools []map[string]any
		if turn == 1 {
			var err error
			tools, err = resolveSEC0041ToolTokens(req, firstRoundTools)
			if err != nil {
				return nil, 500, err
			}
			// RCP-20261101: snapshots reside in ephemeral Run memory (er.Snapshots) and are not in the
			// stage_review_snapshots table prior to materialization — read the first stage snapshot ID via the test read seam.
			var stageID string
			if er := b.svc.EphemeralGetForTest(req.ReviewRunID, b.owner.UserID); er != nil {
				er.mu.RLock()
				if len(er.Snapshots) > 0 {
					stageID = er.Snapshots[0].ID
				}
				er.mu.RUnlock()
			}
			for _, call := range tools {
				id, _ := call["tool_call_id"].(string)
				name, _ := call["tool_name"].(string)
				args, _ := call["arguments"].(map[string]any)
				signature, err := toolRequestSignature(provider.ToolRequest{ToolCallID: id, ToolName: name, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				b.providerAuditIDs[id] = storedToolCallID(req.ReviewRunID, stageID, id, signature)
			}
		}
		body := map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Review complete.",
			"findings": []map[string]any{{
				"finding_key": "skill.nowhere", "category": "correctness", "severity": "high",
				"title": "Unbounded mutation without WHERE", "message": "The statement has no WHERE condition.", "suggestion": "Add a WHERE condition.",
				"fingerprint_group_ids": []string{req.AnonymizedStatementGroups[0].GroupID},
				"evidence_ids":          []string{}, "fact_basis": "model_only",
			}},
			"tool_requests": tools,
		}
		raw, _ := json.Marshal(body)
		return raw, 200, nil
	})
	draft := b.createDraftWithSQL(t, statementSQL)
	er, runID := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(statementSQL),
		IdempotencyKey: "b7-" + t.Name() + "-" + runKeySuffix()})
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	return b, runID
}

func expectSEC0041RunState(t *testing.T, b *b7fx, runID, wantState string, wantCode int32) {
	t.Helper()
	var state string
	var code *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).
		Row().Scan(&state, &code); err != nil {
		t.Fatalf("read run state: %v", err)
	}
	if state != wantState || code == nil || *code != wantCode {
		t.Fatalf("run state = %s/%v, want %s/%d", state, code, wantState, wantCode)
	}
}

// toolCallRow reads the persisted result of a single tool call.
type toolCallRow struct {
	Success   bool
	ErrorCode *int32
}

func sec0041ToolCall(t *testing.T, b *b7fx, runID, callID string) toolCallRow {
	t.Helper()
	if storedID := b.providerAuditIDs[callID]; storedID != "" {
		callID = storedID
	}
	var row toolCallRow
	if err := b.db.Raw(`select success, error_code from review_tool_calls
		where review_run_id = ? and id = ? and tool_kind = 'builtin_database_fact'`,
		runID, callID).Row().Scan(&row.Success, &row.ErrorCode); err != nil {
		var state string
		var businessCode *int32
		var total int64
		b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).
			Row().Scan(&state, &businessCode)
		b.db.Raw(`select count(*) from review_tool_calls where review_run_id = ? and tool_kind = 'builtin_database_fact'`,
			runID).Scan(&total)
		code := any(nil)
		if businessCode != nil {
			code = *businessCode
		}
		t.Fatalf("tool call %s missing: %v (run=%s/%v tool_calls=%d)",
			callID, err, state, code, total)
	}
	return row
}

// TestSEC0041OutOfMappingToolsFail2012WithoutDBTouch out-of-scope schema and table pairs ->
// all three tools are booked as 2012; zero calls to any FactSource method (never touching the DB),
// and zero tool evidence rows are produced.
func TestSEC0041OutOfMappingToolsFail2012WithoutDBTouch(t *testing.T) {
	b, runID := runSEC0041WithTools(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000101",
			"tool_name": "schema_metadata", "arguments": map[string]any{"schema": "otherdb"}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000102",
			"tool_name": "table_statistics",
			"arguments": map[string]any{"schema": "otherdb", "table": "orders"}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000103",
			"tool_name": "safe_explain",
			"arguments": map[string]any{"schema": "otherdb", "group_id": "g-out"}},
	})
	for _, id := range []string{
		"9f0b1e2c-0000-4000-8000-000000000101",
		"9f0b1e2c-0000-4000-8000-000000000102",
		"9f0b1e2c-0000-4000-8000-000000000103",
	} {
		row := sec0041ToolCall(t, b, runID, id)
		if row.Success || row.ErrorCode == nil || *row.ErrorCode != 2012 {
			t.Fatalf("out-of-scope call %s must fail 2012, got success=%v code=%v",
				id, row.Success, row.ErrorCode)
		}
	}
	if f := b.facts; f == nil || f.calls != 0 || f.metadataCalls != 0 ||
		f.tableStatsCalls != 0 || f.indexCalls != 0 || f.explainCalls != 0 {
		t.Fatalf("out-of-scope calls must not touch fact source: %+v", b.facts)
	}
	var facts int64
	b.db.Raw(`select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`, runID).Scan(&facts)
	if facts != 0 {
		t.Fatalf("out-of-scope calls must not produce evidence, got %d", facts)
	}
	expectSEC0041RunState(t, b, runID, string(domain.RunStatePartial), 2007)
}

// TestSEC0041InMappingToolsPass in-scope objects -> allowed: table-scoped schema_metadata and
// safe_explain both use Run tokens, reach FactSource, and persist evidence.
func TestSEC0041InMappingToolsPass(t *testing.T) {
	b, runID := runSEC0041WithTools(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000201",
			"tool_name": "schema_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000202",
			"tool_name": "safe_explain",
			"arguments": map[string]any{
				"schema": sec0041SchemaToken, "group_id": sec0041GroupID,
			}},
	})
	row := sec0041ToolCall(t, b, runID, "9f0b1e2c-0000-4000-8000-000000000201")
	if !row.Success {
		t.Fatalf("in-scope schema_metadata must succeed: %+v", row)
	}
	row = sec0041ToolCall(t, b, runID, "9f0b1e2c-0000-4000-8000-000000000202")
	if !row.Success {
		t.Fatalf("in-scope safe_explain must succeed: %+v", row)
	}
	if f := b.facts; f == nil || f.scopedMetadataCalls != 1 || f.explainCalls != 1 {
		t.Fatalf("in-scope calls must reach fact source: %+v", b.facts)
	}
	var facts int64
	b.db.Raw(`select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`, runID).Scan(&facts)
	if facts != 2 {
		t.Fatalf("in-scope evidence rows = %d, want 2", facts)
	}
	// Both tools are in scope with complete coverage; the High finding converges normally to blocked/2008.
	expectSEC0041RunState(t, b, runID, string(domain.RunStateBlocked), 2008)
}

func TestSEC0041UnqualifiedSQLUsesDefaultSchema(t *testing.T) {
	b, runID := runSEC0041ScriptedSQL(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000211",
			"tool_name": "schema_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000212",
			"tool_name": "table_statistics", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000213",
			"tool_name": "index_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000214",
			"tool_name": "safe_explain", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "group_id": sec0041GroupID,
			}},
	}, nil, nil, "update orders set amount = amount + 1 where id = 1;\n")
	for _, name := range []string{
		"schema_metadata", "table_statistics", "index_metadata", "safe_explain",
	} {
		var total, failures int64
		if err := b.db.Raw(`select count(*) from review_tool_calls
			where review_run_id = ? and tool_name = ?`, runID, name).Scan(&total).Error; err != nil {
			t.Fatal(err)
		}
		if err := b.db.Raw(`select count(*) from review_tool_calls
			where review_run_id = ? and tool_name = ? and not success`,
			runID, name).Scan(&failures).Error; err != nil {
			t.Fatal(err)
		}
		if total != 1 || failures != 0 {
			t.Fatalf("default schema tool %s = total %d failures %d", name, total, failures)
		}
	}
	if f := b.facts; f == nil || f.scopedMetadataCalls != 1 || f.tableStatsCalls != 1 ||
		f.indexCalls != 1 || f.explainCalls != 1 {
		t.Fatalf("default schema calls did not reach fact source: %+v", f)
	}
	var evidence int64
	b.db.Raw(`select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`,
		runID).Scan(&evidence)
	if evidence != 4 {
		t.Fatalf("default schema evidence rows = %d, want 4", evidence)
	}
}

func TestSEC0041OutOfScopeExplicitQualifierFailsClosedWithoutDBTouch(t *testing.T) {
	b, runID := runSEC0041ScriptedSQL(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000219",
			"tool_name": "schema_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000220",
			"tool_name": "table_statistics", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000221",
			"tool_name": "safe_explain", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "group_id": sec0041GroupID,
			}},
		// Since MySQL multi-db support allows valid non-system qualifiers (verified via catalog lookup),
		// built-in system schema references such as mysql.user must fail closed with 2012 (sql_schema_out_of_scope)
		// without reaching FactSource.
	}, nil, nil, "update mysql.user set amount = amount + 1 where id = 1;\n")
	for _, id := range []string{
		"9f0b1e2c-0000-4000-8000-000000000219",
		"9f0b1e2c-0000-4000-8000-000000000220",
		"9f0b1e2c-0000-4000-8000-000000000221",
	} {
		row := sec0041ToolCall(t, b, runID, id)
		if row.Success || row.ErrorCode == nil || *row.ErrorCode != 2012 {
			t.Fatalf("out-of-scope qualifier call %s must fail 2012, got success=%v code=%v",
				id, row.Success, row.ErrorCode)
		}
	}
	if f := b.facts; f == nil || f.calls != 0 || f.metadataCalls != 0 ||
		f.scopedMetadataCalls != 0 || f.tableStatsCalls != 0 ||
		f.indexCalls != 0 || f.explainCalls != 0 {
		t.Fatalf("out-of-scope qualifier calls must not touch fact source: %+v", f)
	}
	var evidence int64
	b.db.Raw(`select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`,
		runID).Scan(&evidence)
	if evidence != 0 {
		t.Fatalf("out-of-scope qualifier calls produced evidence: %d", evidence)
	}
	expectSEC0041RunState(t, b, runID, string(domain.RunStatePartial), 2007)
	var failureName *string
	if err := b.db.Raw(`select failure_name from review_runs where id = ?`, runID).Scan(&failureName).Error; err != nil {
		t.Fatalf("read failure_name: %v", err)
	}
	if failureName == nil || *failureName != "sql_schema_out_of_scope" {
		t.Fatalf("out-of-scope explicit schema failure_name = %v, want sql_schema_out_of_scope", failureName)
	}
}

// TestSEC0041PhysicalSchemaUsesMatchingFactsAndScopes covers B10-compatible
// SQL that explicitly names the mapped physical schema. The model-visible
// schema token comes from that physical SQL identity; trusted execution may
// use it directly, and returned facts must preserve the same token.
func TestSEC0041ExplicitAllowedSchemaUsesMatchingFactsAndScopes(t *testing.T) {
	b, runID := runSEC0041ScriptedSQL(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000215",
			"tool_name": "schema_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000216",
			"tool_name": "table_statistics", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000217",
			"tool_name": "index_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000218",
			"tool_name": "safe_explain", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "group_id": sec0041GroupID,
			}},
	}, nil, nil, "update shop.orders set amount = amount + 1 where id = 1;\n")
	for _, name := range []string{
		"schema_metadata", "table_statistics", "index_metadata", "safe_explain",
	} {
		var total, failures int64
		if err := b.db.Raw(`select count(*) from review_tool_calls
			where review_run_id = ? and tool_name = ?`, runID, name).Scan(&total).Error; err != nil {
			t.Fatal(err)
		}
		if err := b.db.Raw(`select count(*) from review_tool_calls
			where review_run_id = ? and tool_name = ? and not success`,
			runID, name).Scan(&failures).Error; err != nil {
			t.Fatal(err)
		}
		if total != 1 || failures != 0 {
			t.Fatalf("allowed schema tool %s = total %d failures %d", name, total, failures)
		}
	}
	if f := b.facts; f == nil || f.scopedMetadataCalls != 1 || f.tableStatsCalls != 1 ||
		f.indexCalls != 1 || f.explainCalls != 1 {
		t.Fatalf("allowed schema calls did not reach matching fact source: %+v", f)
	}
	var evidence int64
	b.db.Raw(`select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`,
		runID).Scan(&evidence)
	if evidence != 4 {
		t.Fatalf("allowed schema evidence rows = %d, want 4", evidence)
	}
}

// TestSEC0041MalformedTableRejectedBeforeDB malformed identifier in table argument
// (even when schema is in mapping) -> booked as 2012 without touching the DB (pre-DB check).
func TestSEC0041MalformedTableRejectedBeforeDB(t *testing.T) {
	b, runID := runSEC0041WithTools(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000301",
			"tool_name": "table_statistics",
			"arguments": map[string]any{"schema": sec0041SchemaToken, "table": "bad name"}},
	})
	row := sec0041ToolCall(t, b, runID, "9f0b1e2c-0000-4000-8000-000000000301")
	if row.Success || row.ErrorCode == nil || *row.ErrorCode != 2012 {
		t.Fatalf("malformed table must fail 2012, got success=%v code=%v", row.Success, row.ErrorCode)
	}
	if f := b.facts; f == nil || f.tableStatsCalls != 0 {
		t.Fatalf("malformed table must not touch fact source: %+v", b.facts)
	}
	if f := b.facts; f.scopedMetadataCalls != 0 {
		t.Fatalf("malformed table must not produce scoped metadata: %+v", f)
	}
	expectSEC0041RunState(t, b, runID, string(domain.RunStatePartial), 2007)
}

// TestSEC0041FlowHashDriftFailsClosedAllStages removed under RCP-20261101.
// Legacy premise: the sql_grouping task re-read flow/datasource rows upon worker claim and re-verified
// them against flow_snapshot_hash in the task payload — drift after launch but before execution (e.g.
// changing datasource host) was rejected with 2007 without leaving a run row. In ephemeral precheck,
// freezing and grouping complete within the same in-memory boundary of StartEphemeralRun: policy and
// stage profiles are frozen into er (er.FlowHash / snapshot envelopes), and review/evidence collection
// consume only the frozen copy with no post-launch re-read window; mutating the datasource afterwards
// does not affect an in-flight ephemeral Run (safety is guaranteed by freezing rather than re-verification rejection), and the next run sees the new profile.

// TestSEC0041EmptyMappingFallsBackToDatabaseName stages without mapping entries fall back to
// datasources.database_name: when the mapping decrypts to an empty list and database_name='shop'
// -> database_name serves as the sole allowed schema (shop is allowed and reaches the DB; other is rejected with 2012).
func TestSEC0041DatabaseNameIsTheOnlyAllowedSchema(t *testing.T) {
	b, runID := runSEC0041Scripted(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000501",
			"tool_name": "schema_metadata", "arguments": map[string]any{
				"schema": sec0041SchemaToken, "table": sec0041TableToken,
			}},
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000502",
			"tool_name": "safe_explain",
			"arguments": map[string]any{"schema": "other", "group_id": "g-fallback"}},
	}, nil, nil)
	row := sec0041ToolCall(t, b, runID, "9f0b1e2c-0000-4000-8000-000000000501")
	if !row.Success {
		t.Fatalf("database_name scope must allow schema=shop: %+v", row)
	}
	row = sec0041ToolCall(t, b, runID, "9f0b1e2c-0000-4000-8000-000000000502")
	if row.Success || row.ErrorCode == nil || *row.ErrorCode != 2012 {
		t.Fatalf("binding must reject schema outside database_name with 2012, got success=%v code=%v",
			row.Success, row.ErrorCode)
	}
	if f := b.facts; f == nil || f.scopedMetadataCalls != 1 || f.tableStatsCalls != 0 ||
		f.indexCalls != 0 || f.explainCalls != 0 {
		t.Fatalf("allow path must reach fact source exactly once: %+v", b.facts)
	}
	var facts int64
	b.db.Raw(`select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`, runID).Scan(&facts)
	if facts != 1 {
		t.Fatalf("allow path must produce exactly one evidence row, got %d", facts)
	}
	// A single out-of-bounds 2012 in the same stage makes coverage incomplete; evidence from the allowed
	// path is preserved, but terminal state converges to partial/2007 with incomplete coverage taking precedence over the High finding.
	expectSEC0041RunState(t, b, runID, string(domain.RunStatePartial), 2007)
}

// TestSEC0041MissingDatabaseNameFailsClosed when datasources.database_name is NULL, no provable
// policy catalog scope can be formed; fails closed before sending to the Provider, creating no tool calls and never touching the DB.
func TestSEC0041MissingDatabaseNameFailsClosed(t *testing.T) {
	b, runID := runSEC0041Scripted(t, []map[string]any{
		{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000601",
			"tool_name": "schema_metadata", "arguments": map[string]any{"schema": "shop"}},
	}, func(b *b7fx) {
		// Set NULL before StartRun so the target identity is frozen together with flow_snapshot_hash,
		// constructing an empty allowed object set.
		if err := b.db.Exec(`update datasources set database_name = null where id in
			(select datasource_id from flow_stages where flow_id = ?)`, b.flowID).Error; err != nil {
			t.Fatal(err)
		}
	}, nil)
	var toolCalls int64
	if err := b.db.Raw(
		`select count(*) from review_tool_calls
		 where review_run_id = ? and tool_kind = 'builtin_database_fact'`,
		runID,
	).Scan(&toolCalls).Error; err != nil {
		t.Fatal(err)
	}
	if toolCalls != 0 {
		t.Fatalf("missing database_name must fail before tool execution, got %d calls", toolCalls)
	}
	if f := b.facts; f == nil || f.calls != 0 || f.metadataCalls != 0 ||
		f.tableStatsCalls != 0 || f.indexCalls != 0 || f.explainCalls != 0 {
		t.Fatalf("fail-closed fallback must not touch fact source: %+v", b.facts)
	}
	expectSEC0041RunState(t, b, runID, string(domain.RunStateFailed), 2007)
}
