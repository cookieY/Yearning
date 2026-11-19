// inputinvalidate.go Review input (Prompt Tool / internal knowledge) Hash change ->
// unsubmitted Reviews referencing the old Hash are immediately marked outdated (PRD 9.3.1/9.3.2).
//
// Reference record: review_tool_calls(tool_kind, input_ref_id, config_hash)
// is written by the Runner at injection time (run-scoped, immutable once the Run starts).
package application

import (
	"context"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

// InvalidateInputRefs invalidates unsubmitted Runs referencing (inputRefID, oldConfigHash)
// to outdated (idempotent: filtered by state <> outdated).
func InvalidateInputRefs(ctx context.Context, tx *gorm.DB, toolKind, inputRefID, oldConfigHash string) error {
	var rows []struct {
		ID      string
		State   string
		Version int64
	}
	err := tx.WithContext(ctx).Raw(`
		select r.id::text as id, r.state, r.version
		from review_runs r
		join change_drafts d on d.id = r.draft_id
		join review_tool_calls c on c.review_run_id = r.id
		where c.tool_kind = ? and c.input_ref_id = ? and c.config_hash = ?
		  and r.state <> 'outdated'
		  and d.state <> 'submitted'`, toolKind, inputRefID, oldConfigHash).Scan(&rows).Error
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		next, err := domain.RunTransition(domain.RunState(r.State), domain.RunEventInputsChanged)
		if err != nil {
			return domain.ErrIllegalTransition
		}
		res := tx.WithContext(ctx).Exec(`
			update review_runs set state = ?, version = version + 1, finished_at = ?,
				business_err_code = null, failure_name = null
			where id = ? and version = ?`, string(next), nowUTC(), r.ID, r.Version)
		if res.Error != nil {
			return res.Error
		}
	}
	return nil
}

// InputRefsExist reports whether an input is referenced by any Run (deletion guard).
func InputRefsExist(ctx context.Context, db *gorm.DB, toolKind, inputRefID string) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Raw(`
		select count(*) from review_tool_calls where tool_kind = ? and input_ref_id = ?`,
		toolKind, inputRefID).Scan(&n).Error
	return n > 0, err
}
