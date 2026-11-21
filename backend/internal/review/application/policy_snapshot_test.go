package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
)

func TestCanonicalReviewPolicyIdentityIsDeterministic(t *testing.T) {
	base := reviewPolicySnapshotFixture()
	baseID, _, err := canonicalReviewPolicyIdentity(base)
	if err != nil {
		t.Fatal(err)
	}

	reordered := reviewPolicySnapshotFixture()
	reordered.Flow.Stages[0], reordered.Flow.Stages[1] = reordered.Flow.Stages[1], reordered.Flow.Stages[0]
	reordered.RuleSet.MemberIDs[0], reordered.RuleSet.MemberIDs[1] =
		reordered.RuleSet.MemberIDs[1], reordered.RuleSet.MemberIDs[0]
	reordered.PromptTools[0], reordered.PromptTools[1] =
		reordered.PromptTools[1], reordered.PromptTools[0]
	reordered.KnowledgeEntries[0], reordered.KnowledgeEntries[1] =
		reordered.KnowledgeEntries[1], reordered.KnowledgeEntries[0]

	reorderedID, _, err := canonicalReviewPolicyIdentity(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedID != baseID {
		t.Fatalf("canonical policy identity depends on input order: %s != %s", reorderedID, baseID)
	}
}

func TestCanonicalReviewPolicyIdentityCoversEveryFrozenPolicySurface(t *testing.T) {
	base := reviewPolicySnapshotFixture()
	baseID, _, err := canonicalReviewPolicyIdentity(base)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*FrozenReviewPolicy)
	}{
		{
			name: "rule set identity",
			mutate: func(p *FrozenReviewPolicy) {
				p.RuleSet.ID = "00000000-0000-4000-8000-000000000099"
			},
		},
		{
			name: "rule set membership",
			mutate: func(p *FrozenReviewPolicy) {
				p.RuleSet.MemberIDs = append(p.RuleSet.MemberIDs, "00000000-0000-4000-8000-000000000098")
			},
		},
		{
			name: "prompt tool definition",
			mutate: func(p *FrozenReviewPolicy) {
				p.PromptTools[0].Definition.KnowledgeText = "replacement mandatory rule"
			},
		},
		{
			name: "prompt tool engine",
			mutate: func(p *FrozenReviewPolicy) {
				p.PromptTools[0].Engine = "postgresql"
			},
		},
		{
			name: "prompt tool parameter",
			mutate: func(p *FrozenReviewPolicy) {
				p.PromptTools[0].Parameters["max_rows"] = json.Number("501")
			},
		},
		{
			name: "knowledge definition",
			mutate: func(p *FrozenReviewPolicy) {
				p.KnowledgeEntries[0].Definition.KnowledgeText = "replacement knowledge rule"
			},
		},
		{
			name: "knowledge scope",
			mutate: func(p *FrozenReviewPolicy) {
				p.KnowledgeEntries[0].ScopeType = knowledge.ScopeTable
				p.KnowledgeEntries[0].TableName = "audit"
			},
		},
		{
			name: "stage engine",
			mutate: func(p *FrozenReviewPolicy) {
				p.Flow.Stages[0].DatasourceEngine = "mysql"
			},
		},
		{
			name: "stage compatibility profile",
			mutate: func(p *FrozenReviewPolicy) {
				p.Flow.Stages[0].DatasourceMode = "mysql"
			},
		},
		{
			name: "stage approval membership",
			mutate: func(p *FrozenReviewPolicy) {
				p.Flow.Stages[0].ApprovalStepActors = append(
					p.Flow.Stages[0].ApprovalStepActors,
					"00000000-0000-4000-8000-000000000097",
				)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := reviewPolicySnapshotFixture()
			tc.mutate(&policy)
			got, _, err := canonicalReviewPolicyIdentity(policy)
			if err != nil {
				t.Fatal(err)
			}
			if got == baseID {
				t.Fatalf("%s change did not alter canonical policy identity", tc.name)
			}
		})
	}
}

