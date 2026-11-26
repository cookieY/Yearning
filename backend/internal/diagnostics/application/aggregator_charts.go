// aggregator_charts.go holds aggregator domain helpers and operations.
package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/platform/pagination"
	"time"
)

func (a *aggregatedStatementStat) absorbBucket(b persistence.SQLDiagnosticsBucketMetricModel) {
	a.latestCallsTotal = b.CallsTotal
	a.latestTotalExecTimeMS = b.TotalExecTimeMS
	a.latestRowsExamined = b.RowsExaminedOrSharedBlksReadTotal
	a.latestRowsSent = b.RowsSentOrRowsReturnedTotal
	a.latestLockOrTemp = b.LockTimeMSOrTempBlksWrittenTotal
	if b.ResetDetected {
		a.resetDetected = true
	}
	if b.FirstObserved {
		a.cumulativeBucketCount++
	} else {
		a.deltaBucketCount++
		a.calls += b.DeltaCalls
		a.totalExecTimeMS += b.DeltaExecTimeMS
		a.rowsExaminedOrSharedBlksRead += b.DeltaRowsExaminedOrSharedBlksRead
		a.rowsSentOrRowsReturned += b.DeltaRowsSentOrRowsReturned
		a.lockTimeMSOrTempBlksWritten += b.DeltaLockTimeMSOrTempBlksWritten
	}
}

func aggregateBucketsByStatement(
	buckets []persistence.SQLDiagnosticsBucketMetricModel,
	stmtByID map[string]persistence.SQLDiagnosticsStatementModel,
) (map[string]*aggregatedStatementStat, map[string][]persistence.SQLDiagnosticsBucketMetricModel) {
	byStmt := make(map[string]*aggregatedStatementStat)
	bucketBySnap := make(map[string][]persistence.SQLDiagnosticsBucketMetricModel)
	for _, b := range buckets {
		bucketBySnap[b.SnapshotID] = append(bucketBySnap[b.SnapshotID], b)
		st, ok := stmtByID[b.StatementID]
		if !ok {
			continue
		}
		agg, exists := byStmt[b.StatementID]
		if !exists {
			agg = &aggregatedStatementStat{stmt: st}
			byStmt[b.StatementID] = agg
		}
		agg.absorbBucket(b)
	}
	return byStmt, bucketBySnap
}

func (a *aggregatedStatementStat) effectiveMetrics(overallMode string) (calls int64, totalMS float64, avgMS float64, rowsExamined int64, rowsSent int64, lockOrTemp float64, mode string) {
	if overallMode != "cumulative_only" && a.deltaBucketCount > 0 {
		calls = a.calls
		totalMS = a.totalExecTimeMS
		rowsExamined = a.rowsExaminedOrSharedBlksRead
		rowsSent = a.rowsSentOrRowsReturned
		lockOrTemp = a.lockTimeMSOrTempBlksWritten
		if a.resetDetected {
			mode = "reset_or_gap"
		} else {
			mode = "window_delta"
		}
	} else {
		mode = "cumulative_only"
		calls = a.latestCallsTotal
		totalMS = a.latestTotalExecTimeMS
		rowsExamined = a.latestRowsExamined
		rowsSent = a.latestRowsSent
		lockOrTemp = a.latestLockOrTemp
	}
	if calls > 0 {
		avgMS = round3(totalMS / float64(calls))
	}
	totalMS = round3(totalMS)
	lockOrTemp = round3(lockOrTemp)
	return
}

func resolveOverviewQuery(q OverviewQuery, now time.Time) (overviewWindow, error) {
	to := q.To.UTC()
	if to.IsZero() {
		to = now
	}
	from := q.From.UTC()
	if from.IsZero() || !from.Before(to) {
		from = to.Add(-1 * time.Hour)
	}
	windowDur := to.Sub(from)
	compareTo := from
	compareFrom := from.Add(-windowDur)
	if q.CompareFrom != nil && q.CompareTo != nil && q.CompareFrom.Before(*q.CompareTo) {
		compareFrom = q.CompareFrom.UTC()
		compareTo = q.CompareTo.UTC()
	}
	limit := pagination.ClampLimit(q.Limit, 50, 200)
	sortBy := q.SortBy
	switch sortBy {
	case "", "total_exec_time_ms", "avg_exec_time_ms", "calls", "rows_examined":
		if sortBy == "" {
			sortBy = "total_exec_time_ms"
		}
	default:
		return overviewWindow{}, domain.ErrValidation("Unsupported sort_by field")
	}
	return overviewWindow{
		now:         now,
		from:        from,
		to:          to,
		compareFrom: compareFrom,
		compareTo:   compareTo,
		sortBy:      sortBy,
		limit:       limit,
	}, nil
}

