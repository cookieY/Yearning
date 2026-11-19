package application

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func policyCatalogTestCatalog(t *testing.T, identifiers ...database.PolicyCatalogIdentifier) database.PolicyCatalog {
	t.Helper()
	schemas := map[string]bool{}
	for _, identifier := range identifiers {
		schemas[identifier.Schema] = true
	}
	for schema := range schemas {
		identifiers = append(identifiers, database.PolicyCatalogIdentifier{
			Kind: database.PolicyCatalogSchema, Schema: schema, Name: schema,
		})
	}
	return database.PolicyCatalog{Identifiers: identifiers, SchemaCount: len(schemas)}
}

func policyCompilerFixture(t *testing.T) (database.PolicyCatalog, *stageSchemaBinding) {
	return policyCatalogTestCatalog(t,
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "audit_table"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "unused_table"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "archive", Name: "audit_table"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "customers"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogView, Schema: "app", Name: "audit_view"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "app", Relation: "audit_table", Name: "actor"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "app", Relation: "customers", Name: "actor"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogIndex, Schema: "app", Relation: "audit_table", Name: "audit_actor_idx"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogConstraint, Schema: "app", Relation: "audit_table", Name: "audit_actor_fk"},
	), bindingOf("app", "archive")
}
func TestPolicyCompilerRejectsRawCatalogAndTargetDatabaseNamesInEveryHumanChannel(t *testing.T) {
	catalog, binding := policyCompilerFixture(t)
	for _, name := range []string{"private_prod", "PRIVATE_PROD", "private&#95;prod", "private&amp;#95;prod", "audit_table", "UNUSED_TABLE", "actor", "audit_actor_idx", "audit_actor_fk", "audit_view", "audit&#95;table", "audit&amp;#95;table"} {
		for _, channel := range []string{"skill_name", "skill_text", "parameter", "knowledge_name", "purpose", "knowledge_text"} {
			t.Run(name+"/"+channel, func(t *testing.T) {
				sk := agent.SkillInjection{ToolID: "trusted", Name: "bounded policy", KnowledgeText: "Require bounded queries.", FindingKey: "skill.trusted"}
				entry := &knowledge.Entry{ID: "trusted", Name: "bounded policy", Purpose: "bounded query", Definition: knowledge.Definition{KnowledgeText: "Require bounded queries."}}
				switch channel {
				case "skill_name":
					sk.Name = name
				case "skill_text":
					sk.KnowledgeText = name
				case "parameter":
					sk.KnowledgeText = "Inspect {{target}}"
					sk.Parameters = map[string]any{"target": name}
				case "knowledge_name":
					entry.Name = name
				case "purpose":
					entry.Purpose = name
				case "knowledge_text":
					entry.Definition.KnowledgeText = name
				}
				_, err := compilePolicy([]agent.SkillInjection{sk}, []*knowledge.Entry{entry}, privacy.NewRunMapper(), binding, catalog, "private_prod", newMetadataScope())
				if !errors.Is(err, ErrPolicyPrivacy) {
					t.Fatalf("raw name allowed: %v", err)
				}
				if strings.Contains(err.Error(), name) {
					t.Fatal("error leaked raw name")
				}
			})
		}
	}
}
func TestPolicyCompilerGraphIdentityReplayAndChannels(t *testing.T) {
	catalog, binding := policyCompilerFixture(t)
	sk := agent.SkillInjection{ToolID: "audit_table", FindingKey: "skill.audit_table", Name: `policy {{ref:table:["app","audit_table"]}}`, KnowledgeText: `Inspect {{ref:column:["app","audit_table","actor"]}} and {{ref:column:["app","customers","actor"]}}; compare {{ref:table:["archive","audit_table"]}} and {{ref:index:["app","audit_table","audit_actor_idx"]}}; repeat {{ref:table:["app","audit_table"]}}; {{limit}}.`, Parameters: map[string]any{"limit": float64(10)}, MaxSeverity: "critical"}
	entry := &knowledge.Entry{ID: "audit_table", FindingKey: "exp.audit_table", Name: `review {{ref:table:["app","audit_table"]}}`, Purpose: `protect {{ref:column:["app","audit_table","actor"]}}`, Definition: knowledge.Definition{KnowledgeText: "Require bounded queries.", MaxSeverity: "low"}}
	m := privacy.NewRunMapper()
	prior := newMetadataScope()
	got, err := compilePolicy([]agent.SkillInjection{sk}, []*knowledge.Entry{entry}, m, binding, catalog, "private_prod", prior)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior.objects) != 0 || sk.Parameters == nil || entry.Name == got.knowledge[0].Name {
		t.Fatal("frozen source mutated")
	}
	var tables, columns []provider.AnonymizedObjectReference
	for _, o := range got.objects {
		if o.Kind == "table" {
			tables = append(tables, o)
		}
		if o.Kind == "column" {
			columns = append(columns, o)
		}
	}
	if len(tables) != 3 || len(columns) != 2 {
		t.Fatalf("graph counts %d/%d", len(tables), len(columns))
	}
	if columns[0].Token != columns[1].Token || columns[0].ParentToken == columns[1].ParentToken {
		t.Fatal("child identity lost its parent edge")
	}
	if tables[0].Token != tables[2].Token || tables[0].SchemaToken == tables[2].SchemaToken {
		t.Fatal("same basename lost schema edge")
	}
	req := agent.BuildRequest("run", agent.ContextInput{Skills: got.skills, Knowledge: got.knowledge, ObjectReferences: got.objects}, nil)
	if !strings.Contains(req.SystemPrompt, `id="audit_table"`) || !strings.Contains(req.SystemPrompt, "finding_key: skill.audit_table") || !strings.Contains(req.SystemPrompt, "finding_key: exp.audit_table") {
		t.Fatal("trusted metadata changed")
	}
	if strings.Contains(req.SystemPrompt, "unused_table") || strings.Contains(req.SystemPrompt, "{{") || strings.Contains(req.SystemPrompt, "policy_object_references") {
		t.Fatal("uncompiled values entered prompt")
	}
	if !strings.Contains(req.SystemPrompt, `name="review schema_1.table_1"`) {
		t.Fatal("knowledge names not compiled before rendering")
	}
	if len(m.Snapshot()) != 6 {
		t.Fatalf("undeclared object mapped: %d", len(m.Snapshot()))
	}
	restored := privacy.NewRunMapper()
	pairs, err := privacy.UnmarshalMapping(privacy.MarshalMapping(m.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Restore(pairs); err != nil {
		t.Fatal(err)
	}
	replay, err := compilePolicy([]agent.SkillInjection{sk}, []*knowledge.Entry{entry}, restored, binding, catalog, "private_prod", prior)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got.objects)
	b, _ := json.Marshal(replay.objects)
	if string(a) != string(b) {
		t.Fatal("replay graph drift")
	}
	for _, p := range restored.Snapshot() {
		if !isPhysicalSQLScope(p.Scope) {
			t.Fatal("second identity namespace allocated")
		}
	}
}
func TestPolicyCompilerFailsClosed(t *testing.T) {
	catalog, binding := policyCompilerFixture(t)
	for _, text := range []string{`{{ref:schema:["app"]}}`, `{{ref:table:["app","missing"]}}`, `{{ref:table:["app"]}}`, `{{ref:view:["app","audit_view"]}}`, `{{ref:constraint:["app","audit_table","audit_actor_fk"]}}`, `{{ref:column:["app","audit_view","actor"]}}`, `{{missing}}`, `{{ref:function:["app","danger"]}}`, `{{ref:table:["app","audit_table"]}`, `{{ref:table:["app",7]}}`, `{{ref:table:["app","Audit_Table"]}}`} {
		_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: text}}, nil, privacy.NewRunMapper(), binding, catalog, "", newMetadataScope())
		if !errors.Is(err, ErrPolicyPrivacy) {
			t.Fatalf("unproven reference allowed: %s", text)
		}
	}
	for _, tc := range []struct {
		name string
		c    database.PolicyCatalog
		b    *stageSchemaBinding
		m    *privacy.RunMapper
	}{
		{"no mapper", catalog, binding, nil}, {"no binding", catalog, nil, privacy.NewRunMapper()},
		{"empty catalog", database.PolicyCatalog{}, binding, privacy.NewRunMapper()},
		{"count mismatch", database.PolicyCatalog{Identifiers: catalog.Identifiers, SchemaCount: 1}, binding, privacy.NewRunMapper()},
		{"duplicate", database.PolicyCatalog{Identifiers: append(append([]database.PolicyCatalogIdentifier{}, catalog.Identifiers...), catalog.Identifiers[0]), SchemaCount: 2}, binding, privacy.NewRunMapper()},
		{"unauthorized", catalog, bindingOf("private"), privacy.NewRunMapper()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: "bounded query"}}, nil, tc.m, tc.b, tc.c, "", newMetadataScope())
			if !errors.Is(err, ErrPolicyPrivacy) {
				t.Fatalf("invalid catalog accepted: %v", err)
			}
		})
	}
}
func TestPolicyCompilerCollisionAndAbsentSQLName(t *testing.T) {
	catalog, binding := policyCompilerFixture(t)
	catalog.Identifiers = append(catalog.Identifiers, database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "TABLE_1"})
	m := privacy.NewRunMapper()
	m.Token(privacy.KindTable, "absent_sql_table")
	for _, text := range []string{"bounded query", "absent_sql_table"} {
		_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: text}}, nil, m, binding, catalog, "", newMetadataScope())
		if !errors.Is(err, ErrPolicyPrivacy) {
			t.Fatal("collision/absent SQL name passed")
		}
	}

	databaseMapper := privacy.NewRunMapper()
	databaseToken, tokenErr := databaseMapper.Token(privacy.KindTable, "target_database")
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: "bounded query"}}, nil,
		databaseMapper, binding, catalog, databaseToken, newMetadataScope())
	if !errors.Is(err, ErrPolicyPrivacy) {
		t.Fatal("target database name collided with mapper token")
	}
}

