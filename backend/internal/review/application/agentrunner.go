// agentrunner.go production review Runner: Provider chain + bounded model loop + read-only
// tool evidence collection + deterministic injection (skills + internal knowledge) + budget reservation/ledger.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// AgentDeps injects external wiring for AgentRunner (replaceable in tests).
type AgentDeps struct {
	// Dial constructs a Provider Adapter (production: OpenAI; test: Fake).
	Dial func(ep provider.Endpoint) (provider.Client, error)
	// FactSourceFor constructs the evidence collection port and its release function for a frozen datasource target
	// (runID is passed for safe_explain to resolve representative statements).
	FactSourceFor func(ctx context.Context, datasourceID string, target FrozenDatasourceTarget, runID string) (tool.FactSource, func(), error)
	// RuntimeBudget provides injectable runtime bounds (nil = PRD defaults).
	RuntimeBudget *RuntimeBudgetConfig
}

// AgentRunner is the production ephemeral review executor.
type AgentRunner struct {
	svc      *Services
	deps     AgentDeps
	registry *tool.Registry
	budget   RuntimeBudgetConfig
}

// RuntimeBudgetConfig defines injectable runtime bounds (defaulting to PRD 9.5).
type RuntimeBudgetConfig struct {
	PerStageEstimateMinor int64 // Per-stage reservation estimate (in minor currency units).
	// Below are PRD 9.5 bound overrides (0 = corresponding default value).
	MaxModelTurns   int
	MaxModelCalls   int
	MaxToolCalls    int
	MaxDurationSec  int
	PerModelTimeout int // seconds
	PerToolTimeout  int // seconds
}

// NewAgentRunner constructs an AgentRunner.
func NewAgentRunner(svc *Services, deps AgentDeps) *AgentRunner {
	perStage := int64(100)
	if deps.RuntimeBudget != nil {
		perStage = deps.RuntimeBudget.PerStageEstimateMinor
		if perStage <= 0 {
			perStage = 100
		}
	}
	return &AgentRunner{svc: svc, deps: deps,
		registry: tool.NewRegistry(), budget: RuntimeBudgetConfig{PerStageEstimateMinor: perStage}}
}

// runtimeBudgetOf returns effective runtime bounds (falling back to PRD defaults when injected fields are 0).
func (r *AgentRunner) runtimeBudgetOf() agent.RuntimeBudget {
	def := agent.DefaultRuntimeBudget()
	if r.deps.RuntimeBudget == nil {
		return def
	}
	get := func(v, d int) int {
		if v > 0 {
			return v
		}
		return d
	}
	return agent.RuntimeBudget{
		MaxModelTurns:   get(r.deps.RuntimeBudget.MaxModelTurns, def.MaxModelTurns),
		MaxModelCalls:   get(r.deps.RuntimeBudget.MaxModelCalls, def.MaxModelCalls),
		MaxToolCalls:    get(r.deps.RuntimeBudget.MaxToolCalls, def.MaxToolCalls),
		MaxDurationSec:  get(r.deps.RuntimeBudget.MaxDurationSec, def.MaxDurationSec),
		PerModelTimeout: get(r.deps.RuntimeBudget.PerModelTimeout, def.PerModelTimeout),
		PerToolTimeout:  get(r.deps.RuntimeBudget.PerToolTimeout, def.PerToolTimeout),
	}
}

// injections holds the frozen injection set.
type injections struct {
	Skills           []agent.SkillInjection
	SkillsByStage    map[string][]agent.SkillInjection
	KnowledgeByStage map[string][]*knowledge.Entry
	// mandatoryKeysByStage maps stage snapshot id to frozen mandatory
	// finding keys. Any actual hit is effective Critical.
	mandatoryKeysByStage map[string]map[string]bool
	// touchedTablesByStage holds tables referenced by statements in each stage (NEW-AI-03
	// token legend input; shares the same source as knowledge matching, frozen in the reference write transaction).
	touchedTablesByStage   map[string][]string
	knowledgeScopeComplete map[string]bool
}

