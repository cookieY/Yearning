package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestPromptToolAI_ValidationAndFailClosedWithoutProvider(t *testing.T) {
	svc := &Services{}
	ctx := context.Background()

	// Validation errors (1001 VALIDATION_FAILED).
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{
			name: "optimize empty knowledge_text",
			fn: func() error {
				_, err := svc.OptimizePromptToolPrompt(ctx, OptimizePromptToolInput{KnowledgeText: "   "})
				return err
			},
		},
		{
			name: "optimize invalid engine",
			fn: func() error {
				_, err := svc.OptimizePromptToolPrompt(ctx, OptimizePromptToolInput{KnowledgeText: "rule", Engine: "invalid"})
				return err
			},
		},
		{
			name: "generate empty knowledge_text",
			fn: func() error {
				_, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{KnowledgeText: ""})
				return err
			},
		},
		{
			name: "generate invalid engine",
			fn: func() error {
				_, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{KnowledgeText: "rule", Engine: "bad"})
				return err
			},
		},
		{
			name: "run empty knowledge_text",
			fn: func() error {
				_, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{KnowledgeText: ""})
				return err
			},
		},
		{
			name: "run empty cases",
			fn: func() error {
				_, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{KnowledgeText: "rule"})
				return err
			},
		},
		{
			name: "run empty case id",
			fn: func() error {
				_, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
					KnowledgeText: "rule",
					Cases:         []PromptToolTestCase{{ID: "", SQL: "UPDATE t SET a = 1 WHERE id = 1"}},
				})
				return err
			},
		},
		{
			name: "run invalid engine",
			fn: func() error {
				_, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
					KnowledgeText: "rule",
					Engine:        "bad",
					Cases:         []PromptToolTestCase{{ID: "c1", SQL: "UPDATE t SET a = 1 WHERE id = 1"}},
				})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); !domain.IsCode(err, 1001) {
				t.Fatalf("%s: expected 1001 VALIDATION_FAILED, got %v", tc.name, err)
			}
		})
	}

	// Fail closed (2011 AI_PROVIDER_FAILED) when no enabled AI provider is configured.
	if _, err := svc.OptimizePromptToolPrompt(ctx, OptimizePromptToolInput{
		Name:          "builtin.dml-max-insert-rows",
		Description:   "Max rows check",
		Parameters:    map[string]any{"max_rows": 100},
		KnowledgeText: "Limit batch insert rows to {{max_rows}}.",
	}); !domain.IsCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("OptimizePromptToolPrompt without provider: expected 2011, got %v", err)
	}

	if _, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{
		Name:          "builtin.ddl-table-comment-required",
		Description:   "Require table comment on CREATE TABLE",
		Engine:        "mysql",
		KnowledgeText: "Every CREATE TABLE statement must include a table COMMENT.",
	}); !domain.IsCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("GeneratePromptToolTestCases without provider: expected 2011, got %v", err)
	}

	if _, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
		Name:          "builtin.ddl-table-comment-required",
		Engine:        "mysql",
		KnowledgeText: "Every CREATE TABLE statement must include a table COMMENT.",
		Cases: []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE t1 (id BIGINT PRIMARY KEY) COMMENT='ok';", ExpectedCompliant: true},
		},
	}); !domain.IsCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("RunPromptToolTest without provider: expected 2011, got %v", err)
	}

	if _, err := svc.CallActiveProviderJSON(ctx, "sys", "user"); err == nil {
		t.Fatal("expected error when provider chain is unavailable")
	}
}

