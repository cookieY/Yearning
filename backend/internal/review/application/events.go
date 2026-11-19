// events.go Review-domain event emission and Run invalidation.
// Events and Outbox rows are written atomically within the command transaction (repository-commands
// business_outbox_atomicity: required).
package application

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// asMap converts an event payload struct into a map via deterministic JSON (input to Recorder.Emit).
func asMap(v any) (map[string]any, error) {
	raw, err := jsonMarshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// emitDraftEvent writes a change_draft domain event (subject=change-drafts/{id}).
func emitDraftEvent(ctx context.Context, tx *gorm.DB, rec *auditlog.Recorder,
	internalType, draftID, actorUserID string, data any) error {
	m, err := asMap(data)
	if err != nil {
		return err
	}
	return rec.Emit(ctx, tx, internalType, domain.EventSubject("change-drafts", draftID),
		auditlog.Actor{Kind: "user", UserID: actorUserID}, m)
}

// emitRunEvent writes a review_run domain event (subject=review-runs/{id}).
func emitRunEvent(ctx context.Context, tx *gorm.DB, rec *auditlog.Recorder,
	internalType, runID string, actor auditlog.Actor, data any) error {
	m, err := asMap(data)
	if err != nil {
		return err
	}
	return rec.Emit(ctx, tx, internalType, domain.EventSubject("review-runs", runID), actor, m)
}

// invalidateActiveRuns invalidates all non-outdated Runs bound to oldRevision or earlier to outdated
// (inputs_changed). Terminal conclusions cannot be reused for a new Revision.
// Invalidation does not emit a separate external event (in the routing table, run invalidation reason is carried by draft revised).
func invalidateActiveRuns(ctx context.Context, tx *gorm.DB, draftID string, oldRevision int64) error {
	var rows []persistence.ReviewRunModel
	if err := tx.WithContext(ctx).
		Where("draft_id = ? and draft_revision <= ? and state <> ?", draftID, oldRevision, string(domain.RunStateOutdated)).
		Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		run := &rows[i]
		next, err := domain.RunTransition(domain.RunState(run.State), domain.RunEventInputsChanged)
		if err != nil {
			// An invalid transition indicates persisted state is inconsistent with the state machine; fail closed.
			return domain.ErrIllegalTransition
		}
		sets := map[string]any{
			"state":       string(next),
			"version":     run.Version + 1,
			"finished_at": nowUTC(),
		}
		if next == domain.RunStateOutdated && run.State != string(domain.RunStateQueued) && run.State != string(domain.RunStateRunning) {
			// Failure fields from failed/blocked/partial must be cleared under outdated
			// (review_run_failure_fields CHECK).
			sets["business_err_code"] = nil
			sets["failure_name"] = nil
		}
		res := tx.WithContext(ctx).Model(&persistence.ReviewRunModel{}).
			Where("id = ? and version = ?", run.ID, run.Version).
			Updates(sets)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConcurrent
		}
	}
	return nil
}