// GetOverview returns the 6 overview chart groups and sortable SQL statement table for the given datasource and time window (P101).
func (s *Services) GetOverview(ctx context.Context, datasourceID string, q OverviewQuery) (*domain.DiagnosticsOverviewView, error) {
	ds, err := s.loadDatasourceRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.ensureDiagnosticsConfigRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}

	win, err := resolveOverviewQuery(q, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	var snapshots []persistence.SQLDiagnosticsSnapshotModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and sampled_at >= ? and sampled_at <= ? and probe_state in ('ready', 'no_samples')", datasourceID, win.from, win.to).
		Order("sampled_at asc, id asc").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}

	// Auto-collect an initial snapshot on demand when diagnostics is enabled and the live window has no snapshots yet.
	if len(snapshots) == 0 && cfg.Enabled && cfg.State != "paused_on_failure" && (q.To.IsZero() || win.now.Sub(win.to) < 2*time.Minute) {
		if cfg.LastCollectedAt == nil || win.now.Sub(*cfg.LastCollectedAt) >= 15*time.Second {
			if _, collectErr := s.CollectScheduledSnapshot(ctx, datasourceID, win.now); collectErr == nil {
				if refreshedCfg, cfgErr := s.ensureDiagnosticsConfigRow(ctx, datasourceID); cfgErr == nil {
					cfg = refreshedCfg
				}
				if win.now.After(win.to) {
					win.to = win.now
				}
				_ = s.DB.WithContext(ctx).
					Where("datasource_id = ? and sampled_at >= ? and sampled_at <= ? and probe_state in ('ready', 'no_samples')", datasourceID, win.from, win.to).
					Order("sampled_at asc, id asc").
					Find(&snapshots).Error
			} else if refreshedCfg, cfgErr := s.ensureDiagnosticsConfigRow(ctx, datasourceID); cfgErr == nil {
				cfg = refreshedCfg
			}
		}
	}

	var buckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and bucket_end >= ? and bucket_end <= ?", datasourceID, win.from, win.to).
		Order("bucket_end asc, id asc").
		Find(&buckets).Error; err != nil {
		return nil, err
	}

	var stmts []persistence.SQLDiagnosticsStatementModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ?", datasourceID).
		Find(&stmts).Error; err != nil {
		return nil, err
	}
	stmtByID := make(map[string]persistence.SQLDiagnosticsStatementModel, len(stmts))
	for _, st := range stmts {
		stmtByID[st.ID] = st
	}

	overallCoverage := determineOverallCoverageMode(snapshots, buckets)
	byStmt, bucketBySnap := aggregateBucketsByStatement(buckets, stmtByID)
	loadTrend := buildOverviewLoadTrend(snapshots, bucketBySnap, overallCoverage, cfg.IntervalSeconds)
	stmtRows := s.buildSortedStatementRows(ctx, ds, byStmt, overallCoverage, win.sortBy)

	topCount := min(10, len(stmtRows))
	topStatements := buildOverviewTopStatements(stmtRows[:topCount])
	scatterPoints := buildOverviewScatterPoints(stmtRows)
	heatmap := buildOverviewHeatmap(stmtRows[:topCount], snapshots, bucketBySnap, overallCoverage)

	var baselineBuckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and bucket_end >= ? and bucket_end < ?", datasourceID, win.compareFrom, win.compareTo).
		Find(&baselineBuckets).Error; err != nil {
		return nil, err
	}
	baseByStmt, _ := aggregateBucketsByStatement(baselineBuckets, stmtByID)
	regressions, newHighLoad := buildOverviewComparisonCharts(byStmt, baseByStmt, overallCoverage)

	if len(stmtRows) > win.limit {
		stmtRows = stmtRows[:win.limit]
	}

	probeState := normalizeProbeState(cfg.LastProbeState)

	return &domain.DiagnosticsOverviewView{
		DatasourceID:      ds.ID,
		Engine:            ds.Engine,
		CompatibilityMode: ds.CompatibilityMode,
		ProbeState:        probeState,
		State:             cfg.State,
		CoverageMode:      overallCoverage,
		From:              win.from,
		To:                win.to,
		CompareFrom:       &win.compareFrom,
		CompareTo:         &win.compareTo,
		LastCollectedAt:   cfg.LastCollectedAt,
		SnapshotCount:     len(snapshots),
		LoadTrend:         loadTrend,
		TopStatements:     topStatements,
		ScatterPoints:     scatterPoints,
		Heatmap:           heatmap,
		Regressions:       regressions,
		NewHighLoad:       newHighLoad,
		Statements:        stmtRows,
	}, nil
}

