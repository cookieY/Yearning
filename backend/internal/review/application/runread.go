// runread.go Run read models and query helpers.
package application

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/pagination"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// RunView is the ReviewRun read model (OpenAPI ReviewRun schema).
type RunView struct {
	ID                    string             `json:"id"`
	DraftID               string             `json:"draft_id"`
	DraftRevision         int64              `json:"draft_revision"`
	State                 string             `json:"state"`
	StatementCount        int64              `json:"statement_count"`
	FingerprintGroupCount int64              `json:"fingerprint_group_count"`
	StageResults          []StageResultView  `json:"stage_results"`
	Gate                  domain.GatePayload `json:"gate"`
	// OBS-001 ruling: review results surface real AI identity and token usage to submitters and approvers.
	Usage       *RunUsageView `json:"usage,omitempty"`
	FailureCode *string       `json:"failure_code"`
	Version     int64         `json:"version"`
	CreatedAt   string        `json:"created_at"`
	StartedAt   *string       `json:"started_at"`
	FinishedAt  *string       `json:"finished_at"`
}

// RunUsageView aggregates the actual model identity and token usage for this Run (OBS-001).
type RunUsageView struct {
	ProviderName string `json:"provider_name"`
	ModelName    string `json:"model_name"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	AmountMinor  int64  `json:"amount_minor"`
	Currency     string `json:"currency"`
}

// StageResultView is the stage result view (ReviewStageResult schema).
type StageResultView struct {
	StagePosition   int     `json:"stage_position"`
	DatasourceID    string  `json:"datasource_id,omitempty"`
	State           string  `json:"state"`
	HighestSeverity string  `json:"highest_severity"`
	GatePassed      bool    `json:"gate_passed"`
	FindingCount    int64   `json:"finding_count"`
	EvidenceCount   int64   `json:"evidence_count"`
	SnapshotHash    string  `json:"snapshot_hash"`
	Summary         *string `json:"summary"`
	SummaryLocale   *string `json:"summary_locale"`
}

// FindingView is the finding view (ReviewFinding schema).
type FindingView struct {
	ID                 string   `json:"id"`
	StagePosition      int      `json:"stage_position"`
	FingerprintGroupID *string  `json:"fingerprint_group_id"`
	Category           string   `json:"category"`
	Severity           string   `json:"severity"`
	Title              string   `json:"title"`
	Message            string   `json:"message"`
	Suggestion         *string  `json:"suggestion"`
	ModelConfidence    *string  `json:"model_confidence"`
	EvidenceIDs        []string `json:"evidence_ids"`
}

// loadRun reads a Run row.
func (s *Services) loadRun(ctx context.Context, db *gorm.DB, runID string) (*persistence.ReviewRunModel, error) {
	var m persistence.ReviewRunModel
	if err := db.WithContext(ctx).Where("id = ?", runID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !domain.ValidRunState(domain.RunState(m.State)) {
		return nil, errEnumUnknown
	}
	return &m, nil
}

// loadRunForUpdate reads a Run row with a row lock.
func (s *Services) loadRunForUpdate(ctx context.Context, tx *gorm.DB, runID string) (*persistence.ReviewRunModel, error) {
	var m persistence.ReviewRunModel
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
		Where("id = ?", runID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !domain.ValidRunState(domain.RunState(m.State)) {
		return nil, errEnumUnknown
	}
	return &m, nil
}

// loadStageSnapshots reads stage snapshots ordered by position.
func (s *Services) loadStageSnapshots(ctx context.Context, db *gorm.DB, runID string) ([]persistence.StageReviewSnapshotModel, error) {
	var rows []persistence.StageReviewSnapshotModel
	if err := db.WithContext(ctx).Where("review_run_id = ?", runID).
		Order("stage_position").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// stagedCoverageIncomplete reports whether any stage failed to establish
// complete review coverage. Coverage takes precedence over risk findings when
// classifying the run's terminal error: a high finding is still persisted, but
// it cannot turn an incomplete review into a fully evaluated risk result.
func stagedCoverageIncomplete(rows []persistence.StageReviewSnapshotModel) bool {
	for i := range rows {
		switch domain.StageReviewState(rows[i].State) {
		case domain.StageStateFailed, domain.StageStatePartial,
			domain.StageStatePending, domain.StageStateRunning:
			return true
		}
		for _, reason := range persistence.DecodeTextArray(rows[i].GateReasonCodes) {
			if reason == "coverage_partial" || reason == "evidence_incomplete" {
				return true
			}
		}
	}
	return false
}

// runTerminalSummary aggregates stage results and gate outcomes required for events.
func (s *Services) runTerminalSummary(ctx context.Context, tx *gorm.DB, runID string) ([]domain.StageResultData, domain.GatePayload, int32, int32, error) {
	rows, err := s.loadStageSnapshots(ctx, tx, runID)
	if err != nil {
		return nil, domain.GatePayload{}, 0, 0, err
	}
	var run persistence.ReviewRunModel
	if err := tx.WithContext(ctx).Where("id = ?", runID).First(&run).Error; err != nil {
		return nil, domain.GatePayload{}, 0, 0, err
	}
	out := make([]domain.StageResultData, 0, len(rows))
	inputs := make([]domain.GateInput, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		var findingCount, evidenceCount int64
		if err := tx.WithContext(ctx).Raw(
			`select count(*) from review_findings where stage_review_id = ?`, row.ID).
			Scan(&findingCount).Error; err != nil {
			return nil, domain.GatePayload{}, 0, 0, err
		}
		if err := tx.WithContext(ctx).Raw(
			`select count(*) from evidence_facts where stage_review_id = ?`, row.ID).
			Scan(&evidenceCount).Error; err != nil {
			return nil, domain.GatePayload{}, 0, 0, err
		}
		out = append(out, domain.StageResultData{
			StagePosition: int(row.StagePosition), State: row.State,
			HighestSeverity: row.HighestSeverity,
			FindingCount:    int(findingCount), EvidenceCount: int(evidenceCount),
			SnapshotHash: row.SnapshotHash,
		})
		inputs = append(inputs, domain.GateInput{
			StagePosition: int(row.StagePosition),
			State:         domain.StageReviewState(row.State),
			HighestFound:  domain.Severity(row.HighestSeverity),
		})
	}
	gate := domain.EvaluateGate(inputs)
	return out, domain.GatePayload{Passed: gate.Passed, ReasonCodes: gate.ReasonCodes},
		run.StatementCount, run.FingerprintGroupCount, nil
}

// GetRun assembles a Run view (owner verification is performed by the caller).
func (s *Services) GetRun(ctx context.Context, db *gorm.DB, runID string) (*RunView, error) {
	run, err := s.loadRun(ctx, db, runID)
	if err != nil {
		return nil, err
	}
	return s.buildRunView(ctx, db, run)
}

// GetRunOwned reads a Run and verifies draft ownership (unrelated Runs uniformly return 1002).
func (s *Services) GetRunOwned(ctx context.Context, actor Actor, runID string, meta ...RequestMeta) (*RunView, error) {
	run, err := s.loadRun(ctx, s.DB, runID)
	if err != nil {
		return nil, err
	}
	draft, err := s.loadDraft(ctx, s.DB, run.DraftID)
	if err != nil {
		return nil, err
	}
	if draft.OwnerUserID != actor.UserID {
		return nil, ErrNotFound
	}
	view, err := s.buildRunView(ctx, s.DB, run)
	if err != nil {
		return nil, err
	}
	return s.restoreRunDisplay(ctx, actor, displayMeta(meta), view)
}

// buildRunView queries stage aggregates and assembles the view.
func (s *Services) buildRunView(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel) (*RunView, error) {
	rows, err := s.loadStageSnapshots(ctx, db, run.ID)
	if err != nil {
		return nil, err
	}
	stages := make([]StageResultView, 0, len(rows))
	inputs := make([]domain.GateInput, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		var findingCount, evidenceCount int64
		if err := db.WithContext(ctx).Raw(
			`select count(*) from review_findings where stage_review_id = ?`, row.ID).
			Scan(&findingCount).Error; err != nil {
			return nil, err
		}
		if err := db.WithContext(ctx).Raw(
			`select count(*) from evidence_facts where stage_review_id = ?`, row.ID).
			Scan(&evidenceCount).Error; err != nil {
			return nil, err
		}
		summary, summaryLocale, err := acceptedSummaryAndLocaleFromStageContext(
			s.Crypter, run.ID, row.StagePosition, crypto.Envelope{
				Ciphertext: row.NormalizedContextCiphertext,
				Nonce:      row.NormalizedContextNonce,
				KeyID:      row.NormalizedContextKeyID,
				Algorithm:  row.NormalizedContextAlgorithm,
			})
		if err != nil {
			return nil, err
		}
		stages = append(stages, StageResultView{
			StagePosition: int(row.StagePosition), DatasourceID: row.DatasourceID,
			State: row.State, HighestSeverity: row.HighestSeverity,
			GatePassed: row.GatePassed, FindingCount: findingCount,
			EvidenceCount: evidenceCount, SnapshotHash: row.SnapshotHash,
			Summary: summary, SummaryLocale: summaryLocale,
		})
		inputs = append(inputs, domain.GateInput{
			StagePosition: int(row.StagePosition),
			State:         domain.StageReviewState(row.State),
			HighestFound:  domain.Severity(row.HighestSeverity),
		})
	}
	gate := domain.EvaluateGate(inputs)
	var failure *string
	if run.FailureName != nil {
		failure = run.FailureName
	}
	usage, uerr := s.runUsageOf(ctx, db, run.ID)
	if uerr != nil {
		return nil, uerr
	}
	view := &RunView{
		ID: run.ID, DraftID: run.DraftID, DraftRevision: run.DraftRevision,
		State: run.State, StatementCount: int64(run.StatementCount),
		FingerprintGroupCount: int64(run.FingerprintGroupCount),
		StageResults:          stages,
		Gate:                  domain.GatePayload{Passed: gate.Passed, ReasonCodes: gate.ReasonCodes},
		FailureCode:           failure, Version: run.Version,
		CreatedAt: run.CreatedAt.UTC().Format(timeFormat),
	}
	if run.StartedAt != nil {
		t := run.StartedAt.UTC().Format(timeFormat)
		view.StartedAt = &t
	}
	if run.FinishedAt != nil {
		t := run.FinishedAt.UTC().Format(timeFormat)
		view.FinishedAt = &t
	}
	if usage != nil {
		view.Usage = usage
	}
	return view, nil
}

// runUsageOf aggregates model identity and token usage for this Run from the ledger (OBS-001).
func (s *Services) runUsageOf(ctx context.Context, db *gorm.DB, runID string) (*RunUsageView, error) {
	var agg struct {
		ProviderID   string
		ModelName    string
		InputTokens  int64
		OutputTokens int64
		AmountMinor  int64
		CallCount    int64
	}
	if err := db.WithContext(ctx).Raw(`
		select provider_id, coalesce(max(model_name), '') as model_name,
		       coalesce(sum(input_token_count), 0) as input_tokens,
		       coalesce(sum(output_token_count), 0) as output_tokens,
		       coalesce(sum(amount_minor), 0) as amount_minor,
		       count(*) as call_count
		from ai_usage_ledger where review_run_id = ? group by provider_id`, runID).
		Scan(&agg).Error; err != nil {
		return nil, err
	}
	if agg.CallCount == 0 {
		return nil, nil
	}
	var providerName string
	_ = db.WithContext(ctx).Raw(
		`select name from ai_providers where id = ?`, agg.ProviderID).Scan(&providerName).Error
	currency := ""
	_ = db.WithContext(ctx).Raw(
		`select coalesce(max(currency), '') from ai_usage_ledger where review_run_id = ?`, runID).Scan(&currency)
	return &RunUsageView{
		ProviderName: providerName, ModelName: agg.ModelName,
		InputTokens: agg.InputTokens, OutputTokens: agg.OutputTokens,
		AmountMinor: agg.AmountMinor, Currency: currency,
	}, nil
}

// ListRunFindings returns paginated Run findings (created_at, id keyset).
func (s *Services) ListRunFindings(ctx context.Context, actor Actor, runID string, limit int, afterCursor string, meta ...RequestMeta) ([]FindingView, *string, error) {
	run, err := s.loadRun(ctx, s.DB, runID)
	if err != nil {
		return nil, nil, err
	}
	draft, err := s.loadDraft(ctx, s.DB, run.DraftID)
	if err != nil {
		return nil, nil, err
	}
	if draft.OwnerUserID != actor.UserID {
		return nil, nil, ErrNotFound
	}
	limit = pagination.ClampLimit(limit, 50, 200)
	type row struct {
		ID         string
		StagePos   int32
		GroupID    *string
		Category   string
		Severity   string
		Title      string
		Message    string
		Suggestion *string
		Confidence *string
		CreatedAt  time.Time
	}
	q := s.DB.WithContext(ctx).Table("review_findings f").
		Select(`f.id::text as id, s.stage_position as stage_pos, f.fingerprint_group_id::text as group_id,
		        f.category, f.severity, f.title, f.message, f.suggestion,
		        f.model_confidence::text as confidence, f.created_at as created_at`).
		Joins("join stage_review_snapshots s on s.id = f.stage_review_id").
		Where("s.review_run_id = ?", runID)
	if afterCursor != "" {
		created, id, err := pagecursor.DecodeUUID(afterCursor)
		if err != nil {
			return nil, nil, ErrValidation
		}
		q = q.Where("(f.created_at, f.id) < (?, ?)", created, id)
	}
	var rows []row
	if err := q.Order("f.created_at desc, f.id desc").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		c := pagecursor.Encode(last.CreatedAt, last.ID)
		next = &c
		rows = rows[:limit]
	}
	// Batch-load evidence references (preventing N+1 queries).
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	evMap := map[string][]string{}
	if len(ids) > 0 {
		type evRow struct {
			FindingID string
			EvID      string
		}
		var evRows []evRow
		if err := s.DB.WithContext(ctx).Raw(`
			select finding_id::text as finding_id, evidence_fact_id::text as ev_id
			from review_finding_evidence where finding_id in ? order by evidence_fact_id`, ids).
			Scan(&evRows).Error; err != nil {
			return nil, nil, err
		}
		for _, e := range evRows {
			evMap[e.FindingID] = append(evMap[e.FindingID], e.EvID)
		}
	}
	out := make([]FindingView, 0, len(rows))
	for i := range rows {
		r := rows[i]
		ev := evMap[r.ID]
		if ev == nil {
			ev = []string{}
		}
		out = append(out, FindingView{
			ID: r.ID, StagePosition: int(r.StagePos), FingerprintGroupID: r.GroupID,
			Category: r.Category, Severity: r.Severity, Title: r.Title,
			Message: r.Message, Suggestion: r.Suggestion, ModelConfidence: r.Confidence,
			EvidenceIDs: ev,
		})
	}
	restored, err := s.restoreFindingDisplay(ctx, actor, displayMeta(meta), runID, out)
	if err != nil {
		return nil, nil, err
	}
	return restored, next, nil
}
