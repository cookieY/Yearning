// fakerunner.go B6 FakeRunner: deterministic review engine without model connection
// (deliverable "Fake Runner"; R009 grouping consumes the B5 engine).
// Two-stage task chain (async_tasks.kind):
//
//	sql_grouping: streaming analysis -> single-transaction creation of run row (real counts satisfying
//	  fingerprint_group_count CHECK 1..1000) + all sql_statements +
//	  sql_fingerprint_groups + pending stage snapshots, and chained creation of the ai_review task;
//	ai_review: per-stage deterministic finding synthesis + evidence -> gate -> terminal run state + draft state machine + dual events.
//
// All steps are idempotent (run row existence = grouping watermark; terminal snapshot state = stage watermark)
// and recoverable across process interruptions; grouping failure produces no run row (review_runs only holds grouped inputs).
package application

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// RunnerHooks provides test fault-injection points (nil = production default). A hook panic is treated as
// a worker crash for controlled injection in Worker interruption recovery gates.
type RunnerHooks struct {
	AfterGrouping func(runID string)
	AfterStage    func(runID string, position int)
}

// FakeRunner encapsulates the Run execution context.
type FakeRunner struct {
	svc   *Services
	hooks *RunnerHooks
}

// NewFakeRunner constructs a runner.
func NewFakeRunner(svc *Services, hooks *RunnerHooks) *FakeRunner {
	return &FakeRunner{svc: svc, hooks: hooks}
}

// TaskInput is the task input for sql_grouping/ai_review.
type TaskInput struct {
	ReviewRunID   string `json:"review_run_id"`
	DraftID       string `json:"draft_id"`
	DraftRevision int64  `json:"draft_revision"`
	SQLHMAC       string `json:"sql_hmac"`

	FlowSnapshotHash string `json:"flow_snapshot_hash"`
	RuleConfigHash   string `json:"rule_config_hash"`
	ToolConfigHash   string `json:"tool_config_hash"`

	// PolicySnapshot* carry the immutable Run policy inside the existing
	// encrypted async_tasks.input payload. The task chain copies the envelope.
	PolicySnapshotCiphertext []byte `json:"policy_snapshot_ciphertext,omitempty"`
	PolicySnapshotNonce      []byte `json:"policy_snapshot_nonce,omitempty"`
	PolicySnapshotKeyID      string `json:"policy_snapshot_key_id,omitempty"`
	PolicySnapshotAlgorithm  string `json:"policy_snapshot_algorithm,omitempty"`
	PolicySnapshotHash       string `json:"policy_snapshot_hash,omitempty"`

	OutputLocale string `json:"output_locale,omitempty"`
}

// Execute is the task entry point: run row absent -> grouping phase; present -> review phase.
// fenceGuard verifies the current worker still holds the task before writing terminal business state (zombie worker defense).
func (r *FakeRunner) Execute(ctx context.Context, db *gorm.DB, in TaskInput,
	fenceGuard func() error, beat func(completed, total int64)) error {
	run, err := r.svc.loadRunOrNull(ctx, db, in.ReviewRunID)
	if err != nil {
		return err
	}
	if run == nil {
		err := r.groupingPhase(ctx, db, in, beat)
		if err != nil {
			// Any failure in the grouping phase never leaves a reviewing deadlock: the draft converges to failed
			// (idempotent: only when still in reviewing; zombie workers are rejected by the guard).
			if errors.Is(err, tasks.ErrFenceLost) {
				return err
			}
			return r.failDraftNoRun(ctx, db, &persistence.ChangeDraftModel{ID: in.DraftID}, err)
		}
		return nil
	}
	if domain.RunTerminal(domain.RunState(run.State)) {
		return nil // Idempotent replay
	}
	if _, err := r.svc.VerifyAndDecodePolicySnapshot(ctx, db, in, PolicySnapshotPurposeReview); err != nil {
		return r.finish(ctx, db, run, fenceGuard, domain.RunEventInputsChanged, 0, "")
	}
	return r.reviewPhases(ctx, db, run, fenceGuard, beat)
}

