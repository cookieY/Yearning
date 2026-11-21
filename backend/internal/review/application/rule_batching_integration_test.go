//go:build integration

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

type atomicCountingFactSource struct {
	tableMetaCalls atomic.Int64
	catalogCalls   atomic.Int64
	statsCalls     atomic.Int64
}

func (f *atomicCountingFactSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql"}, nil
}

func (f *atomicCountingFactSource) SchemaMetadata(_ context.Context, schema string) (map[string]any, error) {
	return map[string]any{"schema": schema, "table_count": 1, "tables": []any{"orders"}}, nil
}

func (f *atomicCountingFactSource) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	f.tableMetaCalls.Add(1)
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 2, "returned_column_count": 2,
		"columns": []any{
			map[string]any{"column_name": "id", "ordinal": 1, "type_class": "integer"},
			map[string]any{"column_name": "amount", "ordinal": 2, "type_class": "integer"},
		},
		"metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}

func (f *atomicCountingFactSource) TableStatistics(_ context.Context, schema, table string) (map[string]any, error) {
	f.statsCalls.Add(1)
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 2, "estimate": true, "metadata_complete": true, "truncated": false,
	}, nil
}

func (f *atomicCountingFactSource) IndexMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"indexes": []map[string]any{{"index_name": "PRIMARY"}}, "metadata_complete": true, "truncated": false,
	}, nil
}

func (f *atomicCountingFactSource) SafeExplain(_ context.Context, schema, _ string) (map[string]any, error) {
	return map[string]any{"schema": schema, "access_method": "index_scan", "estimated_rows": 10, "estimate": true}, nil
}

func (f *atomicCountingFactSource) PolicyCatalog(_ context.Context, schemas []string) (database.PolicyCatalog, error) {
	f.catalogCalls.Add(1)
	identifiers := make([]database.PolicyCatalogIdentifier, 0, len(schemas))
	for _, sc := range schemas {
		identifiers = append(identifiers, database.PolicyCatalogIdentifier{
			Kind: database.PolicyCatalogSchema, Schema: sc, Name: sc,
		})
	}
	return database.PolicyCatalog{Identifiers: identifiers, SchemaCount: len(schemas)}, nil
}

