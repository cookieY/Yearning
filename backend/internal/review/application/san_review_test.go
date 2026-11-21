package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
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

func acquireSANPGTestLock(t *testing.T) {
	t.Helper()
	path := os.TempDir() + "/yearningx-itest-shared.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

func TestSANReviewAndProviderStore_PGFlow(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("YEARNING_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("YEARNING_TEST_POSTGRES_DSN unset; skipping PG-backed SAN review and provider store flow")
	}
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	acquireSANPGTestLock(t)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("postgres sql.DB failed: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	tables := []string{
		"review_tool_calls", "evidence_raw_payloads", "evidence_facts", "review_finding_evidence", "review_findings",
		"stage_review_snapshots", "sql_statements", "sql_fingerprint_groups", "review_runs",
		"change_drafts", "draft_sql_payloads", "async_tasks", "idempotency_records",
		"domain_events", "outbox_events", "audit_events",
		"flow_approval_step_actors", "flow_execution_actors", "flow_approval_steps",
		"flow_stages", "flows", "permission_group_flow_grants", "permission_group_members",
		"permission_groups", "datasource_credentials", "datasource_tls_materials",
		"datasources", "knowledge_entries", "rule_set_prompt_tools", "prompt_tools", "rule_sets",
		"ai_providers", "ai_usage_ledger", "ai_daily_budget_usage", "system_settings", "users",
	}
	for _, tbl := range tables {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b13-pg","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(3), makeKey(4))
	if err := os.WriteFile(keyPath, []byte(keyringDoc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := auditlog.NewRecorder(db)
	svc, err := NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.DatasourceIdentityProbe = func(_ context.Context, _ *gorm.DB, _ *crypto.Crypter, row datasourceTargetRow) (database.ServerIdentity, error) {
		dbName := ""
		if row.DatabaseName != nil {
			dbName = *row.DatabaseName
		}
		return database.ServerIdentity{
			EngineFamily:         row.CompatibilityMode,
			DatabaseName:         dbName,
			ConfiguredEndpointID: fmt.Sprintf("%s:%d", row.Host, row.Port),
			ServerVersion:        "8.0.36",
			InstanceMarker:       "mysql:b13-" + row.ID,
			LowerCaseTableNames:  "0",
		}, nil
	}

	var mu sync.Mutex
	seenSystems := map[string][]string{}
	runTurns := 0
	mockOpenAISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"san-4b"},{"id":"xmodel-v3.1"}]}`))
			return
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sysContent := ""
		userContent := ""
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				sysContent = m.Content
			case "user":
				userContent = m.Content
			}
		}
		mu.Lock()
		seenSystems[req.Model] = append(seenSystems[req.Model], sysContent)
		mu.Unlock()

		// Handle CallActiveProviderJSON call cleanly.
		if strings.Contains(sysContent, "custom-json-helper") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": `{"ok":"pg-json"}`}}},
			})
			return
		}

		// Parse structured review wire payload.
		trimmed := strings.TrimPrefix(userContent, "Review request JSON payload follows. Respond ONLY with a single json object matching the required JSON Schema.\n")
		var wire struct {
			ReviewRunID               string `json:"review_run_id"`
			AnonymizedStatementGroups []any  `json:"anonymized_statement_groups"`
			NormalizedEvidence        []any  `json:"normalized_evidence"`
		}
		_ = json.Unmarshal([]byte(trimmed), &wire)

		var reply string
		if wire.ReviewRunID == provider.ProbeRunID || len(wire.AnonymizedStatementGroups) == 0 {
			reply = `{"schema_version":1,"review_run_id":"` + wire.ReviewRunID + `","outcome":"complete","summary":"Probe OK","findings":[],"tool_requests":[]}`
		} else {
			mu.Lock()
			runTurns++
			turn := runTurns
			mu.Unlock()
			switch turn {
			case 1:
				// Turn 1: SAN requests raw schema/table tool arguments -> triggers translateSANToolArgsToTokens + copyNormalizedFactForSAN.
				reply = `{"schema_version":1,"review_run_id":"` + wire.ReviewRunID + `","outcome":"complete","summary":"Need table schema","findings":[],"tool_requests":[{"tool_call_id":"550e8400-e29b-41d4-a716-446655440000","tool_name":"schema_metadata","arguments":{"schema":"shop","table":"orders"}}]}`
			case 2:
				// Turn 2: Force single schema retry so schemaPending branch runs.
				reply = `{"invalid_schema":true}`
			default:
				// Final turn after retry completes cleanly.
				reply = `{"schema_version":1,"review_run_id":"` + wire.ReviewRunID + `","outcome":"complete","summary":"No blocking issues on shop.orders.","findings":[],"tool_requests":[]}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer mockOpenAISrv.Close()

	ctx := context.Background()
	actorID := uuid.NewString()
	owner := Actor{UserID: uuid.NewString(), Username: "san_owner"}

	// Invalid system_prompt_profile fails validation at create.
	if _, err := svc.CreateAIProvider(ctx, actorID, CreateAIProviderInput{
		Name: "bad-prof", ProviderKind: "openai_compatible", BaseURL: mockOpenAISrv.URL + "/v1",
		ModelName: "san-4b", Enabled: false, SelectionPriority: 9, APIKey: "sk-1",
		Configuration: map[string]any{"system_prompt_profile": "v3_raw"},
	}); err == nil {
		t.Fatal("expected invalid system_prompt_profile to be rejected")
	}

	// Create enabled SAN provider -> runs probeCandidate and auto-assigns SAN privacy hash.
	sanView, err := svc.CreateAIProvider(ctx, actorID, CreateAIProviderInput{
		Name: "san-primary", ProviderKind: "openai_compatible", BaseURL: mockOpenAISrv.URL + "/v1",
		ModelName: "san-4b", Enabled: true, SelectionPriority: 1, APIKey: "sk-san",
		Configuration: map[string]any{"system_prompt_profile": "san"},
	})
	if err != nil {
		t.Fatalf("CreateAIProvider SAN failed: %v", err)
	}
	if sanView.Configuration == nil || sanView.Configuration.SystemPromptProfile == nil || *sanView.Configuration.SystemPromptProfile != "san" {
		t.Fatalf("expected system_prompt_profile=san in view, got %+v", sanView.Configuration)
	}

	// Verify ListAIProviders, GetAIProvider, TestAIProviderConnection, ListProviderModels.
	listed, _, err := svc.ListAIProviders(ctx, 10, "", "san")
	if err != nil || len(listed) != 1 || listed[0].ID != sanView.ID {
		t.Fatalf("ListAIProviders unexpected result: len=%d err=%v", len(listed), err)
	}
	gotView, err := svc.GetAIProvider(ctx, sanView.ID)
	if err != nil || gotView.ID != sanView.ID {
		t.Fatalf("GetAIProvider failed: %v", err)
	}
	if err := svc.TestAIProviderConnection(ctx, sanView.ID); err != nil {
		t.Fatalf("TestAIProviderConnection SAN failed: %v", err)
	}
	models, err := svc.ListProviderModels(ctx, mockOpenAISrv.URL+"/v1", "", sanView.ID)
	if err != nil || len(models) != 2 {
		t.Fatalf("ListProviderModels with stored key failed: err=%v models=%+v", err, models)
	}

	// ReplaceAIProvider: switch from san -> default -> back to san (verifying config persistence & hash updates).
	if _, err := svc.ReplaceAIProvider(ctx, sanView.ID, ReplaceAIProviderInput{
		ExpectVersion: sanView.Version, Name: sanView.Name, ProviderKind: sanView.ProviderKind,
		BaseURL: sanView.BaseURL, ModelName: "san-4b", Enabled: false, SelectionPriority: 1,
		Configuration: map[string]any{"system_prompt_profile": "invalid"},
	}); err == nil {
		t.Fatal("expected invalid system_prompt_profile in ReplaceAIProvider to fail")
	}
	repDef, err := svc.ReplaceAIProvider(ctx, sanView.ID, ReplaceAIProviderInput{
		ExpectVersion: sanView.Version, Name: sanView.Name, ProviderKind: sanView.ProviderKind,
		BaseURL: sanView.BaseURL, ModelName: "xmodel-v3.1", Enabled: false, SelectionPriority: 1,
		Configuration: map[string]any{"system_prompt_profile": "default"},
	})
	if err != nil || repDef.Configuration == nil || *repDef.Configuration.SystemPromptProfile != "default" {
		t.Fatalf("ReplaceAIProvider to default failed: err=%v view=%+v", err, repDef)
	}
	repSAN, err := svc.ReplaceAIProvider(ctx, sanView.ID, ReplaceAIProviderInput{
		ExpectVersion: repDef.Version, Name: sanView.Name, ProviderKind: sanView.ProviderKind,
		BaseURL: sanView.BaseURL, ModelName: "san-4b", Enabled: true, SelectionPriority: 1,
		Configuration: map[string]any{"system_prompt_profile": "san"},
	})
	if err != nil || repSAN.Configuration == nil || *repSAN.Configuration.SystemPromptProfile != "san" {
		t.Fatalf("ReplaceAIProvider back to san failed: err=%v view=%+v", err, repSAN)
	}

	// Test CallActiveProviderJSON against the PG-persisted SAN chain.
	jsonOut, err := svc.CallActiveProviderJSON(ctx, "custom-json-helper", "hi")
	if err != nil || jsonOut != `{"ok":"pg-json"}` {
		t.Fatalf("CallActiveProviderJSON PG failed: err=%v out=%q", err, jsonOut)
	}

	// Seed datasource + rule set + flow + grant + draft for resolver & AgentRunner test.
	dsID := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'b13-ds', 'mysql', 'mysql', 'native', 'db.example.com', 3306, 'shop', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ab", 32), actorID).Error; err != nil {
		t.Fatalf("seed datasource: %v", err)
	}
	ruleView, err := svc.CreateRuleSet(ctx, actorID, "b13-rules-"+uuid.NewString()[:8], true, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("CreateRuleSet: %v", err)
	}
	flowID := uuid.NewString()
	if err := db.Exec(`insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, 'b13-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, ruleView.ID, actorID).Error; err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	stageID := uuid.NewString()
	if err := db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 1, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageID, flowID, dsID, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	stepID := uuid.NewString()
	if err := db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position) values (?, ?, ?, 1)`,
		stepID, flowID, stageID).Error; err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepID, actorID).Error; err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	if err := db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`, stageID, actorID).Error; err != nil {
		t.Fatalf("seed exec actor: %v", err)
	}
	permGroupID := uuid.NewString()
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'b13-group', true, 1, ?, now(), now())`, permGroupID, actorID).Error; err != nil {
		t.Fatalf("seed perm group: %v", err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, permGroupID, owner.UserID).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, permGroupID, flowID).Error; err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	const sampleSQL = "UPDATE orders SET amount = amount + 1 WHERE id = 1;\n"
	draft, err := svc.CreateDraft(ctx, owner, CreateDraftInput{FlowID: flowID, Title: "b13 san pg"})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	draft, err = svc.ReplaceSQL(ctx, owner, draft.ID, []byte(sampleSQL), draft.Version)
	if err != nil {
		t.Fatalf("ReplaceSQL: %v", err)
	}

	// Execute AgentRunner ephemeral run -> exercises SAN tool_requests translation + copyNormalizedFactForSAN + schemaPending retry.
	runner := NewAgentRunner(svc, AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewOpenAIAdapter(ep, nil)
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return sanTestFactSource{}, func() {}, nil
		},
		RuntimeBudget: &RuntimeBudgetConfig{
			PerStageEstimateMinor: 100, MaxModelTurns: 6, MaxModelCalls: 6, MaxToolCalls: 4, MaxDurationSec: 30, PerModelTimeout: 10, PerToolTimeout: 5,
		},
	})
	runID, err := svc.StartEphemeralRun(ctx, owner, runner, EphemeralStartInput{
		FlowID: flowID, DraftID: draft.ID, SQL: []byte(sampleSQL), IdempotencyKey: "san-pg-run-1",
	})
	if err != nil {
		t.Fatalf("StartEphemeralRun: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		er := svc.EphemeralGetForTest(runID, owner.UserID)
		if er != nil && (er.State() == domain.RunStateReady || er.State() == domain.RunStateFailed || er.State() == domain.RunStatePartial || er.State() == domain.RunStateBlocked) {
			if er.State() != domain.RunStateReady {
				t.Fatalf("expected ephemeral SAN run ready, got %s", er.State())
			}
			if merr := db.Transaction(func(tx *gorm.DB) error {
				return svc.MaterializeEphemeralRunTx(ctx, tx, er, draft.ID, draft.Revision)
			}); merr != nil {
				t.Fatalf("MaterializeEphemeralRunTx: %v", merr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for SAN ephemeral run %s", runID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify DB-backed resolveRepresentativeStatementSQL + runProfileForDB with empty profileHint.
	var groupID string
	if err := db.Raw(`select id::text from sql_fingerprint_groups where review_run_id = ? limit 1`, runID).Scan(&groupID).Error; err != nil || groupID == "" {
		t.Fatalf("query materialized group_id: err=%v id=%q", err, groupID)
	}
	if prof := runProfileForDB(ctx, db, runID); prof != "mysql/mysql" {
		t.Fatalf("expected runProfileForDB=mysql/mysql, got %q", prof)
	}
	resolvedSQL, err := resolveRepresentativeStatementSQL(ctx, db, crypter, runID, groupID, "")
	if err != nil || !strings.Contains(resolvedSQL, "orders") {
		t.Fatalf("resolveRepresentativeStatementSQL from PG draft failed: err=%v sql=%q", err, resolvedSQL)
	}
	if _, err := resolveRepresentativeStatementSQL(ctx, db, crypter, runID, uuid.NewString(), ""); !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("expected ErrFactSourceUnavailable for unknown group, got %v", err)
	}

	mu.Lock()
	sanPrompts := append([]string{}, seenSystems["san-4b"]...)
	mu.Unlock()
	if len(sanPrompts) < 3 {
		t.Fatalf("expected SAN prompts across probe + tool turn + retry turn, got %d", len(sanPrompts))
	}
	if !strings.Contains(sanPrompts[len(sanPrompts)-1], "flag: retry_schema") {
		t.Fatalf("expected flag: retry_schema on retried SAN system prompt, got:\n%s", sanPrompts[len(sanPrompts)-1])
	}
}
