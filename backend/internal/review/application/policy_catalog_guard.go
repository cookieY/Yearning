package application

import (
	"context"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// compileStagePolicy revalidates a complete trusted inventory before every
// Provider send and budget reservation. Frozen policy values remain immutable.
func (r *AgentRunner) compileStagePolicy(ctx context.Context,
	snap *persistence.StageReviewSnapshotModel, target FrozenDatasourceTarget,
	runID string, skills []agent.SkillInjection, entries []*knowledge.Entry,
	mapper *privacy.RunMapper, binding *stageSchemaBinding, scope metadataScope,
) (compiledPolicy, error) {
	compiled, _, err := r.compileStagePolicyWithCatalog(ctx, snap, target, runID, skills, entries, mapper, binding, scope)
	return compiled, err
}

// compileStagePolicyWithCatalog compiles stage policy and also returns the
// verified PolicyCatalog snapshot so multi-batch stage execution can revalidate
// slices in memory without reopening the target database per chunk.
func (r *AgentRunner) compileStagePolicyWithCatalog(ctx context.Context,
	snap *persistence.StageReviewSnapshotModel, target FrozenDatasourceTarget,
	runID string, skills []agent.SkillInjection, entries []*knowledge.Entry,
	mapper *privacy.RunMapper, binding *stageSchemaBinding, scope metadataScope,
) (compiledPolicy, database.PolicyCatalog, error) {
	if len(skills) == 0 && len(entries) == 0 {
		return compiledPolicy{scope: scope}, database.PolicyCatalog{}, nil
	}
	if r == nil || r.deps.FactSourceFor == nil || snap == nil || mapper == nil {
		return compiledPolicy{}, database.PolicyCatalog{}, ErrPolicyPrivacy
	}
	schemas, err := policyCatalogAuthorizedSchemas(binding)
	if err != nil {
		return compiledPolicy{}, database.PolicyCatalog{}, ErrPolicyPrivacy
	}
	catalogCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	source, closeSource, err := r.deps.FactSourceFor(catalogCtx, snap.DatasourceID, target, runID)
	if closeSource != nil {
		defer closeSource()
	}
	if err != nil || source == nil {
		return compiledPolicy{}, database.PolicyCatalog{}, ErrPolicyPrivacy
	}
	reader, ok := source.(policyCatalogSource)
	if !ok {
		return compiledPolicy{}, database.PolicyCatalog{}, ErrPolicyPrivacy
	}
	catalog, err := reader.PolicyCatalog(catalogCtx, schemas)
	if err != nil {
		return compiledPolicy{}, database.PolicyCatalog{}, ErrPolicyPrivacy
	}
	compiled, err := compilePolicy(skills, entries, mapper, binding, catalog, target.DatabaseName, scope)
	if err != nil {
		return compiledPolicy{}, database.PolicyCatalog{}, err
	}
	return compiled, catalog, nil
}
