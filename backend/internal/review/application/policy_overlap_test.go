package application

import (
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"testing"
)

// A policy reference must reuse the physical member already bound from SQL,
// including a logical default schema that executes against a physical schema.
func TestPolicySQLOverlapRequiresOneFact(t *testing.T) {
	m := privacy.NewRunMapper()
	st, _ := m.Token(privacy.KindSchema, "physical")
	tt, _ := m.Token(privacy.KindTable, "orders")
	prior := newMetadataScope()
	prior.schemas[st] = true
	prior.objects[st] = map[string]bool{tt: true}
	catalog := policyCatalogTestCatalog(t, database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "physical", Name: "orders"})
	compiled, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: `Inspect {{ref:table:["physical","orders"]}}.`}}, nil, m, bindingOf("physical"), catalog, "", prior)
	if err != nil {
		t.Fatal(err)
	}
	evidence := []provider.NormalizedEvidenceRef{{FactKind: "schema_metadata", Fact: map[string]any{"schema": st, "table": tt, "existence": "present", "metadata_complete": true, "columns": []any{}}}}
	if !compiled.scope.observed(evidence) {
		t.Fatal("one physical table requires duplicate SQL and policy evidence")
	}
}
