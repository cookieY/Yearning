// ai_diagnose_llm.go holds ai_diagnose domain helpers and operations.
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

type persistedAIReportPayload struct {
	DesensitizedContextSummary  string                                              `json:"desensitized_context_summary"`
	ObservedFacts               []domain.DiagnosticAIObservedFactView               `json:"observed_facts"`
	RootCauseHypotheses         []domain.DiagnosticAIRootCauseHypothesisView        `json:"root_cause_hypotheses"`
	OptimizationRecommendations []domain.DiagnosticAIOptimizationRecommendationView `json:"optimization_recommendations"`
	VerificationSteps           []string                                            `json:"verification_steps"`
}

var (
	tableClausePattern = regexp.MustCompile(`(?i)\b(?:from|join|update|into|table)\s+([a-zA-Z0-9_$."` + "`" + `]+)`)
	whereColPattern    = regexp.MustCompile(`(?i)[` + "`" + `"]?([a-zA-Z_][a-zA-Z0-9_]*)[` + "`" + `"]?\s*(?:=|>|<|>=|<=|\bin\b|\blike\b|\bbetween\b)`)
	orderByColPattern  = regexp.MustCompile(`(?i)\border\s+by\s+[` + "`" + `"]?([a-zA-Z_][a-zA-Z0-9_]*)[` + "`" + `"]?`)

	// hallucinatedPlanPhrases blocks wording that asserts deterministic execution plan facts without EXPLAIN evidence (P102 anti-hallucination gate).
	hallucinatedPlanPhrases = []string{
		"\u5df2\u786e\u8ba4\u6267\u884c\u8ba1\u5212",
		"EXPLAIN\u786e\u8ba4",
		"EXPLAIN\u663e\u793a\u5df2\u547d\u4e2d",
		"\u786e\u8ba4\u547d\u4e2d\u7d22\u5f15",
		"execution plan confirmed",
		"explain output confirms",
		"explain shows index hit",
		"confirmed index hit",
	}
)

// ValidateNoHallucinatedPlanFacts verifies that hypotheses are not stated as confirmed execution plan facts when EXPLAIN evidence is absent.
func ValidateNoHallucinatedPlanFacts(facts []domain.DiagnosticAIObservedFactView, hypotheses []domain.DiagnosticAIRootCauseHypothesisView) error {
	for _, f := range facts {
		combined := strings.ToLower(f.Metric + " " + f.Value)
		for _, banned := range hallucinatedPlanPhrases {
			if strings.Contains(combined, strings.ToLower(banned)) {
				return domain.ErrValidation("Index hits or full-table scans must not be stated as observed facts without EXPLAIN plan evidence")
			}
		}
	}
	for _, h := range hypotheses {
		if strings.TrimSpace(h.Confidence) == "" || strings.TrimSpace(h.EvidenceBasis) == "" {
			return domain.ErrValidation("Root-cause hypotheses must include both confidence level and statistical/metadata evidence basis")
		}
		lower := strings.ToLower(h.Hypothesis)
		for _, banned := range hallucinatedPlanPhrases {
			if strings.Contains(lower, strings.ToLower(banned)) {
				return domain.ErrValidation("Root-cause hypotheses must not assert a confirmed execution plan without EXPLAIN evidence")
			}
		}
	}
	return nil
}

// DesensitizeTemplateResult holds the per-request desensitized SQL template and anonymized table metadata summary.
// RawTemplate and RawSchemaMetadata are held strictly in memory for explicit SAN ("san") endpoints and are never persisted.
type DesensitizeTemplateResult struct {
	AnonymizedTemplate string
	AnonymizedSummary  string
	RawTemplate        string
	RawSchemaMetadata  string
	PrimarySchema      string
	PrimaryTable       string
	CandidateCols      []string
	ExistingIndexes    []database.MetadataIndex
}

func (s *Services) loadLatestAIReport(ctx context.Context, datasourceID, statementID string) (*domain.DiagnosticAIReportView, error) {
	var row persistence.SQLDiagnosticsAIReportModel
	err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and statement_id = ? and expires_at > ?", datasourceID, statementID, time.Now().UTC()).
		Order("created_at desc").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return s.reportViewFromModel(&row)
}

