// Package dashboard implements the S005 dashboard domain (PRD11): personal workbench counters,
// operations/system-health Dashboards, and announcement revisions/publications. All metrics come from
// bounded real-time aggregation queries windowed by submitted_at without full-table scans; management
// metrics exclude SQL text and sensitive object names. The dashboard_daily_metrics table and its
// DailyMetric projection are reserved for future daily rollups and have no writers or readers as of
// 2026-10-23 (see docs/features/11-dashboard-prd.md §5).
package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// Service is the dashboard domain service.
type Service struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter
	Now     func() time.Time
	// ReleaseHTTPClient is the HTTP client for fetching release snapshots; nil uses the package default (with timeout).
	// Tests inject a custom Transport here.
	ReleaseHTTPClient *http.Client
}

// New constructs a Service.
func New(db *gorm.DB, crypter *crypto.Crypter) *Service {
	return &Service{DB: db, Crypter: crypter, Now: func() time.Time { return time.Now().UTC() }}
}

// MyDashboardView conforms to OpenAPI MyDashboard (counts scoped to the caller's own authorization).
type MyDashboardView struct {
	DraftCount              int    `json:"draft_count"`
	SubmittedOrderCount     int    `json:"submitted_order_count"`
	PendingApprovalCount    int    `json:"pending_approval_count"`
	PendingExecutionCount   int    `json:"pending_execution_count"`
	BlockedReviewCount      int    `json:"blocked_review_count"`
	ActiveQueryGrantCount   int    `json:"active_query_grant_count"`
	ActiveQuerySessionCount int    `json:"active_query_session_count"`
	RefreshedAt             string `json:"refreshed_at"`
}

// MyDashboard returns the user's personal workbench (bounded COUNT queries scoped to the caller's relationships).
func (s *Service) MyDashboard(ctx context.Context, userID string) (*MyDashboardView, error) {
	v := &MyDashboardView{RefreshedAt: s.Now().Format(time.RFC3339Nano)}
	queries := []struct {
		dest *int
		sql  string
	}{
		{&v.DraftCount, `select count(*) from change_drafts where owner_user_id = ? and state in ('draft','ready')`},
		{&v.SubmittedOrderCount, `select count(*) from change_orders where submitter_user_id_snapshot = ? and state in ('submitted','stage_approval_active','stage_execution_pending','scheduled','blocked_datasource_unavailable')`},
		{&v.PendingApprovalCount, `select (
			(select count(*) from order_approval_actors a join order_approval_steps st on st.id = a.step_id join order_stages s on s.id = st.stage_id join change_orders o on o.id = s.order_id where a.reviewer_user_id_snapshot = ? and st.state = 'active')
			+
			(select count(*) from query_access_approval_actors qa join query_access_approval_steps qst on qst.id = qa.step_id join query_access_requests qr on qr.id = qst.request_id where qa.reviewer_user_id_snapshot = ? and qst.state = 'active' and qr.state = 'access_pending')
		)`},
		{&v.PendingExecutionCount, `select count(*) from order_execution_actors ea join order_stages st on st.id = ea.stage_id join change_orders o on o.id = st.order_id where ea.executor_user_id_snapshot = ? and o.state in ('stage_execution_pending','scheduled') and st.state = 'execution_pending'`},
		{&v.ActiveQueryGrantCount, `select count(*) from query_grants where requester_user_id_snapshot = ? and state = 'active' and expires_at > now()`},
		{&v.ActiveQuerySessionCount, `select count(*) from query_sessions where user_id_snapshot = ? and state = 'active'`},
	}
	for _, q := range queries {
		args := []any{userID}
		if q.dest == &v.PendingApprovalCount {
			args = []any{userID, userID}
		}
		if err := s.DB.WithContext(ctx).Raw(q.sql, args...).Scan(q.dest).Error; err != nil {
			return nil, err
		}
	}
	// Blocked reviews = count of blocked runs on the caller's own drafts (bounded index scan).
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from review_runs r join change_drafts d on d.id = r.draft_id
		where d.owner_user_id = ? and r.state = 'blocked'`, userID).Scan(&v.BlockedReviewCount).Error; err != nil {
		return nil, err
	}
	return v, nil
}

// DailyMetric projects a row from dashboard_daily_metrics. This table is a reserved daily rollup target
// with no writers or readers as of 2026-10-23; read surfaces use bounded real-time aggregation (see package doc).
type DailyMetric struct {
	MetricDay string          `gorm:"column:metric_day"`
	MetricKey string          `gorm:"column:metric_key"`
	ScopeKind string          `gorm:"column:scope_kind"`
	ScopeID   *string         `gorm:"column:scope_id"`
	Dimension string          `gorm:"column:dimension_key"`
	Payload   json.RawMessage `gorm:"column:metric_payload"`
	Version   int64           `gorm:"column:version"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
}

