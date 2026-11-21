// Package application implements configuration management, staggered single-concurrency
// collection, circuit breaking, monotonic delta bucket aggregation, on-demand template fetching,
// in-memory desensitized AI diagnosis, and optimization order draft bridging for the SQL performance diagnostics domain.
package application

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
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
		LastProbeState:           cfg.LastProbeState,
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
	probeState := cfg.LastProbeState
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
			probeState = string(probeRes.State)
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
		probeState := cfg.LastProbeState
		if !cfg.Enabled {
			probeState = "disabled"
		}
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
	return s.Crypter.Seal(crypto.PurposePayload, []byte(sqlTemplate), crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "sql_diagnostics_statements",
		PrimaryKey:   statementID,
		Column:       "template",
		Purpose:      "diagnostics_statement_template",
	})
}

func (s *Services) openStatementTemplate(stmt *persistence.SQLDiagnosticsStatementModel) (string, bool) {
	if stmt == nil || len(stmt.TemplateCiphertext) == 0 || stmt.TemplateKeyID == nil || stmt.TemplateAlgorithm == nil {
		return "", false
	}
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: stmt.TemplateCiphertext,
		Nonce:      stmt.TemplateNonce,
		KeyID:      *stmt.TemplateKeyID,
		Algorithm:  *stmt.TemplateAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "sql_diagnostics_statements",
		PrimaryKey:   stmt.ID,
		Column:       "template",
		Purpose:      "diagnostics_statement_template",
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
