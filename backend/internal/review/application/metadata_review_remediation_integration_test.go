//go:build integration

package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

const repeatedProviderToolID = "81f391d0-4192-4e6c-846e-6ccb7e00d44a"

func completeWithTools(req provider.Request, tools []map[string]any) ([]byte, int, error) {
	body, _, _ := emptyCompleteProviderBody(req)
	var response map[string]any
	_ = json.Unmarshal(body, &response)
	if tools != nil {
		response["tool_requests"] = tools
	}
	raw, err := json.Marshal(response)
	return raw, 200, err
}

func TestMetadataToolIDIsScopedToStage(t *testing.T) {
	b := newB7Fixture(t)
	b.addReviewLocaleSecondStage(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		if len(req.NormalizedEvidence) == 0 {
			requests, err := scopedMetadataToolRequests(req, repeatedProviderToolID)
			if err != nil {
				t.Fatal(err)
			}
			return completeWithTools(req, requests)
		}
		return completeWithTools(req, nil)
	})
	runID := b.runMetadataContextReview(t, deps, "UPDATE orders SET amount=1 WHERE id=1;")
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil || run.State != "ready" {
		t.Fatalf("cross-stage identical provider IDs: state=%v err=%v", run, err)
	}
	var count int64
	b.db.Raw(`SELECT count(*) FROM review_tool_calls WHERE review_run_id=? AND tool_kind='builtin_database_fact'`, runID).Scan(&count)
	if count != 2 {
		t.Fatalf("persisted tool calls=%d want 2", count)
	}
}

func TestMetadataToolIDCannotHideDifferentRequest(t *testing.T) {
	b := newB7Fixture(t)
	turn := 0
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		turn++
		if turn <= 2 {
			requests, err := scopedMetadataToolRequests(req, repeatedProviderToolID)
			if err != nil {
				t.Fatal(err)
			}
			if turn == 2 {
				requests[0]["tool_name"] = "index_metadata"
			}
			return completeWithTools(req, requests)
		}
		return completeWithTools(req, nil)
	})
	runID := b.runMetadataContextReview(t, deps, "UPDATE orders SET amount=1 WHERE id=1;")
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil || run.State != "partial" {
		t.Fatalf("conflicting ID must be incomplete: state=%v err=%v", run, err)
	}
	if b.facts.indexCalls != 0 {
		t.Fatal("conflicting request executed")
	}
}

func TestMetadataToolIDReplayComparesArguments(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical request", true: "different arguments"}[changed], func(t *testing.T) {
			b := newB7Fixture(t)
			turn := 0
			deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
				turn++
				if turn <= 2 {
					requests, err := scopedMetadataToolRequests(req, repeatedProviderToolID)
					if err != nil {
						t.Fatal(err)
					}
					if changed && turn == 2 {
						requests[0]["arguments"].(map[string]any)["table"] = "table_999"
					}
					return completeWithTools(req, requests)
				}
				return completeWithTools(req, nil)
			})
			runID := b.runMetadataContextReview(t, deps, "UPDATE orders SET amount=1 WHERE id=1;")
			run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
			want := map[bool]string{false: "ready", true: "partial"}[changed]
			if err != nil || run.State != want {
				t.Fatalf("state=%v err=%v, want %s", run, err, want)
			}
			var count int64
			b.db.Raw(`SELECT count(*) FROM review_tool_calls WHERE review_run_id=? AND tool_kind='builtin_database_fact'`, runID).Scan(&count)
			if count != map[bool]int64{false: 1, true: 2}[changed] {
				t.Fatalf("audit calls=%d", count)
			}
		})
	}
}

func TestMetadataRuntimeDowngradeDiscardsCompleteSummary(t *testing.T) {
	b := newB7Fixture(t)
	runID := b.runMetadataContextReview(t, b.fakeDial(emptyCompleteProviderBody), "UPDATE orders SET amount=1 WHERE id=1;")
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "partial" || len(run.StageResults) != 1 || run.StageResults[0].State != "partial" {
		t.Fatalf("coverage must be partial, not risk blocked: %#v", run)
	}
	if run.StageResults[0].Summary != nil || run.StageResults[0].SummaryLocale != nil {
		t.Fatal("runtime downgrade preserved a contradictory complete summary")
	}
}

