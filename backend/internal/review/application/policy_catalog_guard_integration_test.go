//go:build integration

package application

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// policyCatalogIntegrationSource extends the existing B7 read-only fact
// fixture without changing its production-facing interface.
type policyCatalogIntegrationSource struct {
	*fakeFactSource
	catalog       database.PolicyCatalog
	err           error
	calls         int
	failAt        int
	requestedKeys []string
	tableRequests []scopedTableTokens
}

func (s *policyCatalogIntegrationSource) PolicyCatalog(
	_ context.Context,
	schemas []string,
) (database.PolicyCatalog, error) {
	s.calls++
	if s.failAt > 0 && s.calls >= s.failAt {
		return database.PolicyCatalog{}, database.ErrPolicyCatalogUnavailable
	}
	s.requestedKeys = append([]string(nil), schemas...)
	if s.err != nil {
		return database.PolicyCatalog{}, s.err
	}
	return s.catalog, nil
}

func (s *policyCatalogIntegrationSource) TableMetadata(
	ctx context.Context,
	schema string,
	table string,
) (map[string]any, error) {
	s.tableRequests = append(s.tableRequests, scopedTableTokens{Schema: schema, Table: table})
	return s.fakeFactSource.TableMetadata(ctx, schema, table)
}

type policyObjectReferenceContext struct {
	Kind             string `json:"kind"`
	Token            string `json:"token"`
	SchemaToken      string `json:"schema_token"`
	ParentTableToken string `json:"parent_token"`
}

type policyObjectReferencePayload struct {
	PolicyObjectReferences []policyObjectReferenceContext `json:"objects"`
}

func policyObjectReferencesFromPrompt(
	t *testing.T,
	prompt string,
) policyObjectReferencePayload {
	t.Helper()
	marker := strings.Index(prompt, `"objects"`)
	if marker < 0 {
		t.Fatalf("Provider prompt has no objects context:\n%s", prompt)
	}
	start := strings.LastIndex(prompt[:marker], "{")
	if start < 0 {
		t.Fatal("objects has no enclosing JSON object")
	}
	var payload policyObjectReferencePayload
	if err := json.NewDecoder(strings.NewReader(prompt[start:])).Decode(&payload); err != nil {
		t.Fatalf("decode objects: %v\n%s", err, prompt[start:])
	}
	return payload
}

func (b *b7fx) setPolicyCatalogRule(
	t *testing.T,
	knowledgeText string,
	params map[string]any,
) {
	t.Helper()
	def := PromptToolDefinition{
		KnowledgeText: knowledgeText,
		MaxSeverity:   "critical",
		Version:       1,
	}
	hash, err := promptToolConfigHash("must-where", "all", params, def)
	if err != nil {
		t.Fatalf("hash catalog rule: %v", err)
	}
	definitionJSON, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal catalog rule definition: %v", err)
	}
	paramsJSON, err := json.Marshal(normalizeParams(params))
	if err != nil {
		t.Fatalf("marshal catalog rule parameters: %v", err)
	}
	if err := b.db.Exec(`
		update prompt_tools
		set definition = ?::jsonb, parameters = ?::jsonb, config_hash = ?
		where id = ?`,
		string(definitionJSON), string(paramsJSON), hash, b.toolID).Error; err != nil {
		t.Fatalf("set catalog rule: %v", err)
	}
}

func policyCatalogIntegrationCatalog(
	identifiers ...database.PolicyCatalogIdentifier,
) database.PolicyCatalog {
	schemas := map[string]bool{}
	schemaIdentifiers := map[string]bool{}
	for _, identifier := range identifiers {
		schemas[identifier.Schema] = true
		if identifier.Kind == database.PolicyCatalogSchema {
			schemaIdentifiers[identifier.Schema] = true
		}
	}
	for schema := range schemas {
		if !schemaIdentifiers[schema] {
			identifiers = append(identifiers, database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogSchema, Schema: schema, Name: schema,
			})
		}
	}
	return database.PolicyCatalog{
		Identifiers: identifiers,
		SchemaCount: len(schemas),
	}
}

