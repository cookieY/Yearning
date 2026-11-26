package application

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
)

type fakeMetadataAdapter struct {
	database.Adapter
	schemaCalls atomic.Int32
	tableCalls  atomic.Int32
	columnCalls atomic.Int32

	gate    chan struct{}
	entered chan struct{}

	failSchemas atomic.Bool
	schemas     []string
	tables      map[string][]string
	columns     map[string][]database.MetadataColumn
}

func (f *fakeMetadataAdapter) ListSchemas(ctx context.Context, _ *database.PurposePool) ([]string, error) {
	f.schemaCalls.Add(1)
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.failSchemas.Load() {
		return nil, errors.New("simulated target failure")
	}
	return f.schemas, nil
}

func (f *fakeMetadataAdapter) ListTables(ctx context.Context, _ *database.PurposePool, schema string) ([]string, error) {
	f.tableCalls.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.tables[schema], nil
}

func (f *fakeMetadataAdapter) ListColumns(ctx context.Context, _ *database.PurposePool, schema, table string) ([]database.MetadataColumn, error) {
	f.columnCalls.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.columns[schema+"."+table], nil
}

type dynamicMaskingSource struct {
	mu    sync.RWMutex
	byKey map[string][]string
}

func (d *dynamicMaskingSource) Vocabulary(_ context.Context, _ *gorm.DB, flowID, datasourceID string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cols := d.byKey[flowID+"|"+datasourceID]
	out := make([]string, len(cols))
	copy(out, cols)
	return out, nil
}

func newUnitMetadataServices(adapter *fakeMetadataAdapter, now *time.Time, poolOpens *atomic.Int32) (*Services, *dynamicMaskingSource) {
	mask := &dynamicMaskingSource{byKey: map[string][]string{}}
	svc := &Services{
		MaskingSource: mask,
		metaCacheTTL:  60 * time.Second,
		nowFunc:       func() time.Time { return *now },
		PoolFactory: func(_ context.Context, _ *datasource.DatasourceModel, _ int) (database.Adapter, *database.PurposePool, error) {
			if poolOpens != nil {
				poolOpens.Add(1)
			}
			return adapter, &database.PurposePool{}, nil
		},
	}
	return svc, mask
}

func TestMetadataCacheHitTTLExpiryAndEviction(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var poolOpens atomic.Int32
	adapter := &fakeMetadataAdapter{
		schemas: []string{"app", "public"},
		tables:  map[string][]string{"app": {"users", "orders"}},
		columns: map[string][]database.MetadataColumn{
			"app.users": {
				{SchemaName: "app", TableName: "users", ColumnName: "id", Ordinal: 1, DatabaseType: "bigint"},
				{SchemaName: "app", TableName: "users", ColumnName: "email", Ordinal: 2, DatabaseType: "varchar"},
			},
		},
	}
	svc, _ := newUnitMetadataServices(adapter, &now, &poolOpens)
	fp := "fp-1"
	scope := &metadataTargetScope{
		ds:      &datasource.DatasourceModel{ID: "ds-1", Version: 1, Enabled: true, IdentityFingerprint: &fp},
		credKey: "query:cred-1:100",
		flowID:  "flow-1",
	}

	// Repeated reads within TTL hit cache without re-opening target pools.
	s1, err := svc.loadCachedSchemas(context.Background(), scope)
	if err != nil || len(s1) != 2 {
		t.Fatalf("first ListSchemas failed: s1=%+v err=%v", s1, err)
	}
	s2, err := svc.loadCachedSchemas(context.Background(), scope)
	if err != nil || len(s2) != 2 {
		t.Fatalf("second ListSchemas failed: s2=%+v err=%v", s2, err)
	}
	if adapter.schemaCalls.Load() != 1 || poolOpens.Load() != 1 {
		t.Fatalf("expected 1 target call on schema cache hit, got schemaCalls=%d poolOpens=%d",
			adapter.schemaCalls.Load(), poolOpens.Load())
	}

	// Advancing clock past 60s TTL triggers a fresh upstream fetch.
	now = now.Add(61 * time.Second)
	if _, err := svc.loadCachedSchemas(context.Background(), scope); err != nil {
		t.Fatalf("post-expiry ListSchemas failed: %v", err)
	}
	if adapter.schemaCalls.Load() != 2 {
		t.Fatalf("expected 2 target calls after TTL expiry, got %d", adapter.schemaCalls.Load())
	}

	// Bounded cache evicts oldest entries cleanly when full.
	small := newMetadataCache(2)
	small.set("k1", "v1", now.Add(10*time.Second), now)
	small.set("k2", "v2", now.Add(20*time.Second), now)
	small.set("k3", "v3", now.Add(30*time.Second), now)
	if _, ok := small.get("k1", now); ok {
		t.Fatal("expected oldest entry k1 to be evicted when cache exceeded capacity")
	}
	if _, ok := small.get("k2", now); !ok {
		t.Fatal("expected k2 to remain cached")
	}
	if _, ok := small.get("k3", now); !ok {
		t.Fatal("expected k3 to be cached")
	}
}

