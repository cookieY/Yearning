// collector_config.go holds collector domain helpers and operations.
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"hash/fnv"
	"time"
)

// NewServices constructs the application services for the SQL performance diagnostics domain.
func NewServices(
	db *gorm.DB,
	crypter *crypto.Crypter,
	audit *auditlog.Recorder,
	reviewSvc *application.Services,
	reviewAgent *application.AgentRunner,
	orderSvc *orderapp.Services,
) (*Services, error) {
	if db == nil || crypter == nil {
		return nil, errors.New("diagnostics: nil db or crypter dependency")
	}
	return &Services{
		DB:          db,
		Crypter:     crypter,
		Audit:       audit,
		Review:      reviewSvc,
		ReviewAgent: reviewAgent,
		Orders:      orderSvc,
		Collector:   NewLiveStatsCollectorBackend(db, crypter),
		HolderID:    "diag-" + uuid.NewString()[:8],
	}, nil
}

// StaggerOffsetSeconds computes the deterministic stagger offset in seconds (fnv32a(datasource_id) % interval_seconds).
func StaggerOffsetSeconds(datasourceID string, intervalSeconds int) int {
	if intervalSeconds <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(datasourceID))
	return int(h.Sum32()) % intervalSeconds
}

// NewLiveStatsCollectorBackend constructs a live read-only database statistics collector.
func NewLiveStatsCollectorBackend(db *gorm.DB, crypter *crypto.Crypter) *LiveStatsCollectorBackend {
	return &LiveStatsCollectorBackend{
		DB:      db,
		Crypter: crypter,
		pools:   make(map[string]*sharedReadPoolEntry),
	}
}

func closeSharedReadPoolEntry(entry *sharedReadPoolEntry) {
	if entry == nil {
		return
	}
	if entry.pool != nil {
		_ = entry.pool.Close()
	}
	if entry.adapter != nil {
		_ = entry.adapter.Close()
	}
}

func (b *LiveStatsCollectorBackend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for k, entry := range b.pools {
		closeSharedReadPoolEntry(entry)
		delete(b.pools, k)
	}
}

func (b *LiveStatsCollectorBackend) acquireReadPool(ctx context.Context, ds DatasourceRow) (database.Adapter, *database.PurposePool, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, context.Canceled
	}
	if cached, ok := b.pools[ds.ID]; ok && cached != nil {
		b.mu.Unlock()
		return cached.adapter, cached.pool, nil
	}
	b.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	resCh := b.poolGroup.DoChan(ds.ID, func() (any, error) {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil, context.Canceled
		}
		if cached, ok := b.pools[ds.ID]; ok && cached != nil {
			b.mu.Unlock()
			return cached, nil
		}
		b.mu.Unlock()

		// Decouple dial lifetime from any single caller so one canceled waiter cannot abort coalesced callers.
		dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return b.openAndCacheReadPool(dialCtx, ds)
	})

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case res := <-resCh:
		if res.Err != nil {
			return nil, nil, res.Err
		}
		entry, ok := res.Val.(*sharedReadPoolEntry)
		if !ok || entry == nil {
			return nil, nil, database.ErrUnsupportedTarget
		}
		return entry.adapter, entry.pool, nil
	}
}

