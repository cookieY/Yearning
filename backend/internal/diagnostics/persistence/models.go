// models.go GORM models for the SQL Performance Diagnostics domain (gorm-model-registry: diagnostics/persistence;
// schema frozen by 000027_sql_diagnostics.up.sql).
// JSON tags and GORM association fields are strictly prohibited on persistence models (ADR-0005 / gorm-model-registry).
package persistence

import "time"

// DatasourceDiagnosticsConfigModel maps to datasource_diagnostics_configs.
type DatasourceDiagnosticsConfigModel struct {
	ID                       string     `gorm:"column:id;primaryKey"`
	DatasourceID             string     `gorm:"column:datasource_id"`
	Enabled                  bool       `gorm:"column:enabled"`
	IntervalSeconds          int        `gorm:"column:interval_seconds"`
	EffectiveIntervalSeconds int        `gorm:"column:effective_interval_seconds"`
	TopN                     int        `gorm:"column:top_n"`
	State                    string     `gorm:"column:state"`
	LastProbeState           string     `gorm:"column:last_probe_state"`
	LastProbeAt              *time.Time `gorm:"column:last_probe_at"`
	LastCollectedAt          *time.Time `gorm:"column:last_collected_at"`
	LastManualSnapshotAt     *time.Time `gorm:"column:last_manual_snapshot_at"`
	ConsecutiveFailures      int        `gorm:"column:consecutive_failures"`
	PausedReason             *string    `gorm:"column:paused_reason"`
	Version                  int64      `gorm:"column:version"`
	CreatedAt                time.Time  `gorm:"column:created_at"`
	UpdatedAt                time.Time  `gorm:"column:updated_at"`
}

func (DatasourceDiagnosticsConfigModel) TableName() string {
	return "datasource_diagnostics_configs"
}

// DiagnosticsInstanceLeaseModel maps to diagnostics_instance_leases.
type DiagnosticsInstanceLeaseModel struct {
	ID                        string     `gorm:"column:id;primaryKey"`
	ServerIdentityFingerprint string     `gorm:"column:server_identity_fingerprint"`
	DatasourceID              string     `gorm:"column:datasource_id"`
	HolderID                  string     `gorm:"column:holder_id"`
	LeaseExpiresAt            time.Time  `gorm:"column:lease_expires_at"`
	LastCollectedAt           *time.Time `gorm:"column:last_collected_at"`
	Version                   int64      `gorm:"column:version"`
	CreatedAt                 time.Time  `gorm:"column:created_at"`
	UpdatedAt                 time.Time  `gorm:"column:updated_at"`
}

func (DiagnosticsInstanceLeaseModel) TableName() string {
	return "diagnostics_instance_leases"
}

