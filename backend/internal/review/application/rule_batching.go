package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
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

// planRuleBatches computes the unified ruleBudget from budgetEp.context_window,
// measures each rule's marginal token footprint using the format-agnostic
// buildReq callback, enforces the Fail-Closed safety floor for oversized rules,
// and returns either a single passthrough batch or deterministic slices.
func planRuleBatches(
	ctx context.Context,
	budgetEp provider.Endpoint,
	rawSkills, compiledSkills []agent.SkillInjection,
	rawKnowledge, compiledKnowledge []*knowledge.Entry,
	buildReq ruleBatchRequestBuilder,
) ([]ruleBatch, ruleBudgetPlan, error) {
	baseReq, err := buildReq(ctx, nil, nil, nil, nil)
	if err != nil {
		return nil, ruleBudgetPlan{}, err
	}
	plan := computeRuleBudget(budgetEp, baseReq)
	if len(rawSkills) == 0 && len(rawKnowledge) == 0 {
		return []ruleBatch{{}}, plan, nil
	}
	if plan.contextWindow <= 0 {
		return []ruleBatch{{
			rawSkills:         rawSkills,
			compiledSkills:    compiledSkills,
			rawKnowledge:      rawKnowledge,
			compiledKnowledge: compiledKnowledge,
		}}, plan, nil
	}
	if plan.hardMaxRuleTokens <= 0 {
		return nil, plan, newRuleExceedsContextWindowError(fmt.Sprintf(
			"base review context (~%d tokens) plus output reserve (%d tokens) exceeds provider context_window (%d); increase provider context_window or reduce SQL batch size",
			plan.baseTokens, plan.outputReserve, plan.contextWindow,
		))
	}

	baseSysTokens := estimateTextTokensConservative(baseReq.SystemPrompt)
	items := make([]measuredRuleItem, 0, len(rawSkills)+len(rawKnowledge))
	totalRuleTokens := 0

	for i := range rawSkills {
		var singleCompiled []agent.SkillInjection
		if i < len(compiledSkills) {
			singleCompiled = compiledSkills[i : i+1]
		}
		req, rerr := buildReq(ctx, rawSkills[i:i+1], singleCompiled, nil, nil)
		if rerr != nil {
			return nil, plan, rerr
		}
		delta := estimateTextTokensConservative(req.SystemPrompt) - baseSysTokens
		if delta < 1 {
			delta = 1
		}
		label := strings.TrimSpace(rawSkills[i].Name)
		if label == "" {
			label = strings.TrimSpace(rawSkills[i].FindingKey)
		}
		if delta > plan.hardMaxRuleTokens {
			return nil, plan, newRuleExceedsContextWindowError(fmt.Sprintf(
				"single rule %q requires ~%d tokens plus base context (~%d tokens) and output reserve (%d tokens), exceeding provider context_window (%d); increase provider context_window or simplify this rule",
				label, delta, plan.baseTokens, plan.outputReserve, plan.contextWindow,
			))
		}
		totalRuleTokens += delta
		items = append(items, measuredRuleItem{
			isSkill:  true,
			skillIdx: i,
			label:    label,
			tokens:   delta,
		})
	}

	for j := range rawKnowledge {
		var singleCompiled []*knowledge.Entry
		if j < len(compiledKnowledge) {
			singleCompiled = compiledKnowledge[j : j+1]
		}
		req, rerr := buildReq(ctx, nil, nil, rawKnowledge[j:j+1], singleCompiled)
		if rerr != nil {
			return nil, plan, rerr
		}
		delta := estimateTextTokensConservative(req.SystemPrompt) - baseSysTokens
		if delta < 1 {
			delta = 1
		}
		label := ""
		if rawKnowledge[j] != nil {
			label = strings.TrimSpace(rawKnowledge[j].Name)
			if label == "" {
				label = strings.TrimSpace(rawKnowledge[j].FindingKey)
			}
		}
		if delta > plan.hardMaxRuleTokens {
			return nil, plan, newRuleExceedsContextWindowError(fmt.Sprintf(
				"single experience rule %q requires ~%d tokens plus base context (~%d tokens) and output reserve (%d tokens), exceeding provider context_window (%d); increase provider context_window or simplify this rule",
				label, delta, plan.baseTokens, plan.outputReserve, plan.contextWindow,
			))
		}
		totalRuleTokens += delta
		items = append(items, measuredRuleItem{
			isSkill:      false,
			knowledgeIdx: j,
			label:        label,
			tokens:       delta,
		})
	}

	// Single-batch passthrough when all filtered rules fit within ruleBudget.
	if totalRuleTokens <= plan.ruleBudget {
		return []ruleBatch{{
			rawSkills:         rawSkills,
			compiledSkills:    compiledSkills,
			rawKnowledge:      rawKnowledge,
			compiledKnowledge: compiledKnowledge,
			estimatedTokens:   totalRuleTokens,
		}}, plan, nil
	}

	// Deterministic sequential packing into batches fitting within ruleBudget.
	// Any single item with ruleBudget < tokens <= hardMaxRuleTokens runs in its
	// own solo batch so mandatory rules are never dropped or truncated.
	batches := make([]ruleBatch, 0, (totalRuleTokens/max(plan.ruleBudget, 1))+1)
	var cur ruleBatch
	flush := func() {
		if len(cur.rawSkills) > 0 || len(cur.rawKnowledge) > 0 {
			batches = append(batches, cur)
			cur = ruleBatch{}
		}
	}
	appendItem := func(b *ruleBatch, item measuredRuleItem) {
		if item.isSkill {
			b.rawSkills = append(b.rawSkills, rawSkills[item.skillIdx])
			if item.skillIdx < len(compiledSkills) {
				b.compiledSkills = append(b.compiledSkills, compiledSkills[item.skillIdx])
			}
		} else {
			b.rawKnowledge = append(b.rawKnowledge, rawKnowledge[item.knowledgeIdx])
			if item.knowledgeIdx < len(compiledKnowledge) {
				b.compiledKnowledge = append(b.compiledKnowledge, compiledKnowledge[item.knowledgeIdx])
			}
		}
		b.estimatedTokens += item.tokens
	}

	for _, item := range items {
		if item.tokens > plan.ruleBudget {
			flush()
			var solo ruleBatch
			appendItem(&solo, item)
			batches = append(batches, solo)
			continue
		}
		if cur.estimatedTokens > 0 && cur.estimatedTokens+item.tokens > plan.ruleBudget {
			flush()
		}
		appendItem(&cur, item)
	}
	flush()

	return batches, plan, nil
}