func (s *Services) reportViewFromModel(row *persistence.SQLDiagnosticsAIReportModel) (*domain.DiagnosticAIReportView, error) {
	plain, err := s.Crypter.OpenColumn(crypto.PurposeDiagnosticsAIReport, crypto.ColumnRef{
		Table:      "sql_diagnostics_ai_reports",
		PrimaryKey: row.ID,
		Column:     "report",
	}, crypto.Envelope{
		Ciphertext: row.ReportCiphertext,
		Nonce:      row.ReportNonce,
		KeyID:      row.ReportKeyID,
		Algorithm:  row.ReportAlgorithm,
	})
	if err != nil {
		return nil, err
	}
	var payload persistedAIReportPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, err
	}
	var candidateSQL *string
	if len(row.CandidateSQLCiphertext) > 0 && row.CandidateSQLKeyID != nil && row.CandidateSQLAlgorithm != nil {
		cPlain, cerr := s.Crypter.OpenColumn(crypto.PurposeDiagnosticsCandidateSQL, crypto.ColumnRef{
			Table:      "sql_diagnostics_ai_reports",
			PrimaryKey: row.ID,
			Column:     "candidate_sql",
		}, crypto.Envelope{
			Ciphertext: row.CandidateSQLCiphertext,
			Nonce:      row.CandidateSQLNonce,
			KeyID:      *row.CandidateSQLKeyID,
			Algorithm:  *row.CandidateSQLAlgorithm,
		})
		if cerr == nil {
			cs := string(cPlain)
			candidateSQL = &cs
		}
	}
	return &domain.DiagnosticAIReportView{
		ReportID:                    row.ID,
		DatasourceID:                row.DatasourceID,
		StatementID:                 row.StatementID,
		WindowStart:                 row.WindowStart,
		WindowEnd:                   row.WindowEnd,
		CoverageMode:                row.CoverageMode,
		DesensitizedContextSummary:  payload.DesensitizedContextSummary,
		ObservedFacts:               payload.ObservedFacts,
		RootCauseHypotheses:         payload.RootCauseHypotheses,
		OptimizationRecommendations: payload.OptimizationRecommendations,
		VerificationSteps:           payload.VerificationSteps,
		CandidateSQL:                candidateSQL,
		CandidateStatementKind:      row.CandidateStatementKind,
		SubmissionState:             row.SubmissionState,
		FlowID:                      row.FlowID,
		EphemeralRunID:              row.EphemeralRunID,
		SubmittedOrderID:            row.SubmittedOrderID,
		SubmittedOrderDisplayNo:     row.SubmittedOrderDisplayNo,
		CreatedAt:                   row.CreatedAt,
	}, nil
}

// ---- Optimization recommendations bridged to change order precheck and submission (P103: enforcing FlowDatasourceGranted and Submission Gate) ----

func (s *Services) verifyChangeFlowPermission(ctx context.Context, userID, flowID, datasourceID string) error {
	var count int64
	err := s.DB.WithContext(ctx).Raw(`
		select count(1)
		from flows f
		join flow_stages fs on fs.flow_id = f.id
		join permission_group_flow_grants pgf on pgf.flow_id = f.id
		join permission_groups pg on pg.id = pgf.permission_group_id and pg.enabled = true
		join permission_group_members pgm on pgm.permission_group_id = pg.id and pgm.user_id = ?
		where f.id = ? and fs.datasource_id = ?
		  and f.flow_type = 'change_review' and f.status = 'enabled'`,
		userID, flowID, datasourceID).Scan(&count).Error
	if err != nil || count == 0 {
		return domain.ErrDatasourceForbidden("Current user is not granted submission permission on the change_review workflow for this datasource")
	}
	return nil
}

