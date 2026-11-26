// read.go Execution read surface: Attempt view (OpenAPI ExecutionAttempt) and
// statement ledger pagination (ExecutionStatement). Visibility = work order relevant actors
// (submitter / frozen approver / frozen executor; admins have no implicit read access).
package application

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/platform/pagination"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
)

// OscDetailsView maps to OscExecutionDetails.
type OscDetailsView struct {
	Tool            string     `json:"tool"`
	ToolVersion     string     `json:"tool_version"`
	BinarySHA256    string     `json:"binary_sha256"`
	PlanHash        string     `json:"plan_hash"`
	Phase           string     `json:"phase"`
	ProgressBps     int        `json:"progress_basis_points"`
	RowsCopied      int64      `json:"rows_copied"`
	ResidualState   string     `json:"residual_state"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
}

// AttemptView maps to the ExecutionAttempt schema.
type AttemptView struct {
	ID            string          `json:"id"`
	StageID       string          `json:"stage_id"`
	ExecutionKind string          `json:"execution_kind"`
	State         string          `json:"state"`
	SendBoundary  string          `json:"send_boundary"`
	Osc           *OscDetailsView `json:"osc"`
	Version       int64           `json:"version"`
	CreatedAt     time.Time       `json:"created_at"`
}

// StatementView maps to the ExecutionStatement schema.
type StatementView struct {
	ID               string     `json:"id"`
	Ordinal          int32      `json:"ordinal"`
	StatementKind    string     `json:"statement_kind"`
	State            string     `json:"state"`
	AffectedRowCount *int64     `json:"affected_row_count"`
	FailureName      *string    `json:"failure_name"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

// loadAttempt reads an Attempt row (unlocked).
func (s *Services) loadAttempt(ctx context.Context, db *gorm.DB, attemptID string) (*persistence.ExecutionAttemptModel, error) {
	var at persistence.ExecutionAttemptModel
	if err := db.WithContext(ctx).Where("id = ?", attemptID).First(&at).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	return &at, nil
}

// CanReadAttemptAsExecutor reports whether userID is an executor of the Attempt:
// either matching the initiated_by snapshot or being a member of the frozen executor snapshot
// in order_execution_actors for the Attempt's stage. The stage_execution branch of getTask uses
// this check to allow executors while restricting builtin admin (RCP-20261021-GETTASK-EXECUTOR-READ /
// SEC-0059): an admin outside this business relation is rejected just like a regular user;
// progress visibility for submitters/approvers continues to use the CanReadAttempt read surface
// without being broadened by this check.
func CanReadAttemptAsExecutor(ctx context.Context, db *gorm.DB, attemptID, userID string) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Raw(`
		select count(*) from execution_attempts ea
		left join order_execution_actors e on e.stage_id = ea.stage_id
		where ea.id = ? and (
			ea.initiated_by_user_id_snapshot = ?
			or e.executor_user_id_snapshot = ?
		)`, attemptID, userID, userID).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CanReadAttempt reports whether userID has a frozen relation to the Attempt's work order.
func (s *Services) CanReadAttempt(ctx context.Context, attemptID, userID string) (bool, error) {
	var orderID string
	if err := s.DB.WithContext(ctx).Raw(
		`select order_id::text from execution_attempts where id = ?`, attemptID).
		Scan(&orderID).Error; err != nil {
		return false, err
	}
	if orderID == "" {
		return false, domain.ErrNotFound()
	}
	var n int64
	err := s.DB.WithContext(ctx).Raw(`
		select (
			(select count(*) from change_orders o
			 where o.id = ? and o.submitter_user_id_snapshot = ?)
			+ (select count(*) from order_approval_actors a
			 join order_approval_steps st on st.id = a.step_id
			 join order_stages os on os.id = st.stage_id
			 where os.order_id = ? and a.reviewer_user_id_snapshot = ?)
			+ (select count(*) from order_execution_actors e
			 join order_stages os on os.id = e.stage_id
			 where os.order_id = ? and e.executor_user_id_snapshot = ?)
		)`, orderID, userID, orderID, userID, orderID, userID).Scan(&n).Error
	return n > 0, err
}

// buildAttemptView assembles the AttemptView.
func (s *Services) buildAttemptView(ctx context.Context, db *gorm.DB, attemptID string) (*AttemptView, error) {
	at, err := s.loadAttempt(ctx, db, attemptID)
	if err != nil {
		return nil, err
	}
	v := &AttemptView{
		ID: at.ID, StageID: at.StageID, ExecutionKind: at.ExecutionKind,
		State: at.State, SendBoundary: at.SendBoundary,
		Version: at.Version, CreatedAt: at.CreatedAt,
	}
	if domain.ExecutionKind(at.ExecutionKind).IsOscKind() {
		tool := "gh-ost"
		if domain.ExecutionKind(at.ExecutionKind) == domain.KindPtOsc {
			tool = "pt-osc"
		}
		v.Osc = &OscDetailsView{
			Tool: tool, ToolVersion: ptr.Deref(at.OSCToolVersion),
			BinarySHA256: ptr.Deref(at.OSCBinarySHA256), PlanHash: ptr.Deref(at.OSCPlanHash),
			Phase:       ptr.Deref(at.OSCPhase),
			ProgressBps: derefInt(at.OSCProgressBasisPoints), RowsCopied: derefInt64(at.OSCRowsCopied),
			ResidualState: ptr.Deref(at.OSCResidualState), LastHeartbeatAt: at.OSCLastHeartbeatAt,
		}
	}
	return v, nil
}

// GetAttempt reads the Attempt view (visibility is verified beforehand by the caller).
func (s *Services) GetAttempt(ctx context.Context, attemptID string) (*AttemptView, error) {
	return s.buildAttemptView(ctx, s.DB, attemptID)
}

// ListStatements paginates the statement ledger (after = last ordinal of previous page; default limit = 50).
func (s *Services) ListStatements(ctx context.Context, attemptID string, after string, limit int) ([]StatementView, string, bool, error) {
	limit = pagination.ClampLimit(limit, 50, 200)
	afterOrdinal := 0
	if after != "" {
		v, err := strconv.Atoi(after)
		if err != nil || v < 0 {
			return nil, "", false, domain.ErrValidation()
		}
		afterOrdinal = v
	}
	var rows []persistence.ExecutionStatementModel
	if err := s.DB.WithContext(ctx).Where(
		"attempt_id = ? and ordinal > ?", attemptID, afterOrdinal).
		Order("ordinal").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]StatementView, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		items = append(items, StatementView{
			ID: r.ID, Ordinal: r.Ordinal, StatementKind: r.StatementKind, State: r.State,
			AffectedRowCount: r.AffectedRowCount, FailureName: r.FailureName,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		})
	}
	nextAfter := ""
	if hasMore && len(rows) > 0 {
		nextAfter = strconv.Itoa(int(rows[len(rows)-1].Ordinal))
	}
	return items, nextAfter, hasMore, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