// stageOutcome holds stage review artifacts (collected outside the stage transaction, persisted inside it).
type stageOutcome struct {
	mapper               *privacy.RunMapper
	toolCalls            []persistence.ReviewToolCallModel
	evidence             []evidenceDraft
	findings             []mappedFinding
	summary              map[string]any
	acceptedSummary      string
	acceptedOutputLocale string
	outcome              string // complete|partial
	// evidenceUnavailable NEW-AI-03: zero Observed evidence in this stage (all tools failed or produced no
	// tokenized facts) — marks silent degradation of the review to model_only, surfaced via gate_reason_codes
	// and summary to the submission gate and UI.
	evidenceUnavailable bool
	evidenceIncomplete  bool
}

// evidenceDraft holds pending evidence to persist (ciphertext + fact ID + controlled raw bytes).
type evidenceDraft struct {
	factID string
	source string
	env    crypto.Envelope
	mac    string
	raw    []byte
}

// mappedFinding holds a validated finding.
type mappedFinding struct {
	cause     string
	sourceKey string
	groupID   string
	category  string
	severity  domain.Severity
	title     string
	message   string
	suggest   string
	evidence  []string
}

// governFindingSeverity separates mandatory frozen policy findings from
// model-native observations. Every actual violation of a mandatory key is
// Critical regardless of the severity proposed by the model or its legacy
// max_severity metadata. An unknown key remains visible as an observation but
// cannot create a High/Critical business block.
func governFindingSeverity(key, severity string, mandatoryKeys map[string]bool) (string, bool) {
	switch severity {
	case "info", "low", "medium", "high", "critical":
	default:
		return "", false
	}
	if mandatoryKeys[key] {
		return "critical", true
	}
	if severity == "high" || severity == "critical" {
		return "medium", true
	}
	return severity, true
}

// stageValidityFunc is the pluggable data-fetching side for stage execution precondition evaluation
// (DB path = reads from DB; ephemeral Run = built from in-memory models — RCP-20261101-EPHEMERAL-PRECHECK).
type stageValidityFunc func(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string,
	refs []provider.NormalizedEvidenceRef) (ValidityResult, []mappedFinding, map[string]any, error)

// stageGroupsFunc fetches stage anonymized groups (DB path = reads from DB; ephemeral = in-memory group models).
type stageGroupsFunc func(datasourceID string) ([]provider.AnonymizedGroup, error)

