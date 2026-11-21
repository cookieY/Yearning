// ephemeral_agent.go Ephemeral Run executor: in-memory grouping + stage review loop + terminal convergence.
//
// Shares the exact same pure-compute routines as the DB pipeline (fakerunner.groupingPhase / AgentRunner.review:
// analyzeRunStatements / reviewStageCore / evaluateValidityProgram / budget ledger), differing only in where
// results land: all business rows stay in EphemeralRun memory and are written by the materializer upon submit.
// Model calls, budget reservation/settlement, and the per-call reference ledger still write to the database
// per ruling boundaries (pure counts without SQL or finding content).
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// fakeProviderPlanHash is the static plan hash stamped onto review runs using the fallback plan identity.
var fakeProviderPlanHash = sha256Hex([]byte("fake-runner-v1"))

// heavyGroupingSem limits concurrent heavyweight (> ephemeralSyncSyntaxCheckMaxBytes) groupings
// inside a single review worker process to keep peak RSS within the Pod memory budget.
var heavyGroupingSem = make(chan struct{}, 2)

// ExecuteEphemeralRun executes an ephemeral precheck in a goroutine until reaching a terminal state
// (idempotent: returns immediately if already terminal). Any failure converges to an in-memory terminal
// state without ever touching the draft table.
func (r *AgentRunner) ExecuteEphemeralRun(ctx context.Context, er *EphemeralRun) {
	if er.TerminalState() {
		return
	}
	groupErr := r.ephemeralGrouping(ctx, er)
	if groupErr != nil {
		zap.L().Error("ephemeral_run_failed", zap.String("review_run_id", er.ID),
			zap.String("phase", "grouping"), zap.Int32("code", failureCodeOf(groupErr)),
			zap.String("error", groupErr.Error()))
		r.ephemeralFinish(er, domain.RunEventFailed, failureCodeOf(groupErr), failureNameOf(groupErr))
		return
	}
	reviewErr := r.ephemeralReview(ctx, er)
	if reviewErr != nil {
		if ctx.Err() != nil {
			r.ephemeralFinish(er, domain.RunEventIncomplete, 2007, "cancelled")
			return
		}
		zap.L().Error("ephemeral_run_failed", zap.String("review_run_id", er.ID),
			zap.String("phase", "review"), zap.Int32("code", failureCodeOf(reviewErr)),
			zap.String("error", reviewErr.Error()))
		r.ephemeralFinish(er, domain.RunEventFailed, failureCodeOf(reviewErr), failureNameOf(reviewErr))
		return
	}
}

