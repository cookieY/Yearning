package application

import (
	"errors"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"testing"
)

func TestPolicyCompilerRejectsChineseAdjacentRawName(t *testing.T) {
	catalog, binding := policyCompilerFixture(t)
	for _, text := range []string{"\u68c0\u67e5audit_table\u8868\u7684\u7ea6\u675f", "\u7981\u6b62\u5220\u9664unused_table\u8868", "actor\u5b57\u6bb5\u5fc5\u987b\u6b63\u786e"} {
		_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: text}}, nil, privacy.NewRunMapper(), binding, catalog, "", newMetadataScope())
		if !errors.Is(err, ErrPolicyPrivacy) {
			t.Fatal("Chinese prose retained raw catalog identifier")
		}
	}
}

func TestPolicyCompilerPreservesExactJSONReferenceIdentity(t *testing.T) {
	catalog := policyCatalogTestCatalog(t, database.PolicyCatalogIdentifier{Kind: database.PolicyCatalogTable, Schema: "app", Name: "audit&amp;actor"})
	_, err := compilePolicy([]agent.SkillInjection{{KnowledgeText: `Inspect {{ref:table:["app","audit&amp;actor"]}}.`}}, nil, privacy.NewRunMapper(), bindingOf("app"), catalog, "", newMetadataScope())
	if err != nil {
		t.Fatal("typed JSON identity was altered by HTML decoding")
	}
}