// mergeBatchFindings deterministically merges findings produced across rule
// batches by (fingerprint_group_id, finding_key), retaining the highest
// severity and deduplicating/merging evidence_ids.
func mergeBatchFindings(batchFindings [][]provider.Finding) []provider.Finding {
	type mergeKey struct {
		groupID    string
		findingKey string
	}
	type mergedEntry struct {
		finding  provider.Finding
		evSet    map[string]bool
		hasEvRef bool
	}

	byKey := make(map[mergeKey]*mergedEntry)
	keys := make([]mergeKey, 0)

	for _, findings := range batchFindings {
		for _, f := range findings {
			for _, gid := range f.FingerprintGroupIDs {
				mk := mergeKey{groupID: gid, findingKey: f.FindingKey}
				entry, exists := byKey[mk]
				if !exists {
					evSet := make(map[string]bool, len(f.EvidenceIDs))
					hasEv := f.FactBasis == "evidence"
					if hasEv {
						for _, eid := range f.EvidenceIDs {
							evSet[eid] = true
						}
					}
					cp := f
					cp.FingerprintGroupIDs = []string{gid}
					cp.EvidenceIDs = nil
					byKey[mk] = &mergedEntry{finding: cp, evSet: evSet, hasEvRef: hasEv}
					keys = append(keys, mk)
					continue
				}
				if domain.SeverityRank(domain.Severity(f.Severity)) > domain.SeverityRank(domain.Severity(entry.finding.Severity)) {
					entry.finding.Severity = f.Severity
					entry.finding.Category = f.Category
					entry.finding.Title = f.Title
					entry.finding.Message = f.Message
					entry.finding.Suggestion = f.Suggestion
				}
				if f.FactBasis == "evidence" {
					entry.hasEvRef = true
					for _, eid := range f.EvidenceIDs {
						entry.evSet[eid] = true
					}
				}
			}
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].groupID != keys[j].groupID {
			return keys[i].groupID < keys[j].groupID
		}
		return keys[i].findingKey < keys[j].findingKey
	})

	out := make([]provider.Finding, 0, len(keys))
	for _, mk := range keys {
		entry := byKey[mk]
		if entry.hasEvRef {
			entry.finding.FactBasis = "evidence"
			evIDs := make([]string, 0, len(entry.evSet))
			for eid := range entry.evSet {
				evIDs = append(evIDs, eid)
			}
			sort.Strings(evIDs)
			entry.finding.EvidenceIDs = evIDs
		} else {
			entry.finding.EvidenceIDs = []string{}
		}
		out = append(out, entry.finding)
	}
	return out
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