func seedCustomPromptTool(t *testing.T, b *b7fx, name, findingKey, knowledgeText string) string {
	t.Helper()
	id := uuid.NewString()
	def := PromptToolDefinition{
		KnowledgeText: knowledgeText,
		MaxSeverity:   "high",
		Version:       1,
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := promptToolConfigHash(name, "all", map[string]any{}, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`
		insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, finding_key, created_by_user_id, created_at, updated_at)
		values (?, ?, 'enabled', 'all', '{}'::jsonb, ?, ?, 1, ?, ?, now(), now())`,
		id, name, defJSON, hash, findingKey, b.userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`
		insert into rule_set_prompt_tools (rule_set_id, prompt_tool_id)
		select id, ? from rule_sets limit 1`, id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func clearSeededPromptTools(t *testing.T, b *b7fx) {
	t.Helper()
	if err := b.db.Exec(`delete from rule_set_prompt_tools`).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`delete from prompt_tools`).Error; err != nil {
		t.Fatal(err)
	}
}

func updateProviderProfileAndWindow(t *testing.T, b *b7fx, modelName, profile string, contextWindow int) {
	t.Helper()
	cfg := map[string]any{
		"context_window":        contextWindow,
		"system_prompt_profile": profile,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`
		update ai_providers set model_name = ?, configuration = ?::jsonb, version = version + 1 where id = ?`,
		modelName, string(raw), b.providerID).Error; err != nil {
		t.Fatal(err)
	}
}

func runEphemeralAndWait(t *testing.T, b *b7fx, runner *AgentRunner, sqlText string, materialize bool) (*EphemeralRun, string) {
	t.Helper()
	draft := b.createDraftWithSQL(t, sqlText)
	runID, err := b.svc.StartEphemeralRun(context.Background(), b.owner, runner, EphemeralStartInput{
		FlowID:         b.flowID,
		DraftID:        draft.ID,
		SQL:            []byte(sqlText),
		OutputLocale:   "en-US",
		IdempotencyKey: "batch-eph-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("StartEphemeralRun: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		er := b.svc.EphemeralGetForTest(runID, b.owner.UserID)
		if er != nil && er.TerminalState() {
			if materialize {
				if merr := b.db.Transaction(func(tx *gorm.DB) error {
					return b.svc.MaterializeEphemeralRunTx(context.Background(), tx, er, draft.ID, draft.Revision)
				}); merr != nil {
					t.Fatalf("MaterializeEphemeralRunTx: %v", merr)
				}
			}
			return er, runID
		}
		if time.Now().After(deadline) {
			t.Fatalf("ephemeral run %s did not reach terminal state", runID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRuleFastFilterIntegration_DBAndEphemeral(t *testing.T) {
	b := newB7Fixture(t)
	clearSeededPromptTools(t, b)
	seedCustomPromptTool(t, b, "custom.insert-columns", "custom.insert-columns", "INSERT must list target columns explicitly.")
	seedCustomPromptTool(t, b, "custom.update-delete-where", "custom.update-delete-where", "UPDATE or DELETE must include a WHERE clause.")
	seedCustomPromptTool(t, b, "custom.drop-truncate-block", "custom.drop-truncate-block", "Forbid DROP and TRUNCATE statements.")
	seedCustomPromptTool(t, b, "custom.audit-updated-at", "custom.audit-updated-at", "Verify updated_at audit timestamp conventions.")

	var mu sync.Mutex
	var capturedPrompts []string
	facts := &atomicCountingFactSource{}
	runner := NewAgentRunner(b.svc, AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
				mu.Lock()
				capturedPrompts = append(capturedPrompts, req.SystemPrompt)
				mu.Unlock()
				if len(req.NormalizedEvidence) == 0 {
					tables, err := scopedTablesFromRequest(req)
					if err != nil || len(tables) == 0 {
						return nil, 500, fmt.Errorf("missing scoped tables: %v", err)
					}
					raw, _ := json.Marshal(map[string]any{
						"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
						"summary": "Requesting table metadata.", "findings": []any{},
						"tool_requests": []map[string]any{{
							"tool_call_id": uuid.NewString(),
							"tool_name":    "schema_metadata",
							"arguments":    map[string]any{"schema": tables[0].Schema, "table": tables[0].Table},
						}},
					})
					return raw, 200, nil
				}
				raw, _ := json.Marshal(map[string]any{
					"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
					"summary": "Review completed.", "findings": []any{}, "tool_requests": []any{},
				})
				return raw, 200, nil
			}), nil
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return facts, func() {}, nil
		},
	})

	// Ephemeral Review Path with pure INSERT SQL (materialized to DB).
	er, _ := runEphemeralAndWait(t, b, runner, "INSERT INTO orders (id, amount) VALUES (1, 100);", true)

	er.mu.RLock()
	var ephSkills []string
	for _, tc := range er.ToolCalls {
		if tc.ToolKind == "prompt_tool" {
			ephSkills = append(ephSkills, tc.ToolName)
		}
	}
	ephState := er.Run.State
	er.mu.RUnlock()
	if ephState != string(domain.RunStateReady) {
		t.Fatalf("ephemeral run expected ready, got %s", ephState)
	}
	if strings.Join(ephSkills, ",") != "custom.audit-updated-at,custom.insert-columns" {
		t.Fatalf("ephemeral path expected only matching+fail-open skills, got %v", ephSkills)
	}
	if len(capturedPrompts) == 0 {
		t.Fatal("expected provider prompts captured")
	}
	if strings.Contains(capturedPrompts[0], "custom.update-delete-where") || strings.Contains(capturedPrompts[0], "custom.drop-truncate-block") {
		t.Fatalf("filtered-out skills leaked into prompt:\n%s", capturedPrompts[0])
	}
	if !strings.Contains(capturedPrompts[0], "custom.insert-columns") || !strings.Contains(capturedPrompts[0], "custom.audit-updated-at") {
		t.Fatalf("expected surviving skills missing from prompt:\n%s", capturedPrompts[0])
	}
}

func TestAdaptiveRuleBatchingIntegration_SANAndDefaultShareScheduler(t *testing.T) {
	profiles := []struct {
		name         string
		modelName    string
		profile      string
		expectMarker string
	}{
		{name: "san_8192", modelName: "san-4b", profile: promptprofile.ProfileSAN, expectMarker: "<rules>"},
		{name: "default_8192", modelName: "gpt-4o-mini", profile: promptprofile.ProfileDefault, expectMarker: "=== AUDIT SKILLS"},
	}

	for _, tc := range profiles {
		t.Run(tc.name, func(t *testing.T) {
			b := newB7Fixture(t)
			clearSeededPromptTools(t, b)
			updateProviderProfileAndWindow(t, b, tc.modelName, tc.profile, 8192)

			body := strings.Repeat("Inspect every SQL statement thoroughly for schema safety and runtime locks. ", 8)
			const totalSkills = 22
			for i := 0; i < totalSkills; i++ {
				name := fmt.Sprintf("custom.batch-rule-%02d", i)
				seedCustomPromptTool(t, b, name, name, fmt.Sprintf("Rule %02d applies to all statements: %s", i, body))
			}

			facts := &atomicCountingFactSource{}
			var callCounter atomic.Int64
			var mu sync.Mutex
			var seenPrompts []string

			runner := NewAgentRunner(b.svc, AgentDeps{
				Dial: func(ep provider.Endpoint) (provider.Client, error) {
					return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
						callIdx := callCounter.Add(1)
						mu.Lock()
						seenPrompts = append(seenPrompts, req.SystemPrompt)
						mu.Unlock()

						if !strings.Contains(req.SystemPrompt, tc.expectMarker) {
							return nil, 500, fmt.Errorf("prompt missing expected renderer marker %q", tc.expectMarker)
						}
						if len(req.NormalizedEvidence) == 0 {
							return nil, 500, fmt.Errorf("expected pre-hydrated NormalizedEvidence on batch call %d", callIdx)
						}
						gid := req.AnonymizedStatementGroups[0].GroupID
						evID := req.NormalizedEvidence[0].EvidenceID

						sev := "low"
						title := "Low severity duplicate observation"
						if callIdx >= 2 {
							sev = "medium"
							title = "Higher severity merged observation"
						}
						findings := []map[string]any{{
							"finding_key":           "obs.merged-overlap",
							"category":              "performance",
							"severity":              sev,
							"title":                 title,
							"message":               "Observed overlap across rule chunks.",
							"suggestion":            "Inspect index selectivity.",
							"fingerprint_group_ids": []string{gid},
							"evidence_ids":          []string{evID},
							"fact_basis":            "evidence",
						}}
						if strings.Contains(req.SystemPrompt, "custom.batch-rule-00") {
							findings = append(findings, map[string]any{
								"finding_key":           "custom.batch-rule-00",
								"category":              "governance",
								"severity":              "high",
								"title":                 "Mandatory policy violation",
								"message":               "Matched custom.batch-rule-00.",
								"suggestion":            "Adjust SQL statement.",
								"fingerprint_group_ids": []string{gid},
								"evidence_ids":          []string{evID},
								"fact_basis":            "evidence",
							})
						}
						raw, _ := json.Marshal(map[string]any{
							"schema_version": 1,
							"review_run_id":  req.ReviewRunID,
							"outcome":        "complete",
							"summary":        fmt.Sprintf("Chunk %d completed.", callIdx),
							"findings":       findings,
							"tool_requests":  []any{},
						})
						return raw, 200, nil
					}), nil
				},
				FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
					return facts, func() {}, nil
				},
			})

			_, runID := runEphemeralAndWait(t, b, runner, "UPDATE orders SET amount = 20 WHERE id = 1;", true)

			if callCounter.Load() < 2 {
				t.Fatalf("expected multi-batch execution (>=2 provider calls) for 8192 window, got %d", callCounter.Load())
			}
			if facts.tableMetaCalls.Load() != 1 {
				t.Fatalf("expected target DB TableMetadata called exactly once across all batches, got %d", facts.tableMetaCalls.Load())
			}
			for i := 0; i < totalSkills; i++ {
				key := fmt.Sprintf("custom.batch-rule-%02d", i)
				found := false
				for _, p := range seenPrompts {
					if strings.Contains(p, key) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("skill %q missing from all batch prompts", key)
				}
			}

			var runState string
			var errCode *int32
			var failureName *string
			if err := b.db.Raw(`select state, business_err_code, failure_name from review_runs where id = ?`, runID).Row().Scan(&runState, &errCode, &failureName); err != nil {
				t.Fatal(err)
			}
			if runState != string(domain.RunStateBlocked) || errCode == nil || *errCode != 2008 {
				var cVal int32
				if errCode != nil {
					cVal = *errCode
				}
				var fVal string
				if failureName != nil {
					fVal = *failureName
				}
				t.Fatalf("expected blocked/2008, got %s/%d (%s)", runState, cVal, fVal)
			}

			var findingRows []struct {
				Severity string
				Title    string
			}
			if err := b.db.Raw(`
				select rf.severity, rf.title
				from review_findings rf
				join stage_review_snapshots s on s.id = rf.stage_review_id
				where s.review_run_id = ?
				order by rf.severity, rf.title`, runID).Scan(&findingRows).Error; err != nil {
				t.Fatal(err)
			}
			if len(findingRows) != 2 {
				t.Fatalf("expected 2 merged findings (1 medium overlap + 1 critical mandatory), got %d: %+v", len(findingRows), findingRows)
			}
			if findingRows[0].Severity != "critical" || findingRows[1].Severity != "medium" || findingRows[1].Title != "Higher severity merged observation" {
				t.Fatalf("unexpected merged findings: %+v", findingRows)
			}

			var ledgerRows int64
			if err := b.db.Raw(`select count(*) from ai_usage_ledger where review_run_id = ? and outcome = 'succeeded'`, runID).Scan(&ledgerRows).Error; err != nil {
				t.Fatal(err)
			}
			if ledgerRows != callCounter.Load() {
				t.Fatalf("expected %d ledger rows matching batch calls, got %d", callCounter.Load(), ledgerRows)
			}
		})
	}
}