func policyCatalogIntegrationDeps(
	source tool.FactSource,
	respond func(provider.Request) ([]byte, int, error),
) AgentDeps {
	return AgentDeps{
		Dial: func(endpoint provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(endpoint, respond), nil
		},
		FactSourceFor: func(
			context.Context,
			string,
			FrozenDatasourceTarget,
			string,
		) (tool.FactSource, func(), error) {
			return source, func() {}, nil
		},
	}
}

func policyCatalogIntegrationComplete(
	req provider.Request,
) ([]byte, int, error) {
	body := map[string]any{
		"schema_version": 1,
		"review_run_id":  req.ReviewRunID,
		"outcome":        "complete",
		"summary":        "Review complete.",
		"findings":       []any{},
		"tool_requests":  []any{},
	}
	raw, err := json.Marshal(body)
	return raw, 200, err
}

func requirePolicyCatalogPrivacyFailed(
	t *testing.T,
	b *b7fx,
	runID string,
	providerCalls int,
) {
	t.Helper()
	var state string
	var code *int32
	if err := b.db.Raw(
		`select state, business_err_code from review_runs where id = ?`,
		runID,
	).Row().Scan(&state, &code); err != nil {
		t.Fatalf("read catalog failure run state: %v", err)
	}
	if state != string(domain.RunStateFailed) || code == nil || *code != 2007 {
		t.Fatalf("catalog failure run = %s/%v, want failed/2007", state, code)
	}
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil {
		t.Fatalf("read catalog failure gate: %v", err)
	}
	if run.Gate.Passed {
		t.Fatal("catalog privacy failure unexpectedly passed the submission gate")
	}
	if len(run.StageResults) == 0 {
		t.Fatal("catalog privacy failure has no stage gate evidence")
	}
	for _, stage := range run.StageResults {
		if stage.GatePassed {
			t.Fatalf("catalog privacy failure stage %d unexpectedly passed its gate",
				stage.StagePosition)
		}
	}
	if providerCalls != 0 {
		t.Fatalf("Provider called before catalog privacy proof succeeded: calls=%d", providerCalls)
	}
}

func TestPolicyCatalogGuardIntegrationRejectsRawCatalogNameAbsentFromSQL(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t,
		"Inspect shop.audit_table before accepting the statement.", nil)
	source := &policyCatalogIntegrationSource{
		fakeFactSource: &fakeFactSource{},
		catalog: policyCatalogIntegrationCatalog(
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "audit_table",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogColumn, Schema: "shop", Relation: "audit_table",
				Name: "actor",
			},
		),
	}
	providerCalls := 0
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
		providerCalls++
		return policyCatalogIntegrationComplete(req)
	})

	runID := b.runAgentSQL(t, deps, "select 1;\n", "catalog-raw-"+runKeySuffix())

	requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
	if source.calls == 0 {
		t.Fatal("catalog guard did not request the trusted catalog")
	}
}

func TestPolicyCatalogGuardIntegrationRejectsFrozenDatabaseNameInEveryPolicyChannel(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *b7fx)
	}{
		{
			name: "prompt tool",
			prepare: func(t *testing.T, b *b7fx) {
				b.setPolicyCatalogRule(t,
					"Never operate against private_prod during this review.", nil)
			},
		},
		{
			name: "knowledge entry",
			prepare: func(t *testing.T, b *b7fx) {
				definition, err := json.Marshal(map[string]any{
					"knowledge_text": "Never operate against private_prod during this review.",
					"max_severity":   "medium",
					"version":        1,
				})
				if err != nil {
					t.Fatalf("marshal knowledge privacy rule: %v", err)
				}
				if err := b.db.Exec(`
					update knowledge_entries
					set scope_type = 'datasource', database_name = 'private_prod', table_name = null,
					    definition = ?::jsonb, config_hash = repeat('e', 64)
					where id = ?`,
					string(definition), b.entryTable).Error; err != nil {
					t.Fatalf("set knowledge privacy rule: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newB7Fixture(t)
			if err := b.db.Exec(`
				update datasources
				set database_name = 'private_prod'
				where id = (
					select datasource_id
					from flow_stages
					where flow_id = ?
					order by position
					limit 1
				)`, b.flowID).Error; err != nil {
				t.Fatalf("set distinct frozen database name: %v", err)
			}
			test.prepare(t, b)
			source := &policyCatalogIntegrationSource{
				fakeFactSource: &fakeFactSource{},
				catalog: policyCatalogIntegrationCatalog(
					database.PolicyCatalogIdentifier{
						Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
					},
					database.PolicyCatalogIdentifier{
						Kind: database.PolicyCatalogColumn, Schema: "shop", Relation: "orders",
						Name: "id",
					},
				),
			}
			providerCalls := 0
			deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
				providerCalls++
				return policyCatalogIntegrationComplete(req)
			})

			runID := b.runAgentSQL(t, deps, "select 1;\n",
				"catalog-database-name-"+runKeySuffix())

			requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
			var ledger int64
			if err := b.db.Raw(
				`select count(*) from ai_usage_ledger where review_run_id = ?`, runID,
			).Scan(&ledger).Error; err != nil {
				t.Fatalf("read database-name privacy ledger: %v", err)
			}
			if ledger != 0 {
				t.Fatalf("database-name privacy failure recorded %d Provider charges", ledger)
			}
		})
	}
}