// groupingPhase sql_grouping: single-pass streaming analysis + atomic persistence + chained task.
func (r *FakeRunner) groupingPhase(ctx context.Context, db *gorm.DB, in TaskInput, beat func(int64, int64)) error {
	s := r.svc
	// Idempotency recheck: under concurrent replay, another worker may have already created the run row.
	if existing, err := s.loadRunOrNull(ctx, db, in.ReviewRunID); err != nil {
		return err
	} else if existing != nil {
		return r.ensureReviewTask(ctx, db, in)
	}
	draft, err := s.loadDraft(ctx, db, in.DraftID)
	if err != nil {
		return err
	}
	if draft.Revision != in.DraftRevision {
		// Input already invalidated: task converges to success (the run is no longer meaningful).
		return nil
	}
	if _, err := s.VerifyAndDecodePolicySnapshot(ctx, db, in, PolicySnapshotPurposeReview); err != nil {
		return r.failDraftNoRun(ctx, db, draft, domain.ErrReviewOutdated)
	}
	if err := s.verifyFrozenHashesForDraft(ctx, db, in.DraftID,
		in.FlowSnapshotHash, in.RuleConfigHash, in.ToolConfigHash); err != nil {
		if errors.Is(err, errInputsDrift) {
			return r.failDraftNoRun(ctx, db, draft, domain.ErrCoverageIncomplete)
		}
		return r.failDraftNoRun(ctx, db, draft, err)
	}
	profile, err := s.draftProfile(ctx, db, in.DraftID)
	if err != nil {
		return r.failDraftNoRun(ctx, db, draft, err)
	}
	var payload persistence.DraftSQLPayloadModel
	if err := db.WithContext(ctx).Where("draft_id = ?", in.DraftID).
		First(&payload).Error; err != nil {
		return r.failDraftNoRun(ctx, db, draft, err)
	}
	plaintext, err := openPayload(s.Crypter, "draft_sql_payloads", in.DraftID, "sql_ciphertext", cryptoEnvelopeOf(payload))
	if err != nil {
		return r.failDraftNoRun(ctx, db, draft, domain.ErrAnonymizationFailed)
	}
	if !verifyHMAC(s.Crypter, plaintext, in.SQLHMAC) {
		return r.failDraftNoRun(ctx, db, draft, &domain.Error{Code: domain.CodeSQLFingerprintFailed, Name: "SQL_FINGERPRINT_FAILED"})
	}
	// Single-pass streaming analysis (memory retains only structural material; raw statement text is discarded immediately after use).
	split := changeset.NewSplitter(bytes.NewReader(plaintext), profile, 0)
	mapper := privacy.NewRunMapper()
	stmts, aggs, aerr := analyzeRunStatements(split, profile, mapper, s.Engine, payload.StatementCount, beat)
	if aerr != nil {
		return r.failDraftNoRun(ctx, db, draft, aerr)
	}
	fpKeyID, err := s.Crypter.HMACKeyID(sqlHMACPurpose)
	if err != nil {
		return err
	}
	stages, err := s.loadFlowStages(ctx, db, draft.FlowID)
	if err != nil {
		return err
	}
	if len(stages) == 0 {
		return r.failDraftNoRun(ctx, db, draft, domain.ErrCoverageIncomplete)
	}
	for i := range stages {
		if stages[i].IdentityFP == nil || *stages[i].IdentityFP == "" {
			return r.failDraftNoRun(ctx, db, draft, &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"})
		}
	}
	frozenTargets := make(map[int]frozenDatasourceTarget, len(stages))
	for i := range stages {
		st := stages[i]
		target, err := s.probeFrozenDatasourceTarget(ctx, datasourceTargetRow{
			ID: st.DatasourceID, Engine: st.Engine,
			CompatibilityMode: st.CompatibilityMode,
			Host:              st.Host, Port: st.Port, DatabaseName: st.DatabaseName,
			IdentityFP: st.IdentityFP, Enabled: st.Enabled,
		})
		if err != nil {
			return r.failDraftNoRun(ctx, db, draft, domain.ErrCoverageIncomplete)
		}
		frozenTargets[st.Position] = target
	}
	postgresStatements := make([]postgresAnalyzedStatement, 0, len(stmts))
	for i := range stmts {
		postgresStatements = append(postgresStatements, postgresAnalyzedStatement{
			result:      stmts[i].res,
			objects:     stmts[i].objects,
			pathUnsafe:  stmts[i].pathUnsafe,
			callables:   stmts[i].callables,
			callablesOK: stmts[i].callableOK,
		})
	}
	postgresContextByPosition := make(map[int]map[string]any, len(stages))
	for i := range stages {
		stage := stages[i]
		extra, err := postgresContextValueForStage(
			ctx, s, stage, frozenTargets[stage.Position], postgresStatements, mapper,
		)
		if err != nil {
			return r.failDraftNoRun(ctx, db, draft, domain.ErrCoverageIncomplete)
		}
		postgresContextByPosition[stage.Position] = extra
	}
	now := nowUTC()
	// Atomic persistence: run row + statements + groups + skeletons + chained task (single-transaction watermark).
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := s.loadDraftForUpdate(ctx, tx, in.DraftID)
		if err != nil {
			return err
		}
		if existing, err := s.loadRunOrNull(ctx, tx, in.ReviewRunID); err != nil {
			return err
		} else if existing != nil {
			return nil
		}
		if locked.Revision != in.DraftRevision {
			return nil // Concurrent revision: abandon this grouping
		}
		if err := s.verifyFrozenHashesForDraft(ctx, tx, in.DraftID,
			in.FlowSnapshotHash, in.RuleConfigHash, in.ToolConfigHash); err != nil {
			if errors.Is(err, errInputsDrift) {
				return domain.ErrCoverageIncomplete
			}
			return err
		}
		// SQL Hash binding (complete_ai_precheck precondition): the HMAC frozen in task input must match
		// the current payload and be persisted to the Run row for terminal and submission re-verification (B8).
		var payloadHMAC string
		if err := tx.WithContext(ctx).Raw(
			`select plaintext_hmac from draft_sql_payloads where draft_id = ?`, in.DraftID).
			Scan(&payloadHMAC).Error; err != nil {
			return err
		}
		if payloadHMAC == "" {
			return ErrNotFound
		}
		if payloadHMAC != in.SQLHMAC {
			return ErrConcurrent
		}
		run := persistence.ReviewRunModel{
			ID: in.ReviewRunID, DraftID: in.DraftID, DraftRevision: in.DraftRevision,
			State:            string(domain.RunStateRunning),
			FlowSnapshotHash: in.FlowSnapshotHash, RuleConfigHash: in.RuleConfigHash,
			ToolConfigHash: in.ToolConfigHash, ProviderPlanHash: fakeProviderPlanHash,
			FingerprintAlgorithmVersion:   fingerprint.AlgorithmVersion,
			AnonymizationAlgorithmVersion: "run-mapper-v1",
			StatementCount:                int32(len(stmts)),
			FingerprintGroupCount:         int32(len(aggs)),
			BudgetDay:                     now.Format("2006-01-02"),
			Version:                       1,
			CreatedAt:                     now,
			StartedAt:                     &now,
		}
		if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
			return err
		}
		// SQL Hash frozen binding (000008 insert-only fact table;
		// complete_ai_precheck precondition review_bound_to_exact_draft_revision_and_sql_hash).
		if err := tx.WithContext(ctx).Exec(`
			insert into review_run_sql_bindings (review_run_id, sql_payload_hmac, created_at)
			values (?, ?, ?)
			on conflict (review_run_id) do nothing`,
			run.ID, in.SQLHMAC, now).Error; err != nil {
			return err
		}
		// The grouping phase and subsequent tool facts share the same RunMapper. The mapping envelope must be
		// persisted within the exact same transaction that creates the Run, statement, and group rows; failure
		// at any step must never leave behind a Run whose typed_shape tokens cannot be restored.
		if err := persistIdentifierMappingTx(ctx, tx, s.Crypter, run.ID, mapper); err != nil {
			return err
		}
		batch := make([]persistence.SQLStatementModel, 0, 512)
		insertBatch := func(rows []persistence.SQLStatementModel) error {
			if len(rows) == 0 {
				return nil
			}
			return tx.WithContext(ctx).CreateInBatches(rows, 512).Error
		}
		for i := range stmts {
			st := stmts[i]
			typed, err := jsonMarshal(map[string]any{
				"kind": st.res.StatementKind, "operation": st.operation,
				"dialect_profile": st.res.DialectProfile, "quality": st.res.Quality,
				"tuple_count": st.res.TupleCount, "parameter_count": st.res.ParameterCount,
				"anonymized_sql": st.res.AnonymizedSQL, "objects": st.objects,
				"statement_ordinal":         i + 1,
				"metadata_context_complete": st.res.MetadataContextComplete,
				"execution_validity":        st.res.Validity,
			})
			if err != nil {
				return err
			}
			lit, err := jsonMarshal(st.res.LiteralTypeCounts)
			if err != nil {
				return err
			}
			batch = append(batch, persistence.SQLStatementModel{
				ID: newID(), ReviewRunID: run.ID, Ordinal: int32(i + 1),
				StatementKind: st.class, Operation: st.operation,
				SizeBytes:        int32(st.res.ByteLen),
				AlgorithmVersion: st.res.AlgorithmVersion, DialectProfile: st.res.DialectProfile,
				FingerprintQuality: st.res.Quality, FingerprintKeyID: st.res.FingerprintKeyID,
				CanonicalFingerprintHMAC: st.res.CanonicalFingerprintHMAC,
				DialectFingerprintHMAC:   st.res.DialectFingerprintHMAC,
				ExecutionShapeHMAC:       st.res.ExecutionShapeHMAC,
				TypedShape:               typed, LiteralTypeCounts: lit,
				TupleCount: int32(st.res.TupleCount), ParameterCount: int32(st.res.ParameterCount),
			})
			if len(batch) >= 512 {
				if err := insertBatch(batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
		if err := insertBatch(batch); err != nil {
			return err
		}
		for i := range aggs {
			g := aggs[i]
			var repID string
			if err := tx.WithContext(ctx).Raw(`
				select id::text from sql_statements
				where review_run_id = ?
				  and canonical_fingerprint_hmac = ?
				  and dialect_fingerprint_hmac = ?
				order by ordinal limit 1`,
				run.ID, g.canon, g.dialect).Scan(&repID).Error; err != nil {
				return err
			}
			if repID == "" {
				return &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"}
			}
			shape := map[string]any{
				"algorithm_version":             fingerprint.AlgorithmVersion,
				"representative_anonymized_sql": g.representativeAnonymizedSQL,
				"statement_ordinal":             g.representativeOrdinal,
				"statement_ordinal_ranges":      g.statementOrdinalRanges,
				"objects":                       g.objects,
				"metadata_context_complete":     g.metadataContextComplete,
				"in_value_count_max":            g.inMax,
				"values_row_count_max":          g.valMax,
				"unbounded_mutation":            g.unbounded,
			}
			for k, v := range g.facts.m {
				shape[k] = v
			}
			typed, err := jsonMarshal(shape)
			if err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Create(&persistence.SQLFingerprintGroupModel{
				ID: newID(), ReviewRunID: run.ID,
				CanonicalFingerprintHMAC: g.canon, DialectFingerprintHMAC: g.dialect,
				AlgorithmVersion: fingerprint.AlgorithmVersion, FingerprintKeyID: fpKeyID,
				TypedShape: typed, InValueCountMax: int32(g.inMax), ValuesRowCountMax: int32(g.valMax),
				StatementCount: int32(g.count), RepresentativeStatementID: repID,
			}).Error; err != nil {
				return err
			}
		}
		for i := range stages {
			st := stages[i]
			contextBase := map[string]any{
				"stage_position": st.Position, "datasource_id": st.DatasourceID,
				"engine": st.Engine, "review_run_id": run.ID,
			}
			for key, value := range postgresContextByPosition[st.Position] {
				contextBase[key] = value
			}
			contextValue, err := withFrozenDatasourceTarget(contextBase, frozenTargets[st.Position])
			if err != nil {
				return err
			}
			env, err := sealStageReviewContext(s.Crypter, run.ID, int32(st.Position), contextValue)
			if err != nil {
				return err
			}
			snapModel := &persistence.StageReviewSnapshotModel{
				ID: newID(), ReviewRunID: run.ID, StagePosition: int32(st.Position),
				DatasourceID: st.DatasourceID, DatasourceIdentityFingerprint: *st.IdentityFP,
				State: string(domain.StageStatePending), HighestSeverity: string(domain.SeverityNone),
				GatePassed: false, GateReasonCodes: persistence.EncodeTextArray(nil),
				NormalizedContextCiphertext: env.Ciphertext, NormalizedContextNonce: env.Nonce,
				NormalizedContextKeyID: env.KeyID, NormalizedContextAlgorithm: env.Algorithm,
				SnapshotHash: sha256Hex([]byte(run.ID + "|" + fmt.Sprint(st.Position) + "|" + *st.IdentityFP)),
				CreatedAt:    now,
			}
			if err := tx.WithContext(ctx).Create(snapModel).Error; err != nil {
				return err
			}
		}
		// Chained ai_review task (same transaction; crash replay backfills it via ensureReviewTask).
		return createReviewTask(ctx, tx, in)
	})
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) || isPgCheckViolation(err) {
			return r.failDraftNoRun(ctx, db, draft, err)
		}
		return err
	}
	if r.hooks != nil && r.hooks.AfterGrouping != nil {
		r.hooks.AfterGrouping(in.ReviewRunID)
	}
	return nil
}