func (b *LiveStatsCollectorBackend) openAndCacheReadPool(ctx context.Context, ds DatasourceRow) (*sharedReadPoolEntry, error) {
	var entry *sharedReadPoolEntry
	if b.openPoolFn != nil {
		opened, err := b.openPoolFn(ctx, ds)
		if err != nil {
			return nil, err
		}
		entry = opened
	} else {
		cred, purpose, err := b.openReadOnlyCredential(ctx, ds.ID)
		if err != nil {
			return nil, err
		}
		if !database.ReadOnlyPurpose(purpose) {
			return nil, database.ErrReadOnlyViolation
		}
		mat, err := b.openTLSMaterial(ctx, ds.ID)
		if err != nil {
			return nil, err
		}
		dbName := ""
		if ds.DatabaseName != nil {
			dbName = *ds.DatabaseName
		}
		ep := database.Endpoint{
			Host:             ds.Host,
			Port:             ds.Port,
			DatabaseName:     dbName,
			CompatibilityMod: ds.CompatibilityMode,
		}
		opts := database.AdapterOptions{
			StatementTimeoutMS: 2000,
			MaxOpenConns:       2, // D5: diagnostics connections consume at most 2 active connections on the target database
		}
		var adapter database.Adapter
		switch ds.CompatibilityMode {
		case "mysql":
			adapter = database.NewMySQLAdapter(ep, opts)
		case "postgresql":
			adapter = database.NewPostgreSQLAdapter(ep, opts)
		default:
			return nil, database.ErrUnsupportedTarget
		}
		pool, err := adapter.OpenPurposePool(ctx, purpose, cred, mat)
		if err != nil {
			_ = adapter.Close()
			return nil, err
		}
		entry = &sharedReadPoolEntry{adapter: adapter, pool: pool}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		closeSharedReadPoolEntry(entry)
		return nil, context.Canceled
	}
	if b.pools == nil {
		b.pools = make(map[string]*sharedReadPoolEntry)
	}
	if existing, ok := b.pools[ds.ID]; ok && existing != nil {
		closeSharedReadPoolEntry(entry)
		return existing, nil
	}
	b.pools[ds.ID] = entry
	return entry, nil
}

// openReadOnlyCredential loads only query or review read-only credentials and never falls back to execution credentials (D2/P101).
func (b *LiveStatsCollectorBackend) openReadOnlyCredential(ctx context.Context, datasourceID string) (database.Credential, database.Purpose, error) {
	res, err := datasource.ResolveCredential(ctx, b.DB, b.Crypter, datasourceID, datasource.CredentialResolveOptions{
		Purposes:       []string{string(database.PurposeQuery), string(database.PurposeReview)},
		SkipUnreadable: true,
	})
	if err != nil {
		return database.Credential{}, "", domain.ErrInsufficientDBPrivilege("missing_read_only_credential")
	}
	purpose := database.Purpose(res.Purpose)
	if !database.ReadOnlyPurpose(purpose) {
		return database.Credential{}, "", domain.ErrInsufficientDBPrivilege("missing_read_only_credential")
	}
	return database.Credential{Username: res.Username, Password: res.Password}, purpose, nil
}

func (b *LiveStatsCollectorBackend) openTLSMaterial(ctx context.Context, datasourceID string) (*database.TLSMaterial, error) {
	return datasource.ResolveTLSMaterial(ctx, b.DB, b.Crypter, datasourceID)
}

func (b *LiveStatsCollectorBackend) Probe(ctx context.Context, ds DatasourceRow) (*database.StatsProbeResult, error) {
	adapter, pool, err := b.acquireReadPool(ctx, ds)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Code == domain.CodeDiagnosticsInsufficientDBPrivilege {
			return &database.StatsProbeResult{
				State:                database.StatsProbeInsufficientPriv,
				EngineFamily:         ds.CompatibilityMode,
				SanitizedFailureName: "missing_read_only_credential",
			}, nil
		}
		if errors.Is(err, database.ErrUnsupportedTarget) {
			return &database.StatsProbeResult{
				State:                database.StatsProbeUnsupportedEngine,
				EngineFamily:         ds.CompatibilityMode,
				SanitizedFailureName: "unsupported_engine",
			}, nil
		}
		return &database.StatsProbeResult{
			State:                database.StatsProbeCollectionFailed,
			EngineFamily:         ds.CompatibilityMode,
			SanitizedFailureName: "connection_failed",
		}, nil
	}
	return database.ProbeStatementStats(ctx, adapter, pool)
}

func (b *LiveStatsCollectorBackend) ReadSnapshot(ctx context.Context, ds DatasourceRow, topN int) ([]database.NativeStatementStatRow, error) {
	adapter, pool, err := b.acquireReadPool(ctx, ds)
	if err != nil {
		return nil, err
	}
	return database.ReadStatementStatsSnapshot(ctx, adapter, pool, topN)
}

