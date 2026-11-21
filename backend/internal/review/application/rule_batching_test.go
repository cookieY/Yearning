package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

func makeBatchTestBuilder(t *testing.T, ep provider.Endpoint, groups []provider.AnonymizedGroup) ruleBatchRequestBuilder {
	t.Helper()
	mapper := privacy.NewRunMapper()
	ctx := withRepresentativeSQLResolver(context.Background(), func(_, _ string) (string, bool, error) {
		return "UPDATE app.orders SET status = 'done' WHERE id = 1;", true, nil
	})
	var cachedSAN []provider.AnonymizedGroup
	target := frozenDatasourceTarget{
		DatasourceID:      "ds-1",
		Engine:            "mysql",
		CompatibilityMode: "mysql",
		ServerVersion:     "8.0.36",
	}
	return func(
		reqCtx context.Context,
		rawSkills, compiledSkills []agent.SkillInjection,
		rawKnowledge, compiledKnowledge []*knowledge.Entry,
	) (provider.Request, error) {
		if endpointIsSAN(ep) {
			return buildSANStageRequest(ctx, stagePromptRequest{
				runID:           "run-batch-test",
				target:          target,
				groups:          groups,
				rawSkills:       rawSkills,
				rawKnowledge:    rawKnowledge,
				runMapper:       mapper,
				outputLocale:    "en-US",
				cachedSANGroups: &cachedSAN,
			})
		}
		cin := agent.ContextInput{
			EngineProfile: "mysql/mysql",
			OutputLocale:  "en-US",
			Skills:        compiledSkills,
			Knowledge:     compiledKnowledge,
			Groups:        groups,
		}
		return agent.BuildRequest("run-batch-test", cin, tool.NewRegistry()), nil
	}
}

func makeSyntheticSkills(count int, bodyRepeats int) []agent.SkillInjection {
	skills := make([]agent.SkillInjection, 0, count)
	body := strings.Repeat("Validate SQL constraint and execution risk carefully across target tables. ", bodyRepeats)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("custom.rule-%02d", i)
		skills = append(skills, agent.SkillInjection{
			ToolID:        fmt.Sprintf("tool-%02d", i),
			Name:          name,
			FindingKey:    name,
			Engine:        "all",
			KnowledgeText: fmt.Sprintf("Rule %02d: %s", i, body),
			MaxSeverity:   "high",
			ConfigHash:    fmt.Sprintf("hash-%02d", i),
		})
	}
	return sortSkillsByDeterminism(skills)
}

func makeSyntheticKnowledge(count int, bodyRepeats int) []*knowledge.Entry {
	entries := make([]*knowledge.Entry, 0, count)
	body := strings.Repeat("Production operational guardrail for high-traffic ledger tables. ", bodyRepeats)
	for i := 0; i < count; i++ {
		entries = append(entries, &knowledge.Entry{
			ID:         fmt.Sprintf("kb-%02d", i),
			Name:       fmt.Sprintf("exp-%02d", i),
			Purpose:    "risk_guard",
			ScopeType:  knowledge.ScopeGlobal,
			FindingKey: fmt.Sprintf("exp.rule-%02d", i),
			Definition: knowledge.Definition{
				KnowledgeText: fmt.Sprintf("Experience %02d: %s", i, body),
				MaxSeverity:   "high",
				Version:       1,
			},
			ConfigHash: fmt.Sprintf("kb-hash-%02d", i),
		})
	}
	knowledge.SortByDeterminism(entries)
	return entries
}

