// rule_batching_evidence.go holds rule_batching domain helpers and operations.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"golang.org/x/sync/singleflight"
	"math"
	"sync"
)

const (
	// ruleBudgetSafetyRatio caps the single-call rule+experience budget at 40%
	// of the provider context_window so SQL, evidence, and model output always
	// retain ample headroom.
	ruleBudgetSafetyRatio = 0.40

	// maxConcurrentRuleBatches bounds concurrent rule-batch model invocations
	// within a single review stage.
	maxConcurrentRuleBatches = 3

	// defaultOutputReserveCap caps the default output token reserve when
	// max_tokens is not explicitly configured on the provider endpoint.
	defaultOutputReserveCap = 2048

	// chatMessageFramingTokens accounts for chat message role/delimiter overhead.
	chatMessageFramingTokens = 12
)

// ErrRuleExceedsContextWindow indicates fail-closed rejection when an
// indivisible rule (or the base context alone) cannot fit within the active
// provider's context_window. Mandatory rules are never silently dropped or
// truncated.
var ErrRuleExceedsContextWindow = errors.New("review: rule exceeds provider context_window")

type ruleBudgetError struct {
	domainErr *domain.Error
}

func (e *ruleBudgetError) Error() string {
	if e == nil || e.domainErr == nil {
		return ErrRuleExceedsContextWindow.Error()
	}
	if e.domainErr.Detail != "" {
		return e.domainErr.Detail
	}
	return e.domainErr.Error()
}

func (e *ruleBudgetError) Unwrap() []error {
	if e == nil || e.domainErr == nil {
		return []error{ErrRuleExceedsContextWindow}
	}
	return []error{ErrRuleExceedsContextWindow, e.domainErr}
}

func newRuleExceedsContextWindowError(detail string) error {
	return &ruleBudgetError{
		domainErr: &domain.Error{
			Code:   domain.CodeReviewCoverageIncomp,
			Name:   "rule_exceeds_context_window",
			Detail: detail,
		},
	}
}

// ruleBudgetPlan holds the unified token budget computed from Provider.context_window.
type ruleBudgetPlan struct {
	contextWindow     int
	outputReserve     int
	framingReserve    int
	baseTokens        int
	hardMaxRuleTokens int
	safetyCap         int
	ruleBudget        int
}

// ruleBatch represents one deterministic slice of skills and knowledge entries
// scheduled to fit within ruleBudget (or solo within hardMaxRuleTokens).
type ruleBatch struct {
	rawSkills         []agent.SkillInjection
	compiledSkills    []agent.SkillInjection
	rawKnowledge      []*knowledge.Entry
	compiledKnowledge []*knowledge.Entry
	estimatedTokens   int
}

// selectBudgetEndpoint selects the governing endpoint for context-window rule
// budgeting: the endpoint with the smallest positive context_window across the
// chain (tie-breaking by earliest chain order). Returns (_, false) if no
// endpoint configures a positive context_window.
func selectBudgetEndpoint(chain *provider.Chain) (provider.Endpoint, bool) {
	if chain == nil || len(chain.Endpoints) == 0 {
		return provider.Endpoint{}, false
	}
	var chosen provider.Endpoint
	minWindow := 0
	for _, ep := range chain.Endpoints {
		cw := provider.ConfigPositiveInt(ep.Configuration, "context_window")
		if cw <= 0 {
			continue
		}
		if minWindow == 0 || cw < minWindow {
			minWindow = cw
			chosen = ep
		}
	}
	if minWindow <= 0 {
		return provider.Endpoint{}, false
	}
	return chosen, true
}

// estimateTextTokensConservative returns a conservative (upper-bound oriented)
// pure-Go token estimate for prompt budget planning so batched requests stay
// safely within Provider.context_window across English, SQL, JSON, and CJK text.
func estimateTextTokensConservative(s string) int {
	if s == "" {
		return 0
	}
	var units float64
	for _, r := range s {
		switch {
		case r > 0x7f:
			units += 0.85
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			units += 0.15
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_':
			units += 0.30
		default:
			units += 0.45
		}
	}
	est := int(math.Ceil(units))
	if est < 1 {
		return 1
	}
	return est
}