// ephemeralGrouping mirrors the analysis section of groupingPhase: streaming analysis -> in-memory model construction.
func (r *AgentRunner) ephemeralGrouping(ctx context.Context, er *EphemeralRun) error {
	s := r.svc
	stages, err := s.loadFlowStages(ctx, s.DB, er.FlowID)
	if err != nil {
		return err
	}
	if len(stages) == 0 {
		return domain.ErrCoverageIncomplete
	}
	hasPostgresStage := false
	for i := range stages {
		if stages[i].Engine == "postgresql" || stages[i].CompatibilityMode == "postgresql" {
			hasPostgresStage = true
			break
		}
	}

	er.mu.RLock()
	plain := er.SQLPlaintext
	profile := er.Profile
	mapper := er.Mapper
	expectedCount := int32(er.ExpectedStatementCount)
	validateSyntax := !er.syntaxValidated
	runID := er.ID
	er.mu.RUnlock()

	releaseRawSQL := func() {
		er.mu.Lock()
		er.SQLPlaintext = nil
		er.mu.Unlock()
		if s.Ephemeral != nil {
			s.Ephemeral.releaseSQLBytes(er)
		}
	}
	defer releaseRawSQL()

	if len(plain) > ephemeralSyncSyntaxCheckMaxBytes {
		select {
		case heavyGroupingSem <- struct{}{}:
			defer func() { <-heavyGroupingSem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	analysis, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
		runID:           runID,
		rawSQL:          plain,
		profile:         profile,
		mapper:          mapper,
		engine:          s.Engine,
		expectedCount:   expectedCount,
		validateSyntax:  validateSyntax,
		inlineLimit:     ephemeralInlineStatementLimit,
		collectPostgres: hasPostgresStage,
	})
	plain = nil
	releaseRawSQL()
	if err != nil {
		return err
	}

	for i := range stages {
		if stages[i].IdentityFP == nil || *stages[i].IdentityFP == "" {
			return &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"}
		}
	}
	frozenTargets := make(map[int]frozenDatasourceTarget, len(stages))
	for i := range stages {
		st := stages[i]
		target, terr := s.probeFrozenDatasourceTarget(ctx, datasourceTargetRow{
			ID: st.DatasourceID, Engine: st.Engine,
			CompatibilityMode: st.CompatibilityMode,
			Host:              st.Host, Port: st.Port, DatabaseName: st.DatabaseName,
			IdentityFP: st.IdentityFP, Enabled: st.Enabled,
		})
		if terr != nil {
			return domain.ErrCoverageIncomplete
		}
		frozenTargets[st.Position] = target
	}
	postgresContextByPosition := make(map[int]map[string]any, len(stages))
	for i := range stages {
		stage := stages[i]
		extra, perr := postgresContextValueForStage(ctx, s, stage, frozenTargets[stage.Position], analysis.postgresStatements, er.Mapper)
		if perr != nil {
			return domain.ErrCoverageIncomplete
		}
		postgresContextByPosition[stage.Position] = extra
	}
	analysis.postgresStatements = nil
	fpKeyID, err := s.Crypter.HMACKeyID(sqlHMACPurpose)
	if err != nil {
		return err
	}
	now := nowUTC()
	er.mu.Lock()
	defer er.mu.Unlock()
	er.SQLPlaintext = nil
	er.Run = persistence.ReviewRunModel{
		ID: er.ID, DraftID: er.DraftID, DraftRevision: 0,
		State:            string(domain.RunStateRunning),
		FlowSnapshotHash: er.FlowHash, RuleConfigHash: er.RuleHash,
		ToolConfigHash: er.ToolHash, ProviderPlanHash: fakeProviderPlanHash,
		FingerprintAlgorithmVersion:   fingerprint.AlgorithmVersion,
		AnonymizationAlgorithmVersion: "run-mapper-v1",
		StatementCount:                analysis.statementCount,
		FingerprintGroupCount:         int32(len(analysis.groups)),
		BudgetDay:                     now.Format("2006-01-02"),
		Version:                       1,
		CreatedAt:                     er.CreatedAt,
		StartedAt:                     &now,
	}
	er.analyzedStatementCount = analysis.statementCount
	er.Statements = analysis.inlineStatements
	er.repOrdinalToStmtID = analysis.repOrdinalToStmtID
	er.validityTable = analysis.validityTable
	er.validityRefs = analysis.validityRefs

	// Group models (representative statements resolved in O(1) via repOrdinalToStmtID).
	er.Groups = make([]persistence.SQLFingerprintGroupModel, 0, len(analysis.groups))
	er.groupMeta = make(map[string]ephemeralGroupMeta, len(analysis.groups))
	er.repSQLByGroup = make(map[string]string, len(analysis.groups))
	for i := range analysis.groups {
		g := analysis.groups[i]
		repID := analysis.repOrdinalToStmtID[g.representativeOrdinal]
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
		typed, merr := jsonMarshal(shape)
		if merr != nil {
			return merr
		}
		gid := newID()
		er.Groups = append(er.Groups, persistence.SQLFingerprintGroupModel{
			ID: gid, ReviewRunID: er.ID,
			CanonicalFingerprintHMAC: g.canon, DialectFingerprintHMAC: g.dialect,
			AlgorithmVersion: fingerprint.AlgorithmVersion, FingerprintKeyID: fpKeyID,
			TypedShape: typed, InValueCountMax: int32(g.inMax), ValuesRowCountMax: int32(g.valMax),
			StatementCount: int32(g.count), RepresentativeStatementID: repID,
		})
		er.groupMeta[gid] = ephemeralGroupMeta{
			RepKind:      g.repKind,
			RepOperation: g.repOperation,
			SizeMax:      g.sizeMax,
			RepOrdinal:   g.representativeOrdinal,
		}
		er.repSQLByGroup[gid] = g.repSQL
	}
	// Stage snapshot skeletons (identical to groupingPhase: encrypted frozen target context).
	er.Snapshots = make([]persistence.StageReviewSnapshotModel, 0, len(stages))
	for i := range stages {
		st := stages[i]
		contextBase := map[string]any{
			"stage_position": st.Position, "datasource_id": st.DatasourceID,
			"engine": st.Engine, "review_run_id": er.ID,
		}
		for key, value := range postgresContextByPosition[st.Position] {
			contextBase[key] = value
		}
		contextValue, cerr := withFrozenDatasourceTarget(contextBase, frozenTargets[st.Position])
		if cerr != nil {
			return cerr
		}
		env, serr := sealStageReviewContext(s.Crypter, er.ID, int32(st.Position), contextValue)
		if serr != nil {
			return serr
		}
		er.Snapshots = append(er.Snapshots, persistence.StageReviewSnapshotModel{
			ID: newID(), ReviewRunID: er.ID, StagePosition: int32(st.Position),
			DatasourceID: st.DatasourceID, DatasourceIdentityFingerprint: *st.IdentityFP,
			State: string(domain.StageStatePending), HighestSeverity: string(domain.SeverityNone),
			GatePassed: false, GateReasonCodes: persistence.EncodeTextArray(nil),
			NormalizedContextCiphertext: env.Ciphertext, NormalizedContextNonce: env.Nonce,
			NormalizedContextKeyID: env.KeyID, NormalizedContextAlgorithm: env.Algorithm,
			SnapshotHash: sha256Hex([]byte(er.ID + "|" + fmt.Sprint(st.Position) + "|" + *st.IdentityFP)),
			CreatedAt:    now,
		})
	}
	er.StartedAt = &now
	er.state = domain.RunStateRunning
	return nil
}

// ephemeralReview mirrors the review loop of AgentRunner.review (with in-memory persistence).
func (r *AgentRunner) ephemeralReview(ctx context.Context, er *EphemeralRun) error {
	s := r.svc
	er.mu.RLock()
	run := er.Run
	snapshots := append([]persistence.StageReviewSnapshotModel(nil), er.Snapshots...)
	er.mu.RUnlock()
	// Provider chain (fail closed; ledger still writes to DB).
	chain, err := s.LoadProviderChain(ctx, s.DB, r.deps.Dial)
	if err != nil {
		return &domain.Error{Code: 2011, Name: "provider_unavailable"}
	}
	er.mu.Lock()
	if er.Run.ProviderPlanHash != chain.PlanHash() {
		er.Run.ProviderPlanHash = chain.PlanHash()
		er.Run.Version++
	}
	run = er.Run
	er.mu.Unlock()
	// Freeze injection references (in-memory version: reference rows go into memory).
	inj, err := r.ephemeralFreezeInjections(ctx, er, chain)
	if err != nil {
		return err
	}
	schemaBindings := r.frozenStageSchemasForFlow(ctx, s.DB, er.FlowID, &run, snapshots)
	budgetSettings, err := s.LoadBudgetSettings(ctx, s.DB)
	if err != nil {
		return err
	}
	outputLocale := ResolveOutputLocale(er.OutputLocale)
	stageCtx := withRepresentativeSQLResolver(ctx, func(runID, groupID string) (string, bool, error) {
		return ephemeralRepresentativeSQL(er, runID, groupID)
	})
	for i := range snapshots {
		snap := &snapshots[i]
		if domain.StageTerminal(domain.StageReviewState(snap.State)) {
			continue
		}
		if ctx.Err() != nil {
			return &domain.Error{Code: 2007, Name: "REVIEW_COVERAGE_INCOMPLETE"}
		}
		_, stageErr := r.reviewStageCore(stageCtx, s.DB, &run, snap, chain, inj, budgetSettings, er.Mapper,
			schemaBindings, outputLocale, ephemeralValidityOf(er),
			func(datasourceID string) ([]provider.AnonymizedGroup, error) {
				return ephemeralStageGroups(er, datasourceID), nil
			},
			func(c context.Context, out *stageOutcome) error {
				return ephemeralCommitStage(s, er, snap, out)
			})
		if stageErr != nil {
			if errors.Is(stageErr, errInputsDrift) {
				return stageErr
			}
			if errors.Is(stageErr, ErrBudgetExhausted) {
				return &domain.Error{Code: 2010, Name: "budget_exhausted"}
			}
			return stageErr
		}
	}
	// B8 coverage check (in-memory counts).
	er.mu.RLock()
	stmtCount := er.trackedStatementCountLocked()
	var groupSum int64
	for _, g := range er.Groups {
		groupSum += int64(g.StatementCount)
	}
	freshSnaps := append([]persistence.StageReviewSnapshotModel(nil), er.Snapshots...)
	er.mu.RUnlock()
	if stmtCount != int64(run.StatementCount) || groupSum != int64(run.StatementCount) {
		return domain.ErrCoverageIncomplete
	}
	gateInputs := make([]domain.GateInput, 0, len(freshSnaps))
	for i := range freshSnaps {
		gateInputs = append(gateInputs, domain.GateInput{
			StagePosition: int(freshSnaps[i].StagePosition),
			State:         domain.StageReviewState(freshSnaps[i].State),
			HighestFound:  domain.Severity(freshSnaps[i].HighestSeverity),
		})
	}
	gate := domain.EvaluateGate(gateInputs)
	if gate.Passed {
		r.ephemeralFinish(er, domain.RunEventCompleted, 0, "")
		return nil
	}
	severityBlocked := false
	for _, gi := range gateInputs {
		if domain.SeverityRank(gi.HighestFound) >= domain.SeverityRank(domain.BlockSeverityThreshold) {
			severityBlocked = true
		}
	}
	if stagedCoverageIncomplete(freshSnaps) || !severityBlocked {
		r.ephemeralFinish(er, domain.RunEventIncomplete, 2007, "coverage_incomplete")
		return nil
	}
	r.ephemeralFinish(er, domain.RunEventRiskBlocked, 2008, "risk_blocked")
	return nil
}

// ephemeralFinish converges to an in-memory terminal state: Run state machine convergence + remaining snapshot
// convergence; neither writes to the draft nor emits events.
func (r *AgentRunner) ephemeralFinish(er *EphemeralRun, trigger domain.RunTrigger, bizCode int32, failureName string) {
	zap.L().Info("ephemeral_run_finish", zap.String("review_run_id", er.ID),
		zap.String("trigger", string(trigger)), zap.Int32("code", bizCode),
		zap.String("name", failureName))
	er.mu.Lock()
	if domain.RunTerminal(er.state) {
		er.mu.Unlock()
		return
	}
	next, err := domain.RunTransition(er.state, trigger)
	if err != nil {
		next = domain.RunStateFailed
		bizCode, failureName = 2011, "run_converge_failed"
	}
	now := nowUTC()
	er.state = next
	er.Run.State = string(next)
	er.Run.Version++
	er.ErrCode = 0
	er.Failure = ""
	er.Run.BusinessErrCode = nil
	er.Run.FailureName = nil
	if next == domain.RunStateBlocked || next == domain.RunStatePartial || next == domain.RunStateFailed {
		er.ErrCode = bizCode
		er.Failure = failureName
		code := bizCode
		name := failureName
		er.Run.BusinessErrCode = &code
		er.Run.FailureName = &name
	}
	if next == domain.RunStateFailed || next == domain.RunStatePartial {
		terminal := terminalStageOf(next)
		for i := range er.Snapshots {
			st := domain.StageReviewState(er.Snapshots[i].State)
			if st == domain.StageStatePending || st == domain.StageStateRunning {
				er.Snapshots[i].State = terminal
				er.Snapshots[i].GatePassed = false
				er.Snapshots[i].FinishedAt = &now
			}
		}
	}
	er.FinishedAt = &now
	er.Run.FinishedAt = &now
	er.expiresAt = now.Add(r.svc.EphemeralTTL())
	er.SQLPlaintext = nil
	er.validityRefs = nil
	er.validityTable = nil
	er.repSQLByGroup = nil
	er.groupMeta = nil
	if next != domain.RunStateReady || len(er.Statements) > 0 {
		er.repOrdinalToStmtID = nil
	}
	wasBulk := er.bulkSQL
	er.mu.Unlock()

	if r.svc != nil && r.svc.Ephemeral != nil {
		r.svc.Ephemeral.releaseSQLBytes(er)
		r.svc.Ephemeral.enforceTerminalCap()
	}
	if wasBulk {
		debug.FreeOSMemory()
	}
}

// ephemeralFreezeInjections mirrors freezeInjections (in-memory sink; knowledge scope resolved via in-memory groups).
func (r *AgentRunner) ephemeralFreezeInjections(ctx context.Context, er *EphemeralRun, chain *provider.Chain) (*injections, error) {
	if er.Policy == nil {
		return nil, ErrPolicySnapshotInvalid
	}
	er.mu.RLock()
	snapshots := append([]persistence.StageReviewSnapshotModel(nil), er.Snapshots...)
	run := er.Run
	er.mu.RUnlock()
	bindings := r.frozenStageSchemasForFlow(ctx, r.svc.DB, er.FlowID, &run, snapshots)
	inj := &injections{
		SkillsByStage:          map[string][]agent.SkillInjection{},
		KnowledgeByStage:       map[string][]*knowledge.Entry{},
		mandatoryKeysByStage:   map[string]map[string]bool{},
		touchedTablesByStage:   map[string][]string{},
		knowledgeScopeComplete: map[string]bool{},
	}
	for _, raw := range er.Policy.PromptTools {
		inj.Skills = append(inj.Skills, agent.SkillInjection{ToolID: raw.ID, Name: raw.Name, Engine: raw.Engine, Parameters: deepCopyMap(raw.Parameters), KnowledgeText: raw.Definition.KnowledgeText, ConfigHash: raw.ConfigHash, FindingKey: raw.FindingKey, MaxSeverity: raw.Definition.MaxSeverity})
	}
	now := time.Now().UTC()
	for i := range snapshots {
		snap := snapshots[i]
		target, err := frozenDatasourceTargetFromContext(r.svc.Crypter, er.ID, snap.StagePosition, crypto.Envelope{
			Ciphertext: snap.NormalizedContextCiphertext,
			Nonce:      snap.NormalizedContextNonce,
			KeyID:      snap.NormalizedContextKeyID,
			Algorithm:  snap.NormalizedContextAlgorithm,
		})
		if err != nil {
			return nil, err
		}
		groups := ephemeralStageGroups(er, snap.DatasourceID)
		engineSkills := make([]agent.SkillInjection, 0, len(inj.Skills))
		for _, sk := range inj.Skills {
			if sk.MatchesEngine(target.Engine, target.CompatibilityMode) {
				engineSkills = append(engineSkills, sk)
			}
		}
		stageSkills := sortSkillsByDeterminism(filterStageSkills(engineSkills, groups))
		inj.SkillsByStage[snap.ID] = stageSkills
		mandatory := map[string]bool{}
		for _, sk := range stageSkills {
			addMandatoryFindingKey(mandatory, sk.FindingKey)
			er.mu.Lock()
			er.ToolCalls = append(er.ToolCalls, persistence.ReviewToolCallModel{
				ID: newID(), ReviewRunID: er.ID, StageReviewID: &snap.ID,
				ToolKind: "prompt_tool", ToolName: sk.Name,
				InputRefID: strPtr(sk.ToolID), ConfigHash: strPtr(sk.ConfigHash),
				Success: true, StartedAt: now, FinishedAt: now,
			})
			er.mu.Unlock()
		}
		scope, complete, err := resolveKnowledgeScope(snap.DatasourceID, groups, bindings[snap.StagePosition], er.Mapper)
		if err != nil {
			return nil, err
		}
		inj.knowledgeScopeComplete[snap.ID] = complete
		inj.touchedTablesByStage[snap.ID] = append([]string(nil), scope.Tables...)
		entries := []*knowledge.Entry{}
		for _, raw := range er.Policy.KnowledgeEntries {
			entry := &knowledge.Entry{ID: raw.ID, Name: raw.Name, Purpose: raw.Purpose, State: knowledge.StateEnabled, ScopeType: raw.ScopeType, DatasourceID: raw.DatasourceID, DatabaseName: raw.DatabaseName, TableName: raw.TableName, Definition: raw.Definition, ConfigHash: raw.ConfigHash, FindingKey: raw.FindingKey, Version: raw.Version}
			if !entry.Matched(scope) {
				continue
			}
			entries = append(entries, entry)
		}
		knowledge.SortByDeterminism(entries)
		inj.KnowledgeByStage[snap.ID] = entries
		for _, entry := range entries {
			addMandatoryFindingKey(mandatory, entry.FindingKey)
			er.mu.Lock()
			er.ToolCalls = append(er.ToolCalls, persistence.ReviewToolCallModel{
				ID: newID(), ReviewRunID: er.ID, StageReviewID: &snap.ID,
				ToolKind: "knowledge_entry", ToolName: entry.Name,
				InputRefID: strPtr(entry.ID), ConfigHash: strPtr(entry.ConfigHash),
				Success: true, StartedAt: now, FinishedAt: now,
			})
			er.mu.Unlock()
		}
		inj.mandatoryKeysByStage[snap.ID] = mandatory
	}
	return inj, nil
}

// ephemeralCommitStage mirrors commitStage (in-memory sink): evidence raw sealing, tool calls, findings,
// terminal snapshot state, and normalized context resealing (accepted_summary/provenance).
func ephemeralCommitStage(s *Services, er *EphemeralRun, snap *persistence.StageReviewSnapshotModel, out *stageOutcome) error {
	hashInput, err := jsonMarshal(map[string]any{
		"deterministic_summary":  out.summary,
		"accepted_summary":       out.acceptedSummary,
		"accepted_output_locale": out.acceptedOutputLocale,
	})
	if err != nil {
		return err
	}
	snapHash, err := factHMAC(s.Crypter, hashInput)
	if err != nil {
		return err
	}
	highest := domain.SeverityNone
	for i := range out.findings {
		highest = domain.MaxSeverity(highest, out.findings[i].severity)
	}
	stageState := domain.StageStatePassed
	reasons := []string{}
	if domain.SeverityRank(highest) >= domain.SeverityRank(domain.BlockSeverityThreshold) {
		stageState = domain.StageStateBlocked
		reasons = append(reasons, "high_or_critical_finding")
	}
	if out.outcome == "partial" {
		stageState = domain.StageStatePartial
		reasons = append(reasons, "coverage_partial")
	}
	if out.evidenceUnavailable {
		reasons = append(reasons, "evidence_unavailable")
	}
	if out.evidenceIncomplete {
		reasons = append(reasons, "evidence_incomplete")
	}
	now := nowUTC()
	findingIDs := make([]string, len(out.findings))
	provenance := make([]map[string]any, len(out.findings))
	for i, finding := range out.findings {
		findingIDs[i] = newID()
		provenance[i] = findingProvenance(findingIDs[i], finding)
	}
	er.mu.Lock()
	defer er.mu.Unlock()
	for i := range out.evidence {
		e := &out.evidence[i]
		rawEnv, rawHash, rerr := sealRawEvidence(s.Crypter, e.factID, e.raw)
		if rerr != nil {
			return rerr
		}
		er.Facts = append(er.Facts, ephemeralFact{
			ID: e.factID, SnapID: snap.ID, SourceRef: e.source,
			FactEnv: e.env, FactMAC: e.mac,
			RawEnv: rawEnv, RawHash: rawHash, CreatedAt: now,
		})
	}
	er.ToolCalls = append(er.ToolCalls, out.toolCalls...)
	for i, f := range out.findings {
		er.Findings = append(er.Findings, ephemeralFinding{
			ID: findingIDs[i], SnapID: snap.ID, GroupID: f.groupID,
			Category: f.category, Severity: string(f.severity),
			Title: f.title, Message: f.message, Suggestion: f.suggest,
			Evidence: append([]string(nil), f.evidence...), CreatedAt: now,
		})
	}
	// Reseal normalized context (accepted_summary/execution_validity/provenance).
	validityProof, hasValidityProof := out.summary["execution_validity"]
	if out.acceptedSummary != "" || hasValidityProof || len(provenance) > 0 {
		contextValue, oerr := openStageReviewContext(s.Crypter, er.ID, snap.StagePosition, crypto.Envelope{
			Ciphertext: snap.NormalizedContextCiphertext,
			Nonce:      snap.NormalizedContextNonce,
			KeyID:      snap.NormalizedContextKeyID,
			Algorithm:  snap.NormalizedContextAlgorithm,
		})
		if oerr != nil {
			return oerr
		}
		if out.acceptedSummary != "" {
			contextValue["accepted_summary"] = out.acceptedSummary
			contextValue["accepted_output_locale"] = out.acceptedOutputLocale
		}
		if hasValidityProof {
			contextValue["execution_validity"] = validityProof
		}
		contextValue["finding_provenance"] = provenance
		env, serr := sealStageReviewContext(s.Crypter, er.ID, snap.StagePosition, contextValue)
		if serr != nil {
			return serr
		}
		er.ContextEnvelopes[snap.ID] = env
	}
	// Terminal snapshot state (in-memory model converges directly; ContextEnvelopes takes precedence over skeleton ciphertext).
	for i := range er.Snapshots {
		if er.Snapshots[i].ID != snap.ID {
			continue
		}
		er.Snapshots[i].State = string(stageState)
		er.Snapshots[i].HighestSeverity = string(highest)
		er.Snapshots[i].GatePassed = stageState == domain.StageStatePassed
		er.Snapshots[i].GateReasonCodes = persistence.EncodeTextArray(reasons)
		er.Snapshots[i].SnapshotHash = snapHash
		er.Snapshots[i].FinishedAt = &now
		if env, ok := er.ContextEnvelopes[snap.ID]; ok {
			er.Snapshots[i].NormalizedContextCiphertext = env.Ciphertext
			er.Snapshots[i].NormalizedContextNonce = env.Nonce
			er.Snapshots[i].NormalizedContextKeyID = env.KeyID
			er.Snapshots[i].NormalizedContextAlgorithm = env.Algorithm
			delete(er.ContextEnvelopes, snap.ID)
		}
		break
	}
	return nil
}

// ephemeralStageGroups assembles AnonymizedGroup slices from in-memory group models (column-for-column mirror
// of stageGroups row assembly: M7 shape synthesis + maximum single-statement byte count within the group).
func ephemeralStageGroups(er *EphemeralRun, datasourceID string) []provider.AnonymizedGroup {
	er.mu.RLock()
	defer er.mu.RUnlock()
	out := []provider.AnonymizedGroup{}
	for gi := range er.Groups {
		g := &er.Groups[gi]
		var repKind, repOp string
		var sizeMax int64
		if er.groupMeta != nil {
			meta, ok := er.groupMeta[g.ID]
			if !ok {
				continue
			}
			repKind = meta.RepKind
			repOp = meta.RepOperation
			sizeMax = meta.SizeMax
		} else {
			var rep *persistence.SQLStatementModel
			for i := range er.Statements {
				if er.Statements[i].ID == g.RepresentativeStatementID {
					rep = &er.Statements[i]
					break
				}
			}
			if rep == nil {
				continue
			}
			repKind = rep.StatementKind
			repOp = rep.Operation
			for i := range er.Statements {
				st := &er.Statements[i]
				if st.CanonicalFingerprintHMAC == g.CanonicalFingerprintHMAC &&
					st.DialectFingerprintHMAC == g.DialectFingerprintHMAC &&
					int64(st.SizeBytes) > sizeMax {
					sizeMax = int64(st.SizeBytes)
				}
			}
		}
		stats := map[string]any{}
		if len(g.TypedShape) > 0 {
			if err := json.Unmarshal(g.TypedShape, &stats); err != nil {
				continue
			}
		}
		var metadata struct {
			SQL           string                               `json:"representative_anonymized_sql"`
			Objects       []provider.AnonymizedObjectReference `json:"objects"`
			Complete      bool                                 `json:"metadata_context_complete"`
			Ordinal       int32                                `json:"statement_ordinal"`
			OrdinalRanges []provider.StatementOrdinalRange     `json:"statement_ordinal_ranges"`
		}
		if err := json.Unmarshal(g.TypedShape, &metadata); err != nil {
			continue
		}
		delete(stats, "representative_anonymized_sql")
		delete(stats, "objects")
		delete(stats, "metadata_context_complete")
		delete(stats, "statement_ordinal")
		delete(stats, "statement_ordinal_ranges")
		stats["max_statement_bytes"] = sizeMax
		shape := fmt.Sprintf("kind=%v op=%v where=%v",
			repKind, repOp, !boolValue(stats["unbounded_mutation"]))
		out = append(out, provider.AnonymizedGroup{
			GroupID: g.ID, RepresentativeShape: shape, StatementCount: int64(g.StatementCount), Statistics: stats,
			RepresentativeAnonymizedSQL: metadata.SQL, Objects: metadata.Objects,
			MetadataContextComplete: metadata.Complete,
			StatementOrdinal:        metadata.Ordinal, StatementOrdinalRanges: metadata.OrdinalRanges,
		})
	}
	return out
}

// ephemeralValidityOf evaluates execution preconditions from in-memory data: program/group mappings are
// constructed from in-memory models (mirroring loadValidityProgram row logic).
func ephemeralValidityOf(er *EphemeralRun) stageValidityFunc {
	return func(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
		binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string,
		refs []provider.NormalizedEvidenceRef,
	) (ValidityResult, []mappedFinding, map[string]any, error) {
		er.mu.RLock()
		if len(er.validityRefs) > 0 {
			refsCompact := er.validityRefs
			table := er.validityTable
			stmtCount := run.StatementCount
			groupIDs := make([]string, len(er.Groups))
			for i := range er.Groups {
				groupIDs[i] = er.Groups[i].ID
			}
			er.mu.RUnlock()
			program := ValidityProgram{
				Complete:   int32(len(refsCompact)) == stmtCount,
				Statements: make([]StatementValidityAtOrdinal, len(refsCompact)),
			}
			groupIdxByOrd := make([]uint16, len(refsCompact))
			for i, ref := range refsCompact {
				program.Statements[i] = StatementValidityAtOrdinal{
					Ordinal: int32(i + 1),
					Facts:   table[ref.validityIdx],
				}
				groupIdxByOrd[i] = ref.groupIdx
			}
			return evaluateValidityProgramWithGroupLookup(ctx, program, func(ord int32) string {
				idx := int(ord - 1)
				if idx < 0 || idx >= len(groupIdxByOrd) {
					return ""
				}
				gi := int(groupIdxByOrd[idx])
				if gi < 0 || gi >= len(groupIDs) {
					return ""
				}
				return groupIDs[gi]
			}, binding, mapper, engine, locale, refs)
		}
		program := ValidityProgram{Complete: int32(len(er.Statements)) == run.StatementCount}
		groups := map[int32]string{}
		for i := range er.Statements {
			st := &er.Statements[i]
			var shape struct {
				Validity fingerprint.StatementValidity `json:"execution_validity"`
			}
			if err := jsonUnmarshalStrict(st.TypedShape, &shape); err != nil {
				er.mu.RUnlock()
				return ValidityResult{}, nil, nil, err
			}
			if st.Ordinal != int32(i+1) {
				program.Complete = false
			}
			gid := ""
			for gi := range er.Groups {
				g := &er.Groups[gi]
				if g.CanonicalFingerprintHMAC == st.CanonicalFingerprintHMAC &&
					g.DialectFingerprintHMAC == st.DialectFingerprintHMAC {
					gid = g.ID
					break
				}
			}
			if gid == "" {
				program.Complete = false
			}
			program.Statements = append(program.Statements, StatementValidityAtOrdinal{Ordinal: st.Ordinal, Facts: shape.Validity})
			groups[st.Ordinal] = gid
		}
		er.mu.RUnlock()
		return evaluateValidityProgram(ctx, program, groups, binding, mapper, engine, locale, refs)
	}
}

func strPtr(v string) *string { return &v }

func addMandatoryFindingKey(dst map[string]bool, findingKey string) {
	if findingKey == "" {
		return
	}
	dst[findingKey] = true
}

// ephemeralRepresentativeSQL resolves the representative statement SQL for a fingerprint group directly from
// EphemeralRun memory so built-in tools such as safe_explain can inspect execution plans before materialization.
func ephemeralRepresentativeSQL(er *EphemeralRun, runID, groupID string) (string, bool, error) {
	if er == nil || er.ID != runID {
		return "", false, nil
	}
	er.mu.RLock()
	if sqlText, ok := er.repSQLByGroup[groupID]; ok && sqlText != "" {
		er.mu.RUnlock()
		return sqlText, true, nil
	}
	var repStmtID string
	for i := range er.Groups {
		if er.Groups[i].ID == groupID && er.Groups[i].ReviewRunID == runID {
			repStmtID = er.Groups[i].RepresentativeStatementID
			break
		}
	}
	var ordinal int32
	found := false
	if repStmtID != "" {
		for i := range er.Statements {
			if er.Statements[i].ID == repStmtID {
				ordinal = er.Statements[i].Ordinal
				found = true
				break
			}
		}
	}
	plain := er.SQLPlaintext
	profile := er.Profile
	er.mu.RUnlock()
	if !found {
		return "", true, ErrFactSourceUnavailable
	}
	splitter := changeset.NewSplitter(bytes.NewReader(plain), profile, 524288)
	for {
		stmt, done, serr := splitter.Next()
		if serr != nil {
			return "", true, ErrFactSourceUnavailable
		}
		if done {
			return "", true, ErrFactSourceUnavailable
		}
		if int32(stmt.Index) == ordinal {
			return string(stmt.SQL), true, nil
		}
	}
}

// findingProvenance is encrypted with the stage context and bound to the
// existing finding ID. It records the runtime classification without extending
// the public finding schema or storing model-supplied source identifiers.
func findingProvenance(id string, f mappedFinding) map[string]any {
	cause, sourceKey := f.cause, ""
	switch cause {
	case "policy_finding":
		sourceKey = f.sourceKey // assigned only after frozen mandatory-key lookup
	case "proven_invalid_execution":
	default:
		cause = "agent_observation"
	}
	return map[string]any{"finding_id": id, "cause": cause, "source_key": sourceKey,
		"fingerprint_group_id": f.groupID, "evidence_ids": append([]string(nil), f.evidence...)}
}