// PrecheckOptimizationDraft initiates an ephemeral AI precheck for a diagnostic optimization candidate SQL/DDL (reusing StartEphemeralRun).
func (s *Services) PrecheckOptimizationDraft(
	ctx context.Context,
	actor domain.Actor,
	datasourceID string,
	reportID string,
	flowID string,
	candidateSQLOverride *string,
	idempotencyKey string,
) (map[string]any, error) {
	if strings.TrimSpace(flowID) == "" {
		return nil, domain.ErrValidation("Target change review workflow flow_id is required")
	}
	var report persistence.SQLDiagnosticsAIReportModel
	if err := s.DB.WithContext(ctx).
		Where("id = ? and datasource_id = ?", reportID, datasourceID).
		First(&report).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if report.SubmissionState == "submitted" {
		return nil, domain.ErrInvalidState("This diagnostic report has already been submitted as a change order and cannot be prechecked again")
	}

	// Strictly verify that the current user holds submission permission on the target change flow (admin_is_not_business_override = true).
	if err := s.verifyChangeFlowPermission(ctx, actor.UserID, flowID, datasourceID); err != nil {
		return nil, err
	}

	view, err := s.reportViewFromModel(&report)
	if err != nil {
		return nil, err
	}
	sqlText := ""
	if candidateSQLOverride != nil && strings.TrimSpace(*candidateSQLOverride) != "" {
		sqlText = strings.TrimSpace(*candidateSQLOverride)
	} else if view.CandidateSQL != nil {
		sqlText = strings.TrimSpace(*view.CandidateSQL)
	}
	if sqlText == "" {
		return nil, domain.ErrValidation("Candidate optimization SQL is empty; provide a DDL or DML statement to precheck")
	}
	stmtKind := classifyCandidateStatementKind(sqlText)

	var runID string
	if s.PrecheckRunner != nil {
		runID, err = s.PrecheckRunner(ctx, actor, flowID, sqlText, idempotencyKey)
	} else if s.Review != nil && s.ReviewAgent != nil {
		runID, err = s.Review.StartEphemeralRun(ctx, application.Actor{UserID: actor.UserID, Username: actor.Username}, s.ReviewAgent,
			application.EphemeralStartInput{
				FlowID:         flowID,
				SQL:            []byte(sqlText),
				OutputLocale:   application.OutputLocaleFromContext(ctx),
				IdempotencyKey: idempotencyKey,
			})
	} else {
		runID = uuid.NewString()
	}
	if err != nil {
		return nil, domain.ErrValidation(err.Error())
	}

	cEnv, err := s.Crypter.SealColumn(crypto.PurposeDiagnosticsCandidateSQL, crypto.ColumnRef{
		Table:      "sql_diagnostics_ai_reports",
		PrimaryKey: report.ID,
		Column:     "candidate_sql",
	}, []byte(sqlText))
	if err != nil {
		return nil, err
	}
	mac, hKeyID, err := s.computeCandidateSQLHMAC(sqlText)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&persistence.SQLDiagnosticsAIReportModel{}).
		Where("id = ?", report.ID).
		Updates(map[string]any{
			"flow_id":                   flowID,
			"candidate_sql_ciphertext":  cEnv.Ciphertext,
			"candidate_sql_nonce":       cEnv.Nonce,
			"candidate_sql_key_id":      cEnv.KeyID,
			"candidate_sql_algorithm":   cEnv.Algorithm,
			"candidate_sql_hmac":        mac,
			"candidate_sql_hmac_key_id": hKeyID,
			"candidate_statement_kind":  stmtKind,
			"submission_state":          "prechecked",
			"ephemeral_run_id":          runID,
			"version":                   gorm.Expr("version + 1"),
			"updated_at":                now,
		}).Error; err != nil {
		return nil, err
	}

	updated, _ := s.loadReportByID(ctx, report.ID)
	return map[string]any{
		"report":     updated,
		"run_id":     runID,
		"result_ref": "/review-runs/" + runID,
	}, nil
}

func (s *Services) loadReportByID(ctx context.Context, reportID string) (*domain.DiagnosticAIReportView, error) {
	var row persistence.SQLDiagnosticsAIReportModel
	if err := s.DB.WithContext(ctx).Where("id = ?", reportID).First(&row).Error; err != nil {
		return nil, err
	}
	return s.reportViewFromModel(&row)
}

func classifyCandidateStatementKind(sqlText string) string {
	return fingerprint.StatementClassOf(string(fingerprint.FirstOperationSlice([]byte(sqlText))))
}

func extractReferencedSchemas(sqlText string) []string {
	matches := tableClausePattern.FindAllStringSubmatch(sqlText, -1)
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := strings.Trim(m[1], "`\" ")
		idx := strings.LastIndex(raw, ".")
		if idx <= 0 {
			continue
		}
		pref := strings.Trim(raw[:idx], "`\" ")
		low := strings.ToLower(pref)
		if low == "" || seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, pref)
	}
	return out
}

