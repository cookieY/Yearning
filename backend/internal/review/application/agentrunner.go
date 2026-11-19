// agentrunner.go B7 production review Runner: Provider chain + bounded model loop + read-only
// tool evidence collection + deterministic injection (skills + internal knowledge) + budget reservation/ledger.
//
// Shares the exact same task contract as FakeRunner (Execute signature); ai_review tasks are selected
// by the wiring layer: enabled Providers present -> AgentRunner; otherwise fail closed (2011).
// Idempotency: terminal stage state = stage watermark; injection reference existence = Run-scoped watermark;
// crash replay reruns the model loop without producing duplicate rows (the ledger faithfully records both costs).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
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
	// Hooks provides test fault-injection hooks (nil = production).
	Hooks *RunnerHooks
	// RuntimeBudget provides injectable runtime bounds (nil = PRD defaults).
	RuntimeBudget *RuntimeBudgetConfig
}

// AgentRunner is the production review executor. The sql_grouping phase is Provider-independent
// and delegates to the embedded FakeRunner (B6 streaming grouping + atomic persistence + chained task).
type AgentRunner struct {
	svc      *Services
	grouping *FakeRunner
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
	return &AgentRunner{svc: svc, grouping: NewFakeRunner(svc, nil), deps: deps,
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

// Execute implements the same task entry contract as FakeRunner.
func (r *AgentRunner) Execute(ctx context.Context, db *gorm.DB, in TaskInput,
	fenceGuard func() error, beat func(completed, total int64)) error {
	// Under cancellation propagation, both reads and terminal-state writes use an uncancellable context
	// (cancellation: preserve_completed_evidence + auditable terminal state).
	loadCtx := ctx
	if loadCtx.Err() != nil {
		loadCtx = context.WithoutCancel(ctx)
	}
	run, err := r.svc.loadRunOrNull(loadCtx, db, in.ReviewRunID)
	if err != nil {
		return err
	}
	if run == nil {
		// sql_grouping phase (no run row yet): delegate to B6 grouper.
		return r.grouping.Execute(ctx, db, in, fenceGuard, beat)
	}
	if domain.RunTerminal(domain.RunState(run.State)) {
		return nil
	}
	policy, err := r.svc.VerifyAndDecodePolicySnapshot(loadCtx, db, in, PolicySnapshotPurposeReview)
	if err != nil {
		return finishRun(r.svc, context.WithoutCancel(ctx), db, run, fenceGuard, domain.RunEventInputsChanged, 0, "")
	}
	return r.review(ctx, db, run, ResolveOutputLocale(in.OutputLocale), policy, fenceGuard, beat)
}

// review main flow: chain freeze -> injection references -> per-stage model review -> gate terminal state.
func (r *AgentRunner) review(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	outputLocale string, policy *FrozenReviewPolicy, fenceGuard func() error, beat func(int64, int64)) error {
	s := r.svc
	// Terminal-state writes must survive cancellation: derive an uncancellable context
	// (same pattern as worker.go; business reads remain bounded by the original ctx).
	termCtx := context.WithoutCancel(ctx)
	if ctx.Err() != nil {
		return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventIncomplete, 2007, "cancelled")
	}
	// Provider chain (fail closed).
	chain, err := s.LoadProviderChain(ctx, db, r.deps.Dial)
	if err != nil {
		return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventFailed, 2011, "provider_unavailable")
	}
	// B8 frozen input re-verification (SQL Hash / rule Hash): drift immediately transitions to inputs_changed,
	// consuming no further Provider budget (fail closed and cost-saving).
	if err := s.verifyFrozenInputs(ctx, db, run); err != nil {
		if errors.Is(err, errInputsDrift) {
			return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventInputsChanged, 0, "")
		}
		return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventFailed, failureCodeOf(err), failureNameOf(err))
	}
	// Freeze plan hash (replaces fake-runner-v1; chain change -> unsubmitted Run is already invalidated).
	if run.ProviderPlanHash != chain.PlanHash() {
		res := db.WithContext(ctx).Exec(`
			update review_runs set provider_plan_hash = ?, version = version + 1
			where id = ? and version = ?`, chain.PlanHash(), run.ID, run.Version)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConcurrent
		}
		run.Version++
		run.ProviderPlanHash = chain.PlanHash()
	}
	// Freeze injection references (skills + knowledge; once per Run, skipped if already present).
	inj, err := r.freezeInjections(ctx, db, run, policy)
	if err != nil {
		return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventFailed, failureCodeOf(err), failureNameOf(err))
	}
	snapshots, err := s.loadStageSnapshots(ctx, db, run.ID)
	if err != nil {
		return err
	}
	// SEC-0041: schema mapping object sets frozen at submission time (per stage; unavailable -> stage
	// fails closed), used at tool call sites to validate schema/table argument boundaries.
	schemaBindings := r.frozenStageSchemas(ctx, db, run, snapshots)
	// SEC-0002: Run-scoped shared identifier mapper — database/table/column/index tokens across all stages
	// and all tool calls of this Run are allocated by the same RunMapper (review_scoped_stable_anonymous_tokens).
	// SQL template anonymization (grouping phase) and tool facts share the same token semantics; no second
	// token space exists within the model-visible channel.
	runMapper := privacy.NewRunMapper()
	// NEW-AI-01: on crash replay, a new attempt first loads persisted mappings (token continuity is a
	// prerequisite for explainable evidence and idempotent envelope resealing); a no-op on the first attempt
	// when no record exists. Fails closed on load failure (envelope decryption failure / malformed payload):
	// new tokens without continuity could collide with existing tokens and must not proceed (internal storage
	// errors follow general failure mapping without inventing new business codes — error-codes.json 2001-2012 is a controlled vocabulary).
	if err := r.rehydrateIdentifierMapping(ctx, db, run.ID, runMapper); err != nil {
		return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventFailed, failureCodeOf(err), failureNameOf(err))
	}
	total := int64(len(snapshots))
	completed := int64(0)
	budgetSettings, err := s.LoadBudgetSettings(ctx, db)
	if err != nil {
		return err
	}
	for i := range snapshots {
		snap := &snapshots[i]
		if domain.StageTerminal(domain.StageReviewState(snap.State)) {
			completed++
			continue
		}
		if fenceGuard != nil {
			if err := fenceGuard(); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			// N4: inter-stage and intra-stage cancellations uniformly converge to cancelled (auditable terminal state).
			return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventIncomplete, 2007, "cancelled")
		}
		_, err := r.reviewStage(ctx, db, run, snap, chain, inj, budgetSettings, runMapper, schemaBindings, outputLocale)
		if err != nil {
			if ctx.Err() != nil {
				return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventIncomplete, 2007, "cancelled")
			}
			if errors.Is(err, errInputsDrift) {
				return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventInputsChanged, 0, "")
			}
			if errors.Is(err, ErrBudgetExhausted) {
				return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventIncomplete, 2010, "budget_exhausted")
			}
			return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventFailed, failureCodeOf(err), failureNameOf(err))
		}
		// Identifier mapping, evidence and the terminal watermark were
		// persisted atomically by commitStage.
		completed++
		if beat != nil {
			beat(completed, total)
		}
		if r.deps.Hooks != nil && r.deps.Hooks.AfterStage != nil {
			r.deps.Hooks.AfterStage(run.ID, int(snap.StagePosition))
		}
		if ctx.Err() != nil {
			return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventIncomplete, 2007, "cancelled")
		}
	}
	// B8 coverage check (group coverage side; fail closed -> 2007 partial).
	if err := s.verifyRunCoverage(ctx, db, run); err != nil {
		if de, ok := err.(*domain.Error); ok {
			return finishRun(r.svc, termCtx, db, run, fenceGuard, domain.RunEventIncomplete, int32(de.Code), de.Name)
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
		// M9: incomplete coverage takes precedence and converges to coverage_incomplete/2007; only
		// genuine High/Critical findings after complete execution constitute risk_blocked.
		severityBlocked := false
		for _, gi := range gateInputs {
			if domain.SeverityRank(gi.HighestFound) >= domain.SeverityRank(domain.BlockSeverityThreshold) {
				severityBlocked = true
			}
		}
		if stagedCoverageIncomplete(stageRows) || !severityBlocked {
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
	return finishRun(r.svc, termCtx, db, run, fenceGuard, trigger, biz, name)
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

// reviewStage executes a single stage: model loop (outside transaction) + single-transaction persistence (stage watermark).
// runMapper is the shared identifier mapper for this Run (SEC-0002; must not be nil).
// schemaBindings is the per-stage frozen Schema mapping object set for this Run (SEC-0041).
func (r *AgentRunner) reviewStage(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	snap *persistence.StageReviewSnapshotModel, chain *provider.Chain, inj *injections,
	budgetSettings BudgetSettings, runMapper *privacy.RunMapper,
	schemaBindings map[int32]*stageSchemaBinding, outputLocale string) (string, error) {
	return r.reviewStageCore(ctx, db, run, snap, chain, inj, budgetSettings, runMapper,
		schemaBindings, outputLocale, evaluateStageValidity,
		func(datasourceID string) ([]provider.AnonymizedGroup, error) {
			return r.stageGroups(ctx, db, run.ID, datasourceID)
		},
		func(c context.Context, out *stageOutcome) error {
			return r.commitStage(c, db, run, snap, out)
		})
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
	// Persistence writes for ledger/reservation must not be lost due to caller cancellation (N4).
	moneyCtx := context.WithoutCancel(ctx)
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
	localePending := false
	schemaRetries := 0
	schemaPending := false
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
				return r.buildStageEndpointRequest(
					reqCtx, db, run, target, targetContext,
					groups, compiled.objects,
					rawSk, compSk, rawKb, compKb,
					ev, sanEv, runMapper, stageLegend, outputLocale,
					out.evidenceIncomplete, false, false,
					&cachedSANGroups, budgetEp,
				)
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
			goto postModelLoop
		}
	}

	for turn := 0; turn < rb.MaxModelTurns; turn++ {
		if ctx.Err() != nil {
			return "", &domain.Error{Code: 2007, Name: "REVIEW_COVERAGE_INCOMPLETE"}
		}
		if time.Now().After(deadline) {
			if localePending {
				return "", &domain.Error{Code: 2009, Name: "REVIEW_SCHEMA_INVALID"}
			}
			out.outcome = "partial"
			break
		}
		if modelCalls >= rb.MaxModelCalls {
			out.outcome = "partial" // L2: per-stage model call cap
			break
		}
		var compiled compiledPolicy
		if turn == 0 && hasPrecompiled {
			compiled = precompiled
		} else {
			var compileErr error
			compiled, compileErr = r.compileStagePolicy(ctx, snap, target, run.ID, stageSkills, stageKnowledge,
				runMapper, binding, objectScope)
			if compileErr != nil {
				// Policy compilation failures (catalog privacy fence / reference resolution, etc.) previously converged
				// silently to 2007 without any log trail (diagnosis record from RCP-20261031-POLICY-TEXT-GENERIC-COLUMN-ALLOWLIST).
				// Log only the stable error name while keeping details inside the error itself.
				zap.L().Error("policy_compile_failed",
					zap.String("review_run_id", run.ID),
					zap.Int64("stage_position", int64(snap.StagePosition)),
					zap.Int("skills", len(stageSkills)),
					zap.Int("knowledge_entries", len(stageKnowledge)),
					zap.String("error", compileErr.Error()))
				return "", domain.ErrCoverageIncomplete
			}
		}
		objectScope = compiled.scope
		policyObjects = compiled.objects
		buildReqForEndpoint := func(reqCtx context.Context, ep provider.Endpoint) (provider.Request, error) {
			return r.buildStageEndpointRequest(
				reqCtx, db, run, target, targetContext,
				groups, compiled.objects,
				stageSkills, compiled.skills,
				stageKnowledge, compiled.knowledge,
				evidenceRefs, sanEvidenceRefs,
				runMapper, stageLegend, outputLocale,
				out.evidenceIncomplete, localePending, schemaPending,
				&cachedSANGroups, ep,
			)
		}
		day := s.budgetDay(nowUTC())
		if err := s.Reserve(moneyCtx, db, day, budgetSettings.Currency, budgetSettings, r.budget.PerStageEstimateMinor); err != nil {
			if errors.Is(err, ErrBudgetExhausted) {
				return "", ErrBudgetExhausted
			}
			return "", err
		}
		// L2: single model call timeout (PRD 9.5, 60s; contract cap 120s).
		modelCalls++
		callCtx, cancelCall := context.WithTimeout(ctx, time.Duration(rb.PerModelTimeout)*time.Second)
		result, cerr := chain.ReviewStructuredPerEndpoint(callCtx, buildReqForEndpoint, func(sendCtx context.Context) error {
			_, err := r.compileStagePolicy(sendCtx, snap, target, run.ID, stageSkills, stageKnowledge, runMapper, binding, objectScope)
			if err != nil {
				return ErrPolicyPrivacy
			}
			return nil
		})
		cancelCall()
		var localeErr error
		if cerr == nil {
			localeErr = agent.ValidateOutputLocale(result.Response, outputLocale)
			if localeErr != nil {
				cerr = localeErr
				localeFailures++
			}
		}
		if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
			if _, _, _, attempted := provider.ChainErrorInfo(cerr); !attempted {
				if err := s.Release(moneyCtx, db, day, budgetSettings.Currency, r.budget.PerStageEstimateMinor); err != nil {
					return "", err
				}
				return "", domain.ErrCoverageIncomplete
			}
		}
		// Ledger (appended after result or known cost; actual Token usage booked per M1).
		lid, lmodel, usage := ledgerUsageOf(result, chain, cerr)
		// OBS-002 ruling: when token unit prices are configured, bill by actual consumption (unit price per
		// million tokens, rounded up to the smallest currency unit; falls back to the fixed per-stage estimate when unconfigured).
		settleAmount := tokenBasedAmount(usage.InputTokens, usage.OutputTokens,
			budgetSettings.InputPricePerMillionMinor, budgetSettings.OutputPricePerMillionMinor,
			r.budget.PerStageEstimateMinor)
		lerr := s.Settle(moneyCtx, db, run.ID, lid, lmodel,
			int16(minInt(modelCalls, 3)), usage.InputTokens, usage.OutputTokens, settleAmount,
			budgetSettings.Currency, day, ledgerOutcome(cerr))
		if lerr != nil {
			return "", lerr
		}
		// S008: per-HTTP-call reference ledger — every real call (initial / backoff retry / schema
		// repair / failover) binds the request-time rate and parameter snapshot and writes to ai_call_usage
		// (Price-03 never multiplies all calls by the final window rate; Usage-02 preserves reported usage on failed calls).
		refCalls := provider.ChainErrorCalls(cerr)
		if result != nil {
			refCalls = result.Calls
		}
		for i, cr := range refCalls {
			if rerr := s.RecordCallUsage(moneyCtx, db, run.ID, snap.StagePosition,
				modelCalls, i+1, cr); rerr != nil {
				return "", rerr
			}
		}
		if cerr != nil {
			// M5/SEC-0011: on the failure path, release only the unconsumed balance of this reservation not yet
			// settled by Settle. Settle has already transferred settleAmount out of reserved_minor into amount_minor;
			// releasing the full reservation again here would double-deduct reserved, underestimate in-flight
			// reservations, and allow concurrent failures to breach the daily budget.
			if rem := unreleasedReserveMinor(r.budget.PerStageEstimateMinor, settleAmount); rem > 0 {
				if rerr := s.Release(moneyCtx, db, day, budgetSettings.Currency, rem); rerr != nil {
					return "", rerr
				}
			}
			if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
				zap.L().Error("policy_compile_failed",
					zap.String("review_run_id", run.ID),
					zap.Int64("stage_position", int64(snap.StagePosition)),
					zap.String("error", cerr.Error()))
				return "", domain.ErrCoverageIncomplete
			}
			if errors.Is(cerr, provider.ErrSchemaInvalid) {
				// One corrective turn, within existing model/time/budget limits.
				// Rejected text and tool requests never reach persistence or tools.
				if localeErr != nil && localeFailures == 1 && turn+1 < rb.MaxModelTurns && modelCalls < rb.MaxModelCalls && time.Now().Before(deadline) {
					localePending = true
					continue
				}
				// RCP-20261101-EPHEMERAL-PRECHECK (owner ruling 3: model resilience):
				// pure schema violations are allowed up to 2 corrective retries within the existing call/turn/time budget
				// before converging to 2009 — preventing occasional malformed outputs from real Providers (such as
				// json_object mode without schema enforcement) from killing the entire Run on a single miss.
				if schemaRetries < 2 && turn+1 < rb.MaxModelTurns && modelCalls < rb.MaxModelCalls && time.Now().Before(deadline) {
					schemaRetries++
					schemaPending = true
					continue
				}
				return "", &domain.Error{Code: 2009, Name: "REVIEW_SCHEMA_INVALID"}
			}
			// SEC-0037: submitters only see stable failure codes; Provider endpoints, internal addresses, and
			// upstream response body snapshots are never concatenated into failure_name and go only to server-side
			// structured logs (zap global Logger; dropped if unconfigured — an availability loss, never an information leak).
			zap.L().Error("ai_provider_call_failed",
				zap.String("review_run_id", run.ID),
				zap.Int64("stage_position", int64(snap.StagePosition)),
				zap.String("provider_error", cerr.Error()))
			return "", &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}
		}
		lastResp = result.Response
		localePending = false
		schemaPending = false
		lastResult = result
		// Tool requests: SEC-0041 first validates schema/table arguments (after token detokenization) against
		// the stage's frozen Schema mapping — out-of-bounds requests are booked as 2012 without constructing an
		// evidence port or touching the target DB; surviving requests proceed to registry execution and evidence referencing.
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
			if toolCalls >= rb.MaxToolCalls {
				out.evidenceIncomplete = true
				break
			}
			signature, err := toolRequestSignature(tr)
			if err != nil {
				out.evidenceIncomplete = true
				continue
			}
			storedID := storedToolCallID(run.ID, snap.ID, tr.ToolCallID, signature)
			if prior, seen := executedCalls[tr.ToolCallID]; seen {
				if prior != signature {
					out.evidenceIncomplete = true
					if !seenStoredCalls[storedID] {
						toolCalls++
						now := time.Now().UTC()
						out.toolCalls = append(out.toolCalls, persistence.ReviewToolCallModel{
							ID: storedID, ReviewRunID: run.ID, StageReviewID: &snap.ID,
							ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
							Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
						})
						seenStoredCalls[storedID] = true
					}
				}
				continue
			}
			executedCalls[tr.ToolCallID] = signature
			seenStoredCalls[storedID] = true
			toolCalls++
			// SEC-0002 companion: default models return RunMapper tokens as schema/table arguments, while
			// SAN models return raw identifiers; translate SAN arguments into canonical tokens before running
			// shared SEC-0041 scope and RunMapper checks so only validated stage objects touch FactSource.
			callArgs := tr.Arguments
			translationOK := true
			if resultEndpointIsSAN(chain, result) {
				callArgs, translationOK = translateSANToolArgsToTokens(tr.Arguments, runMapper, binding)
			}
			var modelArgs map[string]any
			var resolved map[string]any
			var bindingOK bool
			if translationOK {
				modelArgs = detokenizeToolArgs(callArgs, runMapper)
				resolved, bindingOK = resolveStageBindingToolArgs(binding, modelArgs)
			}
			if !translationOK || !knownToolIdentifiers(callArgs, runMapper) || !objectScope.allows(callArgs) || !bindingOK {
				out.evidenceIncomplete = true
				// SEC-0041: out-of-mapping object -> book as tool failure 2012 (reusing the existing
				// failure path), forming no Observed fact and never touching the database.
				now := time.Now().UTC()
				out.toolCalls = append(out.toolCalls, persistence.ReviewToolCallModel{
					ID: storedID, ReviewRunID: run.ID, StageReviewID: &snap.ID,
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
		// Scope the deferred close to this turn. The pool is released on every
		// exit path before the next Provider call without accumulating defers
		// for the lifetime of reviewStage.
		turnErr := func() error {
			factSrc, closeSrc, ferr := r.deps.FactSourceFor(ctx, snap.DatasourceID, target, run.ID)
			if ferr != nil {
				out.evidenceIncomplete = true
				for _, bc := range liveCalls {
					now := time.Now().UTC()
					out.toolCalls = append(out.toolCalls, persistence.ReviewToolCallModel{
						ID: bc.storedID, ReviewRunID: run.ID, StageReviewID: &snap.ID,
						ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: bc.req.ToolName,
						Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
					})
				}
				return nil
			}
			defer closeSrc()
			factSrc = bindSchemaFactSource(factSrc, binding)
			for _, bc := range liveCalls {
				res := r.registry.Execute(ctx, factSrc, bc.storedID, bc.req.ToolName, bc.args)
				if res.Success {
					if perr := projectToolFactSchema(res.NormalizedFact, bc.req.Arguments, runMapper, binding); perr != nil {
						res.Success = false
						res.NormalizedFact = nil
						res.ErrorCode = factErrCodePtr(2012)
					}
				}
				if res.Success && !factMatchesRequestedScope(bc.req.ToolName, res.NormalizedFact, bc.modelArgs) {
					res.Success = false
					res.NormalizedFact = nil
					res.ErrorCode = factErrCodePtr(2012)
				}
				var sanFact map[string]any
				if res.Success {
					sanFact = copyNormalizedFactForSAN(res.NormalizedFact, res.FactSchemaVersion)
				}
				// SEC-0002: before NormalizedFact enters the Provider request channel (evidenceRefs),
				// replace database/table/column/index names with Run-scoped stable anonymous tokens. Fails closed on
				// any failure: booked as tool failure 2012, and the fact never enters model context with raw identifiers.
				// RawFact (real identifiers) goes only to the local evidence_raw_payloads controlled ciphertext channel.
				if res.Success {
					if terr := tokenizeToolFactWithBindings(res.NormalizedFact, runMapper, bc.req.Arguments); terr != nil {
						res.Success = false
						res.NormalizedFact = nil
						res.ErrorCode = factErrCodePtr(2012)
					}
				}
				out.toolCalls = append(out.toolCalls, persistence.ReviewToolCallModel{
					ID: res.ToolCallID, ReviewRunID: run.ID, StageReviewID: &snap.ID,
					ToolKind: string(res.ToolKind), ToolName: res.ToolName,
					Success: res.Success, ErrorCode: res.ErrorCode,
					StartedAt: res.StartedAt, FinishedAt: res.FinishedAt,
				})
				if !res.Success {
					out.evidenceIncomplete = true
					continue // Failed tools do not form Observed facts
				}
				// Version travels inside the encrypted fact and Provider-visible
				// controlled map; historical evidence is never relabelled on read.
				res.NormalizedFact = cloneValidityFact(res.NormalizedFact)
				res.NormalizedFact["fact_schema_version"] = res.FactSchemaVersion
				if !metadataFactComplete(res.NormalizedFact) {
					out.evidenceIncomplete = true
				}
				factID := newID()
				// factJSON is the tokenized fact (identical to Provider request bytes;
				// sealing, deduplication hash, and EvidenceView share the same source).
				factJSON, err := json.Marshal(res.NormalizedFact)
				if err != nil {
					return err
				}
				env, mac, err := sealPayload(s.Crypter, "evidence_facts", factID, "normalized_fact_ciphertext", factJSON)
				if err != nil {
					return err
				}
				// H1: duplicate calls with identical arguments produce the same fact_hash — deduplicate and reuse by MAC
				// to avoid violating the evidence_facts(stage_review_id, fact_hash) unique constraint and crashing the Run.
				if priorFactID, ok := factByHash[mac]; ok {
					evidenceRefs = append(evidenceRefs, provider.NormalizedEvidenceRef{
						EvidenceID: priorFactID, FactKind: res.ToolName, Fact: res.NormalizedFact})
					sanEvidenceRefs = append(sanEvidenceRefs, provider.NormalizedEvidenceRef{
						EvidenceID: priorFactID, FactKind: res.ToolName, Fact: sanFact})
					continue
				}
				factByHash[mac] = factID
				raw := res.RawFact
				if raw == nil {
					raw = factJSON
				}
				out.evidence = append(out.evidence, evidenceDraft{
					factID: factID, source: "tool_call/" + res.ToolCallID, env: env, mac: mac, raw: raw,
				})
				// C2: normalized fact content enters the next turn's model context along with the reference
				// (capped by normalizeFact + marked as estimate; contains no raw payload).
				evidenceRefs = append(evidenceRefs, provider.NormalizedEvidenceRef{
					EvidenceID: factID, FactKind: res.ToolName, Fact: res.NormalizedFact})
				sanEvidenceRefs = append(sanEvidenceRefs, provider.NormalizedEvidenceRef{
					EvidenceID: factID, FactKind: res.ToolName, Fact: sanFact})
			}
			return nil
		}()
		if turnErr != nil {
			return "", turnErr
		}
	}

postModelLoop:
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

// buildStageEndpointRequest delegates request formatting to either the SAN
// renderer or the default prompt renderer while keeping filtering, budgeting,
// and batching completely decoupled from prompt formatting.
func (r *AgentRunner) buildStageEndpointRequest(
	reqCtx context.Context,
	db *gorm.DB,
	run *persistence.ReviewRunModel,
	target frozenDatasourceTarget,
	targetContext string,
	groups []provider.AnonymizedGroup,
	policyObjects []provider.AnonymizedObjectReference,
	rawSkills, compiledSkills []agent.SkillInjection,
	rawKnowledge, compiledKnowledge []*knowledge.Entry,
	evidenceRefs, sanEvidenceRefs []provider.NormalizedEvidenceRef,
	runMapper *privacy.RunMapper,
	stageLegend []agent.TokenLegendEntry,
	outputLocale string,
	evidenceIncomplete, localePending, schemaPending bool,
	cachedSANGroups *[]provider.AnonymizedGroup,
	ep provider.Endpoint,
) (provider.Request, error) {
	if endpointIsSAN(ep) {
		return buildSANStageRequest(
			reqCtx, db, r.svc.Crypter, run.ID, target,
			groups, policyObjects, rawSkills, rawKnowledge,
			sanEvidenceRefs, runMapper, outputLocale,
			evidenceIncomplete, localePending, schemaPending,
			cachedSANGroups,
		)
	}
	cin := agent.ContextInput{
		EngineProfile:    target.Engine + "/" + target.CompatibilityMode,
		OutputLocale:     outputLocale,
		RuleSnapshot:     []byte(run.RuleConfigHash),
		Skills:           compiledSkills,
		Knowledge:        compiledKnowledge,
		ObjectReferences: policyObjects,
		Groups:           groups,
		Evidence:         evidenceRefs,
		TokenLegend:      metadataTokenLegend(stageLegend, []provider.AnonymizedGroup{{Objects: policyObjects}}),
	}
	req := agent.BuildRequest(run.ID, cin, r.registry)
	req.SystemPrompt += targetContext
	if localePending {
		req.SystemPrompt += "\nThe previous response did not meet the output-language requirement. Regenerate all human text fields in the required language; preserve all review, evidence, privacy and severity rules.\n"
	}
	if schemaPending {
		req.SystemPrompt += "\nThe previous response violated the required output schema. Return strictly schema-conformant structured output only; fix field types and required fields, preserve all review, evidence, privacy and severity rules.\n"
	}
	if evidenceIncomplete {
		req.SystemPrompt += "\nA requested fact tool failed or returned incomplete/unknown context. This stage cannot be complete. Preserve observed findings and return outcome=partial.\n"
	}
	return req, nil
}

// commitStage executes a single transaction per stage: evidence + findings + tool calls + terminal snapshot state (watermark).
func (r *AgentRunner) commitStage(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
	snap *persistence.StageReviewSnapshotModel, out *stageOutcome) error {
	s := r.svc
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
	// NEW-AI-03: stages with zero Observed evidence are explicitly marked in gate_reason_codes
	// (without altering gate passability — model_only reviews are inherently valid; this marker lets
	// submission gates and the UI identify the "evidence unavailable" condition while blocking semantics
	// remain governed by coverage and severity levels).
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
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked persistence.StageReviewSnapshotModel
		if err := tx.WithContext(ctx).Clauses(forUpdate()).
			Select(`id, review_run_id, stage_position, state,
				normalized_context_ciphertext, normalized_context_nonce,
				normalized_context_key_id, normalized_context_algorithm`).
			First(&locked, "id = ?", snap.ID).Error; err != nil {
			return err
		}
		if domain.StageTerminal(domain.StageReviewState(locked.State)) {
			return nil // Idempotent: stage watermark
		}
		if err := persistIdentifierMappingTx(ctx, tx, s.Crypter, run.ID, out.mapper); err != nil {
			return err
		}
		validityProof, hasValidityProof := out.summary["execution_validity"]
		if out.acceptedSummary != "" || hasValidityProof || len(provenance) > 0 {
			contextValue, err := openStageReviewContext(s.Crypter, run.ID,
				locked.StagePosition, crypto.Envelope{
					Ciphertext: locked.NormalizedContextCiphertext,
					Nonce:      locked.NormalizedContextNonce,
					KeyID:      locked.NormalizedContextKeyID,
					Algorithm:  locked.NormalizedContextAlgorithm,
				})
			if err != nil {
				return err
			}
			if out.acceptedSummary != "" {
				contextValue["accepted_summary"] = out.acceptedSummary
				contextValue["accepted_output_locale"] = out.acceptedOutputLocale
			}
			if hasValidityProof {
				contextValue["execution_validity"] = validityProof
			}
			contextValue["finding_provenance"] = provenance
			contextEnvelope, err := sealStageReviewContext(
				s.Crypter, run.ID, locked.StagePosition, contextValue)
			if err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Model(&persistence.StageReviewSnapshotModel{}).
				Where("id = ?", snap.ID).
				Updates(map[string]any{
					"normalized_context_ciphertext": contextEnvelope.Ciphertext,
					"normalized_context_nonce":      contextEnvelope.Nonce,
					"normalized_context_key_id":     contextEnvelope.KeyID,
					"normalized_context_algorithm":  contextEnvelope.Algorithm,
				}).Error; err != nil {
				return err
			}
		}
		for i := range out.evidence {
			e := &out.evidence[i]
			if err := tx.WithContext(ctx).Exec(`
				insert into evidence_facts (id, stage_review_id, source_kind, source_ref,
					normalized_fact_ciphertext, normalized_fact_nonce, normalized_fact_key_id,
					normalized_fact_algorithm, fact_hash, created_at)
				values (?, ?, 'database_tool', ?, ?, ?, ?, 'aes-256-gcm', ?, ?)`,
				e.factID, snap.ID, e.source, e.env.Ciphertext, e.env.Nonce, e.env.KeyID, e.mac, now).Error; err != nil {
				return err
			}
			rawID := newID()
			// SEC-0042: raw envelope AAD and payload_hash must match the read side
			// (RevealRawEvidence) — AAD binds the evidence_facts primary key factID, and payload_hash = HMAC(rawBytes).
			// The write side previously used the raw row's own ID as AAD and the normalized fact HMAC as payload_hash,
			// causing raw reveal on real Runner evidence to always fail (fail closed).
			rawEnv, rawHash, err := sealRawEvidence(s.Crypter, e.factID, e.raw)
			if err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(`
				insert into evidence_raw_payloads (id, evidence_fact_id, raw_payload_ciphertext,
					raw_payload_nonce, raw_payload_key_id, raw_payload_algorithm, payload_hash,
					created_at, expires_at)
				values (?, ?, ?, ?, ?, 'aes-256-gcm', ?, ?, ?)`,
				rawID, e.factID, rawEnv.Ciphertext, rawEnv.Nonce, rawEnv.KeyID, rawHash,
				now, now.Add(7*24*time.Hour)).Error; err != nil {
				return err
			}
		}
		for _, tc := range out.toolCalls {
			if err := tx.WithContext(ctx).Exec(`
				insert into review_tool_calls (id, review_run_id, stage_review_id, tool_kind, tool_name,
					input_ref_id, config_hash, success, error_code, started_at, finished_at)
				values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				tc.ID, tc.ReviewRunID, tc.StageReviewID, tc.ToolKind, tc.ToolName,
				tc.InputRefID, tc.ConfigHash, tc.Success, tc.ErrorCode, tc.StartedAt, tc.FinishedAt).Error; err != nil {
				return err
			}
		}
		for i, f := range out.findings {
			findingID := findingIDs[i]
			suggest := f.suggest
			if err := tx.WithContext(ctx).Exec(`
				insert into review_findings (id, stage_review_id, fingerprint_group_id, category,
					severity, title, message, suggestion, created_at)
				values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				findingID, snap.ID, f.groupID, f.category, string(f.severity),
				f.title, f.message, suggest, now).Error; err != nil {
				return err
			}
			for _, eid := range f.evidence {
				if err := tx.WithContext(ctx).Exec(`
					insert into review_finding_evidence (finding_id, evidence_fact_id) values (?, ?)`,
					findingID, eid).Error; err != nil {
					return err
				}
			}
		}
		res := tx.WithContext(ctx).Exec(`
			update stage_review_snapshots set state = ?, highest_severity = ?, gate_passed = ?,
				gate_reason_codes = ?, snapshot_hash = ?, finished_at = ?
			where id = ? and state in ('pending','running')`,
			string(stageState), string(highest), stageState == domain.StageStatePassed,
			persistence.EncodeTextArray(reasons), snapHash, now, snap.ID)
		if res.Error != nil {
			return res.Error
		}
		return nil
	})
}

