// aggregator_detail.go holds aggregator domain helpers and operations.
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"math"
	"sort"
	"strings"
	"time"
)

func buildOverviewComparisonCharts(
	byStmt map[string]*aggregatedStatementStat,
	baseByStmt map[string]*aggregatedStatementStat,
	overallCoverage string,
) ([]domain.DiagnosticsRegressionItemView, []domain.DiagnosticsNewHighLoadItemView) {
	regressions := make([]domain.DiagnosticsRegressionItemView, 0)
	newHighLoad := make([]domain.DiagnosticsNewHighLoadItemView, 0)

	for _, curAgg := range byStmt {
		baseAgg, seenInBaseline := baseByStmt[curAgg.stmt.ID]
		curCalls, curTotalMS, curAvgMS, _, _, _, _ := curAgg.effectiveMetrics(overallCoverage)
		if !seenInBaseline || (baseAgg.deltaBucketCount == 0 && baseAgg.cumulativeBucketCount == 0) {
			if curCalls > 0 {
				newHighLoad = append(newHighLoad, domain.DiagnosticsNewHighLoadItemView{
					StatementID:       curAgg.stmt.ID,
					EngineFingerprint: curAgg.stmt.EngineFingerprint,
					DatabaseOrSchema:  curAgg.stmt.DatabaseOrSchema,
					CurrentCalls:      curCalls,
					CurrentTotalMS:    curTotalMS,
					CurrentAvgMS:      curAvgMS,
					UnobservedNote:    unobservedHighLoadNote,
				})
			}
			continue
		}

		// D4 minimum sample threshold: both windows must contain at least 3 valid delta buckets and >= 10 cumulative delta calls.
		insufficient := baseAgg.deltaBucketCount < domain.MinComparableDeltaBuckets ||
			curAgg.deltaBucketCount < domain.MinComparableDeltaBuckets ||
			baseAgg.calls < domain.MinComparableDeltaCalls ||
			curAgg.calls < domain.MinComparableDeltaCalls

		baseAvgMS := 0.0
		if baseAgg.calls > 0 {
			baseAvgMS = round3(baseAgg.totalExecTimeMS / float64(baseAgg.calls))
		}
		curDeltaAvgMS := 0.0
		if curAgg.calls > 0 {
			curDeltaAvgMS = round3(curAgg.totalExecTimeMS / float64(curAgg.calls))
		}
		deltaAvg := round3(curDeltaAvgMS - baseAvgMS)
		var ratio *float64
		if !insufficient && baseAvgMS > 0 {
			r := round3((curDeltaAvgMS - baseAvgMS) / baseAvgMS)
			ratio = &r
		}
		if deltaAvg > 0 || insufficient {
			regressions = append(regressions, domain.DiagnosticsRegressionItemView{
				StatementID:         curAgg.stmt.ID,
				EngineFingerprint:   curAgg.stmt.EngineFingerprint,
				DatabaseOrSchema:    curAgg.stmt.DatabaseOrSchema,
				BaselineCalls:       baseAgg.calls,
				BaselineAvgMS:       baseAvgMS,
				CurrentCalls:        curAgg.calls,
				CurrentAvgMS:        curDeltaAvgMS,
				DeltaAvgMS:          deltaAvg,
				RegressionRatio:     ratio,
				InsufficientSamples: insufficient,
			})
		}
	}

	sort.Slice(regressions, func(i, j int) bool {
		if regressions[i].InsufficientSamples != regressions[j].InsufficientSamples {
			return !regressions[i].InsufficientSamples
		}
		return regressions[i].DeltaAvgMS > regressions[j].DeltaAvgMS
	})
	if len(regressions) > 10 {
		regressions = regressions[:10]
	}

	sort.Slice(newHighLoad, func(i, j int) bool {
		return newHighLoad[i].CurrentTotalMS > newHighLoad[j].CurrentTotalMS
	})
	if len(newHighLoad) > 10 {
		newHighLoad = newHighLoad[:10]
	}
	return regressions, newHighLoad
}

