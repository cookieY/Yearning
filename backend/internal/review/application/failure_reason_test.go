package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

type stubCatalogFactSource struct {
	tool.FactSource
	catalog database.PolicyCatalog
	err     error
}

func (s *stubCatalogFactSource) PolicyCatalog(context.Context, []string) (database.PolicyCatalog, error) {
	if s.err != nil {
		return database.PolicyCatalog{}, s.err
	}
	return s.catalog, nil
}

func TestCompileStagePolicyWithCatalogClassifiesFailureReasonsWithoutLeak(t *testing.T) {
	mapper := privacy.NewRunMapper()
	validSkills := []agent.SkillInjection{{
		ToolID:        "custom.rule",
		Name:          "custom.rule",
		KnowledgeText: "Keep transactions bounded.",
		FindingKey:    "custom.rule",
	}}
	snap := &persistence.StageReviewSnapshotModel{
		ID:            "snap-1",
		DatasourceID:  "ds-1",
		StagePosition: 1,
	}
	target := frozenDatasourceTarget{
		DatasourceID: "ds-1",
		DatabaseName: "secret_db",
	}

	okCatalog := database.PolicyCatalog{
		SchemaCount: 1,
		Identifiers: []database.PolicyCatalogIdentifier{
			{Kind: database.PolicyCatalogSchema, Schema: "secret_db", Name: "secret_db"},
			{Kind: database.PolicyCatalogTable, Schema: "secret_db", Name: "secret_orders"},
		},
	}

	cases := []struct {
		name       string
		binding    *stageSchemaBinding
		skills     []agent.SkillInjection
		sourceFn   func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error)
		wantReason string
	}{
		{
			name:       "datasource database required",
			binding:    &stageSchemaBinding{defensible: false, unavailableReason: "datasource_database_required"},
			skills:     validSkills,
			wantReason: "datasource_database_required",
		},
		{
			name:       "postgres search path unavailable",
			binding:    &stageSchemaBinding{defensible: false, unavailableReason: "postgres_search_path_unavailable"},
			skills:     validSkills,
			wantReason: "postgres_search_path_unavailable",
		},
		{
			name:    "review credential missing",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return nil, nil, fmt.Errorf("%w: %w", ErrFactSourceUnavailable, ErrReviewCredentialMissing)
			},
			wantReason: "review_credential_missing",
		},
		{
			name:    "datasource identity changed",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return nil, nil, ErrFactSourceIdentityChanged
			},
			wantReason: "datasource_identity_changed",
		},
		{
			name:    "datasource unreachable",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return nil, nil, errors.New("dial tcp 10.0.0.5:3306: i/o timeout")
			},
			wantReason: "datasource_unreachable",
		},
		{
			name:    "catalog permission denied",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{err: database.ErrMetadataVisibilityUnproven}, nil, nil
			},
			wantReason: "catalog_permission_denied",
		},
		{
			name:    "catalog schema missing",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{err: database.ErrPolicyCatalogSchemaUnavailable}, nil, nil
			},
			wantReason: "catalog_schema_missing",
		},
		{
			name:    "catalog too large",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{err: database.ErrPolicyCatalogTooLarge}, nil, nil
			},
			wantReason: "catalog_too_large",
		},
		{
			name:    "catalog read failed",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{err: database.ErrPolicyCatalogUnavailable}, nil, nil
			},
			wantReason: "catalog_read_failed",
		},
		{
			name:    "catalog validation failed",
			binding: bindingOf("secret_db"),
			skills:  validSkills,
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{catalog: database.PolicyCatalog{SchemaCount: 2}}, nil, nil
			},
			wantReason: "catalog_validation_failed",
		},
		{
			name:    "policy privacy conflict",
			binding: bindingOf("secret_db"),
			skills: []agent.SkillInjection{{
				ToolID:        "custom.leak",
				Name:          "custom.leak",
				KnowledgeText: "Always verify secret_orders before altering rows.",
				FindingKey:    "custom.leak",
			}},
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{catalog: okCatalog}, nil, nil
			},
			wantReason: "policy_privacy_conflict",
		},
		{
			name:    "policy parameter invalid",
			binding: bindingOf("secret_db"),
			skills: []agent.SkillInjection{{
				ToolID:        "custom.badparam",
				Name:          "custom.badparam",
				KnowledgeText: "Limit rows to {{missing_param}}.",
				FindingKey:    "custom.badparam",
			}},
			sourceFn: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
				return &stubCatalogFactSource{catalog: okCatalog}, nil, nil
			},
			wantReason: "policy_parameter_invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceFn := tc.sourceFn
			if sourceFn == nil {
				sourceFn = func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
					return &stubCatalogFactSource{catalog: okCatalog}, nil, nil
				}
			}
			runner := &AgentRunner{deps: AgentDeps{FactSourceFor: sourceFn}}
			_, _, err := runner.compileStagePolicyWithCatalog(
				context.Background(), snap, target, "run-1", tc.skills, nil, mapper, tc.binding, newMetadataScope(),
			)
			if !errors.Is(err, ErrPolicyPrivacy) {
				t.Fatalf("expected errors.Is(err, ErrPolicyPrivacy), got %v", err)
			}
			if gotCode := failureCodeOf(err); gotCode != domain.CodeReviewCoverageIncomp {
				t.Fatalf("failureCodeOf(err) = %d, want %d", gotCode, domain.CodeReviewCoverageIncomp)
			}
			if gotReason := failureNameOf(err); gotReason != tc.wantReason {
				t.Fatalf("failureNameOf(err) = %q, want %q", gotReason, tc.wantReason)
			}
			cov := coverageFailureOf(err)
			if cov.Code != domain.CodeReviewCoverageIncomp || cov.Name != tc.wantReason {
				t.Fatalf("coverageFailureOf(err) = %+v, want Code=2007 Name=%q", cov, tc.wantReason)
			}
			errText := err.Error()
			for _, secret := range []string{"secret_db", "secret_orders", "10.0.0.5"} {
				if strings.Contains(errText, secret) {
					t.Fatalf("error string leaked sensitive identifier %q: %s", secret, errText)
				}
			}
		})
	}
}