func determineOverallCoverageMode(
	snapshots []persistence.SQLDiagnosticsSnapshotModel,
	buckets []persistence.SQLDiagnosticsBucketMetricModel,
) string {
	hasDelta := false
	hasResetOrGap := false
	var totalWindowDeltaCalls int64
	for _, snap := range snapshots {
		if snap.ResetDetected || snap.GapDetected {
			hasResetOrGap = true
		}
	}
	for _, b := range buckets {
		if !b.FirstObserved {
			hasDelta = true
			totalWindowDeltaCalls += b.DeltaCalls
		}
		if b.ResetDetected || b.CoverageMode == "reset_or_gap" {
			hasResetOrGap = true
		}
	}
	if hasDelta && totalWindowDeltaCalls > 0 {
		if hasResetOrGap {
			return "reset_or_gap"
		}
		return "window_delta"
	}
	return "cumulative_only"
}

func buildOverviewLoadTrend(
	snapshots []persistence.SQLDiagnosticsSnapshotModel,
	bucketBySnap map[string][]persistence.SQLDiagnosticsBucketMetricModel,
	overallCoverage string,
	intervalSeconds int,
) []domain.DiagnosticsLoadBucketPointView {
	loadTrend := make([]domain.DiagnosticsLoadBucketPointView, 0, len(snapshots))
	for _, snap := range snapshots {
		snapBuckets := bucketBySnap[snap.ID]
		if len(snapBuckets) == 0 {
			loadTrend = append(loadTrend, domain.DiagnosticsLoadBucketPointView{
				BucketStart:     snap.SampledAt.Add(-time.Duration(maxInt(60, intervalSeconds)) * time.Second),
				BucketEnd:       snap.SampledAt,
				TotalExecTimeMS: 0,
				Calls:           0,
				AvgExecTimeMS:   0,
				ResetDetected:   snap.ResetDetected,
			})
			continue
		}
		var bStart, bEnd time.Time
		var totalMS float64
		var totalCalls int64
		reset := snap.ResetDetected
		for i, sb := range snapBuckets {
			if i == 0 {
				bStart = sb.BucketStart
				bEnd = sb.BucketEnd
			}
			if sb.ResetDetected {
				reset = true
			}
			if overallCoverage == "cumulative_only" {
				totalMS += sb.TotalExecTimeMS
				totalCalls += sb.CallsTotal
			} else {
				totalMS += sb.DeltaExecTimeMS
				totalCalls += sb.DeltaCalls
			}
		}
		avgMS := 0.0
		if totalCalls > 0 {
			avgMS = round3(totalMS / float64(totalCalls))
		}
		loadTrend = append(loadTrend, domain.DiagnosticsLoadBucketPointView{
			BucketStart:     bStart,
			BucketEnd:       bEnd,
			TotalExecTimeMS: round3(totalMS),
			Calls:           totalCalls,
			AvgExecTimeMS:   avgMS,
			ResetDetected:   reset,
		})
	}
	return loadTrend
}

func (s *Services) buildSortedStatementRows(
	ctx context.Context,
	ds *DatasourceRow,
	byStmt map[string]*aggregatedStatementStat,
	overallCoverage string,
	sortBy string,
) []domain.DiagnosticsStatementRowView {
	var grandTotalExecMS float64
	stmtRows := make([]domain.DiagnosticsStatementRowView, 0, len(byStmt))
	for _, agg := range byStmt {
		_, totalMS, _, _, _, _, _ := agg.effectiveMetrics(overallCoverage)
		grandTotalExecMS += totalMS
	}
	for _, agg := range byStmt {
		calls, totalMS, avgMS, rowsExamined, rowsSent, lockOrTemp, mode := agg.effectiveMetrics(overallCoverage)
		share := 0.0
		if grandTotalExecMS > 0 {
			share = round4(totalMS / grandTotalExecMS)
		}
		var preview *string
		if plain, ok := s.openStatementTemplate(&agg.stmt); ok && plain != "" {
			p := truncatePreview(plain, 160)
			preview = &p
		}
		stmtRows = append(stmtRows, domain.DiagnosticsStatementRowView{
			StatementID:                  agg.stmt.ID,
			EngineFingerprint:            agg.stmt.EngineFingerprint,
			YearningFingerprint:          agg.stmt.YearningFingerprint,
			DatabaseOrSchema:             agg.stmt.DatabaseOrSchema,
			TemplateStatus:               agg.stmt.TemplateStatus,
			TemplatePreview:              preview,
			Calls:                        calls,
			TotalExecTimeMS:              totalMS,
			AvgExecTimeMS:                avgMS,
			RowsExaminedOrSharedBlksRead: rowsExamined,
			RowsSentOrRowsReturned:       rowsSent,
			LockTimeMSOrTempBlksWritten:  lockOrTemp,
			ShareOfTotalTime:             share,
			CoverageMode:                 mode,
			FirstSeenAt:                  agg.stmt.FirstSeenAt,
			LastSeenAt:                   agg.stmt.LastSeenAt,
		})
	}
	sortStatementRows(stmtRows, sortBy)
	s.backfillOverviewTemplatePreviews(ctx, ds, stmtRows)
	return stmtRows
}