func (b *LiveStatsCollectorBackend) ReadTemplate(ctx context.Context, ds DatasourceRow, engineFingerprint, databaseOrSchema string) (string, error) {
	adapter, pool, err := b.acquireReadPool(ctx, ds)
	if err != nil {
		return "", err
	}
	return database.ReadStatementTemplate(ctx, adapter, pool, engineFingerprint, databaseOrSchema)
}

func (b *LiveStatsCollectorBackend) ListColumns(ctx context.Context, ds DatasourceRow, schema, table string) ([]database.MetadataColumn, error) {
	adapter, pool, err := b.acquireReadPool(ctx, ds)
	if err != nil {
		return nil, err
	}
	return adapter.ListColumns(ctx, pool, schema, table)
}

func (b *LiveStatsCollectorBackend) ListIndexes(ctx context.Context, ds DatasourceRow, schema, table string) ([]database.MetadataIndex, error) {
	adapter, pool, err := b.acquireReadPool(ctx, ds)
	if err != nil {
		return nil, err
	}
	return adapter.ListIndexes(ctx, pool, schema, table)
}

// ---- Configuration & Datasource Listing ----

func (s *Services) loadDatasourceRow(ctx context.Context, datasourceID string) (*DatasourceRow, error) {
	var ds DatasourceRow
	err := s.DB.WithContext(ctx).Raw(`
		select id::text as id, name, engine, compatibility_mode, deployment_kind,
		       host, port, database_name, identity_fingerprint as server_identity_fingerprint, enabled
		from datasources where id = ?`, datasourceID).Scan(&ds).Error
	if err != nil || ds.ID == "" {
		return nil, domain.ErrNotFound()
	}
	return &ds, nil
}

func (s *Services) ensureDiagnosticsConfigRow(ctx context.Context, datasourceID string) (*persistence.DatasourceDiagnosticsConfigModel, error) {
	var cfg persistence.DatasourceDiagnosticsConfigModel
	err := s.DB.WithContext(ctx).Where("datasource_id = ?", datasourceID).First(&cfg).Error
	if err == nil {
		return &cfg, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	cfg = persistence.DatasourceDiagnosticsConfigModel{
		ID:                       uuid.NewString(),
		DatasourceID:             datasourceID,
		Enabled:                  false,
		IntervalSeconds:          domain.DefaultIntervalSeconds,
		EffectiveIntervalSeconds: domain.DefaultIntervalSeconds,
		TopN:                     domain.DefaultTopN,
		State:                    "disabled",
		LastProbeState:           "collection_failed",
		ConsecutiveFailures:      0,
		Version:                  1,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "datasource_id"}},
		DoNothing: true,
	}).Create(&cfg).Error; err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Where("datasource_id = ?", datasourceID).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func toConfigView(cfg *persistence.DatasourceDiagnosticsConfigModel) *domain.DatasourceDiagnosticsConfigView {
	return &domain.DatasourceDiagnosticsConfigView{
		DatasourceID:             cfg.DatasourceID,
		Enabled:                  cfg.Enabled,
		IntervalSeconds:          cfg.IntervalSeconds,
		EffectiveIntervalSeconds: cfg.EffectiveIntervalSeconds,
		TopN:                     cfg.TopN,
		State:                    cfg.State,
		LastProbeState:           normalizeProbeState(cfg.LastProbeState),
		LastProbeAt:              cfg.LastProbeAt,
		LastCollectedAt:          cfg.LastCollectedAt,
		ConsecutiveFailures:      cfg.ConsecutiveFailures,
		PausedReason:             cfg.PausedReason,
		Version:                  cfg.Version,
		UpdatedAt:                cfg.UpdatedAt,
	}
}

// GetConfig returns the SQL performance diagnostics configuration for a datasource (initializing a default row if none exists yet).
func (s *Services) GetConfig(ctx context.Context, datasourceID string) (*domain.DatasourceDiagnosticsConfigView, error) {
	if _, err := s.loadDatasourceRow(ctx, datasourceID); err != nil {
		return nil, err
	}
	cfg, err := s.ensureDiagnosticsConfigRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	return toConfigView(cfg), nil
}