// ensureReviewTask backfills the ai_review task (replay path; draft row lock serializes
// count-then-insert to prevent duplicate chained tasks).
func (r *FakeRunner) ensureReviewTask(ctx context.Context, db *gorm.DB, in TaskInput) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := r.svc.loadDraftForUpdate(ctx, tx, in.DraftID); err != nil {
			return err
		}
		return createReviewTask(ctx, tx, in)
	})
}

// createReviewTask creates the ai_review task (skipped if already present).
func createReviewTask(ctx context.Context, tx *gorm.DB, in TaskInput) error {
	var n int64
	if err := tx.WithContext(ctx).Raw(
		`select count(*) from async_tasks where kind = ? and aggregate_id = ?`,
		tasks.KindAIReview, in.ReviewRunID).Scan(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	aggregateType := "review_run"
	input, err := jsonMarshal(in)
	if err != nil {
		return err
	}
	now := nowUTC()
	task := tasks.AsyncTaskModel{
		ID: newID(), Kind: tasks.KindAIReview,
		State: "queued", AggregateType: &aggregateType, AggregateID: &in.ReviewRunID,
		Input: input, ProgressUnit: "stages", FenceToken: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	return tx.WithContext(ctx).Create(&task).Error
}

// reviewPhases ai_review: Phase C (per-stage review) + Phase D (gate and terminal state).
func (r *FakeRunner) reviewPhases(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	fenceGuard func() error, beat func(int64, int64)) error {
	s := r.svc
	if ctx.Err() != nil {
		return r.finish(ctx, db, run, fenceGuard, domain.RunEventIncomplete, 2007, "cancelled")
	}
	// B8 frozen input re-verification: SQL Hash / rule Hash drift invalidates as inputs_changed
	// (lazy path backs up eager invalidation; reviewing drafts thereby maintain a valid convergence path).
	if err := s.verifyFrozenInputs(ctx, db, run); err != nil {
		if errors.Is(err, errInputsDrift) {
			return r.finish(ctx, db, run, fenceGuard, domain.RunEventInputsChanged, 0, "")
		}
		return err
	}
	snapshots, err := s.loadStageSnapshots(ctx, db, run.ID)
	if err != nil {
		return err
	}
	total := int64(len(snapshots))
	completed := int64(0)
	for i := range snapshots {
		snap := &snapshots[i]
		if domain.StageTerminal(domain.StageReviewState(snap.State)) {
			completed++
			continue
		}
		if fenceGuard != nil {
			if err := fenceGuard(); err != nil {
				return err // Zombie worker: abandon business write
			}
		}
		if err := r.reviewStage(ctx, db, run, snap, beat); err != nil {
			if errors.Is(err, errInputsDrift) {
				return r.finish(ctx, db, run, fenceGuard, domain.RunEventInputsChanged, 0, "")
			}
			return r.finish(ctx, db, run, fenceGuard, domain.RunEventFailed, failureCodeOf(err), failureNameOf(err))
		}
		completed++
		if beat != nil {
			beat(completed, total)
		}
		if r.hooks != nil && r.hooks.AfterStage != nil {
			r.hooks.AfterStage(run.ID, int(snap.StagePosition))
		}
		if ctx.Err() != nil {
			return r.finish(ctx, db, run, fenceGuard, domain.RunEventIncomplete, 2007, "cancelled")
		}
	}
	// B8 coverage check: statement and group coverage must match the frozen statement_count
	// (group coverage side of evidence_complete_or_fail_closed).
	if err := s.verifyRunCoverage(ctx, db, run); err != nil {
		if de, ok := err.(*domain.Error); ok {
			return r.finish(ctx, db, run, fenceGuard, domain.RunEventIncomplete, int32(de.Code), de.Name)
		}
		return err
	}
	stageRows, gateInputs, err := s.stageGateInputs(ctx, db, run.ID)
	if err != nil {
		return err
	}
	gate := domain.EvaluateGate(gateInputs)
	trigger := domain.RunEventCompleted
	if !gate.Passed {
		blocked := false
		for _, gi := range gateInputs {
			if domain.SeverityRank(gi.HighestFound) >= domain.SeverityRank(domain.BlockSeverityThreshold) {
				blocked = true
			}
		}
		if stagedCoverageIncomplete(stageRows) || !blocked {
			trigger = domain.RunEventIncomplete
		} else {
			trigger = domain.RunEventRiskBlocked
		}
	}
	biz, name := int32(2008), "risk_blocked"
	if trigger == domain.RunEventCompleted {
		biz, name = 0, ""
	} else if trigger == domain.RunEventIncomplete {
		biz, name = 2007, "coverage_incomplete"
	}
	return r.finish(ctx, db, run, fenceGuard, trigger, biz, name)
}

// finishCancelled converges cancellation: run -> partial (preserving completed evidence).
func (r *FakeRunner) finishCancelled(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel) error {
	return r.finish(ctx, db, run, nil, domain.RunEventIncomplete, 2007, "cancelled")
}

// failDraftNoRun handles grouping failure: no run row exists, so draft transitions directly from reviewing -> failed.
func (r *FakeRunner) failDraftNoRun(ctx context.Context, db *gorm.DB, draft *persistence.ChangeDraftModel, cause error) error {
	s := r.svc
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := s.loadDraftForUpdate(ctx, tx, draft.ID)
		if err != nil {
			return err
		}
		if domain.DraftState(fresh.State) != domain.DraftStateReviewing {
			return nil // User has revised in parallel
		}
		to, terr := domain.DraftTransition(domain.DraftStateReviewing, domain.DraftEventReviewFailed)
		if terr != nil {
			return terr
		}
		if err := s.updateDraftVersioned(ctx, tx, fresh, fresh.Version, map[string]any{
			"state": string(to), "version": fresh.Version + 1, "updated_at": nowUTC(),
		}); err != nil {
			return err
		}
		from := string(domain.DraftStateReviewing)
		return emitDraftEvent(ctx, tx, s.Recorder, domain.EventDraftPrecheckComplete, fresh.ID, fresh.OwnerUserID,
			domain.DraftStateChangedData{
				AggregateID: fresh.ID, From: &from, To: string(to),
				ReasonCode: domain.ReasonPrecheckFailed, AggregateVersion: fresh.Version + 1,
			})
	})
	if err != nil {
		return err
	}
	return cause
}

