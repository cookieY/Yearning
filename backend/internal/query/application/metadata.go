// metadata.go handles Session metadata reads (schemas, tables, columns; masked flag evaluated live against the vocabulary).
// Uses an in-process short-TTL cache with singleflight coalescing keyed by datasource version, identity fingerprint,
// and active read-only credential metadata, always checking session/grant authorization before reading the cache.
package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
)

const (
	defaultMetadataCacheTTL        = 60 * time.Second
	defaultMetadataCacheMaxEntries = 4096
)

// SchemaView conforms to the OpenAPI DatasourceSchema schema.
type SchemaView struct {
	Name string `json:"name"`
}

// TableView conforms to the OpenAPI DatasourceTable schema.
type TableView struct {
	SchemaName   string `json:"schema_name"`
	TableName    string `json:"table_name"`
	RelationKind string `json:"relation_kind"`
}

// ColumnView conforms to the OpenAPI DatasourceColumn schema.
type ColumnView struct {
	SchemaName string `json:"schema_name"`
	TableName  string `json:"table_name"`
	ColumnName string `json:"column_name"`
	Ordinal    int    `json:"ordinal"`
	DataType   string `json:"data_type"`
	Nullable   bool   `json:"nullable"`
	Masked     bool   `json:"masked"`
	// §18.51 table structure enrichment (aligned with SHOW FULL FIELDS); empty strings indicate "none"
	// and are hidden by the frontend. All fields are optional (omitted in older responses) and represent read-only catalog metadata.
	ColumnKey    string `json:"column_key,omitempty"`
	ColumnDeflt  string `json:"column_default,omitempty"`
	ColumnExtra  string `json:"extra,omitempty"`
	ColumnRemark string `json:"comment,omitempty"`
}

type metadataCacheEntry struct {
	value     any
	expiresAt time.Time
}

type metadataCache struct {
	mu         sync.RWMutex
	entries    map[string]metadataCacheEntry
	maxEntries int
	flight     singleflight.Group
}

func newMetadataCache(maxEntries int) *metadataCache {
	if maxEntries <= 0 {
		maxEntries = defaultMetadataCacheMaxEntries
	}
	return &metadataCache{
		entries:    make(map[string]metadataCacheEntry),
		maxEntries: maxEntries,
	}
}

func (c *metadataCache) get(key string, now time.Time) (any, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		c.mu.Lock()
		if cur, exists := c.entries[key]; exists && !now.Before(cur.expiresAt) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}
	return entry.value, true
}

