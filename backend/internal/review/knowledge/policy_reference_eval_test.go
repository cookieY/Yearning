package knowledge

import "testing"

func TestRunEvalRejectsUnresolvedPolicyReferences(t *testing.T) {
	for _, text := range []string{`Inspect {{ref:table:app.orders}}`, `Inspect {{missing}}`, `Inspect {{Ref:table:["app","orders"]}}`} {
		e := validEntry(ScopeGlobal)
		e.Definition.KnowledgeText = text
		if report := RunEval(e); report.Pass || report.PrivacyOK {
			t.Fatalf("unresolved references enabled: %+v", report)
		}
	}
	e := validEntry(ScopeGlobal)
	e.Definition.KnowledgeText = `Inspect {{ref:table:["app","orders"]}}`
	if report := RunEval(e); !report.Pass {
		t.Fatalf("well-formed typed reference rejected before target binding: %+v", report)
	}
}