func TestPlanRuleBatches_SingleBatchPassthroughForLargeWindow(t *testing.T) {
	groups := []provider.AnonymizedGroup{
		{GroupID: "11111111-1111-4111-8111-111111111111", RepresentativeShape: "kind=dml op=update where=true", StatementCount: 1},
	}
	skills := makeSyntheticSkills(12, 2)
	entries := makeSyntheticKnowledge(3, 2)

	endpoints := []struct {
		name string
		ep   provider.Endpoint
	}{
		{
			name: "default large window 128k",
			ep: provider.Endpoint{
				ModelName:     "gpt-5",
				Configuration: map[string]any{"context_window": float64(128000)},
			},
		},
		{
			name: "san large window 64k",
			ep: provider.Endpoint{
				ModelName:     "san-4b",
				Configuration: map[string]any{"context_window": float64(65536), "system_prompt_profile": promptprofile.ProfileSAN},
			},
		},
	}

	for _, tc := range endpoints {
		t.Run(tc.name, func(t *testing.T) {
			batches, plan, err := planRuleBatches(
				context.Background(), tc.ep,
				skills, skills, entries, entries,
				makeBatchTestBuilder(t, tc.ep, groups),
			)
			if err != nil {
				t.Fatalf("planRuleBatches failed: %v", err)
			}
			if len(batches) != 1 {
				t.Fatalf("expected 1 single passthrough batch for large window, got %d (ruleBudget=%d)", len(batches), plan.ruleBudget)
			}
			if len(batches[0].rawSkills) != len(skills) || len(batches[0].rawKnowledge) != len(entries) {
				t.Fatalf("expected all skills (%d) and knowledge (%d) preserved in single batch, got (%d, %d)",
					len(skills), len(entries), len(batches[0].rawSkills), len(batches[0].rawKnowledge))
			}
		})
	}
}

func TestPlanRuleBatches_AutoSplitsSmallWindowAcrossSANAndDefault(t *testing.T) {
	groups := []provider.AnonymizedGroup{
		{GroupID: "11111111-1111-4111-8111-111111111111", RepresentativeShape: "kind=dml op=update where=true", StatementCount: 1},
	}
	skills := makeSyntheticSkills(24, 8)
	entries := makeSyntheticKnowledge(4, 8)

	endpoints := []struct {
		name string
		ep   provider.Endpoint
	}{
		{
			name: "san 8192 context window",
			ep: provider.Endpoint{
				ModelName:     "san-4b",
				Configuration: map[string]any{"context_window": float64(8192), "system_prompt_profile": promptprofile.ProfileSAN},
			},
		},
		{
			name: "default 8192 context window",
			ep: provider.Endpoint{
				ModelName:     "gpt-4o-mini",
				Configuration: map[string]any{"context_window": float64(8192), "system_prompt_profile": promptprofile.ProfileDefault},
			},
		},
	}

	for _, tc := range endpoints {
		t.Run(tc.name, func(t *testing.T) {
			builder := makeBatchTestBuilder(t, tc.ep, groups)
			batches1, plan, err := planRuleBatches(context.Background(), tc.ep, skills, skills, entries, entries, builder)
			if err != nil {
				t.Fatalf("planRuleBatches failed: %v", err)
			}
			if len(batches1) < 2 {
				t.Fatalf("expected multiple batches for small 8192 window, got %d (ruleBudget=%d)", len(batches1), plan.ruleBudget)
			}

			// Verify determinism by planning a second time.
			batches2, _, err := planRuleBatches(context.Background(), tc.ep, skills, skills, entries, entries, builder)
			if err != nil {
				t.Fatalf("second planRuleBatches failed: %v", err)
			}
			if len(batches1) != len(batches2) {
				t.Fatalf("non-deterministic batch count: %d vs %d", len(batches1), len(batches2))
			}

			// Verify every skill and knowledge entry appears exactly once in deterministic order
			// and every batch obeys ruleBudget.
			var flattenedSkills []string
			var flattenedKnowledge []string
			for i, b := range batches1 {
				if b.estimatedTokens > plan.ruleBudget {
					t.Fatalf("batch %d estimatedTokens=%d exceeds ruleBudget=%d", i, b.estimatedTokens, plan.ruleBudget)
				}
				if len(b.rawSkills) != len(batches2[i].rawSkills) || len(b.rawKnowledge) != len(batches2[i].rawKnowledge) {
					t.Fatalf("batch %d partition differs across runs", i)
				}
				for _, sk := range b.rawSkills {
					flattenedSkills = append(flattenedSkills, sk.Name)
				}
				for _, kb := range b.rawKnowledge {
					flattenedKnowledge = append(flattenedKnowledge, kb.ID)
				}
			}
			if len(flattenedSkills) != len(skills) {
				t.Fatalf("skill count mismatch: got %d, want %d", len(flattenedSkills), len(skills))
			}
			for i, sk := range skills {
				if flattenedSkills[i] != sk.Name {
					t.Fatalf("skill order mismatch at %d: got %q, want %q", i, flattenedSkills[i], sk.Name)
				}
			}
			if len(flattenedKnowledge) != len(entries) {
				t.Fatalf("knowledge count mismatch: got %d, want %d", len(flattenedKnowledge), len(entries))
			}
			for i, kb := range entries {
				if flattenedKnowledge[i] != kb.ID {
					t.Fatalf("knowledge order mismatch at %d: got %q, want %q", i, flattenedKnowledge[i], kb.ID)
				}
			}
		})
	}
}