// reviewStage Phase C: group summary evidence + terminal snapshot state (single transaction).
// Ruling (2026-08-27): business review findings come exclusively from skills (model path);
// the model-less Fake path produces no business findings — serve wires AgentRunner,
// failing closed (2011) when no Provider is available.
func (r *FakeRunner) reviewStage(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	snap *persistence.StageReviewSnapshotModel, beat func(int64, int64)) error {
	s := r.svc
	highest := domain.SeverityNone
	stageState := domain.StageStatePassed
	summary := map[string]any{
		"stage_position":          snap.StagePosition,
		"datasource_id":           snap.DatasourceID,
		"statement_count":         run.StatementCount,
		"fingerprint_group_count": run.FingerprintGroupCount,
		"highest_severity":        string(highest),
	}
	summaryJSON, err := jsonMarshal(summary)
	if err != nil {
		return err
	}
	snapHash, err := factHMAC(s.Crypter, summaryJSON)
	if err != nil {
		return err
	}
	now := nowUTC()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state string
		if err := tx.WithContext(ctx).Raw(
			`select state from stage_review_snapshots where id = ? for update`, snap.ID).
			Scan(&state).Error; err != nil {
			return err
		}
		if domain.StageTerminal(domain.StageReviewState(state)) {
			return nil
		}
		// Evidence: normalized facts are retained permanently; raw payloads expire in 7 days (enforced by DB CHECK).
		// AAD binds factID (allocated before encryption).
		factID := newID()
		env, mac, err := sealPayload(s.Crypter, "evidence_facts", factID, "normalized_fact_ciphertext", summaryJSON)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			insert into evidence_facts (id, stage_review_id, source_kind, source_ref,
				normalized_fact_ciphertext, normalized_fact_nonce, normalized_fact_key_id,
				normalized_fact_algorithm, fact_hash, created_at)
			values (?, ?, 'fingerprint_group_summary', ?, ?, ?, ?, 'aes-256-gcm', ?, ?)`,
			factID, snap.ID, "review_runs/"+run.ID,
			env.Ciphertext, env.Nonce, env.KeyID, mac, now).Error; err != nil {
			return err
		}
		// Raw envelope uses the unified sealRawEvidence convention (SEC-0042: AAD = factID,
		// payload_hash = HMAC(raw); on the Fake path raw == normalized summary so the value is unchanged,
		// but write and read sides no longer assemble AAD inline independently).
		rawEnv, rawHash, err := sealRawEvidence(s.Crypter, factID, summaryJSON)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			insert into evidence_raw_payloads (id, evidence_fact_id, raw_payload_ciphertext,
				raw_payload_nonce, raw_payload_key_id, raw_payload_algorithm, payload_hash,
				created_at, expires_at)
			values (?, ?, ?, ?, ?, 'aes-256-gcm', ?, ?, ?)`,
			newID(), factID, rawEnv.Ciphertext, rawEnv.Nonce, rawEnv.KeyID, rawHash, now,
			now.Add(7*24*time.Hour)).Error; err != nil {
			return err
		}
		res := tx.WithContext(ctx).Exec(`
			update stage_review_snapshots set state = ?, highest_severity = ?, gate_passed = ?,
				gate_reason_codes = ?, snapshot_hash = ?, finished_at = ?
			where id = ? and state in ('pending','running')`,
			string(stageState), string(highest), stageState == domain.StageStatePassed,
			persistence.EncodeTextArray(reasonCodesOf(stageState, highest)), snapHash, now, snap.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConcurrent
		}
		return nil
	})
}