// prehydrate collects core scoped table and index metadata once on a single
// target connection before concurrent rule batches start.
func (st *stageEvidenceStore) prehydrate(
	ctx context.Context,
	groups []provider.AnonymizedGroup,
	policyObjects []provider.AnonymizedObjectReference,
) error {
	if st.runner == nil || st.runner.deps.FactSourceFor == nil {
		return nil
	}
	planned := st.buildPrehydratePlan(groups, policyObjects)
	if len(planned) == 0 {
		return nil
	}
	factSrc, closeSrc, err := st.runner.deps.FactSourceFor(ctx, st.snap.DatasourceID, st.target, st.runID)
	if err != nil {
		st.mu.Lock()
		st.evidenceIncomplete = true
		now := time.Now().UTC()
		for _, item := range planned {
			sig, serr := toolRequestSignature(provider.ToolRequest{ToolName: item.toolName, Arguments: item.tokenArgs})
			if serr != nil {
				continue
			}
			storedID := storedToolCallID(st.runID, st.snap.ID, "prehydrate:"+sig, sig)
			st.bySig[sig] = cachedEvidenceEntry{success: false}
			if !st.seenStoredCalls[storedID] {
				st.seenStoredCalls[storedID] = true
				st.toolCallCount++
				st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
					ID: storedID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
					ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: item.toolName,
					Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
				})
			}
		}
		st.mu.Unlock()
		return nil
	}
	defer closeSrc()
	factSrc = bindSchemaFactSource(factSrc, st.binding)

	for _, item := range planned {
		sig, serr := toolRequestSignature(provider.ToolRequest{ToolName: item.toolName, Arguments: item.tokenArgs})
		if serr != nil {
			st.mu.Lock()
			st.evidenceIncomplete = true
			st.mu.Unlock()
			continue
		}
		storedID := storedToolCallID(st.runID, st.snap.ID, "prehydrate:"+sig, sig)
		if _, err := st.executeSingleAgainstSource(ctx, factSrc, item.toolName, item.tokenArgs, sig, storedID); err != nil {
			return err
		}
	}
	return nil
}

func (st *stageEvidenceStore) buildPrehydratePlan(
	groups []provider.AnonymizedGroup,
	policyObjects []provider.AnonymizedObjectReference,
) []prehydratePlanItem {
	type tableKey struct {
		schema string
		table  string
	}
	schemaTables := make([]tableKey, 0)
	for schemaTok, tables := range st.scope.objects {
		for tableTok := range tables {
			schemaTables = append(schemaTables, tableKey{schema: schemaTok, table: tableTok})
		}
	}
	sort.Slice(schemaTables, func(i, j int) bool {
		if schemaTables[i].schema != schemaTables[j].schema {
			return schemaTables[i].schema < schemaTables[j].schema
		}
		return schemaTables[i].table < schemaTables[j].table
	})

	indexTableSet := make(map[tableKey]bool)
	scanObjects := func(objs []provider.AnonymizedObjectReference) {
		for _, obj := range objs {
			if obj.Kind != "index" || obj.SchemaToken == "" || obj.ParentToken == "" {
				continue
			}
			if st.scope.objects[obj.SchemaToken][obj.ParentToken] {
				indexTableSet[tableKey{schema: obj.SchemaToken, table: obj.ParentToken}] = true
			}
		}
	}
	for _, g := range groups {
		scanObjects(g.Objects)
	}
	scanObjects(policyObjects)

	indexTables := make([]tableKey, 0, len(indexTableSet))
	for tk := range indexTableSet {
		indexTables = append(indexTables, tk)
	}
	sort.Slice(indexTables, func(i, j int) bool {
		if indexTables[i].schema != indexTables[j].schema {
			return indexTables[i].schema < indexTables[j].schema
		}
		return indexTables[i].table < indexTables[j].table
	})

	planned := make([]prehydratePlanItem, 0, len(schemaTables)+len(indexTables))
	for _, tk := range schemaTables {
		planned = append(planned, prehydratePlanItem{
			toolName:  "schema_metadata",
			tokenArgs: map[string]any{"schema": tk.schema, "table": tk.table},
		})
	}
	for _, tk := range indexTables {
		planned = append(planned, prehydratePlanItem{
			toolName:  "index_metadata",
			tokenArgs: map[string]any{"schema": tk.schema, "table": tk.table},
		})
	}
	return planned
}