func TestPolicyCompilerRejectsInvalidTargetDatabaseName(t *testing.T) {
	catalog, binding := policyCompilerFixture(t)
	for _, databaseName := range []string{"private\x00prod", "private\nprod"} {
		_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: "bounded query"}}, nil,
			privacy.NewRunMapper(), binding, catalog, databaseName, newMetadataScope())
		if !errors.Is(err, ErrPolicyPrivacy) {
			t.Fatalf("invalid target database name accepted: %q", databaseName)
		}
	}
}

func TestPolicyCompilerGenericColumnAllowlistExemptsTextCollisionOnly(t *testing.T) {
	// RCP-20261031-POLICY-TEXT-GENERIC-COLUMN-ALLOWLIST: zero-specificity generic column names are
	// exempted from catalog-word collision checks on skill/knowledge text; all other fence surfaces
	// (table names / schema names / database names / statement identifiers / token collisions) are never exempted.
	catalog := policyCatalogTestCatalog(t,
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "customers"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "app", Relation: "customers", Name: "id"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "app", Relation: "customers", Name: "NAME"},
	)
	binding := bindingOf("app")

	// Skill text containing allowlisted words (mixed case) -> passes: matches the real shape of built-in skill ddl-pk-named-id.
	pkSkill := agent.SkillInjection{ToolID: "builtin.ddl-pk-named-id", Name: "bounded policy",
		KnowledgeText: "Rule: primary key column must be named id (legacy DDLPrimaryKeyMust). Criterion: emit this finding when ddl_pk_named_id=false; do not emit when primary key column name cannot be resolved.",
		FindingKey:    "builtin.ddl-pk-named-id"}
	if _, err := compilePolicy([]agent.SkillInjection{pkSkill}, nil, privacy.NewRunMapper(), binding, catalog, "prod_target", newMetadataScope()); err != nil {
		t.Fatalf("generic column name rejected: %v", err)
	}
	// Knowledge entry text is likewise exempted.
	entry := &knowledge.Entry{ID: "trusted", Name: "bounded policy", Purpose: "bounded query",
		Definition: knowledge.Definition{KnowledgeText: "Tables must include created_at and updated_at columns."}}
	if _, err := compilePolicy(nil, []*knowledge.Entry{entry}, privacy.NewRunMapper(), binding, catalog, "prod_target", newMetadataScope()); err != nil {
		t.Fatalf("generic column name rejected in knowledge text: %v", err)
	}

	// Table names are not exempted: customers is still blocked.
	for _, name := range []string{"customers", "CUSTOMERS"} {
		sk := pkSkill
		sk.KnowledgeText = "Inspect " + name + " columns."
		if _, err := compilePolicy([]agent.SkillInjection{sk}, nil, privacy.NewRunMapper(), binding, catalog, "prod_target", newMetadataScope()); !errors.Is(err, ErrPolicyPrivacy) {
			t.Fatalf("table name exempted: %s", name)
		}
	}
	// Not exempted when the word form is occupied by a non-column identifier: a table named "name" exists -> "name" in text is still blocked.
	clashCatalog := policyCatalogTestCatalog(t,
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "customers"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "name"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "app", Relation: "customers", Name: "name"},
	)
	clashSkill := agent.SkillInjection{ToolID: "trusted", Name: "bounded policy",
		KnowledgeText: "Require bounded name queries.", FindingKey: "skill.trusted"}
	if _, err := compilePolicy([]agent.SkillInjection{clashSkill}, nil, privacy.NewRunMapper(), binding, clashCatalog, "prod_target", newMetadataScope()); !errors.Is(err, ErrPolicyPrivacy) {
		t.Fatal("word occupied by non-column identifier exempted")
	}
	// Database names are not exempted: database name hits an allowlist word -> that word in text is still blocked.
	dbNameCatalog := policyCatalogTestCatalog(t,
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "customers"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "app", Relation: "customers", Name: "id"},
	)
	if _, err := compilePolicy([]agent.SkillInjection{clashSkill}, nil, privacy.NewRunMapper(), binding, dbNameCatalog, "status", newMetadataScope()); err != nil {
		t.Fatalf("database name must not create exemption side effects: %v", err)
	}
	dbNameSkill := clashSkill
	dbNameSkill.KnowledgeText = "Check status flags."
	if _, err := compilePolicy([]agent.SkillInjection{dbNameSkill}, nil, privacy.NewRunMapper(), binding, dbNameCatalog, "status", newMetadataScope()); !errors.Is(err, ErrPolicyPrivacy) {
		t.Fatal("database name exempted via allowlist word")
	}
	// Statement identifiers are exempted under the frozen allowlist (owner ruling 2026-11-01 extension:
	// id/name columns in submitted SQL have the same zero specificity as same-named catalog columns —
	// otherwise create table t (id ...) would inevitably collide with ddl-pk-named-id skill text; stable
	// skill names like builtin.ddl-index-name-prefix also contain generic words). Statement identifiers
	// outside the allowlist remain inside the text fence.
	stmtMapper := privacy.NewRunMapper()
	stmtMapper.Token(privacy.KindTable, "label")
	stmtMapper.Token(privacy.KindTable, "accounts")
	labelSkill := clashSkill
	labelSkill.KnowledgeText = "Check label flags."
	if _, err := compilePolicy([]agent.SkillInjection{labelSkill}, nil, stmtMapper, binding, dbNameCatalog, "prod_target", newMetadataScope()); err != nil {
		t.Fatalf("generic statement identifier must stay exempt: %v", err)
	}
	specificSkill := clashSkill
	specificSkill.KnowledgeText = "Check accounts flags."
	if _, err := compilePolicy([]agent.SkillInjection{specificSkill}, nil, stmtMapper, binding, dbNameCatalog, "prod_target", newMetadataScope()); !errors.Is(err, ErrPolicyPrivacy) {
		t.Fatal("non-generic statement identifier exempted")
	}
}