func TestUnsupportedObjectMetadataCannotBeSkippedByEmptyComplete(t *testing.T) {
	for _, sql := range []string{
		"DROP VIEW private_view;", "DROP INDEX private_index;", "DROP SEQUENCE private_sequence;",
		"ALTER TABLE orders DROP CONSTRAINT private_constraint;",
	} {
		t.Run(sql, func(t *testing.T) {
			b := newB7Fixture(t)
			if strings.HasPrefix(sql, "DROP INDEX ") {
				// Standalone DROP INDEX without ON <table> is valid PostgreSQL DDL syntax.
				usePostgresStage(t, b)
			}
			runID := b.runMetadataContextReview(t, b.fakeDial(emptyCompleteProviderBody), sql)
			expectMetadataContextPartial(t, b, runID)
		})
	}
}

func TestInvalidFindingReferenceDiscardsCompleteSummary(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
		body, _, _ := emptyCompleteProviderBody(req)
		var response map[string]any
		_ = json.Unmarshal(body, &response)
		response["findings"] = []any{map[string]any{
			"finding_key": "runtime.reference", "category": "correctness", "severity": "low",
			"title": "Inspection finding", "message": "Please inspect statement.", "suggestion": "Confirm statement.",
			"fingerprint_group_ids": []string{"unknown-group"}, "fact_basis": "model_only", "evidence_ids": []string{},
		}}
		raw, err := json.Marshal(response)
		return raw, 200, err
	})
	runID := b.runMetadataContextReview(t, deps, "SELECT 1;")
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil || run.State != "partial" || len(run.StageResults) != 1 {
		t.Fatalf("run=%v err=%v", run, err)
	}
	if run.StageResults[0].Summary != nil {
		t.Fatal("late reference validation retained complete summary")
	}
}

const (
	irs01UnqualifiedTable  = "irs01_private_unqualified_table"
	irs01QualifiedTable    = "irs01_private_qualified_table"
	irs01UnqualifiedIndex  = "irs01_private_unqualified_index"
	irs01ScopeTableAlpha   = "irs01_private_scope_alpha"
	irs01ScopeTableBeta    = "irs01_private_scope_beta"
	irs01ScopeSchemaAlpha  = "irs01_private_schema_alpha"
	irs01ScopeSchemaBeta   = "irs01_private_schema_beta"
	irs01MetadataToolIDOne = "e9f16a7d-0000-4000-8000-000000000101"
	irs01MetadataToolIDTwo = "e9f16a7d-0000-4000-8000-000000000102"
)

type irs01RecordingFactSource struct {
	*fakeFactSource
	tableCalls []scopedTableTokens
	indexCalls []scopedTableTokens
}

func (s *irs01RecordingFactSource) TableMetadata(
	_ context.Context, schema, table string,
) (map[string]any, error) {
	s.tableCalls = append(s.tableCalls, scopedTableTokens{Schema: schema, Table: table})
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"columns": []any{map[string]any{
			"column_name": "id", "ordinal": 1, "type_class": "integer",
		}},
		"metadata_complete": true, "truncated": false,
	}, nil
}

func (s *irs01RecordingFactSource) IndexMetadata(
	_ context.Context, schema, table string,
) (map[string]any, error) {
	s.indexCalls = append(s.indexCalls, scopedTableTokens{Schema: schema, Table: table})
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"indexes": []any{}, "metadata_complete": true, "truncated": false,
	}, nil
}

func (b *b7fx) irs01Deps(
	source *irs01RecordingFactSource,
	respond func(provider.Request) ([]byte, int, error),
) AgentDeps {
	return AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(ep, respond), nil
		},
		FactSourceFor: func(
			context.Context, string, FrozenDatasourceTarget, string,
		) (tool.FactSource, func(), error) {
			return source, func() {}, nil
		},
	}
}