func TestAdaptiveRuleBatchingIntegration_FailClosedOversizedSingleRule(t *testing.T) {
	b := newB7Fixture(t)
	clearSeededPromptTools(t, b)
	updateProviderProfileAndWindow(t, b, "san-4b", promptprofile.ProfileSAN, 4096)

	hugeText := strings.Repeat("Critical mandatory rule text that exceeds the entire context window by itself. ", 450)
	seedCustomPromptTool(t, b, "custom.huge-mandatory-rule", "custom.huge-mandatory-rule", hugeText)

	var providerCalls atomic.Int64
	facts := &atomicCountingFactSource{}
	runner := NewAgentRunner(b.svc, AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
				providerCalls.Add(1)
				raw, _ := json.Marshal(map[string]any{
					"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
					"summary": "Should never execute.", "findings": []any{}, "tool_requests": []any{},
				})
				return raw, 200, nil
			}), nil
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return facts, func() {}, nil
		},
	})

	_, runID := runEphemeralAndWait(t, b, runner, "UPDATE orders SET amount = 10 WHERE id = 1;", true)

	if providerCalls.Load() != 0 {
		t.Fatalf("expected 0 provider calls when oversized rule fails closed, got %d", providerCalls.Load())
	}
	var state string
	var code *int32
	var failureName *string
	if err := b.db.Raw(`select state, business_err_code, failure_name from review_runs where id = ?`, runID).Row().Scan(&state, &code, &failureName); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunStateFailed) || code == nil || *code != 2007 || failureName == nil || *failureName != "rule_exceeds_context_window" {
		t.Fatalf("expected failed/2007/rule_exceeds_context_window, got state=%s code=%v failure=%v", state, code, failureName)
	}
}

