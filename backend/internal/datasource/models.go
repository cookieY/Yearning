// models.go GORM models for the datasource domain (gorm-model-registry: datasource/repository/postgres).
package datasource

import "time"

// DatasourceModel maps to the datasources table. Engine is distinct from Vendor/compatibility mode.
type DatasourceModel struct {
	ID                string  `gorm:"column:id;primaryKey"`
	Name              string  `gorm:"column:name"`
	Engine            string  `gorm:"column:engine"`             // mysql|postgresql|tidb|oceanbase|polardb
	CompatibilityMode string  `gorm:"column:compatibility_mode"` // mysql|postgresql
	DeploymentKind    string  `gorm:"column:deployment_kind"`    // native|cloud
	Host              string  `gorm:"column:host"`
	Port              int     `gorm:"column:port"`
	DatabaseName      *string `gorm:"column:database_name"`
	VersionConstraint string  `gorm:"column:version_constraint"`
	Enabled           bool    `gorm:"column:enabled"`
	// TestPurpose is the connection test purpose (§18.61): the table 'Test Connection' action selects
	// stored credentials by this column (defaulting to review; updated via DatasourceWrite).
	TestPurpose         string    `gorm:"column:test_purpose"`
	IdentityFingerprint *string   `gorm:"column:identity_fingerprint"`
	Metadata            []byte    `gorm:"column:metadata"`
	Version             int64     `gorm:"column:version"`
	CreatedByUserID     string    `gorm:"column:created_by_user_id"`
	CreatedAt           time.Time `gorm:"column:created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at"`
}

func (DatasourceModel) TableName() string { return "datasources" }

// DatasourceCredentialModel maps to the datasource_credentials table (per-Purpose credentials).
type DatasourceCredentialModel struct {
	ID                   string    `gorm:"column:id;primaryKey"`
	DatasourceID         string    `gorm:"column:datasource_id"`
	Purpose              string    `gorm:"column:purpose"` // review|query|execution
	CredentialCiphertext []byte    `gorm:"column:credential_ciphertext"`
	CredentialNonce      []byte    `gorm:"column:credential_nonce"`
	CredentialKeyID      string    `gorm:"column:credential_key_id"`
	CredentialAlgorithm  string    `gorm:"column:credential_algorithm"`
	Enabled              bool      `gorm:"column:enabled"`
	CreatedAt            time.Time `gorm:"column:created_at"`
	UpdatedAt            time.Time `gorm:"column:updated_at"`
}

func (DatasourceCredentialModel) TableName() string { return "datasource_credentials" }

// DatasourceTLSMaterialModel maps to the datasource_tls_materials table.
type DatasourceTLSMaterialModel struct {
	ID                 string    `gorm:"column:id;primaryKey"`
	DatasourceID       string    `gorm:"column:datasource_id"`
	Purpose            string    `gorm:"column:purpose"`
	VerifyServer       bool      `gorm:"column:verify_server"`
	ServerName         *string   `gorm:"column:server_name"`
	MaterialCiphertext []byte    `gorm:"column:material_ciphertext"`
	MaterialNonce      []byte    `gorm:"column:material_nonce"`
	MaterialKeyID      *string   `gorm:"column:material_key_id"`
	MaterialAlgorithm  *string   `gorm:"column:material_algorithm"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

func (DatasourceTLSMaterialModel) TableName() string { return "datasource_tls_materials" }

// AsyncTaskModel maps to the async_tasks table (connection test tasks).
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
	BusinessErrCode   *int       `gorm:"column:business_err_code"`
	FailureName       *string    `gorm:"column:failure_name"`
	LeaseOwner        *string    `gorm:"column:lease_owner"`
	LeaseExpiresAt    *time.Time `gorm:"column:lease_expires_at"`
	FenceToken        int64      `gorm:"column:fence_token"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
	FinishedAt        *time.Time `gorm:"column:finished_at"`
}

func (AsyncTaskModel) TableName() string { return "async_tasks" }