func TestPolicyCatalogGuardIntegrationRejectsSchemaOnlyRuleBeforeProvider(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t,
		`Apply this mandatory rule to {{ref:schema:["shop"]}}.`, nil)
	source := &policyCatalogIntegrationSource{
		fakeFactSource: &fakeFactSource{},
		catalog: policyCatalogIntegrationCatalog(
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogColumn, Schema: "shop", Relation: "orders",
				Name: "id",
			},
		),
	}
	providerCalls := 0
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
		providerCalls++
		return policyCatalogIntegrationComplete(req)
	})

	// The SQL itself has a valid table/column target. The separate mandatory
	// schema-only rule still has no supported schema fact tool, so it must fail
	// before the model can return an unsupported model-only Critical finding.
	runID := b.runAgentSQL(t, deps, "select id from orders;",
		"catalog-schema-only-"+runKeySuffix())

	requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
	if source.calls != 1 {
		t.Fatalf("catalog integrity checks=%d, want one pre-Provider check", source.calls)
	}
}

func TestPolicyCatalogGuardIntegrationResolvesTypedReference(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t,
		`Inspect {{ref:table:["shop","audit_table"]}} before accepting the statement.`, nil)
	source := &policyCatalogIntegrationSource{
		fakeFactSource: &fakeFactSource{},
		catalog: policyCatalogIntegrationCatalog(
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "audit_table",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogColumn, Schema: "shop", Relation: "audit_table",
				Name: "actor",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "unused_table",
			},
		),
	}
	var captured []provider.Request
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
		captured = append(captured, req)
		return policyCatalogIntegrationComplete(req)
	})

	runID := b.runAgentSQL(t, deps, "select 1;\n", "catalog-typed-"+runKeySuffix())

	if len(captured) == 0 {
		t.Fatal("typed catalog reference did not reach Provider")
	}
	final := captured[0]
	serialized, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal final Provider request: %v", err)
	}
	for _, rawIdentifier := range []string{"audit_table", "unused_table", `"shop"`} {
		if strings.Contains(string(serialized), rawIdentifier) {
			t.Fatalf("final Provider request retained %q: %s", rawIdentifier, serialized)
		}
	}
	if !strings.Contains(final.SystemPrompt, "table_1") {
		t.Fatalf("explicit catalog reference has no anonymous token:\n%s", final.SystemPrompt)
	}
	tableTokens := map[string]bool{}
	for _, token := range regexp.MustCompile(`\btable_[0-9]+\b`).FindAllString(final.SystemPrompt, -1) {
		tableTokens[token] = true
	}
	if len(tableTokens) != 1 || !tableTokens["table_1"] {
		t.Fatalf("catalog table tokens = %v, want unique table_1 only", tableTokens)
	}
	if len(source.requestedKeys) != 1 || source.requestedKeys[0] != "shop" {
		t.Fatalf("catalog requested schemas = %v, want [shop]", source.requestedKeys)
	}

	var state string
	if err := b.db.Raw(
		`select state from review_runs where id = ?`, runID,
	).Row().Scan(&state); err != nil {
		t.Fatalf("read typed-reference run state: %v", err)
	}
	if state == string(domain.RunStateFailed) {
		t.Fatalf("typed catalog reference failed instead of reaching Provider: %s", state)
	}
}

