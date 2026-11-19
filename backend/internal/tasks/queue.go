// Package tasks implements the persistent async_tasks queue: Claim/Lease/Fence/
// Heartbeat/Complete/Fail/Cancel and recovery (ADR-0003 Lease and Fence protocol).
// Claiming uses FOR UPDATE SKIP LOCKED; each claim increments fence_token so that
// any state write from a stale worker is rejected by the fence check (zombie write protection).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

// Task kinds (the Review-related subset of the controlled async_tasks.kind enum;
// other kinds are defined in their respective packages).
const (
	KindSQLGrouping = "sql_grouping"
	KindAIReview    = "ai_review"
)

// ClaimedTask is a snapshot of a successfully claimed task; Fence is the valid token for this execution.
type ClaimedTask struct {
	ID           string
	Kind         string
	AggregateID  string
	Input        []byte
	FenceToken   int64
	LeaseOwner   string
	LeaseExpires time.Time
}

// ErrNoTask indicates no claimable task is currently available.
var ErrNoTask = errors.New("tasks: no claimable task")

// ErrFenceLost indicates a fence or lease mismatch; the caller must stop writing immediately.
var ErrFenceLost = errors.New("tasks: fence or lease lost")

// claimSQL atomically claims a task: selects one queued or lease-expired running task,
// increments fence_token, and writes the new lease. Reclaiming an expired lease is safe only for
// idempotent local steps; external send boundaries are enforced by the specific Handler (B6 FakeRunner steps are all idempotent local steps).
const claimSQL = `
update async_tasks set
    state = 'running',
    lease_owner = ?,
    lease_expires_at = ?,
    fence_token = fence_token + 1,
    updated_at = now()
where id = (
    select id from async_tasks
    where kind in ? and (state = 'queued' or (state = 'running' and lease_expires_at < now()))
    order by created_at, id
    for update skip locked
    limit 1
)
returning id, kind, coalesce(aggregate_id::text, '') as aggregate_id, input,
          fence_token, lease_owner, lease_expires_at`

// Claim claims a single task; returns ErrNoTask if kinds is empty.
func Claim(ctx context.Context, db *gorm.DB, kinds []string, owner string, lease time.Duration) (*ClaimedTask, error) {
	if len(kinds) == 0 {
		return nil, ErrNoTask
	}
	if lease <= 0 {
		return nil, fmt.Errorf("tasks: lease must be positive")
	}
	var row struct {
		ID           string
		Kind         string
		AggregateID  string
		Input        []byte
		FenceToken   int64
		LeaseOwner   string
		LeaseExpires time.Time
	}
	err := db.WithContext(ctx).Raw(claimSQL, owner, time.Now().UTC().Add(lease), kinds).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, ErrNoTask
	}
	return &ClaimedTask{
		ID: row.ID, Kind: row.Kind, AggregateID: row.AggregateID,
		Input: row.Input, FenceToken: row.FenceToken,
		LeaseOwner: row.LeaseOwner, LeaseExpires: row.LeaseExpires,
	}, nil
}

// guardedUpdate updates the row only when fence and owner match; 0 rows affected returns ErrFenceLost.
func guardedUpdate(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string, sets map[string]any) error {
	res := db.WithContext(ctx).Model(AsyncTaskModel{}).
		Where("id = ? and fence_token = ? and lease_owner = ?", taskID, fence, owner).
		Updates(sets)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrFenceLost
	}
	return nil
}

// Extend renews the lease without modifying progress.
func Extend(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string, extend time.Duration) error {
	return guardedUpdate(ctx, db, taskID, fence, owner, map[string]any{
		"lease_expires_at": time.Now().UTC().Add(extend),
		"updated_at":       time.Now().UTC(),
	})
}

// CheckFence performs a read-only check that fence and owner still match (zombie guard before terminal business writes).
func CheckFence(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string) error {
	var n int64
	if err := db.WithContext(ctx).Raw(
		`select count(*) from async_tasks where id = ? and fence_token = ? and lease_owner = ?`,
		taskID, fence, owner).Scan(&n).Error; err != nil {
		return err
	}
	if n != 1 {
		return ErrFenceLost
	}
	return nil
}

// Heartbeat renews the lease and advances progress.
func Heartbeat(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string, extend time.Duration, completed, total int64) error {
	if completed < 0 || total < 0 || completed > total {
		return fmt.Errorf("tasks: invalid progress %d/%d", completed, total)
	}
	return guardedUpdate(ctx, db, taskID, fence, owner, map[string]any{
		"lease_expires_at":   time.Now().UTC().Add(extend),
		"updated_at":         time.Now().UTC(),
		"progress_completed": completed,
		"progress_total":     total,
	})
}

// Complete marks the task as succeeded and clears its lease.
func Complete(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string, resultRef string) error {
	return guardedUpdate(ctx, db, taskID, fence, owner, map[string]any{
		"state":            "succeeded",
		"result_ref":       resultRef,
		"lease_owner":      nil,
		"lease_expires_at": nil,
		"updated_at":       time.Now().UTC(),
		"finished_at":      time.Now().UTC(),
	})
}

// FailWith marks the task as failed: cancelled/failed states must carry a business error code and failure name
// (per async_task_terminal_fields).
func FailWith(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string, state string, bizCode int32, failureName string) error {
	if state != "failed" && state != "cancelled" {
		return fmt.Errorf("tasks: invalid failure state %q", state)
	}
	return guardedUpdate(ctx, db, taskID, fence, owner, map[string]any{
		"state":             state,
		"business_err_code": bizCode,
		"failure_name":      failureName,
		"lease_owner":       nil,
		"lease_expires_at":  nil,
		"updated_at":        time.Now().UTC(),
		"finished_at":       time.Now().UTC(),
	})
}

// FailBusiness maps a domain error to a task failure (preserving domain errors and falling back to internal failure).
func FailBusiness(ctx context.Context, db *gorm.DB, taskID string, fence int64, owner string, err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return FailWith(ctx, db, taskID, fence, owner, "failed", int32(de.Code), de.Name)
	}
	return FailWith(ctx, db, taskID, fence, owner, "failed", int32(domain.CodeAIProviderFailed), "INTERNAL")
}

// Cancel requests task cancellation: only queued/running tasks can be cancelled, transitioning directly to cancelled.
// Uses REVIEW_COVERAGE_INCOMPLETE (2007) as the cancellation code since cancellation leaves review coverage incomplete,
// while the failure name 'cancelled' faithfully records the reason.
func Cancel(ctx context.Context, db *gorm.DB, taskID string) error {
	res := db.WithContext(ctx).Model(AsyncTaskModel{}).
		Where("id = ? and state in ('queued','running')", taskID).
		Updates(map[string]any{
			"state":             "cancelled",
			"business_err_code": int32(domain.CodeReviewCoverageIncomp),
			"failure_name":      "cancelled",
			"lease_owner":       nil,
			"lease_expires_at":  nil,
			"updated_at":        time.Now().UTC(),
			"finished_at":       time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	// 0 rows affected means the task is already terminal or does not exist: treat cancel as an idempotent no-op
	// (avoiding a misleading 2007 error during race windows); >1 rows affected is prevented by the primary key constraint.
	return nil
}
