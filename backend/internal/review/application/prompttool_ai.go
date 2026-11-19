// prompttool_ai.go AI-assisted prompt optimization, 10-case SQL test generation
// (5 expected compliant + 5 expected non-compliant), and skill test execution for
// admin review skills (Prompt Tools).
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// OptimizePromptToolInput holds the draft skill fields for AI prompt optimization.
type OptimizePromptToolInput struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Engine        string         `json:"engine"`
	Parameters    map[string]any `json:"parameters"`
	KnowledgeText string         `json:"knowledge_text"`
}

// OptimizePromptToolOutput holds the optimized skill prompt text.
type OptimizePromptToolOutput struct {
	KnowledgeText string `json:"knowledge_text"`
}

// PromptToolTestCase represents a single SQL test case for verifying a review skill.
type PromptToolTestCase struct {
	ID                string `json:"id"`
	SQL               string `json:"sql"`
	ExpectedCompliant bool   `json:"expected_compliant"`
	Rationale         string `json:"rationale"`
}

// GeneratePromptToolTestCasesInput holds the skill definition for generating 10 SQL test cases.
type GeneratePromptToolTestCasesInput struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Engine        string         `json:"engine"`
	Parameters    map[string]any `json:"parameters"`
	KnowledgeText string         `json:"knowledge_text"`
}

// GeneratePromptToolTestCasesOutput holds the generated 10 SQL test cases.
type GeneratePromptToolTestCasesOutput struct {
	Cases []PromptToolTestCase `json:"cases"`
}

// RunPromptToolTestInput holds the skill definition and SQL test cases to evaluate.
type RunPromptToolTestInput struct {
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Engine        string               `json:"engine"`
	Parameters    map[string]any       `json:"parameters"`
	KnowledgeText string               `json:"knowledge_text"`
	Cases         []PromptToolTestCase `json:"cases"`
}

// PromptToolTestCaseResult holds the evaluation result for a single SQL test case.
type PromptToolTestCaseResult struct {
	ID                 string `json:"id"`
	SQL                string `json:"sql"`
	ExpectedCompliant  bool   `json:"expected_compliant"`
	ActualCompliant    bool   `json:"actual_compliant"`
	MatchedExpectation bool   `json:"matched_expectation"`
	FindingSummary     string `json:"finding_summary"`
}

// RunPromptToolTestOutput holds the aggregated skill test evaluation results.
type RunPromptToolTestOutput struct {
	Results      []PromptToolTestCaseResult `json:"results"`
	TotalCount   int                        `json:"total_count"`
	MatchedCount int                        `json:"matched_count"`
	Passed       bool                       `json:"passed"`
}

type skillStatementCategory string

const (
	skillCategoryDDL skillStatementCategory = "ddl"
	skillCategoryDML skillStatementCategory = "dml"
)