func TestClassifySkillCategoryAndPlaceholders(t *testing.T) {
	if got := classifySkillCategory("builtin.dml-require-where", "", "Update rule"); got != skillCategoryDML {
		t.Fatalf("expected dml, got %q", got)
	}
	if got := classifySkillCategory("custom.rule", "ddl-implicit-type-conversion", "Check column type"); got != skillCategoryDML {
		t.Fatalf("expected implicit type conversion rule to map to dml, got %q", got)
	}
	if got := classifySkillCategory("builtin.ddl-primary-key", "", "Require primary key"); got != skillCategoryDDL {
		t.Fatalf("expected ddl, got %q", got)
	}
	if got := classifySkillCategory(
		"b6-skill-9999",
		"Require WHERE bounds on DML updates",
		"All UPDATE statements (non-DDL writes) must include a bounded WHERE clause filtering on a primary key or indexed column.",
	); got != skillCategoryDML {
		t.Fatalf("expected optimized DML UPDATE rule mentioning primary key/non-DDL to remain dml, got %q", got)
	}
	if got := classifySkillCategory(
		"custom.fk-cascade",
		"Forbid ON UPDATE CASCADE in table definitions",
		"CREATE TABLE and ALTER TABLE foreign key constraints must not use ON UPDATE CASCADE or ON DELETE CASCADE.",
	); got != skillCategoryDDL {
		t.Fatalf("expected DDL foreign key rule mentioning ON UPDATE/ON DELETE to remain ddl, got %q", got)
	}
	if got := classifySkillCategory(
		"custom.batch-update",
		"\u6279\u91cf\u66f4\u65b0\u6570\u636e\u5fc5\u987b\u6309\u4e3b\u952e\u6216\u7d22\u5f15\u5206\u6279\u6267\u884c",
		"\u5355\u6b21\u66f4\u65b0\u6216\u5220\u9664\u64cd\u4f5c\u5fc5\u987b\u6309\u4e3b\u952e\u6216\u7d22\u5f15\u5217\u8fc7\u6ee4\uff0c\u907f\u514d\u5168\u8868\u626b\u63cf\u3002",
	); got != skillCategoryDML {
		t.Fatalf("expected Chinese DML rule mentioning primary key/index to classify as dml, got %q", got)
	}
	if got := classifySkillCategory(
		"custom.no-drop-table-zh",
		"\u751f\u4ea7\u73af\u5883\u7981\u6b62\u5220\u9664\u8868\u6216\u5220\u9664\u5217",
		"\u7981\u6b62\u5220\u9664\u8868\u3001\u5220\u9664\u7d22\u5f15\u6216\u4fee\u6539\u8868\u7ed3\u6784\u3002",
	); got != skillCategoryDDL {
		t.Fatalf("expected Chinese DDL drop-table rule to classify as ddl, got %q", got)
	}
	if got := classifySkillCategory(
		"custom.safe-delete",
		"Require primary key filter when deleting rows",
		"Deleting rows without a primary key filter is forbidden.",
	); got != skillCategoryDML {
		t.Fatalf("expected progressive DML verb rule to classify as dml, got %q", got)
	}
	if got := classifySkillCategory(
		"custom.non-ddl-write",
		"Must filter by primary key",
		"Non-DDL write rule for updating rows.",
	); got != skillCategoryDML {
		t.Fatalf("expected skill named non-ddl-write to classify as dml, got %q", got)
	}
	if got := classifySkillCategory(
		"custom.prevent-backdrop-update",
		"",
		"Must filter by primary key.",
	); got != skillCategoryDML {
		t.Fatalf("expected skill named prevent-backdrop-update to classify as dml, got %q", got)
	}

	phs := collectParamPlaceholders("Check {{max_rows}} and {{max_cols}}", map[string]any{"extra_param": 5})
	if len(phs) != 3 || !hasAllPlaceholders("Rule {{extra_param}} {{max_cols}} {{max_rows}}", phs) {
		t.Fatalf("unexpected placeholders: %v", phs)
	}
	if hasAllPlaceholders("text without placeholder", []string{"{{missing}}"}) {
		t.Fatal("expected hasAllPlaceholders to return false when placeholder is missing")
	}
}