func TestEffectiveFrozenPolicyToolHashMatchesSubmissionContract(t *testing.T) {
	got := effectiveFrozenPolicyToolHash(FrozenReviewPolicy{
		PromptTools: []FrozenPromptTool{
			{ConfigHash: "tool-b"},
			{ConfigHash: "tool-a"},
		},
		KnowledgeEntries: []FrozenKnowledgeEntry{
			{ID: "k-2", ConfigHash: strings.Repeat("b", 64)},
			{ID: "k-1", ConfigHash: strings.Repeat("a", 64)},
		},
	})
	const want = "888384165d619bd3796b1c34087ed2ccbd90f1584b089218d75bb1093ea25a80"
	if got != want {
		t.Fatalf("effective frozen policy tool hash = %s, want %s", got, want)
	}
}

func reviewPolicySnapshotFixture() FrozenReviewPolicy {
	configA := strings.Repeat("a", 64)
	configB := strings.Repeat("b", 64)
	return FrozenReviewPolicy{
		SchemaVersion: reviewPolicySnapshotSchemaVersion,
		Flow: flowSnapshot{
			FlowID:      "00000000-0000-4000-8000-000000000001",
			FlowVersion: 7,
			Stages: []flowStageSummary{
				{
					Position:               2,
					DatasourceID:           "00000000-0000-4000-8000-000000000012",
					DatasourceEngine:       "postgresql",
					DatasourceMode:         "postgresql",
					DatasourceHost:         "db-2.internal",
					DatasourcePort:         5432,
					DatasourceDatabaseName: "analytics",
					DatasourceIdentityFP:   "identity-b",
					ApprovalStepActors:     []string{"user-c", "user-b"},
				},
				{
					Position:               1,
					DatasourceID:           "00000000-0000-4000-8000-000000000011",
					DatasourceEngine:       "mysql",
					DatasourceMode:         "mysql",
					DatasourceHost:         "db-1.internal",
					DatasourcePort:         3306,
					DatasourceDatabaseName: "orders",
					DatasourceIdentityFP:   "identity-a",
					ApprovalStepActors:     []string{"user-a"},
				},
			},
		},
		RuleSet: FrozenReviewRuleSet{
			ID:         "00000000-0000-4000-8000-000000000021",
			ConfigHash: configA,
			Enabled:    true,
			MemberIDs: []string{
				"00000000-0000-4000-8000-000000000032",
				"00000000-0000-4000-8000-000000000031",
			},
		},
		PromptTools: []FrozenPromptTool{
			{
				ID:         "00000000-0000-4000-8000-000000000032",
				Name:       "row-limit",
				Engine:     "mysql",
				Parameters: map[string]any{"mode": "strict"},
				Definition: PromptToolDefinition{
					KnowledgeText: "reject statements above {{max_rows}} rows",
					MaxSeverity:   "critical",
					Version:       1,
				},
				ConfigHash: configB,
				FindingKey: "skill.32",
				Version:    2,
			},
			{
				ID:         "00000000-0000-4000-8000-000000000031",
				Name:       "require-where",
				Engine:     "all",
				Parameters: map[string]any{"max_rows": json.Number("500")},
				Definition: PromptToolDefinition{
					KnowledgeText: "updates and deletes require a where clause",
					MaxSeverity:   "critical",
					Version:       1,
				},
				ConfigHash: configA,
				FindingKey: "skill.31",
				Version:    1,
			},
		},
		KnowledgeEntries: []FrozenKnowledgeEntry{
			{
				ID:           "00000000-0000-4000-8000-000000000042",
				Name:         "audit-schema",
				Purpose:      "schema policy",
				ScopeType:    knowledge.ScopeDatasource,
				DatasourceID: "00000000-0000-4000-8000-000000000012",
				DatabaseName: "analytics",
				Definition: knowledge.Definition{
					KnowledgeText: "analytic updates require a lock review",
					MaxSeverity:   "critical",
					Version:       1,
				},
				ConfigHash: configB,
				FindingKey: "exp.42",
				Version:    3,
			},
			{
				ID:        "00000000-0000-4000-8000-000000000041",
				Name:      "global-safety",
				Purpose:   "global policy",
				ScopeType: knowledge.ScopeGlobal,
				Definition: knowledge.Definition{
					KnowledgeText: "destructive SQL requires human review",
					MaxSeverity:   "critical",
					Version:       1,
				},
				ConfigHash: configA,
				FindingKey: "exp.41",
				Version:    1,
			},
		},
	}
}
