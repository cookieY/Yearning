// agentrunner.go production review Runner: Provider chain + bounded model loop + read-only
// tool evidence collection + deterministic injection (skills + internal knowledge) + budget reservation/ledger.
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"time"
)

func (r *AgentRunner) reviewStageCore(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	snap *persistence.StageReviewSnapshotModel, chain *provider.Chain, inj *injections,
	budgetSettings BudgetSettings, runMapper *privacy.RunMapper,
	schemaBindings map[int32]*stageSchemaBinding, outputLocale string,
	validity stageValidityFunc, groupsOf stageGroupsFunc, commit stageCommitFunc) (string, error) {
	s := r.svc
	target, err := frozenDatasourceTargetFromContext(s.Crypter, run.ID, snap.StagePosition, crypto.Envelope{
		Ciphertext: snap.NormalizedContextCiphertext,
		Nonce:      snap.NormalizedContextNonce,
		KeyID:      snap.NormalizedContextKeyID,
		Algorithm:  snap.NormalizedContextAlgorithm,
	})
	if err != nil {
		return "", errFrozenDatasourceTarget
	}
	if err := s.verifyFrozenDatasourceConfig(ctx, db, target); err != nil {
		return "", err
	}
	// Anonymized statement groups for this stage (lexical facts: skills make judgments based on these statistics).
	groups, err := groupsOf(snap.DatasourceID)
	if err != nil {
		return "", err
	}
	groups, objectScope, contextComplete, incompleteReason := bindGroupMetadataWithReason(groups, schemaBindings[snap.StagePosition], runMapper)
	// Stage engine is read from the encrypted frozen target; configuration drift must not alter skill or protocol selection.
	stageEngine := target.Engine
	var stageSkills []agent.SkillInjection
	if inj.SkillsByStage != nil {
		if prefiltered, ok := inj.SkillsByStage[snap.ID]; ok {
			stageSkills = append([]agent.SkillInjection(nil), prefiltered...)
		}
	}
	if stageSkills == nil && len(inj.Skills) > 0 {
		engineSkills := make([]agent.SkillInjection, 0, len(inj.Skills))
		for _, sk := range inj.Skills {
			if sk.MatchesEngine(stageEngine, target.CompatibilityMode) {
				engineSkills = append(engineSkills, sk)
			}
		}
		stageSkills = sortSkillsByDeterminism(filterStageSkills(engineSkills, groups))
	}
	stageKnowledge := append([]*knowledge.Entry(nil), inj.KnowledgeByStage[snap.ID]...)
	knowledge.SortByDeterminism(stageKnowledge)
	for _, skill := range stageSkills {
		if err := agent.ValidatePolicyParameters(skill.KnowledgeText, skill.Parameters); err != nil {
			// Log only the stable skill name and error, never knowledge text (parameter validation runs before privacy checks).
			zap.L().Error("policy_parameters_invalid",
				zap.String("review_run_id", run.ID),
				zap.Int64("stage_position", int64(snap.StagePosition)),
				zap.String("prompt_tool_name", skill.Name))
			return "", &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "policy_parameter_invalid"}
		}
	}
	for _, entry := range stageKnowledge {
		if entry == nil || agent.ValidatePolicyParameters(entry.Definition.KnowledgeText, nil) != nil {
			entryName := ""
			if entry != nil {
				entryName = entry.Name
			}
			zap.L().Error("policy_parameters_invalid",
				zap.String("review_run_id", run.ID),
				zap.Int64("stage_position", int64(snap.StagePosition)),
				zap.String("knowledge_entry_name", entryName))
			return "", &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "policy_parameter_invalid"}
		}
	}
	stageMandatoryKeys := inj.mandatoryKeysByStage[snap.ID]
	stageAdvisoryCaps := inj.advisoryKnowledgeCapsByStage[snap.ID]
	// NEW-AI-03: pre-tokenize a controlled anonymous legend (schemas + tables touched by statements)
	// from the stage's frozen Schema binding set — allowing the model to invoke tools using legend tokens
	// on turn 1 and eliminating the circular dependency where tokens only exist after a tool returns.
	stageLegend := buildTokenLegend(runMapper, schemaBindings[snap.StagePosition], nil)
	stageLegend = metadataTokenLegend(stageLegend, groups)
	rb := r.runtimeBudgetOf()
	targetContext, err := providerTargetContext(target)
	if err != nil {
		return "", err
	}
	out := &stageOutcome{
		mapper:             runMapper,
		evidenceIncomplete: !contextComplete || !inj.knowledgeScopeComplete[snap.ID],
		incompleteReason:   incompleteReason,
	}
	var lastResult *provider.Result
	evidenceRefs := []provider.NormalizedEvidenceRef{}
	sanEvidenceRefs := []provider.NormalizedEvidenceRef{}
	var cachedSANGroups []provider.AnonymizedGroup
	factByHash := map[string]string{}
	executedCalls := map[string]string{}
	seenStoredCalls := map[string]bool{}
	toolCalls := 0
	modelCalls := 0
	localeFailures := 0
	schemaRetries := 0
	deadline := time.Now().Add(time.Duration(rb.MaxDurationSec) * time.Second)
	var lastResp *provider.Response
	var policyObjects []provider.AnonymizedObjectReference
	binding := schemaBindings[snap.StagePosition]

	// Unified context_window-driven rule budget planning (shared identically by SAN
	// and default endpoints). Single-batch fits fall straight into the zero-overhead
	// turn loop below; multi-batch plans pre-hydrate NormalizedEvidence once and run
	// bounded concurrent batches followed by deterministic finding merge.
	ruleBatchCount := 1
	var precompiled compiledPolicy
	hasPrecompiled := false
	runSingleBatch := true
	isBlocking := isBlockingSchemaFailureReason(incompleteReason)
	if !contextComplete && (incompleteReason == "datasource_database_required" ||
		(isBlocking && (binding == nil || !binding.defensible))) {
		return "", &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: incompleteReason}
	}
	if budgetEp, hasContextWindow := selectBudgetEndpoint(chain); hasContextWindow && (len(stageSkills) > 0 || len(stageKnowledge) > 0) {
		if !contextComplete && isBlocking {
			return "", &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: incompleteReason}
		}
		compiled, stageCatalog, compileErr := r.compileStagePolicyWithCatalog(ctx, snap, target, run.ID, stageSkills, stageKnowledge,
			runMapper, binding, objectScope)
		if compileErr != nil {
			zap.L().Error("policy_compile_failed",
				zap.String("review_run_id", run.ID),
				zap.Int64("stage_position", int64(snap.StagePosition)),
				zap.Int("skills", len(stageSkills)),
				zap.Int("knowledge_entries", len(stageKnowledge)),
				zap.String("reason", failureNameOf(compileErr)),
				zap.String("error", compileErr.Error()))
			return "", coverageFailureOf(compileErr)
		}
		precompiled = compiled
		hasPrecompiled = true
		objectScope = compiled.scope
		policyObjects = compiled.objects

		buildForBudget := func(ev, sanEv []provider.NormalizedEvidenceRef) ruleBatchRequestBuilder {
			return func(reqCtx context.Context, rawSk, compSk []agent.SkillInjection, rawKb, compKb []*knowledge.Entry) (provider.Request, error) {
				return r.buildStageEndpointRequest(reqCtx, stagePromptRequest{
					db:                 db,
					runID:              run.ID,
					ruleConfigHash:     []byte(run.RuleConfigHash),
					target:             target,
					targetContext:      targetContext,
					groups:             groups,
					policyObjects:      compiled.objects,
					rawSkills:          rawSk,
					compiledSkills:     compSk,
					rawKnowledge:       rawKb,
					compiledKnowledge:  compKb,
					evidence:           ev,
					sanEvidence:        sanEv,
					runMapper:          runMapper,
					stageLegend:        stageLegend,
					outputLocale:       outputLocale,
					evidenceIncomplete: out.evidenceIncomplete,
					cachedSANGroups:    &cachedSANGroups,
				}, budgetEp)
			}
		}

		initialBatches, plan, planErr := planRuleBatches(ctx, budgetEp, stageSkills, compiled.skills, stageKnowledge, compiled.knowledge, buildForBudget(evidenceRefs, sanEvidenceRefs))
		if planErr != nil {
			if errors.Is(planErr, ErrRuleExceedsContextWindow) {
				zap.L().Error("rule_exceeds_context_window",
					zap.String("review_run_id", run.ID),
					zap.Int64("stage_position", int64(snap.StagePosition)),
					zap.Int("context_window", plan.contextWindow),
					zap.Int("base_tokens", plan.baseTokens),
					zap.Int("output_reserve", plan.outputReserve),
					zap.String("error", planErr.Error()))
				return "", planErr
			}
			return "", domain.ErrCoverageIncomplete
		}

		if len(initialBatches) > 1 {
			runSingleBatch = false
			store := newStageEvidenceStore(r, run.ID, snap, target, binding, runMapper, objectScope, rb)
			if preErr := store.prehydrate(ctx, groups, compiled.objects); preErr != nil {
				return "", preErr
			}
			preEv, preSANEv := store.initialEvidenceSnapshot()
			batches, finalPlan, finalPlanErr := planRuleBatches(ctx, budgetEp, stageSkills, compiled.skills, stageKnowledge, compiled.knowledge, buildForBudget(preEv, preSANEv))
			if finalPlanErr != nil {
				if errors.Is(finalPlanErr, ErrRuleExceedsContextWindow) {
					zap.L().Error("rule_exceeds_context_window",
						zap.String("review_run_id", run.ID),
						zap.Int64("stage_position", int64(snap.StagePosition)),
						zap.Int("context_window", finalPlan.contextWindow),
						zap.Int("base_tokens", finalPlan.baseTokens),
						zap.Int("output_reserve", finalPlan.outputReserve),
						zap.String("error", finalPlanErr.Error()))
					return "", finalPlanErr
				}
				return "", domain.ErrCoverageIncomplete
			}

			// Warm SAN groups once before spawning concurrent batch goroutines so
			// shared group slices are strictly read-only during parallel execution.
			for _, ep := range chain.Endpoints {
				if endpointIsSAN(ep) {
					if _, gerr := prepareSANGroups(ctx, db, s.Crypter, run.ID, target.Engine+"/"+target.CompatibilityMode, groups, runMapper, &cachedSANGroups); gerr != nil {
						return "", domain.ErrCoverageIncomplete
					}
					break
				}
			}

			ruleBatchCount = len(batches)
			batchResults, batchErr := r.executeRuleBatches(ctx, stageBatchExecInput{
				db:             db,
				run:            run,
				snap:           snap,
				chain:          chain,
				target:         target,
				targetContext:  targetContext,
				groups:         groups,
				sanGroups:      cachedSANGroups,
				policyObjects:  compiled.objects,
				stageCatalog:   stageCatalog,
				objectScope:    objectScope,
				stageLegend:    stageLegend,
				budgetSettings: budgetSettings,
				runMapper:      runMapper,
				binding:        binding,
				outputLocale:   outputLocale,
				rb:             rb,
				deadline:       deadline,
				store:          store,
				baseIncomplete: out.evidenceIncomplete,
			}, batches)
			if batchErr != nil {
				return "", batchErr
			}

			evDrafts, storeCalls, storeRefs, storeIncomplete, storeReason := store.finalSnapshot()
			out.evidence = append(out.evidence, evDrafts...)
			out.toolCalls = append(out.toolCalls, storeCalls...)
			evidenceRefs = storeRefs
			if storeIncomplete {
				out.evidenceIncomplete = true
				if out.incompleteReason == "" && storeReason != "" {
					out.incompleteReason = storeReason
				}
			}

			allBatchFindings := make([][]provider.Finding, 0, len(batchResults))
			anyPartial := false
			anyOpenTools := false
			anyNilResp := false
			for _, br := range batchResults {
				if br.lastResult != nil {
					lastResult = br.lastResult
				}
				if br.evidenceIncomplete {
					out.evidenceIncomplete = true
				}
				if br.outcome == "partial" {
					anyPartial = true
				}
				if br.lastResp == nil {
					anyNilResp = true
					out.evidenceIncomplete = true
					continue
				}
				if len(br.lastResp.ToolRequests) > 0 {
					anyOpenTools = true
					out.evidenceIncomplete = true
				}
				allBatchFindings = append(allBatchFindings, br.lastResp.Findings)
			}

			if len(allBatchFindings) > 0 || !anyNilResp {
				mergedOutcome := "complete"
				if anyNilResp || anyPartial || anyOpenTools || out.evidenceIncomplete {
					mergedOutcome = "partial"
				}
				var openTools []provider.ToolRequest
				if anyOpenTools {
					openTools = []provider.ToolRequest{{ToolName: "unresolved_batch_tool_request"}}
				}
				summaryText := ""
				if !anyNilResp {
					summaryText = mergeBatchSummaries(batchResults)
				}
				lastResp = &provider.Response{
					SchemaVersion: 1,
					ReviewRunID:   run.ID,
					Outcome:       mergedOutcome,
					Summary:       summaryText,
					Findings:      mergeBatchFindings(allBatchFindings),
					ToolRequests:  openTools,
				}
				out.outcome = mergedOutcome
			}
		}
	}
	if !hasPrecompiled && contextComplete && binding != nil && binding.defensible && binding.hasSQLDerivedSchemas {
		if verifyErr := r.verifySQLDerivedSchemas(ctx, snap, target, run.ID, binding); verifyErr != nil {
			return "", coverageFailureOf(verifyErr)
		}
	}

	if runSingleBatch {
		singleState := &singleBatchStageState{
			out:             out,
			objectScope:     objectScope,
			policyObjects:   policyObjects,
			evidenceRefs:    evidenceRefs,
			sanEvidenceRefs: sanEvidenceRefs,
			cachedSANGroups: &cachedSANGroups,
			factByHash:      factByHash,
			executedCalls:   executedCalls,
			seenStoredCalls: seenStoredCalls,
			toolCalls:       toolCalls,
			modelCalls:      modelCalls,
			localeFailures:  localeFailures,
			schemaRetries:   schemaRetries,
			lastResult:      lastResult,
			lastResp:        lastResp,
		}
		if err := r.runSingleBatchStage(ctx, singleBatchStageInput{
			db:             db,
			run:            run,
			snap:           snap,
			chain:          chain,
			target:         target,
			targetContext:  targetContext,
			groups:         groups,
			stageSkills:    stageSkills,
			stageKnowledge: stageKnowledge,
			stageLegend:    stageLegend,
			budgetSettings: budgetSettings,
			runMapper:      runMapper,
			binding:        binding,
			outputLocale:   outputLocale,
			rb:             rb,
			deadline:       deadline,
			precompiled:    precompiled,
			hasPrecompiled: hasPrecompiled,
		}, singleState); err != nil {
			return "", err
		}
		objectScope = singleState.objectScope
		policyObjects = singleState.policyObjects
		evidenceRefs = singleState.evidenceRefs
		sanEvidenceRefs = singleState.sanEvidenceRefs
		lastResult = singleState.lastResult
		lastResp = singleState.lastResp
	}

	if out.outcome == "" && lastResp != nil {
		out.outcome = lastResp.Outcome
	}
	if lastResp == nil || len(lastResp.ToolRequests) > 0 {
		out.evidenceIncomplete = true
	}
	if !objectScope.observed(evidenceRefs) || !metadataAdditionalFactsObserved(append(append([]provider.AnonymizedGroup{}, groups...), provider.AnonymizedGroup{Objects: policyObjects}), evidenceRefs) {
		out.evidenceIncomplete = true
	}
	validityRes, technicalFindings, validityProof, err := validity(ctx, db, run,
		schemaBindings[snap.StagePosition], runMapper, stageEngine, outputLocale, evidenceRefs)
	if err != nil {
		return "", err
	}
	out.findings = append(out.findings, technicalFindings...)
	if validityRes.Status == ValidityUnknown {
		out.evidenceIncomplete = true
	}
	if out.evidenceIncomplete {
		out.outcome = "partial"
	}
	// NEW-AI-03: zero Observed evidence marker (all tools failed / all bindings rejected / no tools called
	// and no tokenized facts produced) — allows submission gates and UI to recognize unavailable evidence
	// rather than silently degrading to model_only.
	out.evidenceUnavailable = len(evidenceRefs) == 0
	// Finding mapping validation (group existence / evidence existence / severity allowlist).
	groupIDs := map[string]bool{}
	for _, g := range groups {
		groupIDs[g.GroupID] = true
	}
	evidenceIDs := map[string]bool{}
	for _, e := range evidenceRefs {
		evidenceIDs[e.EvidenceID] = true
	}
	if lastResp == nil {
		out.summary = map[string]any{"stage_position": snap.StagePosition, "model_outcome": "partial",
			"evidence_unavailable": out.evidenceUnavailable, "execution_validity": validityProof}
		return out.outcome, commit(ctx, out)
	}
	for _, f := range lastResp.Findings {
		severity, accepted := governFindingSeverity(f.FindingKey, f.Severity, stageMandatoryKeys, stageAdvisoryCaps)
		if !accepted {
			continue // Severity outside the allowlist for a governed Finding -> reject
		}
		cause, sourceKey := "agent_observation", ""
		if stageMandatoryKeys[f.FindingKey] {
			cause, sourceKey = "policy_finding", f.FindingKey
		} else if _, isAdvisory := stageAdvisoryCaps[f.FindingKey]; isAdvisory {
			cause, sourceKey = "knowledge_advisory", f.FindingKey
		}
		ev := []string{}
		if f.FactBasis == "evidence" {
			for _, eid := range f.EvidenceIDs {
				if evidenceIDs[eid] {
					ev = append(ev, eid)
				} else {
					out.evidenceIncomplete = true
				}
			}
			if len(ev) == 0 {
				out.evidenceIncomplete = true
				continue // Database fact claim without existing evidence -> reject
			}
		}
		// L8: persist one row per referenced group (group-level visibility).
		for _, gid := range f.FingerprintGroupIDs {
			if !groupIDs[gid] {
				out.evidenceIncomplete = true
				continue
			}
			out.findings = append(out.findings, mappedFinding{
				cause: cause, sourceKey: sourceKey,
				groupID: gid, category: f.Category, severity: domain.Severity(severity),
				title: f.Title, message: f.Message, suggest: f.Suggestion, evidence: ev,
			})
		}
	}
	if out.evidenceIncomplete {
		out.outcome = "partial"
	}
	// Accept human text only after all runtime evidence and finding-reference
	// checks. A downgraded result must not retain a claim of completion.
	if len(lastResp.ToolRequests) == 0 && out.outcome == lastResp.Outcome {
		out.acceptedSummary = lastResp.Summary
		out.acceptedOutputLocale = outputLocale
	}
	if validityRes.Status == ValidityInvalid && out.outcome != "partial" {
		out.acceptedSummary = "\u5b58\u5728\u76ee\u6807\u5bf9\u8c61\u72b6\u6001\u4e0e\u8bed\u53e5\u524d\u63d0\u4e0d\u7b26\u7684\u8bed\u53e5\uff08\u8be6\u89c1\u98ce\u9669\u53d1\u73b0\uff09\uff0c\u5df2\u963b\u65ad\u63d0\u4ea4\u3002"
		if outputLocale == "en-US" {
			out.acceptedSummary = "At least one statement's object state contradicts its execution prerequisite (see findings). Submission is blocked."
		}
		out.acceptedOutputLocale = outputLocale
	}
	providerName, modelName, fallbackCount := providerNameOf(chain), "unknown", 0
	if lastResult != nil {
		providerName, modelName, fallbackCount = lastResult.ProviderName, lastResult.ModelName, lastResult.FallbackCount
	}
	out.summary = map[string]any{
		"stage_position":       snap.StagePosition,
		"datasource_id":        snap.DatasourceID,
		"model_outcome":        out.outcome,
		"provider_name":        providerName,
		"model_name":           modelName,
		"fallback_count":       fallbackCount,
		"findings":             len(out.findings),
		"tool_calls":           len(out.toolCalls),
		"knowledge_entries":    len(stageKnowledge),
		"skills":               len(stageSkills),
		"rule_batches":         ruleBatchCount,
		"evidence_unavailable": out.evidenceUnavailable,
		"evidence_incomplete":  out.evidenceIncomplete,
		"execution_validity":   validityProof,
	}
	return out.outcome, commit(ctx, out)
}