// finish Phase D: terminal convergence (run state machine + draft state machine + dual events).
func (r *FakeRunner) finish(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	fenceGuard func() error, trigger domain.RunTrigger, bizCode int32, failureName string) error {
	return finishRun(r.svc, ctx, db, run, fenceGuard, trigger, bizCode, failureName)
}

// finishRun executes a single terminal transaction for the Run: run state machine + snapshot convergence + draft state machine + dual events.
func finishRun(s *Services, ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	fenceGuard func() error, trigger domain.RunTrigger, bizCode int32, failureName string) error {
	if fenceGuard != nil {
		if err := fenceGuard(); err != nil {
			return err // Zombie worker: abandon terminal write
		}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock order: lexicographical by resource_type (change_drafts before review_runs).
		draft, err := s.loadDraftForUpdate(ctx, tx, run.DraftID)
		if err != nil {
			return err
		}
		fresh, err := s.loadRunForUpdate(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		if domain.RunTerminal(domain.RunState(fresh.State)) {
			return nil
		}
		nextRun, err := domain.RunTransition(domain.RunState(fresh.State), trigger)
		if err != nil {
			return err
		}
		now := nowUTC()
		sets := map[string]any{
			"state": string(nextRun), "version": fresh.Version + 1, "finished_at": now,
		}
		// review_run_failure_fields CHECK: only blocked/partial/failed carry failure fields;
		// outdated must have them empty (the B8 lazy invalidation path passes through here).
		if nextRun == domain.RunStateBlocked || nextRun == domain.RunStatePartial || nextRun == domain.RunStateFailed {
			sets["business_err_code"] = bizCode
			sets["failure_name"] = failureName
		}
		if err := s.updateRunVersioned(ctx, tx, fresh.ID, fresh.Version, sets); err != nil {
			return err
		}
		if nextRun == domain.RunStateFailed || nextRun == domain.RunStatePartial {
			if err := tx.WithContext(ctx).Exec(`
				update stage_review_snapshots set state = ?, finished_at = ?, gate_passed = false
				where review_run_id = ? and state in ('pending','running')`,
				terminalStageOf(nextRun), now, fresh.ID).Error; err != nil {
				return err
			}
		}
		if domain.DraftState(draft.State) == domain.DraftStateReviewing {
			toDraft, terr := domain.DraftTransition(domain.DraftStateReviewing, draftTriggerOf(nextRun))
			if terr != nil {
				return terr
			}
			if err := s.updateDraftVersioned(ctx, tx, draft, draft.Version, map[string]any{
				"state": string(toDraft), "version": draft.Version + 1, "updated_at": now,
			}); err != nil {
				return err
			}
			draft.Version++
			from := string(domain.DraftStateReviewing)
			if err := emitDraftEvent(ctx, tx, s.Recorder, domain.EventDraftPrecheckComplete, draft.ID, draft.OwnerUserID,
				domain.DraftStateChangedData{
					AggregateID: draft.ID, From: &from, To: string(toDraft),
					ReasonCode: draftReasonOf(nextRun), AggregateVersion: draft.Version,
				}); err != nil {
				return err
			}
		}
		stageResults, gate, stmtCount, groupCount, err := s.runTerminalSummary(ctx, tx, fresh.ID)
		if err != nil {
			return err
		}
		eventState := string(nextRun)
		if eventState == string(domain.RunStateOutdated) {
			eventState = string(domain.RunStatePartial)
		}
		if eventState == string(domain.RunStateFailed) || eventState == string(domain.RunStatePartial) {
			gate = domain.GatePayload{Passed: false, ReasonCodes: appendIfMissing(gate.ReasonCodes, domain.ReasonStageIncomplete)}
		}
		return emitRunEvent(ctx, tx, s.Recorder, domain.EventReviewRunCompleted, fresh.ID,
			auditlog.Actor{Kind: "worker"}, domain.ReviewEventData{
				ReviewRunID: fresh.ID, DraftID: fresh.DraftID, DraftRevision: fresh.DraftRevision,
				State: eventState, StageResults: stageResults, Gate: gate,
				StatementCount: int(stmtCount), FingerprintGroupCount: int(groupCount),
				AggregateVersion: fresh.Version + 1,
			})
	})
}