// estimateRequestPayloadTokensConservative estimates conservative token usage
// for the user-message portion of a provider.Request (groups, evidence, tools).
func estimateRequestPayloadTokensConservative(req provider.Request) int {
	payload := struct {
		RequestSchemaVersion      int                              `json:"request_schema_version"`
		ReviewRunID               string                           `json:"review_run_id"`
		EngineProfile             string                           `json:"engine_profile"`
		AnonymizedStatementGroups []provider.AnonymizedGroup       `json:"anonymized_statement_groups"`
		NormalizedEvidence        []provider.NormalizedEvidenceRef `json:"normalized_evidence"`
		AllowedToolDescriptors    []provider.ToolDescriptor        `json:"allowed_tool_descriptors,omitempty"`
	}{
		RequestSchemaVersion:      req.RequestSchemaVersion,
		ReviewRunID:               req.ReviewRunID,
		EngineProfile:             req.EngineProfile,
		AnonymizedStatementGroups: req.AnonymizedStatementGroups,
		NormalizedEvidence:        req.NormalizedEvidence,
		AllowedToolDescriptors:    req.AllowedToolDescriptors,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	const userPrefix = "Review request JSON payload follows. Respond ONLY with a single json object matching the required JSON Schema.\n"
	return estimateTextTokensConservative(userPrefix) + estimateTextTokensConservative(string(raw))
}

// computeRuleBudget computes the unified rule+experience token budget from an
// endpoint's context_window and a rule-less baseline Request.
func computeRuleBudget(ep provider.Endpoint, baseReq provider.Request) ruleBudgetPlan {
	contextWindow := provider.ConfigPositiveInt(ep.Configuration, "context_window")
	if contextWindow <= 0 {
		return ruleBudgetPlan{}
	}
	quarterWindow := contextWindow / 4
	if quarterWindow < 1 {
		quarterWindow = 1
	}
	outputReserve := min(defaultOutputReserveCap, quarterWindow)
	if maxToks := provider.ConfigPositiveInt(ep.Configuration, "max_tokens"); maxToks > 0 {
		outputReserve = min(maxToks, quarterWindow)
		if outputReserve < 1 {
			outputReserve = 1
		}
	}
	framingReserve := min(256, contextWindow/16) + chatMessageFramingTokens
	baseSysTokens := estimateTextTokensConservative(baseReq.SystemPrompt)
	baseUserTokens := estimateRequestPayloadTokensConservative(baseReq)
	baseTokens := baseSysTokens + baseUserTokens + framingReserve
	hardMax := contextWindow - outputReserve - baseTokens
	safetyCap := int(math.Floor(float64(contextWindow) * ruleBudgetSafetyRatio))
	if safetyCap < 1 {
		safetyCap = 1
	}
	ruleBudget := min(hardMax, safetyCap)
	return ruleBudgetPlan{
		contextWindow:     contextWindow,
		outputReserve:     outputReserve,
		framingReserve:    framingReserve,
		baseTokens:        baseTokens,
		hardMaxRuleTokens: hardMax,
		safetyCap:         safetyCap,
		ruleBudget:        ruleBudget,
	}
}

type ruleBatchRequestBuilder func(
	ctx context.Context,
	rawSkills, compiledSkills []agent.SkillInjection,
	rawKnowledge, compiledKnowledge []*knowledge.Entry,
) (provider.Request, error)

type measuredRuleItem struct {
	isSkill      bool
	skillIdx     int
	knowledgeIdx int
	label        string
	tokens       int
}

// cachedEvidenceEntry records one canonical tool execution shared across all
// concurrent rule batches in a stage so target databases are never queried
// redundantly for the same fact.
type cachedEvidenceEntry struct {
	success bool
	ref     provider.NormalizedEvidenceRef
	sanRef  provider.NormalizedEvidenceRef
}

type stageEvidenceStore struct {
	runner  *AgentRunner
	runID   string
	snap    *persistence.StageReviewSnapshotModel
	target  frozenDatasourceTarget
	binding *stageSchemaBinding
	mapper  *privacy.RunMapper
	scope   metadataScope
	rb      agent.RuntimeBudget

	sf singleflight.Group

	mu                 sync.Mutex
	bySig              map[string]cachedEvidenceEntry
	factByHash         map[string]string
	executedCalls      map[string]string
	seenStoredCalls    map[string]bool
	toolCallCount      int
	toolCalls          []persistence.ReviewToolCallModel
	evidence           []evidenceDraft
	evidenceRefs       []provider.NormalizedEvidenceRef
	sanEvidenceRefs    []provider.NormalizedEvidenceRef
	evidenceIncomplete bool
	incompleteReason   string
}

func newStageEvidenceStore(
	runner *AgentRunner,
	runID string,
	snap *persistence.StageReviewSnapshotModel,
	target frozenDatasourceTarget,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
	scope metadataScope,
	rb agent.RuntimeBudget,
) *stageEvidenceStore {
	return &stageEvidenceStore{
		runner:          runner,
		runID:           runID,
		snap:            snap,
		target:          target,
		binding:         binding,
		mapper:          mapper,
		scope:           scope,
		rb:              rb,
		bySig:           map[string]cachedEvidenceEntry{},
		factByHash:      map[string]string{},
		executedCalls:   map[string]string{},
		seenStoredCalls: map[string]bool{},
		evidenceRefs:    []provider.NormalizedEvidenceRef{},
		sanEvidenceRefs: []provider.NormalizedEvidenceRef{},
	}
}

type prehydratePlanItem struct {
	toolName  string
	tokenArgs map[string]any
}