// runSingleBatchStage executes the iterative model-and-tool loop for stages whose rules fit in one batch.
func (r *AgentRunner) runSingleBatchStage(
	ctx context.Context,
	in singleBatchStageInput,
	st *singleBatchStageState,
) error {
	localePending := false
	schemaPending := false
	for turn := 0; turn < in.rb.MaxModelTurns; turn++ {
		if ctx.Err() != nil {
			return &domain.Error{Code: 2007, Name: "REVIEW_COVERAGE_INCOMPLETE"}
		}
		if time.Now().After(in.deadline) {
			if localePending {
				return &domain.Error{Code: 2009, Name: "REVIEW_SCHEMA_INVALID"}
			}
			st.out.outcome = "partial"
			break
		}
		if st.modelCalls >= in.rb.MaxModelCalls {
			st.out.outcome = "partial"
			break
		}
		var compiled compiledPolicy
		if turn == 0 && in.hasPrecompiled {
			compiled = in.precompiled
		} else {
			var compileErr error
			compiled, compileErr = r.compileStagePolicy(ctx, in.snap, in.target, in.run.ID, in.stageSkills, in.stageKnowledge,
				in.runMapper, in.binding, st.objectScope)
			if compileErr != nil {
				zap.L().Error("policy_compile_failed",
					zap.String("review_run_id", in.run.ID),
					zap.Int64("stage_position", int64(in.snap.StagePosition)),
					zap.Int("skills", len(in.stageSkills)),
					zap.Int("knowledge_entries", len(in.stageKnowledge)),
					zap.String("reason", failureNameOf(compileErr)),
					zap.String("error", compileErr.Error()))
				return coverageFailureOf(compileErr)
			}
		}
		st.objectScope = compiled.scope
		st.policyObjects = compiled.objects

		buildReqForEndpoint := func(reqCtx context.Context, ep provider.Endpoint) (provider.Request, error) {
			return r.buildStageEndpointRequest(reqCtx, stagePromptRequest{
				db:                 in.db,
				runID:              in.run.ID,
				ruleConfigHash:     []byte(in.run.RuleConfigHash),
				target:             in.target,
				targetContext:      in.targetContext,
				groups:             in.groups,
				policyObjects:      compiled.objects,
				rawSkills:          in.stageSkills,
				compiledSkills:     compiled.skills,
				rawKnowledge:       in.stageKnowledge,
				compiledKnowledge:  compiled.knowledge,
				evidence:           st.evidenceRefs,
				sanEvidence:        st.sanEvidenceRefs,
				runMapper:          in.runMapper,
				stageLegend:        in.stageLegend,
				outputLocale:       in.outputLocale,
				evidenceIncomplete: st.out.evidenceIncomplete,
				localePending:      localePending,
				schemaPending:      schemaPending,
				cachedSANGroups:    st.cachedSANGroups,
			}, ep)
		}

		canRetry := turn+1 < in.rb.MaxModelTurns && st.modelCalls+1 < in.rb.MaxModelCalls && time.Now().Before(in.deadline)
		turnOut := r.invokeStageModelTurn(ctx, stageModelTurnInput{
			db:              in.db,
			runID:           in.run.ID,
			stagePosition:   in.snap.StagePosition,
			chain:           in.chain,
			budgetSettings:  in.budgetSettings,
			perModelTimeout: in.rb.PerModelTimeout,
			outputLocale:    in.outputLocale,
			canRetry:        canRetry,
			localeFailures:  &st.localeFailures,
			schemaRetries:   &st.schemaRetries,
			nextCallSeq: func() int {
				st.modelCalls++
				return st.modelCalls
			},
			buildReq: buildReqForEndpoint,
			sendGuard: func(sendCtx context.Context) error {
				if _, err := r.compileStagePolicy(sendCtx, in.snap, in.target, in.run.ID, in.stageSkills, in.stageKnowledge, in.runMapper, in.binding, st.objectScope); err != nil {
					return err
				}
				return nil
			},
		})
		if turnOut.err != nil {
			return turnOut.err
		}
		if turnOut.retry == modelTurnRetryLocale {
			localePending = true
			continue
		}
		if turnOut.retry == modelTurnRetrySchema {
			schemaPending = true
			continue
		}

		result := turnOut.result
		st.lastResp = result.Response
		localePending = false
		schemaPending = false
		st.lastResult = result
		if len(result.Response.ToolRequests) == 0 {
			break
		}

		type boundToolCall struct {
			req       provider.ToolRequest
			args      map[string]any
			modelArgs map[string]any
			storedID  string
		}
		liveCalls := []boundToolCall{}
		for _, tr := range result.Response.ToolRequests {
			if st.toolCalls >= in.rb.MaxToolCalls {
				st.out.evidenceIncomplete = true
				break
			}
			signature, err := toolRequestSignature(tr)
			if err != nil {
				st.out.evidenceIncomplete = true
				continue
			}
			storedID := storedToolCallID(in.run.ID, in.snap.ID, tr.ToolCallID, signature)
			if prior, seen := st.executedCalls[tr.ToolCallID]; seen {
				if prior != signature {
					st.out.evidenceIncomplete = true
					if !st.seenStoredCalls[storedID] {
						st.toolCalls++
						now := time.Now().UTC()
						st.out.toolCalls = append(st.out.toolCalls, persistence.ReviewToolCallModel{
							ID: storedID, ReviewRunID: in.run.ID, StageReviewID: &in.snap.ID,
							ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
							Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
						})
						st.seenStoredCalls[storedID] = true
					}
				}
				continue
			}
			st.executedCalls[tr.ToolCallID] = signature
			st.seenStoredCalls[storedID] = true
			st.toolCalls++

			callArgs := tr.Arguments
			translationOK := true
			if resultEndpointIsSAN(in.chain, result) {
				callArgs, translationOK = translateSANToolArgsToTokens(tr.Arguments, in.runMapper, in.binding)
			}
			var modelArgs map[string]any
			var resolved map[string]any
			var bindingOK bool
			if translationOK {
				modelArgs = detokenizeToolArgs(callArgs, in.runMapper)
				resolved, bindingOK = resolveStageBindingToolArgs(in.binding, modelArgs)
			}
			if !translationOK || !knownToolIdentifiers(callArgs, in.runMapper) || !st.objectScope.allows(callArgs) || !bindingOK {
				st.out.evidenceIncomplete = true
				now := time.Now().UTC()
				st.out.toolCalls = append(st.out.toolCalls, persistence.ReviewToolCallModel{
					ID: storedID, ReviewRunID: in.run.ID, StageReviewID: &in.snap.ID,
					ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
					Success: false, ErrorCode: factErrCodePtr(2012),
					StartedAt: now, FinishedAt: now,
				})
				continue
			}
			reqCopy := tr
			reqCopy.Arguments = callArgs
			liveCalls = append(liveCalls, boundToolCall{
				req: reqCopy, args: resolved, modelArgs: modelArgs, storedID: storedID,
			})
		}
		if len(liveCalls) == 0 {
			continue
		}

		turnErr := func() error {
			factSrc, closeSrc, ferr := r.deps.FactSourceFor(ctx, in.snap.DatasourceID, in.target, in.run.ID)
			if ferr != nil {
				st.out.evidenceIncomplete = true
				if st.out.incompleteReason == "" {
					st.out.incompleteReason = factSourceFailureReason(ferr)
				}
				for _, bc := range liveCalls {
					now := time.Now().UTC()
					st.out.toolCalls = append(st.out.toolCalls, persistence.ReviewToolCallModel{
						ID: bc.storedID, ReviewRunID: in.run.ID, StageReviewID: &in.snap.ID,
						ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: bc.req.ToolName,
						Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
					})
				}
				return nil
			}
			defer closeSrc()
			factSrc = bindSchemaFactSource(factSrc, in.binding)
			for _, bc := range liveCalls {
				execOut, execErr := r.executeValidatedToolCall(
					ctx, factSrc, bc.storedID, bc.req.ToolName, bc.args, bc.req.Arguments, bc.modelArgs, in.runMapper, in.binding,
				)
				st.out.toolCalls = append(st.out.toolCalls, persistence.ReviewToolCallModel{
					ID: execOut.res.ToolCallID, ReviewRunID: in.run.ID, StageReviewID: &in.snap.ID,
					ToolKind: string(execOut.res.ToolKind), ToolName: execOut.res.ToolName,
					Success: execOut.res.Success, ErrorCode: execOut.res.ErrorCode,
					StartedAt: execOut.res.StartedAt, FinishedAt: execOut.res.FinishedAt,
				})
				if !execOut.res.Success {
					st.out.evidenceIncomplete = true
					continue
				}
				if execErr != nil {
					return execErr
				}
				if execOut.metadataIncomplete {
					st.out.evidenceIncomplete = true
				}
				if priorFactID, ok := st.factByHash[execOut.mac]; ok {
					st.evidenceRefs = append(st.evidenceRefs, provider.NormalizedEvidenceRef{
						EvidenceID: priorFactID, FactKind: execOut.res.ToolName, Fact: execOut.res.NormalizedFact})
					st.sanEvidenceRefs = append(st.sanEvidenceRefs, provider.NormalizedEvidenceRef{
						EvidenceID: priorFactID, FactKind: execOut.res.ToolName, Fact: execOut.sanFact})
					continue
				}
				st.factByHash[execOut.mac] = execOut.factID
				st.out.evidence = append(st.out.evidence, evidenceDraft{
					factID: execOut.factID, source: "tool_call/" + execOut.res.ToolCallID, env: execOut.env, mac: execOut.mac, raw: execOut.rawFact,
				})
				st.evidenceRefs = append(st.evidenceRefs, provider.NormalizedEvidenceRef{
					EvidenceID: execOut.factID, FactKind: execOut.res.ToolName, Fact: execOut.res.NormalizedFact})
				st.sanEvidenceRefs = append(st.sanEvidenceRefs, provider.NormalizedEvidenceRef{
					EvidenceID: execOut.factID, FactKind: execOut.res.ToolName, Fact: execOut.sanFact})
			}
			return nil
		}()
		if turnErr != nil {
			return turnErr
		}
	}
	return nil
}