func (st *stageEvidenceStore) executeSingleAgainstSource(
	ctx context.Context,
	factSrc tool.FactSource,
	toolName string,
	tokenArgs map[string]any,
	sig string,
	storedID string,
) (cachedEvidenceEntry, error) {
	modelArgs := detokenizeToolArgs(tokenArgs, st.mapper)
	resolved, bindingOK := resolveStageBindingToolArgs(st.binding, modelArgs)
	if !knownToolIdentifiers(tokenArgs, st.mapper) || !st.scope.allows(tokenArgs) || !bindingOK {
		st.mu.Lock()
		st.evidenceIncomplete = true
		now := time.Now().UTC()
		if !st.seenStoredCalls[storedID] {
			st.seenStoredCalls[storedID] = true
			st.toolCallCount++
			st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
				ID: storedID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
				ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: toolName,
				Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
			})
		}
		entry := cachedEvidenceEntry{success: false}
		st.bySig[sig] = entry
		st.mu.Unlock()
		return entry, nil
	}

	res := st.runner.registry.Execute(ctx, factSrc, storedID, toolName, resolved)
	if res.Success {
		if perr := projectToolFactSchema(res.NormalizedFact, tokenArgs, st.mapper, st.binding); perr != nil {
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
		if terr := tokenizeToolFactWithBindings(res.NormalizedFact, st.mapper, tokenArgs); terr != nil {
			res.Success = false
			res.NormalizedFact = nil
			res.ErrorCode = factErrCodePtr(2012)
		}
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.seenStoredCalls[storedID] {
		st.seenStoredCalls[storedID] = true
		st.toolCallCount++
		st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
			ID: res.ToolCallID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
			ToolKind: string(res.ToolKind), ToolName: res.ToolName,
			Success: res.Success, ErrorCode: res.ErrorCode,
			StartedAt: res.StartedAt, FinishedAt: res.FinishedAt,
		})
	}
	if !res.Success {
		st.evidenceIncomplete = true
		entry := cachedEvidenceEntry{success: false}
		st.bySig[sig] = entry
		return entry, nil
	}

	res.NormalizedFact = cloneValidityFact(res.NormalizedFact)
	res.NormalizedFact["fact_schema_version"] = res.FactSchemaVersion
	if !metadataFactComplete(res.NormalizedFact) {
		st.evidenceIncomplete = true
	}
	factID := newID()
	factJSON, err := json.Marshal(res.NormalizedFact)
	if err != nil {
		return cachedEvidenceEntry{}, err
	}
	env, mac, err := sealPayload(st.runner.svc.Crypter, "evidence_facts", factID, "normalized_fact_ciphertext", factJSON)
	if err != nil {
		return cachedEvidenceEntry{}, err
	}
	if priorFactID, ok := st.factByHash[mac]; ok {
		entry := cachedEvidenceEntry{
			success: true,
			ref:     provider.NormalizedEvidenceRef{EvidenceID: priorFactID, FactKind: res.ToolName, Fact: res.NormalizedFact},
			sanRef:  provider.NormalizedEvidenceRef{EvidenceID: priorFactID, FactKind: res.ToolName, Fact: sanFact},
		}
		st.bySig[sig] = entry
		return entry, nil
	}
	st.factByHash[mac] = factID
	raw := res.RawFact
	if raw == nil {
		raw = factJSON
	}
	st.evidence = append(st.evidence, evidenceDraft{
		factID: factID, source: "tool_call/" + res.ToolCallID, env: env, mac: mac, raw: raw,
	})
	ref := provider.NormalizedEvidenceRef{EvidenceID: factID, FactKind: res.ToolName, Fact: res.NormalizedFact}
	sanRef := provider.NormalizedEvidenceRef{EvidenceID: factID, FactKind: res.ToolName, Fact: sanFact}
	st.evidenceRefs = append(st.evidenceRefs, ref)
	st.sanEvidenceRefs = append(st.sanEvidenceRefs, sanRef)
	entry := cachedEvidenceEntry{success: true, ref: ref, sanRef: sanRef}
	st.bySig[sig] = entry
	return entry, nil
}