func (DailyMetric) TableName() string { return "dashboard_daily_metrics" }

// OperationsView conforms to OpenAPI OperationsDashboard (window_days <= 90).
type OperationsView struct {
	Meta                  SnapshotMeta `json:"meta"`
	ChangeOrderTotal      int64        `json:"change_order_total"`
	DdlStatementTotal     int64        `json:"ddl_statement_total"`
	DmlStatementTotal     int64        `json:"dml_statement_total"`
	CompletedTotal        int64        `json:"completed_total"`
	FailedTotal           int64        `json:"failed_total"`
	PartialFailedTotal    int64        `json:"partial_failed_total"`
	QueryExecutionTotal   int64        `json:"query_execution_total"`
	UserTotal             int64        `json:"user_total"`
	DatasourceTotal       int64        `json:"datasource_total"`
	ApprovalDurationP50MS int64        `json:"approval_duration_p50_ms"`
	ApprovalDurationP95MS int64        `json:"approval_duration_p95_ms"`
	OrderTrend            []TrendPoint `json:"order_trend"`
	// StateDistribution holds change order counts grouped by current state within the window, with keys
	// fixed to all 18 enum values of ChangeOrderState (RCP-20261023-HOME-METRICS-ALIGNMENT).
	StateDistribution StateDistribution `json:"state_distribution"`
}

// ChangeOrderStates is the frozen enum of change_orders.state (OpenAPI
// ChangeOrderState). Distribution keys match it verbatim in the same order.
var ChangeOrderStates = []string{
	"submitted",
	"stage_approval_active",
	"stage_execution_pending",
	"scheduled",
	"running",
	"completed",
	"rejected",
	"withdrawn",
	"withdrawn_after_partial_execution",
	"voided",
	"failed",
	"partial_failed",
	"cancelled",
	"partial_cancelled",
	"result_unknown",
	"blocked_datasource_unavailable",
	"missed_schedule",
	"invalid",
}

// StateDistribution conforms to OpenAPI ChangeOrderStateDistribution:
// all 18 keys are always present (0 when no orders are in that state),
// and the sum of all keys always equals change_order_total for the same window.
type StateDistribution map[string]int64

// NewStateDistribution returns a distribution pre-populated with 18 zeroes to guarantee a stable JSON key set.
func NewStateDistribution() StateDistribution {
	d := make(StateDistribution, len(ChangeOrderStates))
	for _, state := range ChangeOrderStates {
		d[state] = 0
	}
	return d
}

// StateDistributionFromRows merges GROUP BY results into the full 18-key distribution.
// Keys outside the enum are discarded (guarded by a table CHECK constraint and absent in normal operation),
// while missing keys default to 0; the sum of returned values equals the sum of valid input rows.
func StateDistributionFromRows(rows map[string]int64) StateDistribution {
	d := NewStateDistribution()
	for state, count := range rows {
		if _, ok := d[state]; !ok {
			continue
		}
		d[state] = count
	}
	return d
}

// SnapshotMeta conforms to OpenAPI DashboardSnapshotMeta.
type SnapshotMeta struct {
	WindowStart           string   `json:"window_start"`
	WindowEnd             string   `json:"window_end"`
	SystemTimezone        string   `json:"system_timezone"`
	RefreshedAt           string   `json:"refreshed_at"`
	Completeness          string   `json:"completeness"`
	UnavailableMetricKeys []string `json:"unavailable_metric_keys"`
}

