package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestExtractBatchStatementKinds(t *testing.T) {
	t.Run("extracts concrete canonical kinds from RepresentativeShape", func(t *testing.T) {
		groups := []provider.AnonymizedGroup{
			{GroupID: "g1", RepresentativeShape: "kind=dml op=update where=true"},
			{GroupID: "g2", RepresentativeShape: "kind=ddl op=alter where=true"},
		}
		ops, passAll := extractBatchStatementKinds(groups)
		if passAll {
			t.Fatal("expected passAll=false for known canonical ops")
		}
		if len(ops) != 2 || !ops["update"] || !ops["alter"] {
			t.Fatalf("unexpected ops set: %+v", ops)
		}
	})

	t.Run("extracts from single-word shape and statistics fallback", func(t *testing.T) {
		groups := []provider.AnonymizedGroup{
			{GroupID: "g1", RepresentativeShape: "INSERT"},
			{GroupID: "g2", Statistics: map[string]any{"operation": "DELETE"}},
		}
		ops, passAll := extractBatchStatementKinds(groups)
		if passAll {
			t.Fatal("expected passAll=false")
		}
		if len(ops) != 2 || !ops["insert"] || !ops["delete"] {
			t.Fatalf("unexpected ops set: %+v", ops)
		}
	})

	t.Run("fails open when groups are empty or any group is unknown or non-canonical", func(t *testing.T) {
		cases := []struct {
			name   string
			groups []provider.AnonymizedGroup
		}{
			{name: "empty groups", groups: nil},
			{name: "unknown op", groups: []provider.AnonymizedGroup{
				{GroupID: "g1", RepresentativeShape: "kind=dml op=insert where=true"},
				{GroupID: "g2", RepresentativeShape: "kind=dml op=unknown where=true"},
			}},
			{name: "other op", groups: []provider.AnonymizedGroup{
				{GroupID: "g1", RepresentativeShape: "kind=dml op=other where=true"},
			}},
			{name: "empty shape without stats", groups: []provider.AnonymizedGroup{
				{GroupID: "g1", RepresentativeShape: ""},
			}},
			{name: "non-canonical rename op", groups: []provider.AnonymizedGroup{
				{GroupID: "g1", RepresentativeShape: "kind=ddl op=rename where=true"},
			}},
			{name: "non-canonical with cte op", groups: []provider.AnonymizedGroup{
				{GroupID: "g1", RepresentativeShape: "kind=dml op=with where=true"},
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ops, passAll := extractBatchStatementKinds(tc.groups)
				if !passAll || ops != nil {
					t.Fatalf("expected fail-open (nil, true), got (%+v, %v)", ops, passAll)
				}
			})
		}
	})
}

