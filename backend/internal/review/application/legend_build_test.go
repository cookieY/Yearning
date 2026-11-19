// legend_build_test.go NEW-AI-03 regression: controlled anonymous legend construction — contains only
// tokens and semantic roles; pre-tokenizes the binding set and statement-touched tables; skips malformed names.
package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestBuildTokenLegendTokensOnlyAndDeterministic(t *testing.T) {
	mapper := privacy.NewRunMapper()
	binding := &stageSchemaBinding{defensible: true,
		schemas: map[string]bool{"shop": true, "physical_alpha": true}}
	legend := buildTokenLegend(mapper, binding, []string{"orders", "users", "orders"})
	if len(legend) != 4 { // 2 schema + 2 distinct tables
		t.Fatalf("legend entries = %d, want 4: %+v", len(legend), legend)
	}
	seen := map[string]bool{}
	for _, e := range legend {
		if seen[e.Token] {
			t.Fatalf("duplicate legend token %s", e.Token)
		}
		seen[e.Token] = true
		// Structurally the legend contains only tokens and categories: every token must detokenize in the
		// mapper to a real identifier with a category matching the entry.
		kind, original, ok := mapper.Original(e.Token)
		if !ok {
			t.Fatalf("legend token %s not resolvable", e.Token)
		}
		if string(kind) != e.Kind {
			t.Fatalf("legend kind mismatch: %s vs %s", e.Kind, kind)
		}
		_ = original // Real names exist only inside the mapper and never enter legend entries
	}
	// Deterministic sorting (map iteration is unordered -> legend order must be stable and reproducible).
	mapper2 := privacy.NewRunMapper()
	binding2 := &stageSchemaBinding{defensible: true,
		schemas: map[string]bool{"physical_alpha": true, "shop": true}}
	legend2 := buildTokenLegend(mapper2, binding2, []string{"users", "orders"})
	for i := range legend {
		if legend[i].Token != legend2[i].Token || legend[i].Kind != legend2[i].Kind {
			t.Fatalf("legend order not deterministic: %+v vs %+v", legend, legend2)
		}
	}
	// Deduplication: duplicate occurrences of the same table name produce only a single entry.
	// (Duplicate orders above is already covered by len == 4.)
}

// TestBuildTokenLegendTokensResolveAsToolArgs legend tokens must detokenize via detokenizeToolArgs
// to real arguments — closing the bootstrap loop: model calls with schema_1 -> real schema enters
// SEC-0041 binding validation and FactSource.
func TestBuildTokenLegendTokensResolveAsToolArgs(t *testing.T) {
	mapper := privacy.NewRunMapper()
	binding := bindingOf("shop")
	legend := buildTokenLegend(mapper, binding, []string{"orders"})
	if len(legend) != 2 {
		t.Fatalf("legend = %+v", legend)
	}
	var schemaTok, tableTok string
	for _, e := range legend {
		switch e.Kind {
		case "schema":
			schemaTok = e.Token
		case "table":
			tableTok = e.Token
		}
	}
	resolved := detokenizeToolArgs(map[string]any{"schema": schemaTok, "table": tableTok}, mapper)
	if resolved["schema"] != "shop" || resolved["table"] != "orders" {
		t.Fatalf("legend tokens must resolve to originals: %v", resolved)
	}
	// Detokenized real names must fall within the frozen binding set (SEC-0041 linkage).
	if !stageBindingAllowsTool(binding, resolved) {
		t.Fatal("legend-derived args must pass frozen binding check")
	}
	// Out-of-binding real names are still rejected (the legend never expands the authorization boundary).
	if stageBindingAllowsTool(binding, map[string]any{"schema": "otherdb"}) {
		t.Fatal("non-binding schema must stay rejected")
	}
}

func TestBuildTokenLegendDegenerateInputs(t *testing.T) {
	mapper := privacy.NewRunMapper()
	// Unavailable binding (fail-closed default binding) with no touched tables: legend is empty.
	if got := buildTokenLegend(mapper, &stageSchemaBinding{}, nil); got != nil {
		t.Fatalf("empty binding must yield nil legend: %+v", got)
	}
	// No bound objects: emit only touched-table tokens.
	got := buildTokenLegend(mapper, nil, []string{"orders"})
	if len(got) != 1 || got[0].Kind != "table" {
		t.Fatalf("nil binding must still legend touched tables: %+v", got)
	}
	// Malformed names (containing mapping delimiter characters) are skipped without entering the legend or
	// failing — real names remain governed by frozen-set validation.
	bad := &stageSchemaBinding{defensible: true,
		schemas: map[string]bool{"na\tme": true, "ok": true}}
	got = buildTokenLegend(mapper, bad, nil)
	if len(got) != 1 || got[0].Kind != "schema" {
		t.Fatalf("malformed name must be skipped: %+v", got)
	}
}

// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: legend tokenization permits the physical schema itself
// (which the model needs in order to invoke schema/table tools).
func TestBuildTokenLegendUsesAllowedPhysicalSchema(t *testing.T) {
	mapper := privacy.NewRunMapper()
	binding := bindingOf("prod")
	legend := buildTokenLegend(mapper, binding, nil)
	if len(legend) != 1 {
		t.Fatalf("legend = %+v", legend)
	}
	kind, original, ok := mapper.Original(legend[0].Token)
	if !ok || kind != privacy.KindSchema || original != "prod" {
		t.Fatalf("legend schema token = %s (%s/%s/%v), want allowed physical prod", legend[0].Token, kind, original, ok)
	}
	if out := mapper.Lookup(privacy.KindSchema, "app"); out != "" {
		t.Fatalf("out-of-scope schema entered model legend as %s", out)
	}
}