// configureIRS01Scope points the fixture datasource default database to the first scope schema
// (RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: under unmapped semantics, the stage allowed scope = datasources.database_name; subsequent schemas are rejected by the scope graph).
func configureIRS01Scope(t *testing.T, b *b7fx, schemas []string) {
	t.Helper()
	if len(schemas) == 0 {
		t.Fatal("IRS01 scope fixture requires at least one schema")
	}
	if err := b.db.Exec(`update datasources set database_name = ? where id in
		(select datasource_id from flow_stages where flow_id = ?)`,
		schemas[0], b.flowID).Error; err != nil {
		t.Fatalf("point datasource default database at the scope schema: %v", err)
	}
}

func usePostgresStage(t *testing.T, b *b7fx) {
	t.Helper()
	if err := b.db.Exec(`update datasources
		set engine = 'postgresql', compatibility_mode = 'postgresql', version_constraint = '17.x'
		where id in (select datasource_id from flow_stages where flow_id = ?)`,
		b.flowID).Error; err != nil {
		t.Fatalf("switch stage datasource to postgresql: %v", err)
	}
	stubPostgresStageSeams(b.fx)
}

func irs01SerializeAndDecodeRequest(
	t *testing.T, req provider.Request,
) ([]byte, provider.Request) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded provider.Request
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode complete Provider request: %v", err)
	}
	if decoded.RequestSchemaVersion != 1 || decoded.ReviewRunID == "" ||
		decoded.EngineProfile == "" || decoded.SystemPrompt == "" ||
		decoded.RuleSnapshot == nil || len(decoded.AnonymizedStatementGroups) == 0 ||
		len(decoded.AllowedToolDescriptors) == 0 || len(decoded.OutputSchema) == 0 {
		t.Fatalf("incomplete serialized Provider request: %s", raw)
	}
	return raw, decoded
}

func irs01AssertAnonymous(t *testing.T, raw []byte, identifiers ...string) {
	t.Helper()
	lower := strings.ToLower(string(raw))
	for _, identifier := range identifiers {
		if strings.Contains(lower, strings.ToLower(identifier)) {
			t.Fatalf("Provider request leaked raw identifier %q: %s", identifier, raw)
		}
	}
}

func irs01OnlyMetadataGroup(t *testing.T, req provider.Request) provider.AnonymizedGroup {
	t.Helper()
	if len(req.AnonymizedStatementGroups) != 1 {
		t.Fatalf("Provider groups = %d, want 1",
			len(req.AnonymizedStatementGroups))
	}
	return req.AnonymizedStatementGroups[0]
}

func irs01ObjectsOfKind(
	group provider.AnonymizedGroup, kind, relation string,
) []provider.AnonymizedObjectReference {
	var out []provider.AnonymizedObjectReference
	for _, object := range group.Objects {
		if object.Kind == kind && object.Relation == relation {
			out = append(out, object)
		}
	}
	return out
}

func irs01GroupHasSchemaObject(
	group provider.AnonymizedGroup, schemaToken string,
) bool {
	for _, object := range group.Objects {
		if object.Kind == "schema" && object.Relation == "reference" &&
			object.Token == schemaToken {
			return true
		}
	}
	return false
}

func irs01ExpectGateIncomplete(t *testing.T, b *b7fx, runID string) {
	t.Helper()
	expectMetadataContextPartial(t, b, runID)
	var gatePassed bool
	var reasons string
	if err := b.db.Raw(`
		select gate_passed, gate_reason_codes::text
		from stage_review_snapshots where review_run_id = ? limit 1`, runID).
		Row().Scan(&gatePassed, &reasons); err != nil {
		t.Fatalf("read stage gate: %v", err)
	}
	if gatePassed {
		t.Fatal("metadata-incomplete stage gate unexpectedly passed")
	}
	if !strings.Contains(reasons, "evidence_incomplete") {
		t.Fatalf("evidence_incomplete gate reason missing: %s", reasons)
	}
}