func TestMetadataCacheSingleflightCoalescesConcurrentFetches(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var poolOpens atomic.Int32
	adapter := &fakeMetadataAdapter{
		gate:    make(chan struct{}),
		entered: make(chan struct{}, 1),
		schemas: []string{"app", "reporting"},
	}
	svc, _ := newUnitMetadataServices(adapter, &now, &poolOpens)
	fp := "fp-sf"
	scope := &metadataTargetScope{
		ds:      &datasource.DatasourceModel{ID: "ds-sf", Version: 1, Enabled: true, IdentityFingerprint: &fp},
		credKey: "query:cred-sf:1",
	}

	const total = 12
	cancelCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	var wg sync.WaitGroup
	errs := make([]error, total)
	counts := make([]int, total)

	// Start caller 0 (which will cancel mid-flight) so it owns the initial singleflight entry.
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := svc.loadCachedSchemas(cancelCtx, scope)
		errs[0] = err
		counts[0] = len(out)
	}()

	// Wait until the singleflight leader has actually entered adapter.ListSchemas.
	select {
	case <-adapter.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for leader to enter ListSchemas")
	}

	// Launch 11 concurrent waiters that should coalesce onto the same flight.
	for i := 1; i < total; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			out, err := svc.loadCachedSchemas(context.Background(), scope)
			errs[idx] = err
			counts[idx] = len(out)
		}(i)
	}

	// Give waiters a moment to subscribe to singleflight, cancel caller 0, then release the gate.
	time.Sleep(30 * time.Millisecond)
	cancelFirst()
	close(adapter.gate)
	wg.Wait()

	if !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("expected cancelled caller 0 to receive context.Canceled, got %v", errs[0])
	}
	for i := 1; i < total; i++ {
		if errs[i] != nil || counts[i] != 2 {
			t.Fatalf("coalesced caller %d failed: count=%d err=%v", i, counts[i], errs[i])
		}
	}
	if adapter.schemaCalls.Load() != 1 || poolOpens.Load() != 1 {
		t.Fatalf("expected singleflight to issue exactly 1 upstream fetch, got schemaCalls=%d poolOpens=%d",
			adapter.schemaCalls.Load(), poolOpens.Load())
	}
}