func TestPlanRuleBatches_FailClosedOnOversizedSingleRule(t *testing.T) {
	groups := []provider.AnonymizedGroup{
		{GroupID: "11111111-1111-4111-8111-111111111111", RepresentativeShape: "kind=dml op=update where=true", StatementCount: 1},
	}
	hugeSkill := agent.SkillInjection{
		ToolID:        "huge-1",
		Name:          "custom.oversized-mandatory-rule",
		FindingKey:    "custom.oversized-mandatory-rule",
		Engine:        "all",
		KnowledgeText: strings.Repeat("Mandatory blocking audit specification that must never be dropped or truncated. ", 400),
		MaxSeverity:   "critical",
	}

	ep := provider.Endpoint{
		ModelName:     "san-4b",
		Configuration: map[string]any{"context_window": float64(4096), "system_prompt_profile": promptprofile.ProfileSAN},
	}
	batches, _, err := planRuleBatches(
		context.Background(), ep,
		[]agent.SkillInjection{hugeSkill}, []agent.SkillInjection{hugeSkill},
		nil, nil,
		makeBatchTestBuilder(t, ep, groups),
	)
	if err == nil {
		t.Fatalf("expected Fail-Closed error for oversized single rule, got %d batches", len(batches))
	}
	if !errors.Is(err, ErrRuleExceedsContextWindow) {
		t.Fatalf("expected ErrRuleExceedsContextWindow, got %v", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("expected domain.Error unwrap, got %T", err)
	}
	if de.Code != domain.CodeReviewCoverageIncomp || de.Name != "rule_exceeds_context_window" {
		t.Fatalf("unexpected domain.Error: %+v", de)
	}
	if !strings.Contains(de.Detail, "context_window") || !strings.Contains(de.Detail, "custom.oversized-mandatory-rule") {
		t.Fatalf("expected actionable detail mentioning context_window and rule name, got %q", de.Detail)
	}
}

func TestMergeBatchFindings_HighestSeverityAndDedupEvidence(t *testing.T) {
	g1 := "11111111-1111-4111-8111-111111111111"
	g2 := "22222222-2222-4222-8222-222222222222"

	batch0 := []provider.Finding{
		{
			FindingKey:          "obs.missing-index",
			Category:            "performance",
			Severity:            "low",
			Title:               "Low severity note",
			Message:             "Initial observation",
			Suggestion:          "Consider index",
			FingerprintGroupIDs: []string{g1, g2},
			EvidenceIDs:         []string{"ev-2", "ev-1"},
			FactBasis:           "evidence",
		},
		{
			FindingKey:          "builtin.dml-require-where",
			Category:            "safety",
			Severity:            "critical",
			Title:               "Missing WHERE",
			Message:             "Unbounded UPDATE",
			Suggestion:          "Add WHERE",
			FingerprintGroupIDs: []string{g1},
			EvidenceIDs:         []string{},
			FactBasis:           "model_only",
		},
	}
	batch1 := []provider.Finding{
		{
			FindingKey:          "obs.missing-index",
			Category:            "performance",
			Severity:            "high",
			Title:               "High severity full table scan",
			Message:             "Confirmed full table scan on large table",
			Suggestion:          "Add composite index",
			FingerprintGroupIDs: []string{g1},
			EvidenceIDs:         []string{"ev-3", "ev-1"},
			FactBasis:           "evidence",
		},
	}

	merged := mergeBatchFindings([][]provider.Finding{batch0, batch1})
	if len(merged) != 3 {
		t.Fatalf("expected 3 deduplicated (group, finding_key) entries, got %d: %+v", len(merged), merged)
	}

	// Sorted deterministically by (groupID, findingKey):
	// 0: (g1, builtin.dml-require-where)
	// 1: (g1, obs.missing-index) -> merged high + [ev-1, ev-2, ev-3]
	// 2: (g2, obs.missing-index) -> low + [ev-1, ev-2]
	if merged[0].FingerprintGroupIDs[0] != g1 || merged[0].FindingKey != "builtin.dml-require-where" {
		t.Fatalf("unexpected entry 0: %+v", merged[0])
	}
	f1 := merged[1]
	if f1.FingerprintGroupIDs[0] != g1 || f1.FindingKey != "obs.missing-index" {
		t.Fatalf("unexpected entry 1: %+v", f1)
	}
	if f1.Severity != "high" || f1.Title != "High severity full table scan" {
		t.Fatalf("expected highest severity (high) retained, got %+v", f1)
	}
	wantEv := []string{"ev-1", "ev-2", "ev-3"}
	if strings.Join(f1.EvidenceIDs, ",") != strings.Join(wantEv, ",") {
		t.Fatalf("expected merged deduped sorted evidence %v, got %v", wantEv, f1.EvidenceIDs)
	}
	f2 := merged[2]
	if f2.FingerprintGroupIDs[0] != g2 || f2.Severity != "low" {
		t.Fatalf("expected g2 entry untouched at low severity, got %+v", f2)
	}
}

type countingTestFactSource struct {
	schemaCalls *atomic.Int64
	statsCalls  *atomic.Int64
}

func (c countingTestFactSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql", "metadata_complete": true}, nil
}
func (c countingTestFactSource) SchemaMetadata(_ context.Context, schema string) (map[string]any, error) {
	c.schemaCalls.Add(1)
	return map[string]any{"schema": schema, "table_count": 1, "tables": []any{"orders"}}, nil
}
func (c countingTestFactSource) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	c.schemaCalls.Add(1)
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 1, "returned_column_count": 1,
		"columns":           []any{map[string]any{"column_name": "id", "ordinal": 1, "type_class": "integer"}},
		"metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}
func (c countingTestFactSource) TableStatistics(_ context.Context, schema, table string) (map[string]any, error) {
	c.statsCalls.Add(1)
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 1, "estimate": true, "metadata_complete": true, "truncated": false,
	}, nil
}
func (c countingTestFactSource) IndexMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"indexes": []map[string]any{{"index_name": "PRIMARY"}}, "metadata_complete": true, "truncated": false,
	}, nil
}
func (c countingTestFactSource) SafeExplain(_ context.Context, schema, _ string) (map[string]any, error) {
	return map[string]any{"schema": schema, "metadata_complete": true, "truncated": false}, nil
}