func TestPolicyCatalogGuardIntegrationDeclaresOnlyReferencedToolScope(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t,
		`Inspect {{ref:table:["shop","audit_table"]}} before accepting the statement.`, nil)
	source := &policyCatalogIntegrationSource{
		fakeFactSource: &fakeFactSource{},
		catalog: policyCatalogIntegrationCatalog(
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "audit_table",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogColumn, Schema: "shop", Relation: "audit_table",
				Name: "actor",
			},
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "unused_table",
			},
		),
	}
	var captured []provider.Request
	providerCalls := 0
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
		providerCalls++
		captured = append(captured, req)
		switch providerCalls {
		case 1:
			payload := policyObjectReferencesFromPrompt(t, req.SystemPrompt)
			var tableRefs []policyObjectReferenceContext
			for _, ref := range payload.PolicyObjectReferences {
				if ref.Kind == "table" {
					tableRefs = append(tableRefs, ref)
				}
			}
			if len(tableRefs) != 1 {
				t.Fatalf("policy table tool scope = %+v, want one declared table", tableRefs)
			}
			tableRef := tableRefs[0]
			if tableRef.Token == "" || tableRef.SchemaToken == "" ||
				tableRef.ParentTableToken != "" {
				t.Fatalf("invalid declared table reference: %+v", tableRef)
			}
			for _, forbidden := range []string{"audit_table", "unused_table", `"shop"`} {
				if strings.Contains(req.SystemPrompt, forbidden) {
					t.Fatalf("policy tool scope retained raw identifier %q:\n%s",
						forbidden, req.SystemPrompt)
				}
			}
			body := map[string]any{
				"schema_version": 1,
				"review_run_id":  req.ReviewRunID,
				"outcome":        "complete",
				"summary":        "Continue review after reading declared objects.",
				"findings":       []any{},
				"tool_requests": []map[string]any{
					{
						"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000071",
						"tool_name":    "schema_metadata",
						"arguments": map[string]any{
							"schema": tableRef.SchemaToken,
							"table":  tableRef.Token,
						},
					},
					{
						"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000072",
						"tool_name":    "schema_metadata",
						"arguments": map[string]any{
							"schema": tableRef.SchemaToken,
							"table":  "unused_table",
						},
					},
				},
			}
			raw, err := json.Marshal(body)
			return raw, 200, err
		case 2:
			if len(req.NormalizedEvidence) != 1 {
				t.Fatalf("second Provider turn evidence = %d, want the declared table fact",
					len(req.NormalizedEvidence))
			}
			return policyCatalogIntegrationComplete(req)
		default:
			t.Fatalf("unexpected Provider turn %d", providerCalls)
			return nil, 500, nil
		}
	})

	runID := b.runAgentSQL(t, deps, "select 1;\n",
		"catalog-tool-scope-"+runKeySuffix())

	if providerCalls != 2 {
		t.Fatalf("Provider calls=%d, want metadata turn followed by completion", providerCalls)
	}
	if len(captured) != 2 {
		t.Fatalf("captured Provider requests=%d, want 2", len(captured))
	}
	if source.scopedMetadataCalls != 1 {
		t.Fatalf("scoped metadata calls=%d, want exactly the declared table",
			source.scopedMetadataCalls)
	}
	if len(source.tableRequests) != 1 ||
		source.tableRequests[0] != (scopedTableTokens{Schema: "shop", Table: "audit_table"}) {
		t.Fatalf("trusted table metadata requests = %+v, want shop/audit_table only",
			source.tableRequests)
	}
	var successCalls, failedCalls int64
	if err := b.db.Raw(`
		select
			count(*) filter (where success),
			count(*) filter (where not success and error_code = 2012)
		from review_tool_calls
		where review_run_id = ? and tool_name = 'schema_metadata'`,
		runID).Row().Scan(&successCalls, &failedCalls); err != nil {
		t.Fatalf("read policy tool-call scope: %v", err)
	}
	if successCalls != 1 || failedCalls != 1 {
		t.Fatalf("policy tool calls success=%d rejected=%d, want 1/1",
			successCalls, failedCalls)
	}
	var evidenceFacts int64
	if err := b.db.Raw(`
		select count(*) from evidence_facts ef
		join stage_review_snapshots s on s.id = ef.stage_review_id
		where s.review_run_id = ? and ef.source_kind = 'database_tool'`,
		runID).Scan(&evidenceFacts).Error; err != nil {
		t.Fatalf("read declared-table evidence: %v", err)
	}
	if evidenceFacts != 1 {
		t.Fatalf("declared-table evidence facts=%d, want 1", evidenceFacts)
	}

	var state string
	var code *int32
	if err := b.db.Raw(
		`select state, business_err_code from review_runs where id = ?`, runID,
	).Row().Scan(&state, &code); err != nil {
		t.Fatalf("read policy tool-scope run state: %v", err)
	}
	if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
		t.Fatalf("rejected undeclared policy scope run = %s/%v, want partial/2007",
			state, code)
	}
}

