// prompttool_ai.go AI-assisted prompt optimization, 10-case SQL test generation
// (5 expected compliant + 5 expected non-compliant), and skill test execution for
// admin review skills (Prompt Tools).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// ProviderJSONResult holds the stripped JSON completion alongside token usage and endpoint metadata.
type ProviderJSONResult struct {
	Content      string
	UsedSAN      bool
	ProviderID   string
	ModelName    string
	InputTokens  int64
	OutputTokens int64
}

type skillStatementCategory string

const (
	skillCategoryDDL skillStatementCategory = "ddl"
	skillCategoryDML skillStatementCategory = "dml"
)

func providerFailedError(err error, fallbackDetail string) error {
	detail := strings.TrimSpace(fallbackDetail)
	if err != nil {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "no active provider"),
			strings.Contains(msg, "empty provider chain"),
			strings.Contains(msg, "provider chain unavailable"):
			detail = "No enabled AI provider is configured"
		case provider.ExtractHTTPStatus(err) > 0:
			detail = fmt.Sprintf("AI provider request failed (HTTP %d)", provider.ExtractHTTPStatus(err))
		default:
			if maxCtx, promptTokens, ok := provider.ParseContextLengthOverflow(err.Error()); ok {
				detail = fmt.Sprintf("AI provider context limit exceeded (%d > %d tokens)", promptTokens, maxCtx)
			}
		}
	}
	return &domain.Error{
		Code:   domain.CodeAIProviderFailed,
		Name:   "AI_PROVIDER_FAILED",
		Detail: detail,
	}
}

// OptimizePromptToolPrompt rewrites and structures a skill prompt using the active AI provider chain.
// Fails closed with 2011 AI_PROVIDER_FAILED when no provider is configured, upstream fails, or placeholders are lost.
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

	rawResp, err := s.callActiveProviderJSON(ctx, sysPrompt, string(userPayload))
	if err != nil {
		return nil, providerFailedError(err, "AI provider request failed during prompt optimization")
	}
	var parsed struct {
		KnowledgeText string `json:"knowledge_text"`
	}
	if err := json.Unmarshal([]byte(rawResp), &parsed); err != nil {
		return nil, &domain.Error{
			Code:   domain.CodeAIProviderFailed,
			Name:   "AI_PROVIDER_FAILED",
			Detail: "AI provider returned invalid JSON for prompt optimization",
		}
	}
	candidate := strings.TrimSpace(parsed.KnowledgeText)
	if candidate == "" || len(candidate) > 64*1024 {
		return nil, &domain.Error{
			Code:   domain.CodeAIProviderFailed,
			Name:   "AI_PROVIDER_FAILED",
			Detail: "AI provider returned an empty or oversized optimized prompt",
		}
	}
	if !hasAllPlaceholders(candidate, placeholders) {
		return nil, &domain.Error{
			Code:   domain.CodeAIProviderFailed,
			Name:   "AI_PROVIDER_FAILED",
			Detail: "AI provider dropped required template placeholders from optimized prompt",
		}
	}
	return &OptimizePromptToolOutput{KnowledgeText: candidate}, nil
}

// GeneratePromptToolTestCases generates 10 SQL test cases (5 expected compliant, 5 expected non-compliant)
// using the active AI provider chain, failing closed with 2011 AI_PROVIDER_FAILED when unavailable or invalid.
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

	rawResp, err := s.callActiveProviderJSON(ctx, sysPrompt, string(userPayload))
	if err != nil {
		return nil, providerFailedError(err, "AI provider request failed during test case generation")
	}
	var parsed struct {
		Cases []PromptToolTestCase `json:"cases"`
	}
	if err := json.Unmarshal([]byte(rawResp), &parsed); err != nil {
		return nil, &domain.Error{
			Code:   domain.CodeAIProviderFailed,
			Name:   "AI_PROVIDER_FAILED",
			Detail: "AI provider returned invalid JSON for test case generation",
		}
	}
	merged, ok := normalizeAndFilterGeneratedTestCases(parsed.Cases, category)
	if !ok {
		return nil, &domain.Error{
			Code:   domain.CodeAIProviderFailed,
			Name:   "AI_PROVIDER_FAILED",
			Detail: "AI provider did not return 5 valid compliant and 5 valid non-compliant SQL test cases",
		}
	}
	return &GeneratePromptToolTestCasesOutput{Cases: merged}, nil
}

