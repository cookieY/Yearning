// aggregator.go implements the 6 overview chart groups, 3 statement detail chart groups,
// on-demand SQL template fetching, and comparable before/after change order window comparisons.
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"gorm.io/gorm"
	"strings"
	"sync"
	"time"
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

type overviewWindow struct {
	now         time.Time
	from        time.Time
	to          time.Time
	compareFrom time.Time
	compareTo   time.Time
	sortBy      string
	limit       int
}

const overviewPreviewConcurrency = 2

// backfillOverviewTemplatePreviews fetches uncached readable SQL templates for top overview statements
// with worker concurrency aligned to the D5 <= 2 connection budget, aborting early on target-wide errors.
func (s *Services) backfillOverviewTemplatePreviews(ctx context.Context, ds *DatasourceRow, stmtRows []domain.DiagnosticsStatementRowView) {
	if s.Collector == nil || ds == nil || len(stmtRows) == 0 {
		return
	}
	previewLimit := min(25, len(stmtRows))
	type previewSlot struct {
		idx               int
		engineFingerprint string
		databaseOrSchema  string
	}
	var pending []previewSlot
	for i := 0; i < previewLimit; i++ {
		if stmtRows[i].TemplatePreview == nil && stmtRows[i].TemplateStatus != "unavailable" {
			pending = append(pending, previewSlot{
				idx:               i,
				engineFingerprint: stmtRows[i].EngineFingerprint,
				databaseOrSchema:  stmtRows[i].DatabaseOrSchema,
			})
		}
	}
	if len(pending) == 0 {
		return
	}
	results := make([]*string, previewLimit)
	sem := make(chan struct{}, overviewPreviewConcurrency)
	fetchCtx, cancelFetch := context.WithCancel(ctx)
	defer cancelFetch()
	var wg sync.WaitGroup
dispatch:
	for _, slot := range pending {
		if fetchCtx.Err() != nil {
			break dispatch
		}
		select {
		case sem <- struct{}{}:
		case <-fetchCtx.Done():
			break dispatch
		}
		if fetchCtx.Err() != nil {
			<-sem
			break dispatch
		}
		wg.Add(1)
		go func(sl previewSlot) {
			defer func() {
				<-sem
				wg.Done()
			}()
			rawTmpl, terr := s.Collector.ReadTemplate(fetchCtx, *ds, sl.engineFingerprint, sl.databaseOrSchema)
			if terr != nil {
				if !errors.Is(terr, database.ErrTemplateUnavailable) && !errors.Is(terr, database.ErrStatementRequired) {
					cancelFetch()
				}
				return
			}
			if strings.TrimSpace(rawTmpl) != "" {
				p := truncatePreview(rawTmpl, 160)
				results[sl.idx] = &p
			}
		}(slot)
	}
	wg.Wait()
	for _, slot := range pending {
		if results[slot.idx] != nil {
			stmtRows[slot.idx].TemplatePreview = results[slot.idx]
		}
	}
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
		agg.absorbBucket(b)
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