func extractReferencedTables(sqlText string, maxTables int) []string {
	matches := tableClausePattern.FindAllStringSubmatch(sqlText, -1)
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := strings.Trim(m[1], "`\" ")
		if idx := strings.LastIndex(raw, "."); idx >= 0 {
			raw = strings.Trim(raw[idx+1:], "`\" ")
		}
		low := strings.ToLower(raw)
		if low == "" || low == "select" || low == "dual" || seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, raw)
		if len(out) >= maxTables {
			break
		}
	}
	return out
}

func extractFilterAndOrderColumns(sqlText string) []string {
	seen := make(map[string]bool)
	var out []string
	addCol := func(c string) {
		c = strings.Trim(c, "`\" ")
		low := strings.ToLower(c)
		switch low {
		case "", "and", "or", "not", "null", "true", "false", "in", "is", "asc", "desc", "limit":
			return
		}
		if !seen[low] {
			seen[low] = true
			out = append(out, c)
		}
	}
	for _, m := range whereColPattern.FindAllStringSubmatch(sqlText, -1) {
		if len(m) >= 2 {
			addCol(m[1])
		}
	}
	for _, m := range orderByColPattern.FindAllStringSubmatch(sqlText, -1) {
		if len(m) >= 2 {
			addCol(m[1])
		}
	}
	return out
}

func (s *Services) computeCandidateSQLHMAC(sqlText string) ([]byte, string, error) {
	keyID, err := s.Crypter.HMACKeyID("sql_fingerprint")
	if err != nil {
		return nil, "", err
	}
	mac, err := s.Crypter.HMAC("sql_fingerprint", []byte(sqlText))
	if err != nil {
		return nil, "", err
	}
	return mac, keyID, nil
}

// VerifyCandidateSQLHMAC verifies the HMAC integrity of candidate SQL for tests or audits.
func (s *Services) VerifyCandidateSQLHMAC(row *persistence.SQLDiagnosticsAIReportModel, sqlText string) bool {
	if row == nil || len(row.CandidateSQLHMAC) == 0 {
		return false
	}
	mac, _, err := s.computeCandidateSQLHMAC(sqlText)
	if err != nil {
		return false
	}
	return hex.EncodeToString(mac) == hex.EncodeToString(row.CandidateSQLHMAC)
}

