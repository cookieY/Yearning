package promptprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func strPtr(s string) *string { return &s }

func TestResolveSystemPromptProfile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		model      string
		explicit   *string
		wantProf   string
		wantSource string
		wantErr    bool
	}{
		{name: "explicit san overrides gpt-4o", model: "gpt-4o", explicit: strPtr("san"), wantProf: ProfileSAN, wantSource: SourceExplicit},
		{name: "explicit default overrides san", model: "san", explicit: strPtr("default"), wantProf: ProfileDefault, wantSource: SourceExplicit},
		{name: "explicit empty invalid", model: "san", explicit: strPtr(""), wantErr: true},
		{name: "explicit invalid value", model: "san", explicit: strPtr("v4"), wantErr: true},

		{name: "auto exact san", model: "san", wantProf: ProfileSAN, wantSource: SourceAuto},
		{name: "auto upper san", model: "SAN-4B", wantProf: ProfileSAN, wantSource: SourceAuto},
		{name: "auto namespaced san", model: "ollama/san-4b-v4:latest", wantProf: ProfileSAN, wantSource: SourceAuto},
		{name: "auto sql-audit-nano", model: "registry.local/sql-audit-nano:latest", wantProf: ProfileSAN, wantSource: SourceAuto},
		{name: "auto sql-audit-nano-v4", model: "sql-audit-nano-v4", wantProf: ProfileSAN, wantSource: SourceAuto},

		{name: "auto xmodel-v4 defaults without explicit", model: "xmodel-v4", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional xmodel-v3.1 defaults", model: "xmodel-v3.1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional san-v3.1 defaults", model: "san-v3.1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional SAN-4B-v3.1 defaults", model: "SAN-4B-v3.1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional san:v3.1 defaults", model: "san:v3.1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional san:4b-v3.1 defaults", model: "san:4b-v3.1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional sql-audit-nano:v3.1 defaults", model: "sql-audit-nano:v3.1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto transitional ollama/xmodel-v3:latest defaults", model: "ollama/xmodel-v3:latest", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto legacy san_v1 defaults", model: "san_v1", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto legacy san.v2 defaults", model: "san.v2", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto generic deepseek", model: "deepseek-chat", wantProf: ProfileDefault, wantSource: SourceAuto},
		{name: "auto empty model defaults", model: "", wantProf: ProfileDefault, wantSource: SourceAuto},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prof, src, err := Resolve(tc.model, tc.explicit)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got profile=%q source=%q", prof, src)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prof != tc.wantProf || src != tc.wantSource {
				t.Fatalf("got (%q, %q), want (%q, %q)", prof, src, tc.wantProf, tc.wantSource)
			}
		})
	}
}

