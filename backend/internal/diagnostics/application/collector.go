// Package application implements configuration management, staggered single-concurrency
// collection, circuit breaking, monotonic delta bucket aggregation, on-demand template fetching,
// in-memory desensitized AI diagnosis, and optimization order draft bridging for the SQL performance diagnostics domain.
package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"sync"
	"time"
)

// DatasourceRow is a metadata projection of a diagnostics target read from the datasources table.
type DatasourceRow struct {
	ID                        string  `gorm:"column:id"`
	Name                      string  `gorm:"column:name"`
	Engine                    string  `gorm:"column:engine"`
	CompatibilityMode         string  `gorm:"column:compatibility_mode"`
	DeploymentKind            string  `gorm:"column:deployment_kind"`
	Host                      string  `gorm:"column:host"`
	Port                      int     `gorm:"column:port"`
	DatabaseName              *string `gorm:"column:database_name"`
	ServerIdentityFingerprint *string `gorm:"column:server_identity_fingerprint"`
	Enabled                   bool    `gorm:"column:enabled"`
}

// StatsCollectorBackend abstracts read-only native statistics probing, snapshots, on-demand templates, and single-table metadata reads on target databases.
type StatsCollectorBackend interface {
	Probe(ctx context.Context, ds DatasourceRow) (*database.StatsProbeResult, error)
	ReadSnapshot(ctx context.Context, ds DatasourceRow, topN int) ([]database.NativeStatementStatRow, error)
	ReadTemplate(ctx context.Context, ds DatasourceRow, engineFingerprint, databaseOrSchema string) (string, error)
	ListColumns(ctx context.Context, ds DatasourceRow, schema, table string) ([]database.MetadataColumn, error)
	ListIndexes(ctx context.Context, ds DatasourceRow, schema, table string) ([]database.MetadataIndex, error)
}

// PrecheckRunner abstracts the ephemeral AI precheck launcher (bridges to review.Services.StartEphemeralRun by default).
type PrecheckRunner func(ctx context.Context, actor domain.Actor, flowID, sqlText string, idempotencyKey string) (string, error)

// OrderSubmitter abstracts the formal change order submitter (bridges to changeorder.Services.SubmitOrderV2 by default).
type OrderSubmitter func(ctx context.Context, actor domain.Actor, flowID, schemaName, statementKind, sqlText, ephemeralRunID, title, remark, idempotencyKey string) (orderID string, displayNo string, state string, err error)

// Services aggregates the application services for the SQL performance diagnostics domain.
type Services struct {
	DB          *gorm.DB
	Crypter     *crypto.Crypter
	Audit       *auditlog.Recorder
	Review      *application.Services
	ReviewAgent *application.AgentRunner
	Orders      *orderapp.Services
	Collector   StatsCollectorBackend
	HolderID    string

	PrecheckRunner PrecheckRunner
	OrderSubmitter OrderSubmitter
}

// sharedReadPoolEntry caches the per-datasource shared read-only connection pool (MaxOpenConns=2, satisfying the D5 <= 2 active diagnostics connection budget).
type sharedReadPoolEntry struct {
	adapter database.Adapter
	pool    *database.PurposePool
}

// LiveStatsCollectorBackend reads native statistics using the datasource's configured query/review read-only credentials and a shared PurposePool.
type LiveStatsCollectorBackend struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter

	mu         sync.Mutex
	closed     bool
	pools      map[string]*sharedReadPoolEntry
	poolGroup  singleflight.Group
	openPoolFn func(ctx context.Context, ds DatasourceRow) (*sharedReadPoolEntry, error)
}