func TestClassifySkillOps_WordBoundariesAndConservativeFailOpen(t *testing.T) {
	t.Run("single operation buckets", func(t *testing.T) {
		cases := []struct {
			name   string
			skill  agent.SkillInjection
			wantOp string
		}{
			{
				name:   "insert explicit columns",
				skill:  agent.SkillInjection{Name: "builtin.dml-insert-explicit-columns", FindingKey: "builtin.dml-insert-explicit-columns", KnowledgeText: "INSERT must specify target columns explicitly."},
				wantOp: "insert",
			},
			{
				name:   "forbid truncate",
				skill:  agent.SkillInjection{Name: "builtin.forbid-truncate", FindingKey: "builtin.forbid-truncate", KnowledgeText: "Forbid TRUNCATE statements."},
				wantOp: "truncate",
			},
			{
				name:   "forbid drop table",
				skill:  agent.SkillInjection{Name: "builtin.ddl-forbid-drop-table", FindingKey: "builtin.ddl-forbid-drop-table", KnowledgeText: "Forbid DROP TABLE statements."},
				wantOp: "drop",
			},
			{
				name:   "alter column type",
				skill:  agent.SkillInjection{Name: "builtin.ddl-forbid-column-type-change", FindingKey: "builtin.ddl-forbid-column-type-change", KnowledgeText: "ALTER must not modify column type."},
				wantOp: "alter",
			},
			{
				name:   "create table primary key",
				skill:  agent.SkillInjection{Name: "builtin.ddl-require-primary-key", FindingKey: "builtin.ddl-require-primary-key", KnowledgeText: "CREATE TABLE must declare a primary key."},
				wantOp: "create",
			},
			{
				name:   "chinese create table marker",
				skill:  agent.SkillInjection{Name: "custom.pk-check", FindingKey: "custom.pk-check", KnowledgeText: "\u5efa\u8868\u5fc5\u987b\u58f0\u660e\u4e3b\u952e"},
				wantOp: "create",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ops, isAll := classifySkillOps(tc.skill)
				if isAll {
					t.Fatalf("expected specific bucket %q, got isAll=true", tc.wantOp)
				}
				if len(ops) != 1 || !ops[tc.wantOp] {
					t.Fatalf("expected single bucket %q, got %+v", tc.wantOp, ops)
				}
			})
		}
	})

	t.Run("explicit compatible multi-op groups", func(t *testing.T) {
		updateDelete := agent.SkillInjection{
			Name:          "builtin.dml-require-where",
			FindingKey:    "builtin.dml-require-where",
			KnowledgeText: "UPDATE or DELETE statements must include a WHERE clause.",
		}
		ops, isAll := classifySkillOps(updateDelete)
		if isAll || len(ops) != 2 || !ops["update"] || !ops["delete"] {
			t.Fatalf("expected {update, delete}, got ops=%+v isAll=%v", ops, isAll)
		}

		dropTruncate := agent.SkillInjection{
			Name:          "custom.no-destructive-ddl",
			FindingKey:    "custom.no-destructive-ddl",
			KnowledgeText: "Block DROP and TRUNCATE operations in production.",
		}
		ops, isAll = classifySkillOps(dropTruncate)
		if isAll || len(ops) != 2 || !ops["drop"] || !ops["truncate"] {
			t.Fatalf("expected {drop, truncate}, got ops=%+v isAll=%v", ops, isAll)
		}
	})

	t.Run("word boundaries do not false-match column identifiers like updated_at", func(t *testing.T) {
		skills := []agent.SkillInjection{
			{
				Name:          "custom.audit-columns",
				FindingKey:    "custom.audit-columns",
				KnowledgeText: "Tables must include updated_at, created_at, and deleted_at timestamp columns.",
			},
			{
				Name:          "custom.update-time-column",
				FindingKey:    "custom.update_time_check",
				KnowledgeText: "Check that update_time and last_update are indexed properly without truncated_value.",
			},
		}
		for _, sk := range skills {
			ops, isAll := classifySkillOps(sk)
			if !isAll || ops != nil {
				t.Fatalf("expected identifier-only rule %q to classify as all, got ops=%+v isAll=%v", sk.Name, ops, isAll)
			}
		}
	})

	t.Run("generic, ambiguous, and cross-category rules conservatively fail open to all", func(t *testing.T) {
		skills := []agent.SkillInjection{
			{
				Name:          "custom.all-statements",
				FindingKey:    "custom.all-statements",
				KnowledgeText: "This rule applies to all statements, including UPDATE and INSERT.",
			},
			{
				Name:          "custom.chinese-generic",
				FindingKey:    "custom.chinese-generic",
				KnowledgeText: "\u901a\u7528\u89c4\u5219\uff1a\u6240\u6709\u8bed\u53e5\u90fd\u4e0d\u5f97\u8d85\u8fc7\u9608\u503c\uff0c\u4f8b\u5982 UPDATE\u3002",
			},
			{
				Name:          "builtin.max-statement-size",
				FindingKey:    "builtin.max-statement-size",
				KnowledgeText: "Statement byte length must not exceed configured limit.",
			},
			{
				Name:          "builtin.ddl-implicit-type-conversion",
				FindingKey:    "builtin.ddl-implicit-type-conversion",
				KnowledgeText: "WHERE literal types must match target column types.",
			},
			{
				Name:          "custom.cross-category-create-update",
				FindingKey:    "custom.cross-category",
				KnowledgeText: "CREATE TABLE timestamp columns must use ON UPDATE CURRENT_TIMESTAMP.",
			},
			{
				Name:          "builtin.ddl-view-forbid-select-star",
				FindingKey:    "builtin.ddl-view-forbid-select-star",
				KnowledgeText: "\u89c6\u56fe\u7981\u6b62 SELECT * (ddl_is_create_view=true).",
			},
			{
				Name:          "builtin.dml-forbid-subselect",
				FindingKey:    "builtin.dml-forbid-subselect",
				KnowledgeText: "DML statements must not embed SELECT subqueries.",
			},
		}
		for _, sk := range skills {
			ops, isAll := classifySkillOps(sk)
			if !isAll || ops != nil {
				t.Fatalf("expected fail-open all for %q, got ops=%+v isAll=%v", sk.Name, ops, isAll)
			}
		}
	})
}

