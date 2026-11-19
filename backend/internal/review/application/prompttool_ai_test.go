package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestGeneratePromptToolTestCases_NoSelectAndStrictContextAlignment(t *testing.T) {
	svc := &Services{}
	ctx := context.Background()

	skills := []struct {
		name         string
		description  string
		engine       string
		rule         string
		params       map[string]any
		wantCategory skillStatementCategory
	}{
		{
			name:         "builtin.ddl-table-comment-required",
			description:  "Require table comment on CREATE TABLE",
			engine:       "mysql",
			rule:         "Every CREATE TABLE statement must include a non-empty table COMMENT.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-column-comment-required",
			description:  "Require column comment on column definitions",
			engine:       "mysql",
			rule:         "Every column definition in CREATE TABLE or ALTER TABLE ADD COLUMN must include a non-empty COMMENT.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-primary-key-required",
			description:  "Require explicit PRIMARY KEY on CREATE TABLE",
			engine:       "mysql",
			rule:         "Every CREATE TABLE statement must define an explicit PRIMARY KEY.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-drop-truncate",
			description:  "Forbid destructive DROP TABLE and TRUNCATE TABLE",
			engine:       "mysql",
			rule:         "Forbid DROP TABLE, DROP DATABASE, and TRUNCATE TABLE statements.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-max-varchar-length",
			description:  "Limit VARCHAR length",
			engine:       "mysql",
			rule:         "VARCHAR length must not exceed {{max_length}} characters.",
			params:       map[string]any{"max_length": 200},
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-max-index-count",
			description:  "Limit index count per table",
			engine:       "mysql",
			rule:         "A single table must not define more than {{max_count}} secondary indexes.",
			params:       map[string]any{"max_count": 3},
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-max-index-columns",
			description:  "Limit columns per composite index",
			engine:       "mysql",
			rule:         "A composite index must not contain more than {{max_columns}} columns.",
			params:       map[string]any{"max_columns": 3},
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-foreign-key",
			description:  "Forbid FOREIGN KEY constraints",
			engine:       "mysql",
			rule:         "Do not define FOREIGN KEY ... REFERENCES constraints in DDL statements.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-text-blob",
			description:  "Forbid TEXT and BLOB columns",
			engine:       "mysql",
			rule:         "Do not use TEXT, MEDIUMTEXT, LONGTEXT, or BLOB column types.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-float-double",
			description:  "Use DECIMAL instead of FLOAT or DOUBLE",
			engine:       "mysql",
			rule:         "Do not use FLOAT or DOUBLE types; use DECIMAL instead.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.dml-require-where",
			description:  "Require explicit WHERE clause on UPDATE and DELETE",
			engine:       "mysql",
			rule:         "Every UPDATE and DELETE statement must include a non-tautological WHERE clause.",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-insert-explicit-columns",
			description:  "Require explicit column list on INSERT",
			engine:       "mysql",
			rule:         "Every INSERT INTO statement must explicitly specify target column names.",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-forbid-insert-select",
			description:  "Forbid INSERT INTO ... SELECT",
			engine:       "mysql",
			rule:         "Do not use INSERT INTO ... SELECT statements; use explicit VALUES.",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-max-insert-rows",
			description:  "Limit batch INSERT rows",
			engine:       "mysql",
			rule:         "A single batch INSERT statement must not exceed {{max_rows}} rows.",
			params:       map[string]any{"max_rows": 3},
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-forbid-limit-dml",
			description:  "Forbid LIMIT or ORDER BY inside UPDATE and DELETE",
			engine:       "mysql",
			rule:         "Do not use ORDER BY or LIMIT clauses inside UPDATE or DELETE statements.",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-forbid-subquery",
			description:  "Forbid subqueries in DML WHERE clause",
			engine:       "mysql",
			rule:         "Do not use nested subqueries in DML WHERE clauses.",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.ddl-pk-named-id",
			description:  "Primary key column must be named id",
			engine:       "mysql",
			rule:         "Primary key column must be named id.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-pk-integer",
			description:  "Primary key column must use integer type",
			engine:       "mysql",
			rule:         "ddl_pk_is_integer",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-pk-auto-increment",
			description:  "Primary key column must declare AUTO_INCREMENT",
			engine:       "mysql",
			rule:         "ddl_has_auto_increment",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-autoincrement-unsigned",
			description:  "Auto-increment column must declare UNSIGNED",
			engine:       "mysql",
			rule:         "ddl_autoincrement_unsigned",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-column-nullable-check",
			description:  "Columns must declare NOT NULL",
			engine:       "mysql",
			rule:         "ddl_not_null_count",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-column-default-check",
			description:  "Columns must declare DEFAULT value",
			engine:       "mysql",
			rule:         "ddl_default_count",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-max-table-name-length",
			description:  "Table name length limit",
			engine:       "mysql",
			rule:         "ddl_table_name_length",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-identifier-keyword",
			description:  "Forbid SQL reserved keywords as identifiers",
			engine:       "mysql",
			rule:         "ddl_table_identifier_not_keyword",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-across-db-rename",
			description:  "Forbid cross-database table rename",
			engine:       "mysql",
			rule:         "ddl_across_db_rename",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-charset-allowlist",
			description:  "Table charset allowlist",
			engine:       "mysql",
			rule:         "ddl_charset",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-collation-allowlist",
			description:  "Table collation allowlist",
			engine:       "mysql",
			rule:         "ddl_collation",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-drop-database",
			description:  "Forbid DROP DATABASE",
			engine:       "mysql",
			rule:         "ddl_drop_target=database",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-drop-other",
			description:  "Forbid dropping view/index objects",
			engine:       "mysql",
			rule:         "ddl_drop_target other",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-drop-table",
			description:  "Forbid DROP TABLE",
			engine:       "mysql",
			rule:         "drop table is forbidden",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-column-type-change",
			description:  "Forbid modifying column type",
			engine:       "mysql",
			rule:         "ddl_alters_column_type",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-column-position-change",
			description:  "Forbid FIRST/AFTER column reordering",
			engine:       "mysql",
			rule:         "ddl_changes_column_position",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-special-type",
			description:  "Forbid special types",
			engine:       "mysql",
			rule:         "ddl_has_special_type",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-view-forbid-select-star",
			description:  "Forbid SELECT * in CREATE VIEW",
			engine:       "mysql",
			rule:         "ddl_view_select_star",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-allow-create-view",
			description:  "Block CREATE VIEW",
			engine:       "mysql",
			rule:         "ddl_is_create_view",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-allow-partition",
			description:  "Block PARTITION BY",
			engine:       "mysql",
			rule:         "ddl_has_partition",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-forbid-unnamed-index",
			description:  "Forbid unnamed indexes",
			engine:       "mysql",
			rule:         "ddl_index_unnamed_count",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-max-index-key-parts",
			description:  "Limit index key parts",
			engine:       "mysql",
			rule:         "ddl_index_max_key_parts",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-multi-alter",
			description:  "Merge multiple ALTER statements",
			engine:       "mysql",
			rule:         "ddl-multi-alter",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.ddl-multi-ddl",
			description:  "Single DDL per batch",
			engine:       "mysql",
			rule:         "ddl-multi-ddl",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "custom.ddl-generic-rule",
			description:  "Generic DDL governance check",
			engine:       "all",
			rule:         "General schema change governance check.",
			wantCategory: skillCategoryDDL,
		},
		{
			name:         "builtin.dml-forbid-order-by",
			description:  "Forbid ORDER BY in UPDATE/DELETE",
			engine:       "mysql",
			rule:         "dml_has_order_by",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-where-not-null-literal",
			description:  "Forbid = NULL in WHERE",
			engine:       "mysql",
			rule:         "where_literal_null",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-insert-forbid-null",
			description:  "Forbid NULL literal in INSERT",
			engine:       "mysql",
			rule:         "insert_has_null_literal",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "builtin.dml-max-in-value-count",
			description:  "Limit IN value count",
			engine:       "mysql",
			rule:         "in_value_count_max",
			wantCategory: skillCategoryDML,
		},
		{
			name:         "custom.dml-generic-rule",
			description:  "Generic DML mutation rule",
			engine:       "all",
			rule:         "General mutation governance rule.",
			wantCategory: skillCategoryDML,
		},
	}

	for _, tc := range skills {
		t.Run(tc.name, func(t *testing.T) {
			gotCategory := classifySkillCategory(tc.name, tc.description, tc.rule)
			if gotCategory != tc.wantCategory {
				t.Fatalf("classifySkillCategory() = %q, want %q", gotCategory, tc.wantCategory)
			}

			genOut, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{
				Name:          tc.name,
				Description:   tc.description,
				Engine:        tc.engine,
				Parameters:    tc.params,
				KnowledgeText: tc.rule,
			})
			if err != nil {
				t.Fatalf("GeneratePromptToolTestCases() error = %v", err)
			}
			if len(genOut.Cases) != 10 {
				t.Fatalf("len(Cases) = %d, want 10", len(genOut.Cases))
			}

			compliantCount := 0
			nonCompliantCount := 0
			for i, c := range genOut.Cases {
				upper := strings.ToUpper(strings.TrimSpace(c.SQL))
				if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") || strings.HasPrefix(upper, "SHOW") || strings.HasPrefix(upper, "EXPLAIN") {
					t.Fatalf("case[%d] (%s) generated forbidden standalone SELECT/query: %s", i, c.ID, c.SQL)
				}
				if !isStatementValidForCategory(c.SQL, tc.wantCategory) {
					t.Fatalf("case[%d] (%s) violated category %s: %s", i, c.ID, tc.wantCategory, c.SQL)
				}
				if tc.wantCategory == skillCategoryDDL && isStatementValidForCategory(c.SQL, skillCategoryDML) {
					t.Fatalf("DDL skill generated DML statement in case[%d]: %s", i, c.SQL)
				}
				if tc.wantCategory == skillCategoryDML && isStatementValidForCategory(c.SQL, skillCategoryDDL) {
					t.Fatalf("DML skill generated DDL statement in case[%d]: %s", i, c.SQL)
				}
				if c.ExpectedCompliant {
					compliantCount++
				} else {
					nonCompliantCount++
				}
			}
			if compliantCount != 5 || nonCompliantCount != 5 {
				t.Fatalf("expected 5 compliant + 5 non-compliant, got %d + %d", compliantCount, nonCompliantCount)
			}

			runOut, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
				Name:          tc.name,
				Description:   tc.description,
				Engine:        tc.engine,
				Parameters:    tc.params,
				KnowledgeText: tc.rule,
				Cases:         genOut.Cases,
			})
			if err != nil {
				t.Fatalf("RunPromptToolTest() error = %v", err)
			}
			if !runOut.Passed || runOut.MatchedCount != 10 {
				for _, r := range runOut.Results {
					if !r.MatchedExpectation {
						t.Errorf("mismatch on %s (expected=%v actual=%v): sql=%q summary=%q",
							r.ID, r.ExpectedCompliant, r.ActualCompliant, r.SQL, r.FindingSummary)
					}
				}
				t.Fatalf("RunPromptToolTest() passed=%v matched=%d/10", runOut.Passed, runOut.MatchedCount)
			}
		})
	}
}