func buildDiagnoseMessagesForEndpoint(
	ep provider.Endpoint,
	priv *privacy.TurnPrivacySession,
	ds *DatasourceRow,
	desensitized *DesensitizeTemplateResult,
	windowLabel string,
	coverageMode string,
	calls int64,
	totalMS float64,
	avgMS float64,
	rowsExamined int64,
	rowsSent int64,
	lockOrTemp float64,
	isEnglish bool,
) (string, string, error) {
	if ep.EffectivePromptProfile() == promptprofile.ProfileSAN {
		locale := "zh-CN"
		if isEnglish {
			locale = "en-US"
		}
		sysPrompt := promptprofile.BuildSANDiagnoseSystemPrompt(locale)
		rawTemplate := promptprofile.TruncateSQL(desensitized.RawTemplate)
		if rawTemplate == "" {
			rawTemplate = desensitized.AnonymizedTemplate
		}
		userPayloadBytes, err := json.Marshal(map[string]any{
			"engine":                 ds.Engine,
			"compatibility_mode":     ds.CompatibilityMode,
			"window":                 windowLabel,
			"coverage_mode":          coverageMode,
			"sql_template":           rawTemplate,
			"schema_metadata":        desensitized.RawSchemaMetadata,
			"calls":                  calls,
			"total_exec_time_ms":     totalMS,
			"avg_exec_time_ms":       avgMS,
			"rows_or_blocks_scanned": rowsExamined,
			"rows_returned":          rowsSent,
			"lock_ms_or_temp_blocks": round3(lockOrTemp),
		})
		if err != nil {
			return "", "", err
		}
		return sysPrompt, string(userPayloadBytes), nil
	}

	targetLang := "Simplified Chinese (zh-CN)"
	if isEnglish {
		targetLang = "English (en-US)"
	}

	sysPrompt := "You are an expert database performance diagnostics architect.\n" +
		"Analyze the provided desensitized SQL template, desensitized table/index metadata, and statistical window metrics to produce a 4-section structured SQL performance diagnosis report.\n" +
		"Mandatory requirements:\n" +
		"1. Write all human-readable descriptions, titles, hypotheses, rationales, conditions, risk notes, and verification steps in " + targetLang + ".\n" +
		"2. Identifiers in the input are anonymized (e.g., schema_1, table_1, column_1, index_1). Reference those exact anonymized tokens in your explanations and candidate_sql so the server can deterministically restore them.\n" +
		"3. Anti-hallucination rule: No EXPLAIN execution plan is present. Never assert deterministic execution plan facts (never use phrases like 'execution plan confirmed', 'explain output confirms', or assert that an index hit or full table scan was confirmed by EXPLAIN). Always state root causes as hypotheses with confidence ('high', 'medium', or 'low') and statistical/metadata evidence_basis.\n" +
		"4. If an index DDL (such as CREATE INDEX idx_... ON table_1 (column_1, ...)) is appropriate on an application table, include it in candidate_sql of an 'index_ddl' recommendation. If the query only reads system views (such as pg_stat_statements or performance_schema), set candidate_sql to null.\n" +
		"5. Respond strictly with a JSON object of shape:\n" +
		"{\n" +
		"  \"observed_facts\": [{\"metric\": \"...\", \"value\": \"...\", \"window\": \"...\"}],\n" +
		"  \"root_cause_hypotheses\": [{\"hypothesis\": \"...\", \"confidence\": \"high|medium|low\", \"evidence_basis\": \"...\"}],\n" +
		"  \"optimization_recommendations\": [{\"kind\": \"index_ddl|sql_rewrite\", \"title\": \"...\", \"rationale\": \"...\", \"applicable_conditions\": [\"...\"], \"candidate_sql\": \"CREATE INDEX ...\" or null, \"risk_notes\": [\"...\"]}],\n" +
		"  \"verification_steps\": [\"...\"]\n" +
		"}"

	if err := assertDiagnoseDynamicZeroLeak(priv, desensitized, windowLabel); err != nil {
		return "", "", err
	}
	userPayloadBytes, err := json.Marshal(map[string]any{
		"engine":                 ds.Engine,
		"compatibility_mode":     ds.CompatibilityMode,
		"window":                 windowLabel,
		"coverage_mode":          coverageMode,
		"desensitized_template":  desensitized.AnonymizedTemplate,
		"desensitized_metadata":  desensitized.AnonymizedSummary,
		"calls":                  calls,
		"total_exec_time_ms":     totalMS,
		"avg_exec_time_ms":       avgMS,
		"rows_or_blocks_scanned": rowsExamined,
		"rows_returned":          rowsSent,
		"lock_ms_or_temp_blocks": round3(lockOrTemp),
	})
	if err != nil {
		return "", "", err
	}
	return sysPrompt, string(userPayloadBytes), nil
}

func (s *Services) invokeLLMDiagnosisWithReportID(
	ctx context.Context,
	reportID string,
	priv *privacy.TurnPrivacySession,
	ds *DatasourceRow,
	desensitized *DesensitizeTemplateResult,
	windowLabel string,
	coverageMode string,
	calls int64,
	totalMS float64,
	avgMS float64,
	rowsExamined int64,
	rowsSent int64,
	lockOrTemp float64,
	isEnglish bool,
) (*persistedAIReportPayload, *string, *string, error) {
	if s.Review == nil || priv == nil || ds == nil || desensitized == nil {
		dsID := ""
		if ds != nil {
			dsID = ds.ID
		}
		logLLMDiagnosisFailure(dsID, provider.Endpoint{}, errors.New("diagnostics: AI provider service unavailable"))
		return nil, nil, nil, domain.ErrAIProviderFailed("No enabled AI provider is configured")
	}

	budgetDB := s.DB
	if budgetDB == nil {
		budgetDB = s.Review.DB
	}
	estTokens := int64(len(desensitized.AnonymizedTemplate)+len(desensitized.AnonymizedSummary))/3 + 512
	lease, berr := s.acquireDailyBudgetLease(ctx, budgetDB, estTokens)
	if berr != nil {
		return nil, nil, nil, berr
	}

	var lastEP provider.Endpoint
	res, err := s.Review.CallActiveProviderJSONWithMetaPerEndpoint(ctx, func(ep provider.Endpoint) (string, string, error) {
		lastEP = ep
		return buildDiagnoseMessagesForEndpoint(
			ep, priv, ds, desensitized, windowLabel, coverageMode,
			calls, totalMS, avgMS, rowsExamined, rowsSent, lockOrTemp, isEnglish,
		)
	})
	if err != nil || res == nil || strings.TrimSpace(res.Content) == "" {
		_ = lease.Release(context.WithoutCancel(ctx))
		logLLMDiagnosisFailure(ds.ID, lastEP, err)
		return nil, nil, nil, mapLLMInvocationError(err)
	}
	payload, candSQL, candKind, ok := parseAndFinalizeDiagnoseResponse(res.Content, res.UsedSAN, priv, windowLabel)
	outcome := "succeeded"
	if !ok {
		outcome = "schema_failed"
	}
	if serr := lease.Settle(ctx, reportID, res.ProviderID, res.ModelName, "diagnostics", res.InputTokens, res.OutputTokens, outcome); serr != nil && ok {
		return nil, nil, nil, serr
	}
	if !ok {
		logLLMDiagnosisFailure(ds.ID, lastEP, errors.New("diagnostics: llm response rejected by schema or hallucination guard"))
		return nil, nil, nil, domain.ErrAIProviderFailed("AI provider response failed diagnosis schema or plan-fact validation")
	}
	return payload, candSQL, candKind, nil
}