// stageCommitFunc is the persistence sink for stage conclusions (DB path = commitStage transaction; ephemeral = in-memory append).
type stageCommitFunc func(ctx context.Context, out *stageOutcome) error

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
	groups, objectScope, contextComplete := bindGroupMetadata(groups, schemaBindings[snap.StagePosition], runMapper)
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
			return "", domain.ErrCoverageIncomplete
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
			return "", domain.ErrCoverageIncomplete
		}
	}
	stageMandatoryKeys := inj.mandatoryKeysByStage[snap.ID]
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
	out := &stageOutcome{mapper: runMapper, evidenceIncomplete: !contextComplete || !inj.knowledgeScopeComplete[snap.ID]}
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
	if budgetEp, hasContextWindow := selectBudgetEndpoint(chain); hasContextWindow && (len(stageSkills) > 0 || len(stageKnowledge) > 0) {
		compiled, stageCatalog, compileErr := r.compileStagePolicyWithCatalog(ctx, snap, target, run.ID, stageSkills, stageKnowledge,
			runMapper, binding, objectScope)
		if compileErr != nil {
			zap.L().Error("policy_compile_failed",
				zap.String("review_run_id", run.ID),
				zap.Int64("stage_position", int64(snap.StagePosition)),
				zap.Int("skills", len(stageSkills)),
				zap.Int("knowledge_entries", len(stageKnowledge)),
				zap.String("error", compileErr.Error()))
			return "", domain.ErrCoverageIncomplete
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

			evDrafts, storeCalls, storeRefs, storeIncomplete := store.finalSnapshot()
			out.evidence = append(out.evidence, evDrafts...)
			out.toolCalls = append(out.toolCalls, storeCalls...)
			evidenceRefs = storeRefs
			if storeIncomplete {
				out.evidenceIncomplete = true
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
		severity, accepted := governFindingSeverity(f.FindingKey, f.Severity, stageMandatoryKeys)
		if !accepted {
			continue // Severity outside the allowlist for a governed Finding -> reject
		}
		cause, sourceKey := "agent_observation", ""
		if stageMandatoryKeys[f.FindingKey] {
			cause, sourceKey = "policy_finding", f.FindingKey
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

type modelTurnRetryKind uint8

const (
	modelTurnNoRetry modelTurnRetryKind = iota
	modelTurnRetryLocale
	modelTurnRetrySchema
)

type stageModelTurnInput struct {
	db               *gorm.DB
	runID            string
	stagePosition    int32
	chain            *provider.Chain
	budgetSettings   BudgetSettings
	perModelTimeout  int
	outputLocale     string
	canRetry         bool
	localeFailures   *int
	schemaRetries    *int
	preserveCanceled bool
	nextCallSeq      func() int
	buildReq         func(context.Context, provider.Endpoint) (provider.Request, error)
	sendGuard        func(context.Context) error
}

type stageModelTurnOutcome struct {
	result *provider.Result
	retry  modelTurnRetryKind
	err    error
}

// invokeStageModelTurn reserves daily budget, invokes the provider chain, settles ledger usage,
// records per-call usage, and classifies failures or corrective retries consistently.
func (r *AgentRunner) invokeStageModelTurn(ctx context.Context, in stageModelTurnInput) stageModelTurnOutcome {
	s := r.svc
	moneyCtx := context.WithoutCancel(ctx)
	day := s.budgetDay(nowUTC())
	if err := s.Reserve(moneyCtx, in.db, day, in.budgetSettings.Currency, in.budgetSettings, r.budget.PerStageEstimateMinor); err != nil {
		if errors.Is(err, ErrBudgetExhausted) {
			return stageModelTurnOutcome{err: ErrBudgetExhausted}
		}
		return stageModelTurnOutcome{err: err}
	}

	callSeq := in.nextCallSeq()
	callCtx, cancelCall := context.WithTimeout(ctx, time.Duration(in.perModelTimeout)*time.Second)
	result, cerr := in.chain.ReviewStructuredPerEndpoint(callCtx, in.buildReq, in.sendGuard)
	cancelCall()

	var localeErr error
	if cerr == nil {
		localeErr = agent.ValidateOutputLocale(result.Response, in.outputLocale)
		if localeErr != nil {
			cerr = localeErr
			*in.localeFailures++
		}
	}
	if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
		if _, _, _, attempted := provider.ChainErrorInfo(cerr); !attempted {
			if err := s.Release(moneyCtx, in.db, day, in.budgetSettings.Currency, r.budget.PerStageEstimateMinor); err != nil {
				return stageModelTurnOutcome{err: err}
			}
			return stageModelTurnOutcome{err: domain.ErrCoverageIncomplete}
		}
	}

	lid, lmodel, usage := ledgerUsageOf(result, in.chain, cerr)
	settleAmount := tokenBasedAmount(usage.InputTokens, usage.OutputTokens,
		in.budgetSettings.InputPricePerMillionMinor, in.budgetSettings.OutputPricePerMillionMinor,
		r.budget.PerStageEstimateMinor)
	if lerr := s.Settle(moneyCtx, in.db, in.runID, lid, lmodel,
		int16(min(callSeq, 3)), usage.InputTokens, usage.OutputTokens, settleAmount,
		in.budgetSettings.Currency, day, ledgerOutcome(cerr)); lerr != nil {
		return stageModelTurnOutcome{err: lerr}
	}

	refCalls := provider.ChainErrorCalls(cerr)
	if result != nil {
		refCalls = result.Calls
	}
	for i, cr := range refCalls {
		if rerr := s.RecordCallUsage(moneyCtx, in.db, in.runID, in.stagePosition,
			callSeq, i+1, cr); rerr != nil {
			return stageModelTurnOutcome{err: rerr}
		}
	}

	if cerr != nil {
		if rem := unreleasedReserveMinor(r.budget.PerStageEstimateMinor, settleAmount); rem > 0 {
			if rerr := s.Release(moneyCtx, in.db, day, in.budgetSettings.Currency, rem); rerr != nil {
				return stageModelTurnOutcome{err: rerr}
			}
		}
		if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
			zap.L().Error("policy_compile_failed",
				zap.String("review_run_id", in.runID),
				zap.Int64("stage_position", int64(in.stagePosition)),
				zap.String("error", cerr.Error()))
			return stageModelTurnOutcome{err: domain.ErrCoverageIncomplete}
		}
		if in.preserveCanceled && ctx.Err() != nil && errors.Is(cerr, context.Canceled) {
			return stageModelTurnOutcome{err: ctx.Err()}
		}
		if errors.Is(cerr, provider.ErrSchemaInvalid) {
			if localeErr != nil && *in.localeFailures == 1 && in.canRetry {
				return stageModelTurnOutcome{retry: modelTurnRetryLocale}
			}
			if *in.schemaRetries < 2 && in.canRetry {
				*in.schemaRetries++
				return stageModelTurnOutcome{retry: modelTurnRetrySchema}
			}
			return stageModelTurnOutcome{err: &domain.Error{Code: 2009, Name: "REVIEW_SCHEMA_INVALID"}}
		}
		zap.L().Error("ai_provider_call_failed",
			zap.String("review_run_id", in.runID),
			zap.Int64("stage_position", int64(in.stagePosition)),
			zap.String("provider_error", cerr.Error()))
		return stageModelTurnOutcome{err: &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}}
	}

	return stageModelTurnOutcome{result: result}
}

type validatedToolCallOutcome struct {
	res                tool.Result
	sanFact            map[string]any
	factID             string
	factJSON           []byte
	rawFact            []byte
	env                crypto.Envelope
	mac                string
	metadataIncomplete bool
}

// executeValidatedToolCall executes a scope-validated tool request against factSrc, projects and verifies
// scope bindings, tokenizes identifiers before sealing NormalizedFact, and encrypts the AEAD payload.
func (r *AgentRunner) executeValidatedToolCall(
	ctx context.Context,
	factSrc tool.FactSource,
	storedID string,
	toolName string,
	resolvedArgs map[string]any,
	tokenArgs map[string]any,
	modelArgs map[string]any,
	runMapper *privacy.RunMapper,
	binding *stageSchemaBinding,
) (validatedToolCallOutcome, error) {
	res := r.registry.Execute(ctx, factSrc, storedID, toolName, resolvedArgs)
	if res.Success {
		if perr := projectToolFactSchema(res.NormalizedFact, tokenArgs, runMapper, binding); perr != nil {
			res.Success = false
			res.NormalizedFact = nil
			res.ErrorCode = factErrCodePtr(2012)
		}
	}
	if res.Success && !factMatchesRequestedScope(toolName, res.NormalizedFact, modelArgs) {
		res.Success = false
		res.NormalizedFact = nil
		res.ErrorCode = factErrCodePtr(2012)
	}
	var sanFact map[string]any
	if res.Success {
		sanFact = copyNormalizedFactForSAN(res.NormalizedFact, res.FactSchemaVersion)
	}
	if res.Success {
		if terr := tokenizeToolFactWithBindings(res.NormalizedFact, runMapper, tokenArgs); terr != nil {
			res.Success = false
			res.NormalizedFact = nil
			res.ErrorCode = factErrCodePtr(2012)
		}
	}
	if !res.Success {
		return validatedToolCallOutcome{res: res}, nil
	}

	res.NormalizedFact = cloneValidityFact(res.NormalizedFact)
	res.NormalizedFact["fact_schema_version"] = res.FactSchemaVersion
	metadataIncomplete := !metadataFactComplete(res.NormalizedFact)
	factID := newID()
	factJSON, err := json.Marshal(res.NormalizedFact)
	if err != nil {
		return validatedToolCallOutcome{res: res}, err
	}
	env, mac, err := sealPayload(r.svc.Crypter, "evidence_facts", factID, "normalized_fact_ciphertext", factJSON)
	if err != nil {
		return validatedToolCallOutcome{res: res}, err
	}
	raw := res.RawFact
	if raw == nil {
		raw = factJSON
	}
	return validatedToolCallOutcome{
		res:                res,
		sanFact:            sanFact,
		factID:             factID,
		factJSON:           factJSON,
		rawFact:            raw,
		env:                env,
		mac:                mac,
		metadataIncomplete: metadataIncomplete,
	}, nil
}

type singleBatchStageInput struct {
	db             *gorm.DB
	run            *persistence.ReviewRunModel
	snap           *persistence.StageReviewSnapshotModel
	chain          *provider.Chain
	target         frozenDatasourceTarget
	targetContext  string
	groups         []provider.AnonymizedGroup
	stageSkills    []agent.SkillInjection
	stageKnowledge []*knowledge.Entry
	stageLegend    []agent.TokenLegendEntry
	budgetSettings BudgetSettings
	runMapper      *privacy.RunMapper
	binding        *stageSchemaBinding
	outputLocale   string
	rb             agent.RuntimeBudget
	deadline       time.Time
	precompiled    compiledPolicy
	hasPrecompiled bool
}

type singleBatchStageState struct {
	out             *stageOutcome
	objectScope     metadataScope
	policyObjects   []provider.AnonymizedObjectReference
	evidenceRefs    []provider.NormalizedEvidenceRef
	sanEvidenceRefs []provider.NormalizedEvidenceRef
	cachedSANGroups *[]provider.AnonymizedGroup
	factByHash      map[string]string
	executedCalls   map[string]string
	seenStoredCalls map[string]bool
	toolCalls       int
	modelCalls      int
	localeFailures  int
	schemaRetries   int
	lastResult      *provider.Result
	lastResp        *provider.Response
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
					zap.String("error", compileErr.Error()))
				return domain.ErrCoverageIncomplete
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
					return ErrPolicyPrivacy
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

// buildStageEndpointRequest delegates request formatting to either the SAN
// renderer or the default prompt renderer while keeping filtering, budgeting,
// and batching completely decoupled from prompt formatting.
func (r *AgentRunner) buildStageEndpointRequest(
	reqCtx context.Context,
	in stagePromptRequest,
	ep provider.Endpoint,
) (provider.Request, error) {
	if endpointIsSAN(ep) {
		if in.crypter == nil {
			in.crypter = r.svc.Crypter
		}
		return buildSANStageRequest(reqCtx, in)
	}
	cin := agent.ContextInput{
		EngineProfile:    in.target.Engine + "/" + in.target.CompatibilityMode,
		OutputLocale:     in.outputLocale,
		RuleSnapshot:     in.ruleConfigHash,
		Skills:           in.compiledSkills,
		Knowledge:        in.compiledKnowledge,
		ObjectReferences: in.policyObjects,
		Groups:           in.groups,
		Evidence:         in.evidence,
		TokenLegend:      metadataTokenLegend(in.stageLegend, []provider.AnonymizedGroup{{Objects: in.policyObjects}}),
	}
	req := agent.BuildRequest(in.runID, cin, r.registry)
	req.SystemPrompt += in.targetContext
	if in.localePending {
		req.SystemPrompt += "\nThe previous response did not meet the output-language requirement. Regenerate all human text fields in the required language; preserve all review, evidence, privacy and severity rules.\n"
	}
	if in.schemaPending {
		req.SystemPrompt += "\nThe previous response violated the required output schema. Return strictly schema-conformant structured output only; fix field types and required fields, preserve all review, evidence, privacy and severity rules.\n"
	}
	if in.evidenceIncomplete {
		req.SystemPrompt += "\nA requested fact tool failed or returned incomplete/unknown context. This stage cannot be complete. Preserve observed findings and return outcome=partial.\n"
	}
	return req, nil
}

// boolValue reads a jsonb boolean fault-tolerantly.
func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

// ledgerUsageOf resolves Provider/Model/usage for the ledger (on failure, prefers the actual candidate
// hit from chainError, falling back to the primary candidate in the chain).
func ledgerUsageOf(result *provider.Result, chain *provider.Chain, cerr error) (string, string, provider.Usage) {
	if result != nil && result.Response != nil {
		return result.ProviderID, result.ModelName, result.Usage
	}
	if cerr != nil {
		if pid, model, _, ok := provider.ChainErrorInfo(cerr); ok {
			return pid, model, provider.Usage{}
		}
	}
	if len(chain.Endpoints) == 0 {
		return "00000000-0000-0000-0000-000000000000", "unknown", provider.Usage{}
	}
	return chain.Endpoints[0].ProviderID, chain.Endpoints[0].ModelName, provider.Usage{}
}

// tokenBasedAmount OBS-002: bills by actual token usage and configured unit prices; falls back to the
// fixed per-stage estimate when unit prices are unconfigured (both 0). A single call bills at least 1 minor currency unit.
// SEC-0039: all multiplications and additions use checked arithmetic capped at math.MaxInt64 — under
// extreme unit prices or usage, int64 wraparound would turn a huge amount into a tiny one and underestimate
// budget accounting; capping errs on the side of overestimation (budget-side fail safe) without introducing negatives or wraparound.
// TokenBasedAmount computes the billed amount in minor currency units from token usage and unit prices.
func TokenBasedAmount(inputTokens, outputTokens, inputPrice, outputPrice, fallbackMinor int64) int64 {
	return tokenBasedAmount(inputTokens, outputTokens, inputPrice, outputPrice, fallbackMinor)
}

func tokenBasedAmount(inputTokens, outputTokens, inputPrice, outputPrice, fallbackMinor int64) int64 {
	if inputPrice <= 0 && outputPrice <= 0 {
		return fallbackMinor
	}
	amount := addCeil(milliCeilCapped(max(inputTokens, 0), max(inputPrice, 0)),
		milliCeilCapped(max(outputTokens, 0), max(outputPrice, 0)))
	if amount < 1 {
		amount = 1
	}
	return amount
}

// milliCeilCapped returns ceil(tokens * pricePerMillion / 1_000_000).
// Splits into quotient and remainder multiplication, capping at math.MaxInt64 on overflow at any step.
func milliCeilCapped(tokens, price int64) int64 {
	if tokens <= 0 || price <= 0 {
		return 0
	}
	q := tokens / 1_000_000
	r := tokens % 1_000_000
	total := mulCeil(q, price)
	if total == math.MaxInt64 {
		return math.MaxInt64
	}
	if r > 0 {
		part := mulCeil(r, price)
		if part == math.MaxInt64 {
			return math.MaxInt64
		}
		ceil := part / 1_000_000
		if part%1_000_000 != 0 {
			ceil = addCeil(ceil, 1)
		}
		total = addCeil(total, ceil)
	}
	return total
}

// mulCeil performs non-negative checked multiplication, capping at math.MaxInt64 on overflow.
// Overflow is checked using the exact form a > Max/b (floor(Max/b)*b <= Max always holds,
// whereas the inverse form would falsely cap on exact division boundaries).
func mulCeil(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// addCeil performs non-negative checked addition, capping at math.MaxInt64 on overflow.
func addCeil(a, b int64) int64 {
	if a < 0 || b < 0 {
		return 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// unreleasedReserveMinor computes the remaining reservation to release on the failure path (SEC-0011):
// reserved amount minus settled amount (Settle has already transferred settleAmount out of reserved_minor);
// when the settled amount exceeds the reservation, Settle's greatest(..., 0) has already absorbed the difference, leaving a balance of 0.
func unreleasedReserveMinor(reservedMinor, settledMinor int64) int64 {
	if reservedMinor <= 0 {
		return 0
	}
	if settledMinor <= 0 {
		return reservedMinor
	}
	return max(reservedMinor-settledMinor, 0)
}

// factErrCodePtr returns a pointer to the tool error code for tool fact tokenization failure (2012).
func factErrCodePtr(c int) *int { return &c }

func providerNameOf(chain *provider.Chain) string {
	if len(chain.Endpoints) == 0 {
		return ""
	}
	return chain.Endpoints[0].Name
}

func ledgerOutcome(err error) string {
	if err == nil {
		return "succeeded"
	}
	if errors.Is(err, provider.ErrSchemaInvalid) {
		return "schema_failed"
	}
	if errors.Is(err, provider.ErrRejected) {
		return "permanent_failed"
	}
	return "transient_failed"
}

var controlledServerVersion = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4}){0,3}$`)

// providerTargetContext exposes only the dialect and numeric server version
// recorded by the successful frozen identity probe. It never serializes the
// target itself or claims that the live server has been reprobed this turn.
func providerTargetContext(target FrozenDatasourceTarget) (string, error) {
	profile := fingerprint.Profile{Engine: target.Engine, Mode: target.CompatibilityMode}
	if profile.Validate() != nil || target.EngineFamily != target.CompatibilityMode || !controlledServerVersion.MatchString(target.ServerVersion) {
		return "", errFrozenDatasourceTarget
	}
	return fmt.Sprintf("\n<frozen-target-context>engine_profile=%s; server_version=%s; identity_match=matched_at_freeze</frozen-target-context>\n", profile.String(), target.ServerVersion), nil
}

func toolRequestSignature(request provider.ToolRequest) (string, error) {
	value, err := json.Marshal(struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}{request.ToolName, request.Arguments})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:]), nil
}

// storedToolCallID deterministically derives an internal correlation key within one stage; it is not a global DB primary key.
func storedToolCallID(runID, stageID, providerID, signature string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(runID+"\x00"+stageID+"\x00"+providerID+"\x00"+signature)).String()
}
