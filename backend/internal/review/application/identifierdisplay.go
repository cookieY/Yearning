package application

import (
	"context"

	"github.com/cookieY/YearningX/backend/internal/review/display"
)

func displayMeta(meta []RequestMeta) RequestMeta {
	if len(meta) > 0 {
		return meta[0]
	}
	return RequestMeta{}
}

// auditIdentifierDisplay must succeed before any restored text leaves the
// service. Only resource identities and counts enter the audit, never text.
func (s *Services) auditIdentifierDisplay(ctx context.Context, actor Actor, meta RequestMeta, runID, surface string, count int) error {
	if count == 0 {
		return nil
	}
	return s.Recorder.Audit(ctx, nil, auditInputOf("review.identifier_displayed", "read", actor.UserID, actor.Username,
		"review_run", runID, meta.RequestID, meta.SourceIP, meta.UserAgent,
		map[string]any{"surface": surface, "restored_count": count}))
}

func (s *Services) restoreRunDisplay(ctx context.Context, actor Actor, meta RequestMeta, view *RunView) (*RunView, error) {
	renderer, err := display.Load(ctx, s.DB, s.Crypter, view.ID)
	if err != nil {
		return nil, err
	}
	copyView := *view
	copyView.StageResults = append([]StageResultView(nil), view.StageResults...)
	total := 0
	for i, stage := range view.StageResults {
		text, count := renderer.RestorePointerLimit(stage.Summary, 4000)
		copyView.StageResults[i].Summary = text
		total += count
	}
	if err := s.auditIdentifierDisplay(ctx, actor, meta, view.ID, "review_summary", total); err != nil {
		return nil, err
	}
	return &copyView, nil
}

func (s *Services) restoreFindingDisplay(ctx context.Context, actor Actor, meta RequestMeta, runID string, findings []FindingView) ([]FindingView, error) {
	if len(findings) == 0 {
		return findings, nil
	}
	renderer, err := display.Load(ctx, s.DB, s.Crypter, runID)
	if err != nil {
		return nil, err
	}
	out := make([]FindingView, len(findings))
	total := 0
	for i, finding := range findings {
		title, n1 := renderer.RestoreText(finding.Title)
		message, n2 := renderer.RestoreText(finding.Message)
		suggestion, n3 := renderer.RestorePointer(finding.Suggestion)
		finding.Title, finding.Message, finding.Suggestion = title, message, suggestion
		out[i] = finding
		total += n1 + n2 + n3
	}
	if err := s.auditIdentifierDisplay(ctx, actor, meta, runID, "review_findings", total); err != nil {
		return nil, err
	}
	return out, nil
}
