// ai_diagnose.go implements per-request in-memory desensitized 4-section AI diagnosis report generation,
// anti-hallucination validation without execution plans, AES-256-GCM encrypted persistence, and
// StartEphemeralRun + SubmitOrderV2 optimization change order draft bridging (P102 / P103).
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	orderdom "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
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

// DesensitizeStatementTemplate desensitizes a normalized SQL template and single-table metadata in per-request memory while preserving ? / ... / $1 placeholders.
func (s *Services) DesensitizeStatementTemplate(
	ctx context.Context,
	priv *privacy.TurnPrivacySession,
	ds *DatasourceRow,
	schemaHint string,
	rawTemplate string,
) (*DesensitizeTemplateResult, error) {
	if priv == nil {
		return nil, domain.ErrAnonymizationFailed("nil_privacy_session")
	}
	schema := strings.TrimSpace(schemaHint)
	if schema == "" && ds.DatabaseName != nil {
		schema = strings.TrimSpace(*ds.DatabaseName)
	}
	if schema == "" {
		if ds.CompatibilityMode == "postgresql" {
			schema = "public"
		} else {
			schema = "default_db"
		}
	}
	anonSchema, err := priv.RegisterSchema(schema)
	if err != nil {
		return nil, domain.ErrAnonymizationFailed("register_schema_failed")
	}

	tables := extractReferencedTables(rawTemplate, 3)
	for _, pref := range extractReferencedSchemas(rawTemplate) {
		_, _ = priv.RegisterSchema(pref)
	}
	var primaryTable string
	var existingIndexes []database.MetadataIndex
	var anonMetaParts []string
	var rawMetaParts []string

	for i, tbl := range tables {
		if i == 0 {
			primaryTable = tbl
		}
		anonTbl, regErr := priv.RegisterTable(schema, tbl)
		if regErr != nil {
			continue
		}
		var colTokens []string
		var rawColSpecs []string
		var rawIdxSpecs []string
		if s.Collector != nil {
			cols, colErr := s.Collector.ListColumns(ctx, *ds, schema, tbl)
			if (colErr != nil || len(cols) == 0) && ds.CompatibilityMode == "postgresql" && schema != "public" {
				cols, colErr = s.Collector.ListColumns(ctx, *ds, "public", tbl)
			}
			if colErr == nil {
				for _, c := range cols {
					rawColSpecs = append(rawColSpecs, fmt.Sprintf("%s:%s", c.ColumnName, c.DatabaseType))
					if tok, cerr := priv.RegisterColumn(schema, tbl, c.ColumnName); cerr == nil {
						colTokens = append(colTokens, fmt.Sprintf("%s:%s", tok, c.DatabaseType))
					}
				}
			}
			idxs, idxErr := s.Collector.ListIndexes(ctx, *ds, schema, tbl)
			if (idxErr != nil || len(idxs) == 0) && ds.CompatibilityMode == "postgresql" && schema != "public" {
				idxs, idxErr = s.Collector.ListIndexes(ctx, *ds, "public", tbl)
			}
			if idxErr == nil {
				if i == 0 {
					existingIndexes = idxs
				}
				for _, idx := range idxs {
					if len(idx.ColumnNames) > 0 {
						rawIdxSpecs = append(rawIdxSpecs, fmt.Sprintf("%s(%s)", idx.IndexName, strings.Join(idx.ColumnNames, ",")))
					} else {
						rawIdxSpecs = append(rawIdxSpecs, idx.IndexName)
					}
					_, _ = priv.RegisterIndex(schema, tbl, idx.IndexName)
				}
			}
		}
		if len(colTokens) > 0 {
			anonMetaParts = append(anonMetaParts, fmt.Sprintf("%s.%s(%s)", anonSchema, anonTbl, strings.Join(colTokens, ", ")))
		} else {
			anonMetaParts = append(anonMetaParts, fmt.Sprintf("%s.%s", anonSchema, anonTbl))
		}
		rawEntry := fmt.Sprintf("%s.%s", schema, tbl)
		if len(rawColSpecs) > 0 {
			rawEntry = fmt.Sprintf("%s(%s)", rawEntry, strings.Join(rawColSpecs, ", "))
		}
		if len(rawIdxSpecs) > 0 {
			rawEntry += " [indexes: " + strings.Join(rawIdxSpecs, ", ") + "]"
		}
		rawMetaParts = append(rawMetaParts, rawEntry)
	}

	candidateCols := extractFilterAndOrderColumns(rawTemplate)
	for _, col := range candidateCols {
		_, _ = priv.RegisterColumn(schema, primaryTable, col)
	}

	profile := fingerprint.Profile{
		Engine: ds.Engine,
		Mode:   ds.CompatibilityMode,
	}
	anonSQL, err := priv.AnonymizeSQL(rawTemplate, profile)
	if err != nil {
		return nil, domain.ErrAnonymizationFailed("anonymize_template_failed")
	}
	anonSQL = strings.ReplaceAll(anonSQL, ". . .", "...")
	for i := 64; i >= 1; i-- {
		ph := fmt.Sprintf(":val_str_%d", i)
		if bv, ok := priv.LookupValue(ph); ok && (bv.RawValue == "?" || strings.HasPrefix(bv.RawValue, "$")) {
			anonSQL = strings.ReplaceAll(anonSQL, ph, bv.RawValue)
		}
	}

	summary := fmt.Sprintf("dialect=%s template=%q", ds.CompatibilityMode, anonSQL)
	if len(anonMetaParts) > 0 {
		summary += " metadata=[" + strings.Join(anonMetaParts, "; ") + "]"
	}
	if err := priv.AssertZeroLeak(summary); err != nil {
		return nil, domain.ErrAnonymizationFailed("outbound_payload_leak_detected")
	}

	return &DesensitizeTemplateResult{
		AnonymizedTemplate: anonSQL,
		AnonymizedSummary:  summary,
		RawTemplate:        promptprofile.TruncateSQL(rawTemplate),
		RawSchemaMetadata:  strings.Join(rawMetaParts, "; "),
		PrimarySchema:      schema,
		PrimaryTable:       primaryTable,
		CandidateCols:      candidateCols,
		ExistingIndexes:    existingIndexes,
	}, nil
}

