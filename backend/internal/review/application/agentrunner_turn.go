// agentrunner_turn.go holds AgentRunner construction, turn execution, tool validation, and billing helpers.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"math"
	"regexp"
	"time"
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
	// mandatoryKeysByStage maps stage snapshot id to frozen mandatory skill
	// finding keys. Any actual hit is effective Critical.
	mandatoryKeysByStage map[string]map[string]bool
	// advisoryKnowledgeCapsByStage maps stage snapshot id -> exp.<id> -> non-blocking
	// advisory ceiling (info/low/medium). Knowledge hits never block submission.
	advisoryKnowledgeCapsByStage map[string]map[string]string
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
	incompleteReason    string
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

// governFindingSeverity separates mandatory frozen skill findings from
// non-blocking internal experience guidance and model-native observations.
// Every actual violation of a mandatory skill key is Critical regardless of
// the severity proposed by the model or its legacy max_severity metadata.
// Matched internal experience keys (exp.<id>) are capped at their advisory
// ceiling (never exceeding medium) and never create a High/Critical block.
// An unknown key remains visible as an observation capped at medium.
func governFindingSeverity(key, severity string, mandatoryKeys map[string]bool, advisoryCaps map[string]string) (string, bool) {
	switch severity {
	case "info", "low", "medium", "high", "critical":
	default:
		return "", false
	}
	if mandatoryKeys[key] {
		return "critical", true
	}
	if capSev, ok := advisoryCaps[key]; ok {
		effectiveCap := (knowledge.Definition{MaxSeverity: capSev}).EffectiveAdvisorySeverity()
		if domain.SeverityRank(domain.Severity(severity)) > domain.SeverityRank(domain.Severity(effectiveCap)) {
			return effectiveCap, true
		}
		return severity, true
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
			return stageModelTurnOutcome{err: coverageFailureOf(cerr)}
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
		return classifyStageModelTurnError(ctx, in, cerr, localeErr)
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

func classifyStageModelTurnError(ctx context.Context, in stageModelTurnInput, cerr, localeErr error) stageModelTurnOutcome {
	if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
		zap.L().Error("policy_compile_failed",
			zap.String("review_run_id", in.runID),
			zap.Int64("stage_position", int64(in.stagePosition)),
			zap.String("reason", failureNameOf(cerr)),
			zap.String("error", cerr.Error()))
		return stageModelTurnOutcome{err: coverageFailureOf(cerr)}
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
