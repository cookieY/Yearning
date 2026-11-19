// types.go defines request/response views and state constants for the SQL Performance Diagnostics domain (aligned with api/openapi/yearning-v4.yaml).
package domain

import "time"

const (
	DefaultIntervalSeconds = 60
	DefaultTopN            = 200
	MinTopN                = 50
	MaxTopN                = 500
	ManualSnapshotCooldown = 30 * time.Second

	DegradeFailureThreshold = 2
	PauseFailureThreshold   = 5

	MinComparableDeltaBuckets = 3
	MinComparableDeltaCalls   = int64(10)

	BucketRetention   = 14 * 24 * time.Hour
	ArtifactRetention = 30 * 24 * time.Hour
)

// Actor represents an authenticated management-plane caller.
type Actor struct {
	UserID         string
	Username       string
	IsBuiltinAdmin bool
	Role           string
}

// DatasourceDiagnosticsConfigView corresponds to DatasourceDiagnosticsConfig.
type DatasourceDiagnosticsConfigView struct {
	DatasourceID             string     `json:"datasource_id"`
	Enabled                  bool       `json:"enabled"`
	IntervalSeconds          int        `json:"interval_seconds"`
	EffectiveIntervalSeconds int        `json:"effective_interval_seconds"`
	TopN                     int        `json:"top_n"`
	State                    string     `json:"state"`
	LastProbeState           string     `json:"last_probe_state"`
	LastProbeAt              *time.Time `json:"last_probe_at"`
	LastCollectedAt          *time.Time `json:"last_collected_at"`
	ConsecutiveFailures      int        `json:"consecutive_failures"`
	PausedReason             *string    `json:"paused_reason"`
	Version                  int64      `json:"version"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// UpdateDiagnosticsConfigInput corresponds to UpdateDatasourceDiagnosticsConfigRequest.
type UpdateDiagnosticsConfigInput struct {
	Enabled         bool
	IntervalSeconds int
	TopN            int
	ResumeFromPause bool
}

// DiagnosticDatasourceSummaryView corresponds to DiagnosticDatasourceSummary.
type DiagnosticDatasourceSummaryView struct {
	ID                       string     `json:"id"`
	Name                     string     `json:"name"`
	Environment              string     `json:"environment"`
	Engine                   string     `json:"engine"`
	CompatibilityMode        string     `json:"compatibility_mode"`
	Host                     string     `json:"host"`
	Port                     int        `json:"port"`
	DatabaseName             *string    `json:"database_name"`
	DiagnosticsEnabled       bool       `json:"diagnostics_enabled"`
	State                    string     `json:"state"`
	ProbeState               string     `json:"probe_state"`
	IntervalSeconds          int        `json:"interval_seconds"`
	EffectiveIntervalSeconds int        `json:"effective_interval_seconds"`
	TopN                     int        `json:"top_n"`
	LastCollectedAt          *time.Time `json:"last_collected_at"`
}

// DiagnosticsSnapshotSummaryView corresponds to DiagnosticsSnapshotSummary.
type DiagnosticsSnapshotSummaryView struct {
	SnapshotID     string    `json:"snapshot_id"`
	DatasourceID   string    `json:"datasource_id"`
	TriggerKind    string    `json:"trigger_kind"`
	ProbeState     string    `json:"probe_state"`
	StatementCount int       `json:"statement_count"`
	DurationMS     int       `json:"duration_ms"`
	ResetDetected  bool      `json:"reset_detected"`
	GapDetected    bool      `json:"gap_detected"`
	SampledAt      time.Time `json:"sampled_at"`
}

// DiagnosticsLoadBucketPointView corresponds to DiagnosticsLoadBucketPoint (Chart 1: SQL load trend).
type DiagnosticsLoadBucketPointView struct {
	BucketStart     time.Time `json:"bucket_start"`
	BucketEnd       time.Time `json:"bucket_end"`
	TotalExecTimeMS float64   `json:"total_exec_time_ms"`
	Calls           int64     `json:"calls"`
	AvgExecTimeMS   float64   `json:"avg_exec_time_ms"`
	ResetDetected   bool      `json:"reset_detected"`
}

// DiagnosticsTopStatementItemView corresponds to DiagnosticsTopStatementItem (Chart 2: Top 10 SQL hotspots).
type DiagnosticsTopStatementItemView struct {
	StatementID       string  `json:"statement_id"`
	EngineFingerprint string  `json:"engine_fingerprint"`
	DatabaseOrSchema  string  `json:"database_or_schema"`
	TemplatePreview   *string `json:"template_preview"`
	Calls             int64   `json:"calls"`
	TotalExecTimeMS   float64 `json:"total_exec_time_ms"`
	AvgExecTimeMS     float64 `json:"avg_exec_time_ms"`
	ShareOfTotalTime  float64 `json:"share_of_total_time"`
}

// DiagnosticsScatterPointView corresponds to DiagnosticsScatterPoint (Chart 3: calls vs. average latency scatter plot).
type DiagnosticsScatterPointView struct {
	StatementID                  string  `json:"statement_id"`
	EngineFingerprint            string  `json:"engine_fingerprint"`
	DatabaseOrSchema             string  `json:"database_or_schema"`
	Calls                        int64   `json:"calls"`
	AvgExecTimeMS                float64 `json:"avg_exec_time_ms"`
	TotalExecTimeMS              float64 `json:"total_exec_time_ms"`
	RowsExaminedOrSharedBlksRead int64   `json:"rows_examined_or_shared_blks_read"`
}

// DiagnosticsHeatmapBucketCellView corresponds to DiagnosticsHeatmapBucketCell.
type DiagnosticsHeatmapBucketCellView struct {
	BucketStart         time.Time `json:"bucket_start"`
	TotalExecTimeMS     float64   `json:"total_exec_time_ms"`
	Calls               int64     `json:"calls"`
	UnobservedBelowTopN bool      `json:"unobserved_below_top_n"`
}

// DiagnosticsHeatmapSeriesView corresponds to DiagnosticsHeatmapSeries (Chart 4: SQL hotspot heatmap).
type DiagnosticsHeatmapSeriesView struct {
	StatementID       string                             `json:"statement_id"`
	EngineFingerprint string                             `json:"engine_fingerprint"`
	DatabaseOrSchema  string                             `json:"database_or_schema"`
	Buckets           []DiagnosticsHeatmapBucketCellView `json:"buckets"`
}

// DiagnosticsRegressionItemView corresponds to DiagnosticsRegressionItem (Chart 5: performance regression ranking).
type DiagnosticsRegressionItemView struct {
	StatementID         string   `json:"statement_id"`
	EngineFingerprint   string   `json:"engine_fingerprint"`
	DatabaseOrSchema    string   `json:"database_or_schema"`
	BaselineCalls       int64    `json:"baseline_calls"`
	BaselineAvgMS       float64  `json:"baseline_avg_ms"`
	CurrentCalls        int64    `json:"current_calls"`
	CurrentAvgMS        float64  `json:"current_avg_ms"`
	DeltaAvgMS          float64  `json:"delta_avg_ms"`
	RegressionRatio     *float64 `json:"regression_ratio"`
	InsufficientSamples bool     `json:"insufficient_samples"`
}

// DiagnosticsNewHighLoadItemView corresponds to DiagnosticsNewHighLoadItem (Chart 6: newly observed high-load SQL).
type DiagnosticsNewHighLoadItemView struct {
	StatementID       string  `json:"statement_id"`
	EngineFingerprint string  `json:"engine_fingerprint"`
	DatabaseOrSchema  string  `json:"database_or_schema"`
	CurrentCalls      int64   `json:"current_calls"`
	CurrentTotalMS    float64 `json:"current_total_ms"`
	CurrentAvgMS      float64 `json:"current_avg_ms"`
	UnobservedNote    string  `json:"unobserved_note"`
}

// DiagnosticsStatementRowView corresponds to DiagnosticsStatementRow (sortable SQL statement table).
type DiagnosticsStatementRowView struct {
	StatementID                  string    `json:"statement_id"`
	EngineFingerprint            string    `json:"engine_fingerprint"`
	YearningFingerprint          *string   `json:"yearning_fingerprint"`
	DatabaseOrSchema             string    `json:"database_or_schema"`
	TemplateStatus               string    `json:"template_status"`
	TemplatePreview              *string   `json:"template_preview"`
	Calls                        int64     `json:"calls"`
	TotalExecTimeMS              float64   `json:"total_exec_time_ms"`
	AvgExecTimeMS                float64   `json:"avg_exec_time_ms"`
	RowsExaminedOrSharedBlksRead int64     `json:"rows_examined_or_shared_blks_read"`
	RowsSentOrRowsReturned       int64     `json:"rows_sent_or_rows_returned"`
	LockTimeMSOrTempBlksWritten  float64   `json:"lock_time_ms_or_temp_blks_written"`
	ShareOfTotalTime             float64   `json:"share_of_total_time"`
	CoverageMode                 string    `json:"coverage_mode"`
	FirstSeenAt                  time.Time `json:"first_seen_at"`
	LastSeenAt                   time.Time `json:"last_seen_at"`
}

// DiagnosticsOverviewView corresponds to DiagnosticsOverview.
type DiagnosticsOverviewView struct {
	DatasourceID      string                            `json:"datasource_id"`
	Engine            string                            `json:"engine"`
	CompatibilityMode string                            `json:"compatibility_mode"`
	ProbeState        string                            `json:"probe_state"`
	State             string                            `json:"state"`
	CoverageMode      string                            `json:"coverage_mode"`
	From              time.Time                         `json:"from"`
	To                time.Time                         `json:"to"`
	CompareFrom       *time.Time                        `json:"compare_from"`
	CompareTo         *time.Time                        `json:"compare_to"`
	LastCollectedAt   *time.Time                        `json:"last_collected_at"`
	SnapshotCount     int                               `json:"snapshot_count"`
	LoadTrend         []DiagnosticsLoadBucketPointView  `json:"load_trend"`
	TopStatements     []DiagnosticsTopStatementItemView `json:"top_statements"`
	ScatterPoints     []DiagnosticsScatterPointView     `json:"scatter_points"`
	Heatmap           []DiagnosticsHeatmapSeriesView    `json:"heatmap"`
	Regressions       []DiagnosticsRegressionItemView   `json:"regressions"`
	NewHighLoad       []DiagnosticsNewHighLoadItemView  `json:"new_high_load"`
	Statements        []DiagnosticsStatementRowView     `json:"statements"`
}

// DiagnosticsStatementTrendPointView corresponds to DiagnosticsStatementTrendPoint (Detail Chart 1: average latency and call count trend).
type DiagnosticsStatementTrendPointView struct {
	BucketStart         time.Time `json:"bucket_start"`
	BucketEnd           time.Time `json:"bucket_end"`
	Calls               int64     `json:"calls"`
	AvgExecTimeMS       float64   `json:"avg_exec_time_ms"`
	TotalExecTimeMS     float64   `json:"total_exec_time_ms"`
	ResetDetected       bool      `json:"reset_detected"`
	UnobservedBelowTopN bool      `json:"unobserved_below_top_n"`
}

// DiagnosticsStatementCostPointView corresponds to DiagnosticsStatementCostPoint (Detail Chart 2: execution cost trend).
type DiagnosticsStatementCostPointView struct {
	BucketStart                  time.Time `json:"bucket_start"`
	BucketEnd                    time.Time `json:"bucket_end"`
	RowsExaminedOrSharedBlksRead int64     `json:"rows_examined_or_shared_blks_read"`
	RowsSentOrRowsReturned       int64     `json:"rows_sent_or_rows_returned"`
	LockTimeMSOrTempBlksWritten  float64   `json:"lock_time_ms_or_temp_blks_written"`
	SharedBlksHit                *int64    `json:"shared_blks_hit"`
}

// DiagnosticsWindowMetricsView corresponds to DiagnosticsWindowMetrics.
type DiagnosticsWindowMetricsView struct {
	From            time.Time `json:"from"`
	To              time.Time `json:"to"`
	BucketCount     int       `json:"bucket_count"`
	Calls           int64     `json:"calls"`
	TotalExecTimeMS float64   `json:"total_exec_time_ms"`
	AvgExecTimeMS   float64   `json:"avg_exec_time_ms"`
}

// DiagnosticsCandidateOrderItemView corresponds to DiagnosticsCandidateOrderItem.
type DiagnosticsCandidateOrderItemView struct {
	ChangeOrderID   string    `json:"change_order_id"`
	DisplayNo       string    `json:"display_no"`
	Title           string    `json:"title"`
	State           string    `json:"state"`
	ExecutedAt      time.Time `json:"executed_at"`
	RelationHint    string    `json:"relation_hint"`
	ConfirmedLinkID *string   `json:"confirmed_link_id"`
}

// DiagnosticsOrderComparisonView corresponds to DiagnosticsOrderComparison (Detail Chart 3: before/after optimization comparison).
type DiagnosticsOrderComparisonView struct {
	LinkedChangeOrderID     *string                             `json:"linked_change_order_id"`
	LinkedOrderDisplayNo    *string                             `json:"linked_order_display_no"`
	RelationKind            *string                             `json:"relation_kind"`
	ReferenceExecutedAt     *time.Time                          `json:"reference_executed_at"`
	InsufficientSamples     bool                                `json:"insufficient_samples"`
	BeforeWindow            *DiagnosticsWindowMetricsView       `json:"before_window"`
	AfterWindow             *DiagnosticsWindowMetricsView       `json:"after_window"`
	CandidateOrdersInWindow []DiagnosticsCandidateOrderItemView `json:"candidate_orders_in_window"`
}

// DiagnosticOrderLinkView corresponds to DiagnosticOrderLink.
type DiagnosticOrderLinkView struct {
	ID                string     `json:"id"`
	DatasourceID      string     `json:"datasource_id"`
	StatementID       string     `json:"statement_id"`
	TargetStatementID *string    `json:"target_statement_id"`
	ChangeOrderID     string     `json:"change_order_id"`
	OrderDisplayNo    string     `json:"order_display_no"`
	RelationKind      string     `json:"relation_kind"`
	OrderExecutedAt   *time.Time `json:"order_executed_at"`
	ConfirmedByUserID string     `json:"confirmed_by_user_id"`
	CreatedAt         time.Time  `json:"created_at"`
}

// DiagnosticAIObservedFactView corresponds to DiagnosticAIObservedFact.
type DiagnosticAIObservedFactView struct {
	Metric string `json:"metric"`
	Value  string `json:"value"`
	Window string `json:"window"`
}

// DiagnosticAIRootCauseHypothesisView corresponds to DiagnosticAIRootCauseHypothesis.
type DiagnosticAIRootCauseHypothesisView struct {
	Hypothesis    string `json:"hypothesis"`
	Confidence    string `json:"confidence"`
	EvidenceBasis string `json:"evidence_basis"`
}

// DiagnosticAIOptimizationRecommendationView corresponds to DiagnosticAIOptimizationRecommendation.
type DiagnosticAIOptimizationRecommendationView struct {
	Kind                 string   `json:"kind"`
	Title                string   `json:"title"`
	Rationale            string   `json:"rationale"`
	ApplicableConditions []string `json:"applicable_conditions"`
	CandidateSQL         *string  `json:"candidate_sql"`
	RiskNotes            []string `json:"risk_notes"`
}

// DiagnosticAIReportView corresponds to DiagnosticAIReport (four-part structured AI diagnosis report).
type DiagnosticAIReportView struct {
	ReportID                    string                                       `json:"report_id"`
	DatasourceID                string                                       `json:"datasource_id"`
	StatementID                 string                                       `json:"statement_id"`
	WindowStart                 time.Time                                    `json:"window_start"`
	WindowEnd                   time.Time                                    `json:"window_end"`
	CoverageMode                string                                       `json:"coverage_mode"`
	DesensitizedContextSummary  string                                       `json:"desensitized_context_summary"`
	ObservedFacts               []DiagnosticAIObservedFactView               `json:"observed_facts"`
	RootCauseHypotheses         []DiagnosticAIRootCauseHypothesisView        `json:"root_cause_hypotheses"`
	OptimizationRecommendations []DiagnosticAIOptimizationRecommendationView `json:"optimization_recommendations"`
	VerificationSteps           []string                                     `json:"verification_steps"`
	CandidateSQL                *string                                      `json:"candidate_sql"`
	CandidateStatementKind      *string                                      `json:"candidate_statement_kind"`
	SubmissionState             string                                       `json:"submission_state"`
	FlowID                      *string                                      `json:"flow_id"`
	EphemeralRunID              *string                                      `json:"ephemeral_run_id"`
	SubmittedOrderID            *string                                      `json:"submitted_order_id"`
	SubmittedOrderDisplayNo     *string                                      `json:"submitted_order_display_no"`
	CreatedAt                   time.Time                                    `json:"created_at"`
}

// DiagnosticStatementDetailView corresponds to DiagnosticStatementDetail.
type DiagnosticStatementDetailView struct {
	StatementID         string                               `json:"statement_id"`
	DatasourceID        string                               `json:"datasource_id"`
	Engine              string                               `json:"engine"`
	CompatibilityMode   string                               `json:"compatibility_mode"`
	EngineFingerprint   string                               `json:"engine_fingerprint"`
	YearningFingerprint *string                              `json:"yearning_fingerprint"`
	DatabaseOrSchema    string                               `json:"database_or_schema"`
	TemplateStatus      string                               `json:"template_status"`
	TemplateSQL         *string                              `json:"template_sql"`
	TemplateNote        *string                              `json:"template_note"`
	CoverageMode        string                               `json:"coverage_mode"`
	From                time.Time                            `json:"from"`
	To                  time.Time                            `json:"to"`
	Summary             DiagnosticsStatementRowView          `json:"summary"`
	CostUnitNotes       map[string]string                    `json:"cost_unit_notes"`
	TrendSeries         []DiagnosticsStatementTrendPointView `json:"trend_series"`
	CostSeries          []DiagnosticsStatementCostPointView  `json:"cost_series"`
	OrderComparison     DiagnosticsOrderComparisonView       `json:"order_comparison"`
	LatestAIReport      *DiagnosticAIReportView              `json:"latest_ai_report"`
}
