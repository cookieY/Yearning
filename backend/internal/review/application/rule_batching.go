package application

import (
	"context"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

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
				if st.incompleteReason == "" {
					st.incompleteReason = factSourceFailureReason(ferr)
				}
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

func (r *AgentRunner) executeSingleRuleBatch(
	ctx context.Context,
	in stageBatchExecInput,
	batch ruleBatch,
	modelCallSeq *atomic.Int64,
) ruleBatchResult {
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
			return r.buildStageEndpointRequest(reqCtx, stagePromptRequest{
				db:                 in.db,
				runID:              in.run.ID,
				ruleConfigHash:     []byte(in.run.RuleConfigHash),
				target:             in.target,
				targetContext:      in.targetContext,
				groups:             in.groups,
				policyObjects:      in.policyObjects,
				rawSkills:          batch.rawSkills,
				compiledSkills:     batch.compiledSkills,
				rawKnowledge:       batch.rawKnowledge,
				compiledKnowledge:  batch.compiledKnowledge,
				evidence:           batchEvidence,
				sanEvidence:        batchSANEvidence,
				runMapper:          in.runMapper,
				stageLegend:        in.stageLegend,
				outputLocale:       in.outputLocale,
				evidenceIncomplete: res.evidenceIncomplete,
				localePending:      localePending,
				schemaPending:      schemaPending,
				cachedSANGroups:    &localSANGroups,
			}, ep)
		}

		canRetry := turn+1 < in.rb.MaxModelTurns && batchCalls+1 < in.rb.MaxModelCalls && time.Now().Before(in.deadline)
		turnOut := r.invokeStageModelTurn(ctx, stageModelTurnInput{
			db:               in.db,
			runID:            in.run.ID,
			stagePosition:    in.snap.StagePosition,
			chain:            in.chain,
			budgetSettings:   in.budgetSettings,
			perModelTimeout:  in.rb.PerModelTimeout,
			outputLocale:     in.outputLocale,
			canRetry:         canRetry,
			localeFailures:   &localeFailures,
			schemaRetries:    &schemaRetries,
			preserveCanceled: true,
			nextCallSeq: func() int {
				batchCalls++
				return int(modelCallSeq.Add(1))
			},
			buildReq: buildReqForEndpoint,
			sendGuard: func(context.Context) error {
				if len(batch.rawSkills) == 0 && len(batch.rawKnowledge) == 0 {
					return nil
				}
				if _, err := compilePolicy(batch.rawSkills, batch.rawKnowledge, in.runMapper, in.binding, in.stageCatalog, in.target.DatabaseName, in.objectScope); err != nil {
					return err
				}
				return nil
			},
		})
		if turnOut.err != nil {
			res.err = turnOut.err
			return res
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