func validIntervalSeconds(interval int) bool {
	switch interval {
	case 0, 60, 90, 120, 300:
		return true
	default:
		return false
	}
}

// UpdateConfig updates the SQL performance diagnostics configuration for a datasource and runs the 6-state statistics capability probe when enabled (P101).
func (s *Services) UpdateConfig(
	ctx context.Context,
	actor domain.Actor,
	datasourceID string,
	expectVersion int64,
	in domain.UpdateDiagnosticsConfigInput,
) (*domain.DatasourceDiagnosticsConfigView, error) {
	if !validIntervalSeconds(in.IntervalSeconds) {
		return nil, domain.ErrValidation("Collection interval must be 0 (manual only), 60, 90, 120, or 300 seconds")
	}
	if in.TopN < domain.MinTopN || in.TopN > domain.MaxTopN {
		return nil, domain.ErrValidation("Snapshot fingerprint limit top_n must be between 50 and 500")
	}
	ds, err := s.loadDatasourceRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.ensureDiagnosticsConfigRow(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	if expectVersion > 0 && cfg.Version != expectVersion {
		return nil, domain.ErrVersionConflict()
	}

	now := time.Now().UTC()
	newState := "disabled"
	probeState := normalizeProbeState(cfg.LastProbeState)
	var probeAt *time.Time = cfg.LastProbeAt
	consecutiveFailures := cfg.ConsecutiveFailures
	var pausedReason *string = cfg.PausedReason
	effectiveInterval := in.IntervalSeconds

	if in.Enabled {
		// Verify that the datasource has an enabled read-only credential (query or review).
		var roCount int64
		if err := s.DB.WithContext(ctx).Table("datasource_credentials").
			Where("datasource_id = ? and purpose in ('query', 'review') and enabled = true", datasourceID).
			Count(&roCount).Error; err != nil {
			return nil, err
		}
		if roCount == 0 {
			return nil, domain.ErrInsufficientDBPrivilege("missing_read_only_credential")
		}

		probeRes, probeErr := s.Collector.Probe(ctx, *ds)
		if probeErr != nil || probeRes == nil {
			probeState = string(database.StatsProbeCollectionFailed)
		} else {
			probeState = normalizeProbeState(string(probeRes.State))
		}
		probeAt = &now
		newState = "active"
		if in.ResumeFromPause || cfg.State == "paused_on_failure" || !cfg.Enabled {
			consecutiveFailures = 0
			pausedReason = nil
		}
	} else {
		consecutiveFailures = 0
		pausedReason = nil
	}

	updates := map[string]any{
		"enabled":                    in.Enabled,
		"interval_seconds":           in.IntervalSeconds,
		"effective_interval_seconds": effectiveInterval,
		"top_n":                      in.TopN,
		"state":                      newState,
		"last_probe_state":           probeState,
		"last_probe_at":              probeAt,
		"consecutive_failures":       consecutiveFailures,
		"paused_reason":              pausedReason,
		"version":                    gorm.Expr("version + 1"),
		"updated_at":                 now,
	}
	res := s.DB.WithContext(ctx).Model(&persistence.DatasourceDiagnosticsConfigModel{}).
		Where("id = ? and version = ?", cfg.ID, cfg.Version).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, domain.ErrVersionConflict()
	}
	if err := s.DB.WithContext(ctx).Where("id = ?", cfg.ID).First(cfg).Error; err != nil {
		return nil, err
	}
	if s.Audit != nil {
		_ = s.Audit.Audit(ctx, nil, auditlog.AuditInput{
			EventType:     "diagnostics.config_updated",
			ActorKind:     "user",
			ActorID:       &actor.UserID,
			ActorUsername: &actor.Username,
			ResourceType:  "datasource",
			ResourceID:    &datasourceID,
			Action:        "update_diagnostics_config",
			Outcome:       "succeeded",
			Metadata: map[string]any{
				"enabled":          cfg.Enabled,
				"interval_seconds": cfg.IntervalSeconds,
				"top_n":            cfg.TopN,
				"probe_state":      cfg.LastProbeState,
			},
		})
	}
	return toConfigView(cfg), nil
}
