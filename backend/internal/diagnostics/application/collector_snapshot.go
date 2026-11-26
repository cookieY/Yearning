// collector_snapshot.go holds collector domain helpers and operations.
package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// ListDatasources returns the datasource switcher list and collection coverage states for the admin SQL diagnostics page.
func (s *Services) ListDatasources(ctx context.Context) ([]domain.DiagnosticDatasourceSummaryView, error) {
	var dsRows []DatasourceRow
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text as id, name, engine, compatibility_mode, deployment_kind,
		       host, port, database_name, identity_fingerprint as server_identity_fingerprint, enabled
		from datasources
		order by created_at asc, id asc`).Scan(&dsRows).Error; err != nil {
		return nil, err
	}
	var cfgRows []persistence.DatasourceDiagnosticsConfigModel
	if err := s.DB.WithContext(ctx).Find(&cfgRows).Error; err != nil {
		return nil, err
	}
	cfgByDS := make(map[string]persistence.DatasourceDiagnosticsConfigModel, len(cfgRows))
	for _, c := range cfgRows {
		cfgByDS[c.DatasourceID] = c
	}

	items := make([]domain.DiagnosticDatasourceSummaryView, 0, len(dsRows))
	for _, ds := range dsRows {
		cfg, ok := cfgByDS[ds.ID]
		if !ok {
			cfg = persistence.DatasourceDiagnosticsConfigModel{
				DatasourceID:             ds.ID,
				Enabled:                  false,
				IntervalSeconds:          domain.DefaultIntervalSeconds,
				EffectiveIntervalSeconds: domain.DefaultIntervalSeconds,
				TopN:                     domain.DefaultTopN,
				State:                    "disabled",
				LastProbeState:           "collection_failed",
			}
		}
		probeState := normalizeProbeState(cfg.LastProbeState)
		env := "default"
		if ds.DeploymentKind != "" {
			env = ds.DeploymentKind
		}
		items = append(items, domain.DiagnosticDatasourceSummaryView{
			ID:                       ds.ID,
			Name:                     ds.Name,
			Environment:              env,
			Engine:                   ds.Engine,
			CompatibilityMode:        ds.CompatibilityMode,
			Host:                     ds.Host,
			Port:                     ds.Port,
			DatabaseName:             ds.DatabaseName,
			DiagnosticsEnabled:       cfg.Enabled,
			State:                    cfg.State,
			ProbeState:               probeState,
			IntervalSeconds:          cfg.IntervalSeconds,
			EffectiveIntervalSeconds: cfg.EffectiveIntervalSeconds,
			TopN:                     cfg.TopN,
			LastCollectedAt:          cfg.LastCollectedAt,
		})
	}
	return items, nil
}

// ---- Physical Instance Lease, Manual Snapshot Rate Limiting, Circuit Breaker & Monotonic Delta Collection ----

func instanceFingerprintKey(ds *DatasourceRow) string {
	if ds.ServerIdentityFingerprint != nil && *ds.ServerIdentityFingerprint != "" {
		return *ds.ServerIdentityFingerprint
	}
	return fmt.Sprintf("endpoint:%s:%s:%d", ds.CompatibilityMode, ds.Host, ds.Port)
}

// AcquireInstanceLease acquires the physical-instance single-concurrency collection lease (at most 1 active collector per physical instance during the lease TTL).
func (s *Services) AcquireInstanceLease(ctx context.Context, ds *DatasourceRow, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	fpKey := instanceFingerprintKey(ds)
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	var acquired bool
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lease persistence.DiagnosticsInstanceLeaseModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("server_identity_fingerprint = ?", fpKey).
			First(&lease).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			lease = persistence.DiagnosticsInstanceLeaseModel{
				ID:                        uuid.NewString(),
				ServerIdentityFingerprint: fpKey,
				DatasourceID:              ds.ID,
				HolderID:                  s.HolderID,
				LeaseExpiresAt:            expiresAt,
				Version:                   1,
				CreatedAt:                 now,
				UpdatedAt:                 now,
			}
			if err := tx.Create(&lease).Error; err != nil {
				return err
			}
			acquired = true
			return nil
		}
		if err != nil {
			return err
		}
		if lease.LeaseExpiresAt.After(now) && lease.HolderID != s.HolderID {
			acquired = false
			return nil
		}
		if err := tx.Model(&persistence.DiagnosticsInstanceLeaseModel{}).
			Where("id = ?", lease.ID).
			Updates(map[string]any{
				"datasource_id":    ds.ID,
				"holder_id":        s.HolderID,
				"lease_expires_at": expiresAt,
				"version":          gorm.Expr("version + 1"),
				"updated_at":       now,
			}).Error; err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return acquired, err
}

// ReleaseInstanceLease releases the physical instance collection lease and records the completion timestamp.
func (s *Services) ReleaseInstanceLease(ctx context.Context, ds *DatasourceRow, collectedAt time.Time) {
	fpKey := instanceFingerprintKey(ds)
	now := time.Now().UTC()
	_ = s.DB.WithContext(ctx).Model(&persistence.DiagnosticsInstanceLeaseModel{}).
		Where("server_identity_fingerprint = ? and holder_id = ?", fpKey, s.HolderID).
		Updates(map[string]any{
			"lease_expires_at":  now,
			"last_collected_at": collectedAt,
			"version":           gorm.Expr("version + 1"),
			"updated_at":        now,
		}).Error
}

// TriggerManualSnapshot executes a controlled manual snapshot (30s/datasource rate limit + circuit breaker guard + monotonic delta persistence).
func (s *Services) TriggerManualSnapshot(ctx context.Context, actor domain.Actor, datasourceID string) (*domain.DiagnosticsSnapshotSummaryView, error) {
	return s.collectSnapshotInternal(ctx, &actor, datasourceID, "manual", time.Now().UTC())
}

// CollectScheduledSnapshot executes a scheduled snapshot at the specified sample timestamp for the background scheduler or tests.
func (s *Services) CollectScheduledSnapshot(ctx context.Context, datasourceID string, sampledAt time.Time) (*domain.DiagnosticsSnapshotSummaryView, error) {
	return s.collectSnapshotInternal(ctx, nil, datasourceID, "scheduled", sampledAt.UTC())
}

// RunCollectionLoop continuously runs scheduled snapshots for enabled datasources and prunes expired diagnostics data.
func (s *Services) RunCollectionLoop(ctx context.Context) {
	s.runScheduledCollectionPass(ctx, time.Now().UTC())

	collectTicker := time.NewTicker(15 * time.Second)
	defer collectTicker.Stop()
	pruneTicker := time.NewTicker(1 * time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-collectTicker.C:
			s.runScheduledCollectionPass(ctx, now.UTC())
		case now := <-pruneTicker.C:
			_ = s.PruneExpiredDiagnosticsData(ctx, now.UTC())
		}
	}
}

func (s *Services) runScheduledCollectionPass(ctx context.Context, now time.Time) {
	if s == nil || s.DB == nil {
		return
	}
	var configs []persistence.DatasourceDiagnosticsConfigModel
	if err := s.DB.WithContext(ctx).
		Where("enabled = true and state != 'paused_on_failure'").
		Find(&configs).Error; err != nil {
		return
	}
	for _, cfg := range configs {
		if ctx.Err() != nil {
			return
		}
		intervalSec := cfg.EffectiveIntervalSeconds
		if intervalSec <= 0 {
			intervalSec = cfg.IntervalSeconds
		}
		if intervalSec <= 0 {
			continue
		}
		if cfg.LastCollectedAt != nil && now.Sub(*cfg.LastCollectedAt) < time.Duration(intervalSec)*time.Second {
			continue
		}
		_, _ = s.CollectScheduledSnapshot(ctx, cfg.DatasourceID, now)
	}
}

func (s *Services) collectSnapshotInternal(
	ctx context.Context,
	actor *domain.Actor,
	datasourceID string,
	triggerKind string,
	sampledAt time.Time,
) (*domain.DiagnosticsSnapshotSummaryView, error) {
	ds, err := s.loadDatasourceRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.ensureDiagnosticsConfigRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, domain.ErrDatasourceDisabled()
	}
	if cfg.State == "paused_on_failure" {
		reason := ""
		if cfg.PausedReason != nil {
			reason = *cfg.PausedReason
		}
		return nil, domain.ErrCircuitPaused(reason)
	}
	if triggerKind == "manual" && cfg.LastManualSnapshotAt != nil {
		elapsed := sampledAt.Sub(*cfg.LastManualSnapshotAt)
		if elapsed < domain.ManualSnapshotCooldown {
			remain := int((domain.ManualSnapshotCooldown - elapsed + 999*time.Millisecond) / time.Second)
			if remain < 1 {
				remain = 1
			}
			return nil, domain.ErrSnapshotRateLimited(remain)
		}
	}

	acquired, err := s.AcquireInstanceLease(ctx, ds, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, domain.ErrSnapshotRateLimited(5)
	}
	defer s.ReleaseInstanceLease(ctx, ds, sampledAt)

	start := time.Now()
	probeRes, probeErr := s.Collector.Probe(ctx, *ds)
	if probeErr != nil || probeRes == nil {
		return nil, s.recordCollectionFailure(ctx, cfg, ds, triggerKind, sampledAt, int(time.Since(start).Milliseconds()), "probe_failed")
	}
	switch probeRes.State {
	case database.StatsProbeDisabled:
		_ = s.updateProbeState(ctx, cfg, string(probeRes.State), sampledAt)
		return nil, domain.ErrStatsNotEnabled(probeRes.SanitizedFailureName)
	case database.StatsProbeInsufficientPriv:
		_ = s.updateProbeState(ctx, cfg, string(probeRes.State), sampledAt)
		return nil, domain.ErrInsufficientDBPrivilege(probeRes.SanitizedFailureName)
	case database.StatsProbeUnsupportedEngine:
		_ = s.updateProbeState(ctx, cfg, string(probeRes.State), sampledAt)
		return nil, domain.ErrEngineUnsupported(probeRes.SanitizedFailureName)
	case database.StatsProbeCollectionFailed:
		return nil, s.recordCollectionFailure(ctx, cfg, ds, triggerKind, sampledAt, int(time.Since(start).Milliseconds()), probeRes.SanitizedFailureName)
	}

	var rows []database.NativeStatementStatRow
	if probeRes.State == database.StatsProbeReady {
		rows, err = s.Collector.ReadSnapshot(ctx, *ds, cfg.TopN)
		if err != nil {
			return nil, s.recordCollectionFailure(ctx, cfg, ds, triggerKind, sampledAt, int(time.Since(start).Milliseconds()), "snapshot_read_failed")
		}
	}
	durationMS := int(time.Since(start).Milliseconds())

	summary, err := s.persistSnapshotAndBuckets(ctx, ds, cfg, triggerKind, string(probeRes.State), rows, sampledAt, durationMS)
	if err != nil {
		return nil, err
	}
	if actor != nil && s.Audit != nil {
		_ = s.Audit.Audit(ctx, nil, auditlog.AuditInput{
			EventType:     "diagnostics.manual_snapshot_triggered",
			ActorKind:     "user",
			ActorID:       &actor.UserID,
			ActorUsername: &actor.Username,
			ResourceType:  "datasource",
			ResourceID:    &datasourceID,
			Action:        "trigger_manual_diagnostics_snapshot",
			Outcome:       "succeeded",
			Metadata: map[string]any{
				"snapshot_id":     summary.SnapshotID,
				"statement_count": summary.StatementCount,
				"duration_ms":     summary.DurationMS,
			},
		})
	}
	return summary, nil
}

func (s *Services) updateProbeState(ctx context.Context, cfg *persistence.DatasourceDiagnosticsConfigModel, probeState string, now time.Time) error {
	return s.DB.WithContext(ctx).Model(&persistence.DatasourceDiagnosticsConfigModel{}).
		Where("id = ?", cfg.ID).
		Updates(map[string]any{
			"last_probe_state": probeState,
			"last_probe_at":    now,
			"updated_at":       now,
		}).Error
}

// recordCollectionFailure implements the D3 backoff and circuit-breaker rule: 2 consecutive timeouts/failures automatically degrade frequency by 2x (degraded), and 5 consecutive failures automatically pause collection (paused_on_failure).
func (s *Services) recordCollectionFailure(
	ctx context.Context,
	cfg *persistence.DatasourceDiagnosticsConfigModel,
	ds *DatasourceRow,
	triggerKind string,
	sampledAt time.Time,
	durationMS int,
	failureReason string,
) error {
	if failureReason == "" {
		failureReason = "collection_timeout_or_error"
	}
	newFailures := cfg.ConsecutiveFailures + 1
	newState := cfg.State
	effectiveInterval := cfg.EffectiveIntervalSeconds
	var pausedReason *string

	if newFailures >= domain.PauseFailureThreshold {
		newState = "paused_on_failure"
		effectiveInterval = 0
		pausedReason = &failureReason
	} else if newFailures >= domain.DegradeFailureThreshold {
		newState = "degraded"
		if cfg.IntervalSeconds > 0 {
			effectiveInterval = cfg.IntervalSeconds * 2
			if effectiveInterval > 600 {
				effectiveInterval = 600
			}
		}
	}

	_ = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snap := persistence.SQLDiagnosticsSnapshotModel{
			ID:             uuid.NewString(),
			DatasourceID:   ds.ID,
			Engine:         ds.Engine,
			TriggerKind:    triggerKind,
			ProbeState:     string(database.StatsProbeCollectionFailed),
			TopN:           cfg.TopN,
			StatementCount: 0,
			DurationMS:     durationMS,
			FailureReason:  &failureReason,
			SampledAt:      sampledAt,
			CreatedAt:      sampledAt,
		}
		_ = tx.Create(&snap).Error
		return tx.Model(&persistence.DatasourceDiagnosticsConfigModel{}).
			Where("id = ?", cfg.ID).
			Updates(map[string]any{
				"state":                      newState,
				"last_probe_state":           string(database.StatsProbeCollectionFailed),
				"last_probe_at":              sampledAt,
				"effective_interval_seconds": effectiveInterval,
				"consecutive_failures":       newFailures,
				"paused_reason":              pausedReason,
				"version":                    gorm.Expr("version + 1"),
				"updated_at":                 sampledAt,
			}).Error
	})

	if newState == "paused_on_failure" {
		return domain.ErrCircuitPaused(failureReason)
	}
	return domain.ErrStatsNotEnabled(failureReason)
}

// PruneExpiredDiagnosticsData enforces the D4 retention lifecycle: fine-grained buckets are retained for 14 days, and templates/AI reports are retained for 30 days.
func (s *Services) PruneExpiredDiagnosticsData(ctx context.Context, now time.Time) error {
	bucketCutoff := now.UTC().Add(-domain.BucketRetention)
	artifactCutoff := now.UTC().Add(-domain.ArtifactRetention)
	if err := s.DB.WithContext(ctx).
		Where("bucket_end < ?", bucketCutoff).
		Delete(&persistence.SQLDiagnosticsBucketMetricModel{}).Error; err != nil {
		return err
	}
	if err := s.DB.WithContext(ctx).
		Where("sampled_at < ?", bucketCutoff).
		Delete(&persistence.SQLDiagnosticsSnapshotModel{}).Error; err != nil {
		return err
	}
	if err := s.DB.WithContext(ctx).
		Where("expires_at <= ?", now.UTC()).
		Delete(&persistence.SQLDiagnosticsAIReportModel{}).Error; err != nil {
		return err
	}
	if err := s.DB.WithContext(ctx).
		Where("last_seen_at < ?", artifactCutoff).
		Delete(&persistence.SQLDiagnosticsStatementModel{}).Error; err != nil {
		return err
	}
	return nil
}

// ---- AES-256-GCM Seal / Open Helpers ----

func (s *Services) sealStatementTemplate(statementID, sqlTemplate string) (crypto.Envelope, error) {
	return s.Crypter.SealColumn(crypto.PurposeDiagnosticsStatementTemplate, crypto.ColumnRef{
		Table:      "sql_diagnostics_statements",
		PrimaryKey: statementID,
		Column:     "template",
	}, []byte(sqlTemplate))
}

func (s *Services) openStatementTemplate(stmt *persistence.SQLDiagnosticsStatementModel) (string, bool) {
	if stmt == nil || len(stmt.TemplateCiphertext) == 0 || stmt.TemplateKeyID == nil || stmt.TemplateAlgorithm == nil {
		return "", false
	}
	plain, err := s.Crypter.OpenColumn(crypto.PurposeDiagnosticsStatementTemplate, crypto.ColumnRef{
		Table:      "sql_diagnostics_statements",
		PrimaryKey: stmt.ID,
		Column:     "template",
	}, crypto.Envelope{
		Ciphertext: stmt.TemplateCiphertext,
		Nonce:      stmt.TemplateNonce,
		KeyID:      *stmt.TemplateKeyID,
		Algorithm:  *stmt.TemplateAlgorithm,
	})
	if err != nil {
		return "", false
	}
	return string(plain), true
}

func (s *Services) computeYearningFingerprint(engine, compatibilityMode, templateSQL string) *string {
	trimmed := strings.TrimSpace(templateSQL)
	if trimmed == "" || s.Crypter == nil {
		return nil
	}
	eng, err := fingerprint.NewEngine(fingerprint.CryptoSigner{C: s.Crypter}, fingerprint.Options{})
	if err != nil {
		return nil
	}
	res, err := eng.Analyze(fingerprint.Profile{
		Engine: engine,
		Mode:   compatibilityMode,
	}, []byte(trimmed), nil)
	if err != nil || res.CanonicalFingerprintHMAC == "" {
		return nil
	}
	h := res.CanonicalFingerprintHMAC
	return &h
}

func addOptionalMetric[T int64 | float64](dst **T, src *T) {
	if src == nil {
		return
	}
	v := *src
	if *dst != nil {
		v += **dst
	}
	*dst = &v
}

// mergeSnapshotStatRows merges duplicate EngineFingerprint rows across schemas within one snapshot
// so entries align with unique(datasource_id, engine_fingerprint) on sql_diagnostics_statements.
func mergeSnapshotStatRows(rows []database.NativeStatementStatRow) []database.NativeStatementStatRow {
	merged := make([]database.NativeStatementStatRow, 0, len(rows))
	idxByFP := make(map[string]int, len(rows))
	for _, r := range rows {
		if r.EngineFingerprint == "" {
			continue
		}
		if idx, exists := idxByFP[r.EngineFingerprint]; exists {
			m := &merged[idx]
			if m.DatabaseOrSchema == "" && r.DatabaseOrSchema != "" {
				m.DatabaseOrSchema = r.DatabaseOrSchema
			}
			m.CallsTotal += r.CallsTotal
			m.TotalExecTimeMS += r.TotalExecTimeMS
			m.RowsExaminedOrSharedBlksReadTotal += r.RowsExaminedOrSharedBlksReadTotal
			m.RowsSentOrRowsReturnedTotal += r.RowsSentOrRowsReturnedTotal
			m.LockTimeMSOrTempBlksWrittenTotal += r.LockTimeMSOrTempBlksWrittenTotal
			addOptionalMetric(&m.PlansTotal, r.PlansTotal)
			addOptionalMetric(&m.TotalPlanTimeMS, r.TotalPlanTimeMS)
			addOptionalMetric(&m.TempBlksReadTotal, r.TempBlksReadTotal)
			addOptionalMetric(&m.SharedBlksHitTotal, r.SharedBlksHitTotal)
			continue
		}
		idxByFP[r.EngineFingerprint] = len(merged)
		merged = append(merged, r)
	}
	return merged
}

// computeBucketDelta calculates monotonic non-negative counter deltas and detects counter resets.
func computeBucketDelta(
	r database.NativeStatementStatRow,
	prev persistence.SQLDiagnosticsStatementModel,
	gapDetected bool,
) bucketDeltaResult {
	reset := r.CallsTotal < prev.LatestCallsTotal || r.TotalExecTimeMS+0.001 < prev.LatestTotalExecTimeMS
	res := bucketDeltaResult{
		coverageMode: "window_delta",
		reset:        reset,
	}
	if reset {
		res.coverageMode = "reset_or_gap"
		res.calls = maxInt64(0, r.CallsTotal)
		res.execMS = maxFloat64(0, r.TotalExecTimeMS)
		res.rowsExamined = maxInt64(0, r.RowsExaminedOrSharedBlksReadTotal)
		res.rowsSent = maxInt64(0, r.RowsSentOrRowsReturnedTotal)
		res.lockOrTemp = maxFloat64(0, r.LockTimeMSOrTempBlksWrittenTotal)
	} else {
		if gapDetected {
			res.coverageMode = "reset_or_gap"
		}
		res.calls = maxInt64(0, r.CallsTotal-prev.LatestCallsTotal)
		res.execMS = maxFloat64(0, r.TotalExecTimeMS-prev.LatestTotalExecTimeMS)
		res.rowsExamined = maxInt64(0, r.RowsExaminedOrSharedBlksReadTotal-prev.LatestRowsExaminedOrSharedBlksReadTotal)
		res.rowsSent = maxInt64(0, r.RowsSentOrRowsReturnedTotal-prev.LatestRowsSentOrRowsReturnedTotal)
		res.lockOrTemp = maxFloat64(0, r.LockTimeMSOrTempBlksWrittenTotal-prev.LatestLockTimeMSOrTempBlksWrittenTotal)
		res.plans = diffOptionalMetric(r.PlansTotal, prev.LatestPlansTotal, maxInt64)
		res.planMS = diffOptionalMetric(r.TotalPlanTimeMS, prev.LatestTotalPlanTimeMS, maxFloat64)
		res.tempRead = diffOptionalMetric(r.TempBlksReadTotal, prev.LatestTempBlksReadTotal, maxInt64)
		res.sharedHit = diffOptionalMetric(r.SharedBlksHitTotal, prev.LatestSharedBlksHitTotal, maxInt64)
	}
	if res.calls > 0 {
		res.avgMS = res.execMS / float64(res.calls)
	}
	return res
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func diffOptionalMetric[T int64 | float64](cur, prev *T, maxFn func(T, T) T) *T {
	if cur == nil {
		return nil
	}
	var zero T
	if prev == nil || *cur < *prev {
		v := maxFn(zero, *cur)
		return &v
	}
	v := *cur - *prev
	return &v
}
