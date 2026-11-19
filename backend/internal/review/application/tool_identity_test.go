package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestToolRequestSignatureIncludesNameAndCanonicalArguments(t *testing.T) {
	first, err := toolRequestSignature(provider.ToolRequest{ToolName: "schema_metadata", Arguments: map[string]any{"schema": "schema_1", "table": "table_1"}})
	if err != nil {
		t.Fatal(err)
	}
	same, err := toolRequestSignature(provider.ToolRequest{ToolName: "schema_metadata", Arguments: map[string]any{"table": "table_1", "schema": "schema_1"}})
	if err != nil || first != same {
		t.Fatal("argument key order changed request identity")
	}
	for _, changed := range []provider.ToolRequest{
		{ToolName: "index_metadata", Arguments: map[string]any{"schema": "schema_1", "table": "table_1"}},
		{ToolName: "schema_metadata", Arguments: map[string]any{"schema": "schema_2", "table": "table_1"}},
	} {
		signature, err := toolRequestSignature(changed)
		if err != nil || signature == first {
			t.Fatal("different tool request reused identity")
		}
	}
	if _, err := toolRequestSignature(provider.ToolRequest{Arguments: map[string]any{"bad": make(chan int)}}); err == nil {
		t.Fatal("unsupported request signature was accepted")
	}
}

func TestStoredToolCallIdentityHasRunStageAndRequestScope(t *testing.T) {
	base := storedToolCallID("run", "stage", "provider-id", "signature")
	if storedToolCallID("run", "stage", "provider-id", "signature") != base {
		t.Fatal("identity is not replay stable")
	}
	for _, changed := range [][4]string{
		{"other-run", "stage", "provider-id", "signature"},
		{"run", "other-stage", "provider-id", "signature"},
		{"run", "stage", "other-id", "signature"},
		{"run", "stage", "provider-id", "other-signature"},
	} {
		if storedToolCallID(changed[0], changed[1], changed[2], changed[3]) == base {
			t.Fatal("tool audit identity collided")
		}
	}
}