func (s *Services) ensureStatementTemplate(
	ctx context.Context,
	ds *DatasourceRow,
	stmt *persistence.SQLDiagnosticsStatementModel,
) (*string, *string) {
	if plain, ok := s.openStatementTemplate(stmt); ok && plain != "" {
		return &plain, nil
	}
	if stmt.TemplateStatus == "unavailable" {
		note := "Target database did not return a normalized SQL template (restricted by privileges or length truncation); displaying aggregated metrics only"
		return nil, &note
	}

	rawTmpl, err := s.Collector.ReadTemplate(ctx, *ds, stmt.EngineFingerprint, stmt.DatabaseOrSchema)
	now := time.Now().UTC()
	if err != nil || strings.TrimSpace(rawTmpl) == "" {
		stmt.TemplateStatus = "unavailable"
		_ = s.DB.WithContext(ctx).Model(&persistence.SQLDiagnosticsStatementModel{}).
			Where("id = ?", stmt.ID).
			Updates(map[string]any{
				"template_status": "unavailable",
				"version":         gorm.Expr("version + 1"),
				"updated_at":      now,
			}).Error
		note := "Target database did not return a normalized SQL template (restricted by privileges or evicted); displaying aggregated metrics only"
		return nil, &note
	}

	env, sealErr := s.sealStatementTemplate(stmt.ID, rawTmpl)
	if sealErr != nil {
		return &rawTmpl, nil
	}
	yfp := s.computeYearningFingerprint(ds.Engine, ds.CompatibilityMode, rawTmpl)
	stmt.TemplateStatus = "available"
	stmt.TemplateCiphertext = env.Ciphertext
	stmt.TemplateNonce = env.Nonce
	stmt.TemplateKeyID = &env.KeyID
	stmt.TemplateAlgorithm = &env.Algorithm
	stmt.TemplateFetchedAt = &now
	stmt.YearningFingerprint = yfp

	_ = s.DB.WithContext(ctx).Model(&persistence.SQLDiagnosticsStatementModel{}).
		Where("id = ?", stmt.ID).
		Updates(map[string]any{
			"template_status":      "available",
			"template_ciphertext":  env.Ciphertext,
			"template_nonce":       env.Nonce,
			"template_key_id":      env.KeyID,
			"template_algorithm":   env.Algorithm,
			"template_fetched_at":  now,
			"yearning_fingerprint": yfp,
			"version":              gorm.Expr("version + 1"),
			"updated_at":           now,
		}).Error
	return &rawTmpl, nil
}

func (s *Services) buildOrderComparison(
	ctx context.Context,
	datasourceID string,
	stmt *persistence.SQLDiagnosticsStatementModel,
	from time.Time,
	to time.Time,
) (*domain.DiagnosticsOrderComparisonView, error) {
	comp := &domain.DiagnosticsOrderComparisonView{
		InsufficientSamples:     true,
		CandidateOrdersInWindow: []domain.DiagnosticsCandidateOrderItemView{},
	}

	// Load confirmed change order links.
	var links []persistence.SQLDiagnosticsOrderLinkModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and statement_id = ?", datasourceID, stmt.ID).
		Order("created_at desc").
		Find(&links).Error; err != nil {
		return nil, err
	}
	linkByOrderID := make(map[string]persistence.SQLDiagnosticsOrderLinkModel, len(links))
	for _, l := range links {
		linkByOrderID[l.ChangeOrderID] = l
	}

	// Discover completed change orders on this datasource (associated via order_stages.datasource_id).
	var candidateRows []struct {
		ID          string    `gorm:"column:id"`
		DisplayNo   string    `gorm:"column:display_no"`
		Title       string    `gorm:"column:title"`
		State       string    `gorm:"column:state"`
		CompletedAt time.Time `gorm:"column:completed_at"`
	}
	_ = s.DB.WithContext(ctx).Raw(`
		select distinct co.id::text as id, co.display_number as display_no, co.title, co.state,
		       coalesce(co.terminal_at, co.submitted_at) as completed_at
		from change_orders co
		join order_stages os on os.order_id = co.id
		where os.datasource_id = ? and co.state = 'completed'
		order by completed_at desc
		limit 10`, datasourceID).Scan(&candidateRows).Error

	for _, c := range candidateRows {
		hint := "time_overlap_only"
		var confirmedID *string
		if l, ok := linkByOrderID[c.ID]; ok {
			idCopy := l.ID
			confirmedID = &idCopy
			hint = l.RelationKind
		}
		comp.CandidateOrdersInWindow = append(comp.CandidateOrdersInWindow, domain.DiagnosticsCandidateOrderItemView{
			ChangeOrderID:   c.ID,
			DisplayNo:       c.DisplayNo,
			Title:           c.Title,
			State:           c.State,
			ExecutedAt:      c.CompletedAt,
			RelationHint:    hint,
			ConfirmedLinkID: confirmedID,
		})
	}

	if len(links) == 0 {
		return comp, nil
	}

	activeLink := links[0]
	comp.LinkedChangeOrderID = &activeLink.ChangeOrderID
	comp.LinkedOrderDisplayNo = &activeLink.OrderDisplayNo
	comp.RelationKind = &activeLink.RelationKind
	refTime := activeLink.OrderExecutedAt
	if refTime == nil {
		t := activeLink.CreatedAt
		refTime = &t
	}
	comp.ReferenceExecutedAt = refTime

	halfDur := to.Sub(from) / 2
	if halfDur < 5*time.Minute {
		halfDur = 30 * time.Minute
	}
	beforeFrom := refTime.Add(-halfDur)
	beforeTo := *refTime
	afterFrom := *refTime
	afterTo := refTime.Add(halfDur)

	afterStmtID := stmt.ID
	if activeLink.TargetStatementID != nil && *activeLink.TargetStatementID != "" {
		afterStmtID = *activeLink.TargetStatementID
	}

	beforeMetrics, err := s.computeWindowMetrics(ctx, datasourceID, stmt.ID, beforeFrom, beforeTo)
	if err != nil {
		return nil, err
	}
	afterMetrics, err := s.computeWindowMetrics(ctx, datasourceID, afterStmtID, afterFrom, afterTo)
	if err != nil {
		return nil, err
	}
	comp.BeforeWindow = beforeMetrics
	comp.AfterWindow = afterMetrics

	// D4 minimum sample threshold: both before and after windows must contain at least 3 valid delta buckets and >= 10 cumulative calls.
	comp.InsufficientSamples = beforeMetrics.BucketCount < domain.MinComparableDeltaBuckets ||
		afterMetrics.BucketCount < domain.MinComparableDeltaBuckets ||
		beforeMetrics.Calls < domain.MinComparableDeltaCalls ||
		afterMetrics.Calls < domain.MinComparableDeltaCalls

	return comp, nil
}