func (s *Services) persistSnapshotAndBuckets(
	ctx context.Context,
	ds *DatasourceRow,
	cfg *persistence.DatasourceDiagnosticsConfigModel,
	triggerKind string,
	probeState string,
	rows []database.NativeStatementStatRow,
	sampledAt time.Time,
	durationMS int,
) (*domain.DiagnosticsSnapshotSummaryView, error) {
	snapshotID := uuid.NewString()
	var anyReset bool
	var gapDetected bool

	intervalSec := cfg.IntervalSeconds
	if intervalSec <= 0 {
		intervalSec = domain.DefaultIntervalSeconds
	}
	bucketStart := sampledAt.Add(-time.Duration(intervalSec) * time.Second)
	if cfg.LastCollectedAt != nil {
		elapsed := sampledAt.Sub(*cfg.LastCollectedAt)
		if elapsed > 0 && elapsed <= 3*time.Duration(intervalSec)*time.Second {
			bucketStart = *cfg.LastCollectedAt
		} else if elapsed > 3*time.Duration(intervalSec)*time.Second {
			gapDetected = true
		}
	}

	rows = mergeSnapshotStatRows(rows)

	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existingStmts []persistence.SQLDiagnosticsStatementModel
		if err := tx.Where("datasource_id = ?", ds.ID).Find(&existingStmts).Error; err != nil {
			return err
		}
		stmtMap := make(map[string]persistence.SQLDiagnosticsStatementModel, len(existingStmts))
		for _, st := range existingStmts {
			stmtMap[st.EngineFingerprint] = st
		}

		newStmts := make([]persistence.SQLDiagnosticsStatementModel, 0)
		buckets := make([]persistence.SQLDiagnosticsBucketMetricModel, 0, len(rows))
		for _, r := range rows {
			prev, exists := stmtMap[r.EngineFingerprint]

			if !exists {
				stmtID := uuid.NewString()
				newStmts = append(newStmts, persistence.SQLDiagnosticsStatementModel{
					ID:                                      stmtID,
					DatasourceID:                            ds.ID,
					Engine:                                  ds.Engine,
					EngineFingerprint:                       r.EngineFingerprint,
					DatabaseOrSchema:                        r.DatabaseOrSchema,
					TemplateStatus:                          "not_fetched",
					LatestSnapshotID:                        &snapshotID,
					LatestCallsTotal:                        r.CallsTotal,
					LatestTotalExecTimeMS:                   r.TotalExecTimeMS,
					LatestRowsExaminedOrSharedBlksReadTotal: r.RowsExaminedOrSharedBlksReadTotal,
					LatestRowsSentOrRowsReturnedTotal:       r.RowsSentOrRowsReturnedTotal,
					LatestLockTimeMSOrTempBlksWrittenTotal:  r.LockTimeMSOrTempBlksWrittenTotal,
					LatestPlansTotal:                        r.PlansTotal,
					LatestTotalPlanTimeMS:                   r.TotalPlanTimeMS,
					LatestTempBlksReadTotal:                 r.TempBlksReadTotal,
					LatestSharedBlksHitTotal:                r.SharedBlksHitTotal,
					FirstSeenAt:                             sampledAt,
					LastSeenAt:                              sampledAt,
					Version:                                 1,
					CreatedAt:                               sampledAt,
					UpdatedAt:                               sampledAt,
				})
				avgMS := 0.0
				if r.CallsTotal > 0 {
					avgMS = r.TotalExecTimeMS / float64(r.CallsTotal)
				}
				buckets = append(buckets, persistence.SQLDiagnosticsBucketMetricModel{
					ID:                                uuid.NewString(),
					DatasourceID:                      ds.ID,
					StatementID:                       stmtID,
					SnapshotID:                        snapshotID,
					BucketStart:                       bucketStart,
					BucketEnd:                         sampledAt,
					CoverageMode:                      "cumulative_only",
					CallsTotal:                        r.CallsTotal,
					TotalExecTimeMS:                   r.TotalExecTimeMS,
					RowsExaminedOrSharedBlksReadTotal: r.RowsExaminedOrSharedBlksReadTotal,
					RowsSentOrRowsReturnedTotal:       r.RowsSentOrRowsReturnedTotal,
					LockTimeMSOrTempBlksWrittenTotal:  r.LockTimeMSOrTempBlksWrittenTotal,
					DeltaCalls:                        0,
					DeltaExecTimeMS:                   0,
					AvgExecTimeMS:                     avgMS,
					DeltaRowsExaminedOrSharedBlksRead: 0,
					DeltaRowsSentOrRowsReturned:       0,
					DeltaLockTimeMSOrTempBlksWritten:  0,
					ResetDetected:                     false,
					FirstObserved:                     true,
					CreatedAt:                         sampledAt,
				})
				continue
			}

			delta := computeBucketDelta(r, prev, gapDetected)
			if delta.reset {
				anyReset = true
			}

			if err := tx.Model(&persistence.SQLDiagnosticsStatementModel{}).
				Where("id = ?", prev.ID).
				Updates(map[string]any{
					"latest_snapshot_id":                             &snapshotID,
					"latest_calls_total":                             r.CallsTotal,
					"latest_total_exec_time_ms":                      r.TotalExecTimeMS,
					"latest_rows_examined_or_shared_blks_read_total": r.RowsExaminedOrSharedBlksReadTotal,
					"latest_rows_sent_or_rows_returned_total":        r.RowsSentOrRowsReturnedTotal,
					"latest_lock_time_ms_or_temp_blks_written_total": r.LockTimeMSOrTempBlksWrittenTotal,
					"latest_plans_total":                             r.PlansTotal,
					"latest_total_plan_time_ms":                      r.TotalPlanTimeMS,
					"latest_temp_blks_read_total":                    r.TempBlksReadTotal,
					"latest_shared_blks_hit_total":                   r.SharedBlksHitTotal,
					"last_seen_at":                                   sampledAt,
					"version":                                        gorm.Expr("version + 1"),
					"updated_at":                                     sampledAt,
				}).Error; err != nil {
				return err
			}

			buckets = append(buckets, persistence.SQLDiagnosticsBucketMetricModel{
				ID:                                uuid.NewString(),
				DatasourceID:                      ds.ID,
				StatementID:                       prev.ID,
				SnapshotID:                        snapshotID,
				BucketStart:                       bucketStart,
				BucketEnd:                         sampledAt,
				CoverageMode:                      delta.coverageMode,
				CallsTotal:                        r.CallsTotal,
				TotalExecTimeMS:                   r.TotalExecTimeMS,
				RowsExaminedOrSharedBlksReadTotal: r.RowsExaminedOrSharedBlksReadTotal,
				RowsSentOrRowsReturnedTotal:       r.RowsSentOrRowsReturnedTotal,
				LockTimeMSOrTempBlksWrittenTotal:  r.LockTimeMSOrTempBlksWrittenTotal,
				DeltaCalls:                        delta.calls,
				DeltaExecTimeMS:                   delta.execMS,
				AvgExecTimeMS:                     delta.avgMS,
				DeltaRowsExaminedOrSharedBlksRead: delta.rowsExamined,
				DeltaRowsSentOrRowsReturned:       delta.rowsSent,
				DeltaLockTimeMSOrTempBlksWritten:  delta.lockOrTemp,
				DeltaPlans:                        delta.plans,
				DeltaPlanTimeMS:                   delta.planMS,
				DeltaTempBlksRead:                 delta.tempRead,
				DeltaSharedBlksHit:                delta.sharedHit,
				ResetDetected:                     delta.reset,
				FirstObserved:                     false,
				CreatedAt:                         sampledAt,
			})
		}

		if len(newStmts) > 0 {
			if err := tx.Create(&newStmts).Error; err != nil {
				return err
			}
		}

		snap := persistence.SQLDiagnosticsSnapshotModel{
			ID:             snapshotID,
			DatasourceID:   ds.ID,
			Engine:         ds.Engine,
			TriggerKind:    triggerKind,
			ProbeState:     probeState,
			TopN:           cfg.TopN,
			StatementCount: len(rows),
			DurationMS:     durationMS,
			ResetDetected:  anyReset,
			GapDetected:    gapDetected,
			SampledAt:      sampledAt,
			CreatedAt:      sampledAt,
		}
		if err := tx.Create(&snap).Error; err != nil {
			return err
		}
		if len(buckets) > 0 {
			if err := tx.Create(&buckets).Error; err != nil {
				return err
			}
		}

		cfgUpdates := map[string]any{
			"state":                      "active",
			"last_probe_state":           probeState,
			"last_probe_at":              sampledAt,
			"last_collected_at":          sampledAt,
			"effective_interval_seconds": cfg.IntervalSeconds,
			"consecutive_failures":       0,
			"paused_reason":              nil,
			"version":                    gorm.Expr("version + 1"),
			"updated_at":                 sampledAt,
		}
		if triggerKind == "manual" {
			cfgUpdates["last_manual_snapshot_at"] = sampledAt
		}
		return tx.Model(&persistence.DatasourceDiagnosticsConfigModel{}).
			Where("id = ?", cfg.ID).
			Updates(cfgUpdates).Error
	})
	if err != nil {
		return nil, err
	}

	return &domain.DiagnosticsSnapshotSummaryView{
		SnapshotID:     snapshotID,
		DatasourceID:   ds.ID,
		TriggerKind:    triggerKind,
		ProbeState:     probeState,
		StatementCount: len(rows),
		DurationMS:     durationMS,
		ResetDetected:  anyReset,
		GapDetected:    gapDetected,
		SampledAt:      sampledAt,
	}, nil
}

type bucketDeltaResult struct {
	coverageMode string
	calls        int64
	execMS       float64
	avgMS        float64
	rowsExamined int64
	rowsSent     int64
	lockOrTemp   float64
	plans        *int64
	planMS       *float64
	tempRead     *int64
	sharedHit    *int64
	reset        bool
}
