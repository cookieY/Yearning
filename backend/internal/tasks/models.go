// models.go defines GORM models for async_tasks (gorm-model-registry: platform/tasks/postgres).
package tasks

import "time"

// AsyncTaskModel maps to the async_tasks table (mutable_aggregate).
type AsyncTaskModel struct {
	ID                string     `gorm:"column:id;primaryKey"`
	Kind              string     `gorm:"column:kind"`
	State             string     `gorm:"column:state"`
	AggregateType     *string    `gorm:"column:aggregate_type"`
	AggregateID       *string    `gorm:"column:aggregate_id"`
	Input             []byte     `gorm:"column:input"`
	ProgressCompleted int64      `gorm:"column:progress_completed"`
	ProgressTotal     int64      `gorm:"column:progress_total"`
	ProgressUnit      string     `gorm:"column:progress_unit"`
	ResultRef         *string    `gorm:"column:result_ref"`
	BusinessErrCode   *int32     `gorm:"column:business_err_code"`
	FailureName       *string    `gorm:"column:failure_name"`
	LeaseOwner        *string    `gorm:"column:lease_owner"`
	LeaseExpiresAt    *time.Time `gorm:"column:lease_expires_at"`
	FenceToken        int64      `gorm:"column:fence_token"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
	FinishedAt        *time.Time `gorm:"column:finished_at"`
}

func (AsyncTaskModel) TableName() string { return "async_tasks" }

// TaskKindAdmin is the control-plane connection test task kind (used since B3).
const (
	KindAdminConnectionTest      = "admin_connection_test"
	KindAIProviderConnectionTest = "ai_provider_connection_test"
	KindIdentityProviderConnTest = "identity_provider_connection_test"
)