func TestPolicyCatalogGuardIntegrationFailsClosedWithoutCatalog(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t, "Require a bounded statement.", nil)
	source := &policyCatalogIntegrationSource{
		fakeFactSource: &fakeFactSource{},
		err:            database.ErrPolicyCatalogUnavailable,
	}
	providerCalls := 0
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
		providerCalls++
		return policyCatalogIntegrationComplete(req)
	})

	runID := b.runAgentSQL(t, deps, "select 1;\n", "catalog-unavailable-"+runKeySuffix())

	requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
	if source.calls == 0 {
		t.Fatal("PolicyCatalog capability was never queried")
	}
}

func TestPolicyCatalogGuardIntegrationRejectsIncompleteOrAmbiguousCatalog(t *testing.T) {
	orders := database.PolicyCatalogIdentifier{
		Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
	}
	tests := []struct {
		name    string
		catalog database.PolicyCatalog
	}{
		{
			name: "schema count mismatch",
			catalog: database.PolicyCatalog{
				Identifiers: []database.PolicyCatalogIdentifier{
					{Kind: database.PolicyCatalogSchema, Schema: "shop", Name: "shop"},
					orders,
				},
				SchemaCount: 2,
			},
		},
		{
			name: "unauthorized schema",
			catalog: policyCatalogIntegrationCatalog(
				database.PolicyCatalogIdentifier{
					Kind: database.PolicyCatalogTable, Schema: "private", Name: "secret_table",
				},
			),
		},
		{
			name:    "duplicate identity",
			catalog: policyCatalogIntegrationCatalog(orders, orders),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newB7Fixture(t)
			b.setPolicyCatalogRule(t, "Require a bounded statement.", nil)
			source := &policyCatalogIntegrationSource{
				fakeFactSource: &fakeFactSource{},
				catalog:        test.catalog,
			}
			providerCalls := 0
			deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
				providerCalls++
				return policyCatalogIntegrationComplete(req)
			})

			runID := b.runAgentSQL(t, deps, "select 1;\n",
				"catalog-incomplete-"+runKeySuffix())

			requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
		})
	}
}

func TestPolicyCatalogGuardIntegrationRejectsMissingOrdinaryParameter(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t, "Reject statements affecting {{threshold}} rows.", nil)
	source := &policyCatalogIntegrationSource{
		fakeFactSource: &fakeFactSource{},
		catalog: policyCatalogIntegrationCatalog(
			database.PolicyCatalogIdentifier{
				Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
			},
		),
	}
	providerCalls := 0
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
		providerCalls++
		return policyCatalogIntegrationComplete(req)
	})

	runID := b.runAgentSQL(t, deps, "select 1;\n",
		"catalog-missing-param-"+runKeySuffix())

	requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
}

func TestPolicyCatalogGuardIntegrationRejectsUnresolvedOrMalformedReference(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{
		{
			name: "unresolved identity",
			text: `Inspect {{ref:table:["shop","missing"]}}.`,
		},
		{
			name: "malformed reference",
			text: `Inspect {{ref:table:["shop","orders"]}.`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newB7Fixture(t)
			b.setPolicyCatalogRule(t, test.text, nil)
			source := &policyCatalogIntegrationSource{
				fakeFactSource: &fakeFactSource{},
				catalog: policyCatalogIntegrationCatalog(
					database.PolicyCatalogIdentifier{
						Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders",
					},
				),
			}
			providerCalls := 0
			deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
				providerCalls++
				return policyCatalogIntegrationComplete(req)
			})

			runID := b.runAgentSQL(t, deps, "select 1;\n",
				"catalog-bad-ref-"+runKeySuffix())

			requirePolicyCatalogPrivacyFailed(t, b, runID, providerCalls)
		})
	}
}