func TestMetadataCacheEvaluatesMaskingVocabularyLiveAndIsolatesKeys(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var poolOpens atomic.Int32
	adapter := &fakeMetadataAdapter{
		columns: map[string][]database.MetadataColumn{
			"app.users": {
				{SchemaName: "app", TableName: "users", ColumnName: "email", Ordinal: 1, DatabaseType: "varchar"},
				{SchemaName: "app", TableName: "users", ColumnName: "phone", Ordinal: 2, DatabaseType: "varchar"},
			},
		},
	}
	svc, mask := newUnitMetadataServices(adapter, &now, &poolOpens)
	mask.byKey["flow-A|ds-1"] = []string{"email"}
	mask.byKey["flow-B|ds-1"] = []string{"phone"}

	fp1 := "fp-1"
	scope := &metadataTargetScope{
		ds:      &datasource.DatasourceModel{ID: "ds-1", Version: 1, Enabled: true, IdentityFingerprint: &fp1},
		credKey: "query:cred-1:100",
		flowID:  "flow-A",
	}

	colsA, err := svc.loadCachedColumns(context.Background(), scope, "flow-A", "app", "users")
	if err != nil || !colsA[0].Masked || colsA[1].Masked {
		t.Fatalf("flow-A masking mismatch: %+v err=%v", colsA, err)
	}
	// flow-B shares raw column cache without cross-flow masking leakage.
	colsB, err := svc.loadCachedColumns(context.Background(), scope, "flow-B", "app", "users")
	if err != nil || colsB[0].Masked || !colsB[1].Masked {
		t.Fatalf("flow-B masking mismatch: %+v err=%v", colsB, err)
	}
	if adapter.columnCalls.Load() != 1 {
		t.Fatalf("expected 1 upstream column fetch shared across flows, got %d", adapter.columnCalls.Load())
	}

	// Updating flow-A vocabulary takes effect immediately even while raw columns remain cached.
	mask.mu.Lock()
	mask.byKey["flow-A|ds-1"] = []string{"phone"}
	mask.mu.Unlock()
	colsA2, err := svc.loadCachedColumns(context.Background(), scope, "flow-A", "app", "users")
	if err != nil || colsA2[0].Masked || !colsA2[1].Masked {
		t.Fatalf("flow-A updated masking not reflected live: %+v err=%v", colsA2, err)
	}
	if adapter.columnCalls.Load() != 1 {
		t.Fatalf("expected live vocab re-evaluation without extra DB call, got %d", adapter.columnCalls.Load())
	}

	// Changing datasource version, identity fingerprint, or credential key invalidates the cache key.
	scopeV2 := *scope
	dsV2 := *scope.ds
	dsV2.Version = 2
	scopeV2.ds = &dsV2
	if _, err := svc.loadCachedColumns(context.Background(), &scopeV2, "flow-A", "app", "users"); err != nil {
		t.Fatalf("v2 fetch failed: %v", err)
	}
	if adapter.columnCalls.Load() != 2 {
		t.Fatalf("expected datasource version bump to miss cache, got %d", adapter.columnCalls.Load())
	}

	scopeCredRotated := scopeV2
	scopeCredRotated.credKey = "query:cred-2:200"
	if _, err := svc.loadCachedColumns(context.Background(), &scopeCredRotated, "flow-A", "app", "users"); err != nil {
		t.Fatalf("rotated cred fetch failed: %v", err)
	}
	if adapter.columnCalls.Load() != 3 {
		t.Fatalf("expected credential rotation to miss cache, got %d", adapter.columnCalls.Load())
	}
}

func TestMetadataCacheDoesNotCacheErrors(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	adapter := &fakeMetadataAdapter{schemas: []string{"app"}}
	adapter.failSchemas.Store(true)
	svc, _ := newUnitMetadataServices(adapter, &now, nil)
	scope := &metadataTargetScope{
		ds:      &datasource.DatasourceModel{ID: "ds-err", Version: 1, Enabled: true},
		credKey: "poolfactory",
	}

	if _, err := svc.loadCachedSchemas(context.Background(), scope); err == nil {
		t.Fatal("expected error on initial failing fetch")
	}
	adapter.failSchemas.Store(false)
	got, err := svc.loadCachedSchemas(context.Background(), scope)
	if err != nil || len(got) != 1 || got[0].Name != "app" {
		t.Fatalf("expected retry after error to succeed, got %+v err=%v", got, err)
	}
	if adapter.schemaCalls.Load() != 2 {
		t.Fatalf("expected 2 target calls (failed attempt not cached), got %d", adapter.schemaCalls.Load())
	}
}