func (c *metadataCache) set(key string, value any, expiresAt time.Time, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxEntries {
		for k, entry := range c.entries {
			if !now.Before(entry.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.maxEntries {
			var oldestKey string
			var oldestExpiry time.Time
			first := true
			for k, entry := range c.entries {
				if first || entry.expiresAt.Before(oldestExpiry) {
					oldestKey = k
					oldestExpiry = entry.expiresAt
					first = false
				}
			}
			if !first {
				delete(c.entries, oldestKey)
			}
		}
	}
	c.entries[key] = metadataCacheEntry{value: value, expiresAt: expiresAt}
}

func (c *metadataCache) clear() {
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

func (s *Services) getMetadataCache() (*metadataCache, time.Duration) {
	s.metaCacheMu.Lock()
	defer s.metaCacheMu.Unlock()
	if s.metaCache == nil {
		s.metaCache = newMetadataCache(defaultMetadataCacheMaxEntries)
	}
	ttl := s.metaCacheTTL
	return s.metaCache, ttl
}

type metadataTargetScope struct {
	ds      *datasource.DatasourceModel
	credKey string
	flowID  string
}

func (s *Services) resolveCredentialCacheKey(ctx context.Context, dsID string) (string, error) {
	if s.PoolFactory != nil {
		return "poolfactory", nil
	}
	cred, resolvedPurpose, err := s.loadReadOnlyQueryCredential(ctx, dsID)
	if err != nil {
		return "", datasourceUnavailable()
	}
	return fmt.Sprintf("%s:%s:%d", resolvedPurpose, cred.ID, cred.UpdatedAt.UnixNano()), nil
}

// resolveSessionMetadataScope validates the Session, actor ownership, live capability, datasource row,
// and read-only credential presence BEFORE consulting the metadata cache so revoked sessions, wrong actors,
// disabled datasources, or credential rotations can never be bypassed by cached entries.
func (s *Services) resolveSessionMetadataScope(
	ctx context.Context,
	actor Actor,
	sessionID, dsID string,
) (*metadataTargetScope, error) {
	session, err := s.ensureSessionUsable(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.UserIDSnapshot != actor.UserID {
		return nil, domain.ErrNotFound()
	}
	cap, err := s.refreshCapability(ctx, sessionID, dsID)
	if err != nil {
		return nil, err
	}
	switch domain.CapabilityState(cap.State) {
	case domain.CapIdentity:
		return nil, domain.ErrDsNotGranted()
	case domain.CapUnavailable:
		return nil, datasourceUnavailable()
	}
	ds, err := s.loadDatasourceRow(ctx, dsID)
	if err != nil {
		return nil, err
	}
	if !ds.Enabled {
		return nil, datasourceUnavailable()
	}
	credKey, err := s.resolveCredentialCacheKey(ctx, ds.ID)
	if err != nil {
		return nil, err
	}
	return &metadataTargetScope{
		ds:      ds,
		credKey: credKey,
		flowID:  session.FlowIDSnapshot,
	}, nil
}

// resolveFlowMetadataScope validates datasource availability and read-only credential presence
// for submitter-side metadata projection before checking the cache.
func (s *Services) resolveFlowMetadataScope(ctx context.Context, dsID string) (*metadataTargetScope, error) {
	ds, err := s.loadDatasourceRow(ctx, dsID)
	if err != nil {
		return nil, err
	}
	if !ds.Enabled {
		// Align with the query session domain: disabled datasources always surface as unavailable (Review F8).
		return nil, datasourceUnavailable()
	}
	credKey, err := s.resolveCredentialCacheKey(ctx, ds.ID)
	if err != nil {
		return nil, err
	}
	return &metadataTargetScope{
		ds:      ds,
		credKey: credKey,
	}, nil
}

func metadataCacheKey(scope *metadataTargetScope, kind, schema, table string) string {
	fp := ""
	if scope.ds.IdentityFingerprint != nil {
		fp = *scope.ds.IdentityFingerprint
	}
	return fmt.Sprintf(
		"%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		kind, scope.ds.ID, scope.ds.Version, fp, scope.credKey, schema, table,
	)
}

func (s *Services) fetchMetadataCached(
	ctx context.Context,
	scope *metadataTargetScope,
	kind, schema, table string,
	loader func(fetchCtx context.Context, adapter database.Adapter, pool *database.PurposePool) (any, error),
) (any, error) {
	cache, ttl := s.getMetadataCache()
	key := metadataCacheKey(scope, kind, schema, table)
	if ttl > 0 {
		if hit, ok := cache.get(key, s.now()); ok {
			return hit, nil
		}
	}

	timeoutMS := 30_000
	if s.DB != nil {
		if configured := s.loadQuerySettings(ctx, s.DB).DefaultTimeoutMS; configured > 0 {
			timeoutMS = configured
		}
	}

	ch := cache.flight.DoChan(key, func() (any, error) {
		if ttl > 0 {
			if hit, ok := cache.get(key, s.now()); ok {
				return hit, nil
			}
		}
		// Detach from any single caller's cancellation so one aborted request cannot fail
		// other coalesced callers sharing this in-flight metadata lookup.
		fetchCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			time.Duration(timeoutMS)*time.Millisecond,
		)
		defer cancel()

		adapter, pool, err := s.openQueryPool(fetchCtx, scope.ds, timeoutMS)
		if err != nil {
			if errors.Is(err, errQueryPoolBusy) {
				// SEC-0027: global pool budget backpressure -- return structured "system busy" (1012 family).
				return nil, domain.ErrSystem()
			}
			return nil, datasourceUnavailable()
		}
		defer func() { _ = pool.Close() }()

		val, err := loader(fetchCtx, adapter, pool)
		if err != nil {
			return nil, err
		}
		if ttl > 0 {
			now := s.now()
			cache.set(key, val, now.Add(ttl), now)
		}
		return val, nil
	})

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, domain.ErrQueryTimeout()
		}
		return nil, ctx.Err()
	case res := <-ch:
		return res.Val, res.Err
	}
}

func (s *Services) loadCachedSchemas(ctx context.Context, scope *metadataTargetScope) ([]SchemaView, error) {
	val, err := s.fetchMetadataCached(ctx, scope, "schemas", "", "",
		func(fetchCtx context.Context, adapter database.Adapter, pool *database.PurposePool) (any, error) {
			names, lerr := adapter.ListSchemas(fetchCtx, pool)
			if lerr != nil {
				return nil, statementRejected()
			}
			copied := make([]string, len(names))
			copy(copied, names)
			return copied, nil
		})
	if err != nil {
		return nil, err
	}
	names := val.([]string)
	out := make([]SchemaView, 0, len(names))
	for _, n := range names {
		out = append(out, SchemaView{Name: n})
	}
	return out, nil
}

func (s *Services) loadCachedTables(ctx context.Context, scope *metadataTargetScope, schema string) ([]TableView, error) {
	val, err := s.fetchMetadataCached(ctx, scope, "tables", schema, "",
		func(fetchCtx context.Context, adapter database.Adapter, pool *database.PurposePool) (any, error) {
			tables, lerr := adapter.ListTables(fetchCtx, pool, schema)
			if lerr != nil {
				if isTimeout(fetchCtx, lerr) {
					return nil, domain.ErrQueryTimeout()
				}
				return nil, statementRejected()
			}
			copied := make([]string, len(tables))
			copy(copied, tables)
			return copied, nil
		})
	if err != nil {
		return nil, err
	}
	tables := val.([]string)
	out := make([]TableView, 0, len(tables))
	for _, t := range tables {
		// Adapter ListTables returns relation names only; finer kind granularity (distinguishing views/materialized views)
		// requires adapter expansion, so v1 uniformly reports "table" (a valid enum value).
		out = append(out, TableView{SchemaName: schema, TableName: t, RelationKind: "table"})
	}
	return out, nil
}

func (s *Services) loadCachedColumns(
	ctx context.Context,
	scope *metadataTargetScope,
	flowID, schema, table string,
) ([]ColumnView, error) {
	// Always evaluate the live masking vocabulary per call so vocabulary edits take effect immediately
	// and flows sharing a physical datasource never leak each other's masking flags.
	vocab, err := s.maskingVocabulary(ctx, flowID, scope.ds.ID)
	if err != nil {
		return nil, err
	}
	val, err := s.fetchMetadataCached(ctx, scope, "columns", schema, table,
		func(fetchCtx context.Context, adapter database.Adapter, pool *database.PurposePool) (any, error) {
			cols, lerr := adapter.ListColumns(fetchCtx, pool, schema, table)
			if lerr != nil {
				if isTimeout(fetchCtx, lerr) {
					return nil, domain.ErrQueryTimeout()
				}
				return nil, statementRejected()
			}
			copied := make([]database.MetadataColumn, len(cols))
			copy(copied, cols)
			return copied, nil
		})
	if err != nil {
		return nil, err
	}
	cols := val.([]database.MetadataColumn)
	vocabSet := map[string]bool{}
	for _, v := range vocab {
		vocabSet[domain.FoldIdentifier(v)] = true
	}
	out := make([]ColumnView, 0, len(cols))
	for _, c := range cols {
		out = append(out, ColumnView{
			SchemaName: c.SchemaName, TableName: c.TableName, ColumnName: c.ColumnName,
			Ordinal: c.Ordinal, DataType: c.DatabaseType, Nullable: c.Nullable,
			Masked:    vocabSet[domain.FoldIdentifier(c.ColumnName)],
			ColumnKey: c.ColumnKey, ColumnDeflt: c.ColumnDeflt,
			ColumnExtra: c.ColumnExtra, ColumnRemark: c.ColumnRemark,
		})
	}
	return out, nil
}

// ListSchemas lists schemas in the target datasource.
func (s *Services) ListSchemas(ctx context.Context, actor Actor, sessionID, dsID string) ([]SchemaView, error) {
	scope, err := s.resolveSessionMetadataScope(ctx, actor, sessionID, dsID)
	if err != nil {
		return nil, err
	}
	return s.loadCachedSchemas(ctx, scope)
}

// ListTables lists relations (tables and views, defaulting kind to "table") within a schema.
func (s *Services) ListTables(ctx context.Context, actor Actor, sessionID, dsID, schema string) ([]TableView, error) {
	scope, err := s.resolveSessionMetadataScope(ctx, actor, sessionID, dsID)
	if err != nil {
		return nil, err
	}
	return s.loadCachedTables(ctx, scope, schema)
}

// ListColumns lists columns of a table (computing masked live against the vocabulary, case-insensitively).
func (s *Services) ListColumns(ctx context.Context, actor Actor, sessionID, dsID, schema, table string) ([]ColumnView, error) {
	scope, err := s.resolveSessionMetadataScope(ctx, actor, sessionID, dsID)
	if err != nil {
		return nil, err
	}
	return s.loadCachedColumns(ctx, scope, scope.flowID, schema, table)
}

// ---- Submitter-side metadata projection (RCP-20260904-SUBMITTER-METADATA-PROJECTION) ----

// FlowDatasourceGranted verifies that the user is granted access to the flow via an enabled permission group
// (for an enabled change_review flow) and that the datasource is referenced by a stage of that flow.
// Shares the same authorization query logic as workflow.CurrentUserFlows.
func (s *Services) FlowDatasourceGranted(ctx context.Context, userID, flowID, dsID string) (bool, error) {
	var n int64
	err := s.DB.WithContext(ctx).Raw(`
		select count(1)
		from flow_stages fs
		join flows f on f.id = fs.flow_id
		join permission_group_flow_grants g on g.flow_id = f.id
		join permission_group_members m on m.permission_group_id = g.permission_group_id
		join permission_groups pg on pg.id = g.permission_group_id and pg.enabled = true
		where m.user_id = ? and f.flow_type = 'change_review' and f.status = 'enabled'
		  and f.id = ? and fs.datasource_id = ?`, userID, flowID, dsID).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListFlowDatasourceSchemas lists schemas for a flow stage datasource.
func (s *Services) ListFlowDatasourceSchemas(ctx context.Context, dsID string) ([]SchemaView, error) {
	scope, err := s.resolveFlowMetadataScope(ctx, dsID)
	if err != nil {
		return nil, err
	}
	return s.loadCachedSchemas(ctx, scope)
}

// ListFlowDatasourceTables lists relations within a schema for a flow stage datasource.
func (s *Services) ListFlowDatasourceTables(ctx context.Context, dsID, schema string) ([]TableView, error) {
	scope, err := s.resolveFlowMetadataScope(ctx, dsID)
	if err != nil {
		return nil, err
	}
	return s.loadCachedTables(ctx, scope, schema)
}

// ListFlowDatasourceColumns lists table columns for a flow stage datasource (computing masked live against the flow vocabulary).
func (s *Services) ListFlowDatasourceColumns(ctx context.Context, flowID, dsID, schema, table string) ([]ColumnView, error) {
	scope, err := s.resolveFlowMetadataScope(ctx, dsID)
	if err != nil {
		return nil, err
	}
	return s.loadCachedColumns(ctx, scope, flowID, schema, table)
}