func TestPolicyCatalogGuardIntegrationSQLPolicyOverlapOneObservedFact(t *testing.T) {
	for _, sql := range []string{"select id from orders;", "select `id` from `orders`;"} {
		t.Run(sql, func(t *testing.T) {
			b := newB7Fixture(t)
			b.setPolicyCatalogRule(t, `Inspect {{ref:table:["shop","orders"]}} and {{ref:column:["shop","orders","id"]}}.`, nil)
			source := &policyCatalogIntegrationSource{fakeFactSource: &fakeFactSource{}, catalog: policyCatalogIntegrationCatalog(
				database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "shop", Name: "orders"},
				database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogColumn, Schema: "shop", Relation: "orders", Name: "id"},
			)}
			calls := 0
			deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) {
				calls++
				tables, err := scopedTablesFromRequest(req)
				if err != nil {
					t.Fatal(err)
				}
				if len(tables) != 1 {
					t.Fatalf("SQL tables=%d", len(tables))
				}
				payload := policyObjectReferencesFromPrompt(t, req.SystemPrompt)
				tableCount := 0
				for _, o := range payload.PolicyObjectReferences {
					if o.Kind == "table" {
						tableCount++
						if o.Token != tables[0].Table || o.SchemaToken != tables[0].Schema {
							t.Fatal("SQL/policy physical identity diverged")
						}
					}
				}
				if tableCount != 1 {
					t.Fatal("repeated policy reference allocated duplicate member")
				}
				if calls == 1 {
					body := map[string]any{"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete", "summary": "Continue review after reading actual schema.", "findings": []any{}, "tool_requests": []any{map[string]any{"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000081", "tool_name": "schema_metadata", "arguments": map[string]any{"schema": tables[0].Schema, "table": tables[0].Table}}}}
					raw, err := json.Marshal(body)
					return raw, 200, err
				}
				if len(req.NormalizedEvidence) != 1 {
					t.Fatal("expected one observed fact")
				}
				return policyCatalogIntegrationComplete(req)
			})
			runID := b.runAgentSQL(t, deps, sql, "catalog-overlap-"+runKeySuffix())
			var state string
			if err := b.db.Raw(`select state from review_runs where id=?`, runID).Scan(&state).Error; err != nil {
				t.Fatal(err)
			}
			if state != string(domain.RunStateReady) || calls != 2 || source.scopedMetadataCalls != 1 {
				t.Fatalf("overlap state=%s calls=%d facts=%d", state, calls, source.scopedMetadataCalls)
			}
		})
	}
}

// A catalog may disappear between initial compilation and the actual send.
// This must make no Provider call and release the unsent budget reservation.
func TestPolicyCatalogGuardIntegrationRevalidatesActualSend(t *testing.T) {
	b := newB7Fixture(t)
	b.setPolicyCatalogRule(t, "Require bounded queries.", nil)
	source := &policyCatalogIntegrationSource{fakeFactSource: &fakeFactSource{}, catalog: policyCatalogIntegrationCatalog(database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogSchema, Schema: "shop", Name: "shop"}), failAt: 2}
	calls := 0
	deps := policyCatalogIntegrationDeps(source, func(req provider.Request) ([]byte, int, error) { calls++; return policyCatalogIntegrationComplete(req) })
	runID := b.runAgentSQL(t, deps, "select 1;", "catalog-send-"+runKeySuffix())
	requirePolicyCatalogPrivacyFailed(t, b, runID, calls)
	var ledger int64
	if err := b.db.Raw(`select count(*) from ai_usage_ledger where review_run_id=?`, runID).Scan(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || ledger != 0 {
		t.Fatalf("unsent calls=%d ledger=%d", source.calls, ledger)
	}
}