func TestFilterStageSkills_PrecisionAndFailOpen(t *testing.T) {
	allSkills := []agent.SkillInjection{
		{ToolID: "1", Name: "skill.insert-only", FindingKey: "skill.insert-only", KnowledgeText: "INSERT must list columns explicitly."},
		{ToolID: "2", Name: "skill.update-delete", FindingKey: "skill.update-delete", KnowledgeText: "UPDATE and DELETE require WHERE."},
		{ToolID: "3", Name: "skill.drop-truncate", FindingKey: "skill.drop-truncate", KnowledgeText: "Forbid DROP and TRUNCATE."},
		{ToolID: "4", Name: "skill.updated-at-col", FindingKey: "skill.updated-at-col", KnowledgeText: "Audit column updated_at must be NOT NULL."},
		{ToolID: "5", Name: "skill.generic-size", FindingKey: "skill.generic-size", KnowledgeText: "Statement size limit applies to every statement."},
	}

	t.Run("pure INSERT batch filters out unrelated update/delete and drop/truncate rules", func(t *testing.T) {
		groups := []provider.AnonymizedGroup{{GroupID: "g1", RepresentativeShape: "kind=dml op=insert where=true"}}
		got := filterStageSkills(allSkills, groups)
		if len(got) != 3 {
			t.Fatalf("expected 3 surviving skills, got %d: %+v", len(got), got)
		}
		wantNames := map[string]bool{"skill.insert-only": true, "skill.updated-at-col": true, "skill.generic-size": true}
		for _, sk := range got {
			if !wantNames[sk.Name] {
				t.Fatalf("unexpected surviving skill %q", sk.Name)
			}
		}
	})

	t.Run("pure UPDATE batch keeps update/delete plus fail-open rules", func(t *testing.T) {
		groups := []provider.AnonymizedGroup{{GroupID: "g1", RepresentativeShape: "kind=dml op=update where=true"}}
		got := filterStageSkills(allSkills, groups)
		if len(got) != 3 {
			t.Fatalf("expected 3 surviving skills, got %d: %+v", len(got), got)
		}
		wantNames := map[string]bool{"skill.update-delete": true, "skill.updated-at-col": true, "skill.generic-size": true}
		for _, sk := range got {
			if !wantNames[sk.Name] {
				t.Fatalf("unexpected surviving skill %q", sk.Name)
			}
		}
	})

	t.Run("any unknown group in batch forces fail-open passthrough of all skills", func(t *testing.T) {
		groups := []provider.AnonymizedGroup{
			{GroupID: "g1", RepresentativeShape: "kind=dml op=insert where=true"},
			{GroupID: "g2", RepresentativeShape: "kind=dml op=unknown where=true"},
		}
		got := filterStageSkills(allSkills, groups)
		if len(got) != len(allSkills) {
			t.Fatalf("expected all %d skills on fail-open, got %d", len(allSkills), len(got))
		}
	})
}

func TestSortSkillsByDeterminism(t *testing.T) {
	in := []agent.SkillInjection{
		{ToolID: "3", Name: "b.rule", FindingKey: "b.rule", ConfigHash: "h3"},
		{ToolID: "2", Name: "a.rule", FindingKey: "a.rule", ConfigHash: "h2"},
		{ToolID: "1", Name: "a.rule", FindingKey: "a.rule", ConfigHash: "h1"},
	}
	got := sortSkillsByDeterminism(in)
	if len(got) != 3 || got[0].ToolID != "1" || got[1].ToolID != "2" || got[2].ToolID != "3" {
		t.Fatalf("unexpected deterministic order: %+v", got)
	}
	// Ensure original slice was not mutated.
	if in[0].ToolID != "3" {
		t.Fatalf("input slice mutated: %+v", in)
	}
}