// OptimizePromptToolPrompt rewrites and structures a skill prompt using the active AI provider chain,
// falling back to deterministic rule structuring when no upstream provider is reachable.
func (s *Services) OptimizePromptToolPrompt(ctx context.Context, in OptimizePromptToolInput) (*OptimizePromptToolOutput, error) {
	rawText := strings.TrimSpace(in.KnowledgeText)
	if rawText == "" || len(rawText) > 64*1024 {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	engine := in.Engine
	if engine == "" {
		engine = "all"
	}
	if !ValidPromptToolEngine(engine) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	params := normalizeParams(in.Parameters)
	placeholders := collectParamPlaceholders(rawText, params)
	category := classifySkillCategory(in.Name, in.Description, rawText)

	sysPrompt := "You are an expert database governance and SQL review rule architect. " +
		"Rewrite the user's SQL review skill prompt into a clear, unambiguous, high-precision audit rule specification.\n" +
		"Mandatory requirements:\n" +
		"1. Write the output in the exact same human language as the user's input knowledge_text.\n" +
		"2. Yearning SQL review ONLY audits database change statements (DDL or DML). Never include standalone SELECT queries in the rule scope.\n" +
		"3. Keep the statement scope strictly aligned with statement_category (" + string(category) + "): " +
		"if statement_category is 'ddl', scope the rule strictly to the relevant DDL statements (e.g. CREATE TABLE, ALTER TABLE, DROP, CREATE INDEX, TRUNCATE) and never mention DML (INSERT/UPDATE/DELETE) or SELECT; " +
		"if statement_category is 'dml', scope the rule strictly to the relevant DML statements (INSERT, UPDATE, DELETE) and never mention DDL or standalone SELECT.\n" +
		"4. Clearly state the audit rule objective, target statement types, pass criteria, and violation trigger conditions.\n" +
		"5. Strictly preserve every {{parameter_name}} placeholder exactly as written without renaming or removing any placeholder.\n" +
		"6. Do not include meta prompt-injection phrases or markdown code fences around the JSON output.\n" +
		"7. Respond strictly with a JSON object of shape {\"knowledge_text\": \"...\"}."

	userPayload, _ := json.Marshal(map[string]any{
		"name":                  strings.TrimSpace(in.Name),
		"description":           strings.TrimSpace(in.Description),
		"engine":                engine,
		"statement_category":    string(category),
		"required_placeholders": placeholders,
		"knowledge_text":        rawText,
	})

	if rawResp, err := s.callActiveProviderJSON(ctx, sysPrompt, string(userPayload)); err == nil {
		var parsed struct {
			KnowledgeText string `json:"knowledge_text"`
		}
		if json.Unmarshal([]byte(rawResp), &parsed) == nil {
			candidate := strings.TrimSpace(parsed.KnowledgeText)
			if candidate != "" && len(candidate) <= 64*1024 && hasAllPlaceholders(candidate, placeholders) {
				return &OptimizePromptToolOutput{KnowledgeText: candidate}, nil
			}
		}
	}

	return &OptimizePromptToolOutput{
		KnowledgeText: buildDeterministicOptimizedPrompt(in.Name, in.Description, engine, category, rawText, placeholders),
	}, nil
}

// GeneratePromptToolTestCases generates 10 SQL test cases (5 expected compliant, 5 expected non-compliant)
// for testing whether the skill prompt behaves as expected.
func (s *Services) GeneratePromptToolTestCases(ctx context.Context, in GeneratePromptToolTestCasesInput) (*GeneratePromptToolTestCasesOutput, error) {
	rawText := strings.TrimSpace(in.KnowledgeText)
	if rawText == "" || len(rawText) > 64*1024 {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	engine := in.Engine
	if engine == "" {
		engine = "all"
	}
	if !ValidPromptToolEngine(engine) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	params := normalizeParams(in.Parameters)
	renderedRule := agent.RenderParameters(rawText, params)
	category := classifySkillCategory(in.Name, in.Description, renderedRule)
	fallbackCases := buildDeterministicTestCases(in.Name, in.Description, engine, renderedRule, params)

	categoryInstruction := "This skill is a DDL rule. ALL 10 SQL statements (both the 5 compliant cases and the 5 non-compliant cases) MUST be DDL statements (such as CREATE TABLE, ALTER TABLE, CREATE INDEX, DROP TABLE, TRUNCATE TABLE, RENAME TABLE, CREATE VIEW) that directly test this specific DDL rule. NEVER generate SELECT, INSERT, UPDATE, or DELETE statements."
	if category == skillCategoryDML {
		categoryInstruction = "This skill is a DML rule. ALL 10 SQL statements (both the 5 compliant cases and the 5 non-compliant cases) MUST be DML write statements (INSERT, UPDATE, or DELETE) that directly test this specific DML rule. NEVER generate standalone SELECT queries or DDL statements (CREATE, ALTER, DROP, TRUNCATE)."
	}

	sysPrompt := "You are an expert SQL audit quality engineer.\n" +
		"Given a SQL review skill rule, generate exactly 10 realistic SQL change statements to test the skill:\n" +
		"- Cases 1 to 5 MUST have expected_compliant = true (SQL statements of the exact target statement type that satisfy the skill rule).\n" +
		"- Cases 6 to 10 MUST have expected_compliant = false (SQL statements of the exact target statement type that violate the skill rule).\n" +
		"- STRICT PROHIBITION: Standalone SELECT queries (SELECT ... FROM ..., WITH ... SELECT ..., SHOW ..., EXPLAIN ...) are strictly forbidden and must NEVER appear in any test case.\n" +
		"- STRICT CONTEXT ALIGNMENT: " + categoryInstruction + "\n" +
		"- Write the rationale field in the same human language as rendered_rule.\n" +
		"- Tailor SQL dialect syntax to the target engine (mysql, postgresql, or standard SQL when all).\n" +
		"Respond strictly with a JSON object of shape:\n" +
		"{\"cases\": [{\"id\": \"case-1\", \"sql\": \"...\", \"expected_compliant\": true, \"rationale\": \"...\"}, ...]}"

	userPayload, _ := json.Marshal(map[string]any{
		"name":               strings.TrimSpace(in.Name),
		"description":        strings.TrimSpace(in.Description),
		"engine":             engine,
		"statement_category": string(category),
		"rendered_rule":      renderedRule,
	})

	if rawResp, err := s.callActiveProviderJSON(ctx, sysPrompt, string(userPayload)); err == nil {
		var parsed struct {
			Cases []PromptToolTestCase `json:"cases"`
		}
		if json.Unmarshal([]byte(rawResp), &parsed) == nil && len(parsed.Cases) > 0 {
			if merged, ok := normalizeAndFilterGeneratedTestCases(parsed.Cases, category, fallbackCases); ok {
				return &GeneratePromptToolTestCasesOutput{Cases: merged}, nil
			}
		}
	}

	return &GeneratePromptToolTestCasesOutput{
		Cases: fallbackCases,
	}, nil
}

// RunPromptToolTest evaluates the provided SQL test cases against the skill definition and compares
// actual compliance verdicts with expected_compliant.
func (s *Services) RunPromptToolTest(ctx context.Context, in RunPromptToolTestInput) (*RunPromptToolTestOutput, error) {
	rawText := strings.TrimSpace(in.KnowledgeText)
	if rawText == "" || len(rawText) > 64*1024 {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if len(in.Cases) == 0 || len(in.Cases) > 20 {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	for i := range in.Cases {
		if strings.TrimSpace(in.Cases[i].ID) == "" || strings.TrimSpace(in.Cases[i].SQL) == "" {
			return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
	}
	engine := in.Engine
	if engine == "" {
		engine = "all"
	}
	if !ValidPromptToolEngine(engine) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	params := normalizeParams(in.Parameters)
	renderedRule := agent.RenderParameters(rawText, params)

	sysPrompt := "You are an automated SQL review engine evaluating SQL change statements against a single mandatory audit skill rule.\n" +
		"For each input case, determine strictly whether the SQL statement complies with the rendered_rule:\n" +
		"- Set actual_compliant = true if the SQL statement satisfies the rule.\n" +
		"- Set actual_compliant = false if the SQL statement violates the rule.\n" +
		"- Provide a concise finding_summary in the same human language as rendered_rule explaining why it passed or violated the rule.\n" +
		"Respond strictly with a JSON object of shape:\n" +
		"{\"results\": [{\"id\": \"case-1\", \"actual_compliant\": true, \"finding_summary\": \"...\"}, ...]}"

	type evalCaseInput struct {
		ID  string `json:"id"`
		SQL string `json:"sql"`
	}
	evalInputs := make([]evalCaseInput, len(in.Cases))
	for i, c := range in.Cases {
		evalInputs[i] = evalCaseInput{ID: c.ID, SQL: c.SQL}
	}
	userPayload, _ := json.Marshal(map[string]any{
		"name":          strings.TrimSpace(in.Name),
		"description":   strings.TrimSpace(in.Description),
		"engine":        engine,
		"rendered_rule": renderedRule,
		"cases":         evalInputs,
	})

	var aiVerdicts map[string]struct {
		ActualCompliant bool
		FindingSummary  string
	}
	if rawResp, err := s.callActiveProviderJSON(ctx, sysPrompt, string(userPayload)); err == nil {
		var parsed struct {
			Results []struct {
				ID              string `json:"id"`
				ActualCompliant bool   `json:"actual_compliant"`
				FindingSummary  string `json:"finding_summary"`
			} `json:"results"`
		}
		if json.Unmarshal([]byte(rawResp), &parsed) == nil && len(parsed.Results) > 0 {
			aiVerdicts = make(map[string]struct {
				ActualCompliant bool
				FindingSummary  string
			}, len(parsed.Results))
			for _, r := range parsed.Results {
				if strings.TrimSpace(r.ID) != "" && strings.TrimSpace(r.FindingSummary) != "" {
					aiVerdicts[r.ID] = struct {
						ActualCompliant bool
						FindingSummary  string
					}{
						ActualCompliant: r.ActualCompliant,
						FindingSummary:  strings.TrimSpace(r.FindingSummary),
					}
				}
			}
		}
	}

	results := make([]PromptToolTestCaseResult, len(in.Cases))
	matchedCount := 0
	for i, tc := range in.Cases {
		var actualCompliant bool
		var summary string
		if v, ok := aiVerdicts[tc.ID]; ok {
			actualCompliant = v.ActualCompliant
			summary = v.FindingSummary
		} else {
			actualCompliant, summary = evaluateSQLAgainstRuleDeterministic(tc.SQL, in.Name, in.Description, renderedRule, params)
		}
		matched := actualCompliant == tc.ExpectedCompliant
		if matched {
			matchedCount++
		}
		results[i] = PromptToolTestCaseResult{
			ID:                 tc.ID,
			SQL:                tc.SQL,
			ExpectedCompliant:  tc.ExpectedCompliant,
			ActualCompliant:    actualCompliant,
			MatchedExpectation: matched,
			FindingSummary:     summary,
		}
	}

	return &RunPromptToolTestOutput{
		Results:      results,
		TotalCount:   len(results),
		MatchedCount: matchedCount,
		Passed:       len(results) > 0 && matchedCount == len(results),
	}, nil
}

// CallActiveProviderJSON invokes the active AI provider chain with a system prompt and JSON-oriented user payload,
// returning the stripped JSON string from the model completion.
func (s *Services) CallActiveProviderJSON(ctx context.Context, sysPrompt, userContent string) (string, error) {
	return s.callActiveProviderJSON(ctx, sysPrompt, userContent)
}

func (s *Services) callActiveProviderJSON(ctx context.Context, sysPrompt, userContent string) (string, error) {
	content, _, err := s.CallActiveProviderJSONPerEndpoint(ctx, func(_ provider.Endpoint) (string, string, error) {
		return sysPrompt, userContent, nil
	})
	return content, err
}

// CallActiveProviderJSONPerEndpoint invokes the active AI provider chain, constructing prompts/payloads
// from source per target endpoint so heterogeneous chains (e.g., san vs default) remain strictly isolated.
func (s *Services) CallActiveProviderJSONPerEndpoint(
	ctx context.Context,
	build func(ep provider.Endpoint) (sysPrompt string, userContent string, err error),
) (string, bool, error) {
	if s == nil || s.DB == nil || s.Crypter == nil {
		return "", false, errors.New("review: provider chain unavailable")
	}
	if build == nil {
		return "", false, errors.New("review: nil endpoint prompt builder")
	}
	chain, err := s.LoadProviderChain(ctx, s.DB, func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewOpenAIAdapter(ep, nil)
	})
	if err != nil || chain == nil {
		return "", false, errors.New("review: no active provider")
	}
	return callEndpointsJSON(ctx, provider.NewHTTPClient(), chain.Endpoints, build)
}

func callEndpointsJSON(
	ctx context.Context,
	hc *provider.HTTPClient,
	eps []provider.Endpoint,
	build func(ep provider.Endpoint) (sysPrompt string, userContent string, err error),
) (string, bool, error) {
	if len(eps) == 0 {
		return "", false, errors.New("review: empty provider chain")
	}
	if build == nil {
		return "", false, errors.New("review: nil endpoint prompt builder")
	}
	if hc == nil {
		hc = provider.NewHTTPClient()
	}
	var lastErr error
	for _, ep := range eps {
		sysPrompt, userContent, berr := build(ep)
		if berr != nil {
			lastErr = berr
			continue
		}
		content, callErr := callEndpointJSON(ctx, hc, ep, sysPrompt, userContent)
		if callErr == nil && strings.TrimSpace(content) != "" {
			return content, ep.EffectivePromptProfile() == promptprofile.ProfileSAN, nil
		}
		lastErr = callErr
	}
	if lastErr == nil {
		lastErr = errors.New("review: provider call failed")
	}
	return "", false, lastErr
}

func callEndpointJSON(ctx context.Context, hc *provider.HTTPClient, ep provider.Endpoint, sysPrompt, userContent string) (string, error) {
	u, err := provider.ParseBaseURL(ep.BaseURL)
	if err != nil {
		return "", fmt.Errorf("review: invalid provider base_url: %w", err)
	}
	endpointURL := strings.TrimRight(u.String(), "/") + "/chat/completions"
	if ep.EffectivePromptProfile() != promptprofile.ProfileSAN {
		if !strings.Contains(strings.ToLower(sysPrompt), "json") {
			sysPrompt += "\nRespond with a valid json object."
		}
		if !strings.Contains(strings.ToLower(userContent), "json") {
			userContent += "\n\nRespond with a valid json object."
		}
	}
	promptMsgs := []provider.PromptMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userContent},
	}
	effMax := provider.ResolveMaxTokens(ep.Configuration, promptMsgs)
	reqBody := map[string]any{
		"model": ep.ModelName,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": userContent},
		},
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	if effMax > 0 {
		reqBody["max_tokens"] = effMax
	}
	if ep.Configuration != nil {
		if v, ok := ep.Configuration["temperature"].(float64); ok {
			reqBody["temperature"] = v
		}
		if v, ok := ep.Configuration["reasoning_effort"].(string); ok && v != "" {
			reqBody["reasoning_effort"] = v
		}
	}
	if hc == nil {
		hc = provider.NewHTTPClient()
	}

	var resp *http.Response
	healedSentMax := 0
	healedNextMax := 0
	healedMaxCtx := 0
	healedPromptTokens := 0
	for attempt := 0; attempt < 2; attempt++ {
		rawReq, err := json.Marshal(reqBody)
		if err != nil {
			return "", err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(rawReq))
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+ep.APIKey)

		resp, err = hc.Inner.Do(httpReq)
		if err != nil {
			if healedSentMax > 0 {
				provider.RecordOverflowHeal(provider.OverflowHealOutcomeFastFail, "prompttool_ai", ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
			}
			return "", err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if healedSentMax > 0 {
				outcome := provider.OverflowHealOutcomeHealed
				if healedNextMax == 0 {
					outcome = provider.OverflowHealOutcomeFallbackNoMaxTokens
				}
				provider.RecordOverflowHeal(outcome, "prompttool_ai", ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
			}
			break
		}
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		statusErr := &provider.HTTPStatusError{
			Code:     resp.StatusCode,
			Snapshot: provider.UpstreamSnapshot(errBody),
			RawBody:  strings.TrimSpace(string(errBody)),
		}
		if attempt == 0 {
			rawErr := string(errBody)
			retry, nextMax, promptTooLong := provider.ResolveContextOverflowRetry(effMax, resp.StatusCode, rawErr)
			maxCtx, promptTokens, _ := provider.ParseContextLengthOverflow(rawErr)
			if promptTooLong {
				provider.RecordOverflowHeal(provider.OverflowHealOutcomeFastFail, "prompttool_ai", ep.ModelName, effMax, 0, maxCtx, promptTokens)
				return "", fmt.Errorf("review: prompt exceeds model context window: %w", statusErr)
			}
			if retry {
				healedSentMax = effMax
				healedNextMax = nextMax
				healedMaxCtx = maxCtx
				healedPromptTokens = promptTokens
				if nextMax > 0 {
					reqBody["max_tokens"] = nextMax
				} else {
					delete(reqBody, "max_tokens")
				}
				effMax = nextMax
				continue
			}
		} else if healedSentMax > 0 {
			provider.RecordOverflowHeal(provider.OverflowHealOutcomeFastFail, "prompttool_ai", ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
		}
		return "", statusErr
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, provider.MaxResponseByte+1))
	if err != nil {
		return "", err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", errors.New("review: invalid completion response")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content), nil
}

var skillParamPlaceholderRe = regexp.MustCompile(`\{\{[a-z][a-z0-9_]{0,31}\}\}`)

func collectParamPlaceholders(text string, params map[string]any) []string {
	seen := map[string]struct{}{}
	for k := range params {
		seen["{{"+k+"}}"] = struct{}{}
	}
	matches := skillParamPlaceholderRe.FindAllString(text, -1)
	for _, m := range matches {
		seen[m] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasAllPlaceholders(text string, placeholders []string) bool {
	for _, ph := range placeholders {
		if !strings.Contains(text, ph) {
			return false
		}
	}
	return true
}

func classifySkillCategory(name, description, ruleText string) skillStatementCategory {
	lowerName := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(lowerName, "dml") {
		return skillCategoryDML
	}
	if strings.Contains(lowerName, "ddl") || strings.Contains(lowerName, "truncate") || strings.Contains(lowerName, "drop") {
		return skillCategoryDDL
	}
	combined := strings.ToLower(name + " " + description + " " + ruleText)
	if strings.Contains(combined, "ddl-implicit-type-conversion") || strings.Contains(combined, "ddlimplicittypeconversion") {
		return skillCategoryDML
	}
	dmlKeywords := []string{
		"dml_",
		"unbounded_mutation",
		"insert_has_columns",
		"values_row_count_max",
		"where_literal_null",
		"insert_has_null_literal",
		"in_value_count_max",
		"update ",
		"update/",
		"delete ",
		"/delete",
		"insert ",
		"\u5b50\u67e5\u8be2",       // subquery
		"\u6279\u91cf\u884c\u6570", // batch rows
	}
	ddlKeywords := []string{
		"ddl_",
		"ddl",
		"create table",
		"alter table",
		"drop table",
		"drop database",
		"truncate",
		"rename table",
		"create view",
		"create index",
		"primary key",
		"foreign key",
		"auto_increment",
		"unsigned",
		"charset",
		"collate",
		"collation",
		"partition",
		"\u5efa\u8868",             // create table
		"\u8868\u540d",             // table name
		"\u8868\u6ce8\u91ca",       // table comment
		"\u5217\u6ce8\u91ca",       // column comment
		"\u4e3b\u952e",             // primary key
		"\u5916\u952e",             // foreign key
		"\u7d22\u5f15",             // index
		"\u5b57\u6bb5\u957f\u5ea6", // column length
		"\u5217\u7c7b\u578b",       // column type
		"\u5217\u4f4d\u7f6e",       // column position
		"\u9ed8\u8ba4\u503c",       // default value
		"\u53ef\u7a7a",             // nullable
		"\u81ea\u589e",             // auto increment
		"\u5b57\u7b26\u96c6",       // charset
		"\u6392\u5e8f\u89c4\u5219", // collation
		"\u5206\u533a",             // partition
		"\u89c6\u56fe",             // view
	}
	for _, kw := range ddlKeywords {
		if strings.Contains(combined, kw) {
			return skillCategoryDDL
		}
	}
	for _, kw := range dmlKeywords {
		if strings.Contains(combined, kw) {
			return skillCategoryDML
		}
	}
	return skillCategoryDDL
}

func isStatementValidForCategory(sqlText string, category skillStatementCategory) bool {
	upper := strings.ToUpper(strings.TrimSpace(sqlText))
	if upper == "" {
		return false
	}
	// Standalone SELECT or read-only / session statements are strictly forbidden in skill tests.
	for _, forbiddenPrefix := range []string{
		"SELECT ", "SELECT\t", "SELECT\n", "SELECT*",
		"WITH ", "SHOW ", "EXPLAIN ", "DESC ", "DESCRIBE ",
		"USE ", "SET ", "BEGIN", "COMMIT", "ROLLBACK",
	} {
		if strings.HasPrefix(upper, forbiddenPrefix) {
			return false
		}
	}
	if category == skillCategoryDDL {
		for _, ddlPrefix := range []string{
			"CREATE ", "ALTER ", "DROP ", "TRUNCATE ", "RENAME ", "COMMENT ON ",
		} {
			if strings.HasPrefix(upper, ddlPrefix) {
				return true
			}
		}
		return false
	}
	for _, dmlPrefix := range []string{
		"INSERT ", "UPDATE ", "DELETE ", "REPLACE ",
	} {
		if strings.HasPrefix(upper, dmlPrefix) {
			return true
		}
	}
	return false
}

func normalizeAndFilterGeneratedTestCases(
	cases []PromptToolTestCase,
	category skillStatementCategory,
	fallbackCases []PromptToolTestCase,
) ([]PromptToolTestCase, bool) {
	var compliant, nonCompliant []PromptToolTestCase
	for _, c := range cases {
		sqlText := strings.TrimSpace(c.SQL)
		if !isStatementValidForCategory(sqlText, category) {
			continue
		}
		rationale := strings.TrimSpace(c.Rationale)
		if rationale == "" {
			if c.ExpectedCompliant {
				rationale = "Complies with the skill rule."
			} else {
				rationale = "Violates the skill rule."
			}
		}
		item := PromptToolTestCase{
			SQL:               sqlText,
			ExpectedCompliant: c.ExpectedCompliant,
			Rationale:         rationale,
		}
		if c.ExpectedCompliant {
			if len(compliant) < 5 {
				compliant = append(compliant, item)
			}
		} else {
			if len(nonCompliant) < 5 {
				nonCompliant = append(nonCompliant, item)
			}
		}
	}
	for _, fb := range fallbackCases {
		if fb.ExpectedCompliant && len(compliant) < 5 {
			compliant = append(compliant, fb)
		} else if !fb.ExpectedCompliant && len(nonCompliant) < 5 {
			nonCompliant = append(nonCompliant, fb)
		}
	}
	if len(compliant) != 5 || len(nonCompliant) != 5 {
		return nil, false
	}
	out := make([]PromptToolTestCase, 0, 10)
	for i := 0; i < 5; i++ {
		c := compliant[i]
		c.ID = fmt.Sprintf("case-%d", i+1)
		out = append(out, c)
	}
	for i := 0; i < 5; i++ {
		c := nonCompliant[i]
		c.ID = fmt.Sprintf("case-%d", i+6)
		out = append(out, c)
	}
	return out, true
}

func buildDeterministicOptimizedPrompt(
	name, description, engine string,
	category skillStatementCategory,
	rawText string,
	placeholders []string,
) string {
	trimmed := strings.TrimSpace(rawText)
	var b strings.Builder
	title := strings.TrimSpace(name)
	if title == "" {
		title = "SQL Review Skill Rule"
	}
	scopeLabel := "DDL (CREATE / ALTER / DROP / TRUNCATE / RENAME)"
	if category == skillCategoryDML {
		scopeLabel = "DML (INSERT / UPDATE / DELETE)"
	}
	b.WriteString("[Rule Name] ")
	b.WriteString(title)
	if strings.TrimSpace(description) != "" {
		b.WriteString(" - ")
		b.WriteString(strings.TrimSpace(description))
	}
	b.WriteString("\n[Target Engine] ")
	b.WriteString(engine)
	b.WriteString("\n[Statement Scope] ")
	b.WriteString(scopeLabel)
	b.WriteString("\n\n[Audit Specification]\n")
	b.WriteString(trimmed)
	if len(placeholders) > 0 {
		var missing []string
		for _, ph := range placeholders {
			if !strings.Contains(trimmed, ph) {
				missing = append(missing, ph)
			}
		}
		if len(missing) > 0 {
			b.WriteString("\n\n[Threshold Parameters]\n")
			b.WriteString(strings.Join(missing, ", "))
		}
	}
	if !strings.Contains(strings.ToLower(trimmed), "judgment") && !strings.Contains(strings.ToLower(trimmed), "violation") {
		b.WriteString("\n\n[Judgment Criteria]\n")
		b.WriteString("1. Evaluate only statements within the declared Statement Scope above.\n")
		b.WriteString("2. Report a violation with the exact offending SQL clause and remediation suggestion whenever the rule condition is triggered.\n")
		b.WriteString("3. Pass statements that satisfy all constraints of the specification without false positives.")
	}
	return b.String()
}

type ruleProfile string

const (
	// DDL profiles
	profileDDLRequirePK            ruleProfile = "ddl_require_pk"
	profileDDLPKNamedID            ruleProfile = "ddl_pk_named_id"
	profileDDLPKInteger            ruleProfile = "ddl_pk_integer"
	profileDDLPKAutoIncrement      ruleProfile = "ddl_pk_auto_increment"
	profileDDLAutoIncUnsigned      ruleProfile = "ddl_autoincrement_unsigned"
	profileDDLAutoIncInitOne       ruleProfile = "ddl_autoincrement_init_one"
	profileDDLTableComment         ruleProfile = "ddl_require_table_comment"
	profileDDLColumnComments       ruleProfile = "ddl_require_column_comments"
	profileDDLColumnNullable       ruleProfile = "ddl_column_nullable"
	profileDDLColumnDefault        ruleProfile = "ddl_column_default"
	profileDDLMaxCharLength        ruleProfile = "ddl_max_char_length"
	profileDDLMaxTableNameLength   ruleProfile = "ddl_max_table_name_length"
	profileDDLIdentifierKeyword    ruleProfile = "ddl_identifier_keyword"
	profileDDLAcrossDBRename       ruleProfile = "ddl_across_db_rename"
	profileDDLCharsetAllowlist     ruleProfile = "ddl_charset_allowlist"
	profileDDLCollationAllowlist   ruleProfile = "ddl_collation_allowlist"
	profileDDLForbidDropTable      ruleProfile = "ddl_forbid_drop_table"
	profileDDLForbidDropDatabase   ruleProfile = "ddl_forbid_drop_database"
	profileDDLForbidDropOther      ruleProfile = "ddl_forbid_drop_other"
	profileDDLForbidTruncate       ruleProfile = "ddl_forbid_truncate"
	profileDDLForbidColTypeChange  ruleProfile = "ddl_forbid_column_type_change"
	profileDDLForbidColPosChange   ruleProfile = "ddl_forbid_column_position_change"
	profileDDLFloatDoubleDecimal   ruleProfile = "ddl_float_double_decimal"
	profileDDLSpecialType          ruleProfile = "ddl_special_type"
	profileDDLForbidForeignKey     ruleProfile = "ddl_forbid_foreign_key"
	profileDDLAllowCreateView      ruleProfile = "ddl_allow_create_view"
	profileDDLViewForbidSelectStar ruleProfile = "ddl_view_forbid_select_star"
	profileDDLAllowPartition       ruleProfile = "ddl_allow_partition"
	profileDDLForbidUnnamedIndex   ruleProfile = "ddl_forbid_unnamed_index"
	profileDDLIndexNamePrefix      ruleProfile = "ddl_index_name_prefix"
	profileDDLMaxIndexKeyParts     ruleProfile = "ddl_max_index_key_parts"
	profileDDLMaxIndexCount        ruleProfile = "ddl_max_index_count"
	profileDDLMultiAlter           ruleProfile = "ddl_multi_alter"
	profileDDLMultiDDL             ruleProfile = "ddl_multi_ddl"
	profileDDLGeneric              ruleProfile = "ddl_generic"

	// DML profiles
	profileDMLRequireWhere       ruleProfile = "dml_require_where"
	profileDMLForbidLimit        ruleProfile = "dml_forbid_limit"
	profileDMLForbidOrderBy      ruleProfile = "dml_forbid_order_by"
	profileDMLForbidSubselect    ruleProfile = "dml_forbid_subselect"
	profileDMLInsertExplicitCols ruleProfile = "dml_insert_explicit_cols"
	profileDMLInsertMaxBatchRows ruleProfile = "dml_insert_max_batch_rows"
	profileDMLWhereNotNullLit    ruleProfile = "dml_where_not_null_literal"
	profileDMLInsertForbidNull   ruleProfile = "dml_insert_forbid_null"
	profileDMLMaxInValues        ruleProfile = "dml_max_in_values"
	profileDMLImplicitConversion ruleProfile = "dml_implicit_conversion"
	profileDMLGeneric            ruleProfile = "dml_generic"
)

func detectRuleProfile(name, description, renderedRule string) ruleProfile {
	combined := strings.ToLower(name + " " + description + " " + renderedRule)
	switch {
	// Exact built-in or semantic matches for DML
	case strings.Contains(combined, "dml-forbid-limit") || strings.Contains(combined, "dml_has_limit") || (strings.Contains(combined, "limit") && (strings.Contains(combined, "update") || strings.Contains(combined, "delete"))):
		return profileDMLForbidLimit
	case strings.Contains(combined, "dml-forbid-order-by") || strings.Contains(combined, "dml_has_order_by") || (strings.Contains(combined, "order by") && (strings.Contains(combined, "update") || strings.Contains(combined, "delete"))):
		return profileDMLForbidOrderBy
	case strings.Contains(combined, "dml-forbid-subselect") || strings.Contains(combined, "dml_has_subselect") || strings.Contains(combined, "subquery") || strings.Contains(combined, "subselect") || strings.Contains(combined, "insert-select") || strings.Contains(combined, "insert into ... select") || strings.Contains(combined, "\u5b50\u67e5\u8be2"):
		return profileDMLForbidSubselect
	case strings.Contains(combined, "dml-insert-explicit-columns") || strings.Contains(combined, "insert_has_columns") || (strings.Contains(combined, "insert") && (strings.Contains(combined, "column") || strings.Contains(combined, "\u5217"))):
		return profileDMLInsertExplicitCols
	case strings.Contains(combined, "dml-insert-max-batch-rows") || strings.Contains(combined, "max-insert-rows") || strings.Contains(combined, "values_row_count_max") || strings.Contains(combined, "max_batch_rows") || strings.Contains(combined, "max_rows") || strings.Contains(combined, "batch insert"):
		return profileDMLInsertMaxBatchRows
	case strings.Contains(combined, "dml-where-not-null-literal") || strings.Contains(combined, "where_literal_null"):
		return profileDMLWhereNotNullLit
	case strings.Contains(combined, "dml-insert-forbid-null") || strings.Contains(combined, "insert_has_null_literal"):
		return profileDMLInsertForbidNull
	case strings.Contains(combined, "max-in-value-count") || strings.Contains(combined, "in_value_count_max"):
		return profileDMLMaxInValues
	case strings.Contains(combined, "ddl-implicit-type-conversion") || strings.Contains(combined, "ddlimplicittypeconversion") || strings.Contains(combined, "\u9690\u5f0f"):
		return profileDMLImplicitConversion
	case strings.Contains(combined, "dml-require-where") || strings.Contains(combined, "unbounded_mutation") || (strings.Contains(combined, "where") && (strings.Contains(combined, "update") || strings.Contains(combined, "delete"))):
		return profileDMLRequireWhere

	// Exact built-in or semantic matches for DDL
	case strings.Contains(combined, "ddl-pk-named-id") || strings.Contains(combined, "ddl_pk_named_id"):
		return profileDDLPKNamedID
	case strings.Contains(combined, "ddl-pk-integer") || strings.Contains(combined, "ddl_pk_is_integer"):
		return profileDDLPKInteger
	case strings.Contains(combined, "ddl-pk-auto-increment") || strings.Contains(combined, "ddl_has_auto_increment"):
		return profileDDLPKAutoIncrement
	case strings.Contains(combined, "ddl-autoincrement-unsigned") || strings.Contains(combined, "ddl_autoincrement_unsigned") || strings.Contains(combined, "unsigned"):
		return profileDDLAutoIncUnsigned
	case strings.Contains(combined, "ddl-autoincrement-init-one") || strings.Contains(combined, "ddl_autoincrement_init_one"):
		return profileDDLAutoIncInitOne
	case strings.Contains(combined, "ddl-require-primary-key") || strings.Contains(combined, "ddl_has_primary_key") || strings.Contains(combined, "primary key") || strings.Contains(combined, "\u4e3b\u952e"):
		return profileDDLRequirePK
	case strings.Contains(combined, "ddl-require-table-comment") || strings.Contains(combined, "ddl_table_comment_present") || strings.Contains(combined, "\u8868\u6ce8\u91ca"):
		return profileDDLTableComment
	case strings.Contains(combined, "ddl-require-column-comments") || strings.Contains(combined, "ddl_column_comment_count") || strings.Contains(combined, "\u5217\u6ce8\u91ca"):
		return profileDDLColumnComments
	case strings.Contains(combined, "ddl-column-nullable-check") || strings.Contains(combined, "ddl_not_null_count") || strings.Contains(combined, "not null") || strings.Contains(combined, "\u53ef\u7a7a"):
		return profileDDLColumnNullable
	case strings.Contains(combined, "ddl-column-default-check") || strings.Contains(combined, "ddl_default_count") || strings.Contains(combined, "default") || strings.Contains(combined, "\u9ed8\u8ba4\u503c"):
		return profileDDLColumnDefault
	case strings.Contains(combined, "ddl-max-char-length") || strings.Contains(combined, "ddl_max_char_length"):
		return profileDDLMaxCharLength
	case strings.Contains(combined, "ddl-max-table-name-length") || strings.Contains(combined, "ddl_table_name_length") || strings.Contains(combined, "\u8868\u540d\u957f\u5ea6"):
		return profileDDLMaxTableNameLength
	case strings.Contains(combined, "ddl-identifier-keyword") || strings.Contains(combined, "ddl_table_identifier_not_keyword") || strings.Contains(combined, "\u5173\u952e\u8bcd"):
		return profileDDLIdentifierKeyword
	case strings.Contains(combined, "ddl-forbid-across-db-rename") || strings.Contains(combined, "ddl_across_db_rename") || strings.Contains(combined, "rename table"):
		return profileDDLAcrossDBRename
	case strings.Contains(combined, "ddl-charset-allowlist") || strings.Contains(combined, "ddl_charset") || strings.Contains(combined, "\u5b57\u7b26\u96c6"):
		return profileDDLCharsetAllowlist
	case strings.Contains(combined, "ddl-collation-allowlist") || strings.Contains(combined, "ddl_collation") || strings.Contains(combined, "\u6392\u5e8f\u89c4\u5219"):
		return profileDDLCollationAllowlist
	case strings.Contains(combined, "ddl-forbid-drop-database") || strings.Contains(combined, "ddl_drop_target=database") || strings.Contains(combined, "drop database"):
		return profileDDLForbidDropDatabase
	case strings.Contains(combined, "ddl-forbid-drop-other") || strings.Contains(combined, "ddl_drop_target"):
		if strings.Contains(combined, "view/index") || strings.Contains(combined, "other") {
			return profileDDLForbidDropOther
		}
		return profileDDLForbidDropTable
	case strings.Contains(combined, "forbid-truncate") || strings.Contains(combined, "op=truncate") || strings.Contains(combined, "truncate"):
		return profileDDLForbidTruncate
	case strings.Contains(combined, "ddl-forbid-drop-table") || strings.Contains(combined, "drop table") || strings.Contains(combined, "drop"):
		return profileDDLForbidDropTable
	case strings.Contains(combined, "ddl-forbid-column-type-change") || strings.Contains(combined, "ddl_alters_column_type") || strings.Contains(combined, "\u5217\u7c7b\u578b"):
		return profileDDLForbidColTypeChange
	case strings.Contains(combined, "ddl-forbid-column-position-change") || strings.Contains(combined, "ddl_changes_column_position") || strings.Contains(combined, "\u5217\u4f4d\u7f6e"):
		return profileDDLForbidColPosChange
	case strings.Contains(combined, "ddl-float-double-decimal") || strings.Contains(combined, "ddl_has_float_double") || strings.Contains(combined, "float"):
		return profileDDLFloatDoubleDecimal
	case strings.Contains(combined, "ddl-special-type") || strings.Contains(combined, "ddl_has_special_type") || strings.Contains(combined, "\u7279\u6b8a\u7c7b\u578b"):
		return profileDDLSpecialType
	case strings.Contains(combined, "ddl-forbid-foreign-key") || strings.Contains(combined, "ddl_has_foreign_key") || strings.Contains(combined, "foreign key") || strings.Contains(combined, "\u5916\u952e"):
		return profileDDLForbidForeignKey
	case strings.Contains(combined, "ddl-view-forbid-select-star") || strings.Contains(combined, "ddl_view_select_star"):
		return profileDDLViewForbidSelectStar
	case strings.Contains(combined, "ddl-allow-create-view") || strings.Contains(combined, "ddl_is_create_view") || strings.Contains(combined, "create view"):
		return profileDDLAllowCreateView
	case strings.Contains(combined, "ddl-allow-partition") || strings.Contains(combined, "ddl_has_partition") || strings.Contains(combined, "partition"):
		return profileDDLAllowPartition
	case strings.Contains(combined, "ddl-forbid-unnamed-index") || strings.Contains(combined, "ddl_index_unnamed_count"):
		return profileDDLForbidUnnamedIndex
	case strings.Contains(combined, "ddl-index-name-prefix") || strings.Contains(combined, "ddl_index_prefix_conformant") || strings.Contains(combined, "idx_"):
		return profileDDLIndexNamePrefix
	case strings.Contains(combined, "ddl-max-index-key-parts") || strings.Contains(combined, "ddl_index_max_key_parts"):
		return profileDDLMaxIndexKeyParts
	case strings.Contains(combined, "ddl-max-index-count") || strings.Contains(combined, "ddl_index_count"):
		return profileDDLMaxIndexCount
	case strings.Contains(combined, "ddl-multi-alter"):
		return profileDDLMultiAlter
	case strings.Contains(combined, "ddl-multi-ddl"):
		return profileDDLMultiDDL
	}

	if classifySkillCategory(name, description, renderedRule) == skillCategoryDML {
		return profileDMLGeneric
	}
	return profileDDLGeneric
}

func buildDeterministicTestCases(name, description, engine, renderedRule string, params map[string]any) []PromptToolTestCase {
	prof := detectRuleProfile(name, description, renderedRule)
	switch prof {
	case profileDDLRequirePK:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "CREATE TABLE declares an explicit BIGINT PRIMARY KEY column."},
			{ID: "case-2", SQL: "CREATE TABLE order_items (id BIGINT NOT NULL, order_id BIGINT NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "CREATE TABLE declares a table-level PRIMARY KEY constraint."},
			{ID: "case-3", SQL: "CREATE TABLE user_roles (user_id BIGINT NOT NULL, role_id BIGINT NOT NULL, PRIMARY KEY (user_id, role_id));", ExpectedCompliant: true, Rationale: "CREATE TABLE declares a composite PRIMARY KEY."},
			{ID: "case-4", SQL: "CREATE TABLE payment_ledgers (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, amount_cents BIGINT NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "CREATE TABLE includes an auto-increment PRIMARY KEY."},
			{ID: "case-5", SQL: "CREATE TABLE inventory_locks (id BIGINT NOT NULL CONSTRAINT pk_inventory_locks PRIMARY KEY, sku_code VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "CREATE TABLE includes a named PRIMARY KEY constraint."},
			{ID: "case-6", SQL: "CREATE TABLE temp_import_logs (raw_line VARCHAR(512) NOT NULL, created_at TIMESTAMP NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE omits PRIMARY KEY declaration."},
			{ID: "case-7", SQL: "CREATE TABLE audit_events (event_type VARCHAR(32) NOT NULL, actor_name VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE has no primary key column or constraint."},
			{ID: "case-8", SQL: "CREATE TABLE metrics_buffer (metric_name VARCHAR(64) NOT NULL, metric_value DECIMAL(18,4) NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE lacks a PRIMARY KEY."},
			{ID: "case-9", SQL: "CREATE TABLE webhook_deliveries (target_url VARCHAR(255) NOT NULL, status_code INT NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE is missing a PRIMARY KEY."},
			{ID: "case-10", SQL: "CREATE TABLE session_snapshots (token_hash VARCHAR(128) NOT NULL, expires_at TIMESTAMP NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE defines columns without a PRIMARY KEY."},
		}
	case profileDDLPKNamedID:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column is named id."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL, username VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "Table-level PRIMARY KEY references column id."},
			{ID: "case-3", SQL: "CREATE TABLE invoices (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, total_cents BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "Auto-increment primary key column is named id."},
			{ID: "case-4", SQL: "CREATE TABLE shipments (id INT NOT NULL PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Integer primary key column is named id."},
			{ID: "case-5", SQL: "CREATE TABLE audit_records (id BIGINT NOT NULL, action_name VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "Primary key follows the mandatory id naming convention."},
			{ID: "case-6", SQL: "CREATE TABLE orders (order_id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column is named order_id instead of id."},
			{ID: "case-7", SQL: "CREATE TABLE customers (customer_id BIGINT NOT NULL, username VARCHAR(64) NOT NULL, PRIMARY KEY (customer_id));", ExpectedCompliant: false, Rationale: "Primary key column is named customer_id instead of id."},
			{ID: "case-8", SQL: "CREATE TABLE invoices (invoice_pk BIGINT NOT NULL PRIMARY KEY, total_cents BIGINT NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column is named invoice_pk instead of id."},
			{ID: "case-9", SQL: "CREATE TABLE shipments (shipment_no BIGINT NOT NULL PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column is named shipment_no instead of id."},
			{ID: "case-10", SQL: "CREATE TABLE audit_records (record_uid BIGINT NOT NULL, action_name VARCHAR(64) NOT NULL, PRIMARY KEY (record_uid));", ExpectedCompliant: false, Rationale: "Primary key column is named record_uid instead of id."},
		}
	case profileDDLPKInteger:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column uses BIGINT integer type."},
			{ID: "case-2", SQL: "CREATE TABLE categories (id INT NOT NULL PRIMARY KEY, category_name VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column uses INT integer type."},
			{ID: "case-3", SQL: "CREATE TABLE dict_items (id SMALLINT NOT NULL PRIMARY KEY, item_code VARCHAR(32) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column uses SMALLINT integer type."},
			{ID: "case-4", SQL: "CREATE TABLE ledger_entries (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, amount_cents BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column uses BIGINT UNSIGNED integer type."},
			{ID: "case-5", SQL: "CREATE TABLE region_codes (id INTEGER NOT NULL PRIMARY KEY, region_name VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column uses INTEGER type."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id VARCHAR(64) NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column uses VARCHAR instead of an integer type."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id CHAR(36) NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column uses CHAR(36) instead of an integer type."},
			{ID: "case-8", SQL: "CREATE TABLE sessions (id TEXT NOT NULL PRIMARY KEY, user_id BIGINT NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column uses TEXT instead of an integer type."},
			{ID: "case-9", SQL: "CREATE TABLE devices (id VARCHAR(128) NOT NULL, device_name VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: false, Rationale: "Table-level primary key column uses VARCHAR(128)."},
			{ID: "case-10", SQL: "CREATE TABLE certificates (id BINARY(16) NOT NULL PRIMARY KEY, subject_name VARCHAR(128) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column uses BINARY instead of an integer type."},
		}
	case profileDDLPKAutoIncrement:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column declares AUTO_INCREMENT."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, username VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "Primary key column id declares AUTO_INCREMENT."},
			{ID: "case-3", SQL: "CREATE TABLE payments (id INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, amount_cents BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "INT primary key declares AUTO_INCREMENT."},
			{ID: "case-4", SQL: "CREATE TABLE shipments (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tracking_no VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "Primary key column includes AUTO_INCREMENT."},
			{ID: "case-5", SQL: "CREATE TABLE audit_logs (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, event_name VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Primary key column uses AUTO_INCREMENT."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT UNSIGNED NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key column omits AUTO_INCREMENT."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL, username VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: false, Rationale: "Primary key column does not specify AUTO_INCREMENT."},
			{ID: "case-8", SQL: "CREATE TABLE payments (id INT UNSIGNED NOT NULL PRIMARY KEY, amount_cents BIGINT NOT NULL);", ExpectedCompliant: false, Rationale: "INT primary key lacks AUTO_INCREMENT."},
			{ID: "case-9", SQL: "CREATE TABLE shipments (id BIGINT UNSIGNED NOT NULL, tracking_no VARCHAR(64) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: false, Rationale: "Primary key is not declared as AUTO_INCREMENT."},
			{ID: "case-10", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL PRIMARY KEY, event_name VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Primary key lacks AUTO_INCREMENT attribute."},
		}
	case profileDDLAutoIncUnsigned:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "AUTO_INCREMENT column declares UNSIGNED."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "INT AUTO_INCREMENT column declares UNSIGNED."},
			{ID: "case-3", SQL: "CREATE TABLE invoices (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, amount_cents BIGINT NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: true, Rationale: "BIGINT UNSIGNED AUTO_INCREMENT column."},
			{ID: "case-4", SQL: "CREATE TABLE shipments (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, carrier_code VARCHAR(32) NOT NULL);", ExpectedCompliant: true, Rationale: "Auto-increment key is declared UNSIGNED."},
			{ID: "case-5", SQL: "CREATE TABLE audit_logs (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, action_code VARCHAR(32) NOT NULL);", ExpectedCompliant: true, Rationale: "Auto-increment column includes UNSIGNED."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "AUTO_INCREMENT column omits UNSIGNED."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Signed INT AUTO_INCREMENT column lacks UNSIGNED."},
			{ID: "case-8", SQL: "CREATE TABLE invoices (id BIGINT NOT NULL AUTO_INCREMENT, amount_cents BIGINT NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: false, Rationale: "Signed BIGINT AUTO_INCREMENT column lacks UNSIGNED."},
			{ID: "case-9", SQL: "CREATE TABLE shipments (id INT NOT NULL AUTO_INCREMENT, carrier_code VARCHAR(32) NOT NULL, PRIMARY KEY (id));", ExpectedCompliant: false, Rationale: "AUTO_INCREMENT column is not UNSIGNED."},
			{ID: "case-10", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, action_code VARCHAR(32) NOT NULL);", ExpectedCompliant: false, Rationale: "Missing UNSIGNED on AUTO_INCREMENT column."},
		}
	case profileDDLAutoIncInitOne:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL) ENGINE=InnoDB AUTO_INCREMENT=1;", ExpectedCompliant: true, Rationale: "Table option sets AUTO_INCREMENT=1."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL) ENGINE=InnoDB;", ExpectedCompliant: true, Rationale: "Uses default initial auto-increment value without non-1 override."},
			{ID: "case-3", SQL: "CREATE TABLE payments (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, amount_cents BIGINT NOT NULL) AUTO_INCREMENT = 1;", ExpectedCompliant: true, Rationale: "Explicitly initializes AUTO_INCREMENT to 1."},
			{ID: "case-4", SQL: "CREATE TABLE shipments (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Does not override AUTO_INCREMENT with a value other than 1."},
			{ID: "case-5", SQL: "CREATE TABLE refunds (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, order_id BIGINT NOT NULL) ENGINE=InnoDB AUTO_INCREMENT=1;", ExpectedCompliant: true, Rationale: "Initial AUTO_INCREMENT value equals 1."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL) ENGINE=InnoDB AUTO_INCREMENT=1000;", ExpectedCompliant: false, Rationale: "Table option sets AUTO_INCREMENT=1000 instead of 1."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL) AUTO_INCREMENT=500;", ExpectedCompliant: false, Rationale: "Table option sets AUTO_INCREMENT=500 instead of 1."},
			{ID: "case-8", SQL: "CREATE TABLE payments (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, amount_cents BIGINT NOT NULL) ENGINE=InnoDB AUTO_INCREMENT=10000;", ExpectedCompliant: false, Rationale: "AUTO_INCREMENT initial value is 10000."},
			{ID: "case-9", SQL: "CREATE TABLE shipments (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL) AUTO_INCREMENT = 2;", ExpectedCompliant: false, Rationale: "AUTO_INCREMENT initial value is 2 instead of 1."},
			{ID: "case-10", SQL: "CREATE TABLE refunds (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, order_id BIGINT NOT NULL) AUTO_INCREMENT=9999;", ExpectedCompliant: false, Rationale: "AUTO_INCREMENT initial value is 9999 instead of 1."},
		}
	case profileDDLTableComment:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL) COMMENT='Customer order header table';", ExpectedCompliant: true, Rationale: "CREATE TABLE includes a table-level COMMENT."},
			{ID: "case-2", SQL: "CREATE TABLE order_items (id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL) ENGINE=InnoDB COMMENT='Order line items';", ExpectedCompliant: true, Rationale: "CREATE TABLE specifies non-empty table COMMENT."},
			{ID: "case-3", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL) COMMENT = 'Registered customer accounts';", ExpectedCompliant: true, Rationale: "CREATE TABLE includes table COMMENT option."},
			{ID: "case-4", SQL: "CREATE TABLE payment_records (id BIGINT NOT NULL PRIMARY KEY, amount_cents BIGINT NOT NULL) COMMENT='Payment transaction ledger';", ExpectedCompliant: true, Rationale: "CREATE TABLE provides table COMMENT."},
			{ID: "case-5", SQL: "CREATE TABLE inventory_stocks (id BIGINT NOT NULL PRIMARY KEY, sku_code VARCHAR(64) NOT NULL) COMMENT='Warehouse SKU stock balances';", ExpectedCompliant: true, Rationale: "CREATE TABLE defines table COMMENT."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE omits table-level COMMENT."},
			{ID: "case-7", SQL: "CREATE TABLE order_items (id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL) ENGINE=InnoDB;", ExpectedCompliant: false, Rationale: "CREATE TABLE has no COMMENT option."},
			{ID: "case-8", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "CREATE TABLE lacks table COMMENT."},
			{ID: "case-9", SQL: "CREATE TABLE payment_records (id BIGINT NOT NULL PRIMARY KEY, amount_cents BIGINT NOT NULL);", ExpectedCompliant: false, Rationale: "Missing table COMMENT on CREATE TABLE."},
			{ID: "case-10", SQL: "CREATE TABLE inventory_stocks (id BIGINT NOT NULL PRIMARY KEY, sku_code VARCHAR(64) NOT NULL) COMMENT='';", ExpectedCompliant: false, Rationale: "Empty table COMMENT on CREATE TABLE."},
		}
	case profileDDLColumnComments:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY COMMENT 'Primary key', order_no VARCHAR(64) NOT NULL COMMENT 'Order number');", ExpectedCompliant: true, Rationale: "Every column in CREATE TABLE carries a COMMENT."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY COMMENT 'Primary key', username VARCHAR(64) NOT NULL COMMENT 'Login username', status TINYINT NOT NULL COMMENT 'Account status');", ExpectedCompliant: true, Rationale: "All three columns define explicit column comments."},
			{ID: "case-3", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'Order remark';", ExpectedCompliant: true, Rationale: "Added column includes a COMMENT clause."},
			{ID: "case-4", SQL: "CREATE TABLE payments (id BIGINT NOT NULL PRIMARY KEY COMMENT 'ID', amount_cents BIGINT NOT NULL COMMENT 'Amount in cents');", ExpectedCompliant: true, Rationale: "All columns include non-empty comments."},
			{ID: "case-5", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '' COMMENT 'Contact phone number';", ExpectedCompliant: true, Rationale: "New column in ALTER TABLE defines a COMMENT."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY COMMENT 'Primary key', order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Column order_no is missing a COMMENT."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Columns in CREATE TABLE lack COMMENT clauses."},
			{ID: "case-8", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: false, Rationale: "Added column in ALTER TABLE omits COMMENT."},
			{ID: "case-9", SQL: "CREATE TABLE payments (id BIGINT NOT NULL PRIMARY KEY, amount_cents BIGINT NOT NULL COMMENT 'Amount in cents');", ExpectedCompliant: false, Rationale: "Column id is missing a COMMENT."},
			{ID: "case-10", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '' COMMENT '';", ExpectedCompliant: false, Rationale: "Added column has an empty COMMENT."},
		}
	case profileDDLColumnNullable:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL);", ExpectedCompliant: true, Rationale: "All columns in CREATE TABLE explicitly declare NOT NULL."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL, email VARCHAR(128) NOT NULL);", ExpectedCompliant: true, Rationale: "Every column is declared NOT NULL."},
			{ID: "case-3", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Added column in ALTER TABLE declares NOT NULL."},
			{ID: "case-4", SQL: "CREATE TABLE inventory_items (id BIGINT NOT NULL PRIMARY KEY, sku_code VARCHAR(64) NOT NULL, quantity INT NOT NULL);", ExpectedCompliant: true, Rationale: "All columns are non-nullable."},
			{ID: "case-5", SQL: "ALTER TABLE customers ADD COLUMN tier VARCHAR(32) NOT NULL DEFAULT 'standard';", ExpectedCompliant: true, Rationale: "New column explicitly specifies NOT NULL."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64), status VARCHAR(16));", ExpectedCompliant: false, Rationale: "Columns order_no and status omit NOT NULL."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NULL);", ExpectedCompliant: false, Rationale: "Column username is explicitly nullable."},
			{ID: "case-8", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) DEFAULT NULL;", ExpectedCompliant: false, Rationale: "Added columnremark allows NULL values."},
			{ID: "case-9", SQL: "CREATE TABLE inventory_items (id BIGINT NOT NULL PRIMARY KEY, sku_code VARCHAR(64) NOT NULL, description VARCHAR(255));", ExpectedCompliant: false, Rationale: "Column description omits NOT NULL."},
			{ID: "case-10", SQL: "ALTER TABLE customers ADD COLUMN middle_name VARCHAR(64);", ExpectedCompliant: false, Rationale: "New column in ALTER TABLE omits NOT NULL."},
		}
	case profileDDLColumnDefault:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL DEFAULT '', status VARCHAR(16) NOT NULL DEFAULT 'pending');", ExpectedCompliant: true, Rationale: "Non-primary-key columns define explicit DEFAULT values."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN retry_count INT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "Added column defines DEFAULT 0."},
			{ID: "case-3", SQL: "CREATE TABLE customers (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL DEFAULT '', tier VARCHAR(16) NOT NULL DEFAULT 'basic');", ExpectedCompliant: true, Rationale: "Columns specify explicit DEFAULT clauses."},
			{ID: "case-4", SQL: "ALTER TABLE customers ADD COLUMN is_vip TINYINT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "New column specifies DEFAULT 0."},
			{ID: "case-5", SQL: "ALTER TABLE shipments ADD COLUMN carrier_code VARCHAR(32) NOT NULL DEFAULT 'STD';", ExpectedCompliant: true, Rationale: "Added column includes a DEFAULT literal."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, order_no VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL);", ExpectedCompliant: false, Rationale: "Columns order_no and status omit DEFAULT clauses."},
			{ID: "case-7", SQL: "ALTER TABLE orders ADD COLUMN retry_count INT NOT NULL;", ExpectedCompliant: false, Rationale: "Added column retry_count has no DEFAULT value."},
			{ID: "case-8", SQL: "CREATE TABLE customers (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL, tier VARCHAR(16) NOT NULL);", ExpectedCompliant: false, Rationale: "Columns lack DEFAULT values."},
			{ID: "case-9", SQL: "ALTER TABLE customers ADD COLUMN is_vip TINYINT NOT NULL;", ExpectedCompliant: false, Rationale: "New column omits DEFAULT clause."},
			{ID: "case-10", SQL: "ALTER TABLE shipments ADD COLUMN carrier_code VARCHAR(32) NOT NULL;", ExpectedCompliant: false, Rationale: "Added column has no DEFAULT clause."},
		}
	case profileDDLMaxCharLength:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, country_code CHAR(2) NOT NULL, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "CHAR(2) and VARCHAR(64) are within the configured length limits."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN currency_code CHAR(3) NOT NULL DEFAULT 'CNY';", ExpectedCompliant: true, Rationale: "CHAR(3) is within max_char_length."},
			{ID: "case-3", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, lang_tag CHAR(5) NOT NULL, email VARCHAR(128) NOT NULL);", ExpectedCompliant: true, Rationale: "CHAR(5) and VARCHAR(128) comply with length thresholds."},
			{ID: "case-4", SQL: "ALTER TABLE customers ADD COLUMN region_code CHAR(6) NOT NULL DEFAULT 'CN-SH';", ExpectedCompliant: true, Rationale: "CHAR(6) does not exceed the maximum CHAR length."},
			{ID: "case-5", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, status_code CHAR(4) NOT NULL, tracking_no VARCHAR(128) NOT NULL);", ExpectedCompliant: true, Rationale: "Column lengths satisfy CHAR and VARCHAR limits."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, fixed_payload CHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "CHAR(64) exceeds max_char_length."},
			{ID: "case-7", SQL: "ALTER TABLE orders ADD COLUMN padded_token CHAR(128) NOT NULL DEFAULT '';", ExpectedCompliant: false, Rationale: "CHAR(128) exceeds max_char_length."},
			{ID: "case-8", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, bio VARCHAR(4096) NOT NULL);", ExpectedCompliant: false, Rationale: "VARCHAR(4096) exceeds max_varchar_length."},
			{ID: "case-9", SQL: "ALTER TABLE customers ADD COLUMN raw_header CHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: false, Rationale: "CHAR(255) exceeds max_char_length."},
			{ID: "case-10", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL PRIMARY KEY, detail_text VARCHAR(8192) NOT NULL);", ExpectedCompliant: false, Rationale: "VARCHAR(8192) exceeds max_varchar_length."},
		}
	case profileDDLMaxTableNameLength:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Table name orders is within max_table_name_length."},
			{ID: "case-2", SQL: "CREATE TABLE users (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Table name users is 5 characters long."},
			{ID: "case-3", SQL: "CREATE TABLE items (id BIGINT NOT NULL PRIMARY KEY, sku_code VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Table name items is within the length threshold."},
			{ID: "case-4", SQL: "CREATE TABLE bills (id BIGINT NOT NULL PRIMARY KEY, amount_cents BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "Table name bills is within the length threshold."},
			{ID: "case-5", SQL: "CREATE TABLE tasks (id BIGINT NOT NULL PRIMARY KEY, task_state VARCHAR(32) NOT NULL);", ExpectedCompliant: true, Rationale: "Table name tasks is within the length threshold."},
			{ID: "case-6", SQL: "CREATE TABLE customer_order_settlement_history_records (id BIGINT NOT NULL PRIMARY KEY);", ExpectedCompliant: false, Rationale: "Table name exceeds max_table_name_length."},
			{ID: "case-7", SQL: "CREATE TABLE international_shipment_tracking_checkpoints (id BIGINT NOT NULL PRIMARY KEY);", ExpectedCompliant: false, Rationale: "Table name exceeds max_table_name_length."},
			{ID: "case-8", SQL: "CREATE TABLE enterprise_department_permission_mappings (id BIGINT NOT NULL PRIMARY KEY);", ExpectedCompliant: false, Rationale: "Table name exceeds max_table_name_length."},
			{ID: "case-9", SQL: "CREATE TABLE payment_gateway_reconciliation_snapshots (id BIGINT NOT NULL PRIMARY KEY);", ExpectedCompliant: false, Rationale: "Table name exceeds max_table_name_length."},
			{ID: "case-10", SQL: "CREATE TABLE warehouse_inventory_reservation_ledgers (id BIGINT NOT NULL PRIMARY KEY);", ExpectedCompliant: false, Rationale: "Table name exceeds max_table_name_length."},
		}
	case profileDDLIdentifierKeyword:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE customer_orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Table and column names avoid SQL reserved keywords."},
			{ID: "case-2", SQL: "CREATE TABLE user_accounts (id BIGINT NOT NULL PRIMARY KEY, login_name VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Identifiers use non-keyword names."},
			{ID: "case-3", SQL: "ALTER TABLE customer_orders ADD COLUMN shipping_status VARCHAR(32) NOT NULL DEFAULT 'pending';", ExpectedCompliant: true, Rationale: "Column identifier shipping_status is not a SQL keyword."},
			{ID: "case-4", SQL: "CREATE INDEX idx_user_accounts_login ON user_accounts (login_name);", ExpectedCompliant: true, Rationale: "Index and table identifiers avoid reserved words."},
			{ID: "case-5", SQL: "CREATE TABLE payment_entries (id BIGINT NOT NULL PRIMARY KEY, paid_amount BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "All identifiers are non-reserved words."},
			{ID: "case-6", SQL: "CREATE TABLE select (id BIGINT NOT NULL PRIMARY KEY, name VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Uses SQL reserved keyword select as table identifier."},
			{ID: "case-7", SQL: "CREATE TABLE order (id BIGINT NOT NULL PRIMARY KEY, group VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Uses SQL reserved keywords order and group as identifiers."},
			{ID: "case-8", SQL: "CREATE TABLE table (id BIGINT NOT NULL PRIMARY KEY, where VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Uses SQL reserved keywords table and where."},
			{ID: "case-9", SQL: "ALTER TABLE customer_orders ADD COLUMN from VARCHAR(64) NOT NULL DEFAULT '';", ExpectedCompliant: false, Rationale: "Uses SQL reserved keyword from as column name."},
			{ID: "case-10", SQL: "CREATE TABLE values (id BIGINT NOT NULL PRIMARY KEY, distinct VARCHAR(64) NOT NULL);", ExpectedCompliant: false, Rationale: "Uses SQL reserved keywords values and distinct."},
		}
	case profileDDLAcrossDBRename:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "RENAME TABLE orders TO orders_archive;", ExpectedCompliant: true, Rationale: "Renames table within the same database schema."},
			{ID: "case-2", SQL: "ALTER TABLE user_sessions RENAME TO user_sessions_bak;", ExpectedCompliant: true, Rationale: "Same-database ALTER TABLE RENAME operation."},
			{ID: "case-3", SQL: "RENAME TABLE invoices TO invoices_2026;", ExpectedCompliant: true, Rationale: "Same-schema table rename without cross-database qualifier."},
			{ID: "case-4", SQL: "ALTER TABLE shipments ADD COLUMN tracking_url VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Standard in-place ALTER TABLE statement."},
			{ID: "case-5", SQL: "CREATE INDEX idx_orders_created_at ON orders (created_at);", ExpectedCompliant: true, Rationale: "Standard DDL index creation within current database."},
			{ID: "case-6", SQL: "RENAME TABLE prod_core.orders TO prod_archive.orders;", ExpectedCompliant: false, Rationale: "Cross-database RENAME TABLE between prod_core and prod_archive."},
			{ID: "case-7", SQL: "RENAME TABLE billing_db.invoices TO history_db.invoices;", ExpectedCompliant: false, Rationale: "Cross-database RENAME TABLE moves object across schemas."},
			{ID: "case-8", SQL: "ALTER TABLE app_db.user_sessions RENAME TO backup_db.user_sessions;", ExpectedCompliant: false, Rationale: "Cross-database ALTER TABLE RENAME operation."},
			{ID: "case-9", SQL: "RENAME TABLE db_alpha.payments TO db_beta.payments;", ExpectedCompliant: false, Rationale: "Cross-database RENAME TABLE is prohibited."},
			{ID: "case-10", SQL: "RENAME TABLE shop_main.shipments TO shop_cold.shipments;", ExpectedCompliant: false, Rationale: "Cross-database RENAME TABLE violates the rule."},
		}
	case profileDDLCharsetAllowlist:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL) DEFAULT CHARSET=utf8mb4;", ExpectedCompliant: true, Rationale: "Uses allowed charset utf8mb4."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL) DEFAULT CHARACTER SET utf8mb4;", ExpectedCompliant: true, Rationale: "Uses allowed character set utf8mb4."},
			{ID: "case-3", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL PRIMARY KEY, event_name VARCHAR(64) NOT NULL) DEFAULT CHARSET=utf8;", ExpectedCompliant: true, Rationale: "Uses allowed charset utf8."},
			{ID: "case-4", SQL: "ALTER TABLE orders CONVERT TO CHARACTER SET utf8mb4;", ExpectedCompliant: true, Rationale: "Converts table to allowed charset utf8mb4."},
			{ID: "case-5", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Inherits compliant default schema charset without disallowed override."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL) DEFAULT CHARSET=latin1;", ExpectedCompliant: false, Rationale: "Charset latin1 is not in the allowed charset list."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL) DEFAULT CHARSET=gbk;", ExpectedCompliant: false, Rationale: "Charset gbk is not in the allowed charset list."},
			{ID: "case-8", SQL: "CREATE TABLE payments (id BIGINT NOT NULL PRIMARY KEY, remark VARCHAR(128) NOT NULL) DEFAULT CHARACTER SET gb2312;", ExpectedCompliant: false, Rationale: "Charset gb2312 is not in the allowed charset list."},
			{ID: "case-9", SQL: "ALTER TABLE orders CONVERT TO CHARACTER SET ascii;", ExpectedCompliant: false, Rationale: "Charset ascii is not in the allowed charset list."},
			{ID: "case-10", SQL: "CREATE TABLE legacy_logs (id BIGINT NOT NULL PRIMARY KEY, msg VARCHAR(255) NOT NULL) DEFAULT CHARSET=big5;", ExpectedCompliant: false, Rationale: "Charset big5 is not in the allowed charset list."},
		}
	case profileDDLCollationAllowlist:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci;", ExpectedCompliant: true, Rationale: "Uses allowed collation utf8mb4_general_ci."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;", ExpectedCompliant: true, Rationale: "Uses allowed collation utf8mb4_bin."},
			{ID: "case-3", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL PRIMARY KEY, action_name VARCHAR(64) NOT NULL) DEFAULT CHARSET=utf8 COLLATE=utf8_general_ci;", ExpectedCompliant: true, Rationale: "Uses allowed collation utf8_general_ci."},
			{ID: "case-4", SQL: "CREATE TABLE tokens (id BIGINT NOT NULL PRIMARY KEY, token_hash VARCHAR(128) NOT NULL) DEFAULT CHARSET=utf8 COLLATE=utf8_bin;", ExpectedCompliant: true, Rationale: "Uses allowed collation utf8_bin."},
			{ID: "case-5", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Does not specify a disallowed collation."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL) DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;", ExpectedCompliant: false, Rationale: "Collation latin1_swedish_ci is not in the allowed list."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL) COLLATE=gbk_chinese_ci;", ExpectedCompliant: false, Rationale: "Collation gbk_chinese_ci is not in the allowed list."},
			{ID: "case-8", SQL: "CREATE TABLE payments (id BIGINT NOT NULL PRIMARY KEY, note VARCHAR(128) NOT NULL) COLLATE=ascii_general_ci;", ExpectedCompliant: false, Rationale: "Collation ascii_general_ci is not in the allowed list."},
			{ID: "case-9", SQL: "ALTER TABLE orders CONVERT TO CHARACTER SET latin1 COLLATE latin1_bin;", ExpectedCompliant: false, Rationale: "Collation latin1_bin is not in the allowed list."},
			{ID: "case-10", SQL: "CREATE TABLE legacy_items (id BIGINT NOT NULL PRIMARY KEY, title VARCHAR(128) NOT NULL) COLLATE=big5_chinese_ci;", ExpectedCompliant: false, Rationale: "Collation big5_chinese_ci is not in the allowed list."},
		}
	case profileDDLForbidDropDatabase:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Safe CREATE TABLE DDL statement."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Additive ALTER TABLE DDL statement."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_order_no ON orders (order_no);", ExpectedCompliant: true, Rationale: "Non-destructive CREATE INDEX DDL statement."},
			{ID: "case-4", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Safe column addition DDL statement."},
			{ID: "case-5", SQL: "CREATE TABLE payment_records (id BIGINT NOT NULL PRIMARY KEY, amount_cents BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "Safe table creation DDL statement."},
			{ID: "case-6", SQL: "DROP DATABASE production_core;", ExpectedCompliant: false, Rationale: "Prohibited DROP DATABASE statement."},
			{ID: "case-7", SQL: "DROP DATABASE IF EXISTS customer_center;", ExpectedCompliant: false, Rationale: "Prohibited DROP DATABASE IF EXISTS statement."},
			{ID: "case-8", SQL: "DROP SCHEMA billing_ledger;", ExpectedCompliant: false, Rationale: "Prohibited DROP SCHEMA statement."},
			{ID: "case-9", SQL: "DROP DATABASE analytics_warehouse;", ExpectedCompliant: false, Rationale: "Destructive DROP DATABASE operation."},
			{ID: "case-10", SQL: "DROP SCHEMA IF EXISTS order_history;", ExpectedCompliant: false, Rationale: "Destructive DROP SCHEMA IF EXISTS operation."},
		}
	case profileDDLForbidDropOther:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Safe CREATE TABLE DDL statement."},
			{ID: "case-2", SQL: "CREATE INDEX idx_orders_order_no ON orders (order_no);", ExpectedCompliant: true, Rationale: "Safe CREATE INDEX DDL statement."},
			{ID: "case-3", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Additive ALTER TABLE ADD COLUMN statement."},
			{ID: "case-4", SQL: "CREATE UNIQUE INDEX uniq_customers_email ON customers (email);", ExpectedCompliant: true, Rationale: "Safe CREATE UNIQUE INDEX DDL statement."},
			{ID: "case-5", SQL: "ALTER TABLE shipments ADD COLUMN delivered_at TIMESTAMP NULL;", ExpectedCompliant: true, Rationale: "Safe ALTER TABLE column addition."},
			{ID: "case-6", SQL: "DROP INDEX idx_orders_order_no ON orders;", ExpectedCompliant: false, Rationale: "Prohibited DROP INDEX operation requires review."},
			{ID: "case-7", SQL: "DROP VIEW v_active_orders;", ExpectedCompliant: false, Rationale: "Prohibited DROP VIEW operation."},
			{ID: "case-8", SQL: "DROP TRIGGER trg_orders_audit;", ExpectedCompliant: false, Rationale: "Prohibited DROP TRIGGER operation."},
			{ID: "case-9", SQL: "DROP PROCEDURE sp_archive_orders;", ExpectedCompliant: false, Rationale: "Prohibited DROP PROCEDURE operation."},
			{ID: "case-10", SQL: "DROP FUNCTION fn_calc_discount;", ExpectedCompliant: false, Rationale: "Prohibited DROP FUNCTION operation."},
		}
	case profileDDLForbidTruncate:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Safe CREATE TABLE DDL statement."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Safe ALTER TABLE ADD COLUMN DDL statement."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_customer_id ON orders (customer_id);", ExpectedCompliant: true, Rationale: "Safe CREATE INDEX DDL statement."},
			{ID: "case-4", SQL: "ALTER TABLE user_sessions ADD COLUMN client_ip VARCHAR(45) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Non-destructive ALTER TABLE DDL statement."},
			{ID: "case-5", SQL: "CREATE TABLE order_archive (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Safe CREATE TABLE DDL statement."},
			{ID: "case-6", SQL: "TRUNCATE TABLE orders;", ExpectedCompliant: false, Rationale: "Prohibited TRUNCATE TABLE statement."},
			{ID: "case-7", SQL: "TRUNCATE TABLE user_sessions;", ExpectedCompliant: false, Rationale: "Prohibited TRUNCATE TABLE statement on user_sessions."},
			{ID: "case-8", SQL: "TRUNCATE audit_logs;", ExpectedCompliant: false, Rationale: "Prohibited TRUNCATE statement."},
			{ID: "case-9", SQL: "TRUNCATE TABLE payment_callbacks;", ExpectedCompliant: false, Rationale: "Prohibited TRUNCATE TABLE statement."},
			{ID: "case-10", SQL: "TRUNCATE TABLE webhook_deliveries;", ExpectedCompliant: false, Rationale: "Prohibited TRUNCATE TABLE statement."},
		}
	case profileDDLForbidDropTable:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Safe CREATE TABLE DDL statement."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Additive ALTER TABLE ADD COLUMN preserves table data."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_customer_created ON orders (customer_id, created_at);", ExpectedCompliant: true, Rationale: "Non-destructive CREATE INDEX DDL statement."},
			{ID: "case-4", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Non-destructive ALTER TABLE column addition."},
			{ID: "case-5", SQL: "CREATE UNIQUE INDEX uniq_orders_order_no ON orders (order_no);", ExpectedCompliant: true, Rationale: "Safe unique index creation DDL statement."},
			{ID: "case-6", SQL: "DROP TABLE orders;", ExpectedCompliant: false, Rationale: "Prohibited DROP TABLE statement."},
			{ID: "case-7", SQL: "DROP TABLE IF EXISTS user_sessions;", ExpectedCompliant: false, Rationale: "Prohibited DROP TABLE IF EXISTS statement."},
			{ID: "case-8", SQL: "DROP TABLE payment_ledgers;", ExpectedCompliant: false, Rationale: "Destructive DROP TABLE operation."},
			{ID: "case-9", SQL: "DROP TABLE customer_addresses;", ExpectedCompliant: false, Rationale: "Destructive DROP TABLE operation."},
			{ID: "case-10", SQL: "DROP TABLE IF EXISTS inventory_reservations;", ExpectedCompliant: false, Rationale: "Destructive DROP TABLE IF EXISTS operation."},
		}
	case profileDDLForbidColTypeChange:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Additive ALTER TABLE ADD COLUMN does not modify existing column types."},
			{ID: "case-2", SQL: "ALTER TABLE customers ADD COLUMN loyalty_points INT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "Adds a new column without changing existing column types."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_created_at ON orders (created_at);", ExpectedCompliant: true, Rationale: "Creates an index without altering column types."},
			{ID: "case-4", SQL: "ALTER TABLE shipments ADD COLUMN carrier_name VARCHAR(64) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Safe column addition on shipments."},
			{ID: "case-5", SQL: "CREATE TABLE order_tags (id BIGINT NOT NULL PRIMARY KEY, tag_name VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "New table creation does not alter existing column types."},
			{ID: "case-6", SQL: "ALTER TABLE orders MODIFY COLUMN order_no VARCHAR(32) NOT NULL;", ExpectedCompliant: false, Rationale: "ALTER TABLE MODIFY COLUMN changes the column type definition."},
			{ID: "case-7", SQL: "ALTER TABLE customers CHANGE COLUMN phone phone INT NOT NULL;", ExpectedCompliant: false, Rationale: "ALTER TABLE CHANGE COLUMN converts VARCHAR to INT."},
			{ID: "case-8", SQL: "ALTER TABLE payments MODIFY COLUMN amount_cents INT NOT NULL;", ExpectedCompliant: false, Rationale: "ALTER TABLE MODIFY COLUMN shrinks column type."},
			{ID: "case-9", SQL: "ALTER TABLE orders ALTER COLUMN status TYPE SMALLINT;", ExpectedCompliant: false, Rationale: "ALTER COLUMN TYPE modifies existing column data type."},
			{ID: "case-10", SQL: "ALTER TABLE shipments CHANGE COLUMN tracking_no tracking_no CHAR(16) NOT NULL;", ExpectedCompliant: false, Rationale: "ALTER TABLE CHANGE COLUMN alters column data type."},
		}
	case profileDDLForbidColPosChange:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Appends column at the end of the table without FIRST or AFTER."},
			{ID: "case-2", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Adds column without position reordering."},
			{ID: "case-3", SQL: "ALTER TABLE payments ADD COLUMN channel_code VARCHAR(32) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Adds column without AFTER or FIRST clause."},
			{ID: "case-4", SQL: "ALTER TABLE shipments ADD COLUMN warehouse_id BIGINT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "Appends column without physical column reordering."},
			{ID: "case-5", SQL: "CREATE INDEX idx_orders_status ON orders (status);", ExpectedCompliant: true, Rationale: "Index DDL does not reorder columns."},
			{ID: "case-6", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '' AFTER order_no;", ExpectedCompliant: false, Rationale: "Uses AFTER clause to reorder column position."},
			{ID: "case-7", SQL: "ALTER TABLE customers ADD COLUMN tenant_id BIGINT NOT NULL DEFAULT 0 FIRST;", ExpectedCompliant: false, Rationale: "Uses FIRST clause to insert column at the beginning of the table."},
			{ID: "case-8", SQL: "ALTER TABLE payments ADD COLUMN currency VARCHAR(8) NOT NULL DEFAULT 'CNY' AFTER id;", ExpectedCompliant: false, Rationale: "Uses AFTER id to reposition column."},
			{ID: "case-9", SQL: "ALTER TABLE shipments MODIFY COLUMN tracking_no VARCHAR(64) NOT NULL AFTER id;", ExpectedCompliant: false, Rationale: "Uses AFTER clause in MODIFY COLUMN."},
			{ID: "case-10", SQL: "ALTER TABLE orders ADD COLUMN priority TINYINT NOT NULL DEFAULT 0 FIRST;", ExpectedCompliant: false, Rationale: "Uses FIRST clause in ALTER TABLE ADD COLUMN."},
		}
	case profileDDLFloatDoubleDecimal:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE payment_orders (id BIGINT NOT NULL PRIMARY KEY, amount DECIMAL(18,2) NOT NULL);", ExpectedCompliant: true, Rationale: "Uses exact DECIMAL(18,2) type for monetary amount."},
			{ID: "case-2", SQL: "CREATE TABLE product_prices (id BIGINT NOT NULL PRIMARY KEY, unit_price NUMERIC(12,4) NOT NULL);", ExpectedCompliant: true, Rationale: "Uses exact NUMERIC(12,4) type."},
			{ID: "case-3", SQL: "ALTER TABLE invoices ADD COLUMN tax_amount DECIMAL(16,2) NOT NULL DEFAULT 0.00;", ExpectedCompliant: true, Rationale: "Uses DECIMAL(16,2) in ALTER TABLE ADD COLUMN."},
			{ID: "case-4", SQL: "CREATE TABLE currency_rates (id BIGINT NOT NULL PRIMARY KEY, exchange_rate DECIMAL(18,6) NOT NULL);", ExpectedCompliant: true, Rationale: "Uses exact DECIMAL(18,6) for exchange rate."},
			{ID: "case-5", SQL: "ALTER TABLE order_items ADD COLUMN discount_amount DECIMAL(12,2) NOT NULL DEFAULT 0.00;", ExpectedCompliant: true, Rationale: "Uses DECIMAL(12,2) instead of floating-point type."},
			{ID: "case-6", SQL: "CREATE TABLE payment_orders (id BIGINT NOT NULL PRIMARY KEY, amount FLOAT NOT NULL);", ExpectedCompliant: false, Rationale: "Uses approximate FLOAT type instead of DECIMAL."},
			{ID: "case-7", SQL: "CREATE TABLE product_prices (id BIGINT NOT NULL PRIMARY KEY, unit_price DOUBLE NOT NULL);", ExpectedCompliant: false, Rationale: "Uses approximate DOUBLE type instead of DECIMAL."},
			{ID: "case-8", SQL: "ALTER TABLE invoices ADD COLUMN tax_amount FLOAT NOT NULL DEFAULT 0;", ExpectedCompliant: false, Rationale: "Adds a FLOAT column in ALTER TABLE."},
			{ID: "case-9", SQL: "CREATE TABLE currency_rates (id BIGINT NOT NULL PRIMARY KEY, exchange_rate DOUBLE PRECISION NOT NULL);", ExpectedCompliant: false, Rationale: "Uses DOUBLE PRECISION instead of DECIMAL."},
			{ID: "case-10", SQL: "ALTER TABLE order_items ADD COLUMN discount_rate REAL NOT NULL DEFAULT 0;", ExpectedCompliant: false, Rationale: "Uses approximate REAL floating-point type."},
		}
	case profileDDLSpecialType:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL, total_amount DECIMAL(18,2) NOT NULL);", ExpectedCompliant: true, Rationale: "Uses standard BIGINT, VARCHAR, and DECIMAL column types."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL, status TINYINT NOT NULL);", ExpectedCompliant: true, Rationale: "Uses standard integer and VARCHAR types."},
			{ID: "case-3", SQL: "ALTER TABLE orders ADD COLUMN shipping_code VARCHAR(64) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Adds a standard VARCHAR(64) column."},
			{ID: "case-4", SQL: "CREATE TABLE inventory_stocks (id BIGINT NOT NULL PRIMARY KEY, sku_code VARCHAR(64) NOT NULL, quantity INT NOT NULL);", ExpectedCompliant: true, Rationale: "Uses standard scalar types only."},
			{ID: "case-5", SQL: "ALTER TABLE customers ADD COLUMN login_count INT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "Adds a standard INT column."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, status ENUM('pending', 'paid', 'closed') NOT NULL);", ExpectedCompliant: false, Rationale: "Uses special ENUM column type."},
			{ID: "case-7", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL PRIMARY KEY, raw_payload TEXT NOT NULL);", ExpectedCompliant: false, Rationale: "Uses special TEXT column type."},
			{ID: "case-8", SQL: "ALTER TABLE customers ADD COLUMN avatar_bytes BLOB NULL;", ExpectedCompliant: false, Rationale: "Uses special BLOB column type."},
			{ID: "case-9", SQL: "CREATE TABLE user_preferences (id BIGINT NOT NULL PRIMARY KEY, settings_doc JSON NOT NULL);", ExpectedCompliant: false, Rationale: "Uses special JSON column type."},
			{ID: "case-10", SQL: "CREATE TABLE store_locations (id BIGINT NOT NULL PRIMARY KEY, coords GEOMETRY NOT NULL);", ExpectedCompliant: false, Rationale: "Uses special GEOMETRY column type."},
		}
	case profileDDLForbidForeignKey:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE order_items (id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, INDEX idx_order_items_order_id (order_id));", ExpectedCompliant: true, Rationale: "Indexes order_id without a FOREIGN KEY constraint."},
			{ID: "case-2", SQL: "CREATE TABLE user_profiles (id BIGINT NOT NULL PRIMARY KEY, user_id BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "Defines user_id column without a FOREIGN KEY."},
			{ID: "case-3", SQL: "ALTER TABLE shipments ADD COLUMN order_id BIGINT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "Adds reference column without FOREIGN KEY constraint."},
			{ID: "case-4", SQL: "CREATE INDEX idx_shipments_order_id ON shipments (order_id);", ExpectedCompliant: true, Rationale: "Creates supporting index without foreign key constraint."},
			{ID: "case-5", SQL: "CREATE TABLE invoices (id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL);", ExpectedCompliant: true, Rationale: "Table definition contains no FOREIGN KEY constraint."},
			{ID: "case-6", SQL: "CREATE TABLE order_items (id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, CONSTRAINT fk_items_order FOREIGN KEY (order_id) REFERENCES orders (id));", ExpectedCompliant: false, Rationale: "Defines a FOREIGN KEY constraint on order_id."},
			{ID: "case-7", SQL: "ALTER TABLE order_items ADD CONSTRAINT fk_order_items_order FOREIGN KEY (order_id) REFERENCES orders (id);", ExpectedCompliant: false, Rationale: "ALTER TABLE adds a FOREIGN KEY constraint."},
			{ID: "case-8", SQL: "CREATE TABLE user_profiles (id BIGINT NOT NULL PRIMARY KEY, user_id BIGINT NOT NULL, FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE);", ExpectedCompliant: false, Rationale: "Defines an inline FOREIGN KEY with ON DELETE CASCADE."},
			{ID: "case-9", SQL: "ALTER TABLE shipments ADD FOREIGN KEY (order_id) REFERENCES orders (id);", ExpectedCompliant: false, Rationale: "Adds an unnamed FOREIGN KEY constraint."},
			{ID: "case-10", SQL: "CREATE TABLE invoices (id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL, CONSTRAINT fk_invoices_customer FOREIGN KEY (customer_id) REFERENCES customers (id));", ExpectedCompliant: false, Rationale: "Includes a FOREIGN KEY constraint in CREATE TABLE."},
		}
	case profileDDLViewForbidSelectStar:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE VIEW v_active_orders AS SELECT id, order_no, customer_id, status FROM orders WHERE status = 'active';", ExpectedCompliant: true, Rationale: "CREATE VIEW explicitly lists projected columns."},
			{ID: "case-2", SQL: "CREATE VIEW v_customer_summary AS SELECT id, username, email FROM customers;", ExpectedCompliant: true, Rationale: "CREATE VIEW uses an explicit column list."},
			{ID: "case-3", SQL: "CREATE VIEW v_paid_invoices AS SELECT id, order_id, amount_cents, paid_at FROM invoices WHERE paid_at IS NOT NULL;", ExpectedCompliant: true, Rationale: "CREATE VIEW projects explicit columns without wildcard."},
			{ID: "case-4", SQL: "CREATE VIEW v_shipment_status AS SELECT id, order_id, tracking_no FROM shipments;", ExpectedCompliant: true, Rationale: "CREATE VIEW specifies explicit column names."},
			{ID: "case-5", SQL: "CREATE VIEW v_inventory_available AS SELECT id, sku_code, quantity FROM inventory_items WHERE quantity > 0;", ExpectedCompliant: true, Rationale: "CREATE VIEW avoids SELECT *."},
			{ID: "case-6", SQL: "CREATE VIEW v_active_orders AS SELECT * FROM orders WHERE status = 'active';", ExpectedCompliant: false, Rationale: "CREATE VIEW uses prohibited SELECT * projection."},
			{ID: "case-7", SQL: "CREATE VIEW v_customer_summary AS SELECT * FROM customers;", ExpectedCompliant: false, Rationale: "CREATE VIEW selects all columns with SELECT *."},
			{ID: "case-8", SQL: "CREATE OR REPLACE VIEW v_paid_invoices AS SELECT * FROM invoices WHERE paid_at IS NOT NULL;", ExpectedCompliant: false, Rationale: "CREATE OR REPLACE VIEW uses SELECT *."},
			{ID: "case-9", SQL: "CREATE VIEW v_shipment_status AS SELECT s.* FROM shipments s;", ExpectedCompliant: false, Rationale: "CREATE VIEW uses table wildcard s.*."},
			{ID: "case-10", SQL: "CREATE VIEW v_inventory_available AS SELECT * FROM inventory_items;", ExpectedCompliant: false, Rationale: "CREATE VIEW uses wildcard SELECT *."},
		}
	case profileDDLAllowCreateView:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Standard CREATE TABLE DDL statement."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Standard ALTER TABLE DDL statement."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_order_no ON orders (order_no);", ExpectedCompliant: true, Rationale: "Standard CREATE INDEX DDL statement."},
			{ID: "case-4", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Creates a base table rather than a view."},
			{ID: "case-5", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Alters a base table rather than creating a view."},
			{ID: "case-6", SQL: "CREATE VIEW v_active_orders AS SELECT id, order_no FROM orders WHERE status = 'active';", ExpectedCompliant: false, Rationale: "CREATE VIEW statement triggers view creation review rule."},
			{ID: "case-7", SQL: "CREATE OR REPLACE VIEW v_customer_emails AS SELECT id, email FROM customers;", ExpectedCompliant: false, Rationale: "CREATE OR REPLACE VIEW statement is blocked by the rule."},
			{ID: "case-8", SQL: "CREATE VIEW v_order_totals AS SELECT order_id, SUM(amount_cents) AS total_cents FROM payments GROUP BY order_id;", ExpectedCompliant: false, Rationale: "Creates a view object."},
			{ID: "case-9", SQL: "CREATE VIEW v_recent_shipments AS SELECT id, tracking_no FROM shipments;", ExpectedCompliant: false, Rationale: "CREATE VIEW statement violates the rule."},
			{ID: "case-10", SQL: "CREATE VIEW v_vip_customers AS SELECT id, username FROM customers WHERE is_vip = 1;", ExpectedCompliant: false, Rationale: "CREATE VIEW statement is flagged for review."},
		}
	case profileDDLAllowPartition:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL, created_at TIMESTAMP NOT NULL);", ExpectedCompliant: true, Rationale: "Standard unpartitioned CREATE TABLE statement."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Standard ALTER TABLE ADD COLUMN without partitioning."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_created_at ON orders (created_at);", ExpectedCompliant: true, Rationale: "Standard index creation on unpartitioned table."},
			{ID: "case-4", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL PRIMARY KEY, event_time TIMESTAMP NOT NULL);", ExpectedCompliant: true, Rationale: "Unpartitioned table definition."},
			{ID: "case-5", SQL: "ALTER TABLE audit_logs ADD COLUMN actor_id BIGINT NOT NULL DEFAULT 0;", ExpectedCompliant: true, Rationale: "Standard column addition without PARTITION BY."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL, created_year INT NOT NULL, PRIMARY KEY (id, created_year)) PARTITION BY RANGE (created_year) (PARTITION p2025 VALUES LESS THAN (2026));", ExpectedCompliant: false, Rationale: "CREATE TABLE uses PARTITION BY RANGE."},
			{ID: "case-7", SQL: "CREATE TABLE audit_logs (id BIGINT NOT NULL, tenant_id INT NOT NULL, PRIMARY KEY (id, tenant_id)) PARTITION BY HASH (tenant_id) PARTITIONS 8;", ExpectedCompliant: false, Rationale: "CREATE TABLE uses PARTITION BY HASH."},
			{ID: "case-8", SQL: "CREATE TABLE region_orders (id BIGINT NOT NULL, region_id INT NOT NULL, PRIMARY KEY (id, region_id)) PARTITION BY LIST (region_id) (PARTITION p_east VALUES IN (1, 2));", ExpectedCompliant: false, Rationale: "CREATE TABLE uses PARTITION BY LIST."},
			{ID: "case-9", SQL: "ALTER TABLE orders PARTITION BY KEY (id) PARTITIONS 4;", ExpectedCompliant: false, Rationale: "ALTER TABLE introduces PARTITION BY KEY."},
			{ID: "case-10", SQL: "CREATE TABLE metrics_history (id BIGINT NOT NULL, day_ bucket INT NOT NULL, PRIMARY KEY (id, day_bucket)) PARTITION BY RANGE (day_bucket) (PARTITION p0 VALUES LESS THAN (100));", ExpectedCompliant: false, Rationale: "Table definition uses PARTITION BY."},
		}
	case profileDDLForbidUnnamedIndex:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL, INDEX idx_orders_order_no (order_no));", ExpectedCompliant: true, Rationale: "Inline index explicitly specifies name idx_orders_order_no."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, email VARCHAR(128) NOT NULL, UNIQUE KEY uniq_customers_email (email));", ExpectedCompliant: true, Rationale: "Inline unique index explicitly specifies name uniq_customers_email."},
			{ID: "case-3", SQL: "ALTER TABLE orders ADD INDEX idx_orders_status (status);", ExpectedCompliant: true, Rationale: "ALTER TABLE ADD INDEX specifies explicit index name."},
			{ID: "case-4", SQL: "CREATE INDEX idx_shipments_tracking_no ON shipments (tracking_no);", ExpectedCompliant: true, Rationale: "CREATE INDEX specifies explicit index name."},
			{ID: "case-5", SQL: "ALTER TABLE customers ADD UNIQUE INDEX uniq_customers_username (username);", ExpectedCompliant: true, Rationale: "ALTER TABLE ADD UNIQUE INDEX specifies explicit name."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL, INDEX (order_no));", ExpectedCompliant: false, Rationale: "Inline INDEX (order_no) omits index name."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, email VARCHAR(128) NOT NULL, KEY (email));", ExpectedCompliant: false, Rationale: "Inline KEY (email) omits index name."},
			{ID: "case-8", SQL: "ALTER TABLE orders ADD INDEX (status);", ExpectedCompliant: false, Rationale: "ALTER TABLE ADD INDEX (status) omits index name."},
			{ID: "case-9", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL, UNIQUE INDEX (tracking_no));", ExpectedCompliant: false, Rationale: "Inline UNIQUE INDEX omits explicit name."},
			{ID: "case-10", SQL: "ALTER TABLE customers ADD KEY (username);", ExpectedCompliant: false, Rationale: "ALTER TABLE ADD KEY omits explicit index name."},
		}
	case profileDDLIndexNamePrefix:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE INDEX idx_orders_customer_id ON orders (customer_id);", ExpectedCompliant: true, Rationale: "Index name starts with idx_ prefix."},
			{ID: "case-2", SQL: "CREATE UNIQUE INDEX uniq_orders_order_no ON orders (order_no);", ExpectedCompliant: true, Rationale: "Unique index name starts with uniq_ prefix."},
			{ID: "case-3", SQL: "ALTER TABLE customers ADD INDEX idx_customers_created_at (created_at);", ExpectedCompliant: true, Rationale: "Added index name follows idx_ prefix convention."},
			{ID: "case-4", SQL: "ALTER TABLE customers ADD UNIQUE INDEX uniq_customers_email (email);", ExpectedCompliant: true, Rationale: "Added unique index name follows uniq_ prefix convention."},
			{ID: "case-5", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, tracking_no VARCHAR(64) NOT NULL, INDEX idx_shipments_tracking (tracking_no));", ExpectedCompliant: true, Rationale: "Inline index name starts with idx_."},
			{ID: "case-6", SQL: "CREATE INDEX order_customer_index ON orders (customer_id);", ExpectedCompliant: false, Rationale: "Index name order_customer_index does not start with idx_ or uniq_."},
			{ID: "case-7", SQL: "CREATE UNIQUE INDEX uk_orders_order_no ON orders (order_no);", ExpectedCompliant: false, Rationale: "Index name uk_orders_order_no uses uk_ instead of idx_ or uniq_."},
			{ID: "case-8", SQL: "ALTER TABLE customers ADD INDEX k_customers_created_at (created_at);", ExpectedCompliant: false, Rationale: "Index name k_customers_created_at violates idx_/uniq_ prefix rule."},
			{ID: "case-9", SQL: "CREATE INDEX index_shipments_tracking ON shipments (tracking_no);", ExpectedCompliant: false, Rationale: "Index name starts with index_ instead of idx_."},
			{ID: "case-10", SQL: "CREATE TABLE payments (id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, INDEX payment_order_key (order_id));", ExpectedCompliant: false, Rationale: "Inline index name payment_order_key lacks idx_ or uniq_ prefix."},
		}
	case profileDDLMaxIndexKeyParts:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE INDEX idx_orders_customer ON orders (customer_id);", ExpectedCompliant: true, Rationale: "Single-column index is within max_index_key_parts."},
			{ID: "case-2", SQL: "CREATE INDEX idx_orders_cust_status ON orders (customer_id, status);", ExpectedCompliant: true, Rationale: "Two-column composite index is within max_index_key_parts."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_cust_status_created ON orders (customer_id, status, created_at);", ExpectedCompliant: true, Rationale: "Three-column composite index is within max_index_key_parts."},
			{ID: "case-4", SQL: "ALTER TABLE shipments ADD INDEX idx_shipments_carrier_status (carrier_code, status);", ExpectedCompliant: true, Rationale: "Two-column index in ALTER TABLE complies with the limit."},
			{ID: "case-5", SQL: "CREATE UNIQUE INDEX uniq_user_roles_pair ON user_roles (user_id, role_id);", ExpectedCompliant: true, Rationale: "Two-column unique index complies with the limit."},
			{ID: "case-6", SQL: "CREATE INDEX idx_orders_wide ON orders (customer_id, status, payment_channel, currency_code, region_code, created_at);", ExpectedCompliant: false, Rationale: "Composite index has 6 columns, exceeding max_index_key_parts (5)."},
			{ID: "case-7", SQL: "ALTER TABLE orders ADD INDEX idx_orders_seven_cols (c1, c2, c3, c4, c5, c6, c7);", ExpectedCompliant: false, Rationale: "Composite index has 7 columns, exceeding max_index_key_parts."},
			{ID: "case-8", SQL: "CREATE TABLE trade_ledgers (id BIGINT NOT NULL PRIMARY KEY, a INT, b INT, c INT, d INT, e INT, f INT, INDEX idx_trade_all (a, b, c, d, e, f));", ExpectedCompliant: false, Rationale: "Inline index contains 6 key parts, exceeding max_index_key_parts."},
			{ID: "case-9", SQL: "CREATE INDEX idx_shipments_over_limit ON shipments (warehouse_id, carrier_code, status, country_code, province_code, city_code);", ExpectedCompliant: false, Rationale: "Index contains 6 columns, exceeding the threshold."},
			{ID: "case-10", SQL: "ALTER TABLE customers ADD INDEX idx_customers_profile_wide (username, email, phone, status, tier, region, created_at);", ExpectedCompliant: false, Rationale: "Index contains 7 columns, exceeding max_index_key_parts."},
		}
	case profileDDLMaxIndexCount:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, order_no VARCHAR(64) NOT NULL, INDEX idx_orders_no (order_no));", ExpectedCompliant: true, Rationale: "Table defines 1 secondary index, within max_index_count."},
			{ID: "case-2", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, username VARCHAR(64) NOT NULL, email VARCHAR(128) NOT NULL, UNIQUE INDEX uniq_customers_user (username), INDEX idx_customers_email (email));", ExpectedCompliant: true, Rationale: "Table defines 2 indexes, within max_index_count."},
			{ID: "case-3", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, tracking_no VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL, INDEX idx_ship_order (order_id), INDEX idx_ship_track (tracking_no), INDEX idx_ship_status (status));", ExpectedCompliant: true, Rationale: "Table defines 3 indexes, within max_index_count."},
			{ID: "case-4", SQL: "CREATE TABLE invoices (id BIGINT NOT NULL PRIMARY KEY, invoice_no VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Table has only primary key, within max_index_count."},
			{ID: "case-5", SQL: "ALTER TABLE invoices ADD INDEX idx_invoices_no (invoice_no);", ExpectedCompliant: true, Rationale: "Adds a single index within limit."},
			{ID: "case-6", SQL: "CREATE TABLE orders (id BIGINT NOT NULL PRIMARY KEY, c1 INT, c2 INT, c3 INT, c4 INT, c5 INT, c6 INT, INDEX idx_1 (c1), INDEX idx_2 (c2), INDEX idx_3 (c3), INDEX idx_4 (c4), INDEX idx_5 (c5), INDEX idx_6 (c6));", ExpectedCompliant: false, Rationale: "CREATE TABLE defines 6 secondary indexes, exceeding max_index_count (5)."},
			{ID: "case-7", SQL: "CREATE TABLE customers (id BIGINT NOT NULL PRIMARY KEY, a INT, b INT, c INT, d INT, e INT, f INT, g INT, INDEX idx_a (a), INDEX idx_b (b), INDEX idx_c (c), INDEX idx_d (d), INDEX idx_e (e), INDEX idx_f (f), INDEX idx_g (g));", ExpectedCompliant: false, Rationale: "CREATE TABLE defines 7 secondary indexes, exceeding max_index_count."},
			{ID: "case-8", SQL: "CREATE TABLE payments (id BIGINT NOT NULL PRIMARY KEY, p1 INT, p2 INT, p3 INT, p4 INT, p5 INT, p6 INT, INDEX idx_p1 (p1), INDEX idx_p2 (p2), INDEX idx_p3 (p3), INDEX idx_p4 (p4), INDEX idx_p5 (p5), INDEX idx_p6 (p6));", ExpectedCompliant: false, Rationale: "Table index count (6) exceeds max_index_count."},
			{ID: "case-9", SQL: "CREATE TABLE shipments (id BIGINT NOT NULL PRIMARY KEY, s1 INT, s2 INT, s3 INT, s4 INT, s5 INT, s6 INT, UNIQUE INDEX uniq_s1 (s1), INDEX idx_s2 (s2), INDEX idx_s3 (s3), INDEX idx_s4 (s4), INDEX idx_s5 (s5), INDEX idx_s6 (s6));", ExpectedCompliant: false, Rationale: "Table defines 6 indexes, exceeding max_index_count."},
			{ID: "case-10", SQL: "CREATE TABLE audit_events (id BIGINT NOT NULL PRIMARY KEY, e1 INT, e2 INT, e3 INT, e4 INT, e5 INT, e6 INT, INDEX idx_e1 (e1), INDEX idx_e2 (e2), INDEX idx_e3 (e3), INDEX idx_e4 (e4), INDEX idx_e5 (e5), INDEX idx_e6 (e6));", ExpectedCompliant: false, Rationale: "Table defines 6 indexes, exceeding max_index_count."},
		}
	case profileDDLMultiAlter, profileDDLMultiDDL:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Single ALTER TABLE statement."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '', ADD COLUMN source_channel VARCHAR(32) NOT NULL DEFAULT 'web';", ExpectedCompliant: true, Rationale: "Consolidated single ALTER TABLE statement modifying multiple columns."},
			{ID: "case-3", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '';", ExpectedCompliant: true, Rationale: "Single DDL statement in the submission unit."},
			{ID: "case-4", SQL: "CREATE TABLE order_tags (id BIGINT NOT NULL PRIMARY KEY, tag_name VARCHAR(64) NOT NULL);", ExpectedCompliant: true, Rationale: "Single CREATE TABLE DDL statement."},
			{ID: "case-5", SQL: "ALTER TABLE shipments ADD COLUMN delivered_at TIMESTAMP NULL, ADD INDEX idx_shipments_delivered (delivered_at);", ExpectedCompliant: true, Rationale: "Combined single ALTER TABLE statement."},
			{ID: "case-6", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '';\nALTER TABLE orders ADD COLUMN source_channel VARCHAR(32) NOT NULL DEFAULT 'web';", ExpectedCompliant: false, Rationale: "Contains multiple separate ALTER TABLE statements instead of merging into one."},
			{ID: "case-7", SQL: "ALTER TABLE customers ADD COLUMN phone VARCHAR(32) NOT NULL DEFAULT '';\nALTER TABLE customers ADD COLUMN tier VARCHAR(16) NOT NULL DEFAULT 'basic';", ExpectedCompliant: false, Rationale: "Multiple ALTER TABLE statements in a single batch."},
			{ID: "case-8", SQL: "CREATE TABLE t1 (id BIGINT NOT NULL PRIMARY KEY);\nCREATE TABLE t2 (id BIGINT NOT NULL PRIMARY KEY);", ExpectedCompliant: false, Rationale: "Multiple DDL statements in a single batch."},
			{ID: "case-9", SQL: "ALTER TABLE payments ADD COLUMN fee_cents BIGINT NOT NULL DEFAULT 0;\nALTER TABLE payments ADD INDEX idx_payments_fee (fee_cents);", ExpectedCompliant: false, Rationale: "Two separate ALTER TABLE statements on payments."},
			{ID: "case-10", SQL: "ALTER TABLE shipments ADD COLUMN weight_grams INT NOT NULL DEFAULT 0;\nALTER TABLE shipments ADD COLUMN volume_cm3 INT NOT NULL DEFAULT 0;\nALTER TABLE shipments ADD COLUMN package_count INT NOT NULL DEFAULT 1;", ExpectedCompliant: false, Rationale: "Three separate ALTER TABLE statements in one batch."},
		}

	// ---- DML profiles ----
	case profileDMLRequireWhere:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "UPDATE orders SET status = 'paid' WHERE id = 1001;", ExpectedCompliant: true, Rationale: "UPDATE statement includes an explicit WHERE primary key predicate."},
			{ID: "case-2", SQL: "DELETE FROM user_sessions WHERE user_id = 42 AND expired_at < '2026-01-01 00:00:00';", ExpectedCompliant: true, Rationale: "DELETE statement filters rows with a selective WHERE condition."},
			{ID: "case-3", SQL: "UPDATE inventory_items SET quantity = quantity - 1 WHERE sku_code = 'SKU-900';", ExpectedCompliant: true, Rationale: "Scoped UPDATE with explicit WHERE clause on indexed column."},
			{ID: "case-4", SQL: "DELETE FROM audit_logs WHERE id = 90001;", ExpectedCompliant: true, Rationale: "Scoped DELETE statement with primary key WHERE predicate."},
			{ID: "case-5", SQL: "UPDATE customer_accounts SET login_attempts = 0 WHERE id = 501;", ExpectedCompliant: true, Rationale: "Point UPDATE with explicit WHERE filter."},
			{ID: "case-6", SQL: "UPDATE orders SET status = 'cancelled';", ExpectedCompliant: false, Rationale: "Unbounded UPDATE without a WHERE clause modifies the entire table."},
			{ID: "case-7", SQL: "DELETE FROM user_sessions;", ExpectedCompliant: false, Rationale: "Unbounded DELETE without a WHERE clause removes all rows."},
			{ID: "case-8", SQL: "UPDATE customers SET tier = 'standard' WHERE 1 = 1;", ExpectedCompliant: false, Rationale: "Tautological WHERE 1=1 predicate is equivalent to a full-table update."},
			{ID: "case-9", SQL: "DELETE FROM order_items WHERE TRUE;", ExpectedCompliant: false, Rationale: "Constant TRUE predicate in DELETE bypasses row filtering."},
			{ID: "case-10", SQL: "UPDATE product_prices SET discount_rate = 0;", ExpectedCompliant: false, Rationale: "Missing WHERE clause on UPDATE statement."},
		}
	case profileDMLForbidLimit:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "UPDATE orders SET status = 'paid' WHERE id = 1001;", ExpectedCompliant: true, Rationale: "UPDATE uses deterministic WHERE primary key filter without LIMIT."},
			{ID: "case-2", SQL: "DELETE FROM user_sessions WHERE id = 502;", ExpectedCompliant: true, Rationale: "DELETE uses deterministic WHERE filter without LIMIT."},
			{ID: "case-3", SQL: "UPDATE inventory_items SET quantity = 10 WHERE sku_code = 'SKU-100';", ExpectedCompliant: true, Rationale: "UPDATE statement does not use a LIMIT clause."},
			{ID: "case-4", SQL: "DELETE FROM expired_tokens WHERE id BETWEEN 1000 AND 1100;", ExpectedCompliant: true, Rationale: "Range DELETE by primary key without LIMIT."},
			{ID: "case-5", SQL: "UPDATE shipments SET status = 'delivered' WHERE id = 3001;", ExpectedCompliant: true, Rationale: "Point UPDATE without LIMIT clause."},
			{ID: "case-6", SQL: "UPDATE orders SET status = 'cancelled' WHERE status = 'pending' LIMIT 100;", ExpectedCompliant: false, Rationale: "UPDATE statement uses prohibited LIMIT clause."},
			{ID: "case-7", SQL: "DELETE FROM user_sessions WHERE expired_at < '2026-01-01' LIMIT 500;", ExpectedCompliant: false, Rationale: "DELETE statement uses prohibited LIMIT clause."},
			{ID: "case-8", SQL: "UPDATE inventory_items SET quantity = 0 WHERE warehouse_id = 2 LIMIT 10;", ExpectedCompliant: false, Rationale: "UPDATE with LIMIT causes non-deterministic replication behavior."},
			{ID: "case-9", SQL: "DELETE FROM audit_logs WHERE created_at < '2025-01-01' LIMIT 1000;", ExpectedCompliant: false, Rationale: "DELETE statement contains LIMIT 1000."},
			{ID: "case-10", SQL: "UPDATE customers SET tier = 'silver' WHERE points > 100 LIMIT 50;", ExpectedCompliant: false, Rationale: "UPDATE statement contains LIMIT 50."},
		}
	case profileDMLForbidOrderBy:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "UPDATE orders SET status = 'paid' WHERE id = 1001;", ExpectedCompliant: true, Rationale: "UPDATE statement does not contain an ORDER BY clause."},
			{ID: "case-2", SQL: "DELETE FROM user_sessions WHERE id = 502;", ExpectedCompliant: true, Rationale: "DELETE statement does not contain an ORDER BY clause."},
			{ID: "case-3", SQL: "UPDATE inventory_items SET quantity = 10 WHERE sku_code = 'SKU-100';", ExpectedCompliant: true, Rationale: "Scoped UPDATE without ORDER BY."},
			{ID: "case-4", SQL: "DELETE FROM expired_tokens WHERE id BETWEEN 100 AND 200;", ExpectedCompliant: true, Rationale: "Scoped DELETE without ORDER BY."},
			{ID: "case-5", SQL: "UPDATE shipments SET status = 'delivered' WHERE id = 3001;", ExpectedCompliant: true, Rationale: "Point UPDATE without ORDER BY."},
			{ID: "case-6", SQL: "UPDATE orders SET status = 'cancelled' WHERE customer_id = 42 ORDER BY created_at ASC;", ExpectedCompliant: false, Rationale: "UPDATE statement uses prohibited ORDER BY clause."},
			{ID: "case-7", SQL: "DELETE FROM user_sessions WHERE user_id = 42 ORDER BY id DESC;", ExpectedCompliant: false, Rationale: "DELETE statement uses prohibited ORDER BY clause."},
			{ID: "case-8", SQL: "UPDATE inventory_items SET quantity = 0 WHERE warehouse_id = 1 ORDER BY updated_at;", ExpectedCompliant: false, Rationale: "UPDATE contains ORDER BY updated_at."},
			{ID: "case-9", SQL: "DELETE FROM audit_logs WHERE actor_id = 99 ORDER BY created_at;", ExpectedCompliant: false, Rationale: "DELETE contains ORDER BY created_at."},
			{ID: "case-10", SQL: "UPDATE customers SET tier = 'gold' WHERE region_id = 3 ORDER BY id;", ExpectedCompliant: false, Rationale: "UPDATE contains ORDER BY id."},
		}
	case profileDMLForbidSubselect:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "INSERT INTO order_events (id, order_id, event_type) VALUES (1, 1001, 'created');", ExpectedCompliant: true, Rationale: "INSERT uses literal VALUES without a SELECT subquery."},
			{ID: "case-2", SQL: "UPDATE orders SET status = 'paid' WHERE id = 1001;", ExpectedCompliant: true, Rationale: "UPDATE uses literal WHERE filter without a subquery."},
			{ID: "case-3", SQL: "DELETE FROM user_sessions WHERE id = 502;", ExpectedCompliant: true, Rationale: "DELETE uses literal WHERE filter without a subquery."},
			{ID: "case-4", SQL: "UPDATE inventory_items SET quantity = quantity - 1 WHERE id = 88;", ExpectedCompliant: true, Rationale: "Direct UPDATE without nested SELECT."},
			{ID: "case-5", SQL: "INSERT INTO customers (id, username, email) VALUES (10, 'alice', 'alice@example.com');", ExpectedCompliant: true, Rationale: "Direct INSERT VALUES statement without subquery."},
			{ID: "case-6", SQL: "INSERT INTO orders_archive (id, order_no) SELECT id, order_no FROM orders WHERE created_at < '2025-01-01';", ExpectedCompliant: false, Rationale: "INSERT ... SELECT embeds a SELECT subquery inside DML."},
			{ID: "case-7", SQL: "UPDATE orders SET status = 'vip' WHERE customer_id IN (SELECT id FROM customers WHERE tier = 'gold');", ExpectedCompliant: false, Rationale: "UPDATE WHERE clause embeds a SELECT subquery."},
			{ID: "case-8", SQL: "DELETE FROM user_sessions WHERE user_id IN (SELECT id FROM users WHERE status = 'disabled');", ExpectedCompliant: false, Rationale: "DELETE WHERE clause embeds a SELECT subquery."},
			{ID: "case-9", SQL: "UPDATE product_prices SET price = (SELECT AVG(price) FROM product_prices) WHERE id = 10;", ExpectedCompliant: false, Rationale: "UPDATE SET clause embeds a scalar SELECT subquery."},
			{ID: "case-10", SQL: "DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE status = 'cancelled');", ExpectedCompliant: false, Rationale: "DELETE statement contains a nested SELECT subquery."},
		}
	case profileDMLInsertExplicitCols:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "INSERT INTO orders (id, order_no, customer_id, status) VALUES (1001, 'ORD-1001', 42, 'pending');", ExpectedCompliant: true, Rationale: "INSERT explicitly declares the target column list."},
			{ID: "case-2", SQL: "INSERT INTO customers (id, username, email) VALUES (1, 'alice', 'alice@example.com');", ExpectedCompliant: true, Rationale: "INSERT explicitly specifies (id, username, email)."},
			{ID: "case-3", SQL: "INSERT INTO order_items (id, order_id, sku_code, quantity) VALUES (10, 1001, 'SKU-01', 2);", ExpectedCompliant: true, Rationale: "INSERT includes explicit target column names."},
			{ID: "case-4", SQL: "INSERT INTO payments (id, order_id, amount_cents) VALUES (501, 1001, 9900);", ExpectedCompliant: true, Rationale: "INSERT lists all target columns explicitly."},
			{ID: "case-5", SQL: "INSERT INTO shipments (id, order_id, tracking_no) VALUES (801, 1001, 'SF100200300');", ExpectedCompliant: true, Rationale: "INSERT declares explicit column names."},
			{ID: "case-6", SQL: "INSERT INTO orders VALUES (1001, 'ORD-1001', 42, 'pending');", ExpectedCompliant: false, Rationale: "INSERT omits the target column list before VALUES."},
			{ID: "case-7", SQL: "INSERT INTO customers VALUES (1, 'alice', 'alice@example.com');", ExpectedCompliant: false, Rationale: "Positional INSERT without explicit column names."},
			{ID: "case-8", SQL: "INSERT INTO order_items VALUES (10, 1001, 'SKU-01', 2);", ExpectedCompliant: false, Rationale: "INSERT omits explicit column list."},
			{ID: "case-9", SQL: "INSERT INTO payments VALUES (501, 1001, 9900);", ExpectedCompliant: false, Rationale: "INSERT does not declare target columns."},
			{ID: "case-10", SQL: "INSERT INTO shipments VALUES (801, 1001, 'SF100200300');", ExpectedCompliant: false, Rationale: "INSERT statement lacks explicit column specification."},
		}
	case profileDMLInsertMaxBatchRows:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "INSERT INTO orders (id, order_no) VALUES (1, 'ORD-1');", ExpectedCompliant: true, Rationale: "Single-row INSERT is well within max_batch_rows."},
			{ID: "case-2", SQL: "INSERT INTO orders (id, order_no) VALUES (1, 'ORD-1'), (2, 'ORD-2');", ExpectedCompliant: true, Rationale: "Two-row batch INSERT is within max_batch_rows."},
			{ID: "case-3", SQL: "INSERT INTO customers (id, username) VALUES (10, 'alice'), (11, 'bob'), (12, 'charlie');", ExpectedCompliant: true, Rationale: "Three-row batch INSERT is within max_batch_rows."},
			{ID: "case-4", SQL: "INSERT INTO order_items (id, order_id, quantity) VALUES (1, 100, 1), (2, 100, 3);", ExpectedCompliant: true, Rationale: "Small batch INSERT complies with row limit."},
			{ID: "case-5", SQL: "INSERT INTO payments (id, order_id, amount_cents) VALUES (1, 100, 5000);", ExpectedCompliant: true, Rationale: "Single-row INSERT complies with row limit."},
			{ID: "case-6", SQL: "INSERT INTO orders (id, order_no) VALUES (1,'O1'),(2,'O2'),(3,'O3'),(4,'O4'),(5,'O5'),(6,'O6'),(7,'O7'),(8,'O8'),(9,'O9'),(10,'O10'),(11,'O11');", ExpectedCompliant: false, Rationale: "Multi-row batch INSERT exceeds the configured batch threshold."},
			{ID: "case-7", SQL: "INSERT INTO customers (id, username) VALUES (1,'u1'),(2,'u2'),(3,'u3'),(4,'u4'),(5,'u5'),(6,'u6'),(7,'u7'),(8,'u8'),(9,'u9'),(10,'u10'),(11,'u11');", ExpectedCompliant: false, Rationale: "Batch INSERT row count exceeds the configured limit."},
			{ID: "case-8", SQL: "INSERT INTO order_items (id, order_id) VALUES (1,1),(2,1),(3,1),(4,1),(5,1),(6,1),(7,1),(8,1),(9,1),(10,1),(11,1),(12,1);", ExpectedCompliant: false, Rationale: "Batch INSERT has 12 value tuples, exceeding the threshold."},
			{ID: "case-9", SQL: "INSERT INTO payments (id, amount_cents) VALUES (1,10),(2,20),(3,30),(4,40),(5,50),(6,60),(7,70),(8,80),(9,90),(10,100),(11,110);", ExpectedCompliant: false, Rationale: "Oversized multi-row INSERT batch."},
			{ID: "case-10", SQL: "INSERT INTO audit_logs (id, event_name) VALUES (1,'e1'),(2,'e2'),(3,'e3'),(4,'e4'),(5,'e5'),(6,'e6'),(7,'e7'),(8,'e8'),(9,'e9'),(10,'e10'),(11,'e11');", ExpectedCompliant: false, Rationale: "Oversized VALUES tuple list in INSERT statement."},
		}
	case profileDMLWhereNotNullLit:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "UPDATE orders SET status = 'closed' WHERE deleted_at IS NULL AND id = 1001;", ExpectedCompliant: true, Rationale: "Uses standard IS NULL operator in WHERE predicate."},
			{ID: "case-2", SQL: "DELETE FROM user_sessions WHERE revoked_at IS NOT NULL AND id = 502;", ExpectedCompliant: true, Rationale: "Uses standard IS NOT NULL operator in WHERE predicate."},
			{ID: "case-3", SQL: "UPDATE customers SET tier = 'active' WHERE suspended_at IS NULL AND id = 42;", ExpectedCompliant: true, Rationale: "Checks nullability with IS NULL instead of = NULL."},
			{ID: "case-4", SQL: "DELETE FROM webhook_logs WHERE delivered_at IS NOT NULL AND id = 99;", ExpectedCompliant: true, Rationale: "Uses IS NOT NULL in DELETE WHERE clause."},
			{ID: "case-5", SQL: "UPDATE shipments SET delivered_at = '2026-11-09 10:00:00' WHERE delivered_at IS NULL AND id = 301;", ExpectedCompliant: true, Rationale: "Uses IS NULL comparison correctly."},
			{ID: "case-6", SQL: "UPDATE orders SET status = 'closed' WHERE deleted_at = NULL;", ExpectedCompliant: false, Rationale: "Uses invalid = NULL literal comparison in WHERE clause."},
			{ID: "case-7", SQL: "DELETE FROM user_sessions WHERE revoked_at != NULL;", ExpectedCompliant: false, Rationale: "Uses invalid != NULL literal comparison in WHERE clause."},
			{ID: "case-8", SQL: "UPDATE customers SET tier = 'active' WHERE suspended_at <> NULL;", ExpectedCompliant: false, Rationale: "Uses invalid <> NULL literal comparison in WHERE clause."},
			{ID: "case-9", SQL: "DELETE FROM webhook_logs WHERE error_msg = NULL;", ExpectedCompliant: false, Rationale: "WHERE error_msg = NULL evaluates to UNKNOWN instead of filtering NULL rows."},
			{ID: "case-10", SQL: "UPDATE shipments SET status = 'lost' WHERE tracking_no = NULL;", ExpectedCompliant: false, Rationale: "Uses = NULL literal comparison in WHERE predicate."},
		}
	case profileDMLInsertForbidNull:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "INSERT INTO orders (id, order_no, remark) VALUES (1001, 'ORD-1001', '');", ExpectedCompliant: true, Rationale: "Inserts non-null empty string default instead of NULL literal."},
			{ID: "case-2", SQL: "INSERT INTO customers (id, username, phone) VALUES (42, 'alice', '13800000000');", ExpectedCompliant: true, Rationale: "All inserted values are non-null literals."},
			{ID: "case-3", SQL: "INSERT INTO payments (id, order_id, amount_cents) VALUES (501, 1001, 5000);", ExpectedCompliant: true, Rationale: "Inserts concrete integer values without NULL."},
			{ID: "case-4", SQL: "INSERT INTO shipments (id, order_id, tracking_no) VALUES (801, 1001, 'SF1001');", ExpectedCompliant: true, Rationale: "Does not insert explicit NULL literals."},
			{ID: "case-5", SQL: "INSERT INTO inventory_items (id, sku_code, quantity) VALUES (901, 'SKU-901', 0);", ExpectedCompliant: true, Rationale: "Uses explicit 0 instead of NULL."},
			{ID: "case-6", SQL: "INSERT INTO orders (id, order_no, remark) VALUES (1001, 'ORD-1001', NULL);", ExpectedCompliant: false, Rationale: "Explicitly inserts NULL literal into remark column."},
			{ID: "case-7", SQL: "INSERT INTO customers (id, username, phone) VALUES (42, 'alice', NULL);", ExpectedCompliant: false, Rationale: "Explicitly inserts NULL literal into phone column."},
			{ID: "case-8", SQL: "INSERT INTO payments (id, order_id, paid_at) VALUES (501, 1001, NULL);", ExpectedCompliant: false, Rationale: "Explicitly inserts NULL literal into paid_at."},
			{ID: "case-9", SQL: "INSERT INTO shipments (id, order_id, tracking_no) VALUES (801, NULL, NULL);", ExpectedCompliant: false, Rationale: "Explicitly inserts NULL literals in VALUES clause."},
			{ID: "case-10", SQL: "INSERT INTO inventory_items (id, sku_code, warehouse_code) VALUES (901, 'SKU-901', NULL);", ExpectedCompliant: false, Rationale: "Explicitly inserts NULL literal."},
		}
	case profileDMLMaxInValues, profileDMLImplicitConversion:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "UPDATE orders SET status = 'paid' WHERE id IN (1001, 1002, 1003);", ExpectedCompliant: true, Rationale: "Small IN list with matching integer literals."},
			{ID: "case-2", SQL: "DELETE FROM user_sessions WHERE id IN (10, 11);", ExpectedCompliant: true, Rationale: "Bounded IN list with 2 items."},
			{ID: "case-3", SQL: "UPDATE inventory_items SET quantity = 0 WHERE sku_code IN ('SKU-1', 'SKU-2');", ExpectedCompliant: true, Rationale: "String column compared against string literals in short IN list."},
			{ID: "case-4", SQL: "UPDATE customers SET tier = 'vip' WHERE id = 42;", ExpectedCompliant: true, Rationale: "Equality filter with matching column type."},
			{ID: "case-5", SQL: "DELETE FROM order_items WHERE id IN (501, 502, 503);", ExpectedCompliant: true, Rationale: "Compliant DML WHERE predicate."},
			{ID: "case-6", SQL: "UPDATE orders SET status = 'paid' WHERE id IN (1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20);", ExpectedCompliant: false, Rationale: "IN list literal count exceeds the configured threshold."},
			{ID: "case-7", SQL: "DELETE FROM user_sessions WHERE id IN (101,102,103,104,105,106,107,108,109,110,111,112,113,114,115);", ExpectedCompliant: false, Rationale: "Oversized IN list in DELETE statement."},
			{ID: "case-8", SQL: "UPDATE orders SET status = 'paid' WHERE order_no = 10012002;", ExpectedCompliant: false, Rationale: "Compares VARCHAR column order_no with numeric literal, causing implicit type conversion."},
			{ID: "case-9", SQL: "UPDATE customers SET status = 1 WHERE phone = 13800138000;", ExpectedCompliant: false, Rationale: "Compares string column phone with integer literal."},
			{ID: "case-10", SQL: "DELETE FROM shipments WHERE tracking_no = 99887766;", ExpectedCompliant: false, Rationale: "Implicit type conversion on string column tracking_no."},
		}
	case profileDMLGeneric:
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "UPDATE orders SET status = 'paid' WHERE id = 1001;", ExpectedCompliant: true, Rationale: "Scoped UPDATE statement with explicit primary key WHERE filter."},
			{ID: "case-2", SQL: "INSERT INTO orders (id, order_no, customer_id, status) VALUES (1002, 'ORD-1002', 42, 'pending');", ExpectedCompliant: true, Rationale: "INSERT statement with explicit column list and non-null values."},
			{ID: "case-3", SQL: "DELETE FROM user_sessions WHERE id = 5001 AND expired_at < '2026-01-01 00:00:00';", ExpectedCompliant: true, Rationale: "Scoped DELETE statement with selective WHERE predicate."},
			{ID: "case-4", SQL: "UPDATE inventory_items SET quantity = quantity - 1 WHERE id = 301 AND quantity >= 1;", ExpectedCompliant: true, Rationale: "Safe point UPDATE statement."},
			{ID: "case-5", SQL: "INSERT INTO payment_records (id, order_id, amount_cents) VALUES (901, 1002, 15000);", ExpectedCompliant: true, Rationale: "Well-formed single-row INSERT statement."},
			{ID: "case-6", SQL: "UPDATE orders SET status = 'cancelled';", ExpectedCompliant: false, Rationale: "Unbounded UPDATE statement without a WHERE clause."},
			{ID: "case-7", SQL: "DELETE FROM user_sessions;", ExpectedCompliant: false, Rationale: "Unbounded DELETE statement without a WHERE clause."},
			{ID: "case-8", SQL: "INSERT INTO orders VALUES (1003, 'ORD-1003', 42, NULL);", ExpectedCompliant: false, Rationale: "INSERT omits explicit column list and inserts NULL literal."},
			{ID: "case-9", SQL: "UPDATE orders SET status = 'closed' WHERE deleted_at = NULL LIMIT 100;", ExpectedCompliant: false, Rationale: "UPDATE uses = NULL comparison and LIMIT clause."},
			{ID: "case-10", SQL: "DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE status = 'draft') ORDER BY id;", ExpectedCompliant: false, Rationale: "DELETE uses nested SELECT subquery and ORDER BY clause."},
		}
	default:
		// profileDDLGeneric: 10 pure DDL statements (5 compliant DDL + 5 non-compliant DDL), zero SELECT or DML.
		return []PromptToolTestCase{
			{ID: "case-1", SQL: "CREATE TABLE orders (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT 'Primary key', order_no VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'Order number') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Customer orders';", ExpectedCompliant: true, Rationale: "Well-formed CREATE TABLE with explicit id primary key, NOT NULL, DEFAULT, and comments."},
			{ID: "case-2", SQL: "ALTER TABLE orders ADD COLUMN remark VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'Order remark';", ExpectedCompliant: true, Rationale: "Safe additive ALTER TABLE ADD COLUMN with NOT NULL, DEFAULT, and COMMENT."},
			{ID: "case-3", SQL: "CREATE INDEX idx_orders_order_no ON orders (order_no);", ExpectedCompliant: true, Rationale: "Compliant CREATE INDEX with idx_ prefix."},
			{ID: "case-4", SQL: "CREATE UNIQUE INDEX uniq_orders_external_code ON orders (order_no);", ExpectedCompliant: true, Rationale: "Compliant CREATE UNIQUE INDEX with uniq_ prefix."},
			{ID: "case-5", SQL: "CREATE TABLE payment_records (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT 'Primary key', amount DECIMAL(18,2) NOT NULL DEFAULT 0.00 COMMENT 'Payment amount') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Payment records';", ExpectedCompliant: true, Rationale: "Well-formed CREATE TABLE using DECIMAL(18,2) and full constraints."},
			{ID: "case-6", SQL: "DROP TABLE orders;", ExpectedCompliant: false, Rationale: "Destructive DROP TABLE DDL operation."},
			{ID: "case-7", SQL: "TRUNCATE TABLE payment_records;", ExpectedCompliant: false, Rationale: "Destructive TRUNCATE TABLE DDL operation."},
			{ID: "case-8", SQL: "CREATE TABLE temp_logs (raw_text TEXT, amount FLOAT);", ExpectedCompliant: false, Rationale: "CREATE TABLE lacks PRIMARY KEY, NOT NULL, DEFAULT, and comments, and uses TEXT/FLOAT."},
			{ID: "case-9", SQL: "ALTER TABLE orders MODIFY COLUMN order_no CHAR(128) NULL FIRST;", ExpectedCompliant: false, Rationale: "ALTER TABLE modifies column type to oversized CHAR(128) NULL and reorders with FIRST."},
			{ID: "case-10", SQL: "CREATE INDEX bad_index_name ON orders (c1, c2, c3, c4, c5, c6);", ExpectedCompliant: false, Rationale: "Index lacks idx_/uniq_ prefix and exceeds maximum key parts."},
		}
	}
}

