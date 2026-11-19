// models.go defines GORM models for the workflow domain (gorm-model-registry: workflow/repository/postgres).
package workflow

import "time"

// FlowModel maps to the flows table.
type FlowModel struct {
	ID              string    `gorm:"column:id;primaryKey"`
	Name            string    `gorm:"column:name"`
	FlowType        string    `gorm:"column:flow_type"` // change_review|query_access
	Status          string    `gorm:"column:status"`    // enabled|disabled|invalid
	RuleSetID       *string   `gorm:"column:rule_set_id"`
	Version         int64     `gorm:"column:version"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (FlowModel) TableName() string { return "flows" }

// FlowStageModel maps to the flow_stages table; schema mappings use envelope encryption.
type FlowStageModel struct {
	ID                      string `gorm:"column:id;primaryKey"`
	FlowID                  string `gorm:"column:flow_id"`
	Position                int    `gorm:"column:position"`
	DatasourceID            string `gorm:"column:datasource_id"`
	SchemaMappingCiphertext []byte `gorm:"column:schema_mapping_ciphertext"`
	SchemaMappingNonce      []byte `gorm:"column:schema_mapping_nonce"`
	SchemaMappingKeyID      string `gorm:"column:schema_mapping_key_id"`
	SchemaMappingAlgorithm  string `gorm:"column:schema_mapping_algorithm"`
}

func (FlowStageModel) TableName() string { return "flow_stages" }

// FlowApprovalStepModel maps to the flow_approval_steps table;
// flow-level approval steps for query flows have a NULL stage_id.
type FlowApprovalStepModel struct {
	ID       string  `gorm:"column:id;primaryKey"`
	FlowID   string  `gorm:"column:flow_id"`
	StageID  *string `gorm:"column:stage_id"`
	Position int     `gorm:"column:position"`
}

func (FlowApprovalStepModel) TableName() string { return "flow_approval_steps" }

// FlowApprovalStepActorModel maps to the flow_approval_step_actors table.
type FlowApprovalStepActorModel struct {
	StepID string `gorm:"column:step_id;primaryKey"`
	UserID string `gorm:"column:user_id;primaryKey"`
}

func (FlowApprovalStepActorModel) TableName() string { return "flow_approval_step_actors" }

// FlowExecutionActorModel maps to the flow_execution_actors table.
type FlowExecutionActorModel struct {
	StageID string `gorm:"column:stage_id;primaryKey"`
	UserID  string `gorm:"column:user_id;primaryKey"`
}

func (FlowExecutionActorModel) TableName() string { return "flow_execution_actors" }

// FlowQueryCapabilityModel maps to the flow_query_capabilities table.
type FlowQueryCapabilityModel struct {
	FlowID       string `gorm:"column:flow_id;primaryKey"`
	DatasourceID string `gorm:"column:datasource_id;primaryKey"`
	CanQuery     bool   `gorm:"column:can_query"`
	CanExport    bool   `gorm:"column:can_export"`
}

func (FlowQueryCapabilityModel) TableName() string { return "flow_query_capabilities" }

// PermissionGroupModel maps to the permission_groups table.
type PermissionGroupModel struct {
	ID              string    `gorm:"column:id;primaryKey"`
	Name            string    `gorm:"column:name"`
	Enabled         bool      `gorm:"column:enabled"`
	Version         int64     `gorm:"column:version"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (PermissionGroupModel) TableName() string { return "permission_groups" }

// PermissionGroupMemberModel maps to the permission_group_members table.
type PermissionGroupMemberModel struct {
	PermissionGroupID string `gorm:"column:permission_group_id;primaryKey"`
	UserID            string `gorm:"column:user_id;primaryKey"`
}

func (PermissionGroupMemberModel) TableName() string { return "permission_group_members" }

// PermissionGroupFlowGrantModel maps to the permission_group_flow_grants table.
type PermissionGroupFlowGrantModel struct {
	PermissionGroupID string `gorm:"column:permission_group_id;primaryKey"`
	FlowID            string `gorm:"column:flow_id;primaryKey"`
}

func (PermissionGroupFlowGrantModel) TableName() string { return "permission_group_flow_grants" }