func TestCanonicalBuiltinSkillsCompileWithMySQLPrimaryAndSQLKeywordCatalog(t *testing.T) {
	migrationPath := filepath.Join("..", "..", "..", "..", "db", "migrations", "000009_builtin_skill_package.up.sql")
	rawSQL, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read 000009 migration: %v", err)
	}
	re := regexp.MustCompile(`values\s*\(\s*gen_random_uuid\(\),\s*'(builtin\.[a-z0-9-]+)',\s*'[a-z]+',\s*'([a-z]+)',\s*'(\{.*?\})'::jsonb,\s*'(\{.*?\})'::jsonb`)
	matches := re.FindAllStringSubmatch(string(rawSQL), -1)
	if len(matches) != 48 {
		t.Fatalf("expected 48 built-in skills in 000009 migration, got %d", len(matches))
	}

	skills := make([]agent.SkillInjection, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		engine := m[2]
		paramsRaw := strings.ReplaceAll(m[3], "''", "'")
		defRaw := strings.ReplaceAll(m[4], "''", "'")
		var params map[string]any
		if err := json.Unmarshal([]byte(paramsRaw), &params); err != nil {
			t.Fatalf("unmarshal parameters for %s: %v", name, err)
		}
		var def struct {
			KnowledgeText string `json:"knowledge_text"`
		}
		if err := json.Unmarshal([]byte(defRaw), &def); err != nil {
			t.Fatalf("unmarshal definition for %s: %v", name, err)
		}
		if !isCanonicalBuiltinSkill(name, def.KnowledgeText) {
			t.Fatalf("canonical built-in skill hash mismatch for %s", name)
		}
		skills = append(skills, agent.SkillInjection{
			ToolID:        name,
			Name:          name,
			Engine:        engine,
			KnowledgeText: def.KnowledgeText,
			Parameters:    params,
			FindingKey:    name,
		})
	}

	catalog := policyCatalogTestCatalog(t,
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "devops", Name: "ignoresql"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "devops", Relation: "ignoresql", Name: "id"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "devops", Relation: "ignoresql", Name: "md5"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "devops", Relation: "ignoresql", Name: "desc"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "devops", Relation: "ignoresql", Name: "create_time"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogIndex, Schema: "devops", Relation: "ignoresql", Name: "PRIMARY"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogConstraint, Schema: "devops", Relation: "ignoresql", Name: "PRIMARY"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "devops", Name: "alarm_event"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "devops", Relation: "alarm_event", Name: "id"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogIndex, Schema: "devops", Relation: "alarm_event", Name: "PRIMARY"},
		database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogConstraint, Schema: "devops", Relation: "alarm_event", Name: "PRIMARY"},
	)
	binding := bindingOf("devops")
	mapper := privacy.NewRunMapper()
	_, _ = mapper.Token(privacy.KindSchema, "devops")
	_, _ = mapper.Token(privacy.KindTable, "ignoresql")
	_, _ = mapper.Token(privacy.KindColumn, "md5")
	_, _ = mapper.Token(privacy.KindColumn, "desc")
	_, _ = mapper.Token(privacy.KindColumn, "create_time")

	compiled, err := compilePolicy(skills, nil, mapper, binding, catalog, "devops", newMetadataScope())
	if err != nil {
		t.Fatalf("expected all 48 canonical built-in skills to compile with MySQL PRIMARY catalog, got: %v", err)
	}
	if len(compiled.skills) != 48 {
		t.Fatalf("expected 48 compiled skills, got %d", len(compiled.skills))
	}

	// Custom parameter values on a canonical built-in skill that leak a catalog table name must still be rejected.
	leakingSkill := skills[0]
	for _, sk := range skills {
		if sk.Name == "builtin.ddl-charset-allowlist" {
			leakingSkill = sk
			break
		}
	}
	leakingSkill.Parameters = map[string]any{
		"allowed_charsets": []any{"utf8mb4", "ignoresql"},
	}
	if _, err := compilePolicy([]agent.SkillInjection{leakingSkill}, nil, mapper, binding, catalog, "devops", newMetadataScope()); !errors.Is(err, ErrPolicyPrivacy) {
		t.Fatalf("expected ErrPolicyPrivacy when built-in skill parameter leaks catalog table name, got %v", err)
	}
}