func irs01ExpectTableCalls(
	t *testing.T, calls []scopedTableTokens, expected ...scopedTableTokens,
) {
	t.Helper()
	if len(calls) != len(expected) {
		t.Fatalf("metadata calls = %+v, want %+v", calls, expected)
	}
	for _, want := range expected {
		found := false
		for _, got := range calls {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("metadata call %+v missing from %+v", want, calls)
		}
	}
}

func TestIRS01UnqualifiedDropTableGraphScopeAndEmptyComplete(t *testing.T) {
	t.Run("empty complete cannot skip object metadata", func(t *testing.T) {
		b := newB7Fixture(t)
		b.configureMetadataContextScope(t)
		source := &irs01RecordingFactSource{fakeFactSource: &fakeFactSource{}}
		var serialized []byte
		deps := b.irs01Deps(source, func(req provider.Request) ([]byte, int, error) {
			raw, decoded := irs01SerializeAndDecodeRequest(t, req)
			serialized = raw
			group := irs01OnlyMetadataGroup(t, decoded)
			if !group.MetadataContextComplete {
				t.Fatal("unqualified DROP TABLE should initially have complete object coverage")
			}
			tables := irs01ObjectsOfKind(group, "table", "target")
			if len(tables) != 1 || tables[0].Token == "" ||
				tables[0].SchemaToken == "" {
				t.Fatalf("unqualified DROP TABLE target graph = %+v", tables)
			}
			return completeWithTools(req, nil)
		})
		runID := b.runMetadataContextReview(
			t, deps, "DROP TABLE "+irs01UnqualifiedTable+";\n")
		irs01AssertAnonymous(t, serialized, irs01UnqualifiedTable)
		if len(source.tableCalls) != 0 || len(source.indexCalls) != 0 {
			t.Fatalf("empty complete reached metadata source: tables=%+v indexes=%+v",
				source.tableCalls, source.indexCalls)
		}
		irs01ExpectGateIncomplete(t, b, runID)
	})

	t.Run("scoped table fact permits complete review", func(t *testing.T) {
		b := newB7Fixture(t)
		b.configureMetadataContextScope(t)
		source := &irs01RecordingFactSource{fakeFactSource: &fakeFactSource{}}
		var serialized [][]byte
		turn := 0
		deps := b.irs01Deps(source, func(req provider.Request) ([]byte, int, error) {
			turn++
			raw, decoded := irs01SerializeAndDecodeRequest(t, req)
			serialized = append(serialized, raw)
			if turn == 1 {
				group := irs01OnlyMetadataGroup(t, decoded)
				tables := irs01ObjectsOfKind(group, "table", "target")
				if len(tables) != 1 || tables[0].SchemaToken == "" {
					t.Fatalf("unqualified DROP TABLE target graph = %+v", tables)
				}
				requests, err := scopedMetadataToolRequests(req, irs01MetadataToolIDOne)
				if err != nil {
					return nil, 500, err
				}
				return completeWithTools(req, requests)
			}
			return completeWithTools(req, nil)
		})
		runID := b.runMetadataContextReview(
			t, deps, "DROP TABLE "+irs01UnqualifiedTable+";\n")
		irs01ExpectTableCalls(t, source.tableCalls, scopedTableTokens{
			Schema: metadataContextSchema, Table: irs01UnqualifiedTable,
		})
		run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
		if err != nil || run.State != "ready" {
			t.Fatalf("scoped DROP TABLE review = %#v err=%v", run, err)
		}
		if len(serialized) < 2 {
			t.Fatalf("serialized Provider requests = %d, want tool and completion turns",
				len(serialized))
		}
		for _, raw := range serialized {
			irs01AssertAnonymous(t, raw, irs01UnqualifiedTable)
		}
	})
}

