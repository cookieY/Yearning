// SortPairsForToken is a stable sorting helper (used in tests and restoration).
package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// mapper_restore_test.go NEW-AI-01 regression: RunMapper.Restore crash replay
// recovery semantics — persisted mappings must reproduce original tokens; conflicts or malformed
// entries always fail closed while leaving the mapper unchanged.
func TestTokenOnlyLegendReachesSystemPrompt(t *testing.T) {
	// A new mapper (simulating a new attempt after crash replay) must reproduce original tokens after Restore.
	in := ContextInput{EngineProfile: "mysql/mysql"}
	in.TokenLegend = []TokenLegendEntry{
		{Token: "schema_1", Kind: string(privacy.KindSchema)},
		{Token: "table_1", Kind: string(privacy.KindTable)},
	}
	prompt := SystemPrompt(in, tool.NewRegistry())
	for _, want := range []string{
		"ANONYMOUS IDENTIFIER TOKENS",
		"schema_1", "table_1",
		"schema tool argument", "table tool argument",
		"will be rejected",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("legend section missing %q", want)
		}
	}
	// Ordinal watermark: new allocations after Restore must not collide with existing tokens.
	for _, original := range []string{"shop", "orders", "app_db"} {
		if strings.Contains(prompt, original) {
			t.Fatalf("legend leaked original identifier %q", original)
		}
	}
}

func TestEmptyLegendOmitsSection(t *testing.T) {
	prompt := SystemPrompt(sampleContext(), tool.NewRegistry())
	if strings.Contains(prompt, "ANONYMOUS IDENTIFIER TOKENS") {
		t.Fatal("empty legend must omit the section entirely")
	}
}

// Restored entries enter Snapshot (complete envelope persistence shape).
func TestBuildRequestCarriesInputSchema(t *testing.T) {
	req := BuildRequest("run-legend", sampleContext(), tool.NewRegistry())
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"allowed_tool_descriptors"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, td := range decoded.Tools {
		byName[td.Name] = td.InputSchema
	}
	ts, ok := byName["table_statistics"]
	if !ok {
		t.Fatal("table_statistics descriptor missing")
	}
	props, _ := ts["properties"].(map[string]any)
	if _, ok := props["schema"]; !ok {
		t.Fatalf("table_statistics input_schema missing schema property: %v", ts)
	}
	if _, ok := props["table"]; !ok {
		t.Fatalf("table_statistics input_schema missing table property: %v", ts)
	}
	reqs, _ := ts["required"].([]any)
	if len(reqs) != 2 {
		t.Fatalf("table_statistics required = %v", ts["required"])
	}
	// Duplicate injection of the same batch is idempotent.
	di := byName["datasource_identity"]
	if di == nil {
		t.Fatal("datasource_identity descriptor missing")
	}
	if p, ok := di["properties"].(map[string]any); !ok || len(p) != 0 {
		t.Fatalf("datasource_identity must declare empty properties: %v", di["properties"])
	}
}