func TestResolvePostgresAndMySQLStageBindings(t *testing.T) {
	crypter := newSealTestCrypter(t)
	for _, tc := range []struct {
		name string
		cryp *crypto.Crypter
		snap *persistence.StageReviewSnapshotModel
	}{
		{name: "postgres missing snapshot", snap: nil},
		{name: "postgres nil crypter", snap: &persistence.StageReviewSnapshotModel{StagePosition: 1}},
		{name: "postgres invalid envelope", cryp: crypter, snap: &persistence.StageReviewSnapshotModel{StagePosition: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := resolvePostgresStageBinding(tc.cryp, "run-1", frozenFlowStageRow{
				Position: 1, CompatibilityMode: "postgresql",
			}, tc.snap)
			if binding == nil || binding.defensible || binding.postgres != nil || binding.unavailableReason != "postgres_search_path_unavailable" {
				t.Fatalf("unexpected binding: %+v", binding)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		db      *string
		wantOK  bool
		wantErr string
	}{
		{name: "mysql nil database", db: nil, wantErr: "datasource_database_required"},
		{name: "mysql empty database", db: ptrString(""), wantErr: "datasource_database_required"},
		{name: "mysql invalid identifier", db: ptrString("bad db!"), wantErr: "datasource_database_required"},
		{name: "mysql valid database", db: ptrString("app_db"), wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := resolveMySQLStageBinding(frozenFlowStageRow{
				Position: 1, CompatibilityMode: "mysql", DatabaseName: tc.db,
			}, "", nil, nil)
			if tc.wantOK {
				if !binding.defensible || !binding.schemas["app_db"] {
					t.Fatalf("expected defensible binding for app_db, got %+v", binding)
				}
				return
			}
			if binding.defensible || binding.unavailableReason != tc.wantErr {
				t.Fatalf("got %+v, want unavailableReason=%q", binding, tc.wantErr)
			}
		})
	}
}

func TestStageEvidenceStoreFinalSnapshotPropagatesIncompleteReason(t *testing.T) {
	store := &stageEvidenceStore{
		evidenceIncomplete: true,
		incompleteReason:   "review_credential_missing",
	}
	_, _, _, incomplete, reason := store.finalSnapshot()
	if !incomplete || reason != "review_credential_missing" {
		t.Fatalf("got incomplete=%v reason=%q, want true/review_credential_missing", incomplete, reason)
	}
}

func ptrString(s string) *string { return &s }
