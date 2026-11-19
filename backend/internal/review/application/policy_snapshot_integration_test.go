//go:build integration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// TestPolicySnapshotBarrierNeverExecutesReplacementPolicy RCP-20261101 migration:
// the legacy version mutated the policy surface after StartRun froze the task payload snapshot but
// before the worker consumed it to verify the task-chain barrier; on the ephemeral path, policy
// freezing likewise comes from freezeReviewPolicy (called by StartEphemeralRun) and lands in in-memory
// er.Policy — this test mutates the policy surface during the first Provider call (after injection
// references have been frozen from er.Policy) and asserts that both the model prompt and the in-memory
// snapshot contain only the original definitions, never the replacement text.
func TestPolicySnapshotBarrierNeverExecutesReplacementPolicy(t *testing.T) {
	for _, tc := range []struct {
		name            string
		originalMarkers []string
		mutate          func(t *testing.T, b *b7fx, replacement string)
	}{
		{
			name: "tool definition",
			originalMarkers: []string{
				"must-where",
				"UPDATE/DELETE must include a WHERE clause",
			},
			mutate: mutateBarrierToolDefinition,
		},
		{
			name:            "rule set membership",
			originalMarkers: []string{"must-where"},
			mutate:          mutateBarrierRuleSetMembership,
		},
		{
			name: "knowledge definition",
			originalMarkers: []string{
				"global-guard",
				"global-guard boundary rule",
			},
			mutate: mutateBarrierKnowledgeDefinition,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newB7Fixture(t)
			sqlText := "update orders set amount = amount + 1 where id = 1;\n"
			draft := b.createDraftWithSQL(t, sqlText)
			replacement := "policy-barrier-replacement-" + strings.ReplaceAll(tc.name, " ", "-")
			var prompts []string
			mutated := false
			deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
				prompts = append(prompts, req.SystemPrompt)
				if !mutated {
					// Injection references are already frozen from er.Policy — mutate the live policy surface now
					// to construct the "replace after freeze" scenario (constructed during the queue window in the legacy task chain).
					mutated = true
					tc.mutate(t, b, replacement)
				}
				if len(req.AnonymizedStatementGroups) == 0 {
					return nil, 500, errors.New("provider request has no statement group")
				}
				return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
			})
			er, _ := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
				FlowID:         b.flowID,
				DraftID:        draft.ID,
				SQL:            []byte(sqlText),
				IdempotencyKey: "policy-barrier-" + runKeySuffix(),
			})
			if len(prompts) == 0 {
				t.Fatal("provider was never called with the frozen policy")
			}
			joined := strings.Join(prompts, "\n")
			for _, marker := range tc.originalMarkers {
				if !strings.Contains(joined, marker) {
					t.Fatalf("Provider did not receive original frozen marker %q", marker)
				}
			}
			if strings.Contains(joined, replacement) {
				t.Fatalf("Provider received replacement policy %q after freeze", replacement)
			}
			// In-memory snapshot (er.Policy) retains the original definitions.
			assertSnapshotMarkers(t, er.Policy, tc.originalMarkers...)
			assertSnapshotExcludes(t, er.Policy, replacement)
		})
	}
}

// TestPolicySnapshotRejectsSameRunSubstitution removed under RCP-20261101.
// Legacy premise: StartRun sealed the policy snapshot into the sql_grouping task payload, and the test
// constructed a TaskInput substituting another Run's valid snapshot into this Run's payload to verify
// Run-bound AAD rejection. On the ephemeral path, the policy snapshot is in-process er.Policy (with no
// cross-Run task payload to substitute); Run-binding and AAD semantics of the snapshot envelope remain
// continuously covered by unit tests TestPolicySnapshotSealVerifyRoundTrip and
// TestPolicySnapshotVerificationFailsClosed (the run-policy-other substitution case).

func mutateBarrierToolDefinition(t *testing.T, b *b7fx, replacement string) {
	t.Helper()
	ctx := context.Background()
	view, err := b.svc.GetPromptTool(ctx, b.toolID)
	if err != nil {
		t.Fatal(err)
	}
	def := view.Definition
	def.KnowledgeText = replacement
	if _, err := b.svc.ReplacePromptTool(
		ctx,
		view.ID,
		view.Version,
		view.Name,
		"enabled",
		def,
	); err != nil {
		t.Fatalf("replace tool definition after StartRun: %v", err)
	}
}