// DiagnoseStatementWithAI executes in-memory desensitized AI diagnosis for a selected SQL fingerprint and time window and persists the 4-section structured report (P102).
func (s *Services) DiagnoseStatementWithAI(
	ctx context.Context,
	actor domain.Actor,
	datasourceID string,
	statementID string,
	from time.Time,
	to time.Time,
) (*domain.DiagnosticAIReportView, error) {
	ds, err := s.loadDatasourceRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	var stmt persistence.SQLDiagnosticsStatementModel
	if err := s.DB.WithContext(ctx).
		Where("id = ? and datasource_id = ?", statementID, datasourceID).
		First(&stmt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}

	tmplPtr, _ := s.ensureStatementTemplate(ctx, ds, &stmt)
	if tmplPtr == nil || strings.TrimSpace(*tmplPtr) == "" {
		return nil, domain.ErrTemplateUnavailable()
	}
	rawTemplate := strings.TrimSpace(*tmplPtr)

	now := time.Now().UTC()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() || !from.Before(to) {
		from = to.Add(-1 * time.Hour)
	}

	// Load aggregated metric evidence within the statistical window.
	var buckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and statement_id = ? and bucket_end >= ? and bucket_end <= ?",
			datasourceID, statementID, from, to).
		Order("bucket_end asc").
		Find(&buckets).Error; err != nil {
		return nil, err
	}

	agg := &aggregatedStatementStat{stmt: stmt}
	for _, b := range buckets {
		agg.absorbBucket(b)
	}
	var calls int64
	var totalMS float64
	var rowsExamined int64
	var rowsSent int64
	var lockOrTemp float64
	coverageMode := "cumulative_only"
	if agg.deltaBucketCount > 0 {
		calls = agg.calls
		totalMS = agg.totalExecTimeMS
		rowsExamined = agg.rowsExaminedOrSharedBlksRead
		rowsSent = agg.rowsSentOrRowsReturned
		lockOrTemp = agg.lockTimeMSOrTempBlksWritten
		if agg.resetDetected {
			coverageMode = "reset_or_gap"
		} else {
			coverageMode = "window_delta"
		}
	} else {
		calls = stmt.LatestCallsTotal
		totalMS = stmt.LatestTotalExecTimeMS
		rowsExamined = stmt.LatestRowsExaminedOrSharedBlksReadTotal
		rowsSent = stmt.LatestRowsSentOrRowsReturnedTotal
		lockOrTemp = stmt.LatestLockTimeMSOrTempBlksWrittenTotal
	}
	avgMS := 0.0
	if calls > 0 {
		avgMS = round3(totalMS / float64(calls))
	}
	totalMS = round3(totalMS)

	// Per-request in-memory anonymization session: wiped and released immediately on exit.
	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()

	desensitized, err := s.DesensitizeStatementTemplate(ctx, priv, ds, stmt.DatabaseOrSchema, rawTemplate)
	if err != nil {
		return nil, domain.ErrTemplateUnavailable()
	}

	isEnglish := strings.EqualFold(application.OutputLocaleFromContext(ctx), "en-US")
	windowLabel := fmt.Sprintf("%s ~ %s (%s)", from.Format(time.RFC3339), to.Format(time.RFC3339), coverageMode)

	reportID := uuid.NewString()
	llmPayload, candidateSQL, candidateKind, err := s.invokeLLMDiagnosisWithReportID(
		ctx, reportID, priv, ds, desensitized, windowLabel, coverageMode,
		calls, totalMS, avgMS, rowsExamined, rowsSent, lockOrTemp, isEnglish,
	)
	if err != nil {
		return nil, err
	}
	observedFacts := llmPayload.ObservedFacts
	hypotheses := llmPayload.RootCauseHypotheses
	recommendations := llmPayload.OptimizationRecommendations
	verificationSteps := llmPayload.VerificationSteps

	if err := ValidateNoHallucinatedPlanFacts(observedFacts, hypotheses); err != nil {
		return nil, err
	}

	payload := persistedAIReportPayload{
		DesensitizedContextSummary:  desensitized.AnonymizedSummary,
		ObservedFacts:               observedFacts,
		RootCauseHypotheses:         hypotheses,
		OptimizationRecommendations: recommendations,
		VerificationSteps:           verificationSteps,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reportEnv, err := s.Crypter.Seal(crypto.PurposePayload, rawPayload, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "sql_diagnostics_ai_reports",
		PrimaryKey:   reportID,
		Column:       "report",
		Purpose:      "diagnostics_ai_report",
	})
	if err != nil {
		return nil, err
	}

	var candCiphertext, candNonce, candHMAC []byte
	var candKeyID, candAlg, candHMACKeyID *string
	if candidateSQL != nil && *candidateSQL != "" {
		cEnv, cerr := s.Crypter.Seal(crypto.PurposePayload, []byte(*candidateSQL), crypto.AAD{
			DeploymentID: s.Crypter.DeploymentID(),
			Table:        "sql_diagnostics_ai_reports",
			PrimaryKey:   reportID,
			Column:       "candidate_sql",
			Purpose:      "diagnostics_candidate_sql",
		})
		if cerr != nil {
			return nil, cerr
		}
		mac, hKeyID, herr := s.computeCandidateSQLHMAC(*candidateSQL)
		if herr != nil {
			return nil, herr
		}
		candCiphertext = cEnv.Ciphertext
		candNonce = cEnv.Nonce
		candKeyID = &cEnv.KeyID
		candAlg = &cEnv.Algorithm
		candHMAC = mac
		candHMACKeyID = &hKeyID
	}

	row := persistence.SQLDiagnosticsAIReportModel{
		ID:                     reportID,
		DatasourceID:           datasourceID,
		StatementID:            statementID,
		UserID:                 actor.UserID,
		WindowStart:            from,
		WindowEnd:              to,
		CoverageMode:           coverageMode,
		ReportCiphertext:       reportEnv.Ciphertext,
		ReportNonce:            reportEnv.Nonce,
		ReportKeyID:            reportEnv.KeyID,
		ReportAlgorithm:        reportEnv.Algorithm,
		CandidateSQLCiphertext: candCiphertext,
		CandidateSQLNonce:      candNonce,
		CandidateSQLKeyID:      candKeyID,
		CandidateSQLAlgorithm:  candAlg,
		CandidateSQLHMAC:       candHMAC,
		CandidateSQLHMACKeyID:  candHMACKeyID,
		CandidateStatementKind: candidateKind,
		SubmissionState:        "draft_ready",
		ExpiresAt:              now.Add(domain.ArtifactRetention),
		Version:                1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := s.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}

	if s.Audit != nil {
		_ = s.Audit.Audit(ctx, nil, auditlog.AuditInput{
			EventType:     "diagnostics.ai_report_created",
			ActorKind:     "user",
			ActorID:       &actor.UserID,
			ActorUsername: &actor.Username,
			ResourceType:  "sql_diagnostics_ai_report",
			ResourceID:    &reportID,
			Action:        "diagnose_statement_with_ai",
			Outcome:       "succeeded",
			Metadata: map[string]any{
				"datasource_id": datasourceID,
				"statement_id":  statementID,
				"coverage_mode": coverageMode,
			},
		})
	}

	return s.reportViewFromModel(&row)
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
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.ReportCiphertext,
		Nonce:      row.ReportNonce,
		KeyID:      row.ReportKeyID,
		Algorithm:  row.ReportAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "sql_diagnostics_ai_reports",
		PrimaryKey:   row.ID,
		Column:       "report",
		Purpose:      "diagnostics_ai_report",
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
		cPlain, cerr := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
			Ciphertext: row.CandidateSQLCiphertext,
			Nonce:      row.CandidateSQLNonce,
			KeyID:      *row.CandidateSQLKeyID,
			Algorithm:  *row.CandidateSQLAlgorithm,
		}, crypto.AAD{
			DeploymentID: s.Crypter.DeploymentID(),
			Table:        "sql_diagnostics_ai_reports",
			PrimaryKey:   row.ID,
			Column:       "candidate_sql",
			Purpose:      "diagnostics_candidate_sql",
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

	cEnv, err := s.Crypter.Seal(crypto.PurposePayload, []byte(sqlText), crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "sql_diagnostics_ai_reports",
		PrimaryKey:   report.ID,
		Column:       "candidate_sql",
		Purpose:      "diagnostics_candidate_sql",
	})
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

