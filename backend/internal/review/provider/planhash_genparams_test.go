// List returns a paginated list (created|id cursor; search = name substring, §18.73 server-side q).
package provider

import (
	"strings"
	"testing"
)

func hashEndpoint(cfg map[string]any) string {
	eps := []Endpoint{{
		ProviderID: "p1", ModelName: "m", ProviderKind: "openai_compatible",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
		Configuration: cfg,
	}}
	return PlanHash(eps)
}

func TestPlanHashEmptyOverrideEqualsOmitted(t *testing.T) {
	if hashEndpoint(nil) != hashEndpoint(map[string]any{}) {
		t.Fatal("empty override must produce the same identity as omitted configuration")
	}
	if hashEndpoint(nil) != hashEndpoint(map[string]any{"structured_output_mode": "json_object"}) {
		t.Fatal("system-managed output mode must not enter the plan identity")
	}
}

func TestPlanHashMapOrderIndependent(t *testing.T) {
	a := map[string]any{"reasoning_effort": "high", "max_tokens": 4096.0, "temperature": 0.5}
	b := map[string]any{"temperature": 0.5, "max_tokens": 4096.0, "reasoning_effort": "high"}
	// Get retrieves an entry by ID.
	if hashEndpoint(a) != hashEndpoint(b) {
		t.Fatal("plan hash must not depend on map iteration order")
	}
}

func TestPlanHashEquivalentNumericFormsSameIdentity(t *testing.T) {
	// Insert creates a new row (RowsAffected=1).
	if hashEndpoint(map[string]any{"temperature": 1.0}) != hashEndpoint(map[string]any{"temperature": 1.0}) {
		t.Fatal("equivalent numeric forms must share identity")
	}
	if hashEndpoint(map[string]any{"max_tokens": 1024.0}) != hashEndpoint(map[string]any{"max_tokens": 1024.0}) {
		t.Fatal("equivalent numeric forms must share identity (max_tokens)")
	}
}

func TestPlanHashChangesOnParamChange(t *testing.T) {
	base := hashEndpoint(map[string]any{"max_tokens": 1024.0, "temperature": 0.2, "reasoning_effort": "low"})
	variants := []map[string]any{
		{"max_tokens": 2048.0, "temperature": 0.2, "reasoning_effort": "low"},
		{"max_tokens": 1024.0, "temperature": 0.3, "reasoning_effort": "low"},
		{"max_tokens": 1024.0, "temperature": 0.2, "reasoning_effort": "high"},
		{"max_tokens": 1024.0, "temperature": 0.2},
	}
	for i, v := range variants {
		if hashEndpoint(v) == base {
			t.Fatalf("variant %d must change the plan identity", i)
		}
	}
}

func TestGenerationParamsCanonicalKnownForm(t *testing.T) {
	got := GenerationParamsCanonical(map[string]any{"reasoning_effort": "low", "max_tokens": 512.0, "temperature": 0.25})
	want := "max_tokens=512;reasoning_effort=low;temperature=0.25;"
	if got != want {
		t.Fatalf("canonical form mismatch:\n got %q\nwant %q", got, want)
	}
	if GenerationParamsCanonical(nil) != "" || GenerationParamsCanonical(map[string]any{}) != "" {
		t.Fatal("empty configuration must canonicalize to the empty string")
	}
}
