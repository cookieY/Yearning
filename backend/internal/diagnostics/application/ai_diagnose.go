// ai_diagnose.go implements per-request in-memory desensitized 4-section AI diagnosis report generation,
// anti-hallucination validation without execution plans, AES-256-GCM encrypted persistence, and
// StartEphemeralRun + SubmitOrderV2 optimization change order draft bridging (P102 / P103).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
	"time"
)

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
	reportEnv, err := s.Crypter.SealColumn(crypto.PurposeDiagnosticsAIReport, crypto.ColumnRef{
		Table:      "sql_diagnostics_ai_reports",
		PrimaryKey: reportID,
		Column:     "report",
	}, rawPayload)
	if err != nil {
		return nil, err
	}

	var candCiphertext, candNonce, candHMAC []byte
	var candKeyID, candAlg, candHMACKeyID *string
	if candidateSQL != nil && *candidateSQL != "" {
		cEnv, cerr := s.Crypter.SealColumn(crypto.PurposeDiagnosticsCandidateSQL, crypto.ColumnRef{
			Table:      "sql_diagnostics_ai_reports",
			PrimaryKey: reportID,
			Column:     "candidate_sql",
		}, []byte(*candidateSQL))
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