// ---- Helpers ----

const (
	maxStatementOrdinalRanges      = 512
	maxObjectOrdinalRanges         = 512
	maxObjectReferencesPerGroup    = 512
	maxObjectOrdinalRangesPerGroup = 4096
	maxObjectReferencesPerRun      = 4096
	maxObjectOrdinalRangesPerRun   = 16384
)

// statementOrdinalRange represents a contiguous range of statement ordinals for a fingerprint group within the SQL batch.
// JSON bounds are closed intervals [start, end].
type statementOrdinalRange struct {
	Start int32 `json:"start"`
	End   int32 `json:"end"`
}

// objectsAtStatementOrdinal copies single-statement object references and populates the original
// statement ordinal from the grouping phase. The fingerprint layer always outputs 0; must not mutate the shared Result slice in place.
func objectsAtStatementOrdinal(
	refs []fingerprint.ObjectReference,
	ordinal int32,
) []fingerprint.ObjectReference {
	out := make([]fingerprint.ObjectReference, 0, len(refs))
	for _, ref := range refs {
		ref.StatementOrdinal = ordinal
		ref.StatementOrdinalRanges = []fingerprint.StatementOrdinalRange{{
			Start: ordinal,
			End:   ordinal,
		}}
		out = append(out, ref)
	}
	return out
}

// mergeMetadataContextComplete takes the logical AND of metadata context completeness across the group:
// when any statement can only be proven via compatible_lexer fallback, the entire group must not be treated
// by the model as having complete object context to skip table-existence checks.
func mergeMetadataContextComplete(current, next bool) bool {
	return current && next
}

// appendStatementOrdinalRange compresses monotonically increasing statement ordinals within a group into closed intervals.
// Adjacent ordinals merge; interleaved ordinals remain separate intervals. When exceeding the cap, retains
// the first 512 intervals and returns false; the caller must mark the group as metadata context incomplete
// rather than treating truncated intervals as complete object context.
func appendStatementOrdinalRange(
	ranges []statementOrdinalRange,
	ordinal int32,
) ([]statementOrdinalRange, bool) {
	if ordinal < 1 {
		return ranges, false
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if ordinal < last.Start {
			return ranges, false
		}
		if ordinal <= last.End {
			return ranges, true
		}
		if ordinal == last.End+1 {
			last.End = ordinal
			return ranges, true
		}
	}
	if len(ranges) >= maxStatementOrdinalRanges {
		return ranges, false
	}
	return append(ranges, statementOrdinalRange{Start: ordinal, End: ordinal}), true
}

// objectContextBudget bounds the complete Provider object context for a Run.
// Counts are shared across fingerprint groups by groupingPhase.
type objectContextBudget struct {
	objects int
	ranges  int
}

// objectMergeState is the per-group index and range-membership count.
type objectMergeState struct {
	index  map[string]int
	ranges int
}

func newObjectMergeState() *objectMergeState {
	return &objectMergeState{index: map[string]int{}}
}

// mergeObjects merges object references by semantic identity while retaining
// every occurrence as an inclusive statement range. The first ordinal remains
// for compatibility. Any per-object, per-group, or per-Run overflow returns
// false so the group's metadata context becomes incomplete while the accepted
// prefix stays bounded and available for review.
func mergeObjects(
	dst []fingerprint.ObjectReference,
	state *objectMergeState,
	refs []fingerprint.ObjectReference,
	budget *objectContextBudget,
) ([]fingerprint.ObjectReference, bool) {
	if state == nil {
		state = newObjectMergeState()
	}
	if state.index == nil {
		state.index = map[string]int{}
	}
	if budget == nil {
		budget = &objectContextBudget{}
	}
	if dst == nil {
		dst = []fingerprint.ObjectReference{}
	}
	complete := true
	for _, ref := range refs {
		ranges, ok := normalizedObjectRanges(ref)
		if !ok {
			complete = false
			continue
		}
		ref.StatementOrdinalRanges = ranges
		if ref.StatementOrdinal < 1 {
			ref.StatementOrdinal = ranges[0].Start
		}
		key := objectReferenceKey(ref)
		if index, exists := state.index[key]; exists {
			existing := dst[index]
			merged, added, ok := mergeObjectOrdinalRanges(
				existing.StatementOrdinalRanges, ranges, maxObjectOrdinalRanges)
			if !ok ||
				state.ranges+added > maxObjectOrdinalRangesPerGroup ||
				budget.ranges+added > maxObjectOrdinalRangesPerRun {
				complete = false
				continue
			}
			existing.StatementOrdinalRanges = merged
			dst[index] = existing
			state.ranges += added
			budget.ranges += added
			continue
		}
		added := len(ranges)
		if len(dst) >= maxObjectReferencesPerGroup ||
			budget.objects >= maxObjectReferencesPerRun ||
			state.ranges+added > maxObjectOrdinalRangesPerGroup ||
			budget.ranges+added > maxObjectOrdinalRangesPerRun {
			complete = false
			continue
		}
		state.index[key] = len(dst)
		dst = append(dst, ref)
		state.ranges += added
		budget.objects++
		budget.ranges += added
	}
	return dst, complete
}