func TestIRS01UnqualifiedDropIndexHasNoCompleteMetadataParent(t *testing.T) {
	b := newB7Fixture(t)
	b.configureMetadataContextScope(t)
	// Standalone DROP INDEX without ON <table> is valid PostgreSQL DDL syntax.
	usePostgresStage(t, b)
	source := &irs01RecordingFactSource{fakeFactSource: &fakeFactSource{}}
	var serialized []byte
	deps := b.irs01Deps(source, func(req provider.Request) ([]byte, int, error) {
		raw, decoded := irs01SerializeAndDecodeRequest(t, req)
		serialized = raw
		group := irs01OnlyMetadataGroup(t, decoded)
		if group.MetadataContextComplete {
			t.Fatal("standalone DROP INDEX must be metadata-incomplete")
		}
		indexes := irs01ObjectsOfKind(group, "index", "target")
		if len(indexes) != 1 || indexes[0].Token == "" ||
			indexes[0].SchemaToken != "" || indexes[0].ParentToken != "" {
			t.Fatalf("standalone DROP INDEX target graph = %+v", indexes)
		}
		if tables := irs01ObjectsOfKind(group, "table", "target"); len(tables) != 0 {
			t.Fatalf("standalone DROP INDEX invented a table target: %+v", tables)
		}
		return completeWithTools(req, nil)
	})
	runID := b.runMetadataContextReview(
		t, deps, "DROP INDEX "+irs01UnqualifiedIndex+";\n")
	irs01AssertAnonymous(t, serialized, irs01UnqualifiedIndex)
	if len(source.tableCalls) != 0 || len(source.indexCalls) != 0 {
		t.Fatalf("incomplete DROP INDEX reached metadata source: tables=%+v indexes=%+v",
			source.tableCalls, source.indexCalls)
	}
	irs01ExpectGateIncomplete(t, b, runID)
}

func TestIRS01MixedQualifiedAndUnqualifiedDropTablesNeedBothFacts(t *testing.T) {
	b := newB7Fixture(t)
	b.configureMetadataContextScope(t)
	source := &irs01RecordingFactSource{fakeFactSource: &fakeFactSource{}}
	var serialized [][]byte
	turn := 0
	deps := b.irs01Deps(source, func(req provider.Request) ([]byte, int, error) {
		turn++
		raw, decoded := irs01SerializeAndDecodeRequest(t, req)
		serialized = append(serialized, raw)
		if turn == 1 {
			if len(decoded.AnonymizedStatementGroups) != 2 {
				t.Fatalf("mixed batch Provider groups = %d, want 2",
					len(decoded.AnonymizedStatementGroups))
			}
			qualified, unqualified := decoded.AnonymizedStatementGroups[0],
				decoded.AnonymizedStatementGroups[1]
			qualifiedTables := irs01ObjectsOfKind(qualified, "table", "target")
			unqualifiedTables := irs01ObjectsOfKind(unqualified, "table", "target")
			if len(qualifiedTables) != 1 || qualifiedTables[0].SchemaToken == "" ||
				!irs01GroupHasSchemaObject(qualified, qualifiedTables[0].SchemaToken) {
				t.Fatalf("qualified table object graph missing schema/table: group=%+v",
					qualified)
			}
			if len(unqualifiedTables) != 1 || unqualifiedTables[0].SchemaToken == "" {
				t.Fatalf("unqualified table object missing or lost default schema: group=%+v",
					unqualified)
			}
			if irs01GroupHasSchemaObject(unqualified, unqualifiedTables[0].SchemaToken) {
				t.Fatalf("unqualified table unexpectedly has a schema reference object: %+v",
					unqualified)
			}
			tables, err := scopedTablesFromRequest(req)
			if err != nil {
				return nil, 500, err
			}
			if len(tables) != 2 {
				t.Fatalf("mixed batch metadata scope tables = %+v, want qualified and unqualified",
					tables)
			}
			requests, err := scopedMetadataToolRequests(req, irs01MetadataToolIDOne)
			if err != nil {
				return nil, 500, err
			}
			if len(requests) != 2 {
				t.Fatalf("mixed batch metadata requests = %d, want 2", len(requests))
			}
			return completeWithTools(req, requests)
		}
		return completeWithTools(req, nil)
	})
	runID := b.runMetadataContextReview(t, deps,
		"DROP TABLE "+metadataContextSchema+"."+irs01QualifiedTable+";\n"+
			"DROP TABLE "+irs01UnqualifiedTable+";\n")
	irs01ExpectTableCalls(t, source.tableCalls,
		scopedTableTokens{Schema: metadataContextSchema, Table: irs01QualifiedTable},
		scopedTableTokens{Schema: metadataContextSchema, Table: irs01UnqualifiedTable},
	)
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil || run.State != "ready" {
		t.Fatalf("mixed batch review = %#v err=%v", run, err)
	}
	if len(serialized) < 2 {
		t.Fatalf("mixed batch Provider requests = %d, want tool and completion turns",
			len(serialized))
	}
	for _, raw := range serialized {
		irs01AssertAnonymous(t, raw,
			metadataContextSchema, irs01QualifiedTable, irs01UnqualifiedTable)
	}
}