// acquireDailyBudgetLease acquires a shared daily AI budget lease via review/application.
// Fails closed with ErrBudgetServiceUnavailable when s.Review or budgetDB is unconfigured (D14).
func (s *Services) acquireDailyBudgetLease(ctx context.Context, budgetDB *gorm.DB, estTokens int64) (*application.BudgetLease, error) {
	if s == nil || s.Review == nil {
		return nil, application.ErrBudgetServiceUnavailable
	}
	if budgetDB == nil {
		budgetDB = s.DB
	}
	lease, err := s.Review.AcquireBudgetLease(ctx, budgetDB, estTokens)
	if err != nil {
		if errors.Is(err, application.ErrBudgetExhausted) {
			return nil, domain.ErrAIBudgetExhausted()
		}
		return nil, err
	}
	return lease, nil
}

func mapLLMInvocationError(err error) error {
	if errors.Is(err, application.ErrBudgetExhausted) {
		return domain.ErrAIBudgetExhausted()
	}
	if err == nil {
		return domain.ErrAIProviderFailed("AI provider returned an empty response")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no active provider") ||
		strings.Contains(msg, "empty provider chain") ||
		strings.Contains(msg, "provider chain unavailable") {
		return domain.ErrAIProviderFailed("No enabled AI provider is configured")
	}
	if maxCtx, promptTokens, ok := provider.ParseContextLengthOverflow(err.Error()); ok {
		return domain.ErrAIProviderFailed(fmt.Sprintf("AI provider context limit exceeded (%d > %d tokens)", promptTokens, maxCtx))
	}
	if httpStatus := provider.ExtractHTTPStatus(err); httpStatus > 0 {
		return domain.ErrAIProviderFailed(fmt.Sprintf("AI provider request failed (HTTP %d)", httpStatus))
	}
	return domain.ErrAIProviderFailed("AI provider request failed")
}

var aiDiagnosisLLMFailureTotal atomic.Int64

// AIDiagnosisLLMFailureTotal returns the total number of failed AI diagnosis LLM invocations.
func AIDiagnosisLLMFailureTotal() int64 {
	return aiDiagnosisLLMFailureTotal.Load()
}

func logLLMDiagnosisFailure(dsID string, ep provider.Endpoint, err error) {
	total := aiDiagnosisLLMFailureTotal.Add(1)
	httpStatus := 0
	errSummary := "empty_response"
	if err != nil {
		httpStatus = provider.ExtractHTTPStatus(err)
		rawMsg := err.Error()
		if maxCtx, promptTokens, ok := provider.ParseContextLengthOverflow(rawMsg); ok {
			errSummary = fmt.Sprintf("context_length_overflow max_ctx=%d prompt_tokens=%d", maxCtx, promptTokens)
		} else {
			errSummary = provider.UpstreamSnapshot([]byte(rawMsg))
		}
	}
	zap.L().Warn("ai_diagnosis_llm_failed",
		zap.String("datasource_id", dsID),
		zap.String("provider_id", ep.ProviderID),
		zap.String("provider_name", ep.Name),
		zap.String("model_name", ep.ModelName),
		zap.Int("http_status", httpStatus),
		zap.String("error_summary", errSummary),
		zap.Int64("ai_diagnosis_llm_failed_total", total),
	)
}