// SubmitOptimizationOrder submits a prechecked diagnostic optimization draft as a formal change order (reusing SubmitOrderV2 and Submission Gate).
func (s *Services) SubmitOptimizationOrder(
	ctx context.Context,
	actor domain.Actor,
	datasourceID string,
	reportID string,
	flowID string,
	ephemeralRunID string,
	title string,
	remark string,
	idempotencyKey string,
) (map[string]any, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, domain.ErrValidation("Missing required Idempotency-Key header")
	}
	if strings.TrimSpace(flowID) == "" || strings.TrimSpace(ephemeralRunID) == "" || strings.TrimSpace(title) == "" {
		return nil, domain.ErrValidation("flow_id, ephemeral_run_id, and change order title must not be empty")
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

	if report.SubmissionState == "submitted" && report.SubmittedOrderID != nil && report.SubmittedOrderDisplayNo != nil {
		updated, _ := s.reportViewFromModel(&report)
		return map[string]any{
			"report":           updated,
			"order_id":         *report.SubmittedOrderID,
			"order_display_no": *report.SubmittedOrderDisplayNo,
			"order_state":      "stage_approval_active",
		}, nil
	}
	if report.SubmissionState != "prechecked" || report.EphemeralRunID == nil || *report.EphemeralRunID != ephemeralRunID {
		return nil, domain.ErrInvalidState("Run AI Precheck on the current candidate optimization SQL and pass the Submission Gate before submitting")
	}
	if report.FlowID == nil || *report.FlowID != flowID {
		return nil, domain.ErrInvalidState("Target change workflow has changed; re-run AI Precheck before submitting")
	}

	if err := s.verifyChangeFlowPermission(ctx, actor.UserID, flowID, datasourceID); err != nil {
		return nil, err
	}

	view, err := s.reportViewFromModel(&report)
	if err != nil || view.CandidateSQL == nil || strings.TrimSpace(*view.CandidateSQL) == "" {
		return nil, domain.ErrValidation("Candidate optimization SQL is unavailable")
	}
	sqlText := strings.TrimSpace(*view.CandidateSQL)
	stmtKind := "ddl"
	if view.CandidateStatementKind != nil && *view.CandidateStatementKind != "" {
		stmtKind = *view.CandidateStatementKind
	}

	var stmt persistence.SQLDiagnosticsStatementModel
	_ = s.DB.WithContext(ctx).Where("id = ?", report.StatementID).First(&stmt).Error
	schemaName := stmt.DatabaseOrSchema
	if schemaName == "" {
		if ds, derr := s.loadDatasourceRow(ctx, datasourceID); derr == nil && ds.DatabaseName != nil && *ds.DatabaseName != "" {
			schemaName = *ds.DatabaseName
		} else {
			schemaName = "public"
		}
	}

	var orderID, displayNo, orderState string
	if s.OrderSubmitter != nil {
		orderID, displayNo, orderState, err = s.OrderSubmitter(ctx, actor, flowID, schemaName, stmtKind, sqlText, ephemeralRunID, title, remark, idempotencyKey)
	} else if s.Orders != nil && s.Review != nil {
		tTrim := strings.TrimSpace(title)
		var rPtr *string
		if strings.TrimSpace(remark) != "" {
			r := strings.TrimSpace(remark)
			rPtr = &r
		}
		res, _, oerr := s.Orders.SubmitOrderV2(ctx,
			orderapp.Actor{UserID: actor.UserID, Username: actor.Username},
			s.Review,
			orderapp.SubmitOrderV2Input{
				FlowID:         flowID,
				SQL:            []byte(sqlText),
				RunID:          ephemeralRunID,
				Title:          &tTrim,
				Description:    rPtr,
				IdempotencyKey: idempotencyKey,
			},
		)
		if oerr != nil {
			var ode *orderdom.Error
			if errors.As(oerr, &ode) {
				return nil, &domain.Error{Code: int(ode.Code), Name: ode.Name, Message: ode.Error()}
			}
			return nil, domain.ErrValidation(oerr.Error())
		}
		orderID = res.ID
		displayNo = res.DisplayNumber
		orderState = string(res.State)
	} else {
		return nil, domain.ErrInvalidState("Change order service is not configured")
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&persistence.SQLDiagnosticsAIReportModel{}).
		Where("id = ?", report.ID).
		Updates(map[string]any{
			"submission_state":           "submitted",
			"submitted_order_id":         orderID,
			"submitted_order_display_no": displayNo,
			"version":                    gorm.Expr("version + 1"),
			"updated_at":                 now,
		}).Error; err != nil {
		return nil, err
	}

	// Automatically register the change order link so before/after comparison appears in Detail Chart 3 once execution completes.
	relKind := "index_or_schema_change"
	if stmtKind != "ddl" {
		relKind = "same_fingerprint"
	}
	_, _ = s.ConfirmOrderLink(ctx, actor, datasourceID, report.StatementID, orderID, relKind, nil)

	updated, _ := s.loadReportByID(ctx, report.ID)
	return map[string]any{
		"report":           updated,
		"order_id":         orderID,
		"order_display_no": displayNo,
		"order_state":      orderState,
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
	upper := strings.ToUpper(strings.TrimSpace(sqlText))
	if strings.HasPrefix(upper, "CREATE ") || strings.HasPrefix(upper, "ALTER ") || strings.HasPrefix(upper, "DROP ") {
		return "ddl"
	}
	return "dml"
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
	userPayload := string(userPayloadBytes)
	if err := priv.AssertZeroLeak(userPayload); err != nil {
		return "", "", err
	}
	return sysPrompt, userPayload, nil
}

func parseAndFinalizeDiagnoseResponse(
	rawResp string,
	usedSAN bool,
	priv *privacy.TurnPrivacySession,
	windowLabel string,
) (*persistedAIReportPayload, *string, *string, bool) {
	if strings.TrimSpace(rawResp) == "" {
		return nil, nil, nil, false
	}
	var parsed persistedAIReportPayload
	if err := json.Unmarshal([]byte(rawResp), &parsed); err != nil {
		return nil, nil, nil, false
	}
	if len(parsed.ObservedFacts) == 0 || len(parsed.RootCauseHypotheses) == 0 ||
		len(parsed.OptimizationRecommendations) == 0 || len(parsed.VerificationSteps) == 0 {
		return nil, nil, nil, false
	}

	restoreText := func(raw string) string {
		trimmed := strings.TrimSpace(raw)
		if !usedSAN && priv != nil {
			trimmed = priv.RestoreTextTokens(trimmed)
		}
		return strings.TrimSpace(trimmed)
	}

	for i := range parsed.ObservedFacts {
		parsed.ObservedFacts[i].Metric = restoreText(parsed.ObservedFacts[i].Metric)
		parsed.ObservedFacts[i].Value = restoreText(parsed.ObservedFacts[i].Value)
		if strings.TrimSpace(parsed.ObservedFacts[i].Window) == "" {
			parsed.ObservedFacts[i].Window = windowLabel
		}
	}
	for i := range parsed.RootCauseHypotheses {
		parsed.RootCauseHypotheses[i].Hypothesis = restoreText(parsed.RootCauseHypotheses[i].Hypothesis)
		parsed.RootCauseHypotheses[i].EvidenceBasis = restoreText(parsed.RootCauseHypotheses[i].EvidenceBasis)
		conf := strings.ToLower(strings.TrimSpace(parsed.RootCauseHypotheses[i].Confidence))
		if conf != "high" && conf != "medium" && conf != "low" {
			conf = "medium"
		}
		parsed.RootCauseHypotheses[i].Confidence = conf
	}

	var extractedCandSQL *string
	var extractedCandKind *string
	for i := range parsed.OptimizationRecommendations {
		rec := &parsed.OptimizationRecommendations[i]
		rec.Title = restoreText(rec.Title)
		rec.Rationale = restoreText(rec.Rationale)
		for j := range rec.ApplicableConditions {
			rec.ApplicableConditions[j] = restoreText(rec.ApplicableConditions[j])
		}
		for j := range rec.RiskNotes {
			rec.RiskNotes[j] = restoreText(rec.RiskNotes[j])
		}
		if rec.CandidateSQL != nil {
			restoredSQL := restoreText(*rec.CandidateSQL)
			if restoredSQL != "" && !strings.EqualFold(restoredSQL, "null") {
				rec.CandidateSQL = &restoredSQL
				if extractedCandSQL == nil {
					kind := classifyCandidateStatementKind(restoredSQL)
					extractedCandSQL = &restoredSQL
					extractedCandKind = &kind
				}
			} else {
				rec.CandidateSQL = nil
			}
		}
	}
	for i := range parsed.VerificationSteps {
		parsed.VerificationSteps[i] = restoreText(parsed.VerificationSteps[i])
	}

	if err := ValidateNoHallucinatedPlanFacts(parsed.ObservedFacts, parsed.RootCauseHypotheses); err != nil {
		return nil, nil, nil, false
	}

	return &parsed, extractedCandSQL, extractedCandKind, true
}

func (s *Services) invokeLLMDiagnosis(
	ctx context.Context,
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
	return s.invokeLLMDiagnosisWithReportID(
		ctx, uuid.NewString(), priv, ds, desensitized, windowLabel, coverageMode,
		calls, totalMS, avgMS, rowsExamined, rowsSent, lockOrTemp, isEnglish,
	)
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
	reservedMinor, budgetDay, budgetCurrency, budgetSettings, berr := s.reserveDailyBudget(ctx, budgetDB, estTokens)
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
		s.releaseDailyBudget(context.WithoutCancel(ctx), budgetDB, budgetDay, budgetCurrency, reservedMinor)
		logLLMDiagnosisFailure(ds.ID, lastEP, err)
		return nil, nil, nil, mapLLMInvocationError(err)
	}
	payload, candSQL, candKind, ok := parseAndFinalizeDiagnoseResponse(res.Content, res.UsedSAN, priv, windowLabel)
	outcome := "succeeded"
	if !ok {
		outcome = "schema_failed"
	}
	if serr := s.settleDailyBudget(ctx, budgetDB, reportID, budgetDay, budgetCurrency, reservedMinor, budgetSettings, res, outcome); serr != nil && ok {
		return nil, nil, nil, serr
	}
	if !ok {
		logLLMDiagnosisFailure(ds.ID, lastEP, errors.New("diagnostics: llm response rejected by schema or hallucination guard"))
		return nil, nil, nil, domain.ErrAIProviderFailed("AI provider response failed diagnosis schema or plan-fact validation")
	}
	return payload, candSQL, candKind, nil
}