func TestNormalizeAndFilterGeneratedTestCases_RejectsSelectAndCrossCategory(t *testing.T) {
	rawAICases := []PromptToolTestCase{
		{ID: "x-1", SQL: "SELECT id, name FROM users WHERE id = 1;", ExpectedCompliant: true, Rationale: "Invalid SELECT"},
		{ID: "x-2", SQL: "WITH active_users AS (SELECT id FROM users) SELECT * FROM active_users;", ExpectedCompliant: true, Rationale: "Invalid CTE SELECT"},
		{ID: "x-3", SQL: "INSERT INTO users (id, name) VALUES (1, 'alice');", ExpectedCompliant: true, Rationale: "Invalid DML in DDL skill"},
		{ID: "x-4", SQL: "UPDATE users SET name = 'bob' WHERE id = 1;", ExpectedCompliant: false, Rationale: "Invalid DML in DDL skill"},
		{ID: "c-1", SQL: "CREATE TABLE t_ok_1 (id BIGINT PRIMARY KEY) COMMENT='c1';", ExpectedCompliant: true},
		{ID: "c-2", SQL: "CREATE TABLE t_ok_2 (id BIGINT PRIMARY KEY) COMMENT='c2';", ExpectedCompliant: true, Rationale: "ok 2"},
		{ID: "c-3", SQL: "CREATE TABLE t_ok_3 (id BIGINT PRIMARY KEY) COMMENT='c3';", ExpectedCompliant: true, Rationale: "ok 3"},
		{ID: "c-4", SQL: "CREATE TABLE t_ok_4 (id BIGINT PRIMARY KEY) COMMENT='c4';", ExpectedCompliant: true, Rationale: "ok 4"},
		{ID: "c-5", SQL: "CREATE TABLE t_ok_5 (id BIGINT PRIMARY KEY) COMMENT='c5';", ExpectedCompliant: true, Rationale: "ok 5"},
		{ID: "v-1", SQL: "CREATE TABLE t_bad_1 (id BIGINT PRIMARY KEY);", ExpectedCompliant: false},
		{ID: "v-2", SQL: "CREATE TABLE t_bad_2 (id BIGINT PRIMARY KEY);", ExpectedCompliant: false, Rationale: "bad 2"},
		{ID: "v-3", SQL: "CREATE TABLE t_bad_3 (id BIGINT PRIMARY KEY);", ExpectedCompliant: false, Rationale: "bad 3"},
		{ID: "v-4", SQL: "CREATE TABLE t_bad_4 (id BIGINT PRIMARY KEY);", ExpectedCompliant: false, Rationale: "bad 4"},
		{ID: "v-5", SQL: "CREATE TABLE t_bad_5 (id BIGINT PRIMARY KEY);", ExpectedCompliant: false, Rationale: "bad 5"},
	}

	merged, stats, ok := normalizeAndFilterGeneratedTestCases(rawAICases, skillCategoryDDL)
	if !ok {
		t.Fatal("normalizeAndFilterGeneratedTestCases returned ok=false")
	}
	if stats.rejectedCount != 4 || stats.validCompliant != 5 || stats.validNonCompliant != 5 {
		t.Fatalf("unexpected filter stats: %+v", stats)
	}
	if len(merged) != 10 {
		t.Fatalf("len(merged) = %d, want 10", len(merged))
	}
	if merged[0].Rationale == "" || merged[5].Rationale == "" {
		t.Fatal("expected default rationale to be filled")
	}
	for i, c := range merged {
		upper := strings.ToUpper(strings.TrimSpace(c.SQL))
		if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") || strings.HasPrefix(upper, "INSERT") || strings.HasPrefix(upper, "UPDATE") {
			t.Fatalf("merged[%d] retained invalid statement for DDL skill: %s", i, c.SQL)
		}
		if !isStatementValidForCategory(c.SQL, skillCategoryDDL) {
			t.Fatalf("merged[%d] is not a DDL statement: %s", i, c.SQL)
		}
	}

	if _, _, ok := normalizeAndFilterGeneratedTestCases(rawAICases[:6], skillCategoryDDL); ok {
		t.Fatal("expected ok=false when fewer than 5 compliant + 5 non-compliant valid cases remain")
	}
	if isStatementValidForCategory("   ", skillCategoryDDL) {
		t.Fatal("expected false for blank SQL")
	}
	if isStatementValidForCategory("CREATE TABLE t (id INT);", skillCategoryDML) {
		t.Fatal("expected false for DDL in DML category")
	}
	if !isStatementValidForCategory("-- compliant case\nUPDATE\n  orders SET status = 1 WHERE id = 1;", skillCategoryDML) {
		t.Fatal("expected leading line comment + multiline UPDATE to be valid for DML")
	}
	if !isStatementValidForCategory("/* header */ CREATE TABLE t1 (id BIGINT PRIMARY KEY);", skillCategoryDDL) {
		t.Fatal("expected leading block comment + CREATE TABLE to be valid for DDL")
	}
	if !isStatementValidForCategory("# mysql comment\nUPDATE orders SET status = 1 WHERE id = 1;", skillCategoryDML) {
		t.Fatal("expected leading # comment + UPDATE to be valid for DML")
	}
	if !isStatementValidForCategory("COMMENT\n  ON TABLE users IS 'ok';", skillCategoryDDL) {
		t.Fatal("expected multiline COMMENT ON statement to be valid for DDL")
	}
	if isStatementValidForCategory("-- tricky\nSELECT id FROM orders;", skillCategoryDML) {
		t.Fatal("expected SELECT after leading comment to remain rejected")
	}
	if isStatementValidForCategory("/* unclosed comment UPDATE orders SET a = 1;", skillCategoryDML) {
		t.Fatal("expected unclosed block comment to be rejected")
	}
}