func TestAdaptiveRuleBatchingIntegration_ChunkFailurePropagatesStageSemantics(t *testing.T) {
	t.Run("chunk provider failure transitions run to failed 2011", func(t *testing.T) {
		b := newB7Fixture(t)
		clearSeededPromptTools(t, b)
		updateProviderProfileAndWindow(t, b, "san-4b", promptprofile.ProfileSAN, 8192)

		body := strings.Repeat("Enforce strict production schema and safety standards across all statements. ", 8)
		for i := 0; i < 20; i++ {
			name := fmt.Sprintf("custom.fail-rule-%02d", i)
			seedCustomPromptTool(t, b, name, name, fmt.Sprintf("Rule %02d: %s", i, body))
		}

		var calls atomic.Int64
		facts := &atomicCountingFactSource{}
		runner := NewAgentRunner(b.svc, AgentDeps{
			Dial: func(ep provider.Endpoint) (provider.Client, error) {
				return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
					if calls.Add(1) == 2 {
						return nil, 401, nil
					}
					raw, _ := json.Marshal(map[string]any{
						"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
						"summary": "Chunk ok.", "findings": []any{}, "tool_requests": []any{},
					})
					return raw, 200, nil
				}), nil
			},
			FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return facts, func() {}, nil
			},
		})

		_, runID := runEphemeralAndWait(t, b, runner, "UPDATE orders SET amount = 30 WHERE id = 1;", true)

		var state string
		var code *int32
		if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code); err != nil {
			t.Fatal(err)
		}
		if state != string(domain.RunStateFailed) || code == nil || *code != 2011 {
			t.Fatalf("expected failed/2011 on chunk provider error, got state=%s code=%v", state, code)
		}
	})

	t.Run("chunk partial outcome converges stage and run to partial 2007", func(t *testing.T) {
		b := newB7Fixture(t)
		clearSeededPromptTools(t, b)
		updateProviderProfileAndWindow(t, b, "san-4b", promptprofile.ProfileSAN, 8192)

		body := strings.Repeat("Enforce strict production schema and safety standards across all statements. ", 8)
		for i := 0; i < 20; i++ {
			name := fmt.Sprintf("custom.partial-rule-%02d", i)
			seedCustomPromptTool(t, b, name, name, fmt.Sprintf("Rule %02d: %s", i, body))
		}

		var calls atomic.Int64
		facts := &atomicCountingFactSource{}
		runner := NewAgentRunner(b.svc, AgentDeps{
			Dial: func(ep provider.Endpoint) (provider.Client, error) {
				return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
					outcome := "complete"
					if calls.Add(1) == 2 {
						outcome = "partial"
					}
					raw, _ := json.Marshal(map[string]any{
						"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": outcome,
						"summary": "Chunk completed.", "findings": []any{}, "tool_requests": []any{},
					})
					return raw, 200, nil
				}), nil
			},
			FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return facts, func() {}, nil
			},
		})

		_, runID := runEphemeralAndWait(t, b, runner, "UPDATE orders SET amount = 40 WHERE id = 1;", true)

		var state string
		var code *int32
		if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).Row().Scan(&state, &code); err != nil {
			t.Fatal(err)
		}
		if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
			t.Fatalf("expected partial/2007 when any chunk is partial, got state=%s code=%v", state, code)
		}
	})

	t.Run("sibling cancellation mid-call preserves root-cause budget exhaustion 2010", func(t *testing.T) {
		b := newB7Fixture(t)
		clearSeededPromptTools(t, b)
		updateProviderProfileAndWindow(t, b, "san-4b", promptprofile.ProfileSAN, 8192)
		// Allow only 1 stage reservation (100 minor units) so batch 0 reserves and
		// enters its provider call while batch 1 immediately fails with ErrBudgetExhausted.
		b.enforceBudget(t, 100)

		body := strings.Repeat("Enforce strict production schema and safety standards across all statements. ", 8)
		for i := 0; i < 20; i++ {
			name := fmt.Sprintf("custom.budget-rule-%02d", i)
			seedCustomPromptTool(t, b, name, name, fmt.Sprintf("Rule %02d: %s", i, body))
		}

		facts := &atomicCountingFactSource{}
		runner := NewAgentRunner(b.svc, AgentDeps{
			Dial: func(ep provider.Endpoint) (provider.Client, error) {
				return &contextAwareFakeClient{
					onReview: func(ctx context.Context, _ provider.Request) (provider.CallOutcome, error) {
						<-ctx.Done()
						return provider.CallOutcome{}, ctx.Err()
					},
				}, nil
			},
			FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return facts, func() {}, nil
			},
		})

		_, runID := runEphemeralAndWait(t, b, runner, "UPDATE orders SET amount = 50 WHERE id = 1;", true)

		var state string
		var code *int32
		var failureName *string
		if err := b.db.Raw(`select state, business_err_code, failure_name from review_runs where id = ?`, runID).Row().Scan(&state, &code, &failureName); err != nil {
			t.Fatal(err)
		}
		if code == nil || *code != 2010 || failureName == nil || *failureName != "budget_exhausted" {
			t.Fatalf("expected root-cause 2010/budget_exhausted preserved over sibling cancellation, got state=%s code=%v failure=%v", state, code, failureName)
		}
	})
}

type contextAwareFakeClient struct {
	onReview func(ctx context.Context, req provider.Request) (provider.CallOutcome, error)
}

func (c *contextAwareFakeClient) ValidateConfiguration(context.Context) error { return nil }
func (c *contextAwareFakeClient) NegotiateOutputMode(context.Context) (string, error) {
	return provider.OutputModeJSONSchema, nil
}
func (c *contextAwareFakeClient) Health(context.Context) error { return nil }
func (c *contextAwareFakeClient) ReviewStructured(ctx context.Context, req provider.Request) (provider.CallOutcome, error) {
	return c.onReview(ctx, req)
}
