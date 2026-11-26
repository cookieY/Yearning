// ephemeral_agent_stage.go holds ephemeral_agent domain helpers and operations.
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"go.uber.org/zap"
	"runtime/debug"
	"time"
)

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
			zap.String("name", failureNameOf(groupErr)),
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
			zap.String("name", failureNameOf(reviewErr)),
			zap.String("error", reviewErr.Error()))
		r.ephemeralFinish(er, domain.RunEventFailed, failureCodeOf(reviewErr), failureNameOf(reviewErr))
		return
	}
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
	// Compute stage schema bindings once per ephemeral review and reuse for knowledge scoping and stage review.
	schemaBindings := r.frozenStageSchemasForFlow(
		ctx, s.DB, er.FlowID, &run, snapshots,
		func(datasourceID string) []provider.AnonymizedGroup {
			return ephemeralStageGroups(er, datasourceID)
		},
		er.Mapper,
		er.TargetDatabase,
	)
	inj, err := r.ephemeralFreezeInjections(ctx, er, snapshots, schemaBindings)
	if err != nil {
		return err
	}
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
		er.mu.RLock()
		incompleteReason := er.stageIncompleteReason
		er.mu.RUnlock()
		if incompleteReason == "" {
			incompleteReason = "coverage_incomplete"
		}
		r.ephemeralFinish(er, domain.RunEventIncomplete, 2007, incompleteReason)
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
func (r *AgentRunner) ephemeralFreezeInjections(
	ctx context.Context,
	er *EphemeralRun,
	snapshots []persistence.StageReviewSnapshotModel,
	bindings map[int32]*stageSchemaBinding,
) (*injections, error) {
	if er.Policy == nil {
		return nil, ErrPolicySnapshotInvalid
	}
	inj := &injections{
		SkillsByStage:                map[string][]agent.SkillInjection{},
		KnowledgeByStage:             map[string][]*knowledge.Entry{},
		mandatoryKeysByStage:         map[string]map[string]bool{},
		advisoryKnowledgeCapsByStage: map[string]map[string]string{},
		touchedTablesByStage:         map[string][]string{},
		knowledgeScopeComplete:       map[string]bool{},
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
				InputRefID: ptr.To(sk.ToolID), ConfigHash: ptr.To(sk.ConfigHash),
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
		advisoryCaps := map[string]string{}
		for _, entry := range entries {
			if entry.FindingKey != "" {
				advisoryCaps[entry.FindingKey] = entry.Definition.EffectiveAdvisorySeverity()
			}
			er.mu.Lock()
			er.ToolCalls = append(er.ToolCalls, persistence.ReviewToolCallModel{
				ID: newID(), ReviewRunID: er.ID, StageReviewID: &snap.ID,
				ToolKind: "knowledge_entry", ToolName: entry.Name,
				InputRefID: ptr.To(entry.ID), ConfigHash: ptr.To(entry.ConfigHash),
				Success: true, StartedAt: now, FinishedAt: now,
			})
			er.mu.Unlock()
		}
		inj.mandatoryKeysByStage[snap.ID] = mandatory
		inj.advisoryKnowledgeCapsByStage[snap.ID] = advisoryCaps
	}
	return inj, nil
}

// ephemeralCommitStage mirrors commitStage (in-memory sink): evidence raw sealing, tool calls, findings,
// terminal snapshot state, and normalized context resealing (accepted_summary/provenance).
func ephemeralCommitStage(s *Services, er *EphemeralRun, snap *persistence.StageReviewSnapshotModel, out *stageOutcome) error {
	hashInput, err := json.Marshal(map[string]any{
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
	if (out.outcome == "partial" || out.evidenceIncomplete) && out.incompleteReason != "" && er.stageIncompleteReason == "" {
		er.stageIncompleteReason = out.incompleteReason
	}
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

// buildGroupTypedShape encodes the typed_shape JSON payload for a single aggregated group.
func buildGroupTypedShape(g aggregatedGroup) ([]byte, error) {
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
	if g.objectRangesTruncated {
		shape["object_ranges_truncated"] = true
	}
	for k, v := range g.facts.m {
		shape[k] = v
	}
	return json.Marshal(shape)
}

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
	case "policy_finding", "knowledge_advisory":
		sourceKey = f.sourceKey // assigned only after frozen policy or advisory knowledge key lookup
	case "proven_invalid_execution":
	default:
		cause = "agent_observation"
	}
	return map[string]any{"finding_id": id, "cause": cause, "source_key": sourceKey,
		"fingerprint_group_id": f.groupID, "evidence_ids": append([]string(nil), f.evidence...)}
}
