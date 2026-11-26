package application

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

func TestBuildSANStageRequest_CompactPromptTruncationAndQualifiedObjects(t *testing.T) {
	mapper := privacy.NewRunMapper()
	schemaTok, err := mapper.Token(privacy.KindSchema, "ledger_db")
	if err != nil {
		t.Fatal(err)
	}
	tableTok, err := mapper.ScopedToken(privacy.KindTable, "ledger_db", "transfers")
	if err != nil {
		t.Fatal(err)
	}
	idxTok, err := mapper.ScopedToken(privacy.KindIndex, tableTok, "idx_transfers_ref")
	if err != nil {
		t.Fatal(err)
	}

	groupID := "11111111-1111-4111-8111-111111111111"
	runID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	longLiteral := strings.Repeat("Z", 300)
	rawSQL := "INSERT INTO ledger_db.transfers(tag, payload) VALUES ('PAID', '" + longLiteral + "');"

	ctx := withRepresentativeSQLResolver(context.Background(), func(gotRunID, gotGroupID string) (string, bool, error) {
		if gotRunID == runID && gotGroupID == groupID {
			return rawSQL, true, nil
		}
		return "", false, nil
	})

	groups := []provider.AnonymizedGroup{
		{
			GroupID:                     groupID,
			RepresentativeShape:         "kind=dml op=insert",
			RepresentativeAnonymizedSQL: "INSERT INTO " + schemaTok + "." + tableTok + " (tag, payload) VALUES (:string_1, :string_2);",
			StatementCount:              1,
			StatementOrdinal:            1,
			StatementOrdinalRanges:      []provider.StatementOrdinalRange{{Start: 1, End: 1}},
			Objects: []provider.AnonymizedObjectReference{
				{
					Token:                  tableTok,
					Kind:                   "table",
					Relation:               "write_target",
					SchemaToken:            schemaTok,
					StatementOrdinal:       1,
					StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 1}},
				},
			},
		},
	}

	policyObjects := []provider.AnonymizedObjectReference{
		{
			Token:            tableTok,
			Kind:             "table",
			Relation:         "write_target",
			SchemaToken:      schemaTok,
			StatementOrdinal: 1,
		},
		{
			Token:            idxTok,
			Kind:             "index",
			Relation:         "define",
			SchemaToken:      schemaTok,
			ParentToken:      tableTok,
			StatementOrdinal: 2,
		},
	}

	skills := []agent.SkillInjection{
		{
			FindingKey:    "skill.require-comment",
			KnowledgeText: `Inspect {{ref:table:["ledger_db","transfers"]}} comment up to {{max_len}}.`,
			Parameters:    map[string]any{"max_len": 128},
		},
	}
	entries := []*knowledge.Entry{
		{
			FindingKey: "exp.ledger-decimal",
			Definition: knowledge.Definition{
				KnowledgeText: `Verify {{ref:column:["ledger_db","transfers","amount"]}} precision.`,
			},
		},
	}

	var cached []provider.AnonymizedGroup
	req, err := buildSANStageRequest(ctx, stagePromptRequest{
		runID: runID,
		target: frozenDatasourceTarget{
			DatasourceID:      "ds-1",
			Engine:            "mysql",
			CompatibilityMode: "mysql",
			ServerVersion:     "8.0.36",
		},
		groups:        groups,
		policyObjects: policyObjects,
		rawSkills:     skills,
		rawKnowledge:  entries,
		sanEvidence: []provider.NormalizedEvidenceRef{
			{
				EvidenceID: "ev-1",
				FactKind:   "table_statistics",
				Fact: map[string]any{
					"schema": "ledger_db",
					"table":  "transfers",
				},
			},
		},
		runMapper:          mapper,
		outputLocale:       "zh-CN",
		evidenceIncomplete: true,
		schemaPending:      true,
		cachedSANGroups:    &cached,
	})
	if err != nil {
		t.Fatalf("buildSANStageRequest failed: %v", err)
	}

	if !strings.HasPrefix(req.SystemPrompt, "[SAN_HEAD=ai-review]\nlocale=zh-CN; target=mysql/mysql@8.0.36\n") {
		t.Fatalf("unexpected SAN header: %s", req.SystemPrompt)
	}
	for _, forbidden := range []string{"v3", "v4", "_v1", "4.0.0", "mode=", "tokens:"} {
		if strings.Contains(strings.ToLower(req.SystemPrompt), forbidden) {
			t.Fatalf("SAN prompt leaked forbidden token %q:\n%s", forbidden, req.SystemPrompt)
		}
	}
	if !strings.Contains(req.SystemPrompt, `"token":"ledger_db.transfers"`) ||
		!strings.Contains(req.SystemPrompt, `"parent_token":"ledger_db.transfers"`) {
		t.Fatalf("expected qualified raw objects in SAN prompt, got:\n%s", req.SystemPrompt)
	}
	// Duplicate write_target table from groups and policyObjects must be deduplicated cleanly.
	if strings.Count(req.SystemPrompt, `"relation":"write_target"`) != 1 {
		t.Fatalf("expected deduplicated write_target object in SAN prompt, got:\n%s", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "Inspect ledger_db.transfers comment up to 128.") {
		t.Fatalf("expected raw rendered skill reference & param in SAN prompt, got:\n%s", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "Verify ledger_db.transfers.amount precision.") {
		t.Fatalf("expected raw rendered knowledge reference in SAN prompt, got:\n%s", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "flag: evidence_incomplete (return outcome=partial)") ||
		!strings.Contains(req.SystemPrompt, "flag: retry_schema (fix JSON schema compliance)") {
		t.Fatalf("expected compact SAN flags, got:\n%s", req.SystemPrompt)
	}

	gotSQL := req.AnonymizedStatementGroups[0].RepresentativeAnonymizedSQL
	if strings.Contains(gotSQL, longLiteral) {
		t.Fatalf("oversized string literal was not truncated: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "...<truncated:300_chars>'") || !strings.Contains(gotSQL, "'PAID'") {
		t.Fatalf("unexpected truncated SQL: %s", gotSQL)
	}
	// Cached SAN groups reuse on second call without needing resolver again.
	ctxMissing := context.Background()
	req2, err := buildSANStageRequest(ctxMissing, stagePromptRequest{
		runID:           runID,
		target:          frozenDatasourceTarget{DatasourceID: "ds-1", Engine: "mysql", CompatibilityMode: "mysql", ServerVersion: "8.0.36"},
		groups:          groups,
		policyObjects:   policyObjects,
		runMapper:       mapper,
		outputLocale:    "en-US",
		cachedSANGroups: &cached,
	})
	if err != nil {
		t.Fatalf("cached buildSANStageRequest failed: %v", err)
	}
	if len(req2.AnonymizedStatementGroups) != 1 || req2.AnonymizedStatementGroups[0].RepresentativeAnonymizedSQL != gotSQL {
		t.Fatalf("cached SAN groups mismatch: %+v", req2.AnonymizedStatementGroups)
	}
	// Original default groups slice must remain untouched and anonymized.
	if !strings.Contains(groups[0].RepresentativeAnonymizedSQL, schemaTok) {
		t.Fatalf("original anonymized group was mutated: %s", groups[0].RepresentativeAnonymizedSQL)
	}
}

func TestTranslateSANToolArgsToTokensAndScopeGuards(t *testing.T) {
	mapper := privacy.NewRunMapper()
	schemaTok, _ := mapper.Token(privacy.KindSchema, "ledger_db")
	tableTok, _ := mapper.ScopedToken(privacy.KindTable, "ledger_db", "transfers")

	binding := &stageSchemaBinding{
		defensible: true,
		schemas:    map[string]bool{"ledger_db": true},
	}
	scope := metadataScope{
		schemas: map[string]bool{schemaTok: true},
		objects: map[string]map[string]bool{
			schemaTok: {tableTok: true},
		},
	}

	// Valid raw physical identifiers translate into canonical tokens that pass all default security guards.
	tokArgs, ok := translateSANToolArgsToTokens(map[string]any{
		"schema": "ledger_db",
		"table":  "transfers",
	}, mapper, binding)
	if !ok {
		t.Fatal("expected in-scope SAN tool args to translate")
	}
	if tokArgs["schema"] != schemaTok || tokArgs["table"] != tableTok {
		t.Fatalf("unexpected translated tokens: %+v", tokArgs)
	}
	modelArgs := detokenizeToolArgs(tokArgs, mapper)
	resolved, bindingOK := resolveStageBindingToolArgs(binding, modelArgs)
	if !knownToolIdentifiers(tokArgs, mapper) || !scope.allows(tokArgs) || !bindingOK {
		t.Fatalf("translated SAN tool args must pass SEC-0041 guards: known=%v scope=%v binding=%v",
			knownToolIdentifiers(tokArgs, mapper), scope.allows(tokArgs), bindingOK)
	}
	if resolved["schema"] != "ledger_db" || resolved["table"] != "transfers" {
		t.Fatalf("resolved physical args mismatch: %+v", resolved)
	}

	// Empty datasource_identity arguments translate cleanly.
	emptyArgs, ok := translateSANToolArgsToTokens(map[string]any{}, mapper, binding)
	if !ok || len(emptyArgs) != 0 {
		t.Fatalf("expected empty args translation to succeed, got ok=%v args=%+v", ok, emptyArgs)
	}

	// Out-of-scope, unmapped, invalid-identifier, or pre-tokenized args from a SAN model must fail closed.
	rejectedCases := []map[string]any{
		{"schema": "secret_db", "table": "transfers"},
		{"schema": "ledger_db", "table": "unknown_tbl"},
		{"schema": "ledger_db", "table": "bad;table"},
		{"schema": schemaTok, "table": tableTok},
		{"schema": 123},
	}
	for _, bad := range rejectedCases {
		if _, ok := translateSANToolArgsToTokens(bad, mapper, binding); ok {
			t.Fatalf("expected rejection for out-of-scope/invalid SAN args %+v", bad)
		}
	}
}

func TestCopyNormalizedFactForSAN_StripsFreeTextAndPreservesRawIdentifiers(t *testing.T) {
	in := map[string]any{
		"schema":   "ledger_db",
		"tables":   []any{"transfers", "entries"},
		"warnings": []any{"internal secret engine note"},
		"complete": true,
	}
	got := copyNormalizedFactForSAN(in, tool.FactSchemaVersion)
	if _, hasWarnings := got["warnings"]; hasWarnings {
		t.Fatalf("expected warnings free-text field to be stripped: %+v", got)
	}
	if got["schema"] != "ledger_db" {
		t.Fatalf("expected raw schema to stay preserved: %+v", got)
	}
	if got["fact_schema_version"] != tool.FactSchemaVersion {
		t.Fatalf("expected fact_schema_version=%d, got %+v", tool.FactSchemaVersion, got["fact_schema_version"])
	}
	// Mutating original must not mutate SAN copy.
	in["schema"] = "schema_1"
	if got["schema"] != "ledger_db" {
		t.Fatalf("SAN copy aliased input map: %+v", got)
	}
}

func TestMixedProfileChainFallback_ZeroRawLeakToDefaultEndpoint(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	mapper := privacy.NewRunMapper()
	schemaTok, _ := mapper.Token(privacy.KindSchema, "secret_fin")
	tableTok, _ := mapper.ScopedToken(privacy.KindTable, "secret_fin", "payout_orders")
	runID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	groupID := "22222222-2222-4222-8222-222222222222"
	rawSQL := "SELECT id, amount FROM secret_fin.payout_orders WHERE tag = 'VIP';"
	anonSQL := "SELECT column_1, column_2 FROM " + schemaTok + "." + tableTok + " WHERE column_3 = :string_1;"

	var mu sync.Mutex
	captured := map[string]string{}

	sanSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		captured["san"] = string(body)
		mu.Unlock()
		// Return 401 (ErrRejected) to trigger chain fallback to the secondary default endpoint.
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"san auth expired"}}`))
	}))
	defer sanSrv.Close()

	defSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		captured["default"] = string(body)
		mu.Unlock()
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": `{"schema_version":1,"review_run_id":"` + runID + `","outcome":"complete","summary":"Review completed safely.","findings":[],"tool_requests":[]}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer defSrv.Close()

	hc := provider.NewHTTPClient()
	sanProfile := "san"
	epSAN := provider.Endpoint{
		ProviderID:          "prov-san",
		Name:                "local-san",
		ProviderKind:        "openai_compatible",
		BaseURL:             sanSrv.URL + "/v1",
		ModelName:           "san-4b",
		APIKey:              "sk-san",
		PrivacyContractHash: provider.PrivacyContractHashForProfile(sanProfile),
		OutputSchemaHash:    provider.OutputSchemaHash,
		Configuration:       map[string]any{"system_prompt_profile": "san"},
	}
	epDef := provider.Endpoint{
		ProviderID:          "prov-def",
		Name:                "cloud-default",
		ProviderKind:        "openai_compatible",
		BaseURL:             defSrv.URL + "/v1",
		ModelName:           "xmodel-v3.1",
		APIKey:              "sk-def",
		PrivacyContractHash: provider.PrivacyContractHashDefault,
		OutputSchemaHash:    provider.OutputSchemaHash,
	}

	chain, err := provider.NewChain([]provider.Endpoint{epSAN, epDef}, func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewOpenAIAdapter(ep, hc)
	})
	if err != nil {
		t.Fatalf("NewChain failed: %v", err)
	}

	groups := []provider.AnonymizedGroup{
		{
			GroupID:                     groupID,
			RepresentativeShape:         "kind=dql op=select",
			RepresentativeAnonymizedSQL: anonSQL,
			StatementCount:              1,
			StatementOrdinal:            1,
			StatementOrdinalRanges:      []provider.StatementOrdinalRange{{Start: 1, End: 1}},
			Objects: []provider.AnonymizedObjectReference{
				{
					Token:                  tableTok,
					Kind:                   "table",
					Relation:               "read",
					SchemaToken:            schemaTok,
					StatementOrdinal:       1,
					StatementOrdinalRanges: []provider.StatementOrdinalRange{{Start: 1, End: 1}},
				},
			},
		},
	}

	ctx := withRepresentativeSQLResolver(context.Background(), func(gotRunID, gotGroupID string) (string, bool, error) {
		if gotRunID == runID && gotGroupID == groupID {
			return rawSQL, true, nil
		}
		return "", false, nil
	})

	var cached []provider.AnonymizedGroup
	res, err := chain.ReviewStructuredPerEndpoint(ctx, func(reqCtx context.Context, ep provider.Endpoint) (provider.Request, error) {
		if endpointIsSAN(ep) {
			return buildSANStageRequest(reqCtx, stagePromptRequest{
				runID:           runID,
				target:          frozenDatasourceTarget{DatasourceID: "ds-1", Engine: "mysql", CompatibilityMode: "mysql", ServerVersion: "8.0.36"},
				groups:          groups,
				runMapper:       mapper,
				outputLocale:    "en-US",
				cachedSANGroups: &cached,
			})
		}
		return agent.BuildRequest(runID, agent.ContextInput{
			EngineProfile:    "mysql/mysql",
			OutputLocale:     "en-US",
			RuleSnapshot:     []byte("rule-hash"),
			ObjectReferences: groups[0].Objects,
			Groups:           groups,
			Evidence:         []provider.NormalizedEvidenceRef{},
		}, nil), nil
	}, nil)
	if err != nil {
		t.Fatalf("ReviewStructuredPerEndpoint failed: %v", err)
	}
	if res.FallbackCount != 1 || res.ProviderID != "prov-def" {
		t.Fatalf("expected fallback to default endpoint (index 1), got fallback=%d provider=%s", res.FallbackCount, res.ProviderID)
	}

	mu.Lock()
	sanWire := captured["san"]
	defWire := captured["default"]
	mu.Unlock()

	if !strings.Contains(sanWire, "[SAN_HEAD=ai-review]") || !strings.Contains(sanWire, "secret_fin.payout_orders") {
		t.Fatalf("SAN endpoint did not receive compact SAN wire request with raw SQL:\n%s", sanWire)
	}
	for _, leaked := range []string{"secret_fin", "payout_orders", "'VIP'", "[SAN_HEAD=ai-review]"} {
		if strings.Contains(defWire, leaked) {
			t.Fatalf("default fallback endpoint leaked SAN content %q:\n%s", leaked, defWire)
		}
	}
	if !strings.Contains(defWire, schemaTok) || !strings.Contains(defWire, tableTok) {
		t.Fatalf("default fallback endpoint did not receive anonymized tokens:\n%s", defWire)
	}

	// Static ReviewStructuredGuarded (legacy single-request path) must still block cross-profile fallback.
	_, serr := chain.ReviewStructuredGuarded(ctx, provider.Request{ReviewRunID: runID}, nil)
	if serr == nil || !strings.Contains(serr.Error(), "privacy/output contract mismatch") {
		t.Fatalf("expected static ReviewStructuredGuarded to reject mixed-profile fallback, got %v", serr)
	}
}