func (s *Services) computeWindowMetrics(
	ctx context.Context,
	datasourceID string,
	statementID string,
	from time.Time,
	to time.Time,
) (*domain.DiagnosticsWindowMetricsView, error) {
	var buckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and statement_id = ? and bucket_end >= ? and bucket_end <= ? and first_observed = false",
			datasourceID, statementID, from, to).
		Find(&buckets).Error; err != nil {
		return nil, err
	}
	var calls int64
	var totalMS float64
	for _, b := range buckets {
		calls += b.DeltaCalls
		totalMS += b.DeltaExecTimeMS
	}
	avgMS := 0.0
	if calls > 0 {
		avgMS = round3(totalMS / float64(calls))
	}
	return &domain.DiagnosticsWindowMetricsView{
		From:            from,
		To:              to,
		BucketCount:     len(buckets),
		Calls:           calls,
		TotalExecTimeMS: round3(totalMS),
		AvgExecTimeMS:   avgMS,
	}, nil
}

// ConfirmOrderLink confirms and persists the before/after comparison link between a SQL fingerprint and a change order (temporal overlap is only a hint; rewritten fingerprints are explicitly confirmed by the user).
func (s *Services) ConfirmOrderLink(
	ctx context.Context,
	actor domain.Actor,
	datasourceID string,
	statementID string,
	changeOrderID string,
	relationKind string,
	targetStatementID *string,
) (*domain.DiagnosticOrderLinkView, error) {
	switch relationKind {
	case "same_fingerprint", "rewritten_fingerprint_confirmed", "index_or_schema_change":
	default:
		return nil, domain.ErrValidation("Unsupported order relation_kind")
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

	var orderRow struct {
		ID          string     `gorm:"column:id"`
		DisplayNo   string     `gorm:"column:display_no"`
		CompletedAt *time.Time `gorm:"column:completed_at"`
		UpdatedAt   time.Time  `gorm:"column:updated_at"`
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text as id, display_number as display_no, terminal_at as completed_at, submitted_at as updated_at
		from change_orders where id = ?`, changeOrderID).Scan(&orderRow).Error; err != nil || orderRow.ID == "" {
		return nil, domain.ErrNotFound()
	}
	execAt := orderRow.CompletedAt
	if execAt == nil {
		t := orderRow.UpdatedAt
		execAt = &t
	}

	now := time.Now().UTC()
	link := persistence.SQLDiagnosticsOrderLinkModel{
		ID:                uuid.NewString(),
		DatasourceID:      datasourceID,
		StatementID:       statementID,
		TargetStatementID: targetStatementID,
		ChangeOrderID:     orderRow.ID,
		OrderDisplayNo:    orderRow.DisplayNo,
		RelationKind:      relationKind,
		OrderExecutedAt:   execAt,
		ConfirmedByUserID: actor.UserID,
		CreatedAt:         now,
	}
	var existing persistence.SQLDiagnosticsOrderLinkModel
	err := s.DB.WithContext(ctx).
		Where("statement_id = ? and change_order_id = ?", statementID, orderRow.ID).
		First(&existing).Error
	if err == nil {
		link.ID = existing.ID
		link.CreatedAt = existing.CreatedAt
		if err := s.DB.WithContext(ctx).Model(&existing).Updates(map[string]any{
			"target_statement_id":  targetStatementID,
			"relation_kind":        relationKind,
			"order_executed_at":    execAt,
			"confirmed_by_user_id": actor.UserID,
		}).Error; err != nil {
			return nil, err
		}
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.DB.WithContext(ctx).Create(&link).Error; err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	if s.Audit != nil {
		_ = s.Audit.Audit(ctx, nil, auditlog.AuditInput{
			EventType:     "diagnostics.order_link_confirmed",
			ActorKind:     "user",
			ActorID:       &actor.UserID,
			ActorUsername: &actor.Username,
			ResourceType:  "sql_diagnostics_statement",
			ResourceID:    &statementID,
			Action:        "confirm_diagnostic_order_link",
			Outcome:       "succeeded",
			Metadata: map[string]any{
				"change_order_id": link.ChangeOrderID,
				"relation_kind":   link.RelationKind,
			},
		})
	}

	return &domain.DiagnosticOrderLinkView{
		ID:                link.ID,
		DatasourceID:      link.DatasourceID,
		StatementID:       link.StatementID,
		TargetStatementID: link.TargetStatementID,
		ChangeOrderID:     link.ChangeOrderID,
		OrderDisplayNo:    link.OrderDisplayNo,
		RelationKind:      link.RelationKind,
		OrderExecutedAt:   link.OrderExecutedAt,
		ConfirmedByUserID: link.ConfirmedByUserID,
		CreatedAt:         link.CreatedAt,
	}, nil
}

func engineCostUnitNotes(compatibilityMode string) map[string]string {
	if compatibilityMode == "postgresql" {
		return map[string]string{
			"rows_examined_or_shared_blks_read": "shared_blks_read (PostgreSQL physical shared buffer blocks read, typically 8 KiB/block)",
			"rows_sent_or_rows_returned":        "rows (PostgreSQL cumulative rows returned or affected)",
			"lock_time_ms_or_temp_blks_written": "temp_blks_written (PostgreSQL temporary blocks spilled to disk)",
			"shared_blks_hit":                   "shared_blks_hit (PostgreSQL shared buffer cache block hits)",
		}
	}
	return map[string]string{
		"rows_examined_or_shared_blks_read": "SUM_ROWS_EXAMINED (MySQL storage engine rows scanned)",
		"rows_sent_or_rows_returned":        "SUM_ROWS_SENT (MySQL rows returned to client)",
		"lock_time_ms_or_temp_blks_written": "SUM_LOCK_TIME (MySQL table/row lock wait time in ms)",
	}
}

func sortStatementRows(rows []domain.DiagnosticsStatementRowView, sortBy string) {
	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "avg_exec_time_ms":
			if rows[i].AvgExecTimeMS != rows[j].AvgExecTimeMS {
				return rows[i].AvgExecTimeMS > rows[j].AvgExecTimeMS
			}
		case "calls":
			if rows[i].Calls != rows[j].Calls {
				return rows[i].Calls > rows[j].Calls
			}
		case "rows_examined":
			if rows[i].RowsExaminedOrSharedBlksRead != rows[j].RowsExaminedOrSharedBlksRead {
				return rows[i].RowsExaminedOrSharedBlksRead > rows[j].RowsExaminedOrSharedBlksRead
			}
		default:
			if rows[i].TotalExecTimeMS != rows[j].TotalExecTimeMS {
				return rows[i].TotalExecTimeMS > rows[j].TotalExecTimeMS
			}
		}
		return rows[i].StatementID < rows[j].StatementID
	})
}

func truncatePreview(s string, maxRunes int) string {
	collapsed := strings.Join(strings.Fields(s), " ")
	runes := []rune(collapsed)
	if len(runes) <= maxRunes {
		return collapsed
	}
	return string(runes[:maxRunes]) + "…"
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