func mutateBarrierRuleSetMembership(t *testing.T, b *b7fx, replacement string) {
	t.Helper()
	ctx := context.Background()
	tool, err := b.svc.CreatePromptToolForEngine(
		ctx,
		b.userID,
		"policy-barrier-replacement-"+runKeySuffix(),
		"enabled",
		"all",
		nil,
		PromptToolDefinition{
			KnowledgeText: replacement,
			MaxSeverity:   "critical",
			Version:       1,
		},
		"",
	)
	if err != nil {
		t.Fatalf("create replacement tool: %v", err)
	}
	var ruleSetID string
	if err := b.db.Raw(`select rule_set_id::text from flows where id = ?`, b.flowID).
		Scan(&ruleSetID).Error; err != nil {
		t.Fatal(err)
	}
	view, err := b.svc.GetRuleSet(ctx, ruleSetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.ReplaceRuleSet(
		ctx,
		view.ID,
		view.Version,
		view.Name,
		true,
		view.Rules,
		[]string{tool.ID},
	); err != nil {
		t.Fatalf("replace rule-set membership after StartRun: %v", err)
	}
}

func mutateBarrierKnowledgeDefinition(t *testing.T, b *b7fx, replacement string) {
	t.Helper()
	repo := &knowledge.Repository{DB: b.db}
	entry, err := repo.Get(context.Background(), b.entryGlobal)
	if err != nil {
		t.Fatal(err)
	}
	entry.Definition.KnowledgeText = replacement
	service := &knowledge.Service{Repo: repo, Invalidate: InvalidateInputRefs}
	if _, err := service.Replace(
		context.Background(),
		entry.ID,
		knowledge.ReplaceInput{
			ExpectVersion: entry.Version,
			Name:          entry.Name,
			Purpose:       entry.Purpose,
			State:         entry.State,
			ScopeType:     entry.ScopeType,
			DatasourceID:  entry.DatasourceID,
			DatabaseName:  entry.DatabaseName,
			TableName:     entry.TableName,
			Definition:    entry.Definition,
		},
	); err != nil {
		t.Fatalf("replace knowledge definition after StartRun: %v", err)
	}
}

func assertSnapshotMarkers(t *testing.T, policy *FrozenReviewPolicy, markers ...string) {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range markers {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("frozen policy missing original marker %q", marker)
		}
	}
}

func assertSnapshotExcludes(t *testing.T, policy *FrozenReviewPolicy, marker string) {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatalf("frozen policy contains post-StartRun replacement %q", marker)
	}
}

func executePolicySnapshotBarrierTasks(
	t *testing.T,
	b *b7fx,
	agent *AgentRunner,
	runID string,
) error {
	t.Helper()
	for round := 0; round < 20; round++ {
		var rows []struct {
			ID    string
			Input []byte
		}
		if err := b.db.Raw(`
			select id::text as id, input
			from async_tasks
			where aggregate_id = ? and state = 'queued'
			order by created_at, id`, runID).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			var input TaskInput
			if err := json.Unmarshal(row.Input, &input); err != nil {
				t.Fatal(err)
			}
			if err := agent.Execute(context.Background(), b.db, input, nil, nil); err != nil {
				return err
			}
			if err := b.db.Exec(
				`update async_tasks set state = 'succeeded', finished_at = now() where id = ?`,
				row.ID,
			).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("policy snapshot barrier task chain did not drain")
	return nil
}

func policyBarrierStopped(t *testing.T, b *b7fx, runID string) bool {
	t.Helper()
	var runState string
	if err := b.db.Raw(`select state from review_runs where id = ?`, runID).
		Scan(&runState).Error; err != nil {
		t.Fatal(err)
	}
	var draftState string
	var raw []byte
	if err := b.db.Raw(`
		select input
		from async_tasks
		where aggregate_id = ?
		order by created_at, id
		limit 1`, runID).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	var input TaskInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatalf("decode policy barrier task input: %v", err)
		}
		if err := b.db.Raw(
			`select state from change_drafts where id = ?`, input.DraftID,
		).Scan(&draftState).Error; err != nil {
			t.Fatal(err)
		}
	}
	return runState == string(domain.RunStateOutdated) ||
		draftState == string(domain.DraftStateOutdated) ||
		draftState == string(domain.DraftStateFailed) ||
		draftState == string(domain.DraftStatePartial)
}