func TestSANReviewHelpers_UnitEdgeCases(t *testing.T) {
	ctx := context.Background()

	rendered, err := renderSANPolicyText(
		`Check {{ref:schema:["shop"]}} {{ref:table:["shop","orders"]}} {{ref:view:["shop","v_orders"]}} {{ref:column:["shop","orders","id"]}} {{ref:index:["shop","orders","idx_id"]}} {{ref:constraint:["shop","orders","pk_id"]}}.`,
	)
	if err != nil {
		t.Fatalf("renderSANPolicyText valid refs failed: %v", err)
	}
	for _, want := range []string{"shop", "shop.orders", "shop.v_orders", "shop.orders.id", "shop.orders.idx_id", "shop.orders.pk_id"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected %q in rendered SAN policy text %q", want, rendered)
		}
	}
	if _, err := renderSANPolicyText(`Bad {{ref:unknown:["shop"]}}`); err == nil {
		t.Fatal("expected unsupported policy reference kind to fail")
	}

	eps := []provider.Endpoint{
		{ModelName: "san-4b"},
		{ModelName: "xmodel-v3.1"},
	}
	chain := &provider.Chain{Endpoints: eps}
	if resultEndpointIsSAN(nil, nil) {
		t.Fatal("expected nil chain/result to return false")
	}
	if resultEndpointIsSAN(chain, &provider.Result{FallbackCount: -1}) || resultEndpointIsSAN(chain, &provider.Result{FallbackCount: 2}) {
		t.Fatal("expected out-of-range FallbackCount to return false")
	}
	if !resultEndpointIsSAN(chain, &provider.Result{FallbackCount: 0}) {
		t.Fatal("expected index 0 san-4b to be recognized as SAN")
	}
	if resultEndpointIsSAN(chain, &provider.Result{FallbackCount: 1}) {
		t.Fatal("expected index 1 xmodel-v3.1 not to be recognized as SAN")
	}

	if _, ok := translateSANToolArgsToTokens(map[string]any{}, nil, nil); ok {
		t.Fatal("expected nil mapper to fail translation")
	}
	mapper := privacy.NewRunMapper()
	shopTok, _ := mapper.Token(privacy.KindSchema, "shop")
	ordersTok, _ := mapper.ScopedToken(privacy.KindTable, "shop", "orders")
	unscopedTok, _ := mapper.Token(privacy.KindTable, "audit_log")
	binding := &stageSchemaBinding{defensible: true, schemas: map[string]bool{"shop": true}}

	if _, ok := translateSANToolArgsToTokens(map[string]any{"schema": 123}, mapper, binding); ok {
		t.Fatal("expected non-string schema argument to fail")
	}
	if _, ok := translateSANToolArgsToTokens(map[string]any{"schema": "shop"}, mapper, nil); ok {
		t.Fatal("expected nil binding with schema argument to fail")
	}
	if _, ok := translateSANToolArgsToTokens(map[string]any{"schema": "other_db"}, mapper, binding); ok {
		t.Fatal("expected out-of-scope schema to fail")
	}
	emptyTokMapper := privacy.NewRunMapper()
	if _, ok := translateSANToolArgsToTokens(map[string]any{"schema": "shop"}, emptyTokMapper, binding); ok {
		t.Fatal("expected unmapped schema token to fail")
	}
	if _, ok := translateSANToolArgsToTokens(map[string]any{"table": 456}, mapper, binding); ok {
		t.Fatal("expected non-string table argument to fail")
	}
	if _, ok := translateSANToolArgsToTokens(map[string]any{"table": "bad;table"}, mapper, binding); ok {
		t.Fatal("expected invalid table identifier to fail")
	}
	if _, ok := translateSANToolArgsToTokens(map[string]any{"table": "missing_tbl"}, mapper, binding); ok {
		t.Fatal("expected unmapped table token to fail")
	}

	// Table-only call uses binding.defaultSchema() to look up scoped token.
	translatedDefault, ok := translateSANToolArgsToTokens(map[string]any{"table": "orders", "limit": 10}, mapper, binding)
	if !ok || translatedDefault["table"] != ordersTok || translatedDefault["limit"] != 10 {
		t.Fatalf("expected defaultSchema scoped lookup for table, got ok=%v map=%+v", ok, translatedDefault)
	}
	// Full schema + table translation and unscoped table fallback.
	translatedFull, ok := translateSANToolArgsToTokens(map[string]any{"schema": "shop", "table": "orders"}, mapper, binding)
	if !ok || translatedFull["schema"] != shopTok || translatedFull["table"] != ordersTok {
		t.Fatalf("unexpected schema+table translation: ok=%v map=%+v", ok, translatedFull)
	}
	translatedUnscoped, ok := translateSANToolArgsToTokens(map[string]any{"table": "audit_log"}, mapper, nil)
	if !ok || translatedUnscoped["table"] != unscopedTok {
		t.Fatalf("expected unscoped table fallback token %q, got ok=%v map=%+v", unscopedTok, ok, translatedUnscoped)
	}

	if gotNil := copyNormalizedFactForSAN(nil, tool.FactSchemaVersion); len(gotNil) != 0 {
		t.Fatalf("expected nil fact copy to be empty map, got %+v", gotNil)
	}
	if gotNaN := copyNormalizedFactForSAN(map[string]any{"bad": math.NaN()}, tool.FactSchemaVersion); len(gotNaN) != 0 {
		t.Fatalf("expected unmarshalable fact copy to be empty map, got %+v", gotNaN)
	}
	gotFiltered := copyNormalizedFactForSAN(map[string]any{"schema": "shop", "warnings": []string{"raw free text"}}, tool.FactSchemaVersion)
	if _, hasWarn := gotFiltered["warnings"]; hasWarn || gotFiltered["schema"] != "shop" {
		t.Fatalf("expected warnings free-text field stripped, got %+v", gotFiltered)
	}

	// prepareSANGroups resolver error and buildSANStageRequest invalid policy reference error.
	groups := []provider.AnonymizedGroup{{GroupID: "g-1"}}
	if _, err := prepareSANGroups(ctx, nil, nil, "run-1", "mysql/mysql", groups, mapper, nil); !errors.Is(err, domain.ErrCoverageIncomplete) {
		t.Fatalf("expected prepareSANGroups to wrap ErrCoverageIncomplete, got %v", err)
	}
	ctxOK := withRepresentativeSQLResolver(ctx, func(string, string) (string, bool, error) {
		return "SELECT id FROM shop.orders;", true, nil
	})
	if _, err := buildSANStageRequest(ctxOK, stagePromptRequest{
		runID:        "run-1",
		target:       frozenDatasourceTarget{Engine: "mysql", CompatibilityMode: "mysql", ServerVersion: "8.0.36"},
		groups:       groups,
		rawSkills:    []agent.SkillInjection{{FindingKey: "sk.bad", KnowledgeText: `Bad {{ref:schema:["shop"]`}},
		runMapper:    mapper,
		outputLocale: "en-US",
	}); !errors.Is(err, domain.ErrCoverageIncomplete) {
		t.Fatalf("expected invalid skill reference in SAN stage build to return ErrCoverageIncomplete, got %v", err)
	}
	if _, err := buildSANStageRequest(ctxOK, stagePromptRequest{
		runID:        "run-1",
		target:       frozenDatasourceTarget{Engine: "mysql", CompatibilityMode: "mysql", ServerVersion: "8.0.36"},
		groups:       groups,
		rawKnowledge: []*knowledge.Entry{nil, {FindingKey: "kb.bad", Definition: knowledge.Definition{KnowledgeText: `Bad {{ref:table:["shop"]}}`}}},
		runMapper:    mapper,
		outputLocale: "en-US",
	}); !errors.Is(err, domain.ErrCoverageIncomplete) {
		t.Fatalf("expected invalid knowledge reference in SAN stage build to return ErrCoverageIncomplete, got %v", err)
	}

	if _, err := resolveRepresentativeStatementSQL(ctx, nil, nil, "", "", ""); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("expected ErrFactSourceUnavailable for nil DB/crypter, got %v", err)
	}
	if prof := runProfileForDB(ctx, nil, ""); prof != "mysql/mysql" {
		t.Fatalf("expected mysql/mysql default profile for nil DB, got %q", prof)
	}
}