func TestNormalizeAndFilterGeneratedTestCases_RejectsSelectAndCrossCategory(t *testing.T) {
	ddlFallback := buildDeterministicTestCases("builtin.ddl-table-comment-required", "", "mysql", "Require table comment", nil)
	rawAICases := []PromptToolTestCase{
		{ID: "case-1", SQL: "SELECT id, name FROM users WHERE id = 1;", ExpectedCompliant: true, Rationale: "Invalid SELECT"},
		{ID: "case-2", SQL: "WITH active_users AS (SELECT id FROM users) SELECT * FROM active_users;", ExpectedCompliant: true, Rationale: "Invalid CTE SELECT"},
		{ID: "case-3", SQL: "INSERT INTO users (id, name) VALUES (1, 'alice');", ExpectedCompliant: true, Rationale: "Invalid DML in DDL skill"},
		{ID: "case-4", SQL: "UPDATE users SET name = 'bob' WHERE id = 1;", ExpectedCompliant: false, Rationale: "Invalid DML in DDL skill"},
		{ID: "case-5", SQL: "CREATE TABLE t_valid_custom (id BIGINT NOT NULL, PRIMARY KEY (id)) ENGINE=InnoDB COMMENT='Valid custom table';", ExpectedCompliant: true, Rationale: "Valid DDL"},
	}

	merged, ok := normalizeAndFilterGeneratedTestCases(rawAICases, skillCategoryDDL, ddlFallback)
	if !ok {
		t.Fatalf("normalizeAndFilterGeneratedTestCases returned ok=false")
	}
	if len(merged) != 10 {
		t.Fatalf("len(merged) = %d, want 10", len(merged))
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

	// Test normalizeAndFilterGeneratedTestCases when empty rationale is filled and when insufficient cases fail.
	noRationaleCases := []PromptToolTestCase{
		{SQL: "CREATE TABLE t1 (id BIGINT PRIMARY KEY);", ExpectedCompliant: true},
		{SQL: "DROP TABLE t1;", ExpectedCompliant: false},
	}
	if _, ok := normalizeAndFilterGeneratedTestCases(noRationaleCases, skillCategoryDDL, nil); ok {
		t.Fatal("expected ok=false when fewer than 5+5 cases and no fallback")
	}
	mergedWithEmptyRationale, ok := normalizeAndFilterGeneratedTestCases(noRationaleCases, skillCategoryDDL, ddlFallback)
	if !ok || mergedWithEmptyRationale[0].Rationale == "" || mergedWithEmptyRationale[5].Rationale == "" {
		t.Fatal("expected default rationale to be filled")
	}
	if isStatementValidForCategory("   ", skillCategoryDDL) {
		t.Fatal("expected false for blank SQL")
	}
	if isStatementValidForCategory("CREATE TABLE t (id INT);", skillCategoryDML) {
		t.Fatal("expected false for DDL in DML category")
	}
}

func TestOptimizePromptToolPrompt_AndEndpointJSON(t *testing.T) {
	svc := &Services{}
	ctx := context.Background()

	// Validation errors on OptimizePromptToolPrompt, GeneratePromptToolTestCases, RunPromptToolTest.
	if _, err := svc.OptimizePromptToolPrompt(ctx, OptimizePromptToolInput{KnowledgeText: "   "}); err == nil {
		t.Fatal("expected error for empty knowledge_text")
	}
	if _, err := svc.OptimizePromptToolPrompt(ctx, OptimizePromptToolInput{KnowledgeText: "rule", Engine: "invalid"}); err == nil {
		t.Fatal("expected error for invalid engine")
	}
	if _, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{KnowledgeText: ""}); err == nil {
		t.Fatal("expected error for empty knowledge_text in GeneratePromptToolTestCases")
	}
	if _, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{KnowledgeText: "rule", Engine: "bad"}); err == nil {
		t.Fatal("expected error for invalid engine in GeneratePromptToolTestCases")
	}
	if _, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{KnowledgeText: ""}); err == nil {
		t.Fatal("expected error for empty knowledge_text in RunPromptToolTest")
	}
	if _, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{KnowledgeText: "rule"}); err == nil {
		t.Fatal("expected error for empty cases in RunPromptToolTest")
	}
	if _, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
		KnowledgeText: "rule",
		Cases:         []PromptToolTestCase{{ID: "", SQL: "UPDATE t SET a=1"}},
	}); err == nil {
		t.Fatal("expected error for empty case ID in RunPromptToolTest")
	}
	if _, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
		KnowledgeText: "rule",
		Engine:        "bad",
		Cases:         []PromptToolTestCase{{ID: "c1", SQL: "UPDATE t SET a=1"}},
	}); err == nil {
		t.Fatal("expected error for invalid engine in RunPromptToolTest")
	}

	// OptimizePromptToolPrompt deterministic fallback with missing parameter placeholder & DML category.
	optOut, err := svc.OptimizePromptToolPrompt(ctx, OptimizePromptToolInput{
		Name:          "",
		Description:   "Max rows check",
		Engine:        "",
		Parameters:    map[string]any{"max_rows": 100, "extra_param": 10},
		KnowledgeText: "Limit batch insert rows to {{max_rows}} in DML statements.",
	})
	if err != nil {
		t.Fatalf("OptimizePromptToolPrompt failed: %v", err)
	}
	if !strings.Contains(optOut.KnowledgeText, "{{max_rows}}") || !strings.Contains(optOut.KnowledgeText, "{{extra_param}}") {
		t.Fatalf("expected all placeholders preserved in optimized prompt, got: %s", optOut.KnowledgeText)
	}
	if hasAllPlaceholders("text without placeholder", []string{"{{missing}}"}) {
		t.Fatal("expected hasAllPlaceholders to return false when placeholder is missing")
	}

	// CallActiveProviderJSON on nil/unconfigured Services.
	if _, err := svc.CallActiveProviderJSON(ctx, "sys", "user"); err == nil {
		t.Fatal("expected error when provider chain is unavailable")
	}

	// Test callEndpointJSON directly with httptest.NewServer and custom HTTPClient!
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"knowledge_text\\\":\\\"ok\\\"}\\n```\"}}]}"))
	}))
	defer srvOK.Close()

	hc := &provider.HTTPClient{Inner: srvOK.Client()}
	if _, err := callEndpointJSON(ctx, hc, provider.Endpoint{BaseURL: "://bad"}, "s", "u"); err == nil {
		t.Fatal("expected error for invalid base_url")
	}
	gotJSON, err := callEndpointJSON(ctx, hc, provider.Endpoint{
		BaseURL:       srvOK.URL,
		ModelName:     "gpt-4o-mini",
		APIKey:        "sk-test",
		Configuration: map[string]any{"temperature": 0.2},
	}, "sys", "user")
	if err != nil || gotJSON != `{"knowledge_text":"ok"}` {
		t.Fatalf("unexpected callEndpointJSON result: got=%q err=%v", gotJSON, err)
	}

	// Also exercise remaining profiles (ddl-autoincrement-init-one, ddl-index-name-prefix, ddl-implicit-type-conversion, etc.).
	for _, p := range []struct {
		name string
		rule string
	}{
		{name: "builtin.ddl-autoincrement-init-one", rule: "ddl_autoincrement_init_one"},
		{name: "builtin.ddl-index-name-prefix", rule: "ddl_index_prefix_conformant"},
		{name: "custom.implicit-type-conversion", rule: "ddl-implicit-type-conversion"},
		{name: "builtin.ddl-require-table-comment", rule: "ddl_table_comment_present"},
		{name: "builtin.ddl-require-column-comments", rule: "ddl_column_comment_count"},
		{name: "builtin.ddl-max-char-length", rule: "ddl_max_char_length"},
		{name: "builtin.forbid-truncate", rule: "op=truncate"},
		{name: "builtin.ddl-drop-target-table", rule: "ddl_drop_target"},
		{name: "custom.neutral-rule", rule: "enforce naming standard"},
	} {
		gen, err := svc.GeneratePromptToolTestCases(ctx, GeneratePromptToolTestCasesInput{
			Name:          p.name,
			KnowledgeText: p.rule,
		})
		if err != nil || len(gen.Cases) != 10 {
			t.Fatalf("GeneratePromptToolTestCases(%s) failed: err=%v", p.name, err)
		}
		if _, err := svc.RunPromptToolTest(ctx, RunPromptToolTestInput{
			Name:          p.name,
			KnowledgeText: p.rule,
			Cases:         gen.Cases,
		}); err != nil {
			t.Fatalf("RunPromptToolTest(%s) failed: err=%v", p.name, err)
		}
	}

	// Test non-2xx and invalid JSON responses in callEndpointJSON.
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