// TrendPoint represents a daily trend data point.
type TrendPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// OperationsDashboard computes operational statistics (bounded aggregation; timezone from system settings).
func (s *Service) OperationsDashboard(ctx context.Context, windowDays int) (*OperationsView, error) {
	if windowDays <= 0 || windowDays > 90 {
		windowDays = 30
	}
	v := &OperationsView{OrderTrend: []TrendPoint{}, StateDistribution: NewStateDistribution()}
	tz := s.systemTimezone(ctx)
	now := s.Now()
	v.Meta = SnapshotMeta{
		WindowStart:    now.AddDate(0, 0, -windowDays).Format("2006-01-02"),
		WindowEnd:      now.Format("2006-01-02"),
		SystemTimezone: tz,
		RefreshedAt:    now.Format(time.RFC3339Nano),
		Completeness:   "complete",
		// Schema declares a non-null array: serialize as [] rather than null when empty.
		UnavailableMetricKeys: []string{},
	}
	// Capture the window lower bound once and pass it as a parameter to every windowed query. Previously each SQL
	// statement called now() independently: due to wall-clock drift between statements, a concurrently submitted
	// order could land in change_order_total but not state_distribution (or vice versa), breaking the invariant
	// that the sum of state keys equals the total at boundary instants. A single windowStart anchors all queries to the same instant.
	windowStart := now.Add(-time.Duration(windowDays) * 24 * time.Hour)
	// Single snapshot: the 9 scalar queries, approval percentiles, trend, and state distribution below are separate
	// statements; under autocommit, orders committed between statements would cause the sum of state_distribution
	// keys to diverge from change_order_total at boundaries (PRD §4 requires exact equality). Wrapping them in a
	// single REPEATABLE READ read-only transaction ensures all statements observe the same MVCC snapshot so the
	// invariant holds under concurrency. Short read-only transaction holding no write locks.
	tx := s.DB.WithContext(ctx).Begin(&sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer func() { _ = tx.Rollback() }()
	counts := []struct {
		dest   *int64
		sql    string
		window bool
	}{
		{&v.ChangeOrderTotal, `select count(*) from change_orders where submitted_at > $1`, true},
		{&v.CompletedTotal, `select count(*) from change_orders where state = 'completed' and submitted_at > $1`, true},
		{&v.FailedTotal, `select count(*) from change_orders where state = 'failed' and submitted_at > $1`, true},
		{&v.PartialFailedTotal, `select count(*) from change_orders where state = 'partial_failed' and submitted_at > $1`, true},
		{&v.DdlStatementTotal, `select count(*) from sql_statements where statement_kind = 'ddl'`, false},
		{&v.DmlStatementTotal, `select count(*) from sql_statements where statement_kind = 'dml'`, false},
		{&v.QueryExecutionTotal, `select count(*) from query_executions`, false},
		{&v.UserTotal, `select count(*) from users`, false},
		{&v.DatasourceTotal, `select count(*) from datasources`, false},
	}
	for _, q := range counts {
		var err error
		if q.window {
			err = tx.Raw(q.sql, windowStart).Scan(q.dest).Error
		} else {
			err = tx.Raw(q.sql).Scan(q.dest).Error
		}
		if err != nil {
			return nil, err
		}
	}
	// Approval latency P50/P95 (percentile_cont bounded aggregation).
	if err := tx.Raw(`
		select coalesce(percentile_cont(0.5) within group (order by extract(epoch from (st.decided_at - o.submitted_at)) * 1000)::bigint, 0),
		       coalesce(percentile_cont(0.95) within group (order by extract(epoch from (st.decided_at - o.submitted_at)) * 1000)::bigint, 0)
		from order_approval_steps st
		join order_stages sg on sg.id = st.stage_id
		join change_orders o on o.id = sg.order_id
		where st.decided_at is not null
		  and st.decided_at > $1`, windowStart).
		Row().Scan(&v.ApprovalDurationP50MS, &v.ApprovalDurationP95MS); err != nil {
		return nil, err
	}
	// Change order daily trend counts.
	var trend []struct {
		Day string  `gorm:"column:day"`
		N   float64 `gorm:"column:n"`
	}
	if err := tx.Raw(`
		select to_char(submitted_at at time zone ?, 'YYYY-MM-DD') as day, count(*)::float8 as n
		from change_orders where submitted_at > $2
		group by 1 order by 1`, tz, windowStart).Scan(&trend).Error; err != nil {
		return nil, err
	}
	for _, t := range trend {
		v.OrderTrend = append(v.OrderTrend, TrendPoint{Day: t.Day, Value: t.N})
	}
	// State distribution (RCP-20261023-HOME-METRICS-ALIGNMENT): shares the exact same window predicate as
	// change_order_total, so the sum of all keys equals that scalar.
	var distribution []struct {
		State string `gorm:"column:state"`
		N     int64  `gorm:"column:n"`
	}
	if err := tx.Raw(`
		select state, count(*)::bigint as n
		from change_orders where submitted_at > $1
		group by state`, windowStart).Scan(&distribution).Error; err != nil {
		return nil, err
	}
	rows := make(map[string]int64, len(distribution))
	for _, row := range distribution {
		rows[row.State] = row.N
	}
	v.StateDistribution = StateDistributionFromRows(rows)
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return v, nil
}

// HealthComponent conforms to OpenAPI HealthComponent (leaking no dependency identity).
type HealthComponent struct {
	Component   string  `json:"component"`
	Status      string  `json:"status"`
	CheckedAt   string  `json:"checked_at"`
	FailureName *string `json:"failure_name"`
}

// SystemHealthView conforms to OpenAPI SystemHealthDashboard (top-level key =
// checked_at, additionalProperties:false).
type SystemHealthView struct {
	CheckedAt  string            `json:"checked_at"`
	Components []HealthComponent `json:"components"`
}

// SystemHealth probes system health (bounded component probes; failure names use a bounded enum).
func (s *Service) SystemHealth(ctx context.Context) (*SystemHealthView, error) {
	now := s.Now().Format(time.RFC3339Nano)
	v := &SystemHealthView{CheckedAt: now, Components: []HealthComponent{}}
	appendHealth := func(name string, healthy bool, failure string) {
		st := "healthy"
		var fn *string
		if !healthy {
			st = "unavailable"
			f := failure
			fn = &f
		}
		v.Components = append(v.Components, HealthComponent{
			Component: name, Status: st, CheckedAt: now, FailureName: fn})
	}
	var pgOK bool
	if err := s.DB.WithContext(ctx).Raw(`select 1`).Scan(&pgOK).Error; err == nil && pgOK {
		appendHealth("postgresql", true, "")
	} else {
		appendHealth("postgresql", false, "POSTGRESQL_UNAVAILABLE")
	}
	// Outbox backlog health (oldest unpublished > 5m = degraded; probe query error = unavailable,
	// never misreporting a failed query as healthy). Use sql.NullTime because GORM Scan dereferences
	// **time.Time before rows.Scan, which fails when min(occurred_at) is NULL on an empty backlog.
	var oldest sql.NullTime
	if err := s.DB.WithContext(ctx).Raw(
		`select min(occurred_at) from outbox_events where published_at is null`).Scan(&oldest).Error; err != nil {
		appendHealth("outbox", false, "HEALTH_PROBE_FAILED")
	} else if !oldest.Valid || s.Now().Sub(oldest.Time) < 5*time.Minute {
		appendHealth("outbox", true, "")
	} else {
		appendHealth("outbox", false, "OUTBOX_BACKLOG_STALE")
	}
	// Scheduler health (stage_execution queue lag).
	var stuck int64
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from async_tasks where kind = 'stage_execution'
		  and state = 'queued' and created_at < now() - interval '30 minutes'`).Scan(&stuck).Error; err != nil {
		appendHealth("scheduler", false, "HEALTH_PROBE_FAILED")
	} else {
		appendHealth("scheduler", stuck == 0, "TASK_QUEUE_STUCK")
	}
	return v, nil
}

func (s *Service) systemTimezone(ctx context.Context) string {
	var tz string
	_ = s.DB.WithContext(ctx).Raw(
		`select settings->>'system_timezone' from system_settings where namespace = 'general'`).Scan(&tz)
	if tz == "" {
		return "UTC"
	}
	return tz
}

// ErrNotFound / ErrConflict are stable domain errors.
var (
	ErrNotFound = fmt.Errorf("dashboard: not found")
	ErrConflict = fmt.Errorf("dashboard: version conflict")
)
