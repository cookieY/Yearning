// rule_batching_exec.go holds rule_batching domain helpers and operations.
package application

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
	"gorm.io/gorm"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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
		if st.incompleteReason == "" {
			st.incompleteReason = factSourceFailureReason(err)
		}
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

	execOut, execErr := st.runner.executeValidatedToolCall(
		ctx, factSrc, storedID, toolName, resolved, tokenArgs, modelArgs, st.mapper, st.binding,
	)

	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.seenStoredCalls[storedID] {
		st.seenStoredCalls[storedID] = true
		st.toolCallCount++
		st.toolCalls = append(st.toolCalls, persistence.ReviewToolCallModel{
			ID: execOut.res.ToolCallID, ReviewRunID: st.runID, StageReviewID: &st.snap.ID,
			ToolKind: string(execOut.res.ToolKind), ToolName: execOut.res.ToolName,
			Success: execOut.res.Success, ErrorCode: execOut.res.ErrorCode,
			StartedAt: execOut.res.StartedAt, FinishedAt: execOut.res.FinishedAt,
		})
	}
	if !execOut.res.Success {
		st.evidenceIncomplete = true
		entry := cachedEvidenceEntry{success: false}
		st.bySig[sig] = entry
		return entry, nil
	}
	if execErr != nil {
		return cachedEvidenceEntry{}, execErr
	}
	if execOut.metadataIncomplete {
		st.evidenceIncomplete = true
	}
	if priorFactID, ok := st.factByHash[execOut.mac]; ok {
		entry := cachedEvidenceEntry{
			success: true,
			ref:     provider.NormalizedEvidenceRef{EvidenceID: priorFactID, FactKind: execOut.res.ToolName, Fact: execOut.res.NormalizedFact},
			sanRef:  provider.NormalizedEvidenceRef{EvidenceID: priorFactID, FactKind: execOut.res.ToolName, Fact: execOut.sanFact},
		}
		st.bySig[sig] = entry
		return entry, nil
	}
	st.factByHash[execOut.mac] = execOut.factID
	st.evidence = append(st.evidence, evidenceDraft{
		factID: execOut.factID, source: "tool_call/" + execOut.res.ToolCallID, env: execOut.env, mac: execOut.mac, raw: execOut.rawFact,
	})
	ref := provider.NormalizedEvidenceRef{EvidenceID: execOut.factID, FactKind: execOut.res.ToolName, Fact: execOut.res.NormalizedFact}
	sanRef := provider.NormalizedEvidenceRef{EvidenceID: execOut.factID, FactKind: execOut.res.ToolName, Fact: execOut.sanFact}
	st.evidenceRefs = append(st.evidenceRefs, ref)
	st.sanEvidenceRefs = append(st.sanEvidenceRefs, sanRef)
	entry := cachedEvidenceEntry{success: true, ref: ref, sanRef: sanRef}
	st.bySig[sig] = entry
	return entry, nil
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
	incompleteReason string,
) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]evidenceDraft(nil), st.evidence...),
		append([]persistence.ReviewToolCallModel(nil), st.toolCalls...),
		append([]provider.NormalizedEvidenceRef(nil), st.evidenceRefs...),
		st.evidenceIncomplete,
		st.incompleteReason
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
