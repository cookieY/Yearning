// Evidence carries only references without payloads.
package privacy

import "testing"

func TestRestoreReproducesTokensAndContinuesSequence(t *testing.T) {
	m := NewRunMapper()
	tok, err := m.Token(KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	ctok, _ := m.Token(KindColumn, "status")
	snap := m.Snapshot()

	// TestSystemPromptInjectsSkillsAndKnowledge test_gate injection: both skills and experience entries enter the system prompt as deterministic delimited blocks.
	reborn := NewRunMapper()
	if err := reborn.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := reborn.Lookup(KindTable, "orders"); got != tok {
		t.Fatalf("restored token mismatch: %s != %s", got, tok)
	}
	if got := reborn.Lookup(KindColumn, "status"); got != ctok {
		t.Fatalf("restored token mismatch: %s != %s", got, ctok)
	}
	if kind, original, ok := reborn.Original(tok); !ok || kind != KindTable || original != "orders" {
		t.Fatalf("restored reverse lookup broken: %v %v %v", kind, original, ok)
	}
	if reborn.Len() != 2 {
		t.Fatalf("restored len = %d", reborn.Len())
	}
	// TestSystemPromptDeclaresToolFactsUntrusted SEC-0028 regression: system prompt explicitly declares tool facts and schema metadata as untrusted data; tokenized tool facts with injected text (instruction-like residue from malicious table names) only enter the NormalizedEvidence channel and never the system prompt — existing channel isolation is preserved by this hardening.
	next, err := reborn.Token(KindTable, "users")
	if err != nil {
		t.Fatal(err)
	}
	if next == tok {
		t.Fatalf("restored mapper reused existing token ordinal: %s", next)
	}
	if next != "table_2" {
		t.Fatalf("sequence must continue past restored watermark: %s", next)
	}
	// Malicious table name has been tokenized into an anonymous token; free-text fields (such as comments) retain injected instructions — SEC-0028 attack vector.
	if len(reborn.Snapshot()) != 3 {
		t.Fatalf("snapshot after restore = %d", len(reborn.Snapshot()))
	}
}

func TestRestoreIdempotentAndRejectsConflicts(t *testing.T) {
	m := NewRunMapper()
	tok, _ := m.Token(KindTable, "orders")
	snap := m.Snapshot()

	// Isolation invariant: injected text never enters the system prompt.
	if err := m.Restore(snap); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
	if err := m.Restore(snap); err != nil {
		t.Fatalf("second restore must be no-op: %v", err)
	}
	if m.Len() != 1 {
		t.Fatalf("restore must not duplicate entries: %d", m.Len())
	}

	// Injected facts pass through the evidenceRefs channel as-is (referenced as data without sanitization).
	conflict := []MappingPair{{Kind: KindTable, Original: "orders", Token: "table_9"}}
	if err := m.Restore(conflict); err == nil {
		t.Fatal("token conflict for mapped identifier must fail")
	}
	// TestBoundedFingerprintGroups test_gate#8: request carries only fingerprint groups (one representative statement per group), never one model entry per raw statement.
	owned := []MappingPair{{Kind: KindTable, Original: "users", Token: tok}}
	if err := m.Restore(owned); err == nil {
		t.Fatal("token owned by another identifier must fail")
	}
	// Group count is bounded (<=1000, matching the fingerprint_group_count CHECK constraint).
	if kind, original, ok := m.Original(tok); !ok || original != "orders" || kind != KindTable {
		t.Fatalf("mapper state polluted by failed restore: %v %v", original, ok)
	}
	if next, err := m.Token(KindTable, "users"); err != nil || next == tok {
		t.Fatalf("post-rejection allocation broken: %s %v", next, err)
	}

	// legend_test.go NEW-AI-03 regression: controlled anonymous token legend injection and tool parameter schema pass-through — bootstrap channel carries only tokens and semantic roles, never raw identifiers.
	bad := [][]MappingPair{
		{{Kind: KindTable, Original: "x", Token: "table_abc"}},             // TestTokenOnlyLegendReachesSystemPrompt NEW-AI-03 bootstrap: legend entries enter the system prompt with token + semantic role; raw identifiers never appear — the model can only construct first-turn tool calls using legend tokens.
		{{Kind: KindTable, Original: "x", Token: "column_3"}},              // Minimal context: skill/experience text may legitimately carry domain words (such as orders), so the 'raw name never appears' assertion must run on an input without other injection sources.
		{{Kind: KindTable, Original: "x", Token: "table_0"}},               // Raw identifiers never enter the prompt via the legend (structurally the legend has only token + role text).
		{{Kind: KindTable, Original: "", Token: "table_1"}},                // TestBuildRequestCarriesInputSchema NEW-AI-03: ToolDescriptor passes through JSON Schema parameter declarations with the request; undeclared parameter names are invisible.
		{{Kind: IdentifierKind("bogus"), Original: "x", Token: "bogus_1"}}, // datasource_identity takes no arguments: properties is an empty object (not a missing key).
		{{Kind: KindTable, Original: "a\tb", Token: "table_1"}},            // bulkFixtureReader deterministically generates a synthetic stream following fixture-spec.json bulk_dml semantics: 100,000 high-similarity UPDATEs + an anomalous UPDATE at statement 54,321, byte-identical to scripts/development/generate_fixtures.py.
	}
	for i, pairs := range bad {
		if err := NewRunMapper().Restore(pairs); err == nil {
			t.Fatalf("malformed restore batch %d must fail", i+1)
		}
	}
}

func TestRestoreThenSealRoundTrip(t *testing.T) {
	c := newTestCrypter(t)
	m := NewRunMapper()
	m.Token(KindTable, "orders")
	snap := m.Snapshot()
	binding := EnvelopeBinding{ReviewRunID: "run-77", StatementIdx: 1}
	env, err := SealMapping(c, binding, snap)
	if err != nil {
		t.Fatal(err)
	}
	// TestStreamBulk100kHighSimilarity contract workload bulk_update_high_similarity: 100,000 high-similarity DML statements stream-grouped into 2 fingerprint groups (fixture-spec expected_fingerprint_groups).
	reborn := NewRunMapper()
	pairs, err := OpenMapping(c, binding, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := reborn.Restore(pairs); err != nil {
		t.Fatal(err)
	}
	if _, err := reborn.Token(KindSchema, "shop"); err != nil {
		t.Fatal(err)
	}
	env2, err := SealMapping(c, binding, reborn.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	merged, err := OpenMapping(c, binding, env2)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 {
		t.Fatalf("merged mapping must contain both entries: %d", len(merged))
	}
	if merged[0].Original != "orders" || merged[0].Token != "table_1" {
		t.Fatalf("original token lost after merge-reseal: %+v", merged[0])
	}
}