// RunPromptToolTest evaluates the provided SQL test cases against the skill definition via the active
// AI provider chain, failing closed with 2011 AI_PROVIDER_FAILED when unavailable or incomplete.
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

	rawResp, err := s.callActiveProviderJSON(ctx, sysPrompt, string(userPayload))
	if err != nil {
		return nil, providerFailedError(err, "AI provider request failed during skill evaluation")
	}
	var parsed struct {
		Results []struct {
			ID              string `json:"id"`
			ActualCompliant bool   `json:"actual_compliant"`
			FindingSummary  string `json:"finding_summary"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(rawResp), &parsed); err != nil || len(parsed.Results) == 0 {
		return nil, &domain.Error{
			Code:   domain.CodeAIProviderFailed,
			Name:   "AI_PROVIDER_FAILED",
			Detail: "AI provider returned invalid JSON for skill evaluation",
		}
	}

	aiVerdicts := make(map[string]struct {
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

	results := make([]PromptToolTestCaseResult, len(in.Cases))
	matchedCount := 0
	for i, tc := range in.Cases {
		v, ok := aiVerdicts[tc.ID]
		if !ok {
			return nil, &domain.Error{
				Code:   domain.CodeAIProviderFailed,
				Name:   "AI_PROVIDER_FAILED",
				Detail: fmt.Sprintf("AI provider omitted evaluation verdict for case %q", tc.ID),
			}
		}
		matched := v.ActualCompliant == tc.ExpectedCompliant
		if matched {
			matchedCount++
		}
		results[i] = PromptToolTestCaseResult{
			ID:                 tc.ID,
			SQL:                tc.SQL,
			ExpectedCompliant:  tc.ExpectedCompliant,
			ActualCompliant:    v.ActualCompliant,
			MatchedExpectation: matched,
			FindingSummary:     v.FindingSummary,
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
	res, err := s.CallActiveProviderJSONWithMetaPerEndpoint(ctx, build)
	if err != nil {
		return "", false, err
	}
	return res.Content, res.UsedSAN, nil
}

// CallActiveProviderJSONWithMetaPerEndpoint invokes the active AI provider chain with per-endpoint prompt
// construction and returns completion content, SAN profile status, endpoint metadata, and token usage.
func (s *Services) CallActiveProviderJSONWithMetaPerEndpoint(
	ctx context.Context,
	build func(ep provider.Endpoint) (sysPrompt string, userContent string, err error),
) (*ProviderJSONResult, error) {
	if s == nil || s.DB == nil || s.Crypter == nil {
		return nil, errors.New("review: provider chain unavailable")
	}
	if build == nil {
		return nil, errors.New("review: nil endpoint prompt builder")
	}
	chain, err := s.LoadProviderChain(ctx, s.DB, func(ep provider.Endpoint) (provider.Client, error) {
		return provider.NewOpenAIAdapter(ep, nil)
	})
	if err != nil || chain == nil {
		return nil, errors.New("review: no active provider")
	}
	return callEndpointsJSONWithMeta(ctx, provider.NewHTTPClient(), chain.Endpoints, build)
}

func callEndpointsJSON(
	ctx context.Context,
	hc *provider.HTTPClient,
	eps []provider.Endpoint,
	build func(ep provider.Endpoint) (sysPrompt string, userContent string, err error),
) (string, bool, error) {
	res, err := callEndpointsJSONWithMeta(ctx, hc, eps, build)
	if err != nil {
		return "", false, err
	}
	return res.Content, res.UsedSAN, nil
}

func callEndpointsJSONWithMeta(
	ctx context.Context,
	hc *provider.HTTPClient,
	eps []provider.Endpoint,
	build func(ep provider.Endpoint) (sysPrompt string, userContent string, err error),
) (*ProviderJSONResult, error) {
	if len(eps) == 0 {
		return nil, errors.New("review: empty provider chain")
	}
	if build == nil {
		return nil, errors.New("review: nil endpoint prompt builder")
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
		res, callErr := callEndpointJSONWithMeta(ctx, hc, ep, sysPrompt, userContent)
		if callErr == nil && res != nil && strings.TrimSpace(res.Content) != "" {
			res.UsedSAN = ep.EffectivePromptProfile() == promptprofile.ProfileSAN
			res.ProviderID = ep.ProviderID
			res.ModelName = ep.ModelName
			return res, nil
		}
		lastErr = callErr
	}
	if lastErr == nil {
		lastErr = errors.New("review: provider call failed")
	}
	return nil, lastErr
}

func callEndpointJSON(ctx context.Context, hc *provider.HTTPClient, ep provider.Endpoint, sysPrompt, userContent string) (string, error) {
	res, err := callEndpointJSONWithMeta(ctx, hc, ep, sysPrompt, userContent)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

func callEndpointJSONWithMeta(ctx context.Context, hc *provider.HTTPClient, ep provider.Endpoint, sysPrompt, userContent string) (*ProviderJSONResult, error) {
	resp, err := provider.PostChatCompletionWithOverflowHeal(ctx, hc, ep, provider.ChatCompletionTransportRequest{
		CallerTag:            "prompttool_ai",
		ErrorDomain:          "review",
		SystemPrompt:         sysPrompt,
		UserContent:          userContent,
		EnsureSystemJSONHint: true,
		EnsureUserJSONHint:   true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := provider.ReadBoundedResponseBody(resp.Body)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return nil, errors.New("review: invalid completion response")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return &ProviderJSONResult{
		Content:      strings.TrimSpace(content),
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}, nil
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
