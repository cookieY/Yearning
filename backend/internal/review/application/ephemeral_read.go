// ephemeral_read.go Ephemeral Run read surface: run views, findings, and evidence all come from in-memory
// models (except the model call ledger — pure counts reside in the DB). Identifier restoration also comes
// from the in-memory mapping envelope via display.NewRendererFromPairs; audit events are still written (operation log class).
package application

import (
	"context"
	"sort"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/display"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// LookupEphemeralRun reads an ephemeral Run with ownership verification (returns nil if absent).
func (s *Services) LookupEphemeralRun(runID string, ownerUserID string) *EphemeralRun {
	return s.Ephemeral.Get(runID, ownerUserID)
}

// GetEphemeralRunOwned assembles the ephemeral Run view (mirroring buildRunView).
func (s *Services) GetEphemeralRunOwned(ctx context.Context, actor Actor, runID string, meta ...RequestMeta) (*RunView, error) {
	er := s.Ephemeral.Get(runID, actor.UserID)
	if er == nil {
		return nil, ErrNotFound
	}
	er.mu.RLock()
	run := er.Run
	snapshots := append([]persistence.StageReviewSnapshotModel(nil), er.Snapshots...)
	findings := append([]ephemeralFinding(nil), er.Findings...)
	facts := append([]ephemeralFact(nil), er.Facts...)
	er.mu.RUnlock()
	stages := make([]StageResultView, 0, len(snapshots))
	inputs := make([]domain.GateInput, 0, len(snapshots))
	for i := range snapshots {
		row := &snapshots[i]
		var findingCount int64
		for j := range findings {
			if findings[j].SnapID == row.ID {
				findingCount++
			}
		}
		var evidenceCount int64
		for j := range facts {
			if facts[j].SnapID == row.ID {
				evidenceCount++
			}
		}
		summary, summaryLocale, err := acceptedSummaryAndLocaleFromStageContext(s.Crypter, er.ID, row.StagePosition, crypto.Envelope{
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
	usage, uerr := s.runUsageOf(ctx, s.DB, er.ID)
	if uerr != nil {
		return nil, uerr
	}
	view := &RunView{
		ID: er.ID, DraftID: er.DraftID, DraftRevision: run.DraftRevision,
		State: string(er.State()), StatementCount: int64(run.StatementCount),
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
	return s.restoreEphemeralRunDisplay(ctx, actor, displayMeta(meta), er, view)
}

func (s *Services) restoreEphemeralRunDisplay(ctx context.Context, actor Actor, meta RequestMeta, er *EphemeralRun, view *RunView) (*RunView, error) {
	renderer, err := display.NewRendererFromPairs(er.Mapper.Snapshot())
	if err != nil {
		return nil, err
	}
	copyView := *view
	copyView.StageResults = append([]StageResultView(nil), view.StageResults...)
	total := 0
	for i, stage := range copyView.StageResults {
		text, count := renderer.RestorePointerLimit(stage.Summary, 4000)
		copyView.StageResults[i].Summary = text
		total += count
	}
	if err := s.auditIdentifierDisplay(ctx, actor, meta, er.ID, "review_summary", total); err != nil {
		return nil, err
	}
	return &copyView, nil
}

// ListEphemeralRunFindings returns paginated ephemeral Run findings (created_at, id keyset).
func (s *Services) ListEphemeralRunFindings(ctx context.Context, actor Actor, runID string, limit int, afterCursor string, meta ...RequestMeta) ([]FindingView, *string, error) {
	er := s.Ephemeral.Get(runID, actor.UserID)
	if er == nil {
		return nil, nil, ErrNotFound
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	er.mu.RLock()
	findings := append([]ephemeralFinding(nil), er.Findings...)
	snapshots := append([]persistence.StageReviewSnapshotModel(nil), er.Snapshots...)
	er.mu.RUnlock()
	snapPos := map[string]int32{}
	for i := range snapshots {
		snapPos[snapshots[i].ID] = snapshots[i].StagePosition
	}
	type sortable struct {
		f ephemeralFinding
	}
	rows := make([]sortable, 0, len(findings))
	for i := range findings {
		rows = append(rows, sortable{f: findings[i]})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].f.CreatedAt.Equal(rows[j].f.CreatedAt) {
			return rows[i].f.ID > rows[j].f.ID
		}
		return rows[i].f.CreatedAt.After(rows[j].f.CreatedAt)
	})
	if afterCursor != "" {
		created, id, ok := splitCursor(afterCursor)
		if !ok {
			return nil, nil, ErrValidation
		}
		// Keyset filter (in-memory): retain only rows strictly less than cursor (created, id).
		filtered := rows[:0]
		for i := range rows {
			key := rows[i].f.CreatedAt.UTC().Format(timeFormat)
			if key+","+rows[i].f.ID < created+","+id {
				filtered = append(filtered, rows[i])
			}
		}
		rows = filtered
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	var next *string
	if len(rows) == limit {
		last := rows[limit-1].f
		c := last.CreatedAt.UTC().Format(timeFormat) + "|" + last.ID
		next = &c
	}
	renderer, rerr := display.NewRendererFromPairs(er.Mapper.Snapshot())
	if rerr != nil {
		return nil, nil, rerr
	}
	out := make([]FindingView, 0, len(rows))
	for i := range rows {
		f := rows[i].f
		suggest := (*string)(nil)
		if f.Suggestion != "" {
			s := f.Suggestion
			suggest = &s
		}
		groupID := f.GroupID
		ev := append([]string(nil), f.Evidence...)
		if ev == nil {
			ev = []string{}
		}
		out = append(out, FindingView{
			ID: f.ID, StagePosition: int(snapPos[f.SnapID]), FingerprintGroupID: &groupID,
			Category: f.Category, Severity: f.Severity,
			Title: f.Title, Message: f.Message, Suggestion: suggest,
			EvidenceIDs: ev,
		})
	}
	restored, err := s.restoreEphemeralFindingDisplay(ctx, actor, displayMeta(meta), er.ID, out, renderer)
	if err != nil {
		return nil, nil, err
	}
	return restored, next, nil
}

func (s *Services) restoreEphemeralFindingDisplay(ctx context.Context, actor Actor, meta RequestMeta, runID string, findings []FindingView, renderer *display.Renderer) ([]FindingView, error) {
	if len(findings) == 0 {
		return findings, nil
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

// ListEphemeralFindingEvidence returns normalized evidence for an ephemeral finding (owner only).
func (s *Services) ListEphemeralFindingEvidence(ctx context.Context, actor Actor, findingID string) ([]EvidenceView, error) {
	er, f := s.Ephemeral.FindFinding(findingID, actor.UserID)
	if er == nil || f == nil {
		return nil, ErrNotFound
	}
	er.mu.RLock()
	var facts []ephemeralFact
	for i := range er.Facts {
		for _, eid := range f.Evidence {
			if er.Facts[i].ID == eid {
				facts = append(facts, er.Facts[i])
			}
		}
	}
	er.mu.RUnlock()
	sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })
	now := time.Now().UTC()
	out := make([]EvidenceView, 0, len(facts))
	for i := range facts {
		fact := &facts[i]
		plaintext, err := openPayload(s.Crypter, "evidence_facts", fact.ID, "normalized_fact_ciphertext", fact.FactEnv)
		if err != nil {
			return nil, err
		}
		var normalized any
		if err := jsonUnmarshalStrict(plaintext, &normalized); err != nil {
			return nil, err
		}
		out = append(out, EvidenceView{
			ID: fact.ID, SourceKind: "database_tool", SourceRef: fact.SourceRef,
			FactStatus: "known", NormalizedFact: normalized,
			HasRawPayload: fact.CreatedAt.Add(7 * 24 * time.Hour).After(now),
			CollectedAt:   fact.CreatedAt.UTC().Format(timeFormat),
		})
	}
	return out, nil
}