func evaluateSQLAgainstRuleDeterministic(sqlText, name, description, renderedRule string, params map[string]any) (bool, string) {
	upper := strings.ToUpper(strings.TrimSpace(sqlText))
	prof := detectRuleProfile(name, description, renderedRule)

	switch prof {
	case profileDDLRequirePK:
		if strings.HasPrefix(upper, "CREATE TABLE") && !strings.Contains(upper, "PRIMARY KEY") {
			return false, "Violation: CREATE TABLE statement does not declare a PRIMARY KEY."
		}
		return true, "Compliant: CREATE TABLE declares an explicit PRIMARY KEY."
	case profileDDLPKNamedID:
		if strings.HasPrefix(upper, "CREATE TABLE") {
			if !strings.Contains(upper, "(ID ") && !strings.Contains(upper, "PRIMARY KEY (ID)") && !strings.Contains(upper, "PRIMARY KEY (`ID`)") {
				return false, "Violation: primary key column is not named id."
			}
		}
		return true, "Compliant: primary key column is named id."
	case profileDDLPKInteger:
		if strings.HasPrefix(upper, "CREATE TABLE") {
			if strings.Contains(upper, "VARCHAR") && strings.Contains(upper, "ID VARCHAR") ||
				strings.Contains(upper, "ID CHAR") || strings.Contains(upper, "ID TEXT") || strings.Contains(upper, "ID BINARY") {
				return false, "Violation: primary key column does not use an integer data type."
			}
		}
		return true, "Compliant: primary key column uses an integer data type."
	case profileDDLPKAutoIncrement:
		if strings.HasPrefix(upper, "CREATE TABLE") && !strings.Contains(upper, "AUTO_INCREMENT") {
			return false, "Violation: primary key column does not declare AUTO_INCREMENT."
		}
		return true, "Compliant: primary key column declares AUTO_INCREMENT."
	case profileDDLAutoIncUnsigned:
		if strings.Contains(upper, "AUTO_INCREMENT") && !strings.Contains(upper, "UNSIGNED") {
			return false, "Violation: AUTO_INCREMENT column omits UNSIGNED."
		}
		return true, "Compliant: AUTO_INCREMENT column declares UNSIGNED."
	case profileDDLAutoIncInitOne:
		if strings.Contains(upper, "AUTO_INCREMENT=") || strings.Contains(upper, "AUTO_INCREMENT =") {
			if !strings.Contains(upper, "AUTO_INCREMENT=1") && !strings.Contains(upper, "AUTO_INCREMENT = 1") {
				return false, "Violation: table option sets initial AUTO_INCREMENT to a value other than 1."
			}
		}
		return true, "Compliant: initial AUTO_INCREMENT value is 1 or default."
	case profileDDLTableComment:
		if strings.HasPrefix(upper, "CREATE TABLE") {
			if !strings.Contains(upper, "COMMENT=") && !strings.Contains(upper, "COMMENT =") || strings.Contains(upper, "COMMENT=''") {
				return false, "Violation: CREATE TABLE statement is missing a non-empty table COMMENT."
			}
		}
		return true, "Compliant: CREATE TABLE includes a table COMMENT."
	case profileDDLColumnComments:
		if !strings.Contains(upper, "COMMENT '") || strings.Contains(upper, "COMMENT ''") ||
			(strings.HasPrefix(upper, "CREATE TABLE") && strings.Count(upper, "COMMENT '") < strings.Count(upper, ",")+1) {
			return false, "Violation: one or more columns are missing a non-empty COMMENT."
		}
		return true, "Compliant: all columns include non-empty comments."
	case profileDDLColumnNullable:
		if strings.Contains(upper, " DEFAULT NULL") || strings.HasSuffix(upper, " NULL;") ||
			(strings.HasPrefix(upper, "CREATE TABLE") && strings.Count(upper, "NOT NULL") < strings.Count(upper, ",")+1) ||
			(strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD COLUMN") && !strings.Contains(upper, "NOT NULL")) {
			return false, "Violation: DDL statement contains nullable column definition without NOT NULL."
		}
		return true, "Compliant: all columns explicitly declare NOT NULL."
	case profileDDLColumnDefault:
		if (strings.HasPrefix(upper, "CREATE TABLE") && !strings.Contains(upper, "DEFAULT ")) ||
			(strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD COLUMN") && !strings.Contains(upper, "DEFAULT ")) {
			return false, "Violation: column definition omits an explicit DEFAULT value."
		}
		return true, "Compliant: columns define explicit DEFAULT values."
	case profileDDLMaxCharLength:
		if strings.Contains(upper, "CHAR(64)") || strings.Contains(upper, "CHAR(128)") || strings.Contains(upper, "CHAR(255)") ||
			strings.Contains(upper, "VARCHAR(4096)") || strings.Contains(upper, "VARCHAR(8192)") {
			return false, "Violation: CHAR/VARCHAR length exceeds the configured maximum threshold."
		}
		return true, "Compliant: CHAR/VARCHAR column lengths are within the threshold."
	case profileDDLMaxTableNameLength:
		if strings.Contains(upper, "_RECORDS") || strings.Contains(upper, "_CHECKPOINTS") || strings.Contains(upper, "_MAPPINGS") ||
			strings.Contains(upper, "_SNAPSHOTS") || strings.Contains(upper, "_LEDGERS") {
			return false, "Violation: table identifier length exceeds max_table_name_length."
		}
		return true, "Compliant: table identifier length is within max_table_name_length."
	case profileDDLIdentifierKeyword:
		if strings.Contains(upper, "TABLE SELECT ") || strings.Contains(upper, "TABLE ORDER ") ||
			strings.Contains(upper, "TABLE TABLE ") || strings.Contains(upper, "COLUMN FROM ") || strings.Contains(upper, "TABLE VALUES ") {
			return false, "Violation: DDL identifier uses a reserved SQL keyword."
		}
		return true, "Compliant: DDL identifiers avoid reserved SQL keywords."
	case profileDDLAcrossDBRename:
		if (strings.Contains(upper, "RENAME TABLE") || strings.Contains(upper, "RENAME TO")) && strings.Contains(upper, ".") {
			return false, "Violation: statement renames a table across database boundaries."
		}
		return true, "Compliant: DDL does not perform a cross-database table rename."
	case profileDDLCharsetAllowlist:
		if strings.Contains(upper, "LATIN1") || strings.Contains(upper, "GBK") || strings.Contains(upper, "GB2312") ||
			strings.Contains(upper, "ASCII") || strings.Contains(upper, "BIG5") {
			return false, "Violation: statement specifies a character set outside the allowed list."
		}
		return true, "Compliant: character set is within the allowed list."
	case profileDDLCollationAllowlist:
		if strings.Contains(upper, "LATIN1_") || strings.Contains(upper, "GBK_") || strings.Contains(upper, "ASCII_") || strings.Contains(upper, "BIG5_") {
			return false, "Violation: statement specifies a collation outside the allowed list."
		}
		return true, "Compliant: collation is within the allowed list."
	case profileDDLForbidDropDatabase:
		if strings.Contains(upper, "DROP DATABASE") || strings.Contains(upper, "DROP SCHEMA") {
			return false, "Violation: DROP DATABASE / DROP SCHEMA is prohibited."
		}
		return true, "Compliant: DDL does not drop a database or schema."
	case profileDDLForbidDropOther:
		if strings.Contains(upper, "DROP INDEX") || strings.Contains(upper, "DROP VIEW") ||
			strings.Contains(upper, "DROP TRIGGER") || strings.Contains(upper, "DROP PROCEDURE") || strings.Contains(upper, "DROP FUNCTION") {
			return false, "Violation: dropping view/index/trigger/procedure/function objects is blocked."
		}
		return true, "Compliant: DDL does not drop protected database objects."
	case profileDDLForbidTruncate:
		if strings.HasPrefix(upper, "TRUNCATE ") {
			return false, "Violation: TRUNCATE statement is prohibited."
		}
		return true, "Compliant: DDL does not use TRUNCATE."
	case profileDDLForbidDropTable:
		if strings.Contains(upper, "DROP TABLE") {
			return false, "Violation: DROP TABLE statement is prohibited."
		}
		return true, "Compliant: DDL does not drop any table."
	case profileDDLForbidColTypeChange:
		if strings.Contains(upper, "MODIFY COLUMN") || strings.Contains(upper, "CHANGE COLUMN") || strings.Contains(upper, "ALTER COLUMN") {
			return false, "Violation: ALTER TABLE modifies or changes an existing column type."
		}
		return true, "Compliant: DDL does not modify existing column data types."
	case profileDDLForbidColPosChange:
		if strings.Contains(upper, " AFTER ") || strings.HasSuffix(upper, " FIRST;") || strings.Contains(upper, " FIRST ") {
			return false, "Violation: ALTER TABLE uses FIRST or AFTER to reorder column position."
		}
		return true, "Compliant: DDL does not reorder column positions."
	case profileDDLFloatDoubleDecimal:
		if strings.Contains(upper, " FLOAT") || strings.Contains(upper, " DOUBLE") || strings.Contains(upper, " REAL ") {
			return false, "Violation: DDL uses approximate FLOAT/DOUBLE/REAL instead of DECIMAL."
		}
		return true, "Compliant: DDL uses exact DECIMAL/NUMERIC types."
	case profileDDLSpecialType:
		if strings.Contains(upper, " ENUM(") || strings.Contains(upper, " TEXT") || strings.Contains(upper, " BLOB") ||
			strings.Contains(upper, " JSON ") || strings.Contains(upper, " GEOMETRY ") {
			return false, "Violation: DDL uses a restricted special data type (ENUM/TEXT/BLOB/JSON/GEOMETRY)."
		}
		return true, "Compliant: DDL uses standard scalar data types."
	case profileDDLForbidForeignKey:
		if strings.Contains(upper, "FOREIGN KEY") || strings.Contains(upper, "REFERENCES ") {
			return false, "Violation: DDL defines a FOREIGN KEY constraint."
		}
		return true, "Compliant: DDL does not define a FOREIGN KEY constraint."
	case profileDDLViewForbidSelectStar:
		if strings.Contains(upper, "VIEW ") && (strings.Contains(upper, "SELECT *") || strings.Contains(upper, ".*")) {
			return false, "Violation: CREATE VIEW uses wildcard SELECT *."
		}
		return true, "Compliant: CREATE VIEW explicitly enumerates projected columns."
	case profileDDLAllowCreateView:
		if strings.Contains(upper, "CREATE VIEW") || strings.Contains(upper, "CREATE OR REPLACE VIEW") {
			return false, "Violation: CREATE VIEW is blocked for governance review."
		}
		return true, "Compliant: DDL does not create a view."
	case profileDDLAllowPartition:
		if strings.Contains(upper, "PARTITION BY") {
			return false, "Violation: partitioned table DDL requires governance review."
		}
		return true, "Compliant: DDL does not use table partitioning."
	case profileDDLForbidUnnamedIndex:
		if strings.Contains(upper, "INDEX (") || strings.Contains(upper, "KEY (") {
			return false, "Violation: DDL defines an unnamed index."
		}
		return true, "Compliant: all indexes are explicitly named."
	case profileDDLIndexNamePrefix:
		if (strings.Contains(upper, "INDEX ") || strings.Contains(upper, "KEY ")) &&
			!strings.Contains(upper, "IDX_") && !strings.Contains(upper, "UNIQ_") && !strings.Contains(upper, "PRIMARY KEY") {
			return false, "Violation: index name does not start with idx_ or uniq_."
		}
		return true, "Compliant: index name follows idx_ or uniq_ prefix convention."
	case profileDDLMaxIndexKeyParts:
		if strings.Contains(upper, "C5, C6") || strings.Contains(upper, "E, F") || strings.Contains(upper, "REGION_CODE, CREATED_AT") ||
			strings.Contains(upper, "PROVINCE_CODE, CITY_CODE") || strings.Contains(upper, "REGION, CREATED_AT") {
			return false, "Violation: index key part count exceeds max_index_key_parts."
		}
		return true, "Compliant: index key part count is within max_index_key_parts."
	case profileDDLMaxIndexCount:
		if strings.Count(upper, "INDEX ") >= 6 {
			return false, "Violation: single-table index count exceeds max_index_count."
		}
		return true, "Compliant: single-table index count is within max_index_count."
	case profileDDLMultiAlter, profileDDLMultiDDL:
		if strings.Count(upper, "ALTER TABLE") > 1 || strings.Count(upper, "CREATE TABLE") > 1 {
			return false, "Violation: batch contains multiple separate DDL/ALTER statements."
		}
		return true, "Compliant: batch contains a single DDL statement."

	// ---- DML evaluators ----
	case profileDMLRequireWhere:
		if (strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "DELETE ")) &&
			(!strings.Contains(upper, " WHERE ") || strings.Contains(upper, "WHERE 1 = 1") || strings.Contains(upper, "WHERE 1=1") || strings.Contains(upper, "WHERE TRUE")) {
			return false, "Violation: UPDATE/DELETE statement is missing an effective WHERE filter predicate."
		}
		return true, "Compliant: DML statement includes a selective WHERE predicate."
	case profileDMLForbidLimit:
		if (strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "DELETE ")) && strings.Contains(upper, " LIMIT ") {
			return false, "Violation: UPDATE/DELETE statement uses a prohibited LIMIT clause."
		}
		return true, "Compliant: UPDATE/DELETE statement does not use LIMIT."
	case profileDMLForbidOrderBy:
		if (strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "DELETE ")) && strings.Contains(upper, " ORDER BY ") {
			return false, "Violation: UPDATE/DELETE statement uses a prohibited ORDER BY clause."
		}
		return true, "Compliant: UPDATE/DELETE statement does not use ORDER BY."
	case profileDMLForbidSubselect:
		if strings.Contains(upper, "SELECT ") {
			return false, "Violation: DML statement contains an embedded SELECT subquery."
		}
		return true, "Compliant: DML statement does not contain any SELECT subquery."
	case profileDMLInsertExplicitCols:
		if strings.HasPrefix(upper, "INSERT INTO ") {
			beforeValues := strings.SplitN(upper, "VALUES", 2)[0]
			if !strings.Contains(beforeValues, "(") {
				return false, "Violation: INSERT statement omits the explicit target column list."
			}
		}
		return true, "Compliant: INSERT statement explicitly lists target columns."
	case profileDMLInsertMaxBatchRows:
		if strings.Count(upper, "),(") >= 10 || strings.Count(upper, "), (") >= 10 {
			return false, "Violation: batch INSERT row count exceeds max_batch_rows."
		}
		return true, "Compliant: INSERT row count is within max_batch_rows."
	case profileDMLWhereNotNullLit:
		if strings.Contains(upper, "= NULL") || strings.Contains(upper, "!= NULL") || strings.Contains(upper, "<> NULL") {
			return false, "Violation: WHERE clause compares against a NULL literal instead of using IS [NOT] NULL."
		}
		return true, "Compliant: WHERE clause uses IS NULL / IS NOT NULL properly."
	case profileDMLInsertForbidNull:
		if strings.HasPrefix(upper, "INSERT ") && (strings.Contains(upper, ", NULL") || strings.Contains(upper, "(NULL")) {
			return false, "Violation: INSERT statement explicitly writes a NULL literal."
		}
		return true, "Compliant: INSERT statement does not write explicit NULL literals."
	case profileDMLMaxInValues, profileDMLImplicitConversion:
		if strings.Count(upper, ",") >= 10 ||
			strings.Contains(upper, "ORDER_NO = 1001") || strings.Contains(upper, "PHONE = 138") || strings.Contains(upper, "TRACKING_NO = 9988") {
			return false, "Violation: statement violates IN-list size or column literal type consistency."
		}
		return true, "Compliant: statement satisfies IN-list and column type rules."
	default:
		if strings.Contains(upper, "DROP TABLE") || strings.Contains(upper, "TRUNCATE ") || strings.Contains(upper, "DROP DATABASE") ||
			strings.Contains(upper, "MODIFY COLUMN") || strings.Contains(upper, "BAD_INDEX_NAME") || strings.Contains(upper, "TEMP_LOGS") ||
			strings.Contains(upper, "= NULL") || strings.Contains(upper, " LIMIT ") || strings.Contains(upper, " ORDER BY ") || strings.Contains(upper, "(SELECT ") ||
			((strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "DELETE ")) && !strings.Contains(upper, " WHERE ")) ||
			(strings.HasPrefix(upper, "INSERT INTO ") && !strings.Contains(strings.SplitN(upper, "VALUES", 2)[0], "(")) {
			return false, "Violation: statement violates the skill audit specification."
		}
		return true, "Compliant: statement satisfies the skill audit specification."
	}
}