func (s *Services) reserveDailyBudget(ctx context.Context, budgetDB *gorm.DB, estTokens int64) (int64, string, string, application.BudgetSettings, error) {
	if s.Review == nil || budgetDB == nil {
		return 0, "", "", application.BudgetSettings{}, nil
	}
	settings, err := s.Review.LoadBudgetSettings(ctx, budgetDB)
	if err != nil {
		return 0, "", "", settings, err
	}
	if !settings.Enforced {
		return 0, "", "", settings, nil
	}
	amountMinor := int64(1)
	if settings.InputPricePerMillionMinor > 0 {
		if calc := (estTokens * settings.InputPricePerMillionMinor) / 1_000_000; calc > amountMinor {
			amountMinor = calc
		}
	}
	day := s.Review.BudgetDay(time.Now())
	if err := s.Review.Reserve(ctx, budgetDB, day, settings.Currency, settings, amountMinor); err != nil {
		if errors.Is(err, application.ErrBudgetExhausted) {
			return 0, "", "", settings, domain.ErrAIBudgetExhausted()
		}
		return 0, "", "", settings, err
	}
	return amountMinor, day, settings.Currency, settings, nil
}

func (s *Services) settleDailyBudget(
	ctx context.Context,
	budgetDB *gorm.DB,
	reportID string,
	day string,
	currency string,
	reservedMinor int64,
	settings application.BudgetSettings,
	res *application.ProviderJSONResult,
	outcome string,
) error {
	if s.Review == nil || budgetDB == nil || !settings.Enforced || reservedMinor <= 0 || day == "" {
		return nil
	}
	var inTokens, outTokens int64
	providerID := uuid.Nil.String()
	modelName := "diagnostics"
	if res != nil {
		inTokens = res.InputTokens
		outTokens = res.OutputTokens
		if parsed, err := uuid.Parse(strings.TrimSpace(res.ProviderID)); err == nil {
			providerID = parsed.String()
		}
		if strings.TrimSpace(res.ModelName) != "" {
			modelName = strings.TrimSpace(res.ModelName)
		}
	}
	actualMinor := application.TokenBasedAmount(
		inTokens, outTokens,
		settings.InputPricePerMillionMinor, settings.OutputPricePerMillionMinor,
		reservedMinor,
	)
	settleCtx := context.WithoutCancel(ctx)
	if err := budgetDB.WithContext(settleCtx).Transaction(func(tx *gorm.DB) error {
		if err := s.Review.Settle(
			settleCtx, tx, reportID, providerID, modelName,
			1, inTokens, outTokens, actualMinor, currency, day, outcome,
		); err != nil {
			return err
		}
		if rem := reservedMinor - actualMinor; rem > 0 {
			return s.Review.Release(settleCtx, tx, day, currency, rem)
		}
		return nil
	}); err != nil {
		_ = s.Review.Release(settleCtx, budgetDB, day, currency, reservedMinor)
		return err
	}
	return nil
}

func (s *Services) releaseDailyBudget(ctx context.Context, budgetDB *gorm.DB, day, currency string, amountMinor int64) {
	if s.Review == nil || budgetDB == nil || amountMinor <= 0 || day == "" {
		return
	}
	_ = s.Review.Release(ctx, budgetDB, day, currency, amountMinor)
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