type sanTestFactSource struct{}

func (sanTestFactSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql"}, nil
}
func (sanTestFactSource) SchemaMetadata(_ context.Context, schema string) (map[string]any, error) {
	return map[string]any{"schema": schema, "table_count": 1, "tables": []any{"orders"}}, nil
}
func (sanTestFactSource) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	return map[string]any{
		"schema": schema, "table": table, "existence": "present",
		"column_count": 2, "returned_column_count": 2,
		"columns": []any{
			map[string]any{"column_name": "id", "ordinal": 1, "type_class": "integer"},
			map[string]any{"column_name": "amount", "ordinal": 2, "type_class": "integer"},
		},
		"metadata_complete": true, "truncated": false, "estimate": false,
	}, nil
}
func (sanTestFactSource) TableStatistics(_ context.Context, schema, table string) (map[string]any, error) {
	return map[string]any{"schema": schema, "table": table, "existence": "present", "column_count": 2, "estimate": true, "metadata_complete": true, "truncated": false}, nil
}
func (sanTestFactSource) IndexMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	return map[string]any{"schema": schema, "table": table, "existence": "present", "indexes": []map[string]any{{"index_name": "PRIMARY"}}, "metadata_complete": true, "truncated": false}, nil
}
func (sanTestFactSource) SafeExplain(_ context.Context, schema, _ string) (map[string]any, error) {
	return map[string]any{"schema": schema, "metadata_complete": true, "truncated": false}, nil
}