// executeToolRequests serves model tool_requests across concurrent batches using
// cached pre-hydrated facts and singleflight deduplication so target databases
// are queried at most once per canonical (tool, args) signature.
func (st *stageEvidenceStore) executeToolRequests(
	ctx context.Context,
	requests []provider.ToolRequest,
	isSAN bool,
) ([]provider.NormalizedEvidenceRef, []provider.NormalizedEvidenceRef, bool, error) {
	added := make([]provider.NormalizedEvidenceRef, 0, len(requests))
	addedSAN := make([]provider.NormalizedEvidenceRef, 0, len(requests))
	incomplete := false

	for _, tr := range requests {
		callArgs := tr.Arguments
		translationOK := true
		if isSAN {
			callArgs, translationOK = translateSANToolArgsToTokens(tr.Arguments, st.mapper, st.binding)
		}
		if !translationOK {
			st.mu.Lock()
			st.evidenceIncomplete = true
			now := time.Now().UTC()
			if rawSig, serr := toolRequestSignature(tr); serr == nil {
				storedID := storedToolCallID(st.runID, st.snap.ID, tr.ToolCallID, rawSig)
				if !st.seenStoredCalls[storedID] {
					st.seenStoredCalls[storedID] = true
					st.toolCallCount++
					st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
						ID: storedID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
						ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
						Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
					})
				}
			}
			st.mu.Unlock()
			incomplete = true
			continue
		}

		canonicalReq := provider.ToolRequest{ToolCallID: tr.ToolCallID, ToolName: tr.ToolName, Arguments: callArgs}
		sig, serr := toolRequestSignature(canonicalReq)
		if serr != nil {
			st.mu.Lock()
			st.evidenceIncomplete = true
			st.mu.Unlock()
			incomplete = true
			continue
		}

		// Fast path: reuse already pre-hydrated or sibling-fetched evidence without
		// touching the target database.
		st.mu.Lock()
		if cached, ok := st.bySig[sig]; ok {
			st.mu.Unlock()
			if cached.success {
				added = append(added, cached.ref)
				addedSAN = append(addedSAN, cached.sanRef)
			} else {
				incomplete = true
			}
			continue
		}
		if prior, seen := st.executedCalls[tr.ToolCallID]; seen && prior != sig {
			st.evidenceIncomplete = true
			storedID := storedToolCallID(st.runID, st.snap.ID, tr.ToolCallID, sig)
			if !st.seenStoredCalls[storedID] {
				st.seenStoredCalls[storedID] = true
				st.toolCallCount++
				now := time.Now().UTC()
				st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
					ID: storedID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
					ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
					Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
				})
			}
			st.mu.Unlock()
			incomplete = true
			continue
		}
		if st.toolCallCount >= st.rb.MaxToolCalls {
			st.evidenceIncomplete = true
			st.mu.Unlock()
			incomplete = true
			break
		}
		st.executedCalls[tr.ToolCallID] = sig
		st.mu.Unlock()

		storedID := storedToolCallID(st.runID, st.snap.ID, tr.ToolCallID, sig)
		// SEC-0041: validate token membership, stage object scope, and schema
		// binding BEFORE opening a FactSource or touching the target database.
		modelArgs := detokenizeToolArgs(callArgs, st.mapper)
		_, bindingOK := resolveStageBindingToolArgs(st.binding, modelArgs)
		if !knownToolIdentifiers(callArgs, st.mapper) || !st.scope.allows(callArgs) || !bindingOK {
			st.mu.Lock()
			st.evidenceIncomplete = true
			now := time.Now().UTC()
			if !st.seenStoredCalls[storedID] {
				st.seenStoredCalls[storedID] = true
				st.toolCallCount++
				st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
					ID: storedID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
					ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
					Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
				})
			}
			st.bySig[sig] = cachedEvidenceEntry{success: false}
			st.mu.Unlock()
			incomplete = true
			continue
		}

		val, err, _ := st.sf.Do(sig, func() (any, error) {
			st.mu.Lock()
			if cached, ok := st.bySig[sig]; ok {
				st.mu.Unlock()
				return cached, nil
			}
			st.mu.Unlock()

			factSrc, closeSrc, ferr := st.runner.deps.FactSourceFor(ctx, st.snap.DatasourceID, st.target, st.runID)
			if ferr != nil {
				st.mu.Lock()
				st.evidenceIncomplete = true
				now := time.Now().UTC()
				if !st.seenStoredCalls[storedID] {
					st.seenStoredCalls[storedID] = true
					st.toolCallCount++
					st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
						ID: storedID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
						ToolKind: string(tool.KindBuiltinDatabaseFact), ToolName: tr.ToolName,
						Success: false, ErrorCode: factErrCodePtr(2012), StartedAt: now, FinishedAt: now,
					})
				}
				entry := cachedEvidenceEntry{success: false}
				st.bySig[sig] = entry
				st.mu.Unlock()
				return entry, nil
			}
			defer closeSrc()
			factSrc = bindSchemaFactSource(factSrc, st.binding)
			return st.executeSingleAgainstSource(ctx, factSrc, tr.ToolName, callArgs, sig, storedID)
		})
		if err != nil {
			return added, addedSAN, true, err
		}
		entry := val.(cachedEvidenceEntry)
		if entry.success {
			added = append(added, entry.ref)
			addedSAN = append(addedSAN, entry.sanRef)
		} else {
			incomplete = true
		}
	}
	return added, addedSAN, incomplete, nil
}