// stageGroups assembles the anonymized groups for this stage (datasource).
func (r *AgentRunner) stageGroups(ctx context.Context, db *gorm.DB, runID, datasourceID string) ([]provider.AnonymizedGroup, error) {
	var rawRows []struct {
		ID      string
		Kind    string
		Op      string
		Count   int64
		Typed   []byte
		SizeMax int64
	}
	err := db.WithContext(ctx).Raw(`
		select g.id::text as id, ss.statement_kind as kind, ss.operation as op,
		       g.typed_shape::text as typed, g.statement_count as count,
		       mx.size_max as size_max
		from sql_fingerprint_groups g
		join sql_statements ss on ss.id = g.representative_statement_id
		join lateral (
		    select max(s2.size_bytes) as size_max
		    from sql_statements s2
		    where s2.review_run_id = g.review_run_id
		      and s2.canonical_fingerprint_hmac = g.canonical_fingerprint_hmac
		      and s2.dialect_fingerprint_hmac = g.dialect_fingerprint_hmac
		) mx on true
		where g.review_run_id = ?
		order by ss.ordinal`, runID).Scan(&rawRows).Error
	if err != nil {
		return nil, err
	}
	out := []provider.AnonymizedGroup{}
	for _, row := range rawRows {
		stats := map[string]any{}
		if len(row.Typed) > 0 {
			if err := json.Unmarshal(row.Typed, &stats); err != nil {
				return nil, err
			}
		}
		var metadata struct {
			SQL           string                               `json:"representative_anonymized_sql"`
			Objects       []provider.AnonymizedObjectReference `json:"objects"`
			Complete      bool                                 `json:"metadata_context_complete"`
			Ordinal       int32                                `json:"statement_ordinal"`
			OrdinalRanges []provider.StatementOrdinalRange     `json:"statement_ordinal_ranges"`
		}
		if err := json.Unmarshal(row.Typed, &metadata); err != nil {
			return nil, err
		}
		delete(stats, "representative_anonymized_sql")
		delete(stats, "objects")
		delete(stats, "metadata_context_complete")
		delete(stats, "statement_ordinal")
		delete(stats, "statement_ordinal_ranges")
		// M7: shape synthesis aligns with B6 data fetching — statement-level columns + group-level typed_shape;
		// the size-limit skill requires the maximum single-statement byte count within the group (B8 ruling).
		stats["max_statement_bytes"] = row.SizeMax
		shape := fmt.Sprintf("kind=%v op=%v where=%v",
			row.Kind, row.Op, !boolValue(stats["unbounded_mutation"]))
		out = append(out, provider.AnonymizedGroup{
			GroupID: row.ID, RepresentativeShape: shape, StatementCount: row.Count, Statistics: stats,
			RepresentativeAnonymizedSQL: metadata.SQL, Objects: metadata.Objects,
			MetadataContextComplete: metadata.Complete,
			StatementOrdinal:        metadata.Ordinal, StatementOrdinalRanges: metadata.OrdinalRanges,
		})
	}
	return out, nil
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
func tokenBasedAmount(inputTokens, outputTokens, inputPrice, outputPrice, fallbackMinor int64) int64 {
	if inputPrice <= 0 && outputPrice <= 0 {
		return fallbackMinor
	}
	amount := addCeil(milliCeilCapped(max64(inputTokens, 0), max64(inputPrice, 0)),
		milliCeilCapped(max64(outputTokens, 0), max64(outputPrice, 0)))
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
	return max64(reservedMinor-settledMinor, 0)
}

// factErrCodePtr returns a pointer to the tool error code for tool fact tokenization failure (2012).
func factErrCodePtr(c int) *int { return &c }

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

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

// minInt returns the smaller integer (attempt_number contract 1..3).
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
