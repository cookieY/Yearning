// aggregator.go implements the 6 overview chart groups, 3 statement detail chart groups,
// on-demand SQL template fetching, and comparable before/after change order window comparisons.
package application

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
)

const unobservedHighLoadNote = "Not observed in the comparison window Top-N snapshots or collection range; does not imply the statement never executed historically"

// OverviewQuery describes the filter parameters for GET /admin/diagnostics/datasources/{datasource_id}/overview.
type OverviewQuery struct {
	From        time.Time
	To          time.Time
	CompareFrom *time.Time
	CompareTo   *time.Time
	SortBy      string
	Limit       int
}

type aggregatedStatementStat struct {
	stmt                         persistence.SQLDiagnosticsStatementModel
	deltaBucketCount             int
	cumulativeBucketCount        int
	resetDetected                bool
	calls                        int64
	totalExecTimeMS              float64
	rowsExaminedOrSharedBlksRead int64
	rowsSentOrRowsReturned       int64
	lockTimeMSOrTempBlksWritten  float64
	latestCallsTotal             int64
	latestTotalExecTimeMS        float64
	latestRowsExamined           int64
	latestRowsSent               int64
	latestLockOrTemp             float64
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

	now := time.Now().UTC()
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
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sortBy := q.SortBy
	switch sortBy {
	case "", "total_exec_time_ms", "avg_exec_time_ms", "calls", "rows_examined":
		if sortBy == "" {
			sortBy = "total_exec_time_ms"
		}
	default:
		return nil, domain.ErrValidation("Unsupported sort_by field")
	}

	var snapshots []persistence.SQLDiagnosticsSnapshotModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and sampled_at >= ? and sampled_at <= ? and probe_state in ('ready', 'no_samples')", datasourceID, from, to).
		Order("sampled_at asc, id asc").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}

	// Auto-collect an initial snapshot on demand when diagnostics is enabled and the live window has no snapshots yet.
	if len(snapshots) == 0 && cfg.Enabled && cfg.State != "paused_on_failure" && (q.To.IsZero() || now.Sub(to) < 2*time.Minute) {
		if cfg.LastCollectedAt == nil || now.Sub(*cfg.LastCollectedAt) >= 15*time.Second {
			if _, collectErr := s.CollectScheduledSnapshot(ctx, datasourceID, now); collectErr == nil {
				if refreshedCfg, cfgErr := s.ensureDiagnosticsConfigRow(ctx, datasourceID); cfgErr == nil {
					cfg = refreshedCfg
				}
				if now.After(to) {
					to = now
				}
				_ = s.DB.WithContext(ctx).
					Where("datasource_id = ? and sampled_at >= ? and sampled_at <= ? and probe_state in ('ready', 'no_samples')", datasourceID, from, to).
					Order("sampled_at asc, id asc").
					Find(&snapshots).Error
			} else if refreshedCfg, cfgErr := s.ensureDiagnosticsConfigRow(ctx, datasourceID); cfgErr == nil {
				cfg = refreshedCfg
			}
		}
	}

	var buckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and bucket_end >= ? and bucket_end <= ?", datasourceID, from, to).
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

	// Determine the overall coverage mode (cumulative_only | window_delta | reset_or_gap).
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
	overallCoverage := "cumulative_only"
	if hasDelta && totalWindowDeltaCalls > 0 {
		if hasResetOrGap {
			overallCoverage = "reset_or_gap"
		} else {
			overallCoverage = "window_delta"
		}
	}

	// Aggregate per-statement metrics and per-snapshot bucket metrics within the current window.
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
		agg.latestCallsTotal = b.CallsTotal
		agg.latestTotalExecTimeMS = b.TotalExecTimeMS
		agg.latestRowsExamined = b.RowsExaminedOrSharedBlksReadTotal
		agg.latestRowsSent = b.RowsSentOrRowsReturnedTotal
		agg.latestLockOrTemp = b.LockTimeMSOrTempBlksWrittenTotal
		if b.ResetDetected {
			agg.resetDetected = true
		}
		if b.FirstObserved {
			agg.cumulativeBucketCount++
		} else {
			agg.deltaBucketCount++
			agg.calls += b.DeltaCalls
			agg.totalExecTimeMS += b.DeltaExecTimeMS
			agg.rowsExaminedOrSharedBlksRead += b.DeltaRowsExaminedOrSharedBlksRead
			agg.rowsSentOrRowsReturned += b.DeltaRowsSentOrRowsReturned
			agg.lockTimeMSOrTempBlksWritten += b.DeltaLockTimeMSOrTempBlksWritten
		}
	}

	// Chart 1: SQL load trend line chart.
	loadTrend := make([]domain.DiagnosticsLoadBucketPointView, 0, len(snapshots))
	for _, snap := range snapshots {
		snapBuckets := bucketBySnap[snap.ID]
		if len(snapBuckets) == 0 {
			loadTrend = append(loadTrend, domain.DiagnosticsLoadBucketPointView{
				BucketStart:     snap.SampledAt.Add(-time.Duration(maxInt(60, cfg.IntervalSeconds)) * time.Second),
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

	// Compute grand total execution time across the window for share calculation.
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

	// Populate readable SQL template previews for top statements in the overview when not yet cached.
	if s.Collector != nil && ds != nil {
		previewLimit := 25
		if len(stmtRows) < previewLimit {
			previewLimit = len(stmtRows)
		}
		for i := 0; i < previewLimit; i++ {
			if stmtRows[i].TemplatePreview == nil {
				if rawTmpl, terr := s.Collector.ReadTemplate(ctx, *ds, stmtRows[i].EngineFingerprint, stmtRows[i].DatabaseOrSchema); terr == nil && strings.TrimSpace(rawTmpl) != "" {
					p := truncatePreview(rawTmpl, 160)
					stmtRows[i].TemplatePreview = &p
				}
			}
		}
	}

	// Chart 2: Top 10 SQL hotspots horizontal bar chart.
	topCount := 10
	if len(stmtRows) < topCount {
		topCount = len(stmtRows)
	}
	topStatements := make([]domain.DiagnosticsTopStatementItemView, 0, topCount)
	for i := 0; i < topCount; i++ {
		r := stmtRows[i]
		topStatements = append(topStatements, domain.DiagnosticsTopStatementItemView{
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

	// Chart 3: Call count vs. average latency scatter plot.
	scatterPoints := make([]domain.DiagnosticsScatterPointView, 0, len(stmtRows))
	for _, r := range stmtRows {
		scatterPoints = append(scatterPoints, domain.DiagnosticsScatterPointView{
			StatementID:                  r.StatementID,
			EngineFingerprint:            r.EngineFingerprint,
			DatabaseOrSchema:             r.DatabaseOrSchema,
			Calls:                        r.Calls,
			AvgExecTimeMS:                r.AvgExecTimeMS,
			TotalExecTimeMS:              r.TotalExecTimeMS,
			RowsExaminedOrSharedBlksRead: r.RowsExaminedOrSharedBlksRead,
		})
	}

	// Chart 4: SQL hotspot heatmap (Top 10 fingerprints x time buckets).
	heatmap := make([]domain.DiagnosticsHeatmapSeriesView, 0, topCount)
	for i := 0; i < topCount; i++ {
		topStmt := stmtRows[i]
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
			} else {
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
		}
		heatmap = append(heatmap, domain.DiagnosticsHeatmapSeriesView{
			StatementID:       topStmt.StatementID,
			EngineFingerprint: topStmt.EngineFingerprint,
			DatabaseOrSchema:  topStmt.DatabaseOrSchema,
			Buckets:           cells,
		})
	}

	// Chart 5 & Chart 6: Performance regression ranking and newly observed high-load SQL against the comparison window (compareFrom..compareTo).
	var baselineBuckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and bucket_end >= ? and bucket_end < ?", datasourceID, compareFrom, compareTo).
		Find(&baselineBuckets).Error; err != nil {
		return nil, err
	}
	baseByStmt := make(map[string]*aggregatedStatementStat)
	for _, b := range baselineBuckets {
		st, ok := stmtByID[b.StatementID]
		if !ok {
			continue
		}
		agg, exists := baseByStmt[b.StatementID]
		if !exists {
			agg = &aggregatedStatementStat{stmt: st}
			baseByStmt[b.StatementID] = agg
		}
		if b.FirstObserved {
			agg.cumulativeBucketCount++
		} else {
			agg.deltaBucketCount++
			agg.calls += b.DeltaCalls
			agg.totalExecTimeMS += b.DeltaExecTimeMS
		}
	}

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

	if len(stmtRows) > limit {
		stmtRows = stmtRows[:limit]
	}

	probeState := cfg.LastProbeState
	if !cfg.Enabled {
		probeState = "disabled"
	}

	return &domain.DiagnosticsOverviewView{
		DatasourceID:      ds.ID,
		Engine:            ds.Engine,
		CompatibilityMode: ds.CompatibilityMode,
		ProbeState:        probeState,
		State:             cfg.State,
		CoverageMode:      overallCoverage,
		From:              from,
		To:                to,
		CompareFrom:       &compareFrom,
		CompareTo:         &compareTo,
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

// GetStatementDetail returns the single SQL fingerprint detail view (including on-demand SQL template fetching, 3 trend/cost/before-after order comparison charts, and the latest AI report).
func (s *Services) GetStatementDetail(
	ctx context.Context,
	datasourceID string,
	statementID string,
	from time.Time,
	to time.Time,
) (*domain.DiagnosticStatementDetailView, error) {
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

	// Fetch the normalized SQL template on demand (executed only when opening detail or starting AI diagnosis).
	templateSQL, templateNote := s.ensureStatementTemplate(ctx, ds, &stmt)

	now := time.Now().UTC()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() || !from.Before(to) {
		from = to.Add(-1 * time.Hour)
	}

	var snapshots []persistence.SQLDiagnosticsSnapshotModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and sampled_at >= ? and sampled_at <= ? and probe_state in ('ready', 'no_samples')", datasourceID, from, to).
		Order("sampled_at asc, id asc").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}

	var stmtBuckets []persistence.SQLDiagnosticsBucketMetricModel
	if err := s.DB.WithContext(ctx).
		Where("datasource_id = ? and statement_id = ? and bucket_end >= ? and bucket_end <= ?", datasourceID, statementID, from, to).
		Order("bucket_end asc, id asc").
		Find(&stmtBuckets).Error; err != nil {
		return nil, err
	}
	bucketBySnap := make(map[string]persistence.SQLDiagnosticsBucketMetricModel, len(stmtBuckets))
	agg := &aggregatedStatementStat{stmt: stmt}
	for _, b := range stmtBuckets {
		bucketBySnap[b.SnapshotID] = b
		agg.latestCallsTotal = b.CallsTotal
		agg.latestTotalExecTimeMS = b.TotalExecTimeMS
		agg.latestRowsExamined = b.RowsExaminedOrSharedBlksReadTotal
		agg.latestRowsSent = b.RowsSentOrRowsReturnedTotal
		agg.latestLockOrTemp = b.LockTimeMSOrTempBlksWrittenTotal
		if b.ResetDetected {
			agg.resetDetected = true
		}
		if b.FirstObserved {
			agg.cumulativeBucketCount++
		} else {
			agg.deltaBucketCount++
			agg.calls += b.DeltaCalls
			agg.totalExecTimeMS += b.DeltaExecTimeMS
			agg.rowsExaminedOrSharedBlksRead += b.DeltaRowsExaminedOrSharedBlksRead
			agg.rowsSentOrRowsReturned += b.DeltaRowsSentOrRowsReturned
			agg.lockTimeMSOrTempBlksWritten += b.DeltaLockTimeMSOrTempBlksWritten
		}
	}

	overallCoverage := "cumulative_only"
	if agg.deltaBucketCount > 0 && agg.calls > 0 {
		if agg.resetDetected {
			overallCoverage = "reset_or_gap"
		} else {
			overallCoverage = "window_delta"
		}
	}

	// Detail Chart 1 (average latency and call count trend, shared time axis) and Detail Chart 2 (execution cost trend).
	trendSeries := make([]domain.DiagnosticsStatementTrendPointView, 0, len(snapshots))
	costSeries := make([]domain.DiagnosticsStatementCostPointView, 0, len(snapshots))
	for _, snap := range snapshots {
		b, ok := bucketBySnap[snap.ID]
		if !ok {
			trendSeries = append(trendSeries, domain.DiagnosticsStatementTrendPointView{
				BucketStart:         snap.SampledAt.Add(-60 * time.Second),
				BucketEnd:           snap.SampledAt,
				Calls:               0,
				AvgExecTimeMS:       0,
				TotalExecTimeMS:     0,
				ResetDetected:       snap.ResetDetected,
				UnobservedBelowTopN: true,
			})
			costSeries = append(costSeries, domain.DiagnosticsStatementCostPointView{
				BucketStart:                  snap.SampledAt.Add(-60 * time.Second),
				BucketEnd:                    snap.SampledAt,
				RowsExaminedOrSharedBlksRead: 0,
				RowsSentOrRowsReturned:       0,
				LockTimeMSOrTempBlksWritten:  0,
			})
			continue
		}
		calls := b.DeltaCalls
		totalMS := b.DeltaExecTimeMS
		rowsExamined := b.DeltaRowsExaminedOrSharedBlksRead
		rowsSent := b.DeltaRowsSentOrRowsReturned
		lockOrTemp := b.DeltaLockTimeMSOrTempBlksWritten
		if overallCoverage == "cumulative_only" {
			calls = b.CallsTotal
			totalMS = b.TotalExecTimeMS
			rowsExamined = b.RowsExaminedOrSharedBlksReadTotal
			rowsSent = b.RowsSentOrRowsReturnedTotal
			lockOrTemp = b.LockTimeMSOrTempBlksWrittenTotal
		}
		avgMS := 0.0
		if calls > 0 {
			avgMS = round3(totalMS / float64(calls))
		}
		trendSeries = append(trendSeries, domain.DiagnosticsStatementTrendPointView{
			BucketStart:         b.BucketStart,
			BucketEnd:           b.BucketEnd,
			Calls:               calls,
			AvgExecTimeMS:       avgMS,
			TotalExecTimeMS:     round3(totalMS),
			ResetDetected:       b.ResetDetected,
			UnobservedBelowTopN: false,
		})
		costSeries = append(costSeries, domain.DiagnosticsStatementCostPointView{
			BucketStart:                  b.BucketStart,
			BucketEnd:                    b.BucketEnd,
			RowsExaminedOrSharedBlksRead: rowsExamined,
			RowsSentOrRowsReturned:       rowsSent,
			LockTimeMSOrTempBlksWritten:  round3(lockOrTemp),
			SharedBlksHit:                b.DeltaSharedBlksHit,
		})
	}

	calls, totalMS, avgMS, rowsExamined, rowsSent, lockOrTemp, mode := agg.effectiveMetrics(overallCoverage)
	if len(stmtBuckets) == 0 {
		calls = stmt.LatestCallsTotal
		totalMS = round3(stmt.LatestTotalExecTimeMS)
		if calls > 0 {
			avgMS = round3(totalMS / float64(calls))
		}
		rowsExamined = stmt.LatestRowsExaminedOrSharedBlksReadTotal
		rowsSent = stmt.LatestRowsSentOrRowsReturnedTotal
		lockOrTemp = round3(stmt.LatestLockTimeMSOrTempBlksWrittenTotal)
	}
	var preview *string
	if templateSQL != nil && *templateSQL != "" {
		p := truncatePreview(*templateSQL, 160)
		preview = &p
	}
	summary := domain.DiagnosticsStatementRowView{
		StatementID:                  stmt.ID,
		EngineFingerprint:            stmt.EngineFingerprint,
		YearningFingerprint:          stmt.YearningFingerprint,
		DatabaseOrSchema:             stmt.DatabaseOrSchema,
		TemplateStatus:               stmt.TemplateStatus,
		TemplatePreview:              preview,
		Calls:                        calls,
		TotalExecTimeMS:              totalMS,
		AvgExecTimeMS:                avgMS,
		RowsExaminedOrSharedBlksRead: rowsExamined,
		RowsSentOrRowsReturned:       rowsSent,
		LockTimeMSOrTempBlksWritten:  lockOrTemp,
		ShareOfTotalTime:             1.0,
		CoverageMode:                 mode,
		FirstSeenAt:                  stmt.FirstSeenAt,
		LastSeenAt:                   stmt.LastSeenAt,
	}

	// Detail Chart 3: Comparable window comparison before and after change order execution.
	orderComp, err := s.buildOrderComparison(ctx, ds.ID, &stmt, from, to)
	if err != nil {
		return nil, err
	}

	// Load the latest non-expired AI diagnosis report.
	latestReport, _ := s.loadLatestAIReport(ctx, ds.ID, stmt.ID)

	return &domain.DiagnosticStatementDetailView{
		StatementID:         stmt.ID,
		DatasourceID:        ds.ID,
		Engine:              ds.Engine,
		CompatibilityMode:   ds.CompatibilityMode,
		EngineFingerprint:   stmt.EngineFingerprint,
		YearningFingerprint: stmt.YearningFingerprint,
		DatabaseOrSchema:    stmt.DatabaseOrSchema,
		TemplateStatus:      stmt.TemplateStatus,
		TemplateSQL:         templateSQL,
		TemplateNote:        templateNote,
		CoverageMode:        overallCoverage,
		From:                from,
		To:                  to,
		Summary:             summary,
		CostUnitNotes:       engineCostUnitNotes(ds.CompatibilityMode),
		TrendSeries:         trendSeries,
		CostSeries:          costSeries,
		OrderComparison:     *orderComp,
		LatestAIReport:      latestReport,
	}, nil
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