func buildOverviewTopStatements(topRows []domain.DiagnosticsStatementRowView) []domain.DiagnosticsTopStatementItemView {
	out := make([]domain.DiagnosticsTopStatementItemView, 0, len(topRows))
	for _, r := range topRows {
		out = append(out, domain.DiagnosticsTopStatementItemView{
			StatementID:       r.StatementID,
			EngineFingerprint: r.EngineFingerprint,
			DatabaseOrSchema:  r.DatabaseOrSchema,
			TemplatePreview:   r.TemplatePreview,
			Calls:             r.Calls,
			TotalExecTimeMS:   r.TotalExecTimeMS,
			AvgExecTimeMS:     r.AvgExecTimeMS,
			ShareOfTotalTime:  r.ShareOfTotalTime,
		})
	}
	return out
}

func buildOverviewScatterPoints(stmtRows []domain.DiagnosticsStatementRowView) []domain.DiagnosticsScatterPointView {
	out := make([]domain.DiagnosticsScatterPointView, 0, len(stmtRows))
	for _, r := range stmtRows {
		out = append(out, domain.DiagnosticsScatterPointView{
			StatementID:                  r.StatementID,
			EngineFingerprint:            r.EngineFingerprint,
			DatabaseOrSchema:             r.DatabaseOrSchema,
			Calls:                        r.Calls,
			AvgExecTimeMS:                r.AvgExecTimeMS,
			TotalExecTimeMS:              r.TotalExecTimeMS,
			RowsExaminedOrSharedBlksRead: r.RowsExaminedOrSharedBlksRead,
		})
	}
	return out
}

func buildOverviewHeatmap(
	topRows []domain.DiagnosticsStatementRowView,
	snapshots []persistence.SQLDiagnosticsSnapshotModel,
	bucketBySnap map[string][]persistence.SQLDiagnosticsBucketMetricModel,
	overallCoverage string,
) []domain.DiagnosticsHeatmapSeriesView {
	heatmap := make([]domain.DiagnosticsHeatmapSeriesView, 0, len(topRows))
	for _, topStmt := range topRows {
		cells := make([]domain.DiagnosticsHeatmapBucketCellView, 0, len(snapshots))
		for _, snap := range snapshots {
			snapBuckets := bucketBySnap[snap.ID]
			var matched *persistence.SQLDiagnosticsBucketMetricModel
			for j := range snapBuckets {
				if snapBuckets[j].StatementID == topStmt.StatementID {
					matched = &snapBuckets[j]
					break
				}
			}
			if matched == nil {
				cells = append(cells, domain.DiagnosticsHeatmapBucketCellView{
					BucketStart:         snap.SampledAt,
					TotalExecTimeMS:     0,
					Calls:               0,
					UnobservedBelowTopN: true,
				})
				continue
			}
			execMS := matched.DeltaExecTimeMS
			calls := matched.DeltaCalls
			if overallCoverage == "cumulative_only" {
				execMS = matched.TotalExecTimeMS
				calls = matched.CallsTotal
			}
			cells = append(cells, domain.DiagnosticsHeatmapBucketCellView{
				BucketStart:         matched.BucketEnd,
				TotalExecTimeMS:     round3(execMS),
				Calls:               calls,
				UnobservedBelowTopN: false,
			})
		}
		heatmap = append(heatmap, domain.DiagnosticsHeatmapSeriesView{
			StatementID:       topStmt.StatementID,
			EngineFingerprint: topStmt.EngineFingerprint,
			DatabaseOrSchema:  topStmt.DatabaseOrSchema,
			Buckets:           cells,
		})
	}
	return heatmap
}