func normalizedObjectRanges(
	ref fingerprint.ObjectReference,
) ([]fingerprint.StatementOrdinalRange, bool) {
	if len(ref.StatementOrdinalRanges) == 0 {
		if ref.StatementOrdinal < 1 {
			return nil, false
		}
		return []fingerprint.StatementOrdinalRange{{
			Start: ref.StatementOrdinal,
			End:   ref.StatementOrdinal,
		}}, true
	}
	out := make([]fingerprint.StatementOrdinalRange, 0, len(ref.StatementOrdinalRanges))
	for _, next := range ref.StatementOrdinalRanges {
		var ok bool
		out, ok = appendObjectOrdinalRange(out, next, maxObjectOrdinalRanges)
		if !ok {
			return nil, false
		}
	}
	return out, true
}

func mergeObjectOrdinalRanges(
	existing []fingerprint.StatementOrdinalRange,
	incoming []fingerprint.StatementOrdinalRange,
	limit int,
) ([]fingerprint.StatementOrdinalRange, int, bool) {
	out := append([]fingerprint.StatementOrdinalRange{}, existing...)
	added := 0
	for _, next := range incoming {
		before := len(out)
		var ok bool
		out, ok = appendObjectOrdinalRange(out, next, limit)
		if !ok {
			return append([]fingerprint.StatementOrdinalRange{}, existing...), 0, false
		}
		added += len(out) - before
	}
	return out, added, true
}

func appendObjectOrdinalRange(
	ranges []fingerprint.StatementOrdinalRange,
	next fingerprint.StatementOrdinalRange,
	limit int,
) ([]fingerprint.StatementOrdinalRange, bool) {
	if next.Start < 1 || next.End < next.Start {
		return ranges, false
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if next.Start < last.Start {
			return ranges, false
		}
		if next.Start <= last.End {
			if next.End > last.End {
				last.End = next.End
			}
			return ranges, true
		}
		if next.Start == last.End+1 {
			last.End = next.End
			return ranges, true
		}
	}
	if len(ranges) >= limit {
		return ranges, false
	}
	return append(ranges, next), true
}

func objectReferenceKey(ref fingerprint.ObjectReference) string {
	return ref.Token + "\x00" + ref.Kind + "\x00" + string(ref.Relation) +
		"\x00" + ref.SchemaToken + "\x00" + ref.ParentToken
}

// verifyHMAC verifies plaintext HMAC (case-insensitive hex).
func verifyHMAC(c *crypto.Crypter, plaintext []byte, expectedHex string) bool {
	mac, err := c.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(mac), expectedHex)
}

// draftTriggerOf maps terminal Run state -> draft state machine trigger.
// outdated (B8 lazy invalidation: input drift during reviewing) converges via review_failed:
// the draft state machine has no reviewing -> outdated edge, and failed keeps precheck rerunnable
// without falsely presenting a usable conclusion (fail closed).
func draftTriggerOf(run domain.RunState) domain.DraftTrigger {
	switch run {
	case domain.RunStateReady:
		return domain.DraftEventReviewPassed
	case domain.RunStateBlocked:
		return domain.DraftEventRiskGateBlocked
	case domain.RunStatePartial:
		return domain.DraftEventReviewIncomplete
	default:
		return domain.DraftEventReviewFailed
	}
}

// draftReasonOf returns the reason_code for the precheck_completed event.
func draftReasonOf(run domain.RunState) string {
	switch run {
	case domain.RunStateReady:
		return domain.ReasonPrecheckPassed
	case domain.RunStateBlocked:
		return domain.ReasonPrecheckBlocked
	case domain.RunStatePartial:
		return domain.ReasonPrecheckPartial
	default:
		return domain.ReasonPrecheckFailed
	}
}

func terminalStageOf(run domain.RunState) string {
	if run == domain.RunStatePartial {
		return string(domain.StageStatePartial)
	}
	return string(domain.StageStateFailed)
}

func reasonCodesOf(state domain.StageReviewState, highest domain.Severity) []string {
	if state == domain.StageStatePassed {
		return []string{}
	}
	codes := []string{domain.ReasonStageBlocked}
	if domain.SeverityRank(highest) >= domain.SeverityRank(domain.SeverityCritical) {
		codes = append(codes, domain.ReasonCriticalSeverityFinding)
	} else {
		codes = append(codes, domain.ReasonHighSeverityFinding)
	}
	return codes
}

