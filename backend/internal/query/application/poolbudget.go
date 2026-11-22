// poolbudget.go enforces the global concurrency budgets for query-purpose and metadata-purpose pools (SEC-0027).
// Infrastructure-level exhaustion guard: bounds the number of concurrently open query PurposePools in this process
// (isolating user cursor/execution pools from short-lived metadata browsing pools), applying backpressure via
// structured 1012 capacity errors when exhausted without altering the product contract's "no total row or query count limit" semantics.
// Total deployment capacity scales horizontally via Kubernetes replicas (32 query + 8 metadata pools per process) rather than a settings knob --
// the settings contract explicitly lists concurrency/byte limits under forbidden_fields for the query namespace.
package application

import (
	"errors"
	"sync"
)

// maxConcurrentQueryPools is the per-process upper bound on concurrent query-purpose pools (SEC-0027).
// 32 pools * 8 connections (PurposeQuery default MaxOpenConns) = 256 target connections max per process;
// total deployment capacity equals maxConcurrentQueryPools * replicas and is scaled via replica count rather than app settings.
const maxConcurrentQueryPools = 32

// maxConcurrentMetadataPools is the dedicated per-process upper bound on concurrent metadata-browsing pools,
// isolated from queryPoolSlots so 32 open user cursors never block schema/table/column metadata lookups.
const maxConcurrentMetadataPools = 8

// queryPoolSlots is the process-wide query pool slot semaphore (shared across Services instances within the process).
var queryPoolSlots = make(chan struct{}, maxConcurrentQueryPools)

// metadataPoolSlots is the process-wide metadata pool slot semaphore (isolated from queryPoolSlots).
var metadataPoolSlots = make(chan struct{}, maxConcurrentMetadataPools)

// errQueryPoolBusy indicates the pool budget is exhausted (structured backpressure; mapped by the application layer
// to 1012 SYSTEM_CAPACITY_EXHAUSTED, representing "system busy" rather than datasource unavailable).
var errQueryPoolBusy = errors.New("query: concurrent query pool budget exhausted")

// acquireQueryPoolSlot acquires one query pool slot or immediately returns errQueryPoolBusy when full.
// The returned release function is idempotent (repeated calls release the slot only once).
func acquireQueryPoolSlot() (func(), error) {
	select {
	case queryPoolSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-queryPoolSlots }) }, nil
	default:
		return nil, errQueryPoolBusy
	}
}

// acquireMetadataPoolSlot acquires one metadata pool slot or immediately returns errQueryPoolBusy when full.
// The returned release function is idempotent (repeated calls release the slot only once).
func acquireMetadataPoolSlot() (func(), error) {
	select {
	case metadataPoolSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-metadataPoolSlots }) }, nil
	default:
		return nil, errQueryPoolBusy
	}
}