func (st *stageEvidenceStore) initialEvidenceSnapshot() ([]provider.NormalizedEvidenceRef, []provider.NormalizedEvidenceRef) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ev := append([]provider.NormalizedEvidenceRef(nil), st.evidenceRefs...)
	sanEv := append([]provider.NormalizedEvidenceRef(nil), st.sanEvidenceRefs...)
	return ev, sanEv
}

func (st *stageEvidenceStore) finalSnapshot() (
	evidence []evidenceDraft,
	toolCalls []persistence.ReviewToolCallModel,
	evidenceRefs []provider.NormalizedEvidenceRef,
	incomplete bool,
) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]evidenceDraft(nil), st.evidence...),
		append([]persistence.ReviewToolCallModel(nil), st.toolCalls...),
		append([]provider.NormalizedEvidenceRef(nil), st.evidenceRefs...),
		st.evidenceIncomplete
}

type ruleBatchResult struct {
	outcome            string
	lastResp           *provider.Response
	lastResult         *provider.Result
	evidenceIncomplete bool
	err                error
}

type stageBatchExecInput struct {
	db             *gorm.DB
	run            *persistence.ReviewRunModel
	snap           *persistence.StageReviewSnapshotModel
	chain          *provider.Chain
	target         frozenDatasourceTarget
	targetContext  string
	groups         []provider.AnonymizedGroup
	sanGroups      []provider.AnonymizedGroup
	policyObjects  []provider.AnonymizedObjectReference
	stageCatalog   database.PolicyCatalog
	objectScope    metadataScope
	stageLegend    []agent.TokenLegendEntry
	budgetSettings BudgetSettings
	runMapper      *privacy.RunMapper
	binding        *stageSchemaBinding
	outputLocale   string
	rb             agent.RuntimeBudget
	deadline       time.Time
	store          *stageEvidenceStore
	baseIncomplete bool
}

// executeRuleBatches executes partitioned rule batches with bounded concurrency,
// reusing pre-hydrated NormalizedEvidence and recording usage/audit entries
// consistently across all chunks.
func (r *AgentRunner) executeRuleBatches(
	ctx context.Context,
	in stageBatchExecInput,
	batches []ruleBatch,
) ([]ruleBatchResult, error) {
	results := make([]ruleBatchResult, len(batches))
	batchCtx, cancelBatches := context.WithCancel(ctx)
	defer cancelBatches()

	var modelCallSeq atomic.Int64
	concurrency := min(len(batches), maxConcurrentRuleBatches)
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup
	for idx := range batches {
		batchIdx := idx
		batch := batches[idx]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-batchCtx.Done():
				results[batchIdx] = ruleBatchResult{err: batchCtx.Err()}
				return
			}
			res := r.executeSingleRuleBatch(batchCtx, in, batch, &modelCallSeq)
			results[batchIdx] = res
			if res.err != nil {
				cancelBatches()
			}
		}()
	}
	wg.Wait()

	// Check for fatal errors in deterministic batch order. Prefer the root cause
	// error over sibling context.Canceled errors triggered by cancelBatches.
	var firstErr error
	for _, res := range results {
		if res.err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = res.err
		}
		if !errors.Is(res.err, context.Canceled) {
			return results, res.err
		}
	}
	if firstErr != nil {
		if ctx.Err() != nil {
			return results, &domain.Error{Code: 2007, Name: "REVIEW_COVERAGE_INCOMPLETE"}
		}
		return results, firstErr
	}
	return results, nil
}