func appendIfMissing(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// operationOf extracts the first SQL word from statement text as the operation name (uppercase).
// Deterministic operation summary for FakeRunner; replaced by structured analysis in the B7 real engine.
func operationOf(sql []byte) string {
	i := 0
	n := len(sql)
	for i < n {
		b := sql[i]
		switch {
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			i++
			continue
		case b == '(':
			i++ // Nested leading parentheses
			continue
		case b == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		case b == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
			continue
		default:
			start := i
			for i < n && isOpWordByte(sql[i]) {
				i++
			}
			if i == start {
				return strings.ToUpper(string(sql[start : start+1]))
			}
			return strings.ToUpper(string(sql[start:i]))
		}
	}
	return "UNKNOWN"
}

// statementClassOf classifies statement head -> DDL/DML (schema CHECK ddl|dml;
// read statements are classified under dml to participate in data access review).
func statementClassOf(op string) string {
	switch op {
	case "create", "alter", "drop", "truncate":
		return "ddl"
	default:
		return "dml"
	}
}

// hasWhereClause detects whether an UPDATE/DELETE carries a WHERE clause (word-boundary match,
// ignoring occurrences inside strings or comments; structural heuristic material for fake runner).
func hasWhereClause(sql []byte) bool {
	i, n := 0, len(sql)
	for i < n {
		b := sql[i]
		switch {
		case b == '\'':
			i++
			for i < n && sql[i] != '\'' {
				if sql[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case b == '"' || b == '`':
			q := b
			i++
			for i < n && sql[i] != q {
				i++
			}
			i++
		case b == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case b == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
		case isOpWordByte(b):
			start := i
			for i < n && isOpWordByte(sql[i]) {
				i++
			}
			if strings.EqualFold(string(sql[start:i]), "where") {
				return true
			}
		default:
			i++
		}
	}
	return false
}

func isOpWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// failureNameOf maps an error to its failure name.
func failureNameOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return "INTERNAL"
}

func cryptoEnvelopeOf(p persistence.DraftSQLPayloadModel) crypto.Envelope {
	return crypto.Envelope{
		Ciphertext: p.SQLCiphertext, Nonce: p.SQLNonce,
		KeyID: p.SQLKeyID, Algorithm: p.SQLAlgorithm,
	}
}

// ---- Pure-compute extraction for the grouping phase (RCP-20261101-EPHEMERAL-PRECHECK) ----
// The DB path (groupingPhase) and ephemeral Runs (ephemeral_agent.go) share the exact same
// single-pass streaming analysis; this function never touches the database and returns business errors
// directly for the caller to decide how to converge (DB path = failDraftNoRun; ephemeral = in-memory terminal state).

type analyzedStatement struct {
	res        fingerprint.Result
	operation  string
	class      string // ddl|dml (sql_statements.statement_kind)
	noWhere    bool   // UPDATE/DELETE without WHERE (fake runner structural heuristic)
	objects    []fingerprint.ObjectReference
	pathUnsafe bool
	callables  []postgresCallableRef
	callableOK bool
}

type aggregatedGroup struct {
	canon, dialect              string
	representativeAnonymizedSQL string
	representativeOrdinal       int32
	objects                     []fingerprint.ObjectReference
	objectState                 *objectMergeState
	statementOrdinalRanges      []statementOrdinalRange
	metadataContextComplete     bool
	count                       int
	inMax, valMax               int
	unbounded                   bool      // Group contains an UPDATE/DELETE without WHERE
	facts                       stmtFacts // Statement structural facts within the group (no plaintext identifiers)
}

func analyzeRunStatements(split *changeset.Splitter, profile fingerprint.Profile,
	mapper *privacy.RunMapper, engine *fingerprint.Engine, expectedCount int32,
	beat func(completed, total int64),
) ([]analyzedStatement, []aggregatedGroup, error) {
	var stmts []analyzedStatement
	groupIndex := map[string]int{}
	var aggs []aggregatedGroup
	objectBudget := &objectContextBudget{}
	ordinal := 0
	for {
		st, done, ferr := split.Next()
		if ferr != nil {
			return nil, nil, domain.FailureFromFingerprintCode(fingerprintFailureName(ferr))
		}
		if done {
			break
		}
		ordinal++
		res, aerr := engine.Analyze(profile, st.SQL, mapper)
		if aerr != nil {
			return nil, nil, domain.FailureFromFingerprintCode(fingerprintFailureName(aerr))
		}
		op := strings.ToLower(operationOf(st.SQL))
		statementObjects := objectsAtStatementOrdinal(res.Objects, int32(ordinal))
		callables, callablesOK := postgresCallableRefs(string(st.SQL))
		stmts = append(stmts, analyzedStatement{
			res: res, operation: op, class: statementClassOf(op),
			noWhere: (op == "update" || op == "delete") && !hasWhereClause(st.SQL),
			objects: statementObjects, pathUnsafe: postgresStatementPathUnsafe(string(st.SQL)),
			callables: callables, callableOK: callablesOK,
		})
		key := res.CanonicalFingerprintHMAC + "\x00" + res.DialectFingerprintHMAC
		idx, ok := groupIndex[key]
		if !ok {
			idx = len(aggs)
			groupIndex[key] = idx
			aggs = append(aggs, aggregatedGroup{
				canon: res.CanonicalFingerprintHMAC, dialect: res.DialectFingerprintHMAC,
				representativeAnonymizedSQL: res.AnonymizedSQL,
				representativeOrdinal:       int32(ordinal),
				objects:                     []fingerprint.ObjectReference{},
				objectState:                 newObjectMergeState(),
				statementOrdinalRanges:      []statementOrdinalRange{},
				metadataContextComplete:     true,
			})
		}
		aggs[idx].count++
		var ordinalRangeComplete bool
		aggs[idx].statementOrdinalRanges, ordinalRangeComplete = appendStatementOrdinalRange(
			aggs[idx].statementOrdinalRanges,
			int32(ordinal),
		)
		var objectContextComplete bool
		aggs[idx].objects, objectContextComplete = mergeObjects(
			aggs[idx].objects,
			aggs[idx].objectState,
			statementObjects,
			objectBudget,
		)
		aggs[idx].metadataContextComplete = mergeMetadataContextComplete(
			aggs[idx].metadataContextComplete,
			res.MetadataContextComplete,
		) && ordinalRangeComplete && objectContextComplete
		if stmts[len(stmts)-1].noWhere {
			aggs[idx].unbounded = true
		}
		for _, n := range res.InListSizes {
			if n > aggs[idx].inMax {
				aggs[idx].inMax = n
			}
		}
		if res.TupleCount > aggs[idx].valMax {
			aggs[idx].valMax = res.TupleCount
		}
		mergeFactsInto(&aggs[idx].facts, extractStmtFacts(st.SQL, op, stmts[len(stmts)-1].class))
		if beat != nil && ordinal%5000 == 0 {
			beat(int64(ordinal), int64(expectedCount))
		}
	}
	if ordinal == 0 || int32(ordinal) != expectedCount {
		return nil, nil, &domain.Error{Code: domain.CodeSQLParseFailed, Name: "SQL_PARSE_FAILED"}
	}
	if len(aggs) > 1000 {
		return nil, nil, domain.ErrTooManyFingerprints
	}
	return stmts, aggs, nil
}