func TestTruncateOversizedSQLLiterals(t *testing.T) {
	t.Parallel()

	shortSQL := "INSERT INTO orders (status, note) VALUES ('PAID', 'it''s ok');"
	gotShort, err := TruncateOversizedSQLLiterals(shortSQL, MaxInlineLiteralChars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotShort != shortSQL {
		t.Fatalf("short literal changed: got %q want %q", gotShort, shortSQL)
	}

	longBody := strings.Repeat("x", 300)
	commentLong := strings.Repeat("c", 300)
	sqlWithLong := "UPDATE `my_'tbl` SET note = '" + longBody + "' -- '" + commentLong + "'\nWHERE state = 'ACTIVE' /* '" + commentLong + "' */;"
	gotLong, err := TruncateOversizedSQLLiterals(sqlWithLong, MaxInlineLiteralChars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPrefix := "'" + strings.Repeat("x", 64) + "...<truncated:300_chars>'"
	if !strings.Contains(gotLong, wantPrefix) {
		t.Fatalf("missing expected truncated marker in %q", gotLong)
	}
	if !strings.Contains(gotLong, "`my_'tbl`") {
		t.Fatalf("quoted identifier altered: %q", gotLong)
	}
	if !strings.Contains(gotLong, "-- '"+commentLong+"'") || !strings.Contains(gotLong, "/* '"+commentLong+"' */") {
		t.Fatalf("comment prose altered: %q", gotLong)
	}
	if !strings.Contains(gotLong, "state = 'ACTIVE'") {
		t.Fatalf("normal literal altered: %q", gotLong)
	}

	utf8Long := strings.Repeat("\u4e2d", 260)
	gotUTF8, err := TruncateOversizedSQLLiterals("SELECT '"+utf8Long+"';", MaxInlineLiteralChars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotUTF8, "...<truncated:260_chars>'") {
		t.Fatalf("rune count truncation mismatch: %q", gotUTF8)
	}

	if _, err := TruncateOversizedSQLLiterals(shortSQL, 0); err == nil {
		t.Fatalf("expected error for maxChars=0")
	}
}

func TestCanonicalSANGoldenSHA256Fixtures(t *testing.T) {
	t.Parallel()

	reviewNormal := BuildSANReviewSystemPrompt(ReviewSystemPromptInput{
		OutputLocale:  "zh-CN",
		EngineProfile: "mysql/mysql",
		ServerVersion: "8.0.35",
		ObjectReferences: []ObjectReference{
			{
				Token:                  "ledger_db.transfers",
				Kind:                   "table",
				Relation:               "write_target",
				SchemaToken:            "ledger_db",
				StatementOrdinal:       1,
				StatementOrdinalRanges: []StatementOrdinalRange{{Start: 1, End: 1}},
			},
		},
		Skills: []RuleInput{
			{
				FindingKey:    "skill.require-table-comment",
				KnowledgeText: "Every new table must define an explicit table COMMENT up to {{max_len}} chars.",
				Parameters:    map[string]any{"max_len": 128},
			},
		},
		Knowledge: []RuleInput{
			{
				FindingKey:    "exp.ledger-decimal-required",
				KnowledgeText: "Financial amount columns must use DECIMAL instead of FLOAT or DOUBLE.",
			},
		},
		ToolNames: BuiltinToolNames(),
	})

	reviewProbe := BuildSANReviewProbePrompt()

	assistantSQLGenerate, err := BuildSANAssistantSystemPrompt(AssistantSystemPromptInput{
		OutputLocale:   "zh-CN",
		ActorRole:      "authenticated_user",
		CapabilityHint: "sql_generate",
		HasMetadata:    true,
		Articles: []HelpArticleInput{
			{
				ID:            "help-change-orders",
				Title:         "Change Orders & Precheck",
				RoutePath:     "/orders/new",
				Summary:       "Create SQL change drafts, run AI review precheck, and submit for approval.",
				Steps:         []string{"Open /orders/new and select target change flow and datasource.", "Run AI Review precheck before clicking Submit."},
				DetailedGuide: "Precheck results remain valid for 30 minutes unless SQL or rule config changes.",
				PrimaryMatch:  false,
			},
		},
	})
	if err != nil {
		t.Fatalf("assistant_sql_generate failed: %v", err)
	}

	assistantPlatformHelp, err := BuildSANAssistantSystemPrompt(AssistantSystemPromptInput{
		OutputLocale:   "zh-CN",
		ActorRole:      "builtin_admin",
		CapabilityHint: "platform_help",
		HasMetadata:    false,
		Articles: []HelpArticleInput{
			{
				ID:            "help-diagnostics",
				Title:         "SQL Performance Diagnostics",
				RoutePath:     "/admin/diagnostics",
				Summary:       "Collect fingerprint statement stats and generate 4-section AI diagnosis reports.",
				Steps:         []string{"Open /admin/diagnostics as builtin_admin.", "Select a datasource and click AI Diagnose on a slow SQL template."},
				DetailedGuide: "Admin role grants direct access without Permission Groups or Flow grants.",
				PrimaryMatch:  true,
			},
		},
	})
	if err != nil {
		t.Fatalf("assistant_platform_help failed: %v", err)
	}

	diagnoseNormal := BuildSANDiagnoseSystemPrompt("zh-CN")

	golden := map[string]struct {
		text      string
		wantBytes int
		wantSHA   string
	}{
		"review_normal": {
			text:      reviewNormal,
			wantBytes: 754,
			wantSHA:   "26abb3bda7d2deba6d035d70009b63b4b0ee1fac7d1e7263e54e0d4fad43f8af",
		},
		"review_probe": {
			text:      reviewProbe,
			wantBytes: 180,
			wantSHA:   "0a672cd8512b506dec0d0b7750401998d7a82e0919d8ee0b480882986808006a",
		},
		"assistant_sql_generate": {
			text:      assistantSQLGenerate,
			wantBytes: 396,
			wantSHA:   "0539561d5d1cb93796a0c5d89beabb150740da5ca50901b6cf8cfa540af46fe9",
		},
		"assistant_platform_help": {
			text:      assistantPlatformHelp,
			wantBytes: 965,
			wantSHA:   "082456fff12b0b1c353a1e32c96c58a9f5e1ad8477a8325ea3725a8efc0e085f",
		},
		"diagnose_normal": {
			text:      diagnoseNormal,
			wantBytes: 225,
			wantSHA:   "73ea4d35fd6330932dc3ebc30de8b106aefc3fa846c658ae58b126a6ad56145f",
		},
	}

	forbiddenSubstrings := []string{"v3", "v4", "_v1", "4.0.0", "mode=", "tokens:"}

	for name, g := range golden {
		if len(g.text) != g.wantBytes {
			t.Fatalf("%s byte length=%d, want %d\nprompt:\n%s", name, len(g.text), g.wantBytes, g.text)
		}
		gotSHA := sha256Hex(g.text)
		if gotSHA != g.wantSHA {
			t.Fatalf("%s sha256=%s, want %s\nprompt:\n%s", name, gotSHA, g.wantSHA, g.text)
		}
		lower := strings.ToLower(g.text)
		for _, bad := range forbiddenSubstrings {
			if strings.Contains(lower, bad) {
				t.Fatalf("%s contains forbidden substring %q", name, bad)
			}
		}
	}
}

func TestPromptProfile_ConfigExtractionTruncationAndPromptBranches(t *testing.T) {
	t.Parallel()

	// 1. ExtractExplicitProfile, ResolveFromConfiguration, IsSAN
	if p, err := ExtractExplicitProfile(nil); err != nil || p != nil {
		t.Fatalf("expected nil, nil for nil cfg, got p=%v err=%v", p, err)
	}
	if p, err := ExtractExplicitProfile(map[string]any{"max_tokens": 512}); err != nil || p != nil {
		t.Fatalf("expected nil, nil when key absent, got p=%v err=%v", p, err)
	}
	if p, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": nil}); err != nil || p != nil {
		t.Fatalf("expected nil, nil when value is nil, got p=%v err=%v", p, err)
	}
	if p, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": "san"}); err != nil || p == nil || *p != ProfileSAN {
		t.Fatalf("expected explicit string san, got p=%v err=%v", p, err)
	}
	if _, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": "invalid"}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for invalid string, got %v", err)
	}
	var nilStrPtr *string
	if p, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": nilStrPtr}); err != nil || p != nil {
		t.Fatalf("expected nil, nil for (*string)(nil), got p=%v err=%v", p, err)
	}
	if p, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": strPtr("default")}); err != nil || p == nil || *p != ProfileDefault {
		t.Fatalf("expected *string default, got p=%v err=%v", p, err)
	}
	if _, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": strPtr("v4")}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for invalid *string, got %v", err)
	}
	if _, err := ExtractExplicitProfile(map[string]any{"system_prompt_profile": 123}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for non-string type, got %v", err)
	}

	if _, _, err := ResolveFromConfiguration("san", map[string]any{"system_prompt_profile": "bad"}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile from ResolveFromConfiguration, got %v", err)
	}
	if !IsSAN("san", nil) {
		t.Fatal("expected IsSAN=true for san model")
	}
	if IsSAN("xmodel-v3.1", nil) {
		t.Fatal("expected IsSAN=false for xmodel-v3.1")
	}
	if IsSAN("san", map[string]any{"system_prompt_profile": "bad"}) {
		t.Fatal("expected invalid explicit config to fail closed as IsSAN=false")
	}

	// 2. TruncateOversizedSQLLiterals edge cases + TruncateSQL + UTF8RuneLen
	if _, err := TruncateOversizedSQLLiterals("SELECT 1", 0); !errors.Is(err, ErrInvalidMaxLiteralChars) {
		t.Fatalf("expected ErrInvalidMaxLiteralChars, got %v", err)
	}
	if out, err := TruncateOversizedSQLLiterals("SELECT 1 FROM orders", MaxInlineLiteralChars); err != nil || out != "SELECT 1 FROM orders" {
		t.Fatalf("expected fast-path unchanged SQL, got %q err=%v", out, err)
	}
	// Quoted ident with doubled quote + backslash escape inside literal + unclosed comments & literals
	edgeSQL := `SELECT "a""b'c" FROM t WHERE note = 'esc\'quote''pair' -- eof 'comment'`
	if got, err := TruncateOversizedSQLLiterals(edgeSQL, MaxInlineLiteralChars); err != nil || got != edgeSQL {
		t.Fatalf("expected edge SQL unchanged: got %q want %q (err=%v)", got, edgeSQL, err)
	}
	unclosedCommentSQL := `SELECT 1 /* unclosed 'comment' `
	if got, err := TruncateOversizedSQLLiterals(unclosedCommentSQL, MaxInlineLiteralChars); err != nil || got != unclosedCommentSQL {
		t.Fatalf("expected unclosed block comment unchanged, got %q err=%v", got, err)
	}
	unclosedLitSQL := `SELECT 'unterminated literal `
	if got, err := TruncateOversizedSQLLiterals(unclosedLitSQL, MaxInlineLiteralChars); err != nil || got != unclosedLitSQL {
		t.Fatalf("expected unclosed string literal unchanged, got %q err=%v", got, err)
	}
	if got, err := TruncateOversizedSQLLiterals("SELECT '"+strings.Repeat("a", 20)+"'", 8); err != nil || !strings.Contains(got, "'aaaaaaaa...<truncated:20_chars>'") {
		t.Fatalf("expected preview capped to maxChars=8, got %q err=%v", got, err)
	}
	longSQL := "SELECT '" + strings.Repeat("b", 260) + "'"
	if got := TruncateSQL(longSQL); !strings.Contains(got, "...<truncated:260_chars>'") {
		t.Fatalf("expected TruncateSQL truncation, got %q", got)
	}
	if n := UTF8RuneLen("abc\u00e9"); n != 4 {
		t.Fatalf("expected UTF8RuneLen=4, got %d", n)
	}

	// 3. RenderParameters across all Go value types + case-insensitive lookup + missing key
	rendered := RenderParameters(
		"a={{flag}} b={{int_val}} c={{int64_val}} d={{float_int}} e={{float_frac}} f={{str_val}} g={{str_slice}} h={{any_slice}} i={{struct_val}} j={{missing}} k={{case_key}}",
		map[string]any{
			"flag":       true,
			"int_val":    7,
			"int64_val":  int64(42),
			"float_int":  float64(10),
			"float_frac": 3.25,
			"str_val":    "ok",
			"str_slice":  []string{"x", "y"},
			"any_slice":  []any{1, "two"},
			"struct_val": struct{ X int }{X: 1},
			"CASE_KEY":   "matched",
		},
	)
	for _, want := range []string{"a=true", "b=7", "c=42", "d=10", "e=3.25", "f=ok", "g=x,y", "h=1,two", "i=not configured", "j=not configured", "k=matched"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q in rendered string %q", want, rendered)
		}
	}

	// 4. BuildSANReviewSystemPrompt fallback/retry branches
	revPrompt := BuildSANReviewSystemPrompt(ReviewSystemPromptInput{
		OutputLocale:       "en-US",
		EngineProfile:      "",
		ServerVersion:      "",
		ObjectReferences:   []ObjectReference{{Token: "tbl", Kind: "table", Relation: "read"}},
		HasExplicitTools:   true,
		ToolNames:          nil,
		EvidenceIncomplete: true,
		LocaleRetry:        true,
		SchemaRetry:        true,
	})
	for _, want := range []string{
		"locale=en-US; target=unknown@unknown",
		`"statement_ordinal_ranges":[]`,
		"flag: evidence_incomplete",
		"flag: retry_locale",
		"flag: retry_schema",
	} {
		if !strings.Contains(revPrompt, want) {
			t.Fatalf("expected %q in review prompt:\n%s", want, revPrompt)
		}
	}

	// 5. BuildSANAssistantSystemPrompt error guards & non-PrimaryMatch omission
	if _, err := BuildSANAssistantSystemPrompt(AssistantSystemPromptInput{ActorRole: "guest", CapabilityHint: "platform_help"}); !errors.Is(err, ErrInvalidAssistantInput) {
		t.Fatalf("expected invalid actor_role error, got %v", err)
	}
	if _, err := BuildSANAssistantSystemPrompt(AssistantSystemPromptInput{ActorRole: "authenticated_user", CapabilityHint: "bad"}); !errors.Is(err, ErrInvalidAssistantInput) {
		t.Fatalf("expected invalid capability_hint error, got %v", err)
	}
	noHelp, err := BuildSANAssistantSystemPrompt(AssistantSystemPromptInput{
		OutputLocale:   "en-US",
		ActorRole:      "authenticated_user",
		CapabilityHint: "sql_optimize",
		HasMetadata:    false,
		Articles:       []HelpArticleInput{{ID: "a1", RoutePath: "/x", PrimaryMatch: false}},
	})
	if err != nil || strings.Contains(noHelp, "<help_context>") || !strings.Contains(noHelp, "locale=en-US") {
		t.Fatalf("unexpected non-help prompt: err=%v\n%s", err, noHelp)
	}
	helpMixed, err := BuildSANAssistantSystemPrompt(AssistantSystemPromptInput{
		OutputLocale:   "en-US",
		ActorRole:      "builtin_admin",
		CapabilityHint: "sql_generate",
		HasMetadata:    true,
		Articles: []HelpArticleInput{
			{ID: "primary", Title: "Primary", RoutePath: "/p", Summary: "S1", PrimaryMatch: true},
			{ID: "secondary", Title: "Secondary", RoutePath: "/s", Summary: "S2", PrimaryMatch: false},
		},
	})
	if err != nil || !strings.Contains(helpMixed, "[PRIMARY MATCH] Primary") || strings.Contains(helpMixed, "#### [secondary]") {
		t.Fatalf("unexpected mixed primary help rendering: err=%v\n%s", err, helpMixed)
	}

	// 6. BuildSANDiagnoseSystemPrompt English branch
	if diagEN := BuildSANDiagnoseSystemPrompt("en-US"); !strings.Contains(diagEN, "locale=en-US") {
		t.Fatalf("expected locale=en-US in diagnose prompt, got %q", diagEN)
	}
}