func (r *AgentRunner) executeSingleRuleBatch(
	ctx context.Context,
	in stageBatchExecInput,
	batch ruleBatch,
	modelCallSeq *atomic.Int64,
) ruleBatchResult {
	s := r.svc
	moneyCtx := context.WithoutCancel(ctx)

	batchEvidence, batchSANEvidence := in.store.initialEvidenceSnapshot()
	seenEvidence := make(map[string]bool, len(batchEvidence))
	for _, ref := range batchEvidence {
		seenEvidence[ref.EvidenceID] = true
	}

	var res ruleBatchResult
	res.evidenceIncomplete = in.baseIncomplete
	batchCalls := 0
	localeFailures := 0
	localePending := false
	schemaRetries := 0
	schemaPending := false
	localSANGroups := in.sanGroups

	for turn := 0; turn < in.rb.MaxModelTurns; turn++ {
		if ctx.Err() != nil {
			res.err = ctx.Err()
			return res
		}
		if time.Now().After(in.deadline) {
			if localePending {
				res.err = &domain.Error{Code: 2009, Name: "REVIEW_SCHEMA_INVALID"}
				return res
			}
			res.outcome = "partial"
			break
		}
		if batchCalls >= in.rb.MaxModelCalls {
			res.outcome = "partial"
			break
		}

		buildReqForEndpoint := func(reqCtx context.Context, ep provider.Endpoint) (provider.Request, error) {
			return r.buildStageEndpointRequest(
				reqCtx, in.db, in.run, in.target, in.targetContext,
				in.groups, in.policyObjects,
				batch.rawSkills, batch.compiledSkills,
				batch.rawKnowledge, batch.compiledKnowledge,
				batchEvidence, batchSANEvidence,
				in.runMapper, in.stageLegend, in.outputLocale,
				res.evidenceIncomplete, localePending, schemaPending,
				&localSANGroups, ep,
			)
		}

		day := s.budgetDay(nowUTC())
		if err := s.Reserve(moneyCtx, in.db, day, in.budgetSettings.Currency, in.budgetSettings, r.budget.PerStageEstimateMinor); err != nil {
			if errors.Is(err, ErrBudgetExhausted) {
				res.err = ErrBudgetExhausted
				return res
			}
			res.err = err
			return res
		}
		batchCalls++
		callSeq := int(modelCallSeq.Add(1))
		callCtx, cancelCall := context.WithTimeout(ctx, time.Duration(in.rb.PerModelTimeout)*time.Second)
		result, cerr := in.chain.ReviewStructuredPerEndpoint(callCtx, buildReqForEndpoint, func(sendCtx context.Context) error {
			if len(batch.rawSkills) == 0 && len(batch.rawKnowledge) == 0 {
				return nil
			}
			if _, err := compilePolicy(batch.rawSkills, batch.rawKnowledge, in.runMapper, in.binding, in.stageCatalog, in.target.DatabaseName, in.objectScope); err != nil {
				return ErrPolicyPrivacy
			}
			return nil
		})
		cancelCall()

		var localeErr error
		if cerr == nil {
			localeErr = agent.ValidateOutputLocale(result.Response, in.outputLocale)
			if localeErr != nil {
				cerr = localeErr
				localeFailures++
			}
		}
		if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
			if _, _, _, attempted := provider.ChainErrorInfo(cerr); !attempted {
				if err := s.Release(moneyCtx, in.db, day, in.budgetSettings.Currency, r.budget.PerStageEstimateMinor); err != nil {
					res.err = err
					return res
				}
				res.err = domain.ErrCoverageIncomplete
				return res
			}
		}

		lid, lmodel, usage := ledgerUsageOf(result, in.chain, cerr)
		settleAmount := tokenBasedAmount(usage.InputTokens, usage.OutputTokens,
			in.budgetSettings.InputPricePerMillionMinor, in.budgetSettings.OutputPricePerMillionMinor,
			r.budget.PerStageEstimateMinor)
		if lerr := s.Settle(moneyCtx, in.db, in.run.ID, lid, lmodel,
			int16(minInt(callSeq, 3)), usage.InputTokens, usage.OutputTokens, settleAmount,
			in.budgetSettings.Currency, day, ledgerOutcome(cerr)); lerr != nil {
			res.err = lerr
			return res
		}
		refCalls := provider.ChainErrorCalls(cerr)
		if result != nil {
			refCalls = result.Calls
		}
		for i, cr := range refCalls {
			if rerr := s.RecordCallUsage(moneyCtx, in.db, in.run.ID, in.snap.StagePosition,
				callSeq, i+1, cr); rerr != nil {
				res.err = rerr
				return res
			}
		}

		if cerr != nil {
			if rem := unreleasedReserveMinor(r.budget.PerStageEstimateMinor, settleAmount); rem > 0 {
				if rerr := s.Release(moneyCtx, in.db, day, in.budgetSettings.Currency, rem); rerr != nil {
					res.err = rerr
					return res
				}
			}
			if errors.Is(cerr, ErrPolicyPrivacy) || errors.Is(cerr, domain.ErrCoverageIncomplete) {
				zap.L().Error("policy_compile_failed",
					zap.String("review_run_id", in.run.ID),
					zap.Int64("stage_position", int64(in.snap.StagePosition)),
					zap.String("error", cerr.Error()))
				res.err = domain.ErrCoverageIncomplete
				return res
			}
			if ctx.Err() != nil && errors.Is(cerr, context.Canceled) {
				// Preserve sibling cancellation so executeRuleBatches surfaces the
				// failing batch's root-cause error rather than masking it as 2011.
				res.err = ctx.Err()
				return res
			}
			if errors.Is(cerr, provider.ErrSchemaInvalid) {
				if localeErr != nil && localeFailures == 1 && turn+1 < in.rb.MaxModelTurns && batchCalls < in.rb.MaxModelCalls && time.Now().Before(in.deadline) {
					localePending = true
					continue
				}
				if schemaRetries < 2 && turn+1 < in.rb.MaxModelTurns && batchCalls < in.rb.MaxModelCalls && time.Now().Before(in.deadline) {
					schemaRetries++
					schemaPending = true
					continue
				}
				res.err = &domain.Error{Code: 2009, Name: "REVIEW_SCHEMA_INVALID"}
				return res
			}
			zap.L().Error("ai_provider_call_failed",
				zap.String("review_run_id", in.run.ID),
				zap.Int64("stage_position", int64(in.snap.StagePosition)),
				zap.String("provider_error", cerr.Error()))
			res.err = &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}
			return res
		}

		res.lastResp = result.Response
		res.lastResult = result
		localePending = false
		schemaPending = false

		if len(result.Response.ToolRequests) == 0 {
			break
		}
		added, addedSAN, inc, toolErr := in.store.executeToolRequests(ctx, result.Response.ToolRequests, resultEndpointIsSAN(in.chain, result))
		if toolErr != nil {
			res.err = toolErr
			return res
		}
		if inc {
			res.evidenceIncomplete = true
		}
		for i := range added {
			if seenEvidence[added[i].EvidenceID] {
				continue
			}
			seenEvidence[added[i].EvidenceID] = true
			batchEvidence = append(batchEvidence, added[i])
			if i < len(addedSAN) {
				batchSANEvidence = append(batchSANEvidence, addedSAN[i])
			}
		}
	}

	if res.outcome == "" && res.lastResp != nil {
		res.outcome = res.lastResp.Outcome
	}
	if res.lastResp == nil || len(res.lastResp.ToolRequests) > 0 {
		res.evidenceIncomplete = true
		res.outcome = "partial"
	}
	return res
}

func mergeBatchSummaries(results []ruleBatchResult) string {
	seen := make(map[string]bool, len(results))
	parts := make([]string, 0, len(results))
	for _, res := range results {
		if res.lastResp == nil {
			continue
		}
		text := strings.TrimSpace(res.lastResp.Summary)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}