func TestIRS01MetadataScopeRejectsKnownTokenPairingForUnrelatedTable(t *testing.T) {
	b := newB7Fixture(t)
	configureIRS01Scope(t, b, []string{irs01ScopeSchemaAlpha, irs01ScopeSchemaBeta})
	source := &irs01RecordingFactSource{fakeFactSource: &fakeFactSource{}}
	var serialized []byte
	turn := 0
	deps := b.irs01Deps(source, func(req provider.Request) ([]byte, int, error) {
		turn++
		raw, decoded := irs01SerializeAndDecodeRequest(t, req)
		if turn == 1 {
			serialized = raw
			targetCount := 0
			for groupIndex, group := range decoded.AnonymizedStatementGroups {
				groupTables := irs01ObjectsOfKind(group, "table", "target")
				for _, table := range groupTables {
					if table.SchemaToken == "" {
						t.Fatalf("scope group %d table target has no schema: %+v",
							groupIndex, table)
					}
				}
				if len(groupTables) == 0 {
					t.Fatalf("scope group %d table target graph = %+v",
						groupIndex, groupTables)
				}
				targetCount += len(groupTables)
			}
			if targetCount != 2 {
				t.Fatalf("scope fixture targets across Provider groups = %d, want 2",
					targetCount)
			}
			tables, err := scopedTablesFromRequest(req)
			if err != nil {
				return nil, 500, err
			}
			if len(tables) != 2 ||
				tables[0].Schema == tables[1].Schema ||
				tables[0].Table == tables[1].Table {
				t.Fatalf("scope fixture object graph = %+v", tables)
			}
			requests := []map[string]any{
				{
					"tool_call_id": irs01MetadataToolIDOne,
					"tool_name":    "schema_metadata",
					"arguments": map[string]any{
						"schema": tables[0].Schema,
						"table":  tables[0].Table,
					},
				},
				{
					"tool_call_id": irs01MetadataToolIDTwo,
					"tool_name":    "schema_metadata",
					"arguments": map[string]any{
						"schema": tables[0].Schema,
						"table":  tables[1].Table,
					},
				},
			}
			return completeWithTools(req, requests)
		}
		return completeWithTools(req, nil)
	})
	runID := b.runMetadataContextReview(t, deps,
		"DROP TABLE "+irs01ScopeSchemaAlpha+"."+irs01ScopeTableAlpha+";\n"+
			"DROP TABLE "+irs01ScopeSchemaBeta+"."+irs01ScopeTableBeta+";\n")
	irs01AssertAnonymous(t, serialized,
		irs01ScopeSchemaAlpha, irs01ScopeSchemaBeta,
		irs01ScopeTableAlpha, irs01ScopeTableBeta)
	if len(source.tableCalls) != 1 {
		t.Fatalf("scope filter calls = %+v, want only one allowed table", source.tableCalls)
	}
	got := source.tableCalls[0]
	if got != (scopedTableTokens{
		Schema: irs01ScopeSchemaAlpha,
		Table:  irs01ScopeTableAlpha,
	}) {
		t.Fatalf("allowed table metadata call = %+v", got)
	}
	if got == (scopedTableTokens{
		Schema: irs01ScopeSchemaAlpha,
		Table:  irs01ScopeTableBeta,
	}) {
		t.Fatalf("known-token cross-pair reached metadata source: %+v", got)
	}
	irs01ExpectGateIncomplete(t, b, runID)
}