func newTestCrypterForBatchUnit(t *testing.T) *crypto.Crypter {
	t.Helper()
	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"batch-unit","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(7), makeKey(8))
	if err := os.WriteFile(keyPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStageEvidenceStore_PrehydrateAndSingleflightDeduplication(t *testing.T) {
	crypter := newTestCrypterForBatchUnit(t)
	var factOpenCount, schemaCalls, statsCalls atomic.Int64

	runner := &AgentRunner{
		svc:      &Services{Crypter: crypter},
		registry: tool.NewRegistry(),
		deps: AgentDeps{
			FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				factOpenCount.Add(1)
				return countingTestFactSource{schemaCalls: &schemaCalls, statsCalls: &statsCalls}, func() {}, nil
			},
		},
	}

	mapper := privacy.NewRunMapper()
	schemaTok, _ := mapper.Token(privacy.KindSchema, "shop")
	tableTok, _ := mapper.ScopedToken(privacy.KindTable, "shop", "orders")
	otherSchemaTok, _ := mapper.Token(privacy.KindSchema, "secret_db")
	otherTableTok, _ := mapper.ScopedToken(privacy.KindTable, "secret_db", "secrets")

	scope := newMetadataScope()
	scope.schemas[schemaTok] = true
	scope.objects[schemaTok] = map[string]bool{tableTok: true}

	binding := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"shop": true},
	}
	snap := &persistence.StageReviewSnapshotModel{ID: "snap-1", DatasourceID: "ds-1", StagePosition: 1}
	target := frozenDatasourceTarget{DatasourceID: "ds-1", Engine: "mysql", CompatibilityMode: "mysql"}

	store := newStageEvidenceStore(runner, "run-1", snap, target, binding, mapper, scope, agent.DefaultRuntimeBudget())

	// 1. Pre-hydrate once.
	if err := store.prehydrate(context.Background(), nil, nil); err != nil {
		t.Fatalf("prehydrate failed: %v", err)
	}
	if schemaCalls.Load() != 1 || factOpenCount.Load() != 1 {
		t.Fatalf("expected exactly 1 prehydrate schema call and 1 open, got schema=%d open=%d",
			schemaCalls.Load(), factOpenCount.Load())
	}

	// 2. Concurrent batches requesting already pre-hydrated schema_metadata must hit cache (0 extra DB queries).
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			added, _, inc, err := store.executeToolRequests(context.Background(), []provider.ToolRequest{{
				ToolCallID: fmt.Sprintf("00000000-0000-4000-8000-0000000000%02d", idx),
				ToolName:   "schema_metadata",
				Arguments:  map[string]any{"schema": schemaTok, "table": tableTok},
			}}, false)
			if err != nil || inc || len(added) != 1 {
				t.Errorf("cached schema_metadata call %d failed: added=%d inc=%v err=%v", idx, len(added), inc, err)
			}
		}(i)
	}
	wg.Wait()
	if schemaCalls.Load() != 1 || factOpenCount.Load() != 1 {
		t.Fatalf("cached schema_metadata must not re-query target DB: schema=%d open=%d",
			schemaCalls.Load(), factOpenCount.Load())
	}

	// 3. Concurrent batches requesting table_statistics coalesce via singleflight into 1 DB query.
	evIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			added, _, inc, err := store.executeToolRequests(context.Background(), []provider.ToolRequest{{
				ToolCallID: fmt.Sprintf("11111111-1111-4111-8111-1111111111%02d", idx),
				ToolName:   "table_statistics",
				Arguments:  map[string]any{"schema": schemaTok, "table": tableTok},
			}}, false)
			if err != nil || inc || len(added) != 1 {
				t.Errorf("singleflight table_statistics call %d failed: added=%d inc=%v err=%v", idx, len(added), inc, err)
				return
			}
			evIDs[idx] = added[0].EvidenceID
		}(i)
	}
	wg.Wait()
	if statsCalls.Load() != 1 {
		t.Fatalf("expected singleflight to coalesce table_statistics into 1 DB query, got %d", statsCalls.Load())
	}
	for i := 1; i < len(evIDs); i++ {
		if evIDs[i] == "" || evIDs[i] != evIDs[0] {
			t.Fatalf("expected all batches to share identical EvidenceID, got %v", evIDs)
		}
	}

	// 4. Out-of-scope table request must be rejected with 2012 BEFORE opening FactSource (SEC-0041).
	opensBefore := factOpenCount.Load()
	_, _, inc, err := store.executeToolRequests(context.Background(), []provider.ToolRequest{{
		ToolCallID: "22222222-2222-4222-8222-222222222222",
		ToolName:   "schema_metadata",
		Arguments:  map[string]any{"schema": otherSchemaTok, "table": otherTableTok},
	}}, false)
	if err != nil {
		t.Fatalf("unexpected fatal error on out-of-scope tool request: %v", err)
	}
	if !inc {
		t.Fatal("expected incomplete=true for out-of-scope request")
	}
	if factOpenCount.Load() != opensBefore {
		t.Fatalf("SEC-0041 violated: FactSourceFor was opened for out-of-scope object (%d -> %d)", opensBefore, factOpenCount.Load())
	}
}