func TestGeneratePromptToolTestCases_BoundedRetry(t *testing.T) {
	ctx := context.Background()
	validDMLJSON := `{"cases":[` +
		`{"id":"c1","sql":"UPDATE orders SET status='ok' WHERE id=1;","expected_compliant":true,"rationale":"bounded"},` +
		`{"id":"c2","sql":"UPDATE orders SET status='ok' WHERE id=2;","expected_compliant":true,"rationale":"bounded"},` +
		`{"id":"c3","sql":"UPDATE orders SET status='ok' WHERE id=3;","expected_compliant":true,"rationale":"bounded"},` +
		`{"id":"c4","sql":"UPDATE orders SET status='ok' WHERE id=4;","expected_compliant":true,"rationale":"bounded"},` +
		`{"id":"c5","sql":"UPDATE orders SET status='ok' WHERE id=5;","expected_compliant":true,"rationale":"bounded"},` +
		`{"id":"v1","sql":"UPDATE orders SET status='bad';","expected_compliant":false,"rationale":"unbounded"},` +
		`{"id":"v2","sql":"UPDATE items SET qty=0;","expected_compliant":false,"rationale":"unbounded"},` +
		`{"id":"v3","sql":"UPDATE users SET active=false;","expected_compliant":false,"rationale":"unbounded"},` +
		`{"id":"v4","sql":"UPDATE invoices SET paid=true;","expected_compliant":false,"rationale":"unbounded"},` +
		`{"id":"v5","sql":"UPDATE accounts SET balance=0;","expected_compliant":false,"rationale":"unbounded"}` +
		`]}`
	invalidJSON := `{"cases":[{"id":"x1","sql":"SELECT * FROM orders;","expected_compliant":true,"rationale":"bad"}]}`

	attempts := 0
	var secondPayload string
	out, err := generatePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{
		Name:          "b6-skill-retry",
		Description:   "Require WHERE bounds on DML updates",
		Engine:        "all",
		KnowledgeText: "All UPDATE statements must include a WHERE clause using primary key or index.",
	}, func(_ context.Context, _ string, userContent string) (string, error) {
		attempts++
		if attempts == 1 {
			return invalidJSON, nil
		}
		secondPayload = userContent
		return validDMLJSON, nil
	})
	if err != nil {
		t.Fatalf("expected bounded retry to succeed on attempt 2, got %v", err)
	}
	if attempts != 2 || len(out.Cases) != 10 {
		t.Fatalf("unexpected retry outcome: attempts=%d len=%d", attempts, len(out.Cases))
	}
	if !strings.Contains(secondPayload, "retry_feedback") {
		t.Fatalf("expected retry_feedback in attempt 2 payload, got %s", secondPayload)
	}

	// Exhausting both attempts fails closed with 2011 AI_PROVIDER_FAILED.
	failAttempts := 0
	_, err = generatePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{
		Name:          "b6-skill-fail",
		Description:   "Require WHERE bounds on DML updates",
		Engine:        "all",
		KnowledgeText: "All UPDATE statements must include a WHERE clause.",
	}, func(_ context.Context, _ string, _ string) (string, error) {
		failAttempts++
		return invalidJSON, nil
	})
	if failAttempts != 2 || !domain.IsCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("expected 2 attempts and 2011 AI_PROVIDER_FAILED, got attempts=%d err=%v", failAttempts, err)
	}
}

func TestCallEndpointJSONWithMeta_UsageAndErrors(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + "```json\\n{\\\"knowledge_text\\\":\\\"ok\\\"}\\n```" + `"}}],"usage":{"prompt_tokens":123,"completion_tokens":45}}`))
	}))
	defer srvOK.Close()

	hc := &provider.HTTPClient{Inner: srvOK.Client()}
	if _, err := callEndpointJSON(ctx, hc, provider.Endpoint{BaseURL: "://bad"}, "s", "u"); err == nil {
		t.Fatal("expected error for invalid base_url")
	}
	res, err := callEndpointsJSONWithMeta(ctx, hc, []provider.Endpoint{{
		ProviderID:    "prov-1",
		BaseURL:       srvOK.URL,
		ModelName:     "gpt-4o-mini",
		APIKey:        "sk-test",
		Configuration: map[string]any{"temperature": 0.2},
	}}, func(provider.Endpoint) (string, string, error) {
		return "sys", "user", nil
	})
	if err != nil {
		t.Fatalf("callEndpointsJSONWithMeta failed: %v", err)
	}
	if res.Content != `{"knowledge_text":"ok"}` || res.InputTokens != 123 || res.OutputTokens != 45 ||
		res.ProviderID != "prov-1" || res.ModelName != "gpt-4o-mini" || res.UsedSAN {
		t.Fatalf("unexpected ProviderJSONResult: %+v", res)
	}

	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srvErr.Close()
	if _, err := callEndpointJSON(ctx, &provider.HTTPClient{Inner: srvErr.Client()}, provider.Endpoint{BaseURL: srvErr.URL}, "s", "u"); err == nil {
		t.Fatal("expected error for 502 status")
	}

	srvEmptyChoices := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srvEmptyChoices.Close()
	if _, err := callEndpointJSON(ctx, &provider.HTTPClient{Inner: srvEmptyChoices.Client()}, provider.Endpoint{BaseURL: srvEmptyChoices.URL}, "s", "u"); err == nil {
		t.Fatal("expected error for empty choices")
	}
}