// SQLDiagnosticsSnapshotModel maps to sql_diagnostics_snapshots.
type SQLDiagnosticsSnapshotModel struct {
	ID             string    `gorm:"column:id;primaryKey"`
	DatasourceID   string    `gorm:"column:datasource_id"`
	Engine         string    `gorm:"column:engine"`
	TriggerKind    string    `gorm:"column:trigger_kind"`
	ProbeState     string    `gorm:"column:probe_state"`
	TopN           int       `gorm:"column:top_n"`
	StatementCount int       `gorm:"column:statement_count"`
	DurationMS     int       `gorm:"column:duration_ms"`
	ResetDetected  bool      `gorm:"column:reset_detected"`
	GapDetected    bool      `gorm:"column:gap_detected"`
	FailureReason  *string   `gorm:"column:failure_reason"`
	SampledAt      time.Time `gorm:"column:sampled_at"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (SQLDiagnosticsSnapshotModel) TableName() string {
	return "sql_diagnostics_snapshots"
}

// SQLDiagnosticsStatementModel maps to sql_diagnostics_statements.
type SQLDiagnosticsStatementModel struct {
	ID                                      string     `gorm:"column:id;primaryKey"`
	DatasourceID                            string     `gorm:"column:datasource_id"`
	Engine                                  string     `gorm:"column:engine"`
	EngineFingerprint                       string     `gorm:"column:engine_fingerprint"`
	YearningFingerprint                     *string    `gorm:"column:yearning_fingerprint"`
	DatabaseOrSchema                        string     `gorm:"column:database_or_schema"`
	TemplateStatus                          string     `gorm:"column:template_status"`
	TemplateCiphertext                      []byte     `gorm:"column:template_ciphertext"`
	TemplateNonce                           []byte     `gorm:"column:template_nonce"`
	TemplateKeyID                           *string    `gorm:"column:template_key_id"`
	TemplateAlgorithm                       *string    `gorm:"column:template_algorithm"`
	TemplateFetchedAt                       *time.Time `gorm:"column:template_fetched_at"`
	LatestSnapshotID                        *string    `gorm:"column:latest_snapshot_id"`
	LatestCallsTotal                        int64      `gorm:"column:latest_calls_total"`
	LatestTotalExecTimeMS                   float64    `gorm:"column:latest_total_exec_time_ms"`
	LatestRowsExaminedOrSharedBlksReadTotal int64      `gorm:"column:latest_rows_examined_or_shared_blks_read_total"`
	LatestRowsSentOrRowsReturnedTotal       int64      `gorm:"column:latest_rows_sent_or_rows_returned_total"`
	LatestLockTimeMSOrTempBlksWrittenTotal  float64    `gorm:"column:latest_lock_time_ms_or_temp_blks_written_total"`
	LatestPlansTotal                        *int64     `gorm:"column:latest_plans_total"`
	LatestTotalPlanTimeMS                   *float64   `gorm:"column:latest_total_plan_time_ms"`
	LatestTempBlksReadTotal                 *int64     `gorm:"column:latest_temp_blks_read_total"`
	LatestSharedBlksHitTotal                *int64     `gorm:"column:latest_shared_blks_hit_total"`
	FirstSeenAt                             time.Time  `gorm:"column:first_seen_at"`
	LastSeenAt                              time.Time  `gorm:"column:last_seen_at"`
	Version                                 int64      `gorm:"column:version"`
	CreatedAt                               time.Time  `gorm:"column:created_at"`
	UpdatedAt                               time.Time  `gorm:"column:updated_at"`
}

func (SQLDiagnosticsStatementModel) TableName() string {
	return "sql_diagnostics_statements"
}

// SQLDiagnosticsBucketMetricModel maps to sql_diagnostics_bucket_metrics.
type SQLDiagnosticsBucketMetricModel struct {
	ID                                string    `gorm:"column:id;primaryKey"`
	DatasourceID                      string    `gorm:"column:datasource_id"`
	StatementID                       string    `gorm:"column:statement_id"`
	SnapshotID                        string    `gorm:"column:snapshot_id"`
	BucketStart                       time.Time `gorm:"column:bucket_start"`
	BucketEnd                         time.Time `gorm:"column:bucket_end"`
	CoverageMode                      string    `gorm:"column:coverage_mode"`
	CallsTotal                        int64     `gorm:"column:calls_total"`
	TotalExecTimeMS                   float64   `gorm:"column:total_exec_time_ms"`
	RowsExaminedOrSharedBlksReadTotal int64     `gorm:"column:rows_examined_or_shared_blks_read_total"`
	RowsSentOrRowsReturnedTotal       int64     `gorm:"column:rows_sent_or_rows_returned_total"`
	LockTimeMSOrTempBlksWrittenTotal  float64   `gorm:"column:lock_time_ms_or_temp_blks_written_total"`
	DeltaCalls                        int64     `gorm:"column:delta_calls"`
	DeltaExecTimeMS                   float64   `gorm:"column:delta_exec_time_ms"`
	AvgExecTimeMS                     float64   `gorm:"column:avg_exec_time_ms"`
	DeltaRowsExaminedOrSharedBlksRead int64     `gorm:"column:delta_rows_examined_or_shared_blks_read"`
	DeltaRowsSentOrRowsReturned       int64     `gorm:"column:delta_rows_sent_or_rows_returned"`
	DeltaLockTimeMSOrTempBlksWritten  float64   `gorm:"column:delta_lock_time_ms_or_temp_blks_written"`
	DeltaPlans                        *int64    `gorm:"column:delta_plans"`
	DeltaPlanTimeMS                   *float64  `gorm:"column:delta_plan_time_ms"`
	DeltaTempBlksRead                 *int64    `gorm:"column:delta_temp_blks_read"`
	DeltaSharedBlksHit                *int64    `gorm:"column:delta_shared_blks_hit"`
	ResetDetected                     bool      `gorm:"column:reset_detected"`
	FirstObserved                     bool      `gorm:"column:first_observed"`
	CreatedAt                         time.Time `gorm:"column:created_at"`
}

func (SQLDiagnosticsBucketMetricModel) TableName() string {
	return "sql_diagnostics_bucket_metrics"
}

// SQLDiagnosticsAIReportModel maps to sql_diagnostics_ai_reports.
type SQLDiagnosticsAIReportModel struct {
	ID                      string    `gorm:"column:id;primaryKey"`
	DatasourceID            string    `gorm:"column:datasource_id"`
	StatementID             string    `gorm:"column:statement_id"`
	UserID                  string    `gorm:"column:user_id"`
	WindowStart             time.Time `gorm:"column:window_start"`
	WindowEnd               time.Time `gorm:"column:window_end"`
	CoverageMode            string    `gorm:"column:coverage_mode"`
	ReportCiphertext        []byte    `gorm:"column:report_ciphertext"`
	ReportNonce             []byte    `gorm:"column:report_nonce"`
	ReportKeyID             string    `gorm:"column:report_key_id"`
	ReportAlgorithm         string    `gorm:"column:report_algorithm"`
	CandidateSQLCiphertext  []byte    `gorm:"column:candidate_sql_ciphertext"`
	CandidateSQLNonce       []byte    `gorm:"column:candidate_sql_nonce"`
	CandidateSQLKeyID       *string   `gorm:"column:candidate_sql_key_id"`
	CandidateSQLAlgorithm   *string   `gorm:"column:candidate_sql_algorithm"`
	CandidateSQLHMAC        []byte    `gorm:"column:candidate_sql_hmac"`
	CandidateSQLHMACKeyID   *string   `gorm:"column:candidate_sql_hmac_key_id"`
	CandidateStatementKind  *string   `gorm:"column:candidate_statement_kind"`
	SubmissionState         string    `gorm:"column:submission_state"`
	FlowID                  *string   `gorm:"column:flow_id"`
	EphemeralRunID          *string   `gorm:"column:ephemeral_run_id"`
	SubmittedOrderID        *string   `gorm:"column:submitted_order_id"`
	SubmittedOrderDisplayNo *string   `gorm:"column:submitted_order_display_no"`
	ExpiresAt               time.Time `gorm:"column:expires_at"`
	Version                 int64     `gorm:"column:version"`
	CreatedAt               time.Time `gorm:"column:created_at"`
	UpdatedAt               time.Time `gorm:"column:updated_at"`
}

func (SQLDiagnosticsAIReportModel) TableName() string {
	return "sql_diagnostics_ai_reports"
}

// SQLDiagnosticsOrderLinkModel maps to sql_diagnostics_order_links.
type SQLDiagnosticsOrderLinkModel struct {
	ID                string     `gorm:"column:id;primaryKey"`
	DatasourceID      string     `gorm:"column:datasource_id"`
	StatementID       string     `gorm:"column:statement_id"`
	TargetStatementID *string    `gorm:"column:target_statement_id"`
	ChangeOrderID     string     `gorm:"column:change_order_id"`
	OrderDisplayNo    string     `gorm:"column:order_display_no"`
	RelationKind      string     `gorm:"column:relation_kind"`
	OrderExecutedAt   *time.Time `gorm:"column:order_executed_at"`
	ConfirmedByUserID string     `gorm:"column:confirmed_by_user_id"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
}

func (SQLDiagnosticsOrderLinkModel) TableName() string {
	return "sql_diagnostics_order_links"
}
