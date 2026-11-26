package application

import (
	"context"
	"errors"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// policyCompileError wraps a specific privacy-safe coverage reason code while
// remaining matchable as both ErrPolicyPrivacy and *domain.Error (2007).
type policyCompileError struct {
	reason    string
	cause     error
	domainErr *domain.Error
}

func (e *policyCompileError) Error() string {
	if e == nil || e.reason == "" {
		return ErrPolicyPrivacy.Error()
	}
	return ErrPolicyPrivacy.Error() + " (" + e.reason + ")"
}

func (e *policyCompileError) Unwrap() []error {
	if e == nil {
		return []error{ErrPolicyPrivacy}
	}
	if e.cause != nil {
		return []error{ErrPolicyPrivacy, e.domainErr, e.cause}
	}
	return []error{ErrPolicyPrivacy, e.domainErr}
}

func newPolicyCompileError(reason string, cause error) error {
	if reason == "" {
		reason = "REVIEW_COVERAGE_INCOMPLETE"
	}
	return &policyCompileError{
		reason: reason,
		cause:  cause,
		domainErr: &domain.Error{
			Code: domain.CodeReviewCoverageIncomp,
			Name: reason,
		},
	}
}

func schemaBindingFailureReason(binding *stageSchemaBinding) string {
	if binding != nil && binding.unavailableReason != "" {
		return binding.unavailableReason
	}
	return "REVIEW_COVERAGE_INCOMPLETE"
}

func factSourceFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrReviewCredentialMissing):
		return "review_credential_missing"
	case errors.Is(err, ErrFactSourceIdentityChanged):
		return "datasource_identity_changed"
	default:
		return "datasource_unreachable"
	}
}

func policyCatalogReadFailureReason(err error) string {
	switch {
	case errors.Is(err, database.ErrMetadataVisibilityUnproven):
		return "catalog_permission_denied"
	case errors.Is(err, database.ErrPolicyCatalogSchemaUnavailable):
		return "catalog_schema_missing"
	case errors.Is(err, database.ErrPolicyCatalogTooLarge):
		return "catalog_too_large"
	case errors.Is(err, database.ErrPolicyCatalogUnavailable):
		return "catalog_read_failed"
	default:
		return "catalog_validation_failed"
	}
}

func coverageFailureOf(err error) *domain.Error {
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeReviewCoverageIncomp && de.Name != "" {
		return de
	}
	return domain.ErrCoverageIncomplete
}

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

func (r *AgentRunner) loadStagePolicyCatalog(
	ctx context.Context,
	snap *persistence.StageReviewSnapshotModel,
	target FrozenDatasourceTarget,
	runID string,
	binding *stageSchemaBinding,
	requireReader bool,
) (database.PolicyCatalog, bool, error) {
	schemas, err := policyCatalogAuthorizedSchemas(binding)
	if err != nil {
		return database.PolicyCatalog{}, false, newPolicyCompileError(schemaBindingFailureReason(binding), err)
	}
	catalogCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	source, closeSource, err := r.deps.FactSourceFor(catalogCtx, snap.DatasourceID, target, runID)
	if closeSource != nil {
		defer closeSource()
	}
	if err != nil || source == nil {
		return database.PolicyCatalog{}, false, newPolicyCompileError(factSourceFailureReason(err), err)
	}
	reader, ok := source.(policyCatalogSource)
	if !ok {
		if requireReader {
			return database.PolicyCatalog{}, false, newPolicyCompileError("catalog_read_failed", nil)
		}
		return database.PolicyCatalog{}, false, nil
	}
	catalog, err := reader.PolicyCatalog(catalogCtx, schemas)
	if err != nil {
		return database.PolicyCatalog{}, false, newPolicyCompileError(policyCatalogReadFailureReason(err), err)
	}
	return catalog, true, nil
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
		return compiledPolicy{}, database.PolicyCatalog{}, newPolicyCompileError("REVIEW_COVERAGE_INCOMPLETE", nil)
	}
	catalog, _, err := r.loadStagePolicyCatalog(ctx, snap, target, runID, binding, true)
	if err != nil {
		return compiledPolicy{}, database.PolicyCatalog{}, err
	}
	compiled, err := compilePolicy(skills, entries, mapper, binding, catalog, target.DatabaseName, scope)
	if err != nil {
		return compiledPolicy{}, database.PolicyCatalog{}, err
	}
	return compiled, catalog, nil
}

// verifySQLDerivedSchemas proves that SQL-derived schemas exist and are accessible via
// PolicyCatalog even when no policy skills or knowledge entries trigger compileStagePolicyWithCatalog.
func (r *AgentRunner) verifySQLDerivedSchemas(
	ctx context.Context,
	snap *persistence.StageReviewSnapshotModel,
	target FrozenDatasourceTarget,
	runID string,
	binding *stageSchemaBinding,
) error {
	if r == nil || r.deps.FactSourceFor == nil || snap == nil || binding == nil || !binding.hasSQLDerivedSchemas {
		return nil
	}
	_, _, err := r.loadStagePolicyCatalog(ctx, snap, target, runID, binding, false)
	return err
}
